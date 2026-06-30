# claude-code-router config

The router loads its provider list from a YAML file. Default path:

```
~/.claude-code-router/config.yaml
```

Override with `--config-path` or `CONFIG_PATH` env var.

## Schema

```yaml
router:
  default_provider: <provider-key>     # required; must match a key under providers:

trace: <bool>                         # optional; default false. When true, writes one JSON file per /v1/* request to ~/.claude-code-router/trace/ (deprecated — use POST /enabletrace for bounded trace windows; see ## Trace)

providers:
  <provider-key>:
    upstream: <URL>                    # required; e.g. https://api.anthropic.com
    token: <string>                    # optional; if absent, client's Authorization header passes through
    models:                            # filepath.Match glob patterns
      - "<pattern>"
      - ...
```

## Routing

On every `/v1/*` request, the router:

1. Reads the JSON body, extracts the top-level `model` field
2. Walks the providers' `models:` lists in declaration order
3. First glob that matches → forwards to that provider's upstream
4. No match (or non-JSON body / no model field) → falls back to `default_provider`

Glob syntax is Go's `filepath.Match` — `*` matches any run of characters, `?` matches one, `[abc]` is a character class. Patterns with literal `[` need to use `*` (e.g. `deepseek-v4-flash*` not `deepseek-v4-flash[1m]`).

## Aliases

The optional top-level `aliases:` block maps short operator-typed model names to the full model identifier the upstream expects. Aliases are resolved router-side BEFORE provider routing — the upstream always sees the full model name.

```yaml
aliases:
  qwen: qwen3.6:35b-a3b-coding-nvfp4
  minimax: MiniMax-M3-highspeed
  deepseek: deepseek-v4-flash-2025-12-01
  opus: claude-opus-4-7
```

Then in any Claude Code session:

```
> /model qwen      # router sees "qwen", rewrites body .model to "qwen3.6:35b-a3b-coding-nvfp4", routes via qwen* glob to ollama-local
```

### Semantics

- **Single-hop.** If `aliases: {a: b, b: c}` and the request uses `model: a`, the upstream receives `model: b` — NOT `c`. The router resolves once and forwards.
- **Case-sensitive.** `aliases["Qwen"]` and `aliases["qwen"]` are distinct entries (same byte-exact match as provider glob keys).
- **Optional.** Configs without an `aliases:` block route exactly as before. Backward-compatible.
- **Log line.** On a hit, the router logs `[alias] qwen -> qwen3.6:35b-a3b-coding-nvfp4` at glog `V(1)` — visible in `/tmp/claude-code-router.log` when the router runs with `-v=1` or higher.

### Validation

| Condition | Behavior |
|---|---|
| Alias key equals a provider name (e.g. `aliases: { minimax: ... }` AND `providers: { minimax: ... }`) | **Error** at `config.Load` — daemon refuses to start. Operator must rename the alias key or the provider. |
| Alias target matches no provider's `models:` glob (e.g. `aliases: { foo: typo-name }` where no provider lists `typo-name*`) | **Warning** at startup via glog (`[config] alias target "typo-name" (from alias key "foo") matches no provider glob`); config still loads. At runtime, requests using that alias get rewritten to the typo string and fall through to `default_provider`, which likely returns 404. Operator notices the warning at startup. |

## Auth

| `token:` field | Behavior |
|---|---|
| absent / empty | Forward the client's `Authorization` header verbatim — used for Anthropic subscription (Claude Code's OAuth bearer passes through untouched) |
| set | Replace the outbound `Authorization` with `Bearer <token>` — used for fixed-token providers (MiniMax, Ollama, vLLM) |

The router never stores or logs token values; trace files inherit the same invariant — see ## Trace.

## Trace

The `trace:` flag is a top-level boolean. When `true`, every `/v1/*` request produces exactly one JSON file at `~/.claude-code-router/trace/<timestamp>-<request-id>.json` containing the complete request (method, path, headers, body) and complete response (status, headers, body).

When `false` (or absent), no trace files are written and no trace middleware is on the request hot path.

The `Authorization` and `x-api-key` request headers are redacted to `***` in every trace file, regardless of header case. All other headers and the entire request/response bodies are logged verbatim — operator's data, operator's disk.

The flag is read once at config load; changing it requires a router restart (see ## Reload).

No retention, rotation, or cleanup is provided — the operator runs `rm` manually.

### Runtime toggle via `/enabletrace` and `/disabletrace`

Two operator-local HTTP endpoints toggle trace logging at runtime without a router restart:

- `POST /enabletrace` — turns tracing on for a bounded 5-minute window that auto-disables on expiry. Repeated calls reset the window (each cancels the prior timer and starts a fresh 5-minute window).
- `POST /disabletrace` — turns tracing off immediately and cancels the pending TTL timer.

Both endpoints are registered on the operator-local listener (`127.0.0.1:8788`) with no authentication — the same trust model as `/setloglevel`. Example:

```bash
curl -X POST http://127.0.0.1:8788/enabletrace   # trace enabled
curl -X POST http://127.0.0.1:8788/disabletrace  # trace disabled
```

The 5-minute window auto-disables on expiry — if the operator forgets `/disabletrace`, tracing stops on its own. The 5-minute TTL is a hardcoded constant (not configurable via config file, query param, or request body).

The toggle is process-internal state only — it does not persist across router restarts, and it does not depend on config reload or SIGHUP.

### Deprecation of `trace:` config flag

The legacy `trace: true` config flag still works as an always-on opt-in (trace middleware is mounted when `IsEnabled() || cfg.Trace` is true — flag-OR-config). However, leaving `trace: true` on indefinitely is a disk and privacy hazard (full request/response bodies written to disk for every request). Use the bounded `/enabletrace` toggle instead for capturing a single problematic exchange.

## Example — all four providers

```yaml
router:
  default_provider: anthropic-subscription

providers:

  anthropic-subscription:
    upstream: https://api.anthropic.com
    # no token: → forward client's Authorization (subscription OAuth)
    models:
      - "claude-opus-*"
      - "claude-sonnet-*"
      - "claude-haiku-*"
      - "opus"
      - "sonnet"
      - "haiku"

  minimax:
    upstream: https://api.minimax.io/anthropic
    token: "<your MiniMax API key>"
    models:
      - "MiniMax-*"

  ollama-local:
    upstream: http://localhost:11434
    token: "ollama"                   # Ollama's literal-string convention
    models:
      - "qwen*"

aliases:
  qwen: qwen3.6:35b-a3b-coding-nvfp4
  minimax: MiniMax-M3-highspeed
  deepseek: deepseek-v4-flash-2025-12-01
  opus: claude-opus-4-7
```

`chmod 600 ~/.claude-code-router/config.yaml` since it holds API tokens.

## Switching mid-session

The router decides per-request. To switch backends inside a Claude Code session, just use `/model <name>`:

```
> /model qwen                   # alias → next request rewritten to qwen3.6:35b-a3b-coding-nvfp4, routed to ollama-local
> /model minimax                # alias → next request rewritten to MiniMax-M3-highspeed, routed to minimax
> /model claude-opus-4-7        # no alias match, glob routes to anthropic-subscription
```

No router restart, no Claude Code restart. The session stays alive across switches.

## Reload

Edit the config file and send SIGHUP to the running router to pick up the change without restarting the process or dropping in-flight requests:

```bash
kill -HUP $(pgrep claude-code-router)
```

On success the router logs one line at `config reloaded old_providers=N new_providers=M` and serves new requests from the updated config. Requests already in flight finish against the config they started under. An invalid config (missing file, invalid YAML, validation failure) is rejected: the old config stays active and the router logs `config reload failed: ...` at WARNING.

A full process restart is still needed to change the `--listen` address or TLS material — those are not hot-reloadable.

`launchctl kickstart -k` / `systemctl --user restart` still work for a hard restart (binary upgrade, listener-address change), but are no longer required for config edits.

## Related

- [README.md](../README.md) — install, `clauder` shell function
- [docs/launchd-service.md](launchd-service.md) — macOS service setup
- [docs/systemd-user-service.md](systemd-user-service.md) — Linux service setup

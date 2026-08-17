# claude-code-router config

The router loads its provider list from a YAML file. Default path (XDG):

```
~/.config/claude-code-router/config.yaml
```

Falls back to the legacy `~/.claude-code-router/config.yaml` if the XDG directory doesn't exist yet but the legacy one does.

Override with `--config-path` or `CONFIG_PATH` env var — an explicit value always wins over both defaults.

## Schema

```yaml
router:
  default_provider: <provider-key>     # required; must match a key under providers:

auth:
  key: <string>                        # optional; shared key gating non-loopback /v1/* requests (see ## Inbound auth). Absent or empty disables the check.

trace: <bool>                         # optional; default false. When true, writes one JSON file per /v1/* request to ~/.claude-code-router/trace/ (deprecated — use POST /enabletrace for bounded trace windows; see ## Trace)

providers:
  <provider-key>:
    upstream: <URL>                    # required; e.g. https://api.anthropic.com
    token: <string>                    # optional; if absent, client's Authorization header passes through
    models:                            # filepath.Match glob patterns
      - "<pattern>"
      - ...
    requiresLeadingSystem:             # optional; glob patterns for models whose chat template rejects a non-leading system message (see ## Requires leading system)
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

## Requires leading system

Claude Code puts a `system`-role entry inside the message list, after the first user turn, in addition to the dedicated top-level `system` block. Some models' chat templates reject any system entry that is not first and answer `HTTP 500 {"type":"error","error":{"type":"api_error","message":"system message must be at the beginning"}}` before inference starts.

Example:

```yaml
providers:
  ollama-local:
    upstream: http://localhost:11434
    token: ollama
    models:
      - "qwen*"
    requiresLeadingSystem:
      - "qwen3.8*"
```

### Semantics

- **Glob syntax.** Same `filepath.Match` syntax as `models:` — `*`, `?`, `[abc]`. Matched against the FULLY RESOLVED model name, i.e. after alias resolution and after the `[1m]` suffix is stripped.
- **Opt-in, default off.** Absent, `null`, and an empty list are equivalent and all mean: never transform anything for this provider. A config that does not mention the field routes byte-for-byte as before.
- **What the transform does.** Every `system`-role entry that is not at index 0 of `messages` is removed from the list and its content appended to the top-level `system` block, in the order the entries appeared. Content given as a plain string becomes one `{"type":"text","text":"..."}` block; content already given as a block list is appended block for block. The surviving messages keep their relative order.
- **What it does not do.** A `system` entry already at index 0 stays there and is NOT copied into the top-level block, even when a top-level block also exists — the upstream then receives system content in both places, which is the shape Claude Code already sends today and which upstreams accept. No merging, deduplication, summarisation, or reordering beyond moving entries.
- **Untransformed paths.** A request whose model does not match, or that has no misplaced system entry, is forwarded as the exact bytes received. Same for a body the transform cannot interpret (`messages` not a list, an entry that is not an object, a system `content` that is neither a string nor a block list): the request is forwarded unchanged with one `WARNING` line and the client gets the upstream's own status — the transform never turns a request into a router-generated error.
- **Scope is the matched route.** The patterns come from the provider whose `models:` glob matched. A request that matches no provider glob and falls through to `default_provider` is never transformed — if you need the transform for such a model, give that provider a `models:` glob that matches it.
- **Log line.** On a fire, the router logs `[system-lift] model=qwen3.8:27b-mlx moved=2` at glog `V(2)` — the same verbosity as the `[alias]` and `[1m-strip]` detail lines, so it is invisible at the always-on `V(1)` default. Raise the level with `curl http://127.0.0.1:8788/setloglevel/2` (auto-reverts after 5 minutes) before grepping `/tmp/claude-code-router.log` for it. The line carries the model name and a count only — never system-message content.

### Why per model and not per provider

ollama's system-position restriction is a property of each MODEL's chat template, not of the ollama server, so two models behind the same provider legitimately disagree. Verified 2026-08-15 with identical curl payloads against the same ollama instance: `qwen3.6:35b-a3b-coding-nvfp4` → 200, `qwen3.8:27b-mlx` → 500, `qwen3.8:27b-mtp-q4_K_M` → 500 — all three match the `ollama-local` provider's `qwen*` glob. A provider-wide boolean switch would therefore silently rewrite prompts for models that never needed it, which is why there is no such switch and no global toggle, environment variable, or CLI flag.

### Validation

| Condition | Behavior |
|---|---|
| Malformed glob pattern (e.g. `[`) | Config load fails with `provider "<name>": invalid requiresLeadingSystem glob "["`; router refuses to start |
| Well-formed pattern that matches no model the operator actually uses | Not an error; no warning. The symptom is that no `[system-lift]` line ever appears while the upstream keeps returning 500, and the fix is to widen the pattern. |

## Auth

| `token:` field | Behavior |
|---|---|
| absent / empty | Forward the client's `Authorization` header verbatim — used for Anthropic subscription (Claude Code's OAuth bearer passes through untouched) |
| set | Replace the outbound `Authorization` with `Bearer <token>` — used for fixed-token providers (MiniMax, Ollama, vLLM) |

The router never stores or logs token values; trace files inherit the same invariant — see ## Trace.

## Inbound auth

The optional top-level `auth:` block configures a shared key that gates the `/v1/*` inference path for non-loopback callers.

```yaml
# auth:                        # omit (or set key: "") to disable inbound auth on /v1/*; required when the listener binds beyond 127.0.0.1
#   key: "<YOUR_ROUTER_KEY>"
```

- **Disabled by default.** Absent, `null`, and an empty `key` are equivalent and all mean the router behaves exactly as it does today — no caller is challenged. This is the only mode for existing single-user localhost setups, so upgrading is not a behavior change.
- **Non-empty key ⇒ the check is on.** With `auth.key` set, every non-loopback `/v1/*` request must present the matching key in the `x-router-key` header; a missing or wrong key is refused with `401 Unauthorized` (constant-time comparison via `crypto/subtle`; neither the presented nor the configured key is ever logged). Loopback requests are exempt, so the operator's own Claude Code on the host keeps working keyless.
- **`ROUTER_AUTH_KEY` wins over the config.** The router resolves the gate key env-first at startup: if the `ROUTER_AUTH_KEY` environment variable is non-empty, it is used regardless of the config's `auth:` block — so `auth.key` may be either a literal key or simply a non-empty marker, and a config with no `auth:` block at all still enables auth when the env var is set. Only when `ROUTER_AUTH_KEY` is empty does `auth.key` apply, so existing literal-key configs keep working unchanged. The env var is the raw secret's home: a launchd wrapper fetches it from TeamVault and injects it at startup, keeping the secret out of the config file entirely.
- **The key never reaches an upstream.** A presented `x-router-key` is stripped from a cloned request before it is forwarded, so a key carried via `ANTHROPIC_CUSTOM_HEADERS` never leaks to a provider.
- **SIGHUP applies changes.** `auth.key` lives in the same config that hot-reloads on SIGHUP — edit the file, `kill -HUP $(pgrep claude-code-router)`, and the new key (or its removal) is live without a restart. (`ROUTER_AUTH_KEY` is read once at startup; changing it requires a restart.)
- **Caller side.** A remote caller (e.g. a dark-factory YOLO container) presents the key by having Claude Code send it: set `ANTHROPIC_CUSTOM_HEADERS` to carry `x-router-key: <value>` alongside `ANTHROPIC_BASE_URL`. Without this, an operator can enable auth but has no way to configure a caller.
- **Sensitive.** The value is a shared secret, like the provider `token:` fields. Keep the config at `chmod 600`; the router never logs the key. Prefer keeping the raw secret in TeamVault and letting the launchd wrapper inject it via `ROUTER_AUTH_KEY` — never commit a literal key to the config file.

## Trace

The `trace:` flag is a top-level boolean. When `true`, every `/v1/*` request produces exactly one JSON file at `~/.claude-code-router/trace/<timestamp>-<request-id>.json` containing the complete request (method, path, headers, body) and complete response (status, headers, body).

When `false` (or absent), no trace files are written and no trace middleware is on the request hot path.

The `Authorization`, `x-api-key`, and `x-router-key` request headers are redacted to `***` in every trace file, regardless of header case. All other headers and the entire request/response bodies are logged verbatim — operator's data, operator's disk.

The flag is read once at config load; changing it requires a router restart (see ## Reload).

No retention, rotation, or cleanup is provided — the operator runs `rm` manually.

### Runtime toggle via `/enabletrace` and `/disabletrace`

Two operator-local HTTP endpoints toggle trace logging at runtime without a router restart:

- `POST /enabletrace` — turns tracing on for a bounded 5-minute window that auto-disables on expiry. Repeated calls reset the window (each cancels the prior timer and starts a fresh 5-minute window).
- `POST /disabletrace` — turns tracing off immediately and cancels the pending TTL timer.

Both endpoints are registered on the operator-local listener (`127.0.0.1:8788`) and, like `/setloglevel` and `/gc`, are guarded by an unconditional loopback-only check: a non-loopback request is refused with `403 Forbidden` before handler logic runs. The guard is always on (no config knob to disable it) and is the protection once the listener binds beyond `127.0.0.1` — a remote caller can never toggle tracing (body capture), force GC, or change log levels. Example:

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

# Optional: require non-loopback /v1/* callers to present this key.
# Omit or leave empty to disable inbound auth.
auth:
  key: "<YOUR_ROUTER_KEY>"

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
    requiresLeadingSystem:            # qwen3.8's chat template rejects a non-leading system message
      - "qwen3.8*"

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

`launchctl kickstart -k` still works for a hard restart after a binary upgrade, and `systemctl --user restart` re-reads the unit file on Linux; neither is required for config edits. On macOS, a `--listen` change is NOT picked up by `kickstart -k` (it restarts with the cached args) — edit the plist, then `launchctl bootout gui/$(id -u)/de.bborbe.claude-code-router` followed by `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/de.bborbe.claude-code-router.plist`.

## Related

- [README.md](../README.md) — install, `clauder` shell function
- [docs/launchd-service.md](launchd-service.md) — macOS service setup
- [docs/systemd-user-service.md](systemd-user-service.md) — Linux service setup

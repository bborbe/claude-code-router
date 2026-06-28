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

The router never stores or logs token values.

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
    token: "<paste from teamvault MOPmQL>"
    models:
      - "MiniMax-*"

  seibert-vllm:
    upstream: https://vllm.seibert.tools
    token: "<paste from teamvault-sm 0DaxOm>"
    models:
      - "deepseek-*"

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

Config changes require a router restart (no hot-reload in v1):

```bash
# macOS launchd
launchctl kickstart -k gui/$(id -u)/de.bborbe.claude-code-router

# Linux systemd-user
systemctl --user restart claude-code-router.service

# Local foreground (development)
# Ctrl-C, then `make run` again
```

## Related

- [README.md](../README.md) — install, `clauder` shell function
- [docs/launchd-service.md](launchd-service.md) — macOS service setup
- [docs/systemd-user-service.md](systemd-user-service.md) — Linux service setup

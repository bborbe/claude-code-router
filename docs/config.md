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
```

`chmod 600 ~/.claude-code-router/config.yaml` since it holds API tokens.

## Switching mid-session

The router decides per-request. To switch backends inside a Claude Code session, just use `/model <name>`:

```
> /model MiniMax-M3-highspeed   # next request → minimax
> /model claude-opus-4-7        # next request → anthropic-subscription
> /model qwen3.6:35b            # next request → ollama-local
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

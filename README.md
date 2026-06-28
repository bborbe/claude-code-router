# claude-code-router

[![CI](https://github.com/bborbe/claude-code-router/actions/workflows/ci.yml/badge.svg)](https://github.com/bborbe/claude-code-router/actions/workflows/ci.yml)

Local HTTP router for Claude Code. Forwards `/v1/*` requests to one of several LLM providers (Anthropic subscription, MiniMax, local Ollama, company vLLM/DeepSeek, …) based on the request's `model` field, using a declarative YAML config. Switch backends mid-session with `/model <name>` — no router or session restart.

## Install

1. Install the binary onto your `PATH`:

   ```bash
   make install
   ```

   Drops `claude-code-router` into `$(go env GOPATH)/bin/`.

2. Create the config at `~/.claude-code-router/config.yaml`:

   ```bash
   mkdir -p ~/.claude-code-router
   cp docs/config.example.yaml ~/.claude-code-router/config.yaml
   chmod 600 ~/.claude-code-router/config.yaml
   # then edit the file, paste real provider tokens
   ```

   Schema reference: [docs/config.md](docs/config.md).

3. Run it continuously in the background — pick your platform:

   - macOS: [docs/launchd-service.md](docs/launchd-service.md)
   - Linux: [docs/systemd-user-service.md](docs/systemd-user-service.md)

4. Add the `clauder` shell function (below) so your Claude Code sessions can route through it.

## `clauder` shell function

`clauder` is a one-line wrapper that points Claude Code at the local router for a single invocation. The choice stays per-session: `claude` still talks directly to Anthropic, `clauder` goes through the router.

Append to `~/.zshrc` (or `~/.bashrc` on Linux):

```bash
# Routes Claude Code through claude-code-router on 127.0.0.1:8788.
# IMPORTANT: do NOT set ANTHROPIC_API_KEY — that overrides the subscription
# OAuth bearer and breaks auth. clauder sets only ANTHROPIC_BASE_URL.
clauder() {
  ANTHROPIC_BASE_URL="http://127.0.0.1:8788" claude "$@"
}
```

Reload the shell (`source ~/.zshrc`) and use `clauder` whenever you want the router in the loop:

```bash
clauder                 # interactive Claude Code session via the router
clauder --resume        # resume previous session via the router
clauder -p "summarize"  # one-shot prompt via the router
claude                  # direct to Anthropic, router not involved
```

## Switching providers mid-session

The router decides per-request. Inside any `clauder` Claude Code session:

```
> /model claude-opus-4-8        # next request → anthropic-subscription
> /model MiniMax-M3-highspeed   # next request → minimax
> /model deepseek-v4-flash      # next request → seibert-vllm
> /model qwen3.6:35b            # next request → ollama-local
```

The matching is glob-based (`claude-*`, `MiniMax-*`, etc.) — patterns are declared per provider in the YAML. Unmatched model names fall through to `default_provider`.

## Integrations

- [Dark-factory ↔ claude-code-router](docs/dark-factory-integration.md) — route YOLO container prompts through the host router for unified token storage + observability

## Develop

```bash
make precommit   # gofmt, lint, vet, test, vulncheck, gosec, osv-scanner, trivy
make run         # run locally on 127.0.0.1:8788 with -logtostderr -v=2
```

## License

BSD-2. See [LICENSE](LICENSE).

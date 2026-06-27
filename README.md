# claude-code-router

[![CI](https://github.com/bborbe/claude-code-router/actions/workflows/ci.yml/badge.svg)](https://github.com/bborbe/claude-code-router/actions/workflows/ci.yml)

Local HTTP router for Claude Code that forwards `/v1/messages` requests to one of several LLM providers (Anthropic subscription, MiniMax, local Ollama, company vLLM/DeepSeek) based on the request's `model` field — using a declarative config at `~/.dark-factory/config.yaml`.

**v1 skeleton state** (this commit): minimal HTTP listener on `127.0.0.1:8788` with a `/healthz` endpoint. Provider routing logic + config loading land in subsequent commits.

## Install

1. Install the binary onto your `PATH`:

   ```bash
   make install
   ```

   Drops `claude-code-router` into `$(go env GOPATH)/bin/`.

2. Run it continuously in the background — pick your platform:

   - macOS: [docs/launchd-service.md](docs/launchd-service.md)
   - Linux: [docs/systemd-user-service.md](docs/systemd-user-service.md)

3. Add the `clauder` shell function (below) so your Claude Code sessions can route through it.

## `clauder` shell function

`clauder` is a one-line wrapper that points Claude Code at the local router for a single invocation. The choice stays per-session: `claude` still talks directly to Anthropic, `clauder` goes through the router.

Append to `~/.zshrc` (or `~/.bashrc` on Linux):

```bash
# Routes Claude Code through claude-code-router on 127.0.0.1:8788.
# WICHTIG: do NOT set ANTHROPIC_API_KEY — that overrides the subscription
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

## Develop

```bash
make precommit   # gofmt, lint, vet, test, vulncheck
make run         # run locally on 127.0.0.1:8788
```

## License

BSD-2. See [LICENSE](LICENSE).

# claude-code-router

[![CI](https://github.com/bborbe/claude-code-router/actions/workflows/ci.yml/badge.svg)](https://github.com/bborbe/claude-code-router/actions/workflows/ci.yml)

Local HTTP router for Claude Code that forwards `/v1/messages` requests to one of several LLM providers (Anthropic subscription, MiniMax, local Ollama, company vLLM/DeepSeek) based on the request's `model` field — using a declarative config at `~/.dark-factory/config.yaml`.

**v1 skeleton state** (this commit): minimal HTTP listener on `127.0.0.1:8788` with a `/healthz` endpoint. Provider routing logic + config loading land in subsequent commits.

## Install

```bash
make install   # builds binary, installs launchd plist, starts service
```

## Develop

```bash
make precommit   # gofmt, lint, vet, test
make run         # run locally on 127.0.0.1:8788
```

## License

BSD-2. See [LICENSE](LICENSE).

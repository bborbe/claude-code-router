# Changelog

All notable changes to this project will be documented in this file.

Please choose versions by [Semantic Versioning](http://semver.org/).

## Unreleased

- `make run` now sets `-listen=127.0.0.1:8788 -logtostderr -v=2` so router activity is visible on stderr during local testing (previously ran with defaults and no log output).

## v0.1.1

- Add `docs/launchd-service.md` and `docs/systemd-user-service.md` — copy-paste install for macOS launchd and Linux systemd-user (mirrors the semantic-search docs pattern; no install.sh script to maintain).
- README rewritten with a 3-step Install (binary → service → `clauder`) and a dedicated `clauder` shell-function section explaining why it sets only `ANTHROPIC_BASE_URL` (subscription OAuth bearer would break under `ANTHROPIC_API_KEY`).

## v0.1.0

- Initial scaffold copied from `go-skeleton`, stripped to a local CLI tool shape (no k8s, no Kafka, no BoltKV, no Sentry, no Prometheus).
- Minimal `main.go` binds an HTTP listener on `127.0.0.1:8788` (configurable via `--listen`).
- `pkg/handler/healthz.go` returns `200 OK` on `/healthz`.
- `pkg/factory/factory.go` wires the router via `CreateRouter()`.
- BSD-2 license, GitHub Actions CI inherited from skeleton.

# Changelog

All notable changes to this project will be documented in this file.

Please choose versions by [Semantic Versioning](http://semver.org/).

## v0.5.0

- feat: Add `aliases:` YAML block to router config for mapping short model names to full model strings, with collision validation and orphan-target warning
- feat: ModelRouter consults `aliases` map and rewrites the request body's `.model` field on a hit before glob-routing; upstream sees the resolved full model name; emits `[alias] <short> -> <resolved>` at glog V(1). Nil/empty aliases map is a no-op (backward compatible). Body rewrite preserves all other top-level fields. Returns 500 if JSON re-marshal fails mid-flight.
- README refreshed for v0.4.0 reality: drop "v1 skeleton state" language, correct config path `~/.dark-factory/...` → `~/.claude-code-router/...`, add Install step 2 (create config from example), add "Switching providers mid-session" section showing `/model` usage. Replaced lone "WICHTIG" with "IMPORTANT" (English consistency).
- launchd + systemd service docs now include the `-config-path` flag in `ProgramArguments` / `ExecStart` — without it the binary loads the default path (`~/.claude-code-router/config.yaml`), which is fine, but explicit-in-the-doc avoids the "where do I plug the config?" question.
- Service docs note the config file must exist before `launchctl bootstrap` / `systemctl --user enable --now` — agent crash-loops if config is missing.
- **Model aliases.** New optional `aliases:` block in `~/.claude-code-router/config.yaml` maps short names to full model identifiers (e.g. `qwen: qwen3.6:35b-a3b-coding-nvfp4`). Operator types `/model qwen`; the router rewrites the request body's `.model` field to the full name single-hop, before provider routing — the upstream always sees the full name. Validation: hard error on alias-key colliding with a provider name; glog warning when an alias target matches no provider glob. Configs without `aliases:` continue to load unchanged. See [docs/config.md#aliases](docs/config.md#aliases).

## v0.4.0

- **Multi-provider routing via YAML config.** Router now loads `~/.claude-code-router/config.yaml` (override with `--config-path`) and dispatches `/v1/*` requests by the body's `model` field. Each provider declares its upstream URL, optional `token:` (replaces Authorization with `Bearer <token>`; absent = forward client's OAuth bearer untouched), and a list of `filepath.Match` glob patterns. Unmatched models fall through to `router.default_provider`.
- New packages: `pkg/config` (YAML loader + validation), `pkg/handler/NewModelRouter` (body-parses `model` field, glob-matches, dispatches), `pkg/handler/NewAuthSwapTransport` (per-request Authorization swap, request cloned so caller's headers aren't mutated).
- `pkg/factory.CreateRouterFromConfig` wires per-provider proxies + the model-router; `factory.CreateServer` signature changed to `(listen, configPath)` and now returns `(run.Func, error)` to surface config-load failures.
- Sample config at `docs/config.example.yaml`; full schema reference in `docs/config.md`.
- Mid-session switching: `/model <name>` in Claude Code is all that's needed — no router restart.

## v0.3.0

- Mount Anthropic reverse proxy on `/v1/` — every Claude Code request (`/v1/messages`, `/v1/models`, etc.) now forwards verbatim to `https://api.anthropic.com`. The Authorization header (subscription OAuth bearer) passes through untouched; upstream errors surface as `502 Bad Gateway` with the error message. Task 3 will add model-name routing to other providers.
- Add `pkg/handler/NewAnthropicProxyHandler` (wraps `libhttp.NewProxy`) with 3 Ginkgo specs: POST forward + body preservation, Authorization header pass-through, 502 on upstream transport failure.

## v0.2.0

- Add `pkg/handler/NewLoggingHandler` middleware; wrap the router in it so every request emits `[req] METHOD path -> STATUS` at glog `V(1)`. Makes router activity visible during local testing (essential for diagnosing whether Claude Code reached the router when `/v1/messages` is still 404 in the skeleton state).

## v0.1.2

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

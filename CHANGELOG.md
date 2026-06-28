# Changelog

All notable changes to this project will be documented in this file.

Please choose versions by [Semantic Versioning](http://semver.org/).

## Unreleased

- **fix: raise `DefaultProxyTransport.ResponseHeaderTimeout` from 60s to 5min.** Long-generation requests (e.g. `/compact` on a large session, big code-gen prompts) regularly need 60-300s before Anthropic sends the first byte of response headers. The old 60s cap produced `net/http: timeout awaiting response headers` 502s mid-flight, which in claude-code manifested as `/compact` appearing to hang at 95% — claude-code's SDK silently retried after each 502, so what looked like a stuck 7-minute `/compact` was actually multiple stuck 60s rounds plus one successful round. Bump to 5 minutes covers the worst observed case while still bounding a genuinely-wedged connection.
- **fix: raise HTTP server `WriteTimeout` to 10min and `ReadTimeout` to 5min (were 30s each).** libhttp.NewServer's defaults capped each leg of the streaming chain — `WriteTimeout=30s` killed any SSE response that streamed for more than 30 seconds (router → claude, common on `/compact` body streaming); `ReadTimeout=30s` killed any request body that took more than 30 seconds to upload (claude → router, relevant for `/compact`'s large session context). Both are wrong defaults for an LLM-proxy use case. Kept finite (not 0) so genuinely wedged Anthropic outages surface as clean server-side timeouts the operator can investigate instead of piling up goroutines forever as claude-code's SDK retries. Worst-observed `/compact` body stream was ~1min, so 10min WriteTimeout is generous 10x headroom; ReadTimeout 5min similarly. `ReadHeaderTimeout=10s` and `IdleTimeout=60s` stay at defaults.
- **debug: V(4) `[upstream]` log line per upstream RoundTrip.** New `NewLoggingRoundTripper` (in `pkg/handler/logging-roundtripper.go`) wraps the auth-swap transport; logs `[upstream] METHOD host/path ttfb=X status=N` at glog V(4). Silent at default verbosity; bump via `curl http://127.0.0.1:8788/setloglevel/4` for one debug session (auto-reverts after 5min). Useful for distinguishing "Anthropic slow to send first byte" (high TTFB) from "Anthropic slow to stream body" (low TTFB, high `[req]` latency).
- **docs: inline the full sample config in `README.md`** so new operators see the YAML shape at a glance instead of clicking through to `docs/config.example.yaml`. Adds the canonical 3-badge set (Go Reference, Go Report Card, DeepWiki) per `readme-guide.md` — was CI-only before.
- **docs: scrub internal-org references** from public docs. Removes the `seibert-vllm` provider example, teamvault token-paste hints, and the `→ seibert-vllm` example comment from `README.md`, `docs/config.example.yaml`, `docs/config.md`, and `docs/dark-factory-integration.md`. Replaced with generic `<YOUR_MINIMAX_API_KEY>` / `<your MiniMax API key>` placeholders. Public-repo hygiene — the docs should be useful to anyone setting up a router, not gated on internal credential-store access.

## v0.9.0

- **feat: Prometheus `/metrics` endpoint.** Replace the `# metrics not enabled in v1 skeleton` stub with `promhttp.Handler()` against the default Prometheus registry (matches go-skeleton convention — also exposes `go_gc_*`, `go_memstats_*`, `process_*` runtime series for spotting GC pressure / memory growth on the long-running router daemon). Three `ccrouter_*` application series: `ccrouter_requests_total{provider,model,status_class}` counter, `ccrouter_request_duration_seconds_bucket{provider,model}` histogram (LLM-shaped buckets 100ms…60s), `ccrouter_alias_resolutions_total{alias,resolved}` counter. Cardinality ~1k application series total at 5 providers × 15 models. Metrics emit unconditionally per request (NOT sampled — log sampling stays at the V(1) `[req]` line). Operator scrape config + Grafana queries in `docs/metrics.md`. Closes the open backlog item under [[Multi-Provider Claude Code Proxy]].
- **breaking: `handler.NewModelRouter` signature gains a `*handler.Metrics` parameter** (last positional). Same pattern as PR #6 `defaultProviderName` and PR #8 `sampler` adds. `factory.CreateRouterFromConfig` already threads it; no YAML config-format change.
- **fix: SSE flush passthrough in `statusRecorder`.** Add `Unwrap() http.ResponseWriter` so `http.NewResponseController` (Go 1.20+) can reach the underlying writer's `Flush`/`Hijack`/`SetReadDeadline`/`SetWriteDeadline` through the wrapper. Without it, SSE chunks from Anthropic piled up in an intermediate buffer instead of flushing per chunk — symptom was Claude Code spinners stuck mid-stream and `/compact` appearing to hang at 95% (bytes did arrive, just all at once when the response closed). Regression introduced when `statusRecorder` was extracted into the model-router (PR #6); affected every SSE response since v0.6.0.
- **fix: pre-initialize `ccrouter_alias_resolutions_total{alias, resolved}` for each configured alias.** Per [go-prometheus-metrics-guide.md#counter-pre-initialization](https://github.com/bborbe/coding/blob/master/docs/go-prometheus-metrics-guide.md): without `.Add(0)` at startup, `rate(ccrouter_alias_resolutions_total[5m]) > X` alert expressions return no-data (not zero) until the alias is first hit, so alerts can't fire on a system that's broken but hasn't yet routed a single aliased request. Request counter labels include unbounded `model` so pre-init doesn't apply there.

## v0.8.0

- **refactor: flatten `pkg/cli` + `pkg/config` into `pkg/`.** Aligns with [[Go Package Layout Guide]] — default is a single flat `pkg/` package with two conventional exceptions (`pkg/factory/` + `pkg/handler/`); none of the 5 split triggers (cycle break, >30 files, etc.) apply to `cli` or `config`. Removes `pkg/cli/cli.go` (1 file) and `pkg/config/config*.go` (3 files) — files moved to `pkg/cli.go`, `pkg/config.go`, `pkg/config_test.go` with `package pkg`. Duplicate `pkg/config/config_suite_test.go` dropped (`pkg/pkg_suite_test.go` already covers the `pkg_test` suite). Import-only impact: `cli.NewApp` → `pkg.NewApp`, `config.Load` / `config.Config` → `pkg.Load` / `pkg.Config`. No external callers; factory + main updated.

## v0.7.0

- **feat: sample 200 `[req]` log lines.** `NewModelRouter` gains a `log.Sampler` parameter (factory passes `liblog.DefaultSamplerFactory.Sampler()` — `SamplerList{NewSampleTime(10s), NewSamplerGlogLevel(4)}`). Non-200 responses are always logged (errors are signal); 200s are logged at most once per 10s, OR unconditionally when `-v` ≥ 4 — so `curl /setloglevel/4` brings back per-request visibility for deep debug. Steady-state log becomes operator-readable under concurrent /model traffic.
- **feat: log unknown-path 404s.** New `NewNotFoundHandler` registered at `/` in the factory's mux. Catches anything not matched by `/v1/`, `/healthz`, `/readiness`, `/metrics`, `/setloglevel/`, or `/gc`. Logs at V(1) as `[404] METHOD path` so probes / typos (`/messages` without `/v1`) surface in the operator log instead of vanishing into stdlib's bare 404 default.
- **breaking: `handler.NewModelRouter` signature gains a `liblog.Sampler` parameter** (last positional). Same shape as PR #6's `defaultProviderName` add — `factory.CreateRouterFromConfig` already threads it; no YAML config-format change.

## v0.6.0

- **feat: structured per-request log line.** Replace the two-line `[route]` + `[req]` pair with a single structured line at glog V(1): `[req] POST /v1/messages model=m3 alias=MiniMax-M3-highspeed provider=minimax status=200 latency=842ms`. Fields cover incoming model, alias resolution (if any), provider name from the YAML config, HTTP status, and total wall-time latency rounded to ms. Alias-resolution + route-match detail demoted to V(2). Outer `NewLoggingHandler` middleware removed — admin endpoints (`/healthz`, `/readiness`, etc.) no longer log per request.
- **feat: runtime log-level toggle via `/setloglevel/<level>`.** Replace the noop stub with a real handler backed by `bborbe/log.LogLevelSetter`. `curl http://127.0.0.1:8788/setloglevel/3` bumps verbosity without restarting the launchd agent; auto-reverts to V(1) after 5 minutes so a forgotten bump can't leave the router in verbose mode indefinitely. Returns 400 on a non-integer level.
- **breaking: `handler.NewModelRouter` signature change.** New `defaultProviderName string` parameter (positional, after `routes`) so the fallback path appears in the structured log. `handler.ModelRoute` gains a `ProviderName` field. `factory.CreateRouterFromConfig` already threads these through — no YAML config-format change required.

## v0.5.1

- docs: add `docs/dark-factory-integration.md` — end-to-end recipe for routing dark-factory's YOLO containers through the local router. Covers the 4 required changes (router `0.0.0.0` bind, claude-yolo tinyproxy allowlist, `--add-host=host.docker.internal:host-gateway` for Linux portability, `~/.dark-factory/config.yaml` redirect), the platform matrix (Docker Desktop / OrbStack / Rancher Desktop auto-resolve `host.docker.internal`; raw Linux `dockerd` doesn't), verification curl/launchd procedure, and failure-mode table.

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

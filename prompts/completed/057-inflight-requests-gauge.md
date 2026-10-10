---
status: completed
summary: Added ccrouter_inflight_requests and ccrouter_inflight_requests_peak gauges via a new handler.InFlight collector, wired around every upstream dispatch, with tests, docs and CHANGELOG updates.
execution_id: claude-code-router-inflight-exec-057-inflight-requests-gauge
dark-factory-version: dev
created: "2026-10-10T15:21:24Z"
queued: "2026-10-10T15:21:24Z"
started: "2026-10-10T15:21:26Z"
completed: "2026-10-10T15:26:26Z"
---

# Export in-flight request gauges per provider

<summary>
- Operators can see how many requests are running in parallel against each provider right now
- A second gauge reports the highest parallel count over the last 60 seconds, so a burst that starts and ends between two samples is still visible
- The peak is not reset when read, so several scrapers or pushers all see the same value
- Every way a request can end — success, upstream error, client cancel, panic in the handler — brings the count back down
- The count never goes negative
- Existing metrics and their labels are unchanged
- The metrics doc and CHANGELOG describe the two new series
</summary>

<objective>
Add `ccrouter_inflight_requests{provider}` (current concurrency, start → +1, end → −1) and `ccrouter_inflight_requests_peak{provider}` (max over a sliding 60 s window) to the router's Prometheus metrics, so the operator can chart how many requests run in parallel per provider and compare against the per-upstream concurrency caps (summed per provider) under real load.
</objective>

<context>
Read CLAUDE.md / AGENTS.md if present, and `docs/dod.md`.
Read `pkg/handler/metrics.go` — the `Metrics` struct, `NewMetrics`, `Register`, and the GoDoc style used for each collector.
Read `pkg/handler/model-router.go` — `NewModelRouter`: the handler resolves `providerName` (from `defaultProviderName`, a pool `member.Provider`, or a `route.ProviderName`) and then calls `target.ServeHTTP(ur, r)`; `metrics.ObserveRequest(providerName, ...)` follows it. The five early-return paths before dispatch (body too large, body read failed, pool rewrite failed, alias rewrite failed, `[1m]`-strip rewrite failed) never dispatch upstream. The handler closure lives in the unexported `newModelRouter`, shared by `NewModelRouter` and `NewModelRouterWithPools`.
Read `pkg/handler/metrics_test.go` and `pkg/handler/model-router_test.go` for the Ginkgo/Gomega style, how a fresh `prometheus.NewRegistry()` is used per test, and how metric values are read back (`testutil` or gatherer).
The peak window's clock is the `libtime.CurrentDateTimeGetter` (`github.com/bborbe/time`) passed to `NewMetrics`; the factory passes the same `o.currentDateTime` it gives `NewModelRouterWithPools`. Never `time.Now()`.
Read `docs/metrics.md` — the `## Series` table and cardinality paragraph.
</context>

<requirements>
1. New file `pkg/handler/inflight.go` with an exported `InFlight` type that implements `prometheus.Collector` and exports two gauges labeled by `provider`:
   - `ccrouter_inflight_requests` — current number of requests dispatched to the provider and not yet returned.
   - `ccrouter_inflight_requests_peak` — the maximum of that count over the last 60 seconds (export the window as a named constant). Computed at collect time from per-second maxima; never reset on read; always at least the current count (a request that started more than 60 s ago still counts).
   - Constructor takes a `libtime.CurrentDateTimeGetter`. Concurrency-safe (mutex).
   - A `Start(provider string) func()` method: increments, records the peak, returns an end function that decrements exactly once even if called twice, never below zero.
   - A provider appears in the output only after its first request (no pre-initialization — provider set is bounded by config).
2. Wire it into `Metrics`: add an `InFlight *InFlight` field and change the constructor to `NewMetrics(aliases map[string]string, currentDateTime libtime.CurrentDateTimeGetter) *Metrics`, building `InFlight` from that getter. Never call `libtime.NewCurrentDateTime()` inside `NewMetrics`. In `pkg/factory/factory.go` (`handler.NewMetrics(cfg.Aliases)`) pass `o.currentDateTime`. Update every test call site (`pkg/handler/metrics_test.go`, `pkg/handler/model-router_test.go`, `pkg/handler/model-router_key_routing_test.go`) to pass a clock (`libtime.NewCurrentDateTime()` is fine in tests). Register it in `Register` alongside the other collectors, and update the `Metrics`/`NewMetrics` GoDoc (collector list, cardinality budget).
3. In `newModelRouter` (shared by both exported constructors), call `end := metrics.InFlight.Start(providerName)` immediately before `target.ServeHTTP(ur, r)` and `defer end()` so the decrement runs on every exit including a panic in the target. The early-return paths that never dispatch must not touch the gauge.
4. Tests (Ginkgo/Gomega, existing style):
   - `inflight_test.go`: start/end updates current; two concurrent starts → current 2, peak 2; after both end → current 0, peak still 2 within the window; after advancing the injected clock past 60 s → peak 0; calling the end function twice decrements once; per-provider isolation; concurrent Start/end from many goroutines leaves current at 0 — `make test` runs with `-race=false`, so also run `go test -mod=mod -race ./pkg/handler/...` once and confirm it is clean; a request still open after the clock advances past 60 s keeps peak ≥ current.
   - Through the real boundary: register `Metrics` on a fresh registry, serve a request through `NewModelRouter` with a target handler that blocks until released, assert `ccrouter_inflight_requests{provider=...}` reads 1 while blocked and 0 after (use `prometheus.NewPedanticRegistry()` so Describe/Collect consistency is checked); and a target that panics (recovered in the test) still leaves it at 0.
5. Docs: add both series to the `## Series` table in `docs/metrics.md`, a short paragraph on the peak semantics (60 s sliding, not reset on read, why), on what the count includes — the provider handler wraps the 429 throttle gate (pacing delay) and the per-upstream concurrency limiters (queue wait, overflow 429), so the gauge counts queued, paced and executing requests, and a value above the sum of the provider's `maxConcurrentRequests` means requests are queueing — and that a config reload builds a new `Metrics`, so the gauge briefly under-reports and the peak resets, and Grafana queries: `sum by (provider) (ccrouter_inflight_requests)`, `max by (provider) (ccrouter_inflight_requests_peak)`, and the no-new-metric average `sum by (provider) (rate(ccrouter_request_duration_seconds_sum[5m]))`. Update the cardinality paragraph (+2 series per provider).
6. `CHANGELOG.md`: one `feat:` bullet under `## Unreleased` (create the heading at the top if absent).
7. Before you finish, re-run `<verification>` and confirm it passes; walk each requirement above against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass; existing metric names and labels unchanged (`NewMetrics` gains only the clock parameter)
- Use `github.com/bborbe/time` for time, `github.com/bborbe/errors` if any error is returned, `glog.V(n)` for any logging
- No new dependencies
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` -- must pass.
</verification>

---
spec: ["019-per-host-concurrency-cap"]
status: draft
created: "2026-10-10T15:30:08Z"
---

# Per-host in-flight gauge: ccrouter_upstream_inflight{host}

<summary>
- `/metrics` gains one new gauge, `ccrouter_upstream_inflight`, with one series per upstream host the router serves.
- For a capped host the value is how many requests currently hold one of that host's shared slots, so it can never read above the configured cap. An operator can see the cap holding instead of inferring it.
- For an uncapped host the value is the live count of requests in flight to it. Every served host gets a series, capped or not.
- The value is read at scrape time from the host limiter itself, so it is always current, and it returns to 0 once requests finish, including after a failed request.
- A host named in the config that no provider points at gets no series.
- After a SIGHUP reload, `/metrics` reports the rebuilt handler tree's hosts, consistent with how the existing `ccrouter_*` series behave across reloads.
- Existing counters and histograms are unchanged; the new series is purely additive.
</summary>

<objective>
Expose `ccrouter_upstream_inflight{host="<host>"}`, a gauge of each upstream host's current in-flight request count. It reads the shared host limiter's occupancy for capped hosts and the in-flight count for uncapped ones. The per-host bound added in prompt 1 then becomes observable on `/metrics`, which is the rung-2 evidence that a burst against `vllm.seibert.tools` peaks at the configured cap.
</objective>

<context>
- Repo root is the current working directory. Use repo-relative paths only. This is a single-module repo, and `make precommit` / `make test` are root targets.
- This prompt depends on prompt 1 (`1-spec-019-host-limit-config-and-wiring.md`). Do not execute it until prompt 1 has shipped:
  - `pkg/handler/host-limiter.go`, with `type HostLimiter`, `func NewHostLimiter(maxConcurrentRequests int, maxConcurrentWait time.Duration) *HostLimiter`, `func (h *HostLimiter) Wrap(next http.Handler) http.Handler`, and `func (h *HostLimiter) InFlight() int`. `InFlight` returns semaphore occupancy when capped and the live in-flight count when unlimited.
  - `pkg.UpstreamHostKey(u *url.URL) string` in `pkg/config.go`.
  - A local `hostLimiters := make(map[string]*handler.HostLimiter)` in `CreateRouterFromConfig` in `pkg/factory/factory.go`, filled with exactly one limiter per distinct host every provider and pool member resolves to, capped or not.

  Read all three before writing code, and confirm the real signatures match.
- Read `pkg/handler/metrics.go`:
  - The top-of-file declarations: `const UnknownModelLabel`, then `var LatencyBucketsSeconds`.
  - The `Metrics` struct, `NewMetrics`, and `(*Metrics).Register(reg prometheus.Registerer) error`, which loops over a `[]prometheus.Collector{...}` literal.
  - The file imports `strconv` and `github.com/prometheus/client_golang/prometheus`.

  **Coordination:** a sibling branch (`feat/inflight-gauge`, not yet merged) edits the `Metrics` struct, `NewMetrics`, the `Register` collector loop, and adds `pkg/handler/inflight.go` with per-provider gauges `ccrouter_inflight_requests{provider}` / `ccrouter_inflight_requests_peak{provider}`. To keep whichever branch merges second a clean rebase, this prompt's `metrics.go` edit is ONE small additive const block, and the collector lives in its own new file.
- Read `pkg/factory/factory.go`, `CreateRouterFromConfig`. Note the `if err := metrics.Register(o.metricsRegisterer); err != nil { return nil, errors.Wrapf(ctx, err, "register metrics") }` call after `buildModelPools`, and the `gatherer` / `buildMux(...)` tail that serves `/metrics` from the same registerer.
  - At startup the registerer is `prometheus.DefaultRegisterer`.
  - On each SIGHUP reload it is a fresh `NewReloadRegistry()`. That is why registering a second collector per build never collides.
- Read `pkg/handler/metrics_test.go` for the existing registration-contract rows (`Register against a fresh registry succeeds`, `testutil.CollectAndCount` usage).
- Read `pkg/factory/metrics_wiring_test.go` for the "scrape `/metrics` through the returned mux" row shape (`httptest.NewRequest(http.MethodGet, "/metrics", nil)` → body `ContainSubstring`).
- Read `pkg/handler/concurrency-limiter_test.go` for the package-level `blockingHandler` / `newBlockingHandler` / `serveAsync` / `newMessagesRequest` helpers (package `handler_test`). Reuse them and do NOT redeclare them.
- Read `pkg/factory/host_limiter_wiring_test.go` (from prompt 1) for the peak-tracking upstream and host-key helper shapes.
- Read `docs/dod.md` — Definition of Done (GoDoc on every new exported identifier, `bborbe/errors` conventions, Ginkgo/Gomega coverage).
- Coding plugin docs (in-container paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md`: naming, labels, custom collectors, `testutil` assertions.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`: Ginkgo v2 + Gomega.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md`: GoDoc conventions.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`: `errors.Wrapf(ctx, err, ...)`.
</context>

<requirements>
1. **Minimal additive edit in `pkg/handler/metrics.go`.** Insert ONE const block immediately after the `LatencyBucketsSeconds` var declaration and before the `Metrics` struct's doc comment. Do NOT touch the `Metrics` struct, `NewMetrics`, `Register`, `ObserveRequest`, `statusClass`, or any other existing line.

   ```go
   const (
   	// upstreamInFlightMetricName is the per-upstream-host in-flight gauge
   	// (spec 019), exported by the collector in
   	// upstream-inflight-collector.go. It is a distinct series from the
   	// per-provider in-flight gauges and is registered separately from
   	// Metrics.Register so the two never collide.
   	upstreamInFlightMetricName = "ccrouter_upstream_inflight"
   	// upstreamInFlightMetricHelp is the HELP text of
   	// ccrouter_upstream_inflight.
   	upstreamInFlightMetricHelp = "Current number of /v1/* requests in flight to an upstream host, summed across every provider and pool member resolving to that host (spec 019). For a host capped in upstreamHostLimits this is the shared semaphore's occupancy and never exceeds the cap; for an uncapped host it is the live in-flight count."
   )
   ```

2. **New collector in `pkg/handler/upstream-inflight-collector.go`** (package `handler`, license header as in sibling files). The file name and identifiers are host/upstream-prefixed so they cannot collide with the sibling branch's `inflight.go`.

   ```go
   // NewUpstreamInFlightCollector returns a Prometheus collector exporting
   // ccrouter_upstream_inflight{host} (spec 019): one gauge series per entry
   // of hostLimiters, valued by that HostLimiter's InFlight() read at scrape
   // time — the shared semaphore occupancy for a capped host, the live
   // in-flight count for an uncapped one. hostLimiters is the factory's
   // one-limiter-per-host map; the collector snapshots its keys at
   // construction (sorted, for deterministic output) and never mutates it.
   // The collector is registered once per handler
   // tree, on the same registerer as the tree's other ccrouter_* series.
   func NewUpstreamInFlightCollector(hostLimiters map[string]*HostLimiter) prometheus.Collector
   ```

   Implementation contract:
   - An unexported `upstreamInFlightCollector` struct holds `desc *prometheus.Desc`, a sorted `hosts []string` and a parallel `limiters []*HostLimiter`. Build `desc` with `prometheus.NewDesc(upstreamInFlightMetricName, upstreamInFlightMetricHelp, []string{"host"}, nil)`. Sort with `sort.Strings`.
   - `Describe(ch chan<- *prometheus.Desc)` sends `c.desc`. This is a checked collector, so registration validates the name and label.
   - `Collect(ch chan<- prometheus.Metric)` emits, for each host, `prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(limiter.InFlight()), host)`. `InFlight()` is a channel `len` or an atomic load, so scraping takes no lock and never blocks the request path.
   - Do NOT add peak, queue-depth, wait-time, rejection or limit series (spec Non-goals: "No new queue-depth, wait-time, or rejection metrics beyond the one in-flight gauge"). Do NOT pre-create series for hosts absent from the map. A config entry naming a host no provider resolves to has no limiter and therefore no series, per the spec Failure Mode.

3. **Factory registration in `pkg/factory/factory.go`.** Directly after the existing `metrics.Register(o.metricsRegisterer)` error check in `CreateRouterFromConfig`, add:

   ```go
   // ccrouter_upstream_inflight{host} (spec 019): one series per distinct
   // upstream host this handler tree serves, read live from the shared
   // host limiters. Registered on the same registerer as the tree's other
   // ccrouter_* series, so a SIGHUP reload (fresh registry) exposes the
   // rebuilt tree's hosts.
   if err := o.metricsRegisterer.Register(handler.NewUpstreamInFlightCollector(hostLimiters)); err != nil {
   	return nil, errors.Wrapf(ctx, err, "register upstream inflight collector")
   }
   ```

   `hostLimiters` is complete at this point and never mutated afterwards, so the collector reads it without races. Do not change anything else in the factory.

4. **Handler tests in a new `pkg/handler/upstream-inflight-collector_test.go`** (package `handler_test`, Ginkgo v2 + Gomega, `github.com/prometheus/client_golang/prometheus/testutil`):
   - **AC 8, capped host occupancy 0 → 1 → 0:**
     - `hl := handler.NewHostLimiter(2, time.Second)` and `c := handler.NewUpstreamInFlightCollector(map[string]*handler.HostLimiter{"vllm.seibert.tools": hl})`.
     - Assert `testutil.ToFloat64(c) == 0` at rest.
     - Serve one request through `hl.Wrap(inner)` async, where `inner` is a `blockingHandler`. `Eventually` `testutil.ToFloat64(c) == 1` while it is held.
     - Close `inner.release`. `Eventually` the request is done and `testutil.ToFloat64(c) == 0`.
   - **AC 8, uncapped host is present and counts:** the same 0 → 1 → 0 sequence with `handler.NewHostLimiter(0, time.Second)`. The series exists for an uncapped host and reports its in-flight count with no limit applied.
   - **Labels, multiple hosts:**
     - Build a collector over two hosts (`"a.example"` capped, `"127.0.0.1:8317"` uncapped).
     - Register it on `prometheus.NewPedanticRegistry()`, which must succeed. The pedantic registry checks `Collect` is consistent with `Describe`.
     - `Gather()` returns no error and one `ccrouter_upstream_inflight` family of type GAUGE with exactly two metrics.
     - Their `host` label values are exactly `{"127.0.0.1:8317", "a.example"}`, and there is no other label.
   - **Cap holds in the gauge:**
     - With `NewHostLimiter(1, 50*time.Millisecond)`, one request is held via `hl.Wrap(innerA)`.
     - A second request via `hl.Wrap(innerB)` times out with 429.
     - During and after that timeout `testutil.ToFloat64(c)` stays `1` and never reads 2. The queued request is not counted as in flight.
     - Release and clean up.
   - **Gauge returns to 0 after a failed request (spec Failure Mode — slot leak):** for both `NewHostLimiter(2, time.Second)` and `NewHostLimiter(0, time.Second)`, serve once through `hl.Wrap(panicky)` where `panicky` panics, inside a func that `recover()`s; then `testutil.ToFloat64(c) == 0`.

5. **Factory wiring test in a new `pkg/factory/upstream_inflight_wiring_test.go`** (package `factory_test`). Mirror the scrape shape in `metrics_wiring_test.go` and the blocking upstream in `host_limiter_wiring_test.go`. Use a dedicated `reg := prometheus.NewRegistry()` passed via `factory.WithMetricsRegisterer(reg)`, and scrape `/metrics` THROUGH THE RETURNED MUX. That is the real exposition boundary.
   - **AC 8, end to end, capped and uncapped hosts both exposed:**
     - Set up two `httptest` upstreams `srvA` and `srvB`. They are on different ports, so they have different host keys `keyA` / `keyB` via `pkg.UpstreamHostKey`.
     - Provider `"capped"` (`Models: ["c*"]`, `Upstream: srvA.URL`) with `UpstreamHostLimits: {keyA: {MaxConcurrentRequests: 1, MaxConcurrentWaitSeconds: 5}}`. Provider `"free"` (`Models: ["f*"]`, `Upstream: srvB.URL`) is uncapped.
     - Scrape at rest: the body contains `ccrouter_upstream_inflight{host="<keyA>"} 0` and `ccrouter_upstream_inflight{host="<keyB>"} 0`.
     - Hold one `c1` request on srvA. `Eventually` a scrape body contains `ccrouter_upstream_inflight{host="<keyA>"} 1`.
     - Hold one `f1` request on srvB. `Eventually` a scrape body contains `...{host="<keyB>"} 1`.
     - Release both. `Eventually` a scrape shows both hosts back at `0`.
   - **Inert host has no series (spec Failure Mode):** add `"unused.example": {MaxConcurrentRequests: 1}` to the same `UpstreamHostLimits`; every scrape body must NOT contain `host="unused.example"`.
   - **Shared host reports the summed count:**
     - Two providers `"a"` and `"b"` on the SAME `srvA.URL`, with no host cap. Hold one request on each.
     - `Eventually` the scrape shows ONE series for `keyA` with value `2`, not one per provider.
   - **Reload exposes the rebuilt tree:**
     - Build twice with two DIFFERENT fresh registries, mirroring the reloader's `NewReloadRegistry()` per rebuild. The first build has `keyA` capped and the second has `UpstreamHostLimits: nil`.
     - Both builds succeed, so there is no duplicate-registration error. Each mux's `/metrics` contains a `ccrouter_upstream_inflight{host="<keyA>"}` series.

6. **Self-check before finishing.** Run `make precommit` and confirm it passes. Then walk spec AC 8 against these rows: occupancy 0 at rest, 1 while held, 0 after; present for uncapped hosts; and `grep -c 'ccrouter_upstream_inflight' pkg/handler/metrics.go` ≥ 1. Confirm the `metrics.go` diff is ONLY the new const block.
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- `/metrics` exposes a gauge `ccrouter_upstream_inflight` labelled by `host`, reporting that host's current in-flight count. That is the shared semaphore's occupancy where a cap is configured, and the in-flight count where none is. It is present for every host the router serves, including uncapped ones.
- The existing `ccrouter_*` counter and histogram series are unchanged, and `ccrouter_upstream_inflight` is additive. Do NOT modify the `Metrics` struct, `NewMetrics`, `Metrics.Register`, `statusClass`, or any existing collector's name, help or labels.
- Do NOT add any test that asserts the ABSENCE of `ccrouter_inflight_requests*` in a scrape. Once the sibling branch merges, those series legitimately appear and such a test would break.
- Do NOT add, rename or reference the per-provider gauges `ccrouter_inflight_requests` / `ccrouter_inflight_requests_peak`, and do NOT create or edit `pkg/handler/inflight.go`. The sibling branch `feat/inflight-gauge` owns them, and `ccrouter_upstream_inflight{host}` is a distinct series that stays. Keep the `metrics.go` edit to the single additive const block so the second-merging branch rebases cleanly.
- No new queue-depth, wait-time, rejection, peak or limit metrics. Exactly one new series family.
- The gauge label is the upstream host key (operator-configured infrastructure). Never label by request header, model, API key, provider or client identity.
- Do NOT change host-limiter, config or limiter behavior. Prompt 1 owns that. The only factory edit is the collector registration.
- The reloader stays untouched. The per-reload fresh registry already makes per-build registration safe.
- Do NOT touch `docs/` or `CHANGELOG.md`. Prompt 3 owns them.
- No new dependencies. `prometheus/client_golang` (including `testutil`) is already a dependency.
- Tests must not depend on real wall-clock waits beyond small explicit windows (≤ 300ms `Consistently`, and the 50ms host wait).
- GoDoc on every new exported identifier, `github.com/bborbe/errors` wrapping, and Ginkgo/Gomega conventions (`docs/dod.md`).
- No AI attribution in code or comments.
- Existing tests must still pass. `make precommit` must be green.
</constraints>

<verification>
make precommit

# Prompt-1 dependency present (fail loudly if mis-sequenced):
grep -n 'func (h \*HostLimiter) InFlight' pkg/handler/host-limiter.go
grep -n 'func UpstreamHostKey' pkg/config.go

# AC 8 — gauge name in metrics.go (expect >=1):
grep -c 'ccrouter_upstream_inflight' pkg/handler/metrics.go

# Collector + factory registration:
grep -n 'func NewUpstreamInFlightCollector' pkg/handler/upstream-inflight-collector.go
grep -n 'NewUpstreamInFlightCollector(hostLimiters)' pkg/factory/factory.go

# Targeted suites:
go test -mod=mod -count=1 ./pkg/handler/ ./pkg/factory/
</verification>

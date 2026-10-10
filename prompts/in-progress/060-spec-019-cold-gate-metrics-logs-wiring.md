---
status: approved
spec: [019-cold-start-admission-gate]
created: "2026-10-10T14:08:35Z"
queued: "2026-10-10T15:02:15Z"
branch: dark-factory/cold-start-admission-gate
---

# Cold-start admission gate: metrics, log lines, and factory wiring

<summary>
- The router exposes four new additive Prometheus series for the cold-start gate, each labeled by provider: a count of delayed cold requests, a count of refused cold requests, a gauge of in-flight cold tokens, and a histogram of cold time-to-first-token.
- The in-flight gauge rises while a cold request holds its reservation and returns to zero as the budget is released.
- Each delayed cold request and each refused cold request emits one INFO log line carrying the provider, the decision, and the reason.
- No log line ever contains a raw session id — the client-controlled id is used only as a grouping key.
- The gate is wired into the request tree per provider, immediately after the upstream pool handler and before the existing 429 delay gate, so the existing gate stays outermost and cold admission is the last gate before the pool.
- Because the gate sits inside the provider handler the model router dispatches to, both glob-routed and default-provider traffic pass through it, and a refusal lands in the existing rate-limited status class — no new status class value.
- With the knobs unset the wired gate is the pool handler unchanged, so an unconfigured router behaves byte-for-byte as it does today.
- A SIGHUP reload (or any second construction from a changed config) rebuilds the gate enforcing the new values, with fresh in-memory state.
</summary>

<objective>
Make the cold-start admission gate observable and live: add the four additive Prometheus series and the `[coldgate]` INFO log lines, then wire the gate into `pkg/factory/factory.go` between each provider's upstream pool handler and the throttle gate so the admission behaviour shipped by prompts 2 and 3 actually runs on the request path and hot-reloads with the rest of the config.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only. Single-module Go repo. `make test` / `make precommit` are root targets; this repo's `Makefile` does NOT use `ROOTDIR`/`default.env`, so no override is needed. `.git` may be masked in the container — NEVER put a bare `git` command in `<verification>`.
- This prompt depends on prompts 2 and 3: read the shipped `pkg/handler/cold-start-gate.go` in full (`NewColdStartGate`'s signature includes `budgetTokens`, `sessionWindow`, `newSessionsPerMinute`, `maxWait`, `now`, and `metrics handler.ColdGateMetrics`; the gate already increments the nil-safe `Delayed`, `Refused`, `TokensInFlight`, and `TTFT` handles at its decision sites, and `refuse` / the delayed-admission path are where the log lines go).
- Read `pkg/handler/metrics.go` — the `Metrics` struct (its last field is `ThrottledTotal *prometheus.CounterVec`, spec 018), `NewMetrics(aliases map[string]string) *Metrics`, `Register(reg prometheus.Registerer) error` (the collector loop to extend), and `LatencyBucketsSeconds` (the histogram buckets to reuse). The `statusClass` 7-value enum and the `4xx_rate_limited` classification are NOT touched (spec Non-goals: "No new Prometheus status_class value").
- Read `pkg/factory/factory.go` — `CreateRouterFromConfig`'s per-provider loop: `metrics := handler.NewMetrics(cfg.Aliases)` already sits BEFORE the loop; `providerHandler := handler.NewUpstreamPoolHandler(ctx, members)`; then the spec-018 `providerHandler = handler.NewThrottleGate(...)`; then `providerHandlers[name] = providerHandler`. The `routes` loop carries `Handler: providerHandler` and `defaultHandler` is read from `providerHandlers[cfg.Router.DefaultProvider]`, so wrapping the value stored in `providerHandlers[name]` covers both paths. `defaultMaxConcurrentWaitSeconds` / `defaultThrottleMaxDelaySeconds` are the precedent for a factory-level default constant. `o.currentDateTime.Now` is the injected clock.
- Read `pkg/factory/throttle_gate_wiring_test.go` — the wiring-test shape to mirror (`makeConfig` building `*pkg.Config`, `newMessagesRequest`, `serveAsync`, an explicit `prometheus.NewRegistry()` registerer, a fixed `WithCurrentDateTime(clock)`, an `httptest.NewServer` upstream with an atomic call counter, the rebuild row calling `CreateRouterFromConfig` twice, and the `4xx_rate_limited` negative-evidence row gathering families from the registry).
- Read `pkg/factory/factory.go`'s `buildMux` — `/v1/` is wrapped by `handler.NewSessionMiddleware`, which strips the `X-Session-Id` header and carries it on the request context. A wiring test that drives the real dispatch path therefore sets the id via the `X-Session-Id` header, not the context directly.
- Read `pkg/reloader/reloader.go` — `Reload` calls `pkg.Load` then the build func (`CreateRouterFromConfig`), so a second construction IS the SIGHUP path; admission state is in-memory, so a rebuild resets the window, the budget, and the bucket.
- Read `pkg/handler/model-router_test.go` — the `captureStderr(fn func()) string` helper (pair with `flag.Set("logtostderr", "true")`); reuse it for the log rows.
- Coding plugin docs (in-container paths): `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` (CounterVec/GaugeVec/HistogramVec naming, `Register`, `testutil.ToFloat64`/`CollectAndCount`), `go-logging-guide.md` (unconditional INFO via `glog.Infof`), `go-testing-guide.md`, `go-factory-pattern.md` (`Create*` wiring lives in `pkg/factory/`), `go-doc-best-practices.md`.
</context>

<requirements>
1. **Four additive collectors in `pkg/handler/metrics.go`.** Add these four fields to the `Metrics` struct (after `ThrottledTotal`) and construct them in `NewMetrics` with exactly these names, label sets, and types; extend `Register`'s collector slice to include all four:
   ```go
   ColdAdmissionDelayedTotal  *prometheus.CounterVec   // ccrouter_cold_admission_delayed_total{provider}
   ColdAdmissionRefusedTotal  *prometheus.CounterVec   // ccrouter_cold_admission_refused_total{provider}
   ColdTokensInFlight         *prometheus.GaugeVec     // ccrouter_cold_tokens_in_flight{provider}
   ColdTTFTSeconds            *prometheus.HistogramVec // ccrouter_cold_ttft_seconds{provider}
   ```
   - Both counters: `prometheus.NewCounterVec(prometheus.CounterOpts{Name: ..., Help: ...}, []string{"provider"})`.
   - The gauge: `prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "ccrouter_cold_tokens_in_flight", Help: ...}, []string{"provider"})`.
   - The histogram: `prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "ccrouter_cold_ttft_seconds", Help: ..., Buckets: LatencyBucketsSeconds}, []string{"provider"})`.
   - Do NOT pre-initialize per-provider series and do NOT add an `Observe*` method — the gate already holds the handles. Do NOT modify `statusClass` or add a `status_class` value.
2. **`[coldgate]` INFO log lines in `pkg/handler/cold-start-gate.go`** (unconditional `glog.Infof`, mirroring the throttle gate's `[throttle]` lines):
   - In `refuse`, at the top: `glog.Infof("[coldgate] provider=%s decision=refused reason=%s", g.provider, reason)` where `reason` is `queue_full` or `timeout`.
   - On the delayed path (a request admitted after waiting), once: `glog.Infof("[coldgate] provider=%s decision=delayed reason=%s", g.provider, reason)` where `reason` is `budget` or `rate`.
   - NEVER include the session id, the body, or any client-controlled value in any line; the only interpolated values are the provider name and the fixed reason token.
3. **Factory wiring in `pkg/factory/factory.go`.** Do NOT add a factory-side max-wait constant: the handler's own `coldGateMaxWait` is the single source of truth, and a factory copy would silently shadow a later change to it — the same reasoning applies to the session window. Pass `0` and let the constructor resolve it. In the per-provider loop, wrap the pool handler with the cold gate BEFORE the existing throttle gate (order is load-bearing — the throttle gate must stay outermost):
   ```go
   providerHandler := handler.NewUpstreamPoolHandler(ctx, members)
   providerHandler = handler.NewColdStartGate(
       providerHandler,
       name,
       prov.ColdPrefillBudgetTokens,
       time.Duration(prov.ColdSessionWindowSeconds)*time.Second,
       prov.NewSessionRatePerMinute,
       0, // 0 → let the constructor resolve its own 30s max-wait default
       o.currentDateTime.Now,
       handler.ColdGateMetrics{
           Delayed:        metrics.ColdAdmissionDelayedTotal,
           Refused:        metrics.ColdAdmissionRefusedTotal,
           TokensInFlight: metrics.ColdTokensInFlight,
           TTFT:           metrics.ColdTTFTSeconds,
       },
   )
   // existing spec-018 throttle gate wraps the cold gate:
   providerHandler = handler.NewThrottleGate(providerHandler, name, ...)
   providerHandlers[name] = providerHandler
   ```
   A non-positive `coldSessionWindowSeconds` yields a non-positive `time.Duration`, which the constructor resolves to its 600-second default — do NOT add a factory-side window default. A provider with both cold knobs absent/0/negative gets the pool handler unchanged from `NewColdStartGate` (its no-op path), so the tree is unchanged for it. Do NOT add a separate wrap for `defaultHandler` (it reads the wrapped `providerHandlers[...]` value by construction).
4. **Tests in `pkg/handler/cold-start-gate_metrics_test.go`** (`package handler_test`; add a local helper that constructs the gate with real collectors). Rows:
   - **AC 11 (four series, isolated registry):** register the four collectors on a fresh `prometheus.NewRegistry()`; drive a delayed request (budget blocked, then the budget freed by releasing a held reservation) → `testutil.ToFloat64(delayed.WithLabelValues("p")) == 1`; drive a timeout refusal → `refused == 1`; a blocking upstream holding a reservation → `Eventually` `testutil.ToFloat64(tokensInFlight.WithLabelValues("p"))` is non-zero, then release → `Eventually` it is 0; an upstream that writes `event: content_block_delta\ndata: {...}\n\n` → `testutil.CollectAndCount(ttft, "ccrouter_cold_ttft_seconds")` reaches 1.
   - **AC 12 (logging, no session ids):** `flag.Set("logtostderr", "true")`; inside `captureStderr`, drive one delayed request and one refused request, both carrying a distinctive session id (e.g. `SECRET-SESSION-abc123`). Assert the captured output contains a line matching `[coldgate] provider=p decision=delayed reason=budget` and a line matching `[coldgate] provider=p decision=refused reason=` (with `timeout` or `queue_full`); and assert the captured output contains NO occurrence of `SECRET-SESSION-abc123`.
5. **Tests in `pkg/factory/cold_gate_wiring_test.go`** (`package factory_test`, mirroring `throttle_gate_wiring_test.go`). The upstream is an `httptest.NewServer` handler that blocks on a release channel after recording entry, so a reservation is held. Every request is a `newMessagesRequest(model)`-shaped POST to `/v1/messages` whose session id arrives via `req.Header.Set("X-Session-Id", <id>)` (the session middleware strips it and carries it on the context), with a body long enough that `len(body)*2/7 > 1`. Rows:
   - **AC 13 (wiring + SIGHUP rebuild):** build handler1 with `ColdPrefillBudgetTokens: 1`, `NewSessionRatePerMinute: 0` via `CreateRouterFromConfig(ctx, cfg1, factory.WithMetricsRegisterer(reg1), factory.WithCurrentDateTime(clock))`. Serve one cold request (id `hold`) in a goroutine → `Eventually` the upstream entry count is 1 (admitted alone; its reservation is held). Then fire `33` concurrent cold requests with distinct session ids (the fixed queue capacity is 32, so the 33rd finds the queue full) → `Eventually` exactly one is answered HTTP 429 while the others wait; cancel the waiters' contexts and release the upstream to clean up. Then build handler2 from a config with `ColdPrefillBudgetTokens: 1000000` (the changed knob) via a SECOND `CreateRouterFromConfig(ctx, cfg2, factory.WithMetricsRegisterer(reg2), factory.WithCurrentDateTime(clock))` call with a FRESH `reg2 := prometheus.NewRegistry()` (exactly the reloader's rebuild; reusing `reg1` makes `metrics.Register` return `AlreadyRegisteredError` and the build fails — `throttle_gate_wiring_test.go` avoids this with its own `reg2`) and serve one cold request → `Eventually` it reaches the upstream and returns 200 — the rebuilt tree applies the new values with fresh state.
   - **AC 11 negative evidence:** with the registerer `reg1`, gather `families, err := reg1.Gather()` and assert the `ccrouter_requests_total` family contains a series whose `status_class` label is `"4xx_rate_limited"` with counter value ≥ 1 (the gate's refusal records through the UNCHANGED class), and that no `status_class` label value outside the existing 7-value enum appears.
   Note: `handler.ColdGateQueueCapacity` (prompt 3's `export_test.go` seam) is NOT visible from `package factory_test` — use the literal `33` for the queue-full burst there.
6. **Before finishing, re-run `<verification>` and confirm every command passes.** Walk requirements 1–5 against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- This prompt depends on prompts 2 and 3: do not approve or execute until `pkg/handler/cold-start-gate.go` ships `NewColdStartGate` (with the rate + max wait) and its nil-safe `ColdGateMetrics` increments.
- Keying and placement (spec Constraints): the gate is per provider, constructed immediately after the upstream pool handler and before the throttle gate, so the existing 429 delay gate is outermost and cold admission is the last gate before the pool. The model router's body read, alias resolution, `[1m]` strip, key routing, and system-lift flow are untouched. Do NOT modify `pkg/handler/model-router.go`.
- No new Prometheus `status_class` value — the existing `4xx_rate_limited` classification covers the refusal; the four new series are additive only (spec Non-goals).
- The refusal reuses the existing `limiter429Body` constant; the body must not carry queue depth, provider name, upstream URL, or any session-identifying value. Raw session ids must never appear in any log line.
- Fixed internal constants (NOT knobs): rate burst 2, queue capacity 32, max wait 30s, estimate divisor 3.5, `Retry-After` clamp 1–60s. The session window IS a knob (`coldSessionWindowSeconds`), 600s fallback when absent or negative. Do NOT add config fields, opt-out flags, or tunable thresholds.
- SIGHUP reload applies changed knobs without a restart: the reloader rebuilds the per-provider tree; admission state is in-memory, so a reload resets the window, the in-flight budget, and the bucket — reversible by design.
- Per-provider independence: no shared/global cold state; two providers sharing one upstream each bound their own cold work.
- No new dependencies — the Go standard library plus existing `bborbe/*` libraries suffice.
- No AI attribution in code or comments.
- `make precommit` must remain green. Follow `docs/dod.md`.
- Do NOT touch `docs/`, `README.md`, or `CHANGELOG.md` in this prompt — the next prompt owns documentation.
</constraints>

<verification>
make precommit

# Four additive series + register loop:
grep -n 'ccrouter_cold_admission_delayed_total' pkg/handler/metrics.go
grep -n 'ccrouter_cold_admission_refused_total' pkg/handler/metrics.go
grep -n 'ccrouter_cold_tokens_in_flight' pkg/handler/metrics.go
grep -n 'ccrouter_cold_ttft_seconds' pkg/handler/metrics.go
grep -n 'ColdAdmissionDelayedTotal\|ColdAdmissionRefusedTotal\|ColdTokensInFlight\|ColdTTFTSeconds' pkg/handler/metrics.go

# status_class enum untouched:
grep -c 'func statusClass' pkg/handler/metrics.go     # expect 1
grep -c '4xx_rate_limited' pkg/handler/metrics.go     # expect >= 1

# Log lines + no session id interpolated:
grep -n 'decision=delayed reason=%s' pkg/handler/cold-start-gate.go
grep -n 'decision=refused reason=%s' pkg/handler/cold-start-gate.go
! grep -nE 'Infof.*[Ss]essionID' pkg/handler/cold-start-gate.go

# Factory wiring: cold gate inside the throttle gate:
! grep -nE '^\s*(const|var)\s+(defaultColdGateMaxWait|coldGateMaxWait)' pkg/factory/factory.go   # no factory-side duplicate of the handler default (matches a DECLARATION, not a comment)
grep -n 'handler.NewColdStartGate' pkg/factory/factory.go

# Test rows exist:
grep -c 'ccrouter_cold_ttft_seconds' pkg/handler/cold-start-gate_metrics_test.go   # expect >= 1
grep -c 'NewColdStartGate\|ColdPrefillBudgetTokens' pkg/factory/cold_gate_wiring_test.go   # expect >= 1

# Handler + factory suites:
go test -count=1 ./pkg/handler/ ./pkg/factory/
</verification>

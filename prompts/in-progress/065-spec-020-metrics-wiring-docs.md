---
status: approved
spec: [020-ttft-backpressure-and-circuit-breaker]
created: "2026-10-11T09:12:32Z"
queued: "2026-10-11T10:05:40Z"
---

# Metrics, factory wiring, SIGHUP rebuild, and documentation

<summary>
- Two new additive Prometheus series expose the new behaviours: a gauge of the live effective cold-prefill budget per provider, and a counter of circuit-breaker state transitions per provider and state.
- The breaker's three state series are pre-initialized to zero so an alert on a transition evaluates to 0 rather than no-data for a provider that has not tripped yet.
- Both behaviours are wired into the per-provider request tree: the backpressure controller is built alongside the cold-start gate and drives its live budget, and the breaker sits outermost so an open breaker short-circuits the whole provider.
- With the knobs unset, the wired tree is unchanged and neither new series exists — a provider that opts in gets the behaviour and the series; one that does not is byte-for-byte as it is today.
- A SIGHUP reload rebuilds the per-provider tree, so changed knob values are live without a restart; the in-memory budget and breaker state reset and re-accumulate from the next requests.
- Operators can read both new knobs in the config reference and the example config, and both new series in the metrics reference.
- The changelog names both new knobs under a new `Unreleased` heading.
- Nothing in this change alters the existing `status_class` enum or any existing series.
</summary>

<objective>
Make both spec-020 behaviours live and observable: add the two additive Prometheus collectors, wire the backpressure controller and the circuit breaker into `pkg/factory/factory.go` so they run on the real request path and hot-reload with the config, and document the knobs, the series, and the changelog entry — so the shipped code, the config reference, and the metrics reference all describe the same thing. This prompt also carries the documentation scope that the spec's Suggested Decomposition assigned to its own row.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only. Single-module Go repo. `make test` / `make precommit` are root targets; this repo's `Makefile` does NOT use `ROOTDIR`/`default.env`, so no override is needed. `.git` may be masked in other repos; never rely on a bare `git` command.
- This prompt depends on prompts 2 and 3: read the shipped `pkg/handler/ttft-backpressure.go` (`NewTTFTBackpressure(provider string, ceilingTokens int, thresholdSeconds int, now func() libtime.DateTime, budgetGauge *prometheus.GaugeVec) *TTFTBackpressure`, `Budget()`, `ObserveColdTTFT`, `ObserveStatus`) and `pkg/handler/circuit-breaker.go` (`NewCircuitBreaker(next http.Handler, provider string, failureThreshold int, now func() libtime.DateTime, transitions *prometheus.CounterVec) http.Handler`), plus the modified `pkg/handler/cold-start-gate.go` (`NewColdStartGateWithBackpressure(next, provider, budgetTokens, backpressure, sessionWindow, newSessionsPerMinute, maxWait, now, metrics)`).
- Read `pkg/handler/metrics.go` — the `Metrics` struct (`ColdTTFTSeconds` is the newest field, followed by `InFlight`), `NewMetrics(aliases map[string]string, currentDateTime libtime.CurrentDateTimeGetter) *Metrics`, and `Register(reg prometheus.Registerer) error` (the collector slice to extend). The `statusClass` 7-value enum and the `4xx_rate_limited` classification are NOT touched (spec 020 Non-goals: "No new Prometheus `status_class` value").
- Read `pkg/factory/factory.go` — `CreateRouterFromConfig`'s per-provider loop: `metrics := handler.NewMetrics(cfg.Aliases, o.currentDateTime)` sits BEFORE the loop; `providerHandler := handler.NewUpstreamPoolHandler(ctx, members)`; then `providerHandler = coldGateHandler(providerHandler, name, prov, o.currentDateTime.Now, metrics)`; then `providerHandler = handler.NewThrottleGate(...)`; then `providerHandlers[name] = providerHandler`. Also read the `coldGateHandler` helper (it calls `handler.NewColdStartGate` with `0` as the max wait so the handler's own default is the single source of truth — keep that property). `defaultThrottleMaxDelaySeconds` is the precedent for a factory-level default constant; `o.currentDateTime.Now` is the injected clock.
- Read `pkg/factory/cold_gate_wiring_test.go` and `pkg/factory/throttle_gate_wiring_test.go` — the wiring-test shape to mirror (`makeConfig` building `*pkg.Config`, `newMessagesRequest`, `serveAsync`, an explicit `prometheus.NewRegistry()` registerer per build, a fixed `WithCurrentDateTime(clock)`, an `httptest.Server` upstream with an atomic call counter, the rebuild row calling `CreateRouterFromConfig` twice with a FRESH registry each time, and gathering families from the registry with `reg.Gather()`).
- Read `pkg/reloader/reloader.go` — `Reload` calls `pkg.Load` then the build func (`CreateRouterFromConfig`), so a second construction IS the SIGHUP path; both behaviours keep in-memory state, so a rebuild resets the budget and the breaker.
- Read `pkg/handler/cold-start-gate_metrics_test.go` — the collector-test shape: `handler.NewMetrics(nil, testDateTime)`, `m.Register(reg)` on a fresh `prometheus.NewRegistry()`, `testutil.ToFloat64(...)`, `testutil.CollectAndCount(...)`, and the `[coldgate]` log rows. Mirror it.
- Read `pkg/handler/throttle-gate_test.go` — `t0`, `newClock()`, `newGateStub(status, body)` (settable status via `setStatus`). Reuse them.
- Read `docs/config.md` — the `## Schema` block (the provider block with the commented knobs, around lines 51–56) and the `## Cold-start admission gate` section (around line 256, immediately before `## Upstream pools`). Read `docs/config.example.yaml` — the `ollama-local` provider carries the commented throttle and cold-start blocks. Read `docs/metrics.md` — the `## Series` table and the prose paragraph after it that describes the `ccrouter_cold_*` series. Read `CHANGELOG.md` — it currently starts with `## v0.50.0` and has NO `## Unreleased` heading.
- Coding plugin docs (in-container paths): `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` (GaugeVec/CounterVec naming, `Register`, `testutil.ToFloat64`/`CollectAndCount`, pre-initializing a bounded label set), `go-factory-pattern.md` (`Create*` wiring lives in `pkg/factory/`), `go-testing-guide.md`, `go-doc-best-practices.md`, `changelog-guide.md`, `documentation-guide.md`.
</context>

<requirements>
1. **Two additive collectors in `pkg/handler/metrics.go`.** Add these two fields to the `Metrics` struct (after `ColdTTFTSeconds`) and construct them in `NewMetrics`; extend `Register`'s collector slice to include both; extend the `Metrics` struct's GoDoc to mention them:
   ```go
   	ColdPrefillBudgetTokens       *prometheus.GaugeVec   // ccrouter_cold_prefill_budget_tokens{provider}
   	BreakerStateTransitionsTotal  *prometheus.CounterVec // ccrouter_breaker_state_transitions_total{provider,state}
   ```
   - `prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "ccrouter_cold_prefill_budget_tokens", Help: "Current effective cold-prefill token budget the cold-start admission gate admits against, labeled by provider. Reads the configured ceiling until the TTFT backpressure loop adjusts it, then the halved or grown value (spec 020)."}, []string{"provider"})`.
   - `prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ccrouter_breaker_state_transitions_total", Help: "Number of circuit-breaker state transitions, labeled by provider and state (closed|open|half_open). The three state series are pre-initialized to 0 for a provider with the breaker enabled (spec 020)."}, []string{"provider", "state"})`.
   - Do NOT pre-initialize per-provider series in `NewMetrics` — the controller sets the budget gauge and the breaker pre-initializes its own three counter series. Do NOT modify `statusClass` or add a `status_class` value. Both collectors are additive only.
2. **Factory wiring in `pkg/factory/factory.go`.** In `CreateRouterFromConfig`'s per-provider loop, build the controller and wrap the provider handler in the documented order. The breaker must be OUTERMOST (outside the throttle gate and the cold gate); the cold gate stays inside the throttle gate (spec 020 Constraints: "the breaker wraps the provider handler outermost … so an open breaker short-circuits the whole provider; the backpressure controller is constructed alongside the cold-start gate and adjusts that gate's effective budget"). The existing loop already declares `providerHandler` at `pkg/factory/factory.go:293` and `prov := cfg.Providers[name]` above it — insert the `backpressure :=` construction before the existing `providerHandler :=` line, change the `coldGateHandler` call, and append the breaker wrap after the `NewThrottleGate` call; do not re-declare `providerHandler`.
   ```go
   		backpressure := handler.NewTTFTBackpressure(
   			name,
   			prov.ColdPrefillBudgetTokens,
   			prov.TtftBackpressureThresholdSeconds,
   			o.currentDateTime.Now,
   			metrics.ColdPrefillBudgetTokens,
   		)
   		providerHandler := handler.NewUpstreamPoolHandler(ctx, members)
   		providerHandler = coldGateHandler(
   			providerHandler, name, prov, backpressure, o.currentDateTime.Now, metrics,
   		)
   		// existing spec-018 throttle gate wraps the cold gate (unchanged)
   		providerHandler = handler.NewThrottleGate(providerHandler, name, ...)
   		// spec-020 breaker wraps everything, outermost
   		providerHandler = handler.NewCircuitBreaker(
   			providerHandler,
   			name,
   			prov.BreakerFailureThreshold,
   			o.currentDateTime.Now,
   			metrics.BreakerStateTransitionsTotal,
   		)
   		providerHandlers[name] = providerHandler
   ```
   - Extend the `coldGateHandler` helper's signature to take `backpressure *handler.TTFTBackpressure` and have it call `handler.NewColdStartGateWithBackpressure(...)` passing that controller; keep passing `0` for the max wait so the handler's own default stays the single source of truth, and keep passing `0`-resolved session window semantics.
   - Do NOT add a factory-side constant for the window, the open period, the growth step, or the floor — those are the handlers' own fixed internals. Do NOT add a separate wrap for `defaultHandler` (it reads the wrapped `providerHandlers[...]` value by construction). A provider with neither knob set gets `backpressure == nil` and the breaker's no-op path, so its tree is unchanged and no new series exists for it.
   - Update the per-provider loop comments: the cold-gate comment's "so the existing 429 delay gate stays outermost" and the throttle comment's "wraps the provider's pool handler" are stale once the breaker is outermost; rewrite them to state cold gate → throttle gate → breaker (outermost).
3. **Tests in `pkg/handler/cold-start-gate_backpressure_metrics_test.go`** (`package handler_test`, mirroring `cold-start-gate_metrics_test.go`). Build the real collector set with `handler.NewMetrics(nil, testDateTime)` registered on a fresh `prometheus.NewRegistry()`. Rows:
   - **AC 11 — the budget gauge tracks live state:** construct a controller with `handler.NewTTFTBackpressure("p", 65536, 20, clock.Now, m.ColdPrefillBudgetTokens)`; assert `testutil.ToFloat64(m.ColdPrefillBudgetTokens.WithLabelValues("p"))` equals `65536` at construction; drive one over-threshold window on the injected clock and assert it equals `32768`; drive one clean window and assert it equals `40960` — the value, not merely that the series exists.
   - **AC 12 — the breaker counter is pre-initialized and increments:** construct a breaker with `handler.NewCircuitBreaker(stub, "p", 5, clock.Now, m.BreakerStateTransitionsTotal)`; assert `state="closed"`, `state="open"`, and `state="half_open"` each read `0`; drive one closed→open transition; assert `state="open"` reads exactly `1`.
   - **AC 3 negative evidence for both new series:** with `threshold == 0` (no controller) and `failureThreshold == 0` (no breaker), `testutil.CollectAndCount(m.ColdPrefillBudgetTokens, "ccrouter_cold_prefill_budget_tokens")` is `0` and `testutil.CollectAndCount(m.BreakerStateTransitionsTotal, "ccrouter_breaker_state_transitions_total")` is `0`.
   Do NOT re-assert the `[backpressure]` / `[breaker]` log lines here — prompts 2 and 3 own those rows.
4. **Tests in `pkg/factory/backpressure_wiring_test.go`** (`package factory_test`, mirroring `cold_gate_wiring_test.go`). The upstream is an `httptest.Server` whose status the row can flip; build with `factory.CreateRouterFromConfig(ctx, cfg, factory.WithMetricsRegisterer(reg), factory.WithCurrentDateTime(clock))` using a FRESH `prometheus.NewRegistry()` per build. Rows:
   - **Gauge wired and reading the ceiling:** with `ColdPrefillBudgetTokens: 65536` and `TtftBackpressureThresholdSeconds: 20`, gather `reg.Gather()` and assert the `ccrouter_cold_prefill_budget_tokens` family carries a series for provider `"t"` reading `65536`.
   - **Breaker wired outermost:** with `BreakerFailureThreshold: 2` and the upstream answering `500`, two requests forward (status 500 passed through) and the third is answered `429` with `Retry-After: 30` while the upstream call count stops rising — proving the breaker sits outside the pool handler.
   - **Disabled is a no-op:** with neither knob set, `reg.Gather()` contains no `ccrouter_cold_prefill_budget_tokens` family and no `ccrouter_breaker_state_transitions_total` family.
   - **SIGHUP rebuild applies changed knobs with fresh state:** build handler1 with `BreakerFailureThreshold: 2` and trip it (third request 429); then build handler2 from a config with `BreakerFailureThreshold: 100` on a FRESH registry (exactly the reloader's rebuild — reusing the registry makes `metrics.Register` return `AlreadyRegisteredError` and the build fails) and assert a request now forwards and reaches the upstream. Add a second assertion proving the state did not carry over: build handler2 with `BreakerFailureThreshold: 2` (unchanged from handler1) and assert its first two requests forward and reach the upstream before the third opens the breaker — a carried-over open state would answer the first request 429.
5. **`docs/config.md`.** (a) Add the two knobs to the `## Schema` provider block, directly after the `newSessionRatePerMinute` line, so operators copying the schema see them; the comment on the `ttftBackpressureThresholdSeconds` line must itself contain the word `default` (e.g. `Absent or 0 or negative = disabled (the default).`). (b) Add a new `## TTFT backpressure and circuit breaker` section immediately after the `## Cold-start admission gate` section (i.e. before `## Upstream pools`). The section must link the existing cold-start section and state, for each knob, its meaning, its default, and its zero-value semantics:
   - `ttftBackpressureThresholdSeconds` — enables the backpressure loop; **Default: off** — absent, `0`, or negative disables it; it has an effect only when `coldPrefillBudgetTokens > 0` because it adjusts that budget; the recommended starting value is `20`.
   - `breakerFailureThreshold` — enables the circuit breaker; **Default: off** — absent, `0`, or negative disables it; independent of the cold-start gate; the recommended starting value is `5`.
   - Document the fixed internal constants as defaults and NOT knobs: the 60s observation window, the 8s clean-recovery median threshold, the 8192-token growth step, the floor divisor 16, the 30s breaker open period, and the single probe per open→half-open transition.
   - Document the behaviour: the budget halves on an over-threshold or failed window and grows on a clean one; the breaker answers 429 with an integer `Retry-After` equal to the open period and never a router-made 5xx; a non-5xx resets the failure count; both are provider-level only (a knob on an `upstreams:` member is silently ignored, like the throttle and cold knobs); validation is lenient; a SIGHUP reload resets the in-memory state; and the observability (`[backpressure] provider=<name> budget=<n>` and `[breaker] provider=<name> state=open|half-open|closed` INFO lines, plus the two series in `docs/metrics.md`).
6. **`docs/config.example.yaml`.** Add both knobs, commented out, on the `ollama-local` provider next to the existing cold-start block, each with a one-line comment naming its meaning and its zero-value semantics, and pointing at the new `docs/config.md` section.
7. **`docs/metrics.md`.** Add both series to the `## Series` table (`ccrouter_cold_prefill_budget_tokens` / `provider` / gauge; `ccrouter_breaker_state_transitions_total` / `provider`, `state` / counter) and add a prose paragraph describing them: the gauge reads the configured ceiling until the backpressure loop adjusts it, the counter's three `state` series are pre-initialized to 0 for a provider with the breaker enabled, the `state` label is bounded to `closed|open|half_open`, and both are additive — the `status_class` enum is unchanged and a breaker 429 still records through `4xx_rate_limited`.
8. **`CHANGELOG.md`.** The file currently has no `## Unreleased` heading (v0.50.0 folded it). Create the `## Unreleased` heading at the top of the version list (after the file's intro paragraph, before `## v0.50.0`) and add one `- feat: …` bullet under it that names both `ttftBackpressureThresholdSeconds` and `breakerFailureThreshold` and states what they do, in the style of the existing v0.50.0 entry. Also extend the `README.md` per-provider-knob sentence (the "Optional per-provider knobs tune admission and pacing …" line) to name `ttftBackpressureThresholdSeconds` / `breakerFailureThreshold` alongside the existing cold and throttle knobs — `docs/dod.md` requires README.md updated when configuration changes, and that sentence enumerates the knob set.
9. **Before finishing, re-run `<verification>` and confirm every command passes.** Walk requirements 1–8 against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Placement (spec 020 Constraints): the breaker wraps the provider handler outermost (outside the 429 delay gate and the cold-start gate); the backpressure controller is constructed alongside the cold-start gate and adjusts that gate's effective budget. The model router's body read, alias resolution, `[1m]` strip, key routing, and system-lift flow are untouched. Do NOT modify `pkg/handler/model-router.go`.
- No new Prometheus `status_class` value — a breaker 429 and a budget refusal both record through the existing `4xx_rate_limited` class; the two new series are additive (spec 020 Non-goals).
- Metrics: the `ccrouter_` prefix; the budget is a Gauge and the breaker transitions a Counter ending `_total`; the `state` label is bounded to `closed|open|half_open` and all three series are pre-initialized to 0 for an enabled provider; no high-cardinality label is added.
- Fixed internal constants (NOT knobs): the 60s window, the 8s clean median threshold, the 8192 growth step, the floor divisor 16, the 30s open period, and the single probe. The two knobs' recommended starting values are 20 (seconds) and 5 (failures). Do NOT add config fields, opt-out flags, or tunable thresholds beyond the two fields prompt 1 shipped.
- SIGHUP applies changed knobs without a restart: the reloader rebuilds the per-provider tree; both behaviours keep in-memory state, so a reload resets the budget and the breaker — reversible by design.
- Per-provider independence: no shared/global state; two providers sharing one upstream each bound their own cold work and their own breaker.
- The breaker's 429 reuses the existing `limiter429Body` constant and the existing `writeRateLimited` helper; the body must not carry the provider name, the upstream URL, the failure count, or the breaker state.
- Time comes from the injected `github.com/bborbe/time` clock, never `time.Now()` — tests must not sleep through a real window or open period.
- No new dependencies — the Go standard library plus the existing `bborbe/*` libraries suffice.
- No AI attribution in code or comments.
- `make precommit` must remain green. Follow `docs/dod.md` (GoDoc on every new exported identifier; Ginkgo/Gomega coverage ≥ 80% on new `pkg/<package>` code; `README.md`, `docs/config.md`, `docs/config.example.yaml`, `docs/metrics.md`, and `CHANGELOG.md` all updated).
</constraints>

<verification>
make precommit

# Two additive collectors + register loop:
grep -n 'ccrouter_cold_prefill_budget_tokens' pkg/handler/metrics.go
grep -n 'ccrouter_breaker_state_transitions_total' pkg/handler/metrics.go
grep -n 'ColdPrefillBudgetTokens\|BreakerStateTransitionsTotal' pkg/handler/metrics.go

# status_class enum untouched:
grep -c 'func statusClass' pkg/handler/metrics.go     # expect 1
grep -c '4xx_rate_limited' pkg/handler/metrics.go     # expect >= 1

# Factory wiring: controller built, cold gate takes it, breaker outermost:
grep -n 'handler.NewTTFTBackpressure' pkg/factory/factory.go
grep -n 'handler.NewColdStartGateWithBackpressure' pkg/factory/factory.go
grep -n 'handler.NewCircuitBreaker' pkg/factory/factory.go
grep -n 'metrics.ColdPrefillBudgetTokens\|metrics.BreakerStateTransitionsTotal' pkg/factory/factory.go

# Test rows exist:
grep -c 'ccrouter_cold_prefill_budget_tokens\|ccrouter_breaker_state_transitions_total' pkg/handler/cold-start-gate_backpressure_metrics_test.go   # expect >= 1
grep -c 'TtftBackpressureThresholdSeconds\|BreakerFailureThreshold' pkg/factory/backpressure_wiring_test.go   # expect >= 1

# Docs (spec 020 AC 14, 15, 16 evidence):
grep -c 'ttftBackpressureThresholdSeconds' docs/config.md          # expect >= 1
grep -c 'breakerFailureThreshold' docs/config.md                   # expect >= 1
sed -n '/ttftBackpressureThresholdSeconds/,+8p' docs/config.md | grep -ci 'default'   # expect >= 1
sed -n '/^## TTFT backpressure and circuit breaker/,/^## /p' docs/config.md | grep -ci 'default'   # expect >= 2
grep -c 'ttftBackpressureThresholdSeconds' docs/config.example.yaml   # expect >= 1
grep -c 'breakerFailureThreshold' docs/config.example.yaml            # expect >= 1
grep -c 'ccrouter_cold_prefill_budget_tokens' docs/metrics.md      # expect >= 1
grep -c 'ccrouter_breaker_state_transitions_total' docs/metrics.md # expect >= 1
sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c ttftBackpressureThresholdSeconds   # expect >= 1
sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c breakerFailureThreshold            # expect >= 1
grep -c 'ttftBackpressureThresholdSeconds' README.md                 # expect >= 1
grep -c 'breakerFailureThreshold' README.md                          # expect >= 1

# Repo-wide reference sweep (docs/dod.md requires this when config fields change) — every hit must be an intended surface:
grep -rn 'ttftBackpressureThresholdSeconds\|breakerFailureThreshold' --include='*.md' --include='*.yaml' . | grep -v '_test'

# Suites:
go test -count=1 ./pkg/...
</verification>

<!--
OPEN QUESTION (resolved best-effort — flag at audit): the spec assigns the log-line AC (13) to this
prompt's bucket, but the lines are emitted by the controller (prompt 2) and the breaker (prompt 3) at
their change sites, so their rows live in those prompts' test files and are NOT duplicated here (a
duplicate row would be sibling-test duplication). This prompt's rows cover the two collectors and the
factory wiring. Confirm at audit that splitting AC 13 across prompts 2 and 3 is acceptable.

OPEN QUESTION: the wiring row that proves the breaker is outermost uses a 500-returning upstream and
asserts the third request is 429 with Retry-After 30. A row that drove a real window boundary through
the HTTP path would need the injected clock advanced mid-request, which the wiring harness does not
support; the budget-gauge halving is therefore asserted at the handler level (requirement 3) and the
wiring row asserts only the construction-time ceiling. Confirm at audit.
-->

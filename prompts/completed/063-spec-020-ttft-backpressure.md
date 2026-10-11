---
status: completed
spec: [020-ttft-backpressure-and-circuit-breaker]
summary: Added the per-provider TTFT backpressure controller in pkg/handler/ttft-backpressure.go and wired the cold-start gate to admit against its live effective budget via NewColdStartGateWithBackpressure, with full Ginkgo/Gomega coverage driving the injected clock.
execution_id: claude-code-router-backpressure-exec-063-spec-020-ttft-backpressure
dark-factory-version: v0.196.0
created: "2026-10-11T09:12:32Z"
queued: "2026-10-11T10:05:40Z"
started: "2026-10-11T10:20:38Z"
completed: "2026-10-11T10:42:10Z"
---

# TTFT backpressure: windowed controller + live cold-prefill budget

<summary>
- A provider with a TTFT threshold set watches its own measured cold time-to-first-token over fixed one-minute windows.
- When a window's median cold time-to-first-token rises above the configured threshold, or any request in the window fails upstream, the provider's cold-prefill budget halves — once per window, never below one sixteenth of the configured ceiling.
- When a window is clean — median below the fixed eight-second line and no failure — the budget grows back by a fixed step, never above the configured ceiling.
- A window whose median sits between the clean line and the threshold holds the budget steady, so the loop does not oscillate around the threshold.
- The cold-start gate now admits against that live budget, so a halving takes effect on the next cold request rather than only after a restart.
- The window's failure signal is the upstream response status: a 5xx counts, anything below 500 does not, and a client that disconnects is never a failure.
- Each change of the effective budget prints one INFO line naming the provider and the new budget.
- With the threshold unset, zero, or negative — or with no cold-prefill budget configured — nothing changes: the gate keeps using the ceiling it always used, and no line is printed.
- Two providers keep separate budgets; halving one never touches the other, even when they share one upstream.
</summary>

<objective>
Build the per-provider TTFT backpressure controller in `pkg/handler/ttft-backpressure.go` and make the cold-start admission gate admit against its live effective budget, so a provider whose backend slows down is fed less cold work instead of more — with the window, the halving, the growth step, the floor, and the hysteresis band all driven by the router's injected clock so tests never sleep through a real window.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only. Single-module Go repo. `make test` / `make precommit` are root targets; this repo's `Makefile` does NOT use `ROOTDIR`/`default.env`, so no override is needed. `.git` may be masked in other repos; never rely on a bare `git` command.
- Read `pkg/handler/cold-start-gate.go` in full — this prompt modifies it. Note `NewColdStartGate` (its no-op-when-disabled guard `if budgetTokens <= 0 && newSessionsPerMinute <= 0 { return next }`), the `coldStartGate` struct (all mutable state under one `sync.Mutex`), `tryAdmitLocked` (the budget check `if g.budget > 0 && g.inFlightTokens > 0 && g.inFlightTokens+estimate > g.budget`), `ServeHTTP` (the warm / zero-estimate fast path `g.next.ServeHTTP(w, r)`), and `forwardCold` (the `coldReleaseRecorder` + `sync.OnceFunc` release, whose `onDelta` already observes `g.metrics.TTFT`).
- Read `pkg/handler/throttle-gate.go` — the sibling per-provider controller whose shape this mirrors: the constructor takes the injected clock as `now func() libtime.DateTime` and falls back to `realNow` when nil, all mutable state lives under one `sync.Mutex`, and the window is a fixed 60s internal constant (`throttleObservationWindow`) that is NOT a config knob.
- Read `pkg/handler/upstream-pool-handler.go` — `var realNow = func() libtime.DateTime { return libtime.DateTime(stdtime.Now()) }` (same `handler` package; reuse it, do NOT define a second fallback clock).
- Read `pkg/handler/status-recorder.go` — `statusRecorder` wraps a `ResponseWriter`, records the status on `WriteHeader`/`Write`, and exposes `Unwrap() http.ResponseWriter` so SSE flushing keeps working. Reuse it to observe the response status; do NOT write a second status recorder.
- Read `pkg/handler/export_test.go` and `pkg/handler/throttle-gate_test.go` — `t0` (a fixed `libtime.DateTime`), `newClock() libtime.CurrentDateTime` (pinned at `t0`, with `SetNow`), and the `ThrottleGateObserve` export seam precedent (rows drive the detector directly on the injected clock so nothing sleeps wall-clock).
- Read `pkg/handler/cold-start-gate_test.go` — `coldBody` (700 → estimate 200), `coldDelta`, `coldStartEvent`, `newColdStub(sse string)` (records entries, writes SSE, optionally blocks on a channel while selecting on `r.Context().Done()`), `sizedRequest(sessionID, body string)`, `bodyRequest(body string)`, and `coldInFlight(h http.Handler) int`. Reuse them; do NOT redefine them.
- Read `pkg/handler/model-router_test.go` — `captureStderr(fn func()) string` (pair with `flag.Set("logtostderr", "true")`); reuse it for the log row.
- Read `pkg/config.go` — `Provider.TtftBackpressureThresholdSeconds` and `Provider.ColdPrefillBudgetTokens` (added by prompt 1; this prompt does NOT touch `pkg/config.go`, and the controller receives both as plain `int` parameters).
- Read `docs/config.md` § `## Cold-start admission gate` — the project's statement of the gate's current semantics; this prompt rewrites that gate's admission path (the live effective budget replaces the construction-time ceiling), so the section is the baseline to compare the change against.
- Coding plugin docs (in-container paths): `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` (mutex-guarded shared state; raw `go func()` is exempt in `*_test.go`), `go-testing-guide.md` (external `_test` package, Ginkgo v2 + Gomega), `go-time-injection.md` (injected clock, never `time.Now()`), `go-doc-best-practices.md`, `go-patterns.md`, `go-logging-guide.md`.
</context>

<requirements>
1. **New file `pkg/handler/ttft-backpressure.go`** (package `handler`, standard copyright header, complete GoDoc). Define exactly these fixed constants — each an internal constant, NOT a config knob (spec 020 Non-goals: "No operator-tunable 60s observation window, clean-recovery threshold, growth step, budget floor, breaker open period, or probe count"):
   ```go
   const (
   	// backpressureWindow is the fixed observation window over which the
   	// controller evaluates a provider's cold time-to-first-token and its
   	// upstream failures. Fixed internal constant, not a config knob.
   	backpressureWindow = 60 * stdtime.Second
   	// backpressureCleanMedianSeconds is the fixed median cold-TTFT (in
   	// seconds) below which a failure-free window is clean and grows the
   	// budget. A window whose median sits between this and the configured
   	// threshold holds the budget unchanged, so the loop does not oscillate.
   	backpressureCleanMedianSeconds = 8.0
   	// backpressureGrowthStep is the fixed token step added to the effective
   	// budget on a clean window.
   	backpressureGrowthStep = 8192
   	// backpressureFloorDivisor is the fixed divisor of the configured
   	// ceiling that yields the budget floor (ceiling/16).
   	backpressureFloorDivisor = 16
   )
   ```

2. **`TTFTBackpressure` struct + constructor.** The controller is per provider; a nil `*TTFTBackpressure` is valid and inert (every method nil-safe), so a disabled provider needs no branch at the call site.
   ```go
   // TTFTBackpressure is the per-provider backpressure controller (spec 020).
   // ... full GoDoc: what it tracks, the halve/grow/hold rule, the floor, the
   // ceiling, that the cold gate reads Budget() per admission, that a nil
   // value is inert, and that all mutable state is under mu. ...
   type TTFTBackpressure struct {
   	provider  string
   	ceiling   int
   	threshold float64
   	floor     int
   	now       func() libtime.DateTime
   	gauge     *prometheus.GaugeVec

   	mu          sync.Mutex
   	budget      int
   	started     bool
   	windowIndex int64
   	samples     []float64
   	failed      bool
   }

   func NewTTFTBackpressure(
   	provider string,
   	ceilingTokens int,
   	thresholdSeconds int,
   	now func() libtime.DateTime,
   	budgetGauge *prometheus.GaugeVec,
   ) *TTFTBackpressure
   ```
   - **Disabled returns nil:** `if thresholdSeconds <= 0 || ceilingTokens <= 0 { return nil }`. The feature is inert with no cold budget configured (spec 020 Constraints: "it has an effect only when the cold-start gate is enabled (`coldPrefillBudgetTokens > 0`)").
   - `now == nil` falls back to `realNow`.
   - `floor := ceilingTokens / backpressureFloorDivisor`; if that is `< 1` set it to `1` — a zero budget would make `tryAdmitLocked`'s `g.budget > 0` guard false and silently disable the admission check entirely, so the floor must never reach zero.
   - `budget` starts at `ceilingTokens`. When `budgetGauge != nil`, set `budgetGauge.WithLabelValues(provider).Set(float64(ceilingTokens))` immediately (AC 11: "reads the configured ceiling at construction").

3. **`Budget()` accessor** — nil receiver returns `0`:
   ```go
   // Budget returns the live effective cold-prefill budget this provider
   // admits against — the configured ceiling until the first adjustment, then
   // the halved or grown value. A nil controller returns 0.
   func (b *TTFTBackpressure) Budget() int
   ```
   Take `b.mu` for the read.

4. **Observations.** Both are nil-safe (`if b == nil { return }`) and both take `b.mu`, read `at := b.now().Time()`, call `rollLocked(at)`, then record:
   - `func (b *TTFTBackpressure) ObserveColdTTFT(seconds float64)` — append `seconds` to `samples`.
   - `func (b *TTFTBackpressure) ObserveStatus(status int)` — `if status >= 500 { b.failed = true }`. A status below 500 is NOT a failure (spec 020: "a 5xx counts, anything below 500 does not"). A client disconnect never arrives as a 5xx, so it is never counted. The status observed is the upstream response status at this wrapper's boundary; a `<500` status — including the router's own 429 — is never a failure.

5. **Window roll and evaluation** (spec 020 DB 1–3). Two unexported helpers, both called with `b.mu` held:
   - `rollLocked(at stdtime.Time)`: compute the current window index `w := at.Unix() / int64(backpressureWindow/stdtime.Second)`; if `b.started && w != b.windowIndex` then call `evaluateLocked()`, clear `samples` (`b.samples = b.samples[:0]`) and `failed`, then set `b.started = true` and `b.windowIndex = w`. The observation that crosses the boundary belongs to the NEW window, so it is recorded after the roll. At most one adjustment per window follows from evaluating exactly once per index change. A window whose only traffic was warm still rolls (that is why `forwardObserved` exists, so a warm request's status reaches the window); a clock jump across N windows performs one evaluation, not N; and this wall-clock-aligned window deliberately diverges from the sibling `throttle-gate.go`'s rolling window.
   - `evaluateLocked()`:
     ```go
     median := backpressureMedian(b.samples)
     next := b.budget
     switch {
     case b.failed || median > b.threshold:
         next = b.budget / 2
         if next < b.floor { next = b.floor }
     case median < backpressureCleanMedianSeconds:
         next = b.budget + backpressureGrowthStep
         if next > b.ceiling { next = b.ceiling }
     }
     ```
     When `next != b.budget`: assign `b.budget = next`, publish the gauge (`b.gauge.WithLabelValues(b.provider).Set(float64(b.budget))` when non-nil), and emit exactly one INFO line `glog.Infof("[backpressure] provider=%s budget=%d", b.provider, b.budget)`. When `next == b.budget` do nothing — no log, no gauge write (AC 13: "each effective-budget change emits one INFO line").
   - `backpressureMedian(samples []float64) float64`: `0` for an empty slice (an empty failure-free window is clean and grows — spec 020 Constraints); otherwise copy, `sort.Float64s`, and return the middle value for an odd count or the arithmetic mean of the two central values for an even count (spec 020 Constraints: "The median of a window with an even number of cold TTFT samples is the arithmetic mean of the two central samples").
   - This makes the hysteresis band fall out of the switch: a median between `backpressureCleanMedianSeconds` and the threshold, with no failure, matches neither case and holds the budget.

6. **`pkg/handler/cold-start-gate.go` — live effective budget.** Keep `NewColdStartGate`'s exported signature and its behaviour byte-for-byte unchanged; it becomes a thin delegate:
   ```go
   func NewColdStartGate(
   	next http.Handler,
   	provider string,
   	budgetTokens int,
   	sessionWindow stdtime.Duration,
   	newSessionsPerMinute int,
   	maxWait stdtime.Duration,
   	now func() libtime.DateTime,
   	metrics ColdGateMetrics,
   ) http.Handler {
   	return NewColdStartGateWithBackpressure(
   		next, provider, budgetTokens, nil, sessionWindow, newSessionsPerMinute, maxWait, now, metrics,
   	)
   }
   ```
   Add the controller-aware constructor (same body as today's, plus the controller):
   ```go
   // NewColdStartGateWithBackpressure is NewColdStartGate with a TTFT
   // backpressure controller attached (spec 020): the gate admits against
   // backpressure.Budget() (the live effective budget) instead of the value
   // fixed at construction, and feeds the controller each forwarded request's
   // response status and each cold request's time-to-first-token. A nil
   // backpressure makes this identical to NewColdStartGate.
   func NewColdStartGateWithBackpressure(
   	next http.Handler,
   	provider string,
   	budgetTokens int,
   	backpressure *TTFTBackpressure,
   	sessionWindow stdtime.Duration,
   	newSessionsPerMinute int,
   	maxWait stdtime.Duration,
   	now func() libtime.DateTime,
   	metrics ColdGateMetrics,
   ) http.Handler
   ```
   - Add `bp *TTFTBackpressure` to the `coldStartGate` struct and set it from the parameter. Everything else in the constructor is unchanged, including the disabled guard `if budgetTokens <= 0 && newSessionsPerMinute <= 0 { return next }`.
   - Add the accessor and use it in `tryAdmitLocked` in place of the raw `g.budget`:
     ```go
     // effectiveBudget returns the cold-prefill budget the gate admits against
     // right now: the controller's live effective budget when one is attached,
     // else the ceiling configured at construction. Callers hold g.mu; the
     // controller takes its own mutex inside and never takes g.mu, so the lock
     // order g.mu -> bp.mu is acyclic.
     func (g *coldStartGate) effectiveBudget() int {
     	if g.bp != nil {
     		return g.bp.Budget()
     	}
     	return g.budget
     }
     ```
     In `tryAdmitLocked` replace the budget comparison with `if budget := g.effectiveBudget(); budget > 0 && g.inFlightTokens > 0 && g.inFlightTokens+estimate > budget { return false, false, "budget" }`. Do NOT otherwise change the admission, queue, rate, prune, or release logic.
   - **Warm / zero-estimate path.** In `ServeHTTP` replace the bare `g.next.ServeHTTP(w, r)` on the warm-or-zero-estimate branch with a call to a new helper:
     ```go
     // forwardObserved forwards a request carrying no cold reservation — a warm
     // session or a zero-estimate body. With a controller attached it observes
     // the response status so a warm request's failure still counts toward the
     // provider's window; without one it calls next directly, byte-for-byte the
     // pre-spec-020 path.
     func (g *coldStartGate) forwardObserved(w http.ResponseWriter, r *http.Request) {
     	if g.bp == nil {
     		g.next.ServeHTTP(w, r)
     		return
     	}
     	sr := &statusRecorder{ResponseWriter: w}
     	g.next.ServeHTTP(sr, r)
     	g.bp.ObserveStatus(sr.status)
     }
     ```
   - **Cold path.** In `forwardCold`, when `g.bp != nil`, wrap `w` in the existing `statusRecorder` before building the `coldReleaseRecorder` (the `coldReleaseRecorder` then wraps the status recorder, which captures the status via the promoted `WriteHeader`), call `g.bp.ObserveStatus(sr.status)` after `g.next.ServeHTTP(rec, r)`, and in `onDelta` also call `g.bp.ObserveColdTTFT(seconds)` with the same `seconds` value the TTFT histogram already observes. When `g.bp == nil` the path is byte-for-byte today's. Do NOT change the release semantics, the `Unwrap` chain, or the metrics observation.
7. **Tests in `pkg/handler/ttft-backpressure_test.go`** (`package handler_test`, Ginkgo v2 + Gomega). Reuse `t0`, `newClock()`, `newColdStub`, `sizedRequest`, `coldBody`, `coldDelta`, `coldInFlight`, `captureStderr`, `serveAsync`. Build a fresh clock per row (`clock := newClock()`) and advance it with `clock.SetNow(t0.Add(<duration>))` — never sleep through a real window. Rows:
   - **AC 4 — median halving, once per window:** controller `NewTTFTBackpressure("p", 65536, 20, clock.Now, nil)`. At `t0` observe three cold samples of `25`; assert `Budget() == 65536` (no boundary crossed yet). Advance the clock 61s and observe one more sample; assert `Budget() == 32768` — halved exactly once, not twice, for the window that held three over-threshold samples.
   - **AC 4 — failure halving with no cold samples:** a window in which `ObserveStatus(500)` was called and no cold sample arrived halves on the next window's first observation (`Budget() == 32768`).
   - **AC 5 — growth and cap:** from the halved `32768`, drive consecutive clean windows (samples of `1.0`, no failure) one window at a time and assert the budget steps `32768 → 40960 → 49152 → … → 65536` and never exceeds the configured ceiling.
   - **AC 5 — hysteresis holds:** first drive one over-threshold window so the budget is `32768` (below the ceiling, so a hold is distinguishable from a capped growth), then drive a window with median `15` (between the fixed 8s line and the threshold 20) and no failure; assert `Budget()` is still exactly `32768`.
   - **AC 5 — empty clean window grows:** from a halved budget, establish a window whose only observation is a non-failing `ObserveStatus(200)` (no cold sample), then observe in the next window; assert the budget grows by `backpressureGrowthStep`.
   - **AC 6 — floor:** drive more consecutive over-threshold windows than halvings are available; assert `Budget() == 65536/16` (`4096`) and never less, and that a tiny ceiling (`ceilingTokens: 10`) still yields a floor of at least `1`.
   - **AC 13 — log line:** `flag.Set("logtostderr", "true")`; inside `captureStderr`, drive one halving window and one growth window; assert the captured output contains `[backpressure] provider=p budget=32768` and `[backpressure] provider=p budget=40960`, and that a hold (hysteresis) window adds no further `[backpressure]` line.
   - **AC 11 — gauge publishes the effective budget:** construct a test-local `prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_backpressure_budget", Help: "test"}, []string{"provider"})` and pass it as the controller's `budgetGauge`; assert the `provider="p"` gauge reads the configured ceiling (`65536`) after construction, then the halved value (`32768`) after a halving window, and then the grown value (`40960`) after a clean window.
   - **AC 3 — disabled is inert:** `NewTTFTBackpressure("p", 65536, 0, clock.Now, nil)` and `NewTTFTBackpressure("p", 0, 20, clock.Now, nil)` and a negative threshold each return `nil`; calling `ObserveColdTTFT` / `ObserveStatus` on a nil controller does not panic and `Budget()` returns `0`.
   - **AC 4 — live budget in the gate:** build the gate with `NewColdStartGateWithBackpressure(inner, "p", 65536, bp, stdtime.Minute, 0, 100*stdtime.Millisecond, clock.Now, handler.ColdGateMetrics{})` over a blocking `newColdStub`, where `bp` is a controller with ceiling 65536 and threshold 20. Hold ONE small cold request in flight first (`sizedRequest` with `coldBody` → estimate 200, started via `serveAsync` and awaited on the stub's entry count) — without a prior reservation the gate's lone-request fast path admits any estimate and the assertion is vacuous. Then send a second cold request whose estimate is between 32768 and 65536 (e.g. a ~175000-byte body → estimate 50000): with the ceiling in force it is admitted (upstream entry count rises to 2); after driving a halving window the same request is refused (the 100 ms max wait elapses, `coldInFlight` stays at 50200 — the held 200 plus the admitted request's 50000 — and the entry count stays 2), proving the gate reads the live value and not one frozen at construction.
   - **AC 10 — budget independence:** two controllers A and B with the same ceiling; halve A and assert `B.Budget()` is unchanged, and that a gate wired to B still admits a cold request that A's gate now refuses.
   - **Concurrency:** run N (e.g. 8) goroutines per controller, each calling `ObserveColdTTFT` and `ObserveStatus` across two window boundaries while another goroutine polls `Budget()`; assert no panic and that the final budget is one of the legal halve/grow/hold values (run under `-race`, per the spec's "every read-modify-write must be under a mutex").
   - **Status observation through the gate:** a cold request whose upstream stub answers `500` marks the window failed, so the next window halves; and a request whose context is cancelled while in flight is NOT counted as a failure (the following window grows instead).
   - **Warm / zero-estimate path (`forwardObserved`):** with a controller attached, a WARM request (same session id sent twice) whose upstream answers `500` marks the window failed, so the next window halves — proving the new `forwardObserved` observes status on the no-reservation path. Repeat with a zero-estimate cold request (`bodyRequest` with no body) and assert the same. Also assert that with `bp == nil` a warm request still forwards with the response unchanged.
   - **5xx status boundary:** drive a window in which `ObserveStatus(504)` is called (and, separately, `502`) with no cold sample; assert the next window halves, so the spec's "upstream hangs and the proxy answers 504" failure mode is traversed and not only the `500` boundary.
8. **Before finishing, re-run `<verification>` and confirm every command passes.** Walk requirements 1–7 against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Keying and placement (spec 020 Constraints): the controller is per provider and is constructed alongside the cold-start gate; the gate admits against the live effective budget. The model router's body read, alias resolution, `[1m]` strip, key routing, and system-lift flow are untouched. Do NOT modify `pkg/handler/model-router.go`, `pkg/factory/factory.go`, `pkg/config.go`, `pkg/handler/metrics.go`, `pkg/reloader/`, `docs/`, `README.md`, or `CHANGELOG.md` — later prompts own the breaker, the metrics collectors, the factory wiring, and the documentation.
- `NewColdStartGate`'s exported signature and behaviour must stay byte-for-byte identical — existing callers and tests must compile and pass unchanged.
- Fixed internal constants (NOT knobs): the 60s window, the 8s clean-recovery median threshold, the 8192-token growth step, the floor divisor 16, and the 500 failure boundary. Do NOT add config knobs, opt-out flags, or tunable thresholds. Do NOT add a Prometheus metric or a new collector in this prompt — the controller receives an optional `*prometheus.GaugeVec` handle and sets it; the collector itself is created by a later prompt.
- Time comes from the injected `github.com/bborbe/time` clock, never `time.Now()`. Tests must not sleep through a real window.
- Concurrency: the budget, the window samples, and the failure flag are mutated from concurrent request goroutines — every read-modify-write is under `b.mu`; the tests must exercise concurrent requests. No raw `go func()` outside `*_test.go` — use `github.com/bborbe/run` for any background work (none is expected here).
- Logging uses `glog.Infof(...)` (always-on INFO, matching `[throttle]` / `[coldgate]`) — never `fmt.Printf` / `println`. The log line carries only the provider name and the budget; never a session id, body, or other client-controlled value.
- Errors go through `github.com/bborbe/errors`; no bare `return err`.
- No new dependencies — the Go standard library plus the existing `bborbe/*` libraries suffice.
- No AI attribution in code or comments.
- `make precommit` must remain green. Follow `docs/dod.md` (GoDoc on every new exported identifier; Ginkgo/Gomega coverage ≥ 80% on new `pkg/<package>` code).
</constraints>

<verification>
make precommit

# Controller file + constructor + accessor:
grep -n 'func NewTTFTBackpressure' pkg/handler/ttft-backpressure.go
grep -n 'func (b \*TTFTBackpressure) Budget' pkg/handler/ttft-backpressure.go
grep -n 'func (b \*TTFTBackpressure) ObserveColdTTFT\|func (b \*TTFTBackpressure) ObserveStatus' pkg/handler/ttft-backpressure.go
grep -n 'backpressureWindow\|backpressureCleanMedianSeconds\|backpressureGrowthStep\|backpressureFloorDivisor' pkg/handler/ttft-backpressure.go

# The log line and the gauge write happen only on a change:
grep -n '\[backpressure\] provider=%s budget=%d' pkg/handler/ttft-backpressure.go

# Live budget wired into the gate:
grep -n 'func NewColdStartGateWithBackpressure' pkg/handler/cold-start-gate.go
grep -n 'func (g \*coldStartGate) effectiveBudget' pkg/handler/cold-start-gate.go
grep -n 'g.effectiveBudget()' pkg/handler/cold-start-gate.go
grep -n 'func (g \*coldStartGate) forwardObserved' pkg/handler/cold-start-gate.go

# Old constructor preserved (still exactly one exported no-backpressure form):
grep -c 'func NewColdStartGate(' pkg/handler/cold-start-gate.go   # expect 1
grep -c 'func NewColdStartGateWithBackpressure(' pkg/handler/cold-start-gate.go   # expect 1

# No new metrics collector added in this prompt:
! grep -n 'ccrouter_' pkg/handler/ttft-backpressure.go

# No wall-clock reads:
! grep -nE 'time\.Now\(\)' pkg/handler/ttft-backpressure.go | grep -v '^[[:space:]]*//'

# Test rows exist:
grep -c 'NewTTFTBackpressure\|NewColdStartGateWithBackpressure' pkg/handler/ttft-backpressure_test.go   # expect >= 1
grep -c 'AC 3\|AC 4\|AC 5\|AC 6\|AC 10\|AC 13' pkg/handler/ttft-backpressure_test.go   # expect >= 7

# Handler suite (race detector on, exercising the concurrency row):
ENABLE_RACE=true go test -count=1 ./pkg/handler/
</verification>

<!--
AUDIT RESOLUTION (1) — per-gate status observation is CONFIRMED acceptable: the cold-start gate
observes the response status at its own boundary and feeds the controller, which keeps the controller
self-contained and lets prompt 3 (the breaker) observe status at its own outer boundary independently.
Both react to the same signal (a 5xx response at the provider boundary); one shared status observer is
not required.

AUDIT RESOLUTION (2) — the wall-clock-aligned window with lazy evaluation is CONFIRMED intended: a
window whose only traffic was warm still rolls (that is why `forwardObserved` exists), a clock jump
across N windows performs one evaluation and not N (the roll evaluates exactly once per index
change), and the aligned window deliberately diverges from the sibling `throttle-gate.go`'s rolling
window. A window with no observation at all is never evaluated (no samples, no failure), so a fully
idle provider leaves its budget untouched.
-->

---
status: approved
spec: [020-ttft-backpressure-and-circuit-breaker]
created: "2026-10-11T09:12:32Z"
queued: "2026-10-11T10:05:40Z"
---

# Circuit breaker: consecutive-failure gate with open / half-open / closed

<summary>
- A provider with a failure threshold set counts consecutive upstream failures and stops sending once the count reaches it.
- While the breaker is open, every request is answered with the same 429 the router already uses for its other rate limits, carrying a `Retry-After` for the fixed open period, and the upstream is never called.
- The router never answers a client with a 5xx of its own making — an open breaker produces a 429, never a 500.
- Once the open period elapses exactly one request is let through as a probe while concurrent requests are still answered 429.
- A probe that succeeds closes the breaker and resets the failure count so normal service resumes immediately; a probe that fails reopens it for a further open period.
- A response below 500 resets the consecutive-failure count, and a client that disconnects is never counted as a failure.
- Every state change prints one INFO line naming the provider and the new state, and each transition is counted per provider and state.
- With the threshold unset, zero, or negative the breaker is not built at all: the wrapped handler is returned unchanged and nothing is counted.
</summary>

<objective>
Build the per-provider circuit breaker in `pkg/handler/circuit-breaker.go` — a consecutive-upstream-failure gate that opens after the configured threshold, answers 429 with a `Retry-After` for a fixed open period, admits exactly one half-open probe, and closes on a healthy probe — so a provider whose backend is failing stops being fed work instead of amplifying the failure.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only. Single-module Go repo. `make test` / `make precommit` are root targets; this repo's `Makefile` does NOT use `ROOTDIR`/`default.env`, so no override is needed. `.git` may be masked in other repos; never rely on a bare `git` command.
- Read `pkg/handler/concurrency-limiter.go` — the exact 429 shape to reuse: the `limiter429Body` constant and the `writeRateLimited(w http.ResponseWriter, retryAfterSeconds int)` helper. Do NOT introduce a second 429 body or a second header-write path (spec 020 Constraints: "The breaker's 429 reuses the existing `limiter429Body` constant and the existing `writeRateLimited` helper … do NOT introduce a second 429 body or a second header-write path").
- Read `pkg/handler/throttle-gate.go` — the sibling per-provider gate whose shape this mirrors: the no-op-when-disabled constructor (`if threshold <= 0 { return next }`), the injected clock `now func() libtime.DateTime` with a `realNow` fallback, all mutable state under one `sync.Mutex`, and always-on INFO `[throttle] provider=%s state=…` lines.
- Read `pkg/handler/cold-start-gate.go` — the sibling gate that also returns the inner handler unchanged when disabled, and whose `refuse` method calls `writeRateLimited(w, g.retryAfterSeconds())`.
- Read `pkg/handler/status-recorder.go` — `statusRecorder` wraps a `ResponseWriter`, records the status on `WriteHeader`/`Write`, and exposes `Unwrap() http.ResponseWriter` so SSE flushing keeps working. Reuse it to observe the forwarded response status; do NOT write a second status recorder.
- Read `pkg/handler/upstream-pool-handler.go` — `var realNow = func() libtime.DateTime { return libtime.DateTime(stdtime.Now()) }` (same `handler` package; reuse it, do NOT define a second fallback clock).
- Read `pkg/handler/throttle-gate_test.go` — `t0` (a fixed `libtime.DateTime`), `newClock() libtime.CurrentDateTime` (pinned at `t0`, with `SetNow`), `newGateStub(status int, body string) *gateStub` (records each call in an atomic counter, returns a settable status via `setStatus`, optionally blocks), and the `ThrottleGateObserve` export seam precedent. Reuse them; do NOT redefine them.
- Read `pkg/handler/concurrency-limiter_test.go` — `expected429Body` (the exact 429 body to assert), `serveAsync`, `newMessagesRequest`. Reuse them.
- Read `pkg/handler/model-router_test.go` — `captureStderr(fn func()) string` (pair with `flag.Set("logtostderr", "true")`); reuse it for the log rows.
- Read `pkg/handler/metrics.go` — the collector conventions: the `ccrouter_` prefix, `CounterVec` with a bounded label set, and the "pre-initialize the bounded series to 0" precedent in `NewMetrics` (the aliases loop). The breaker receives the counter as a parameter; the collector itself is created by a later prompt.
- Read `pkg/config.go` — `Provider.BreakerFailureThreshold` (added by prompt 1; this prompt does NOT touch `pkg/config.go`, and the breaker receives the threshold as a plain `int`).
- Coding plugin docs (in-container paths): `/home/node/.claude/plugins/marketplaces/coding/docs/go-state-machine-pattern.md` (bounded state transitions), `go-concurrency-patterns.md` (mutex-guarded shared state; raw `go func()` is exempt in `*_test.go`), `go-testing-guide.md` (external `_test` package, Ginkgo v2 + Gomega), `go-time-injection.md`, `go-prometheus-metrics-guide.md` (CounterVec, `testutil.ToFloat64`), `go-doc-best-practices.md`, `go-logging-guide.md`.
</context>

<requirements>
1. **New file `pkg/handler/circuit-breaker.go`** (package `handler`, standard copyright header, complete GoDoc). Define the fixed constants, the state enum, and the bounded metric-label set:
   ```go
   const (
   	// breakerOpenPeriod is the fixed duration the breaker stays open before
   	// admitting a single probe. Fixed internal constant, not a config knob.
   	breakerOpenPeriod = 30 * stdtime.Second
   	// breakerOpenPeriodSeconds is the integer Retry-After header value sent
   	// while the breaker is open (the open period in whole seconds).
   	breakerOpenPeriodSeconds = int(breakerOpenPeriod / stdtime.Second)
   )

   // breakerState is the circuit breaker's state.
   type breakerState int

   const (
   	// breakerClosed forwards every request and counts consecutive failures.
   	breakerClosed breakerState = iota
   	// breakerOpen refuses every request for the fixed open period.
   	breakerOpen
   	// breakerHalfOpen admits exactly one probe and refuses the rest.
   	breakerHalfOpen
   )

   // breakerStateLabels are the bounded state metric label values, in order.
   // The metric label uses an underscore (half_open) while the log line uses a
   // hyphen (half-open).
   var breakerStateLabels = []string{"closed", "open", "half_open"}
   ```

2. **Constructor** — the disabled path returns the inner handler itself, byte-for-byte (spec AC 3):
   ```go
   // NewCircuitBreaker returns a handler that, when failureThreshold > 0,
   // counts consecutive upstream failures for the wrapped provider and opens
   // the breaker once the count reaches the threshold (spec 020): while open,
   // every request is answered HTTP 429 with the existing static
   // Anthropic-shaped rate_limit_error body and an integer Retry-After header
   // equal to the fixed open period, and the upstream is never invoked. The
   // router never answers a client with a 5xx of its own making. When the open
   // period elapses the breaker admits exactly one probe request while
   // concurrent requests are answered 429; a probe whose response is not a
   // failure closes the breaker and resets the count, and a failing probe
   // reopens it for a further open period. A non-5xx response resets the
   // consecutive-failure count. When failureThreshold <= 0 the wrapper is a
   // no-op and returns next unchanged — the request path is byte-for-byte
   // identical to a release without the breaker (feature-off default).
   //
   // now is the router's injected clock and falls back to the real clock when
   // nil. transitions, when non-nil, counts state transitions labeled by
   // provider and state, with its three series pre-initialized to 0.
   func NewCircuitBreaker(
   	next http.Handler,
   	provider string,
   	failureThreshold int,
   	now func() libtime.DateTime,
   	transitions *prometheus.CounterVec,
   ) http.Handler
   ```
   - `if failureThreshold <= 0 { return next }`.
   - `if now == nil { now = realNow }`.
   - When `transitions != nil`, pre-initialize the three bounded series so `rate(...) > 0` alerts evaluate to 0 rather than no-data: `for _, s := range breakerStateLabels { transitions.WithLabelValues(provider, s).Add(0) }`.

3. **`circuitBreaker` struct** — all mutable state under one `sync.Mutex` (spec 020 Constraints: "shared state … is mutated from concurrent request goroutines and must be safe under a mutex"):
   ```go
   type circuitBreaker struct {
   	next        http.Handler
   	provider    string
   	threshold   int
   	now         func() libtime.DateTime
   	transitions *prometheus.CounterVec

   	mu       sync.Mutex
   	state    breakerState
   	failures int
   	openedAt stdtime.Time
   	probing  bool
   }
   ```
   Add the test-support accessor (mirrors `throttleGate.Throttled()` / `concurrencyLimiter.InFlight()`):
   ```go
   // State returns the breaker's current state as a metric-style label value
   // ("closed", "open", or "half_open"). Only valid on a real breaker
   // (failureThreshold > 0); the no-op path returns next unchanged and has no
   // breaker (mirrors the concurrency limiter's InFlight accessor).
   func (b *circuitBreaker) State() string
   ```

4. **`ServeHTTP`** — admit-or-refuse, then observe the forwarded status:
   ```go
   func (b *circuitBreaker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
   	admitted, probe := b.admit(b.now().Time())
   	if !admitted {
   		writeRateLimited(w, breakerOpenPeriodSeconds)
   		return
   	}
   	sr := &statusRecorder{ResponseWriter: w}
   	b.next.ServeHTTP(sr, r)
   	b.observe(sr.status, probe, b.now().Time())
   }
   ```
   A refused request writes ONLY the shared 429 (via `writeRateLimited`) — never a 5xx, never internal state, never a provider name, upstream URL, failure count, or breaker state in the body (spec 020 Security: "The breaker's 429 body is the static `limiter429Body` constant and must not reveal the provider name, the upstream URL, the breaker state, or the failure count").

5. **`admit(at stdtime.Time) (admitted bool, probe bool)`** (locks `b.mu`; never called with the mutex held):
   - `breakerOpen`: if `at.Sub(b.openedAt) < breakerOpenPeriod` → `(false, false)`. Otherwise the open period has elapsed → transition to `breakerHalfOpen` (via `setStateLocked`), set `b.probing = true`, return `(true, true)` — exactly one probe.
   - `breakerHalfOpen`: if `b.probing` → `(false, false)` (a probe is already in flight, so concurrent requests are 429); otherwise set `b.probing = true` and return `(true, true)`.
   - `breakerClosed` (default): `(true, false)`.

6. **`observe(status int, probe bool, at stdtime.Time)`** (locks `b.mu`; never called with the mutex held). `failed := status >= 500` — the failure signal is the response status at the provider boundary; a 5xx counts, anything below 500 does not, and a client disconnect never arrives as a 5xx so it is never counted (spec 020 Constraints / Security):
   - **Probe verdict** (`probe == true`): clear `b.probing`; on `failed` set `b.failures = b.threshold` and transition to `breakerOpen`; on success set `b.failures = 0` and transition to `breakerClosed`. Return.
   - **Non-probe**: if `b.state != breakerClosed` return. If `status == 0` return without touching `b.failures` — a request that produced no status (client disconnect, or an inner gate that wrote nothing) is neither a failure nor evidence of upstream health. On a 1xx–4xx status set `b.failures = 0` and return. On failure increment `b.failures`, and when `b.failures >= b.threshold` transition to `breakerOpen`.

7. **`setStateLocked(state breakerState, at stdtime.Time)`** (called with `b.mu` held). If `b.state == state` return. Otherwise assign `b.state`; when the new state is `breakerOpen`, record `b.openedAt = at` and clear `b.probing`; emit exactly one INFO line `glog.Infof("[breaker] provider=%s state=%s", b.provider, name)`; and when `b.transitions != nil`, increment `b.transitions.WithLabelValues(b.provider, label).Inc()`. Two small helpers map the state to the two spellings — the log token is hyphenated (`half-open`), the metric label is underscored (`half_open`), per spec AC 13:
   ```go
   func breakerStateLogName(state breakerState) string  // "closed" | "open" | "half-open"
   func breakerStateLabel(state breakerState) string    // "closed" | "open" | "half_open"
   ```
8. **Tests in `pkg/handler/circuit-breaker_test.go`** (`package handler_test`, Ginkgo v2 + Gomega). Reuse `t0`, `newClock()`, `newGateStub`, `serveAsync`, `newMessagesRequest`, `expected429Body`, `captureStderr`, and the concurrent-fire helpers from `pkg/handler/throttle-gate_test.go` (`fireConcurrent`, `closedCount`, `shedIndex`) — they already live in the same `handler_test` package, so a hand-rolled duplicate would be a compile error. ⚠️ For the two status-0 rows (the "Status 0 does not reset the count" row and the "Client disconnect is not a failure" row) `newGateStub` is NOT usable: it always calls `WriteHeader(int(status))`, and a status of 0 panics the httptest recorder (`invalid WriteHeader code 0`). Add a small dedicated stub in this file that returns without calling `WriteHeader`/`Write` — a new helper type is not a "duplicate" of the reused ones. Advance the clock with `clock.SetNow(t0.Add(<duration>))` — never sleep through a real open period. Rows:
   - **AC 7 — open:** threshold `5`, upstream stub returns `500`. Fire 5 requests (each 500) then a 6th: the 6th is answered `429` with body exactly `expected429Body` and header `Retry-After` equal to `"30"`, and the upstream call count does NOT increase for the 6th (the upstream was not invoked).
   - **AC 8 — never a router-made 5xx:** open the breaker (threshold `2`, upstream `500`), then flip the stub to `200` and fire several requests while still inside the open period: every response is the router's `429` and none is a 5xx; assert the observed status set over the open period is a subset of {`429`, the upstream's own status} and contains no 5xx the upstream never produced.
   - **AC 9 — half-open probe, close on success:** from the open state advance the clock `30s`; fire one request with the upstream now returning `200` → it is admitted (upstream invoked) and the breaker returns to `closed` (`State() == "closed"`); the failure count is reset (drive `threshold-1` further 500s and assert the breaker is still closed).
   - **AC 9 — half-open probe, reopen on failure:** from the open state advance `30s` and admit the probe with the upstream still returning `500` → the breaker reopens (`State() == "open"`) and the next request is `429` for a further open period; then advance the clock another `30s` and assert a second probe is admitted (a second open period admits a second probe).
   - **AC 9 — exactly one probe:** with the probe in flight (upstream blocking), concurrent requests are answered `429` while exactly one reaches the upstream (assert the upstream entry count stays 1 until the probe completes).
   - **AC 10 — breaker independence:** two breakers for providers `a` and `b` both wrapping one stub (matching the spec's AC 10 wording, "including two providers sharing one upstream"); open `a` and assert `b` forwards normally (upstream invoked, status passed through).
   - **AC 12 (light) — pre-initialized and incrementing counter:** pass a fresh `prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ccrouter_breaker_state_transitions_total", Help: "test"}, []string{"provider", "state"})`; after construction with provider `"p"`, all three of `state="closed"`, `state="open"`, `state="half_open"` read `0` via `testutil.ToFloat64`; after the first closed→open transition, `state="open"` reads exactly `1`.
   - **AC 13 — log lines:** `flag.Set("logtostderr", "true")`; inside `captureStderr`, drive an open, a half-open, and a close; assert the captured output contains `[breaker] provider=p state=open`, `[breaker] provider=p state=half-open`, and `[breaker] provider=p state=closed`.
   - **Non-5xx resets the count:** drive `threshold-1` failures, then one `200`, then `threshold-1` failures again — the breaker is still `closed`.
   - **Status 0 does not reset the count:** drive `threshold-1` failures, then a status-0 request (the stub returns without writing a status), then one more failure — the breaker opens, proving the status-0 request left the accumulated failure count intact.
   - **Upstream hang and bad gateway count as failures:** a stub answering `504` (and one answering `502`) counts as a failure toward the breaker, so the spec's "upstream hangs and the proxy answers 504" failure mode is traversed rather than only the `500` boundary.
   - **A non-probe verdict cannot drive the breaker:** admit a slow request while `closed`, open the breaker from other requests, then let the slow request return `500` — `State()` is unchanged (the non-probe response is ignored once the state has left `closed`).
   - **Client disconnect is not a failure:** a request whose context is cancelled while in flight (the stub returns without writing a status) does not open the breaker.
   - **AC 3 — disabled is a no-op:** `NewCircuitBreaker(inner, "p", 0, clock.Now, nil)` and a negative threshold each return `BeIdenticalTo(http.Handler(inner))`, and a request through the returned handler reaches `inner` with `200`; with a non-nil counter, no `ccrouter_breaker_state_transitions_total` series is created for a disabled provider.
9. **Before finishing, re-run `<verification>` and confirm every command passes.** Walk requirements 1–8 against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Placement (spec 020 Constraints): the breaker wraps the provider handler OUTERMOST — outside the 429 delay gate and the cold-start gate — so an open breaker short-circuits the whole provider. This prompt does NOT wire it: the breaker is constructed and returned by the caller; a later prompt places it in `pkg/factory/factory.go`. Do NOT modify `pkg/factory/factory.go`, `pkg/config.go`, `pkg/handler/cold-start-gate.go`, `pkg/handler/metrics.go`, `pkg/reloader/`, `docs/`, `README.md`, or `CHANGELOG.md`.
- The 429 reuses the existing `limiter429Body` constant and the existing `writeRateLimited` helper — do NOT introduce a second 429 body or a second header-write path. The body must not carry the provider name, the upstream URL, the failure count, or the breaker state.
- Fixed internal constants (NOT knobs): the 30s open period and the single probe per open→half-open transition. The threshold IS a knob (`breakerFailureThreshold`); it is independent of the cold-start gate. Do NOT add opt-out flags or tunable thresholds. Do NOT add a Prometheus metric or a new collector in this prompt — the breaker receives an optional `*prometheus.CounterVec` handle; the collector itself is created by a later prompt.
- Time comes from the injected `github.com/bborbe/time` clock, never `time.Now()`. Tests must not sleep through a real open period.
- Concurrency: the state, the failure count, and the probe flag are mutated from concurrent request goroutines — every read-modify-write is under `b.mu`; the tests must exercise concurrent requests (the single-probe row does). No raw `go func()` outside `*_test.go` — use `github.com/bborbe/run` for any background work (none is expected here).
- Logging uses `glog.Infof(...)` (always-on INFO, matching `[throttle]` / `[coldgate]`) — never `fmt.Printf` / `println`. The log line carries only the provider name and the state; never a session id, body, or other client-controlled value.
- Errors go through `github.com/bborbe/errors`; no bare `return err`.
- No new dependencies — the Go standard library plus the existing `bborbe/*` libraries suffice.
- No AI attribution in code or comments.
- `make precommit` must remain green. Follow `docs/dod.md` (GoDoc on every new exported identifier; Ginkgo/Gomega coverage ≥ 80% on new `pkg/<package>` code).
</constraints>

<verification>
make precommit

# Breaker file + constructor + accessor:
grep -n 'func NewCircuitBreaker' pkg/handler/circuit-breaker.go
grep -n 'func (b \*circuitBreaker) State' pkg/handler/circuit-breaker.go
grep -n 'breakerOpenPeriod' pkg/handler/circuit-breaker.go
grep -n 'breakerClosed\|breakerHalfOpen' pkg/handler/circuit-breaker.go
grep -n 'breakerOpen' pkg/handler/circuit-breaker.go

# Reuses the shared 429 path — no second body, no second header writer:
grep -n 'writeRateLimited' pkg/handler/circuit-breaker.go
grep -n 'breakerOpenPeriodSeconds' pkg/handler/circuit-breaker.go
# No second 429 body literal and no second Retry-After writer (the GoDoc may name both):
! grep -nE '"type":"error"|const [A-Za-z0-9_]*429[A-Za-z0-9_]* *=' pkg/handler/circuit-breaker.go
! grep -nE 'Header\(\)\.Set\("Retry-After"' pkg/handler/circuit-breaker.go

# Log line and the two state spellings:
grep -n '\[breaker\] provider=%s state=%s' pkg/handler/circuit-breaker.go
grep -n 'half-open' pkg/handler/circuit-breaker.go
grep -n 'half_open' pkg/handler/circuit-breaker.go

# Pre-initializes the three bounded series:
grep -n 'breakerStateLabels' pkg/handler/circuit-breaker.go

# No new metrics collector added in this prompt:
! grep -n 'prometheus.NewCounterVec' pkg/handler/circuit-breaker.go

# No wall-clock reads:
! grep -n 'time.Now()' pkg/handler/circuit-breaker.go

# Test rows exist:
grep -c 'NewCircuitBreaker' pkg/handler/circuit-breaker_test.go   # expect >= 1
grep -c 'expected429Body\|ccrouter_breaker_state_transitions_total' pkg/handler/circuit-breaker_test.go   # expect >= 1

# Handler suite:
go test -count=1 ./pkg/handler/
</verification>

<!--
RESOLVED at audit (1): the probe's own verdict being authoritative is CONFIRMED correct — the spec's
AC 9 "a probe that succeeds closes the breaker and resets the consecutive-failure count" is read as
the probe's own verdict, and a non-probe response is ignored while the state is half-open or open, so
a slow non-probe request that returns after the breaker has already opened cannot reopen or close it.
The "A non-probe verdict cannot drive the breaker" test row exercises exactly this.

RESOLVED at audit (2): the status-0 / inner-429 reset semantics are settled by requirement 6's
non-probe branch — a status-0 response (client disconnect, or an inner gate that wrote nothing) leaves
the consecutive-failure count untouched, while a 1xx–4xx status (including a router-generated 429
passed through the status recorder from the throttle or cold gate inside the breaker) resets it.
No ambiguity remains.
-->

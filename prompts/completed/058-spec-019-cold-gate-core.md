---
status: completed
spec: [019-cold-start-admission-gate]
summary: 'Added pkg/handler/cold-start-gate.go: a per-provider cold-start admission gate with context-session-id warm/cold classification, a body-size prefill estimate (bytes*2/7), budget admission that lets cold requests wait for budget to free, and reservation release on the first content_block_delta, on handler return, and on client disconnect — plus 13 Ginkgo rows covering the disabled no-op, classification, admission, and release paths.'
execution_id: claude-code-router-cold-start-admission-exec-058-spec-019-cold-gate-core
dark-factory-version: v0.196.0
created: "2026-10-10T14:08:35Z"
queued: "2026-10-10T15:02:15Z"
started: "2026-10-10T15:05:26Z"
completed: "2026-10-10T15:12:35Z"
branch: dark-factory/cold-start-admission-gate
---

# Cold-start admission gate: gate core (classification, estimate, budget admission, release)

<summary>
- A request whose session id was seen inside the configured window passes straight through: no wait, no reservation, no new behaviour for an established session.
- A request whose session id is absent, unknown, or last seen outside the window counts as "cold" and is charged a prefill cost estimate derived from its body size.
- Cold requests are admitted while their estimate fits the provider's in-flight budget; a cold request that would exceed it waits until budget frees, without being dropped or answered with an error.
- The reserved budget is released the moment the response stream emits its first content delta — the point at which prefill has finished — not on a timer and not at message start.
- The reservation is also released whenever the response handler returns (stream end, upstream error), and a client that disconnects while waiting never holds a reservation.
- The gate is off unless configured: with the budget absent, zero, or negative the constructor returns the wrapped handler unchanged, so an unconfigured provider is byte-for-byte today's behaviour.
- The gate reports its live reserved-token count through an accessor, so later prompts' tests can read it without depending on Prometheus.
- Streaming is untouched: response bytes pass through unmodified and server-sent-event flushing keeps working through the wrapper.
</summary>

<objective>
Build the cold-start admission gate's core in `pkg/handler/cold-start-gate.go`: cold/warm classification from the request-context session id, a body-size-derived prefill estimate, budget admission that lets cold requests wait for budget to free, and reservation release on the first streamed content delta, on handler return, and on client disconnect — so a later prompt can add the rate cap, the bounded queue with its 429 refusal, the metrics, and the factory wiring on top of a tested admission core.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only. Single-module Go repo (`go.mod` at root). This repo's `Makefile` includes only `tools.env` + `Makefile.docker` — it does NOT reference `ROOTDIR` or `default.env`, so plain `make test` / `make precommit` is correct and no `ROOTDIR` override is needed. `.git` may be masked in the container — NEVER put a bare `git` command in `<verification>`.
- Read `pkg/handler/concurrency-limiter.go` — the pattern this gate mirrors: the static `limiter429Body` constant (do NOT redefine it; a later prompt's refusal reuses it), the no-op-when-disabled constructor shape (`if x <= 0 { return next }`), the unexported struct with an exported accessor (`func (l *concurrencyLimiter) InFlight() int`), and the disconnect discipline (`case <-r.Context().Done():`).
- Read `pkg/handler/throttle-gate.go` — the sibling per-provider gate: its constructor takes the injected clock as `now func() libtime.DateTime`, falls back to `realNow` when nil, and keeps all mutable state under one `sync.Mutex` with transitions in small `...Locked` helpers.
- Read `pkg/handler/upstream-pool-handler.go` — `var realNow = func() libtime.DateTime { return libtime.DateTime(stdtime.Now()) }` (same `handler` package; reuse it, do not define a second fallback clock).
- Read `pkg/handler/session-id.go` — `SessionIDFromContext(ctx) string` (exported, returns "" when absent) and `ContextWithSessionID(ctx, id)` (the test seam). The session middleware strips the `X-Session-Id` header, so the gate MUST read the id from the context and MUST NOT re-read the header.
- Read `pkg/handler/status-recorder.go` + `pkg/handler/usage-recorder.go` — the response-wrapper pattern: embed `http.ResponseWriter`, override `Write`, expose `Unwrap() http.ResponseWriter` so `http.NewResponseController` keeps SSE flushing working (`usage-recorder.go`'s `Unwrap` doc explains why breaking the chain stalls streams).
- Read `pkg/handler/model-router.go` — confirm `r.ContentLength = int64(len(body))` is set before dispatch; the gate reads `ContentLength` and must NOT read or buffer the body.
- Read `pkg/config.go` for the three knobs prompt 1 adds — `ColdPrefillBudgetTokens`, `ColdSessionWindowSeconds`, `NewSessionRatePerMinute` (all `int`, all `,omitempty`). Do NOT add or modify them here: prompt 1 owns the config surface, and this prompt's constructor receives the budget and the window as parameters, so there is no compile dependency on it.
- Read `pkg/handler/export_test.go` (the `package handler` re-export pattern) and `pkg/handler/concurrency-limiter_test.go` + `pkg/handler/throttle-gate_test.go` (`serveAsync`, `newMessagesRequest`, `expected429Body`, `newClock()` pinned at `t0`, `newGateStub`) — all in `package handler_test`; reuse them.
- Coding plugin docs (in-container paths): `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` (mutex-guarded shared state; raw `go func()` is exempt in `*_test.go`), `go-testing-guide.md` (external `_test` package, Ginkgo v2 + Gomega), `go-doc-best-practices.md` (GoDoc on every new exported identifier), `go-patterns.md` (interface→constructor→struct, `bborbe/errors`).
</context>

<requirements>
1. **New file `pkg/handler/cold-start-gate.go`** (package `handler`), with the repo's standard copyright header and complete GoDoc. Define only these fixed constants (each is an internal constant, NOT a config knob — spec Non-goals):
   ```go
   const (
       // coldGateEstimateNumerator / coldGateEstimateDenominator encode the
       // fixed cost-estimate divisor 3.5 as integer arithmetic:
       // estimate = contentLength * 2 / 7 (no floating point).
       coldGateEstimateNumerator   = 2
       coldGateEstimateDenominator = 7
       // coldGateDefaultSessionWindow is the session window used when the
       // configured coldSessionWindowSeconds is absent, 0, or negative.
       coldGateDefaultSessionWindow = 600 * stdtime.Second
       // coldGateDeltaMarker is the SSE/JSON marker whose first appearance in
       // the response stream means prefill has finished.
       coldGateDeltaMarker = "content_block_delta"
   )
   ```
   Do NOT define the rate burst, queue capacity, max wait, or `Retry-After` clamp in this prompt — a later prompt owns them. Do NOT define a second 429 body; reuse `limiter429Body`.

2. **`ColdGateMetrics`** groups the four additive collectors a later prompt defines (each labeled by `provider`). A nil field disables that observation — the gate must never dereference a nil field. Define it now with exactly these fields and names:
   ```go
   // ColdGateMetrics groups the four additive collectors the cold-start
   // admission gate emits, each labeled by provider. A nil field disables
   // that observation; the gate never dereferences a nil field.
   type ColdGateMetrics struct {
       Delayed        *prometheus.CounterVec
       Refused        *prometheus.CounterVec
       TokensInFlight *prometheus.GaugeVec
       TTFT           *prometheus.HistogramVec
   }
   ```

3. **Constructor** (this prompt's signature; a later prompt extends it with the rate and the max wait):
   ```go
   func NewColdStartGate(
       next http.Handler,
       provider string,
       budgetTokens int,
       sessionWindow stdtime.Duration,
       now func() libtime.DateTime,
       metrics ColdGateMetrics,
   ) http.Handler
   ```
   - Disabled is a byte-for-byte no-op (spec AC 3): `if budgetTokens <= 0 { return next }`.
   - `sessionWindow <= 0` resolves to `coldGateDefaultSessionWindow`.
   - `now == nil` falls back to `realNow`.
   - Returns a `*coldStartGate` holding `next`, `provider`, `budgetTokens`, `sessionWindow`, `now`, `metrics`, `seen: make(map[string]stdtime.Time)`, and `notify: make(chan struct{})`.

4. **`coldStartGate` struct + accessor.** All mutable state lives in the struct under a single `sync.Mutex` (spec Constraints: the seen map and the in-flight token count are mutated from concurrent request goroutines). Fields: `next`, `provider`, `budget`, `window`, `now`, `metrics`, then `mu sync.Mutex`, `seen map[string]stdtime.Time`, `inFlightTokens int`, `notify chan struct{}`. Add the accessor:
   ```go
   // InFlight returns the number of cold-prefill tokens currently reserved —
   // the gate's in-flight cold token count. Only valid on a real gate
   // (budgetTokens > 0); the disabled path returns next unchanged and has no
   // gate (mirrors the concurrency limiter's InFlight accessor). Tests read
   // this, not the Prometheus gauge.
   func (g *coldStartGate) InFlight() int
   ```

5. **Classification + the sliding window.** In `ServeHTTP`, read `sessionID := SessionIDFromContext(r.Context())` and `at := g.now().Time()`. Under `g.mu`: prune expired entries, then classify. Warm iff `sessionID != ""` AND the id is in `seen` with `at.Sub(last) < g.window`; a warm request refreshes `seen[sessionID] = at`, unlocks, and forwards with no reservation. Everything else (id absent, unknown, or last seen outside the window) is cold (spec DB 1). A request with an empty session id is ALWAYS cold. Prune helper: delete every entry with `at.Sub(last) >= g.window` so the map cannot grow without bound (spec Constraints).

6. **Cost estimate.** `estimate(contentLength int64) int` returns `contentLength * coldGateEstimateNumerator / coldGateEstimateDenominator` for `contentLength > 0`, else `0` (a negative `ContentLength` means unknown). Use `int64` arithmetic before narrowing, and no floating point anywhere (spec DB 2).

7. **Budget admission + waiting.** A cold request reserves its estimate when admitted; the admission rule is: admitted if the budget is disabled, or nothing is cold in flight, or `inFlightTokens + estimate <= budget`; otherwise it waits (spec DB 2: "admitted immediately when the provider has nothing cold in flight; otherwise it is admitted only while the provider's in-flight cold tokens plus its estimate stay within the configured budget"). Implement one `tryAdmitLocked(sessionID string, estimate int, at stdtime.Time) (admitted bool, warm bool)` called with `g.mu` held:
   - Warm re-check first: if `sessionID != ""` and `seen` already holds it inside the window (a concurrent first request for the same id was admitted while this one waited), refresh `seen[sessionID] = at` and return `(true, true)` — forward with no reservation and no charge.
   - Budget check: if `g.budget > 0 && g.inFlightTokens > 0 && g.inFlightTokens+estimate > g.budget`, return `(false, false)`.
   - Commit: `g.inFlightTokens += estimate`; if `sessionID != ""`, `seen[sessionID] = at`; then broadcast and return `(true, false)`.
   - Recording `seen` only on admission (never at arrival) is required so a request that is never admitted does not mark its id as seen. This is what makes two concurrent first requests for one id cost one charge.
   - Waiting loop: call `tryAdmitLocked`; on success forward; on failure capture `notify := g.notify` under the same lock, unlock, and `select` on `notify` and `<-r.Context().Done()` (client disconnected while held → return without forwarding and without holding a reservation). `broadcastLocked()` closes and replaces `g.notify` under `g.mu`; every waiter re-reads state after waking.
   - This prompt has NO timeout and NO 429: a held request waits until budget frees or the client disconnects. The bounded queue, the max wait, and the refusal are a later prompt.

8. **Reservation release + response observation.** Admitted cold requests forward through a wrapper that observes the stream:
   ```go
   // coldReleaseRecorder wraps the response writer and calls onDelta exactly
   // once, on the first Write carrying the content_block_delta marker — the
   // point at which prefill has finished. Bytes pass through unmodified and
   // Unwrap preserves the SSE-safe flush chain.
   type coldReleaseRecorder struct {
       http.ResponseWriter
       onDelta func()
       once    sync.Once
   }
   func (c *coldReleaseRecorder) Write(b []byte) (int, error)   // write through, then scan for the marker
   func (c *coldReleaseRecorder) Unwrap() http.ResponseWriter   // return the embedded writer
   ```
   Build the per-request release as `release := sync.OnceFunc(func() { /* under g.mu: inFlightTokens -= estimate (clamp at 0); if metrics.TokensInFlight != nil, .WithLabelValues(provider).Sub(float64(estimate)); broadcastLocked() */ })`. Then `defer release()` and call `g.next.ServeHTTP(rec, r)`. The recorder's `onDelta` calls `release()` and, if `metrics.TTFT != nil`, observes `g.now().Time().Sub(dispatchAt).Seconds()` where `dispatchAt` is the time the gate began forwarding (spec DB 5: release on the first `content_block_delta`; not on `message_start`, not on a timer). `sync.OnceFunc` makes the delta release and the deferred release idempotent.
   - Do NOT buffer the body; do NOT delay or reorder writes. A marker split across two Writes is best-effort undetected — the deferred release still frees the reservation (matches `ExtractUsage`'s best-effort SSE scan precedent).
   - Warm requests bypass the wrapper entirely (`reserved == 0` → call `g.next.ServeHTTP(w, r)` directly): no reservation, no TTFT, no gauge.

9. **Tests in `pkg/handler/cold-start-gate_test.go`** (`package handler_test`, Ginkgo v2 + Gomega). Reuse `newClock()`, `serveAsync`, `newMessagesRequest`, `newGateStub`. Route every construction through ONE local helper so a later prompt can extend the constructor in one place:
   `newColdGate := func(next http.Handler, budget int, window stdtime.Duration) http.Handler { return handler.NewColdStartGate(next, "p", budget, window, clock.Now, handler.ColdGateMetrics{}) }`
   Build requests with a session id via `req = req.WithContext(handler.ContextWithSessionID(req.Context(), "s1"))`; build a sized body via `httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))` (which sets `ContentLength`). Add a local streaming stub that can write an SSE event then block on a release channel (selecting on `r.Context().Done()`). Rows:
   - **AC 3 (disabled no-op):** budget `0` and `-1` each return `BeIdenticalTo(http.Handler(inner))`; a request through it reaches the inner handler and returns 200.
   - **AC 4 (classification):** (a) id `s1` sent once (cold, admitted), then sent again while a different cold request is held → the second forwards immediately (upstream entry count rises) even though the held request has not; (b) id `s1` at `t0`, advance the clock past the window, resend `s1` → it is held (cold again); (c) a request with NO session id is held while the budget is consumed (cold); (d) window fallback: construct the gate with `window <= 0`, send `s1` at `t0`, advance 599s → warm, advance past 600s → cold (AC 2's documented 600s fallback).
   - **AC 5 (budget admission + estimate):** (a) a lone cold request whose estimate exceeds the whole budget is admitted (upstream entered); (b) request A (id `a`) admitted with the upstream blocking (reservation held), request B (id `b`, same estimate, budget < 2×estimate) → `Consistently` the upstream entry count stays 1; release A's block → `Eventually` the count reaches 2 and B completes; (c) drive bodies of known sizes through a blocking gate and assert `InFlight()` equals `bytes * 2 / 7` for each.
   - **AC 6 (release detection):** (a) the stub writes `event: content_block_delta\ndata: {...}\n\n` then blocks → `Eventually` `InFlight()` drops to 0 while the handler is still writing; (b) the stub writes only `event: message_start\ndata: {...}\n\n` then blocks → `Consistently` `InFlight()` stays at the estimate; (c) an upstream that returns without a delta → `Eventually` `InFlight()` is 0; (d) a request with an already-cancelled context while budget is blocked → the upstream is never invoked and `InFlight()` is 0; (e) a disconnect while a reservation is held → the handler returns and `Eventually` `InFlight()` is 0.
10. **Before finishing, re-run `<verification>` and confirm every command passes.** Walk requirements 1–9 against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Keying and placement (spec Constraints): the gate is per provider; a later prompt constructs it immediately after the upstream pool handler and before the throttle gate in `pkg/factory/factory.go`, so the existing 429 delay gate stays outermost. The model router's body read, alias resolution, `[1m]` strip, key routing, and system-lift flow are untouched. Do NOT modify `pkg/handler/model-router.go`, `pkg/factory/factory.go`, or `pkg/config.go`.
- Session id comes from the request context (`SessionIDFromContext`) — the middleware strips the `X-Session-Id` header, so the gate MUST NOT re-read the header.
- Body size comes from `req.ContentLength` — the gate must NOT read or buffer the request body.
- Time comes from the injected `github.com/bborbe/time` clock, never `time.Now()` — tests must not sleep through a real window.
- Fixed internal constants (NOT knobs): rate burst 2, queue capacity 32, max wait 30s, estimate divisor 3.5 (integer arithmetic, no floating point), `Retry-After` clamp 1–60s. The session window IS a knob (`coldSessionWindowSeconds`); 600s is its fallback when absent or negative. Do NOT add config knobs, opt-out flags, or tunable thresholds.
- The refusal (a later prompt) reuses the existing `limiter429Body` constant and must not carry queue depth, provider name, upstream URL, or any session-identifying value. Raw session ids must never appear in any log line.
- No 5xx from this gate: no admission outcome produces a 5xx, and no request is dropped without an answer.
- Gate state is mutated from concurrent request goroutines — the seen map and the in-flight token count are shared and must be safe under concurrency; tests must exercise concurrent requests.
- No new dependencies — the Go standard library plus the existing `bborbe/*` libraries suffice.
- No AI attribution in code or comments.
- `make precommit` must remain green — run it before declaring done. Follow `docs/dod.md` (GoDoc on every new exported identifier).
- Do NOT touch `docs/`, `README.md`, `CHANGELOG.md`, `pkg/handler/metrics.go`, or the factory in this prompt — later prompts own the rate/queue/refusal, the metrics/logs/wiring, and the documentation.
</constraints>

<verification>
make precommit

# Gate file + constructor + accessor:
grep -n 'func NewColdStartGate' pkg/handler/cold-start-gate.go
grep -n 'func (g \*coldStartGate) InFlight' pkg/handler/cold-start-gate.go
grep -n 'coldGateEstimateNumerator\|coldGateEstimateDenominator\|coldGateDefaultSessionWindow\|coldGateDeltaMarker' pkg/handler/cold-start-gate.go

# Integer arithmetic (numerator/denominator), not a floating-point divisor:
grep -n 'coldGateEstimateDenominator' pkg/handler/cold-start-gate.go
grep -c 'func NewColdStartGate' pkg/handler/cold-start-gate.go            # expect 1

# This prompt adds no refusal / second 429 body:
! grep -n 'rate_limit_error' pkg/handler/cold-start-gate.go

# Session id from context, not the header; body from ContentLength:
grep -n 'SessionIDFromContext' pkg/handler/cold-start-gate.go
grep -n 'ContentLength' pkg/handler/cold-start-gate.go
! grep -n 'X-Session-Id' pkg/handler/cold-start-gate.go
! grep -n 'io.ReadAll\|ioutil.ReadAll' pkg/handler/cold-start-gate.go

# Release detection marker:
grep -n 'content_block_delta' pkg/handler/cold-start-gate.go

# Test rows exist:
grep -c 'NewColdStartGate' pkg/handler/cold-start-gate_test.go           # expect >= 1

# Handler suite:
go test -count=1 ./pkg/handler/
</verification>

<!--
OPEN QUESTION (resolved best-effort — flag at audit): the spec's DB 1 says the seen map records
"the arrival time of the most recent request carrying each id". This prompt records the id at
ADMISSION, not at arrival, so a request that is never admitted never marks its id as seen —
otherwise a refused request's own retry would arrive "warm" and bypass the gate. The window still
slides with each admitted (or warm) request. Confirm this reading at audit.
OPEN QUESTION: the TTFT histogram is documented as "time from dispatch to the first
content_block_delta"; this prompt measures from the moment the gate forwards (after any wait),
not from the request's arrival, so the histogram does not conflate admission delay with prefill
latency. Confirm at audit.
-->

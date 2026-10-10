---
status: completed
spec: [019-cold-start-admission-gate]
summary: Added the per-minute new-session rate check, bounded queue with fixed 30s max wait, and the 429 refusal with a clamped Retry-After to the cold-start admission gate, with Ginkgo rows for rate, queue-full, timeout, no-5xx, and per-provider independence.
execution_id: claude-code-router-cold-start-admission-exec-059-spec-019-cold-gate-rate-queue-refusal
dark-factory-version: v0.196.0
created: "2026-10-10T14:08:35Z"
queued: "2026-10-10T15:02:15Z"
started: "2026-10-10T15:12:36Z"
completed: "2026-10-10T15:21:11Z"
branch: dark-factory/cold-start-admission-gate
---

# Cold-start admission gate: rate cap, bounded queue, and 429 refusal

<summary>
- Independently of the budget, a provider admits at most a configured number of brand-new sessions per minute, with two admitted immediately as a fixed burst.
- A first request for a session id beyond that allowance waits in the same queue as a budget-blocked request, rather than being refused on sight.
- Waiting cold requests occupy a bounded queue of a fixed size; a request that arrives when the queue is full is refused straight away.
- A request that cannot be admitted within a fixed maximum wait is refused with the same generic Anthropic-shaped rate-limit error body the router already uses, never a 5xx and never a silent drop.
- Every refusal carries an integer `Retry-After` header between 1 and 60 seconds so a well-behaved client backs off and retries cleanly.
- The refusal body is the existing static constant and leaks nothing: no queue depth, no provider name, no upstream URL, no session-identifying value.
- A session id is counted against the per-minute rate exactly once, when its first request is admitted, so two concurrent first requests for one session consume one unit.
- Each provider's budget, rate, and queue are fully independent — exhausting one provider never delays or blocks another, even when two providers share one upstream.
- The shared state behind all of this is safe under concurrent request goroutines, and the tests exercise concurrent bursts.
</summary>

<objective>
Extend the cold-start admission gate with the second admission check (the per-minute new-session rate), the bounded queue, the fixed maximum wait, and the HTTP 429 refusal with a clamped `Retry-After` — so a burst of new sessions is spread out and bounded instead of arriving at the upstream at once, and the excess is refused with a body clients can back off from rather than a 5xx or a hang.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only. Single-module Go repo. `make test` / `make precommit` are root targets; this repo's `Makefile` does NOT use `ROOTDIR`/`default.env`, so no override is needed. `.git` may be masked in the container — NEVER put a bare `git` command in `<verification>`.
- This prompt depends on prompt 2 (`2-spec-019-cold-gate-core.md`): read the shipped `pkg/handler/cold-start-gate.go` in full before editing. It already provides `NewColdStartGate`, the `coldStartGate` struct (fields `next`, `provider`, `budget`, `window`, `now`, `metrics`, `mu`, `seen`, `inFlightTokens`, `notify`), `InFlight()`, `tryAdmitLocked()`, `broadcastLocked()`, `coldReleaseRecorder`, and `ServeHTTP`'s admission loop. (The estimate and the seen-map prune are inline, not named helpers.)
- Read `pkg/handler/concurrency-limiter.go` — the exact shape the queue mirrors: a buffered-channel semaphore, a three-case `select` (slot / timeout → 429 / `<-r.Context().Done()`), and the static `limiter429Body` written verbatim on refusal.
- Read `pkg/handler/throttle-gate.go` — the sibling bounded-queue precedent: `throttleMaxPacedRequests = 32`, the `queueTimer := stdtime.NewTimer(g.maxDelay)` timeout branch, and the refusal writing `limiter429Body`.
- Read `pkg/handler/export_test.go` — the re-export pattern; `var ThrottleMaxPacedRequests = throttleMaxPacedRequests` is the exact precedent for exposing the queue capacity to `handler_test`.
- Read `pkg/handler/concurrency-limiter_test.go` (`expected429Body`, `serveAsync`, `newMessagesRequest`) and `pkg/handler/throttle-gate_test.go` (`newClock()`, `newGateStub`, `fireConcurrent`, `closedCount`, `shedIndex`) — reuse them; the queue-full row is shaped like the throttle overflow row.
- Read `pkg/config.go` — `NewSessionRatePerMinute int \`yaml:"newSessionRatePerMinute,omitempty"\`` (prompt 1) and the lenient semantics: absent, 0, or negative disables the rate check.
- Coding plugin docs (in-container paths): `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` (mutex-guarded shared state; `run.CancelOnFirstErrorWait` over raw `go func()` outside tests), `go-testing-guide.md` (Ginkgo v2 + Gomega), `go-doc-best-practices.md`.
</context>

<requirements>
1. **Add the fixed internal constants** to `pkg/handler/cold-start-gate.go` (each is an internal constant, NOT a config knob — spec Non-goals: "No operator-tunable queue capacity, max wait, rate burst, or cost-estimate divisor"):
   ```go
   const (
       // coldGateRateBurst is the fixed burst of newly-seen session ids a
       // provider admits immediately before the refill applies.
       coldGateRateBurst = 2
       // coldGateQueueCapacity is the fixed bounded queue capacity: at most
       // this many cold requests wait; a request arriving when the queue is
       // full is refused immediately.
       coldGateQueueCapacity = 32
       // coldGateMaxWait is the fixed maximum queue wait before refusal.
       coldGateMaxWait = 30 * stdtime.Second
       // coldGateRetryAfterMinSeconds / coldGateRetryAfterMaxSeconds clamp
       // the integer Retry-After header value.
       coldGateRetryAfterMinSeconds = 1
       coldGateRetryAfterMaxSeconds = 60
   )
   ```

2. **Extend the constructor** to take the rate and the max wait, and update its disabled check. New signature (order is fixed):
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
   ) http.Handler
   ```
   - Disabled is now `if budgetTokens <= 0 && newSessionsPerMinute <= 0 { return next }` — the gate is enabled when EITHER check is configured (spec: "with the budget and the rate each absent, 0, or negative, the gate constructor returns the inner handler unchanged").
   - `maxWait <= 0` resolves to `coldGateMaxWait`.
   - Store `rate`, `maxWait`, and a `queue chan struct{}` of capacity `coldGateQueueCapacity`; initialize `rateTokens = coldGateRateBurst` and `rateLastRefill = now().Time()`. Resolve the `now == nil → realNow` fallback BEFORE this call — `now()` on a nil clock panics.
   - Update prompt 2's test helper `newColdGate` in `pkg/handler/cold-start-gate_test.go` to pass the new arguments (rate `0`, max wait `coldGateMaxWait` equivalent) so prompt 2's rows still compile and pass unchanged.

3. **Rate bucket (lazy refill, under `g.mu`).** Add `refillLocked(at stdtime.Time)`: if `g.rateTokens >= coldGateRateBurst`, set `rateLastRefill = at` and return; else `earned := int(at.Sub(g.rateLastRefill)) * g.rate / int(stdtime.Minute)`; when `earned > 0`, `g.rateTokens = min(g.rateTokens+earned, coldGateRateBurst)` and advance `g.rateLastRefill = g.rateLastRefill.Add(stdtime.Duration(earned) * stdtime.Minute / stdtime.Duration(g.rate))` (exact, no drift). `refillLocked` is a no-op when `g.rate <= 0`.
   Extend `tryAdmitLocked` so that, AFTER the budget check passes, it calls `refillLocked(at)` and, when `g.rate > 0 && g.rateTokens < 1`, returns not-admitted with the blocking reason `"rate"`; the budget check returns reason `"budget"`. Change the signature to `tryAdmitLocked(sessionID string, estimate int, at stdtime.Time) (admitted bool, warm bool, reason string)` and decrement `g.rateTokens` in the commit step (only when `g.rate > 0`). Every admitted cold request — a newly-seen id, an id last seen outside the window, or an id-less request (see the resolved note below) — consumes exactly one unit; the warm re-check already returned early for an id admitted by a concurrent request, so two concurrent first requests for one id consume one unit (spec DB 3).

4. **Bounded queue + max wait + refusal.** Rework `ServeHTTP`'s cold path:
   - Attempt `tryAdmitLocked` FIRST; acquire a queue slot ONLY after that first attempt fails, so a request that can be admitted immediately is never refused `queue_full`. Acquire the slot with a NON-BLOCKING select on `g.queue`; on failure call `g.refuse(w, r, "queue_full")` and return. The slot is held ONLY while waiting — release it the moment the request is admitted, refused, or its context is done (use an explicit `slotHeld` flag so a `defer` release is a no-op after an early release). Do NOT hold the slot through the forward: the queue bounds waiters, not in-flight work.
   - Start `deadline := stdtime.NewTimer(g.maxWait)` with `defer deadline.Stop()` when the request enters the queue. Add `case <-deadline.C: g.refuse(w, r, "timeout")` to the wait `select`, alongside `notify` and `<-r.Context().Done()`.
   - Track whether the request waited: on the first failed admission attempt set `waited = true` and remember the blocking reason (`"budget"` or `"rate"`). When the request is finally admitted after waiting, increment `g.metrics.Delayed` (if non-nil) once; when the warm re-check admits it, do not. On the timeout refusal, use reason `"timeout"`.
   - `refuse(w http.ResponseWriter, r *http.Request, reason string)`: increment `g.metrics.Refused` (if non-nil) once; then
     `w.Header().Set("Content-Type", "application/json")`; `w.Header().Set("Retry-After", strconv.Itoa(g.retryAfterSeconds()))` (add `strconv` to the imports); `w.WriteHeader(http.StatusTooManyRequests)`; `_, _ = w.Write([]byte(limiter429Body))`. NEVER a 5xx.
   - `retryAfterSeconds() int` returns `int(g.maxWait / stdtime.Second)` clamped into `[coldGateRetryAfterMinSeconds, coldGateRetryAfterMaxSeconds]` — always an integer in 1–60, so even a sub-second test max wait yields 1.
   - The body is the EXISTING static `limiter429Body` constant — do not define a second body string, and do not interpolate anything into it (spec Constraints: no queue depth, provider name, upstream URL, or session-identifying value).
   - The `[coldgate]` INFO log lines are a LATER prompt's; do not add them here.

5. **Per-provider independence** falls out of construction: all state lives on the instance, so two gates share nothing. Do not add any package-level mutable state. The seen map and the in-flight token count stay under `g.mu`; the queue is a channel.

6. **Tests in `pkg/handler/cold-start-gate_test.go`** (extend prompt 2's file; `package handler_test`). Add a richer local helper `newColdGateFull(next http.Handler, budget int, rate int, maxWait stdtime.Duration) http.Handler`. Reuse `expected429Body`, `fireConcurrent`, `closedCount`, `shedIndex`, `newClock()`. Rows:
   - **AC 7 (rate):** (a) budget ample, `rate = 2` per minute, a max wait longer than the test (e.g. the 30s default), the upstream blocking so `s1` and `s2` hold their reservations: distinct ids `s1` and `s2` are admitted immediately (upstream entered); a third distinct id `s3` is NOT admitted (`Consistently` the upstream is not entered for it); advance the injected clock by the refill interval (30s for rate 2), then release `s1`'s and `s2`'s upstream blocks so their release broadcasts and wakes `s3` — `Eventually` `s3` is admitted. (A short max wait would refuse `s3` on the real deadline timer first, and a clock advance alone wakes no waiter.) (b) two concurrent first requests for ONE id: fire them concurrently, then send `s2` → admitted immediately, then `s3` → held — proving the two concurrent requests for one id consumed a single rate unit.
   - **AC 8 (bounded queue + refusal):** (a) timeout path — budget blocked by a held reservation, a cold request with a short max wait (e.g. 50ms) is answered 429 with `Body.String()` EXACTLY equal to `expected429Body`, `Content-Type` containing `application/json`, and a `Retry-After` value parsed as an integer in `[1,60]`; (b) queue-full path — budget blocked, a max wait longer than the test (e.g. the 30s default), fire `handler.ColdGateQueueCapacity + 1` concurrent cold requests (hand-roll them with a session id on the request context via `handler.ContextWithSessionID` — the session middleware strips `X-Session-Id`, so the gate never reads the header; `fireConcurrent` sends `newMessagesRequest()`, which carries no session id): exactly one is answered 429 immediately with the static body and a `Retry-After` in `[1,60]`, and the others wait (cancel their contexts to clean up).
   - **AC 9 (never 5xx, never dropped):** a burst exceeding BOTH budget and rate with a short max wait — `Eventually` all requests complete, and each response code is either the forwarded upstream status or 429; assert zero 5xx.
   - **AC 10 (per-provider independence):** two independent gates A and B; exhaust A's budget with a held reservation so an A request waits; a B request forwards immediately (B's upstream entered) while A's is still held; then release A and confirm its request completes.
   - Keep prompt 2's rows passing unchanged.
7. **Test seam in `pkg/handler/export_test.go`:** add, following the `ThrottleMaxPacedRequests` precedent,
   ```go
   // ColdGateQueueCapacity exposes the bounded queue capacity so the
   // queue-full row can saturate it deterministically (spec 019).
   var ColdGateQueueCapacity = coldGateQueueCapacity
   ```
8. **Before finishing, re-run `<verification>` and confirm every command passes.** Walk requirements 1–7 against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- This prompt depends on prompt 2: do not approve or execute until prompt 2 has shipped `pkg/handler/cold-start-gate.go` with `NewColdStartGate`, `tryAdmitLocked`, `broadcastLocked`, `InFlight`, and `coldReleaseRecorder`.
- Fixed internal constants, NOT knobs (spec Non-goals): rate burst 2, queue capacity 32, max wait 30s, estimate divisor 3.5, `Retry-After` clamp 1–60s. The session window IS a knob (`coldSessionWindowSeconds`, 600s fallback). Do NOT add any new config field, opt-out flag, or tunable threshold.
- The refusal reuses the existing `limiter429Body` constant (`pkg/handler/concurrency-limiter.go`); the body is the same static generic message and must not carry queue depth, provider name, upstream URL, or any session-identifying value.
- No admission outcome produces a 5xx, and no request is dropped without an answer: every request either reaches the upstream or receives the 429 refusal.
- A client that disconnects while waiting must never hold a queue slot or a budget reservation — the concurrency limiter's disconnect discipline is the model.
- Bounded resources throughout: at most 32 waiters, each wait at most the max wait, the budget bounded by configuration, and the seen map pruned to the window.
- Time comes from the injected `github.com/bborbe/time` clock, never `time.Now()`; tests must not sleep through a real window — the rate refill row advances the injected clock.
- Per-provider independence: no package-level mutable state; two providers sharing one upstream each bound their own cold work.
- No new dependencies — the Go standard library plus existing `bborbe/*` libraries suffice.
- No AI attribution in code or comments.
- `make precommit` must remain green. Follow `docs/dod.md`.
- Do NOT add the `[coldgate]` log lines, the Prometheus collector definitions, or the factory wiring in this prompt — the next prompt owns metrics, logs, and wiring. Do NOT touch `docs/`, `README.md`, or `CHANGELOG.md`.
</constraints>

<verification>
make precommit

# Fixed internal constants landed:
grep -n 'coldGateRateBurst\|coldGateQueueCapacity\|coldGateMaxWait\|coldGateRetryAfterMinSeconds\|coldGateRetryAfterMaxSeconds' pkg/handler/cold-start-gate.go

# Rate + queue + refusal:
grep -n 'refillLocked\|rateTokens\|rateLastRefill' pkg/handler/cold-start-gate.go
grep -n 'func (g \*coldStartGate) refuse\|func (g \*coldStartGate) retryAfterSeconds' pkg/handler/cold-start-gate.go
grep -n 'Retry-After' pkg/handler/cold-start-gate.go
grep -n 'limiter429Body' pkg/handler/cold-start-gate.go
grep -n 'queue_full\|"timeout"\|"budget"\|"rate"' pkg/handler/cold-start-gate.go

# No new 429 body, no 5xx, no session id / provider in the body:
! grep -n '"rate_limit_error"' pkg/handler/cold-start-gate.go
! grep -n 'X-Session-Id' pkg/handler/cold-start-gate.go   # the gate reads the session id from the request context only
! grep -n 'StatusInternalServerError\|StatusBadGateway\|StatusServiceUnavailable' pkg/handler/cold-start-gate.go

# Disabled is now budget AND rate:
grep -n 'budgetTokens <= 0 && newSessionsPerMinute <= 0' pkg/handler/cold-start-gate.go

# Test seam + rows:
grep -n 'ColdGateQueueCapacity' pkg/handler/export_test.go
grep -c 'ColdGateQueueCapacity\|Retry-After\|refill' pkg/handler/cold-start-gate_test.go   # expect >= 1

# Handler suite:
go test -count=1 ./pkg/handler/
</verification>

<!-- Resolved at audit 2026-10-10: (1) a no-id request consumes one rate unit — it is always cold, and
otherwise the cap is bypassable by omitting the header; (2) the DELAYED reason is budget|rate and the
REFUSED reason is queue_full|timeout, both inside the spec's enum; (3) Retry-After is int(maxWait/1s)
clamped to [1,60] — the spec fixes only the range, so the exact value is an implementation choice. -->

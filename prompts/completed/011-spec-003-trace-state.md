---
status: completed
spec: [003-enabletrace-endpoint]
summary: 'Added process-global trace-state primitive in pkg/handler/trace_state.go with Enable/Disable/IsEnabled methods, 5-minute TTL timer, mutex-serialized cancel-and-restart, and Ginkgo specs covering AC #4/5/6/8 and Failure Modes row 3'
execution_id: claude-code-router-enabletrace-exec-011-spec-003-trace-state
dark-factory-version: dev
created: "2026-06-30T11:57:22Z"
queued: "2026-06-30T12:09:00Z"
started: "2026-06-30T12:10:06Z"
completed: "2026-06-30T12:13:35Z"
---

<summary>
- A new in-memory trace-state primitive lets the router toggle per-request trace logging on and off without a restart.
- Enabling trace turns tracing on for a bounded 5-minute window that automatically disables on expiry.
- Disabling trace mid-window cancels the pending timer immediately so no late reset flips tracing back on.
- Repeated enable calls deterministically reset the window: each cancels the prior timer and starts a fresh 5-minute window, with exactly one live timer at any time.
- A test-only `TRACE_TTL` environment-variable override shortens the window for fast unit tests; production ignores it.
- At boot with no enable call, the trace state is off and no TTL goroutine is running.
- All new Info-level log lines are gated behind `glog.V(n)` and use lowercase messages.
- This prompt adds only the state primitive (no HTTP handlers, no middleware changes) — prompts 2 and 3 build on it.
</summary>

<objective>
Build the process-global trace-state primitive that the `/enabletrace` and `/disabletrace` HTTP endpoints (prompt 2) and the trace middleware (prompt 2) will consult. The primitive owns a process-internal atomic boolean plus a bounded 5-minute TTL timer whose expiry calls `Disable()`. This is prompt 1 of 3 for spec 003 and depends on nothing.
</objective>

<context>
Read CLAUDE.md at the repo root for project conventions.

Read these source files before making changes:
- `specs/in-progress/003-enabletrace-endpoint.md` — the full spec; pay attention to "Desired Behavior" items 1, 4, 5, 6, 8, the "Constraints" section (frozen constant: 5-minute production TTL; `TRACE_TTL` test-only override; glog conventions; trace independence from SIGHUP and `atomic.Pointer[http.Handler]` mux swap), and the "Failure Modes" table rows 1, 3, 4, 7.
- `pkg/handler/setloglevel.go` — the existing operator-local endpoint exemplar whose auto-revert pattern (`SetLoglevelAutoRevert = 5 * time.Minute`, `NewSetLoglevelHandlerWithRevert(autoRevert time.Duration)` test-seam constructor) is the model for the TTL constant + test-only override. Read it to mirror the constant-naming and test-seam conventions exactly.
- `pkg/handler/trace.go` — the v0.14.0 `NewTraceMiddleware(next http.Handler, traceDir string) http.Handler` middleware. Prompt 2 will update it to consult `IsEnabled()` per request; THIS prompt does NOT touch trace.go. Read it only to confirm the redaction logic (Authorization / x-api-key → `***`, case-insensitive) already exists and must not regress.
- `pkg/config.go` — `Config.Trace bool` field (yaml tag `trace,omitempty`). THIS prompt does NOT touch config.go. The config flag stays as a deprecated always-on opt-in; the new toggle is a separate process-internal atomic boolean. Read it only to confirm `Config.Trace` is NOT the same mechanism as the new primitive.
- `pkg/handler/handler_suite_test.go` — the Ginkgo suite runner; new specs go in `package handler_test` to match.
- `pkg/handler/trace_test.go` — existing Ginkgo + Gomega patterns (`httptest.NewRecorder`, `httptest.NewRequest`, `os.MkdirTemp` for temp dirs, `Eventually` for async assertions). Follow the same style for the new specs.
- `/home/node/.claude-yolo/plugins/marketplaces/coding/docs/go-glog-guide.md` — `V(n)` gating convention.
- `/home/node/.claude-yolo/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega patterns.
- `/home/node/.claude-yolo/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` — timer + context-cancel guard patterns for deterministic single-timer state.
- If any container doc path above is unreadable in the YOLO container, fall back to the inline constraints repeated in `<constraints>` below.
</context>

<requirements>

1. **Create a new file** `pkg/handler/trace_state.go` (package `handler`). This file owns the process-global trace-state primitive. It must NOT import `net/http` — the primitive is pure state with no HTTP coupling (the HTTP handlers land in prompt 2).

2. **Define the public API as a concrete type with these exact exported method names** (the spec freezes the semantics; the concrete type name is the agent's choice but must be a single struct type holding an `atomic.Bool` plus timer state):

   - `Enable()` — sets the trace-enabled flag to true; cancels any in-flight TTL timer; starts a fresh 5-minute TTL timer whose expiry calls `Disable()`.
   - `Disable()` — sets the trace-enabled flag to false; cancels any in-flight TTL timer so no later expiry can flip tracing back on.
   - `IsEnabled() bool` — returns the current trace-enabled flag value (atomic load).

   The constructor:

   ```go
   // NewTraceState returns a TraceState whose TTL window is the production
   // constant TraceTTLDefault (5 minutes). Tests use NewTraceStateWithTTL
   // with a shorter window.
   func NewTraceState() *TraceState
   ```

   plus a test-seam constructor mirroring `NewSetLoglevelHandlerWithRevert`:

   ```go
   // NewTraceStateWithTTL returns a TraceState whose TTL window is ttl.
   // Tests use a short ttl for fast expiry assertions; production uses
   // NewTraceState (TraceTTLDefault = 5 minutes).
   func NewTraceStateWithTTL(ttl time.Duration) *TraceState
   ```

3. **Define the TTL constant** mirroring the `setloglevel.go` naming convention (`SetLoglevelAutoRevert = 5 * time.Minute`):

   ```go
   const TraceTTLDefault = 5 * time.Minute
   ```

   Place it alongside the constructor. This is the frozen production value.

4. **`TRACE_TTL` test-only override.** The production constructor `NewTraceState()` MUST use `TraceTTLDefault` (5 minutes) and MUST NOT read the `TRACE_TTL` environment variable. The test-only override path: tests set `TRACE_TTL` env var AND call a constructor that reads it. To keep production code clean while making the env-var path genuinely exercisable from `package handler_test`, expose `traceTTLFromEnv` via an `export_test.go` re-export. Concretely:

   ```go
   // traceTTLFromEnv reads TRACE_TTL and returns the parsed duration, or
   // returns TraceTTLDefault if unset or unparseable. Intended for tests
   // that want to shorten the window without touching the production
   // constructor. Production code calls NewTraceState() which always
   // uses TraceTTLDefault.
   func traceTTLFromEnv() time.Duration
   ```

   AND add `pkg/handler/export_test.go` (package `handler_test`) with:
   ```go
   // export_test.go re-exports unexported symbols for testing.
   package handler_test

   import "time"

   // TraceTTLFromEnv exposes the unexported traceTTLFromEnv for tests.
   var TraceTTLFromEnv = traceTTLFromEnv
   ```

   This makes `traceTTLFromEnv` reachable from `handler_test` so a test can `t.Setenv("TRACE_TTL", "50ms")` then call `TraceTTLFromEnv()` — exercising the real env-var parsing path (a level-1 contract test that `TRACE_TTL` parsing works). Add a test-only constructor `NewTraceStateWithTTL(ttl time.Duration)` for tests that want to inject a duration directly without the env var. Production code calls `NewTraceState()` only — it never reads `TRACE_TTL`. The `TRACE_TTL` env var is a test convenience, NOT a production knob — the spec Non-goal "Do NOT make the 5-minute TTL configurable via config file, query param, or request body" is a hard veto; `TRACE_TTL` is internal-only and must not appear in any docs or production code path.

5. **Timer + cancellation semantics (Desired Behavior items 4, 5; Failure Modes rows 1, 3, 4, 7).** The primitive must guarantee:

   - **Exactly one live timer at any time.** Each `Enable()` call cancels the prior in-flight timer (call `timer.Stop()` / cancel the prior context) BEFORE starting a fresh timer. Repeated `Enable()` calls are idempotent on the flag (true either way) but reset the window deterministically — no overlapping timers, no "which timer wins" ambiguity.
   - **`Disable()` wins over a concurrent expiry.** If `Disable()` is called exactly as the TTL fires, the flag is false and the cancelled timer cannot flip it back on. Use a mutex (`sync.Mutex`) to serialize the cancel-and-restart sequence in `Enable()` and the cancel-and-clear sequence in `Disable()` so there is no goroutine leak (Failure Mode row 3) and no late-reset race (Failure Mode row 4).
   - **Expiry calls `Disable()`.** The TTL goroutine, on firing, calls `Disable()` — which sets the flag false and clears the timer handle. Do NOT call a separate "set flag false" path that bypasses `Disable()`; the expiry path MUST go through `Disable()` so the cancel-clear logic is shared.
   - **No persistent state.** On process crash mid-window, the flag and timer vanish by design (Failure Mode rows 1, 2). Do NOT write to disk, do NOT persist to any registry. Next boot starts with tracing off and no goroutine (Desired Behavior item 6).
   - **Clock skew / system suspend (Failure Mode row 7).** Use `time.Timer` (or `context.WithTimeout`) which is based on the monotonic clock — on resume the timer fires relative to wall time, which may turn tracing off earlier or later than wall-clock 5 minutes. This is the specified behavior; no NTP-aware correction.

   Implementation shape (the agent decides the exact struct fields, but this is the required contract): a `*TraceState` holds an `atomic.Bool` for the enabled flag, a `sync.Mutex` guarding timer lifecycle, a `*time.Timer` (or `context.CancelFunc`) for the in-flight timer, and the `ttl time.Duration` to start each fresh window. `Enable()` locks the mutex, stops the prior timer if non-nil, sets the atomic flag true, starts `time.AfterFunc(ttl, ts.Disable)`, stores the timer under the mutex, unlocks. `Disable()` locks the mutex, stops the timer if non-nil, nils the timer handle, sets the atomic flag false, unlocks. `IsEnabled()` does an atomic load without the mutex (the flag is the hot path consulted per request by the middleware in prompt 2 — it must not contend on the mutex).

6. **Process-global instance.** Expose a package-level default instance that the HTTP handlers (prompt 2) and the middleware (prompt 2) will consult:

   ```go
   // defaultTraceState is the process-global trace-state instance consulted
   // by the /enabletrace + /disabletrace handlers and by NewTraceMiddleware's
   // per-request IsEnabled() check. It is initialized once at package load;
   // a restart resets it to off (no persistence). Tests use
   // NewTraceStateWithTTL to build an isolated instance instead of mutating
   // the process-global default.
   var defaultTraceState = NewTraceState()
   ```

   Provide accessors so prompt 2 does not reach into the unexported var directly — the agent decides the exact accessor shape (e.g. an exported function returning `*TraceState`, or the handlers in prompt 2 close over the default). The key contract: there is exactly one process-global instance, tests build isolated instances via `NewTraceStateWithTTL`, and no test mutates the global default.

7. **glog discipline (Desired Behavior item 8; AC #8).** Any `Info`-level log emitted by the primitive MUST be gated behind `glog.V(n)` — NO bare `glog.Infof` / `glog.Info`. Use lowercase messages (e.g. `trace enabled via endpoint`, `trace disabled via endpoint`, `trace ttl expired`). `glog.Warningf` for unexpected errors (none expected in this primitive, but if a timer scheduling error surfaces, use `Warningf` with a lowercase message). The V(n) level: use `V(2)` to match the codebase convention for operator-opt-in detail (the existing `glog.V(2).Infof("trace enabled")` startup line in `factory.go` is the precedent). Suggested log points:
   - `Enable()`: `glog.V(2).Infof("trace enabled via endpoint")` — fires on each `/enabletrace` call.
   - `Disable()` when called by the handler (not by expiry): `glog.V(2).Infof("trace disabled via endpoint")`.
   - `Disable()` when called by the TTL expiry goroutine: `glog.V(2).Infof("trace ttl expired")`.

   To distinguish "handler called Disable" from "expiry called Disable", either (a) add a private `disable(origin string)` helper that both the handler-facing `Disable()` and the expiry goroutine call with different origin strings, or (b) have the expiry goroutine log its own line before calling `Disable()`. Pick one; do NOT add a separate exported method for the expiry path — the expiry MUST go through the shared `Disable()` to guarantee the cancel-clear logic runs.

8. **Add Ginkgo specs** in a new file `pkg/handler/trace_state_test.go` (package `handler_test`, matching `handler_suite_test.go`). Use `NewTraceStateWithTTL` with a short ttl (e.g. `50 * time.Millisecond`) for fast expiry assertions. Cover these cases (each maps to a spec AC or Failure Mode row):

   - **AC #4 (TTL auto-disable):** `Enable()` then wait past the ttl; assert `IsEnabled()` eventually returns `false` (use Gomega `Eventually` with a 2-second window and 10ms polling, mirroring `setloglevel_test.go`'s auto-revert spec pattern at the `auto-reverts the loglevel after the configured window` spec).
   - **AC #5 (disable mid-window cancels timer):** `Enable()` with a ttl of e.g. 1 second; immediately `Disable()`; wait 1.5 seconds (past the original window); assert `IsEnabled()` is STILL `false` (no late reset flipped it back on). This is the core "Disable wins over a concurrent expiry" assertion (Failure Mode row 4).
   - **AC #6 (repeated enable resets the window, exactly one timer):** Call `Enable()` N times (e.g. 5) in rapid succession with a ttl of 1 second each. Assert that after all calls, `IsEnabled()` is `true`, and after the LAST enable's ttl elapses, the flag flips to `false` exactly ONCE (not N times — the prior timers were cancelled, not expired). This is the observable-contract evidence for this prompt: at most one expiry fires, `IsEnabled()` transitions true→false exactly once after the last enable's ttl. NOTE: the spec AC text references "the prior timer's `Stop()` returned `true` (was still active) exactly once per reset" — `time.Timer.Stop()`'s return value is unreliable per stdlib docs (does not reliably indicate expiry state), so the `Stop()`-return evidence is satisfied at the prompt-2 INTEGRATION level (where the real mux + middleware run) via the observable single-expiry contract, NOT via a fragile test-only counter here. Do NOT invent a counter that mimics the stdlib internal; assert the observable contract.
   - **AC #8 (default state at boot):** A freshly constructed `NewTraceState()` (or `NewTraceStateWithTTL(...)`) has `IsEnabled() == false` and no TTL goroutine running. Assert `IsEnabled()` is `false` immediately after construction.
   - **Failure Mode row 3 (repeated concurrent enable calls):** Call `Enable()` concurrently from N goroutines (e.g. 20) with a 1-second ttl. Assert no panic, `IsEnabled()` is `true`, and after the ttl elapses `IsEnabled()` flips to `false` exactly once (no stacked expiries, no goroutine leak). Use a `sync.WaitGroup` to drive the concurrent enables.
   - **glog V(n) gating:** Add a static spec (mirror the existing one in `trace_test.go` that reads `trace.go` and greps for bare `glog.Infof`) that reads `trace_state.go` and asserts NO line contains `glog.Infof(` or `glog.Info(` without a preceding `glog.V(` on the same line. `glog.Warningf` is exempt from the V(n) gate.

9. **Run `make precommit`** in the repo root. Fix any gofmt / addlicense / lint / golangci-lint issues. All existing tests plus the new trace-state specs must pass. No HTTP handlers or middleware changes are in this prompt — only the state primitive.

</requirements>

<constraints>

- **Token-leak invariant (load-bearing, repeated from spec):** `Authorization` and `x-api-key` are never written raw to trace files. THIS prompt adds no file-writing code (the redaction lives in `trace.go` and is unchanged here), but the constraint governs prompt 2 and is restated so the agent has the full invariant set. No regression from v0.14.0 redaction behavior.
- **`CreateRouterFromConfig` signature unchanged:** `func CreateRouterFromConfig(ctx context.Context, cfg *pkg.Config, opts ...RouterOption) (http.Handler, error)`. Do NOT touch the factory in this prompt — prompt 2 wires the handlers + middleware.
- **`Config.Trace` deprecated, not removed:** `Config.Trace bool` (yaml `trace,omitempty`) stays parsed and functional. `trace: true` continues to emit on every `/v1/*` request regardless of the new atomic boolean (flag-OR-config in prompt 2). Do NOT remove, rename, or break the config field.
- **Trace independent of SIGHUP / `atomic.Pointer[http.Handler]` mux swap:** the new toggle uses a process-internal atomic flag. No SIGHUP dependency. Independent of spec 002-sighup-hot-reload (separate worktree/PR, not yet merged).
- **glog discipline:** any new `Info`-level log is `V(n)`-gated; no bare `glog.Infof`. Log messages are lowercase (`trace enabled via endpoint`, not `Trace Enabled`). `glog.Warningf` is exempt from the V(n) gate but still lowercase.
- **Frozen constant: 5-minute production TTL.** `TRACE_TTL` env var is test-only (internal, not a production knob). Production path (`NewTraceState()`) always uses `TraceTTLDefault = 5 * time.Minute` and never reads `TRACE_TTL`. Do NOT add a config-file or query-param tunable for the TTL (spec Non-goal hard veto).
- **`/setloglevel` pattern to mirror:** operator-local, no auth, short plaintext body, stdlib-mux-registered. The TTL constant + test-seam constructor convention (`SetLoglevelAutoRevert`, `NewSetLoglevelHandlerWithRevert`) is the model for `TraceTTLDefault` + `NewTraceStateWithTTL`.
- **Best-effort, no panic:** the primitive must not panic under concurrent enable/disable calls. The mutex serializes timer lifecycle.
- **Do NOT commit** — dark-factory handles git.
- **Existing tests must still pass.**
- **No HTTP handlers, no middleware changes, no factory changes, no config changes in this prompt.** Only the `trace_state.go` primitive + its Ginkgo specs.

</constraints>

<verification>

```bash
cd /workspace
make precommit
```

Must exit 0. Additionally:

```bash
# Trace-state primitive file exists
ls /workspace/pkg/handler/trace_state.go
# Specs exist
ls /workspace/pkg/handler/trace_state_test.go

# Public API present
grep -n 'func NewTraceState' /workspace/pkg/handler/trace_state.go
# Expect: NewTraceState() and NewTraceStateWithTTL(ttl time.Duration)
grep -n 'func .*Enable(' /workspace/pkg/handler/trace_state.go
grep -n 'func .*Disable(' /workspace/pkg/handler/trace_state.go
grep -n 'func .*IsEnabled(' /workspace/pkg/handler/trace_state.go

# TTL constant
grep -n 'TraceTTLDefault = 5 \* time.Minute' /workspace/pkg/handler/trace_state.go
# Expect: one match

# No bare glog.Infof / glog.Info( in trace_state.go (AC #8 / glog gating)
grep -nE 'glog\.Infof?\(' /workspace/pkg/handler/trace_state.go
# Expect: only lines with glog.V( prefix — zero bare glog.Infof( or glog.Info(

# Config.Trace untouched (still present, still yaml trace,omitempty)
grep -n 'Trace bool' /workspace/pkg/config.go
# Expect: one match, unchanged

# Factory untouched (no /enabletrace or /disabletrace registration yet)
grep -n 'enabletrace\|disabletrace' /workspace/pkg/factory/factory.go
# Expect: zero matches (prompt 2 adds these)

# trace.go untouched (redaction invariant holds)
grep -n '\*\*\*' /workspace/pkg/handler/trace.go
# Expect: redaction literal still present, unchanged
```

</verification>

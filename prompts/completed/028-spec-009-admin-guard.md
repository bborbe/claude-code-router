---
status: completed
spec: [009-inbound-api-key-auth]
summary: Added unconditional loopback guard (NewAdminLoopbackGuard) in front of the four state-changing admin routes (/setloglevel/, /enabletrace, /disabletrace, /gc) in buildMux, refusing non-loopback callers with 403 before handler logic; read-only endpoints stay open. Unit + real-mux integration Ginkgo tests cover bypass, remote refusal, spoof rejection, state-gating, refusal log line, and /gc side-effect gate.
execution_id: claude-code-router-exec-028-spec-009-admin-guard
dark-factory-version: dev
created: "2026-08-17"
queued: "2026-08-17T16:13:22Z"
started: "2026-08-17T16:47:40Z"
completed: "2026-08-17T16:57:03Z"
---

# Unconditional loopback guard for state-changing admin endpoints

<summary>
- Four state-changing admin routes refuse any non-loopback request with HTTP 403: `GET /setloglevel/<n>`, `POST /enabletrace`, `POST /disabletrace`, `POST /gc`.
- The guard is unconditional — there is no config knob to disable it.
- Read-only endpoints (`/healthz`, `/readiness`, `/metrics`, `HEAD /`) remain open to remote callers so health probes keep working.
- The guard runs before any handler logic, so a refused request never changes router state.
- Existing operator-local behavior (loopback) is unchanged for all endpoints.
</summary>

<objective>
Wire a single non-loopback 403 guard in front of the four state-changing admin routes registered in `buildMux`, so that once the listener moves to `0.0.0.0:8788` a remote attacker cannot toggle tracing (body-capture), force GC, or change log levels even if they are the only other caller.
</objective>

<context>
- Repo root is the current working directory.
- Read `pkg/factory/factory.go` — in `buildMux`, the four state-changing admin handlers are registered by the `mux.Handle` calls for `/setloglevel/`, `/enabletrace`, `/disabletrace` and `/gc` (~lines 211-214 today; prompt 2 edits this function first, so anchor on the `mux.Handle` calls, not the line numbers). The read-only registrations (`/healthz`, `/readiness`, `/metrics`, `HEAD /{$}`) must remain unguarded.
- Read `pkg/handler/setloglevel.go`, `enabletrace.go`, `disabletrace.go` for the existing handler shape — the guard wraps these via the `mux.Handle` calls, not by editing the handlers themselves.
- The `IsLoopbackRemoteAddr(string) bool` helper from prompt 1 is the predicate this prompt consumes. Grep for `func IsLoopbackRemoteAddr` first to learn its package path; use the correct import qualifier. It lives in `pkg/handler` per prompt 1's fixed file (`pkg/handler/loopback.go`), so import it as `handler.IsLoopbackRemoteAddr`.
- Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `bborbe/errors`, never `fmt.Errorf`.
- Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — lowercase log messages, `V(n)` gating.
- Read `docs/dod.md` — repo DoD: Ginkgo/Gomega, ≥80% coverage on new code, glog conventions.
- Read `pkg/factory/trace_wiring_test.go` — the exemplar for every test this prompt needs: build the real mux via `factory.CreateRouterFromConfig(ctx, cfg, factory.WithMetricsRegisterer(prometheus.NewRegistry()))`, override `HOME` to a temp dir before any trace-writing request, and use its `captureStderr` helper to assert a glog line.
- Read `pkg/handler/model-router.go` — `http.Error(w, "...", http.StatusX)` is this repo's error-response pattern (there is no `libhttp.WithError` usage anywhere in `pkg/`).
</context>

<requirements>
1. Create the guard at exactly `pkg/handler/admin_loopback_guard.go` with exactly the signature `func NewAdminLoopbackGuard(next http.Handler, isLoopback func(string) bool) http.Handler`. Both the file name and the constructor name are fixed — the wiring snippet in step 3 and the `<verification>` greps depend on them. The `isLoopback` injection mirrors the auth middleware from prompt 2 and keeps the guard testable.

2. Behavior:
   - On every request: read `r.RemoteAddr`. If `isLoopback(remoteAddr)` returns true → `next.ServeHTTP(w, r)` unchanged.
   - Otherwise → respond via `http.Error(w, "admin endpoint loopback-only", http.StatusForbidden)` — the repo's pattern (see `pkg/handler/model-router.go`); `http.Error` sets `Content-Type: text/plain; charset=utf-8` for you. Emit exactly one log line via `glog` — lowercase message like `admin refused path=<method+path> remote=<addr>`. Do not call `next.ServeHTTP`. No state change. No metric emission.

3. Wire the guard in `pkg/factory/factory.go` `buildMux`. The existing `mux.Handle("/setloglevel/", handler.NewSetLoglevelHandler())` etc. become wrapped calls:
   ```go
   mux.Handle("/setloglevel/", handler.NewAdminLoopbackGuard(handler.NewSetLoglevelHandler(), handler.IsLoopbackRemoteAddr))
   mux.Handle("/enabletrace",   handler.NewAdminLoopbackGuard(handler.NewEnableTraceHandler(),   handler.IsLoopbackRemoteAddr))
   mux.Handle("/disabletrace",  handler.NewAdminLoopbackGuard(handler.NewDisableTraceHandler(),  handler.IsLoopbackRemoteAddr))
   mux.Handle("/gc",            handler.NewAdminLoopbackGuard(libhttp.NewGarbageCollectorHandler(), handler.IsLoopbackRemoteAddr))
   ```
   Read-only routes (`/healthz`, `/readiness`, `/metrics`, `HEAD /{$}`) keep their existing direct registration — no guard.

4. Tests with Ginkgo v2 + Gomega. Two levels are required:

   **(a) Unit level** — in `pkg/handler`, exercise `NewAdminLoopbackGuard` directly with a counting fake inner handler and `httptest.NewRecorder()`.

   **(b) Integration level through the real mux** — `buildMux` is unexported and `pkg/factory` has no `export_test.go`; drive the production dispatch path from package `factory_test` via `factory.CreateRouterFromConfig(ctx, cfg, factory.WithMetricsRegisterer(prometheus.NewRegistry()))`, following `pkg/factory/trace_wiring_test.go`. Do NOT add an export seam for `buildMux`.

   Set the remote address by hand: `req := httptest.NewRequest(...)` then `req.RemoteAddr = "10.0.0.1:12345"`, and call `mux.ServeHTTP(rec, req)`. Do NOT use `httptest.NewServer` for the non-loopback cases — a real TCP client on the loopback interface can never produce a non-loopback `RemoteAddr`, so the remote-403 assertions would silently test the wrong thing.

   Cases:
   - **Loopback bypass:** for each of the four wrapped routes, a request from `127.0.0.1:12345` reaches the inner handler and produces the inner handler's response. No 403. For the `/setloglevel/` route use `GET /setloglevel/1` (not `/2`), so the loopback half of the test does not raise the process-global glog verbosity for up to the auto-revert window.
   - **IPv6 loopback bypass:** `[::1]:12345` likewise passes.
   - **Remote 403:** for each of the four wrapped routes (`GET /setloglevel/2`, `POST /enabletrace`, `POST /disabletrace`, `POST /gc`), a request from `10.0.0.1:12345` returns 403 with a `text/plain` body, and the inner handler is NOT called (counting fake at level (a)).
   - **Spoof rejected:** a request from `10.0.0.1:12345` carrying `X-Forwarded-For: 127.0.0.1` (and one carrying `X-Real-IP: ::1`) still returns 403 and still does not reach the inner handler.
   - **State-changing behaviour really is gated:** a remote `POST /enabletrace` through the real mux must leave `handler.DefaultTraceState().IsEnabled()` false; the same call from `127.0.0.1:12345` must flip it to true (this is AC 12's loopback half). Both assertions must run with `HOME` pointed at `GinkgoT().TempDir()` and with `DeferCleanup(handler.DefaultTraceState().Disable)` — see `pkg/factory/trace_wiring_test.go` for the `HOME`-override pattern. If you additionally assert on trace-file counts, the temp `HOME` is mandatory: `traceDir()` resolves `os.UserHomeDir()` and would otherwise write into the real home directory.
   - **`/gc` side-effect gated:** a remote `POST /gc` must NOT reach `libhttp.NewGarbageCollectorHandler()`. Assert via a counting fake inner handler at level (a): invocation count is 0 after the refused probe. Do NOT assert on `go_gc_duration_seconds_count` — the Go runtime can collect at any time from unrelated allocations in the same test binary, so that assertion flakes. The metrics-count evidence for AC 11 is operator-executable and stays on the spec's verification ladder.
   - **Refusal log line:** a remote refusal emits exactly one glog line containing `admin refused`, with the remote address as the last whitespace-separated field (the spec's detection command is `grep 'admin refused' … | awk '{print $NF}'`). Capture with the `captureStderr` helper pattern from `pkg/factory/trace_wiring_test.go`.
   - **Read-only endpoints unguarded:** `GET /healthz`, `GET /readiness`, `GET /metrics`, `HEAD /` from `10.0.0.1:12345` each return their normal response (200 or equivalent), NOT 403. This is the explicit carve-out the spec defends.

5. No counterfeiter mocks needed — collaborators are `http.Handler` (trivially faked) and `isLoopback` (injectable function).

6. Do not modify the inner handlers (`NewSetLoglevelHandler`, etc.). The guard wraps them; their logic is unchanged.

7. Do not modify the SIGHUP reload behavior. The guard is stateless and does not need reload plumbing.

8. Do not modify the read-only endpoints. Their registration lines in `buildMux` are untouched.
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Do NOT add a config knob to disable the guard. It is unconditional by spec.
- Do NOT log the presented key value (this prompt does not read keys, but keep the invariant loud).
- Do NOT emit metrics from the guard itself. Out of scope.
- Do NOT mutate `r` in place. Clone if you need to decorate headers — not expected here, but the rule holds.
- Do NOT read `X-Forwarded-For`, `X-Real-IP`, `Forwarded`, or any other client-supplied header when deciding loopback. The remote address comes from `r.RemoteAddr` (the connection) only — no trusted proxy sits in front of this router, so honouring a forwarded header would let any remote caller claim to be loopback and bypass the guard entirely (spec Security section).
- Use `github.com/bborbe/errors` for error wrapping (probably none needed in this prompt; the guard is purely a yes/no branch).
- Do NOT add new external dependencies. Stdlib `net/http` is sufficient.
- `make precommit` must remain green.
- New code in `pkg/handler/admin_loopback_guard.go` must reach ≥80% statement coverage per `docs/dod.md`.
- The guard's log line uses the same lowercase convention as the rest of the repo. Format: `admin refused path=<method+path> remote=<addr>` — concise, no key fields, no PII.
</constraints>

<verification>
make precommit

# Confirm the four endpoints are wrapped, not the read-only ones:
grep -n 'loopbackGuard\|NewAdminLoopbackGuard\|/healthz\|/readiness\|/metrics\|/gc' pkg/factory/factory.go

# Confirm no naive == compare or accidental auth-reading code slipped in:
grep -rn 'auth\.key\|x-router-key' pkg/handler/admin_loopback_guard.go pkg/factory/factory.go | grep -v _test.go

# Full suite:
go test ./pkg/... -count=1
</verification>
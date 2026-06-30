---
status: cancelled
spec: [002-sighup-hot-reload]
created: "2026-06-30T12:45:00Z"
queued: "2026-06-30T10:39:41Z"
cancelled: "2026-06-30T10:59:35Z"
---

<summary>
- The reloader test suite stops killing the Go test process at teardown.
- No stray SIGHUP reaches the default OS terminate action after Ginkgo exits.
- Tests that send real SIGHUPs drain the signal channel deterministically.
- The package-level signal interceptor survives the whole test process lifetime.
- The duplicate-metrics-registration warning spam is silenced in the reloader tests.
- make precommit runs the full suite clean (no signal: hangup, no FAIL).
</summary>

<objective>
Fix the `signal: hangup` process termination that fires after all reloader specs pass, so `make precommit` exits 0 and the docs prompt (011-spec-002-sighup-docs.md, currently `failed` on `status: partial`) can be requeued and complete. The bug is in the test's signal-interceptor lifecycle, not in the SIGHUP handler feature itself.
</objective>

<context>
Read CLAUDE.md at the repo root (if present).

Read these files before writing the fix — they define the current (broken) signal lifecycle:
- `pkg/reloader/reloader_test.go` — the test file. Key elements:
  - `var sighupInterceptor = make(chan os.Signal, 1)` (line 33) — package-level interceptor channel.
  - `func init()` (line 35) calls `signal.Notify(sighupInterceptor, syscall.SIGHUP)` — intended to swallow SIGHUPs so the process isn't terminated during tests.
  - `func registerSighup()` (line 41) calls `signal.Reset(syscall.SIGHUP)` — this clears ALL handlers including the interceptor, creating a race window.
  - `func restoreSighup()` (line 47) calls `signal.Reset(syscall.SIGHUP)` then re-registers the interceptor.
  - Tests that exercise `RunSighupLoop` call `defer restoreSighup()` (lines 508, 577).
  - Tests send real SIGHUPs via `syscall.Kill(syscall.Getpid(), syscall.SIGHUP)`.
- `pkg/reloader/reloader_suite_test.go` — the Ginkgo suite bootstrap (`TestReloader`, `RunSpecs`, `RegisterFailHandler(Fail)`, 60s timeout). No `AfterSuite` hook currently.
- `pkg/reloader/reloader.go` (from prompt 009) — the `Reloader` type under test. `RunSighupLoop(ctx)` registers its own `signal.Notify(ch, syscall.SIGHUP)` and loops on `<-sighup` / `<-ctx.Done()`.

Reference coding plugin docs (in-container path):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo `AfterSuite` + test isolation.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` — signal handling lifecycle.

Reproduction (verified):
```
cd /workspace
go test -mod=mod -count=1 ./pkg/reloader/...
```
All 10 specs show green `•` marks, then `signal: hangup` fires at process exit → `FAIL github.com/bborbe/claude-code-router/pkg/reloader 1.490s FAIL`. The SIGHUP is sent during a test (e.g. the rapid-repeat or ctx-cancel test), intercepted by `sighupInterceptor` (cap 1) — but if a second SIGHUP arrives while the channel is full, OR during the `signal.Reset` window in `registerSighup`/`restoreSighup`, the default OS terminate action fires. The process survives until Ginkgo tries to exit, then the pending signal kills it.
</context>

<requirements>
1. In `pkg/reloader/reloader_suite_test.go`, add an `AfterSuite` hook (Ginkgo v2 `AfterSuite(func() { ... })`) that deterministically drains and stops the package-level SIGHUP interceptor BEFORE the test process exits:
   - Call `signal.Stop(sighupInterceptor)` — unregisters the interceptor so no more SIGHUPs are delivered to it.
   - Call `signal.Reset(syscall.SIGHUP)` — restores the default OS disposition (the test process is exiting anyway; this prevents a queued SIGHUP from firing during exit).
   - Non-blocking drain of any pending SIGHUP in `sighupInterceptor`: `for { select { case <-sighupInterceptor: continue: default: return } }` — empties the buffered channel (cap 1) so the signal doesn't surface at exit.
   - The `AfterSuite` must run even if a spec panics (Ginkgo guarantees `AfterSuite` runs after `RunSpecs` returns).

2. In `pkg/reloader/reloader_test.go`, fix `registerSighup()` and `restoreSighup()` so they NEVER leave a window where SIGHUP has no handler. The current `signal.Reset(syscall.SIGHUP)` calls (in both functions) clear ALL handlers including the package-level interceptor — that clear window is the race. Verified: `RunSighupLoop` in `pkg/reloader/reloader.go` ALREADY does `defer signal.Stop(sighup)` on its own channel (line 123), so per-loop cleanup is handled — no `reloader.go` change needed. The fix is test-only:
   - Make `registerSighup()` a no-op (the interceptor stays registered throughout the test process; `RunSighupLoop`'s own `signal.Notify` is additive — both subscribers receive SIGHUP, which is fine).
   - Make `restoreSighup()` a no-op OR keep only the re-registration if needed for clarity — but REMOVE the `signal.Reset(syscall.SIGHUP)` call that clears the interceptor. Since `RunSighupLoop` already `signal.Stop`s its own channel on exit, the interceptor is the only remaining subscriber after each test.
   - Net effect: the package-level `sighupInterceptor` is registered ONCE in `init()` and stays registered for the entire test process. No `signal.Reset` calls that clear it. The per-test `RunSighupLoop` channels come and go additively, each cleaning up after itself.

3. Silence the `duplicate metrics collector registration attempted` warning spam (glog WARNING from `pkg/factory/factory.go:123`) in the reloader tests. Verified: `CreateRouterFromConfig` hardcodes `prometheus.DefaultRegisterer` (line 123) — there is no registry-injection seam. So the test-isolation pattern is to reset the default registerer in a `BeforeEach`:
   - In `pkg/reloader/reloader_test.go`, add a top-level `BeforeEach` (or one inside each `Describe`) that does: `prometheus.DefaultRegisterer = prometheus.NewRegistry()`. This gives each spec a fresh registry so `metrics.Register(...)` succeeds instead of warning about duplicates.
   - Add the `prometheus` import (`"github.com/prometheus/client_golang/prometheus"`) to the test file.
   - The warning is non-fatal (the handler constructs fine), but silencing it keeps the test output clean and matches the metrics-reset pattern used in `pkg/handler/model-router_test.go` if present there.

4. Run the full suite (not just `pkg/reloader` in isolation) to confirm the fix holds when all packages run together:
   ```
   make test
   ```
   The `signal: hangup` must NOT appear. Exit 0.

5. Run `make precommit` — must exit 0. This is the gate that the docs prompt (011) failed.
</requirements>

<constraints>
- Do NOT change the SIGHUP handler feature behavior in `pkg/reloader/reloader.go`. Verified: `RunSighupLoop` already does `defer signal.Stop(sighup)` (line 123) — per-loop cleanup is correct. The fix is entirely test-side.
- Do NOT mock `pkg.Load` — the real reload path stays exercised.
- Do NOT remove the real-SIGHUP test assertions (`syscall.Kill(syscall.Getpid(), syscall.SIGHUP)`). The tests MUST exercise the real signal path; the fix is about teardown hygiene, not about faking the signal.
- Token-leak invariant still holds: no `cfg %+v` in any new log lines.
- glog discipline: any new `Infof` gated behind `glog.V(n)` (n>=1). Lowercase messages.
- Do NOT add a per-feature opt-out flag or debounce.
- Do NOT commit — dark-factory handles git.
- `make precommit` remains the single gate.
- Existing tests in `pkg/config_test.go`, `pkg/factory/factory_suite_test.go`, `pkg/handler/*_test.go` must continue to pass.
</constraints>

<verification>
Run `make precommit` in the repo root — must exit 0.

Confirm the signal-lifecycle fix:
```
grep -n 'AfterSuite' pkg/reloader/reloader_suite_test.go          # must show the AfterSuite hook
grep -n 'signal.Stop\|signal.Reset' pkg/reloader/reloader_test.go  # must NOT show signal.Reset on the SIGHUP path (signal.Stop is fine)
go test -mod=mod -count=1 ./pkg/reloader/... 2>&1 | grep -c 'signal: hangup'   # must return 0
```

Confirm the metrics-reset fix:
```
grep -n 'prometheus.NewRegistry\|DefaultRegisterer' pkg/reloader/reloader_test.go pkg/reloader/reloader_suite_test.go  # must show the reset
go test -mod=mod -count=1 ./pkg/reloader/... 2>&1 | grep -c 'duplicate metrics collector'  # must return 0 (or only from non-reloader packages)
```
</verification>

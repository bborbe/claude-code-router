---
status: completed
spec: ["002"]
summary: 'Implemented reloader tests for SIGHUP config reload: AC-2 (provider count increment), AC-3 (invalid YAML rejection), AC-4 (log line shape + token-leak guard), AC-5 (in-flight isolation), AC-9 (SIGHUP does not cancel ctx), plus error-path coverage for deleted file, validation failure, rapid reloads, panic recovery, and ctx-cancelled no-op. Also fixed factory.go to make metrics registration non-fatal and added Warningf logging in Reload for error-path capture.'
execution_id: claude-code-router-sighup-reload-exec-010-spec-002-sighup-tests
dark-factory-version: dev
created: "2026-06-30T10:15:00Z"
queued: "2026-06-30T09:24:34Z"
started: "2026-06-30T09:28:53Z"
completed: "2026-06-30T10:35:40Z"
---

<summary>
- A Ginkgo test asserts that SIGHUP triggers a config reload and the provider count observable via `ConfigSnapshot()` changes from N to M.
- A negative test asserts that an invalid-YAML config on SIGHUP leaves the provider count unchanged, logs a `config reload failed` line, and does NOT log a `config reloaded` line.
- An integration test asserts that an in-flight HTTP request started before SIGHUP completes against the OLD provider (response body marker from the first handler, not the second).
- A test asserts that SIGHUP does NOT cancel the run context (`ctx.Err()` stays `nil`), while SIGINT cancels it within 100ms as the control case.
- A test asserts the reload-success log line shape `config reloaded old_providers=N new_providers=M` and that no token value (`Bearer`/`sk-`/`token:`) appears in the log.
- Tests use the existing Ginkgo v2 + Gomega suite pattern from `pkg/factory/factory_suite_test.go` and `pkg/handler/model-router_test.go`.
</summary>

<objective>
Validate every behavior the spec's Acceptance Criteria assert: reload increments provider count, invalid YAML keeps old count, in-flight requests finish on the old handler, SIGHUP never cancels ctx, and the log line shape is correct and token-free. These tests exercise the `Reloader` + `ConfigSnapshot()` accessor shipped by prompt 1.
</objective>

<context>
Read CLAUDE.md at the repo root and every ancestor up to `~/Documents/workspaces/` for project conventions (if present).

Prompt 1 (`1-spec-002-sighup-handler.md`) MUST be implemented first — it ships the `Reloader` type, `NewReloader`, `Reload`, `RunSighupLoop`, `ServeHTTP`, and `ConfigSnapshot()` in `pkg/reloader/reloader.go` (package `reloader`, a new sibling under `pkg/`), plus the `CreateServer` wiring in `pkg/factory/factory.go`.

Read these source files before writing tests:
- `pkg/reloader/reloader.go` (from prompt 1) — the type under test. Key API: `func NewReloader(cfgPath string, initial http.Handler, build func(ctx context.Context, cfg *pkg.Config) (http.Handler, error)) *Reloader`, `func (r *Reloader) SeedConfig(cfg *pkg.Config)`, `func (r *Reloader) Reload(ctx context.Context) error`, `func (r *Reloader) RunSighupLoop(ctx context.Context)`, `func (r *Reloader) ServeHTTP(w http.ResponseWriter, req *http.Request)`, `func (r *Reloader) ConfigSnapshot() *pkg.Config`. The `build` arg is `factory.CreateRouterFromConfig` injected to avoid an import cycle (see prompt 1).
- `pkg/factory/factory.go` — `func CreateRouterFromConfig(ctx context.Context, cfg *pkg.Config) (http.Handler, error)` and `func CreateServer(ctx context.Context, listen, configPath string) (librun.Func, error)`. Tests may call `CreateRouterFromConfig` directly to build an initial handler for a given config without going through the full server, and pass it as the `build` arg to `NewReloader`.
- `pkg/factory/factory_suite_test.go` — the Ginkgo suite bootstrap pattern (package `factory_test`, 60s timeout, `RegisterFailHandler(Fail)`). **Do NOT add to `factory_suite_test.go`** — the new tests target package `reloader`, so create a NEW suite bootstrap `pkg/reloader/reloader_suite_test.go` in package `reloader_test` (with its own `TestReloader` + `RunSpecs` + `RegisterFailHandler(Fail)`). See `go-testing-guide.md` on per-package Ginkgo suites.
- `pkg/handler/model-router_test.go` — existing Ginkgo patterns: `captureStderr` helper (lines 48-62) for asserting on glog output, `labelHandler(label string) http.Handler` (lines 38-43) for injecting a body marker. **`captureStderr` is an unexported helper in package `handler_test` — it is NOT visible to the new `reloader_test` package.** Copy the helper (a small `func() string` that swaps `os.Stderr` + `glog`'s output) into the new `reloader_test` file, OR replicate the pattern inline. Do not rely on cross-package visibility that does not exist.
- `pkg/config.go` — `Config` struct (lines 29-38): `Providers map[string]Provider`, `Router.DefaultProvider string`, `Aliases map[string]string`. `Provider` (lines 48-59): `Upstream string`, `Token string`, `Models []string`. Tests construct `*pkg.Config` literals directly (the existing tests do — see `pkg/config_test.go`).
- `pkg/config_test.go` — existing `*pkg.Config` literal construction idiom for test fixtures.

Reference coding plugin docs (in-container path):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo + Gomega conventions.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — glog capture in tests.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` — `Eventually`/`Consistently` over goroutine state.
</context>

<requirements>
1. Create `pkg/reloader/reloader_suite_test.go` (package `reloader_test`) as the Ginkgo suite bootstrap — `TestReloader` + `RunSpecs` + `RegisterFailHandler(Fail)`, following the pattern from `pkg/factory/factory_suite_test.go`. Then create `pkg/reloader/reloader_test.go` (also package `reloader_test`) for the `Describe` blocks.

2. Test: SIGHUP reload increments provider count (AC 2). Write a `Describe("Reloader SIGHUP reload", ...)` block:
   - `BeforeEach`: write a 1-provider config YAML to a temp file (use `os.CreateTemp`); build an initial handler via `CreateRouterFromConfig(ctx, cfg)`; construct `reloader := NewReloader(tmpPath, initialHandler, CreateRouterFromConfig)`; seed via `reloader.SeedConfig(cfg)`; start `go reloader.RunSighupLoop(ctx)` with a cancellable `context.Background()`; cancel in `AfterEach`.
   - `It("reloads from N to M providers on SIGHUP", func() { ... })`:
     - Assert starting state: `Expect(reloader.ConfigSnapshot().ProviderCount... )` — note: there is no `ProviderCount()` method; assert on `len(reloader.ConfigSnapshot().Providers)` which equals 1.
     - Overwrite the temp file with a 2-provider YAML.
     - Send SIGHUP to self: `Expect(syscall.Kill(syscall.Getpid(), syscall.SIGHUP)).To(Succeed())`.
     - Assert: `Eventually(func() int { return len(reloader.ConfigSnapshot().Providers) }).Should(Equal(2))` within the suite timeout (use `Eventually(...).WithTimeout(2*time.Second).Should(Equal(2))` for tightness).
   - Both YAML fixtures must have valid `default_provider` referencing an existing provider key and valid `Models` globs (see `pkg/config.go` `Validate`). Use distinct provider keys so the count change is unambiguous.

3. Test: invalid YAML on SIGHUP keeps old count + logs failure + does NOT log success (AC 3). Add to the same `Describe`:
   - `It("rejects invalid YAML and keeps old config", func() { ... })`:
     - Start with a valid 1-provider config; assert `len(reloader.ConfigSnapshot().Providers) == 1`.
     - Overwrite the temp file with invalid YAML: `broken: : :` (a YAML parse error).
     - Capture glog output via the `captureStderr` helper pattern from `pkg/handler/model-router_test.go` (lines 48-62) — wrap the SIGHUP send + a short `Eventually` wait in `captureStderr(func(){ ... })`. glog must be flushed (`glog.Flush()`) inside the captured block.
     - Send SIGHUP: `syscall.Kill(syscall.Getpid(), syscall.SIGHUP)`.
     - Assert: `Consistently(func() int { return len(reloader.ConfigSnapshot().Providers) }).WithTimeout(1*time.Second).Should(Equal(1))`.
     - Assert the captured stderr contains `config reload failed` (`Expect(captured).To(ContainSubstring("config reload failed"))`).
     - Assert the captured stderr does NOT contain `config reloaded` (`Expect(captured).NotTo(ContainSubstring("config reloaded"))`).

4. Test: in-flight request finishes on the old handler (AC 5). Add an integration `Describe("Reloader in-flight isolation", ...)`:
   - Build a slow handler that blocks until a test channel is closed, then writes a body marker `"old"`: `http.HandlerFunc(func(w, r){ <-block; w.WriteHeader(200); w.Write([]byte("old")) })`.
   - Build a fast handler that writes `"new"`: `http.HandlerFunc(func(w, r){ w.WriteHeader(200); w.Write([]byte("new")) })`.
   - Construct a `Reloader` with the slow handler as initial.
   - Start an HTTP request to the reloader in a goroutine (`httptest.NewServer(reloader)` then `http.Get`).
   - While the request is in-flight (blocked), call `reloader.Reload(ctx)` with a fresh config whose `CreateRouterFromConfig` produces a handler that serves `"new"`. Drive through the real `Reload(ctx)` path with a real temp config file — do NOT shortcut via `reloader.handler.Store(fastHandler)`; the test must traverse the real `pkg.Load` + `CreateRouterFromConfig` + atomic-swap boundary the spec's Desired Behavior item 3 mandates.
   - Close the block channel so the in-flight request completes.
   - Assert the in-flight response body is `"old"` (the pre-reload handler), NOT `"new"`.
   - Then send a SECOND request to the reloader and assert its body is `"new"` — proving the swap actually took effect for subsequently-accepted connections.
   - Root-cause framing: this test traverses the atomic-pointer swap boundary with a real concurrent request, catching the bug class "swap affects in-flight requests" which a pure shape test would miss.

5. Test: SIGHUP does not cancel ctx; SIGINT does (AC 9). Add `Describe("Reloader context cancellation", ...)`:
   - `It("SIGHUP does not cancel ctx, SIGINT does", func() { ... })`:
     - Use a context built via `run.ContextWithSig(context.Background())` (import `librun "github.com/bborbe/run"`). This is the SAME function `service.MainCmd` uses.
     - Start `go reloader.RunSighupLoop(ctx)`.
     - Send SIGHUP: `syscall.Kill(syscall.Getpid(), syscall.SIGHUP)`.
     - Assert: `Consistently(func() error { return ctx.Err() }).WithTimeout(500*time.Millisecond).Should(BeNil())`.
     - Send SIGINT: `syscall.Kill(syscall.Getpid(), syscall.SIGINT)`.
     - Assert: `Eventually(func() error { return ctx.Err() }).WithTimeout(100*time.Millisecond).ShouldNot(BeNil())`.
   - NOTE: Ginkgo's own runner intercepts SIGINT, which can make the SIGINT control case flaky. If SIGINT is unreliable under Ginkgo, mark ONLY the SIGINT control case `Pending` with a comment — keep the SIGHUP-does-not-cancel assertion running with a real `syscall.SIGHUP`. The SIGHUP `Consistently(...).Should(BeNil())` is the load-bearing invariant and is non-negotiable. Do NOT substitute a different signal for SIGHUP — the test must exercise the real SIGHUP path.

6. Test: reload-success log line shape + token-leak guard (AC 4). Add to the reload `Describe`:
   - `It("logs config reloaded with provider counts and no token", func() { ... })`:
     - Capture stderr around a successful SIGHUP reload (1-provider -> 2-provider).
     - Assert the captured output matches the regex `config reloaded old_providers=[0-9]+ new_providers=[0-9]+` (use `MatchRegexp` or compile a `regexp.MustCompile`).
     - Assert `old_providers=1` and `new_providers=2` specifically.
     - Assert the captured output does NOT contain `Bearer`, `sk-`, or `token:` (token-leak guard). Use a negative match: `Expect(captured).NotTo(MatchRegexp("(?i)Bearer|sk-|token:"))`.
   - Root-cause framing: this test traverses the logging boundary with a real `Config` containing a `Token` field, catching the bug class "config dumped wholesale to logs".

7. ERROR-PATH COVERAGE: ensure each failure mode row has a test (the invalid-YAML test covers row 2). Add at minimum:
   - `It("rejects a deleted config file and keeps old config", ...)` — delete the temp file, send SIGHUP, assert `Consistently(len(Providers)).Should(Equal(N))` and captured stderr contains `config reload failed`. (Failure Modes row 1.)
   - `It("rejects a config failing Validate and keeps old config", ...)` — write a config where `default_provider` references a missing provider key (see `pkg/config.go` `Validate` line 90-95). Send SIGHUP, assert count unchanged and `config reload failed` in stderr. (Failure Modes row 3.)
   - `It("rapid repeated SIGHUP triggers independent reloads", ...)` — with an unchanged valid config, send SIGHUP twice in quick succession; assert TWO `config reloaded old_providers=N new_providers=N` lines appear in captured stderr (no coalescing — the spec's DB 7 / Non-goal "one SIGHUP = one reload attempt"). (Failure Modes row 5.)
   - `It("SIGHUP after ctx cancel is a no-op", ...)` — cancel `ctx`, then send SIGHUP; assert NO `config reloaded` line appears in captured stderr (the reload goroutine has exited with the context). (Failure Modes row 6.)
   - The panic-recovery row (Failure Modes row 7) is hard to trigger deterministically without injecting a fault. If `CreateRouterFromConfig` cannot be made to panic from a valid config, add a focused unit test on the `reloadWithRecover` wrapper by injecting a `build` function that panics (e.g. `func(ctx, cfg) (http.Handler, error) { panic("test") }`) passed to `NewReloader` — this exercises the recover path directly without mocking `pkg.Load`. Do NOT mock `pkg.Load`; only the `build` fn is injectable.
</requirements>

<constraints>
- Language, logging, test, and gate stack are frozen: Go, `github.com/golang/glog` (V(n)-gated INFO), Ginkgo v2 + Gomega, `make precommit` as the gate.
- `pkg.Load(ctx, path) (*Config, error)` already performs read + tilde expansion + YAML parse + `Validate`. Reuse it verbatim; do NOT mock it in tests (the spec mandates reusing the real loader on the reload path, and tests must traverse that same path).
- `run.ContextWithSig(ctx)` registers `signal.Notify` for `os.Interrupt, syscall.SIGINT, syscall.SIGTERM` and cancels the context on receipt — it does NOT register SIGHUP. Tests assert this invariant directly.
- The mux-swap strategy is the frozen swap mechanism: rebuild the entire `http.Handler` via `CreateRouterFromConfig` on each successful SIGHUP, then swap the `atomic.Pointer[http.Handler]`.
- `NewModelRouter` captures `routes`, `defaultHandler`, and `aliases` at construction time. Tests relying on a fresh model router per reload must construct via `CreateRouterFromConfig` (which builds a fresh `NewModelRouter`).
- Token-leak invariant: reload logging emits provider COUNTS only. Tests assert the log line contains no `Bearer`/`sk-`/`token:` substring.
- glog discipline: every new `Infof` is gated behind `glog.V(n)` (n>=1). Lowercase log messages.
- Do NOT add a per-feature opt-out flag or a tunable debounce/coalesce window.
- Do NOT commit — dark-factory handles git.
- Existing tests in `pkg/config_test.go`, `pkg/factory/factory_suite_test.go`, `pkg/handler/*_test.go` must continue to pass unchanged.
- `make precommit` remains the single gate.
</constraints>

<verification>
Run `make precommit` in the repo root — must exit 0.

Confirm the SIGHUP-does-not-cancel-ctx assertion exists:
```
grep -n 'syscall.SIGHUP' pkg/reloader/reloader_test.go
grep -n 'Consistently.*ctx.Err' pkg/reloader/reloader_test.go
```

Confirm the token-leak negative assertion exists:
```
grep -n 'Bearer\|sk-\|token:' pkg/reloader/reloader_test.go
```
</verification>

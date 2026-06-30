---
status: completed
spec: ["002"]
summary: 'Implemented SIGHUP-driven hot config reload: created pkg/reloader/reloader.go with Reloader type (atomic.Value handler swap + dedicated SIGHUP loop), wired into CreateServer in pkg/factory/factory.go'
execution_id: claude-code-router-sighup-reload-exec-009-spec-002-sighup-handler
dark-factory-version: dev
created: "2026-06-30T10:15:00Z"
queued: "2026-06-30T09:24:30Z"
started: "2026-06-30T09:24:32Z"
completed: "2026-06-30T09:28:52Z"
---

<summary>
- Operators can send SIGHUP to the running router to pick up config edits without a process restart.
- The router rebuilds its entire request-dispatch handler tree from the freshly loaded config and atomically swaps it in; in-flight requests finish against the old tree undisturbed.
- A malformed config (missing file, invalid YAML, validation failure) is rejected; the old config stays active and an error is logged.
- A successful reload logs one INFO line with old/new provider counts; a failed reload logs a WARNING with the error. Token values are never logged.
- The run context is never cancelled by SIGHUP — only SIGINT/SIGTERM cancel it. SIGHUP received after shutdown begins is a no-op.
- A panic during mux rebuild is recovered so it never crashes the process; the old config stays active.
- No file-watcher, no HTTP reload endpoint, no debounce, no opt-out flag.
</summary>

<objective>
Implement the SIGHUP-driven hot config reload: a dedicated signal channel, a reload goroutine that reuses `pkg.Load` + `CreateRouterFromConfig`, and an `atomic.Pointer[http.Handler]` swap mechanism that the HTTP server dispatches through. This is the load-bearing mechanism that makes config edits live without dropping connections.
</objective>

<context>
Read CLAUDE.md at the repo root and every ancestor up to `~/Documents/workspaces/` for project conventions (if present).

Read these source files before writing any code — they define the frozen signatures and existing patterns you must follow:
- `pkg/config.go` — `func Load(ctx context.Context, rawPath string) (*Config, error)` (lines 63-80). REUSE VERBATIM on the reload path; do NOT extract a second loader or a second `Validate`. Do NOT touch `Load`'s signature.
- `pkg/factory/factory.go` — `func CreateServer(ctx context.Context, listen, configPath string) (librun.Func, error)` (lines 29-39) and `func CreateRouterFromConfig(ctx context.Context, cfg *pkg.Config) (http.Handler, error)` (lines 72-152). `CreateServer` currently calls `pkg.Load` once, builds the router, and returns `libhttp.NewServer(listen, router, streamingServerTimeouts)`.
- `pkg/handler/model-router.go` — `func NewModelRouter(routes []handler.ModelRoute, defaultProviderName string, defaultHandler http.Handler, aliases map[string]string, sampler liblog.Sampler, metrics *handler.Metrics, currentDateTime libtime.CurrentDateTimeGetter) http.Handler` (lines 80-88). `NewModelRouter` captures `routes`, `defaultHandler`, and `aliases` at construction time. A fresh `NewModelRouter` per reload is correct — the old instance keeps serving its in-flight requests against its captured closures.
- `pkg/cli.go` — `type ServerFactory func(ctx context.Context, listen, configPath string) (librun.Func, error)` (line 26) and `func (a *App) Run(ctx context.Context) error` (line 44). `App.Run` calls `a.serverFactory(ctx, ...)` then `runner(ctx)`. The `ctx` received by `Run` is `run.ContextWithSig(ctx)` (canceled on SIGINT/SIGTERM) — see `service.MainCmd` at `github.com/bborbe/service` which calls `app.Run(run.ContextWithSig(ctx))`.
- `main.go` — wires `pkg.NewApp(factory.CreateServer)` via `service.MainCmd`. No change needed here.

Library APIs (verified):
- `github.com/bborbe/run` — `type Func func(context.Context) error`. `func ContextWithSig(ctx context.Context) context.Context` registers `signal.Notify` for `os.Interrupt, syscall.SIGINT, syscall.SIGTERM` and cancels the context on receipt. It does NOT register SIGHUP.
- `github.com/bborbe/http` — `func NewServer(addr string, router http.Handler, optionFns ...func(*ServerOptions)) run.Func`. The returned `run.Func` runs the server and shuts down on `ctx.Done()`.
- `github.com/bborbe/errors` — `errors.Wrapf(ctx, err, "format", args...)` and `errors.New(ctx, "msg")` are the existing wrapping idioms (see `pkg/config.go`, `pkg/factory/factory.go`).
- `github.com/golang/glog` — `glog.V(n).Infof(...)`, `glog.Warningf(...)`, `glog.Errorf(...)`. Every new `Infof` MUST be gated behind `glog.V(n)` with n>=1.

Reference coding plugin docs (in-container path, since prompts execute inside a YOLO container):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — glog V(n) gating discipline.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `bberrors.Wrapf` / `errors.New` idioms.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` — goroutine lifecycle + recover.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md` — factory wiring conventions.
</context>

<requirements>
1. Create a new file `pkg/reloader/reloader.go` in package `reloader` containing a `Reloader` type that owns the atomic handler swap and the SIGHUP reload loop. The `reloader` package is a NEW sibling under `pkg/` — implementation types MUST NOT live in `pkg/factory/` per `go-factory-pattern.md` §3 ("Implementation types MUST NOT live inside `pkg/factory/`; the factory package is wiring-only"). Only the `CreateServer` wiring change stays in `pkg/factory/factory.go`. The type must have this shape (field names exact, method signatures exact):

   ```go
   // Reloader holds the atomic request-dispatch handler that the HTTP
   // server serves through, plus the SIGHUP-driven reload loop. On each
   // successful SIGHUP the entire mux is rebuilt via CreateRouterFromConfig
   // and atomically swapped; in-flight requests already inside the old
   // handler tree finish against it. A failed reload leaves the old
   // handler pointer intact.
   type Reloader struct {
       handler atomic.Pointer[http.Handler]
       cfgPath string
   }
   ```

   - `func NewReloader(cfgPath string, initial http.Handler) *Reloader` — constructs the `Reloader` and stores `initial` via `handler.Store(initial)`.
   - `func (r *Reloader) ServeHTTP(w http.ResponseWriter, req *http.Request)` — loads the current handler via `r.handler.Load()` and calls its `ServeHTTP`. This is the `http.Handler` that `libhttp.NewServer` dispatches through.
   - `func (r *Reloader) Reload(ctx context.Context) error` — performs ONE reload attempt: calls `pkg.Load(ctx, r.cfgPath)`, then `r.build(ctx, cfg)` (the injected `CreateRouterFromConfig`), then `r.handler.Store(newRouter)`. Returns the error from `Load` or `r.build` (do NOT swallow). On error the old handler stays active (no `Store` call). Capture the OLD provider count (`len(oldCfg.Providers)`) BEFORE the swap for the log line — expose the old config via a second `atomic.Pointer[pkg.Config]` field on `Reloader` named `current atomic.Pointer[pkg.Config]`, stored alongside the handler on every successful reload (and seeded with the initial config via `SeedConfig`). After a successful swap log exactly one line at `glog.V(1)`:
     `glog.V(1).Infof("config reloaded old_providers=%d new_providers=%d", oldCount, newCount)`
     where `oldCount = len(oldCfg.Providers)` and `newCount = len(cfg.Providers)`.
   - `func (r *Reloader) ConfigSnapshot() *pkg.Config` — returns `r.current.Load()`. This is the test-accessible accessor (used by prompt 2's tests). Returns the active config; nil only before the first successful load (not a real state since `NewReloader` seeds it).

   Note on imports: the `reloader` package imports `github.com/bborbe/claude-code-router/pkg` (as `pkg`) and `github.com/bborbe/claude-code-router/pkg/factory` (as `factory`) — the latter for `factory.CreateRouterFromConfig` (see requirement 3). Avoid an import cycle: `pkg/factory` imports `pkg/reloader` for the `NewReloader`/`RunSighupLoop` wiring, and `pkg/reloader` imports `pkg/factory` for `CreateRouterFromConfig`. **If this cycle blocks compilation, move `CreateRouterFromConfig` to a neutral package (`pkg/router` or inline its logic into `pkg/reloader`), OR have `CreateServer` pass `CreateRouterFromConfig` as a `func(ctx, *pkg.Config) (http.Handler, error)` argument into `NewReloader` so `reloader` does not import `factory` — the function-argument approach is preferred (no package move, no cycle).** Pick the function-argument approach unless the repo already has a cycle-free seam.

2. In `pkg/reloader/reloader.go`, add the SIGHUP loop function:

   ```go
   // RunSighupLoop blocks until ctx is cancelled, handling SIGHUP. On each
   // SIGHUP it calls r.Reload(ctx). A reload error is logged at WARNING
   // with the message `config reload failed: <err>` and the old config is
   // retained. A panic during reload is recovered (no goroutine leak, no
   // process crash) and logged at ERROR. SIGHUP NEVER cancels ctx; only
   // SIGINT/SIGTERM (handled by run.ContextWithSig upstream) cancel it.
   func (r *Reloader) RunSighupLoop(ctx context.Context)
   ```

   Implementation contract:
   - Register a DEDICATED channel: `sighup := make(chan os.Signal, 1); signal.Notify(sighup, syscall.SIGHUP)`. Do NOT reuse the `run.ContextWithSig` channel. The dedicated channel is required by AC "router installs its own `signal.Notify(ch, syscall.SIGHUP)` on a dedicated channel before the HTTP server starts".
   - Loop on a `select` over `<-sighup` and `<-ctx.Done()`.
   - On `<-sighup`: call `r.reloadWithRecover(ctx)` (a private helper wrapping `r.Reload(ctx)` in `defer recover()`). On `<-ctx.Done()`: return (exit the goroutine; subsequent SIGHUP is a no-op because the goroutine has exited).
   - The `reloadWithRecover` helper: `defer func() { if r := recover(); r != nil { glog.Errorf("config reload panic: %v", r) } }()` then call `r.Reload(ctx)`. If `Reload` returns an error, log `glog.Warningf("config reload failed: %v", err)`. NOTE the lowercase message: `config reload failed` (not `Config reload failed`).
   - One SIGHUP = exactly one `Reload` call. No debounce, no coalescing, no "already reloaded" short-circuit (spec Non-goal + DB 7).

3. Wire the `Reloader` into `CreateServer` in `pkg/factory/factory.go`. The current `CreateServer` body (lines 29-39) is:

   ```go
   func CreateServer(ctx context.Context, listen, configPath string) (librun.Func, error) {
       cfg, err := pkg.Load(ctx, configPath)
       if err != nil {
           return nil, errors.Wrapf(ctx, err, "load config")
       }
       router, err := CreateRouterFromConfig(ctx, cfg)
       if err != nil {
           return nil, errors.Wrapf(ctx, err, "build router")
       }
       return libhttp.NewServer(listen, router, streamingServerTimeouts), nil
   }
   ```

   Change ONLY the wiring after the router build — keep `pkg.Load` + `CreateRouterFromConfig` calls and their error wrapping unchanged. To avoid the import cycle described in requirement 1 (`pkg/factory` ↔ `pkg/reloader`), pass `CreateRouterFromConfig` into `NewReloader` as a function argument so `pkg/reloader` never imports `pkg/factory`. The new tail:

   ```go
   reloader := reloader.NewReloader(configPath, router, CreateRouterFromConfig)
   reloader.SeedConfig(cfg) // seed active config for ConfigSnapshot (exported method OR via NewReloader arg — pick one; SeedConfig is preferred so the factory controls seeding)
   go reloader.RunSighupLoop(ctx)
   return libhttp.NewServer(listen, reloader, streamingServerTimeouts), nil
   ```

   - `NewReloader` signature becomes `func NewReloader(cfgPath string, initial http.Handler, build func(ctx context.Context, cfg *pkg.Config) (http.Handler, error)) *Reloader` — `build` is stored on the `Reloader` and called as `r.build(ctx, cfg)` inside `Reload` (replacing the direct `CreateRouterFromConfig` call).
   - Add `func (r *Reloader) SeedConfig(cfg *pkg.Config)` that does `r.current.Store(cfg)` — OR accept the initial config in `NewReloader` as a 4th arg. Either is fine; `SeedConfig` keeps the factory's seeding intent explicit.
   - `pkg/factory/factory.go` gains `import "github.com/bborbe/claude-code-router/pkg/reloader"`. `pkg/reloader/reloader.go` does NOT import `pkg/factory`.
   - `libhttp.NewServer` takes `router http.Handler`; `*reloader.Reloader` satisfies `http.Handler` via its `ServeHTTP` method. The SIGHUP goroutine is started BEFORE `libhttp.NewServer`'s returned `run.Func` begins listening — satisfying "before the HTTP server starts". When `ctx` is cancelled (SIGINT/SIGTERM), `RunSighupLoop` exits and `libhttp.NewServer`'s shutdown path runs. The `ServerFactory` signature in `pkg/cli.go` is UNCHANGED.
   - Raw-goroutine exemption: `go reloader.RunSighupLoop(ctx)` is the canonical single long-lived signal-listener pattern (one goroutine, observes `ctx.Done()`, recovers panics internally) — exempt from the `no-raw-go-func` rule per `go-concurrency-patterns.md`'s CLI signal-listener carve-out.

4. ERROR PATHS (every failure mode must have a defined behavior — these are not optional):
   - Config file deleted/unreadable on SIGHUP: `pkg.Load` returns `errors.Wrapf(ctx, err, "read config %q", expanded)`. `Reloader.Reload` returns that error; `reloadWithRecover` logs `config reload failed: read config ...` at WARNING. Old handler stays active. (Failure Modes row 1.)
   - Invalid YAML on SIGHUP: `pkg.Load` returns `errors.Wrapf(ctx, err, "parse config %q", expanded)`. Logged as `config reload failed: parse config ...` at WARNING. Old config retained. (Failure Modes row 2.)
   - `Validate` failure on SIGHUP: `pkg.Load` returns `errors.Wrapf(ctx, err, "validate config %q", expanded)`. Logged as `config reload failed: validate config ...` at WARNING. Old config retained. (Failure Modes row 3.)
   - `CreateRouterFromConfig` failure on SIGHUP: `Reloader.Reload` returns that error; logged at WARNING. Old handler stays active (no `Store`). (Defensive — `Validate` usually catches upstream issues first.)
   - Panic during mux rebuild: recovered in `reloadWithRecover`; logged at ERROR as `config reload panic: <recovered>`. Old config retained. (Failure Modes row 7.)
   - SIGHUP during an in-flight request: the in-flight request already holds a reference to the old `http.Handler` (via the `Reloader.ServeHTTP` load-then-dispatch); the atomic `Store` only affects subsequent `ServeHTTP` calls. No request is rerouted mid-flight. (Failure Modes row 4 — by design, no extra code needed beyond the atomic swap.)
   - Rapid repeated SIGHUP: each signal triggers one independent `Reload`. No coalescing. (Failure Modes row 5 — by design.)
   - SIGHUP after SIGINT/SIGTERM: `ctx.Done()` fires; `RunSighupLoop` returns; the signal is not consumed. No-op. (Failure Modes row 6 — by design.)

5. TOKEN-LEAK INVARIANT (load-bearing — the auditor will grep the reload log line):
   - The ONLY reload-success log line is `config reloaded old_providers=%d new_providers=%d`.
   - The ONLY reload-failure log line is `config reload failed: %v` (where `%v` is the wrapped error from `pkg.Load`, which itself contains only the path string and the underlying error — no token dump).
   - The ONLY reload-panic log line is `config reload panic: %v`.
   - NEVER add `glog.Infof("config: %+v", cfg)`, `glog.V(1).Infof("%+v", cfg)`, or any field-by-field config dump. The `Config` struct holds `Token` fields (`pkg/config.go` line 54). Emit COUNTS (`len(cfg.Providers)`) and the error string only.
   - Any new log line added by this work must pass: `grep -c 'Bearer\|sk-\|token:' <log>` returns 0 over the reload line.

6. GLOG DISCIPLINE:
   - Every new `Infof` is gated behind `glog.V(n)` with n>=1 (the success line uses `glog.V(1)`).
   - `glog.Warningf(...)` and `glog.Errorf(...)` are unconditional (warnings/errors always surface).
   - Lowercase log messages: `config reloaded`, `config reload failed`, `config reload panic` — NOT `Config reloaded`.

7. NO-CTX-CANCEL INVARIANT (load-bearing — AC 10 asserts this):
   - The SIGHUP path MUST NOT call `cancel()`, MUST NOT call `ctx.Done()` for cancellation, MUST NOT wrap `ctx` in a `context.WithCancel` that fires on SIGHUP.
   - The `ctx` passed to `Reloader.RunSighupLoop` is `run.ContextWithSig(ctx)` (from `service.MainCmd` via `App.Run`). It is cancelled ONLY by SIGINT/SIGTERM. SIGHUP reads from a SEPARATE `signal.Notify` channel and triggers a reload, never a cancellation.
</requirements>

<constraints>
- Language, logging, test, and gate stack are frozen: Go, `github.com/golang/glog` (V(n)-gated INFO), Ginkgo v2 + Gomega, `make precommit` as the gate.
- `pkg.Load(ctx, path) (*Config, error)` already performs read + tilde expansion + YAML parse + `Validate`. Reuse it verbatim on the reload path; do NOT extract a second validation or a second loader. Touching `Load`'s signature is out of scope.
- `run.ContextWithSig(ctx)` registers `signal.Notify` for `os.Interrupt, syscall.SIGINT, syscall.SIGTERM` and cancels the context on receipt — it does NOT register SIGHUP. The router MUST install its own `signal.Notify(ch, syscall.SIGHUP)` on a separate channel. Under no condition does the SIGHUP path cancel the run context.
- The mux-swap strategy is the frozen swap mechanism: rebuild the entire `http.Handler` (the whole mux from `CreateRouterFromConfig`) on each successful SIGHUP, then swap the `atomic.Pointer[http.Handler]` the server dispatches through. The per-request `atomic.Pointer[Config]` alternative is explicitly out of scope.
- `NewModelRouter` captures `routes`, `defaultHandler`, and `aliases` at construction time (not per-request). The mux rebuild therefore constructs a fresh `NewModelRouter` per reload.
- Token-leak invariant: reload logging emits provider COUNTS only. Never `glog.Infof("config: %+v", cfg)`, never a field-by-field dump. Any new log line must pass the no-`Bearer`/`sk-`/`token:`-substring grep.
- glog discipline: every new `Infof` is gated behind `glog.V(n)` (n>=1). Lowercase log messages.
- The config file path is fixed at startup from `--config-path`/`CONFIG_PATH` and is reused unchanged on every reload. The reload path does not re-read `--config-path`.
- Do NOT add a per-feature opt-out flag that disables SIGHUP handling — the SIGHUP handler is the feature; an escape hatch on it is itself a regression.
- Do NOT add a tunable debounce/coalesce window for SIGHUP — invariant; one SIGHUP equals one reload attempt.
- Do NOT commit — dark-factory handles git.
- Existing tests in `pkg/config_test.go`, `pkg/factory/factory_suite_test.go`, `pkg/handler/*_test.go` must continue to pass unchanged.
- `make precommit` remains the single gate; no new linter, formatter, or CI step.
- Test split: prompt 1 ships the `Reloader` type with no accompanying tests; prompt 2 (`2-spec-002-sighup-tests.md`) is the test prompt and depends on prompt 1. This split is the spec's deliberate Suggested Decomposition. The daemon executes prompts in filename order (1 → 2 → 3) on the same feature branch, so prompt 2's tests run before the branch is merged — the per-prompt ≥80% coverage bar is satisfied by the prompt-1+prompt-2 pair, not by prompt 1 alone. Do NOT duplicate prompt 2's tests here.
</constraints>

<verification>
Run `make precommit` in the repo root — must exit 0.

Confirm the wiring compiles and the no-ctx-cancel invariant holds structurally:
```
grep -n 'signal.Notify' pkg/reloader/reloader.go   # must show syscall.SIGHUP on a dedicated channel
grep -n 'context.WithCancel\|cancel()' pkg/reloader/reloader.go  # must return nothing on the SIGHUP path
grep -n 'atomic.Pointer' pkg/reloader/reloader.go   # must show the handler + config pointers
grep -n 'reloader.NewReloader\|reloader.RunSighupLoop' pkg/factory/factory.go      # must show wiring before libhttp.NewServer
```
</verification>

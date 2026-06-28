---
status: completed
summary: Replaced 15x fmt.Errorf with bberrors.Wrapf/New in pkg/config.go and pkg/factory/factory.go, threaded context.Context through Load, Validate, CreateServer, CreateRouterFromConfig.
execution_id: claude-code-router-exec-007-no-fmt-errorf-config-and-factory
dark-factory-version: v0.188.1
created: "2026-06-28T20:13:06Z"
queued: "2026-06-28T20:13:06Z"
started: "2026-06-28T20:13:07Z"
completed: "2026-06-28T20:18:12Z"
---
<summary>
- `/coding:code-review` whole-codebase audit found 15 `no-fmt-errorf` violations across `pkg/config.go` (10) and `pkg/factory/factory.go` (5)
- PR #20 cleaned up the same rule in `pkg/handler/model-router.go`'s `rewriteModelField` — use that as the canonical template
- Project rule: use `bberrors.Wrapf(ctx, err, format, args...)` from `github.com/bborbe/errors` instead of `fmt.Errorf` (ctx threads the request-scoped context through wraps)
- `pkg/config.go::Load` and `pkg/config.go::Validate` are entry-point functions that don't currently take a context — they need a ctx parameter added (breaking sig change, internal only)
- `pkg/factory/factory.go::CreateServer` and `CreateRouterFromConfig` similarly need ctx threading; callers in `pkg/cli.go` already have ctx available
</summary>

<objective>
Eliminate all 15 `no-fmt-errorf` violations in `pkg/config.go` and `pkg/factory/factory.go` by replacing `fmt.Errorf("...: %w", err)` with `bberrors.Wrapf(ctx, err, "...")` from `github.com/bborbe/errors`, threading `context.Context` through `Load`, `Validate`, `CreateServer`, and `CreateRouterFromConfig`.
</objective>

<context>
Read `docs/dod.md` for Definition of Done criteria.

Files to read (all of them, before editing):
- `pkg/config.go` — `Load(rawPath string)`, `Validate()`, `expandTilde(rawPath string)` (10 `fmt.Errorf` sites at lines 63, 67, 71, 74, 82, 85, 88, 95, 100, 113)
- `pkg/factory/factory.go` — `CreateServer(listen, configPath string)` and `CreateRouterFromConfig(cfg *pkg.Config)` (5 `fmt.Errorf` sites at lines 29, 33, 76, 98, 103)
- `pkg/cli.go` — callers of `CreateServer` (to thread ctx in)
- `pkg/handler/model-router.go::rewriteModelField` (line 192-205) — canonical pattern PR #20 established for `bberrors.Wrapf` usage
- `pkg/config_test.go` — test call sites for `Load` and `Validate` (need ctx in calls)
- `pkg/factory/factory_test.go` if present — test call sites for `CreateServer` and `CreateRouterFromConfig`

Reference for the `bberrors` API: `github.com/bborbe/errors/errors_wrapf.go` — signature is `Wrapf(ctx context.Context, err error, format string, args ...interface{}) error`.

Reference for prior usage of this pattern in the codebase: `pkg/handler/model-router.go` lines 192/197/202 (post-PR #20).
</context>

<requirements>

1. **`pkg/config.go::Load`**: add `ctx context.Context` as first parameter. Replace all 10 `fmt.Errorf` calls with `bberrors.Wrapf(ctx, err, "...")` (drop the `: %w` since Wrapf handles wrapping). Add `bberrors "github.com/bborbe/errors"` import; remove `fmt` if no longer used (it may still be needed for `fmt.Sprintf` elsewhere — check). Update the function GoDoc.

2. **`pkg/config.go::Validate`**: same pattern — add `ctx context.Context` as first parameter, swap to `bberrors.Wrapf(ctx, err, "...")`.

3. **`pkg/factory/factory.go::CreateServer`**: add `ctx context.Context` as first parameter. Pass ctx into `pkg.Load(ctx, ...)` and `CreateRouterFromConfig(ctx, ...)`. Swap the 2 `fmt.Errorf` calls.

4. **`pkg/factory/factory.go::CreateRouterFromConfig`**: add `ctx context.Context` as first parameter. Swap the 3 `fmt.Errorf` calls.

5. **`pkg/cli.go`**: the `ServerFactory` type alias (defined as `type ServerFactory func(listen, configPath string) (librun.Func, error)` at ~line 24) MUST be updated to `type ServerFactory func(ctx context.Context, listen, configPath string) (librun.Func, error)` — without this, compilation breaks. The call site `a.serverFactory(a.Listen, a.ConfigPath)` (inside `App.Run` or equivalent) must be updated to `a.serverFactory(ctx, a.Listen, a.ConfigPath)`. The `ctx` is already in scope (Run receives it). `main.go` injection `pkg.NewApp(factory.CreateServer)` is type-checked through the alias — no change to main.go itself needed.

6. **Test files**: every `Load(...)` / `Validate()` / `CreateServer(...)` / `CreateRouterFromConfig(...)` call site in tests gets a `context.Background()` (or `ctx` from BeforeEach) as the new first arg. No test logic changes — purely mechanical sig propagation.

7. **CHANGELOG.md**: add an `## Unreleased` section (or append to existing) with two bullets:
   - `refactor: replace 15× fmt.Errorf in pkg/config.go and pkg/factory/factory.go with bberrors.Wrapf(ctx, ...). Threads context.Context through Load, Validate, CreateServer, CreateRouterFromConfig.`
   - `**Breaking**: Load, Validate, CreateServer, CreateRouterFromConfig signatures gain ctx context.Context as first positional parameter. Internal API — no external callers.`

8. **`make precommit` must pass** — golangci-lint, all tests, license headers.

</requirements>

<acceptance-criteria>

- [ ] `grep -rn 'fmt.Errorf' pkg/config.go pkg/factory/factory.go` returns ZERO matches
- [ ] `Load`, `Validate`, `CreateServer`, `CreateRouterFromConfig` all take `ctx context.Context` as first parameter
- [ ] All test call sites compile and pass
- [ ] `make precommit` exits 0 (golangci-lint 0 issues, all tests green)
- [ ] CHANGELOG `## Unreleased` section documents both the refactor and the breaking sig changes
- [ ] No new lint issues introduced

</acceptance-criteria>

<scope-out>

- Do NOT touch `pkg/handler/*.go` `fmt.Errorf` calls (already cleaned up in PR #20)
- Do NOT touch `pkg/handler/logging-roundtripper.go::time.Now()` — that's a separate prompt (008)
- Do NOT restructure or extract helpers; pure mechanical refactor
- Do NOT change the wrap message text beyond dropping the trailing `: %w` (Wrapf handles wrapping)
- Do NOT add new error types or domain-specific error wrappers
- Do NOT change YAML schema or validation semantics

</scope-out>

<verification>

```bash
cd /workspace
grep -rn 'fmt.Errorf' pkg/config.go pkg/factory/factory.go  # expect: no output
make precommit  # expect: exit 0
```

</verification>

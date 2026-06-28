---
status: draft
created: "2026-06-28T00:00:00Z"
---

<summary>
- Five `fmt.Errorf` calls in `pkg/factory/factory.go` must use `errors.Wrapf` from `github.com/bborbe/errors` instead
- `CreateServer` and `CreateRouterFromConfig` need `context.Context` as first parameter to support `errors.Wrapf(ctx, err, ...)` calls
- The context must be threaded from `main.go` through to the factory functions
</summary>

<objective>
Replace all `fmt.Errorf` calls in `pkg/factory/factory.go` with `errors.Wrapf` / `errors.Errorf` from `github.com/bborbe/errors`, and thread `context.Context` through the call chain so error wrapping has proper context.
</objective>

<context>
Read `docs/dod.md` for Definition of Done criteria.

Files to read before making changes (read ALL first):
- `/workspace/pkg/factory/factory.go` — the file with the 5 `fmt.Errorf` calls and both `Create*` functions
- `/workspace/main.go` — caller of `factory.CreateServer` to understand how to thread ctx
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — for `errors.Wrapf`/`errors.Errorf` usage conventions
</context>

<requirements>
1. **Thread `context.Context` into factory functions.** In `pkg/factory/factory.go`:
   - Change `CreateServer(listen, configPath string)` to `CreateServer(ctx context.Context, listen, configPath string)`
   - Change `CreateRouterFromConfig(cfg *pkgcfg.Config)` to `CreateRouterFromConfig(ctx context.Context, cfg *pkgcfg.Config)`
   Both functions must accept `ctx context.Context` as their first parameter.

2. **Update `main.go` call site.** Find where `factory.CreateServer` is called and pass the ctx from `service.MainCmd`. The ctx from `service.MainCmd`'s Run function is the correct context to use — it carries cancellation signals.

3. **Replace `fmt.Errorf` with `errors.Wrapf`/`errors.Errorf`.** In `pkg/factory/factory.go`, replace each `fmt.Errorf`:
   - Line ~28: `fmt.Errorf("load config: %w", err)` → `errors.Wrapf(ctx, err, "load config")`
   - Line ~32: `fmt.Errorf("build router: %w", err)` → `errors.Wrapf(ctx, err, "build router")`
   - Line ~51: `fmt.Errorf("provider %q: parse upstream %q: %w", ...)` → `errors.Wrapf(ctx, err, "provider %q: parse upstream %q", name, prov.Upstream)`
   - Line ~70: `fmt.Errorf("default_provider %q not in providers", ...)` → `errors.Errorf(ctx, "default_provider %q not in providers", cfg.Router.DefaultProvider)` (no cause to wrap — new sentinel error)
   - Line ~75: `fmt.Errorf("register metrics: %w", err)` → `errors.Wrapf(ctx, err, "register metrics")`

4. **Add import.** Add `"github.com/bborbe/errors"` to the imports. Verify it compiles: `go build ./pkg/factory/...`
</requirements>

<constraints>
- Only change files in `.` (this repo)
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- The `ctx` parameter must be the FIRST parameter on both `CreateServer` and `CreateRouterFromConfig`
- Do NOT use `context.Background()` — use the ctx passed from the caller
</constraints>

<verification>
make precommit
</verification>

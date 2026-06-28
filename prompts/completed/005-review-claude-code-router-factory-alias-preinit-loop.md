---
status: completed
summary: Moved alias counter pre-initialization from factory loop into NewMetrics constructor; CreateRouterFromConfig now has zero if/for/switch
execution_id: claude-code-router-review-fixes-exec-005-review-claude-code-router-factory-alias-preinit-loop
dark-factory-version: v0.187.11-2-g53614c0-dirty
created: "2026-06-28T00:00:00Z"
queued: "2026-06-28T16:30:02Z"
started: "2026-06-28T16:30:29Z"
completed: "2026-06-28T16:32:36Z"
---

<summary>
- Pre-initialization of the alias-resolution counter currently lives as a loop inside the router factory function
- That loop belongs inside the metrics constructor so the factory stays pure composition
- The metrics constructor will accept the alias map and seed zero-valued counter series for each pair
- A nil alias map must be safe — no panic, no iteration
- Behavior preserved: alerts still see `0` (not no-data) for declared aliases before first hit
</summary>

<objective>
Move the alias counter pre-initialization out of the router factory and into the metrics constructor, so the factory body has zero `if`/`for`/`switch` statements while keeping the operator-side observability guarantee for declared aliases.
</objective>

<context>
Read `docs/dod.md` for Definition of Done criteria.

Files to read (all of them, before editing):
- `pkg/factory/factory.go` — `CreateRouterFromConfig`; loop at ~line 113 over `cfg.Aliases`
- `pkg/handler/metrics.go` — `Metrics` struct, `NewMetrics` (concrete `*Metrics` return at ~line 42)
- `pkg/handler/metrics_test.go` — `NewMetrics()` call at ~line 20
- `pkg/handler/model-router_test.go` — `NewMetrics()` calls at ~lines 31, 414, 480

Project rule (no external doc needed): factory functions must contain only composition — no `if`, no `for`, no `switch` in the body. Conditional / iterative setup belongs in the constructor of the type being assembled.
</context>

<requirements>

1. **Change `NewMetrics` signature** in `pkg/handler/metrics.go` to accept the alias map and pre-initialize the counter inside the constructor:

   ```go
   func NewMetrics(aliases map[string]string) *Metrics {
       m := &Metrics{ /* existing collectors */ }
       for alias, resolved := range aliases {
           m.AliasResolutions.WithLabelValues(alias, resolved).Add(0)
       }
       return m
   }
   ```

   A `nil` map ranges zero times in Go — no extra guard needed, no panic.

2. **Remove the loop from `pkg/factory/factory.go`.** The block at ~lines 100–115 becomes:

   ```go
   metrics := handler.NewMetrics(cfg.Aliases)
   ```

   Delete the explanatory comment + `for alias, resolved := range cfg.Aliases { ... }` block. After this change, `CreateRouterFromConfig` contains no `if` / `for` / `switch`.

3. **Update test call sites** to pass `nil` (these tests don't care about pre-init):
   - `pkg/handler/metrics_test.go:20` — `handler.NewMetrics()` → `handler.NewMetrics(nil)`
   - `pkg/handler/model-router_test.go:31` — same
   - `pkg/handler/model-router_test.go:414` — same
   - `pkg/handler/model-router_test.go:480` — same

4. **Add a unit test in `pkg/handler/metrics_test.go`** that locks the new contract:
   - `NewMetrics(map[string]string{"qwen":"qwen-coder"})` followed by `testutil.ToFloat64(m.AliasResolutions.WithLabelValues("qwen","qwen-coder"))` returns `0` (series exists, value zero).
   - `NewMetrics(nil)` returns non-nil and does not panic.

   Import `"github.com/prometheus/client_golang/prometheus/testutil"` for the assertion.

</requirements>

<constraints>
- Only change files in `.` (this repo)
- Do NOT commit — dark-factory handles git
- `CreateRouterFromConfig` body must contain zero `if` / `for` / `switch` after the change
- A `nil` aliases map must be a no-op (no panic, no iteration error)
- Use stdlib `fmt.Errorf` for any new error wrapping (project convention — no `bborbe/errors`)
- Existing tests must still pass
</constraints>

<verification>
make precommit
</verification>

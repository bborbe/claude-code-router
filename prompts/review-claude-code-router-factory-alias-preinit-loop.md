---
status: draft
created: "2026-06-28T00:00:00Z"
---

<summary>
- A `for` loop inside `CreateRouterFromConfig` iterates `cfg.Aliases` to pre-initialize Prometheus counter series — violates the factory pattern rule (no conditional logic in factory body)
- The loop must move into the `Metrics` constructor in `pkg/handler/metrics.go` so `CreateRouterFromConfig` remains pure composition
</summary>

<objective>
Refactor the alias pre-initialization loop out of `CreateRouterFromConfig` and into the `Metrics` constructor, keeping `CreateRouterFromConfig` as pure composition with zero `if`/`for`/`switch` statements.
</objective>

<context>
Read `docs/dod.md` for Definition of Done criteria.

Files to read before making changes (read ALL first):
- `/workspace/pkg/factory/factory.go` — lines 77–90 where the alias pre-init loop lives
- `/workspace/pkg/handler/metrics.go` — the `Metrics` struct and `NewMetrics()` constructor
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md` — for the "no conditionals in factory body" rule
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` — for counter pre-initialization conventions
</context>

<requirements>
1. **Move alias pre-init loop into `Metrics` constructor.** In `pkg/handler/metrics.go`, change `NewMetrics()` to accept `aliases map[string]string` and perform the pre-initialization inside the constructor:

   ```go
   func NewMetrics(aliases map[string]string) *Metrics {
       m := &Metrics{...}
       for alias, resolved := range aliases {
           m.AliasResolutions.WithLabelValues(alias, resolved).Add(0)
       }
       return m
   }
   ```

   If `NewMetrics` currently returns an interface, change the return type to the concrete `*Metrics` so the constructor can perform the pre-init. If there is a `Metrics` interface, keep it and add a separate `NewMetricsWithAliases(aliases map[string]string) *Metrics` function.

2. **Update `CreateRouterFromConfig` call site.** In `pkg/factory/factory.go`, change:
   ```go
   metrics := handler.NewMetrics()
   ```
   to:
   ```go
   metrics := handler.NewMetrics(cfg.Aliases)
   ```
   Remove the `for alias, resolved := range cfg.Aliases { ... }` loop entirely from `factory.go`.

3. **Propagate to test call sites.** Find all `NewMetrics()` call sites:
   ```bash
   grep -rn 'NewMetrics()' /workspace --include='*.go'
   ```
   Update any call site that needs to pass `nil` for aliases (backward-compatible — the constructor handles nil gracefully).

4. **Verify.** Run `go build ./...` to confirm no type errors, then `make test`.
</requirements>

<constraints>
- Only change files in `.` (this repo)
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- The factory function `CreateRouterFromConfig` must have zero `if`/`for`/`switch` statements after this change
- `cfg.Aliases` may be nil — the constructor must handle nil gracefully (no nil map iteration panic)
</constraints>

<verification>
make precommit
</verification>

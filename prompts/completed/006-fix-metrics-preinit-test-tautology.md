---
status: completed
summary: 'Tightened pre-init tests: replaced lazy-access value reads with CollectAndCount assertions, added nil-aliases series count check'
execution_id: claude-code-router-review-fixes-exec-006-fix-metrics-preinit-test-tautology
dark-factory-version: v0.187.11-2-g53614c0-dirty
created: "2026-06-28T16:45:00Z"
queued: "2026-06-28T16:44:52Z"
started: "2026-06-28T16:44:54Z"
completed: "2026-06-28T16:46:10Z"
---

<summary>
- The pre-initialization unit test for the metrics constructor currently asserts nothing useful
- `CounterVec.WithLabelValues` lazy-creates the child counter on first call, so reading its value always returns zero — whether or not the constructor pre-seeded that series
- The test passes identically with the pre-init loop deleted, so the contract is unprotected
- The real contract (the labeled series exists in the collector's gathered output before any traffic) needs an assertion that does not itself create the series
- A nil-aliases test must verify the same gather-side contract: zero series, no panic
</summary>

<objective>
Tighten the pre-initialization test so it fails when the constructor's pre-init loop is removed, by asserting the gathered series count instead of reading a value through the lazy-creating accessor.
</objective>

<context>
Read `docs/dod.md` for Definition of Done criteria.

Files to read (all of them, before editing):
- `pkg/handler/metrics.go` — `NewMetrics(aliases map[string]string) *Metrics` and the `AliasResolutions` `*prometheus.CounterVec`
- `pkg/handler/metrics_test.go` — the existing `Context("NewMetrics with alias map", ...)` block at ~line 107

Background: `(*prometheus.CounterVec).WithLabelValues(...)` returns the child `Counter`, constructing it on demand if it does not yet exist. Therefore reading a value through `WithLabelValues` cannot distinguish "pre-initialized" from "created by this call." The library's standard test utility for "how many child series currently exist" is `prometheus/testutil.CollectAndCount(collector)`.
</context>

<requirements>

1. **Replace the tautological `pre-initializes counter series to zero` assertion** in `pkg/handler/metrics_test.go` (~line 109). The new assertion must verify the series exists *without* calling `WithLabelValues` first:

   ```go
   import promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
   // ...
   It("pre-initializes one counter series per declared alias", func() {
       aliasMetrics := handler.NewMetrics(map[string]string{
           "qwen": "qwen-coder",
           "m3":   "MiniMax-M3-highspeed",
       })
       Expect(promtestutil.CollectAndCount(aliasMetrics.AliasResolutions)).To(Equal(2))
   })
   ```

   `CollectAndCount` calls `Collect` on the collector and counts emitted metrics — pre-init'd series show up, lazy-only ones don't. Deleting the `for ... Add(0)` loop in `NewMetrics` MUST make this expectation fail with `0`.

2. **Tighten the nil-aliases test** to assert the same gather-side contract:

   ```go
   It("creates no series and does not panic when aliases is nil", func() {
       var m *handler.Metrics
       Expect(func() { m = handler.NewMetrics(nil) }).NotTo(Panic())
       Expect(promtestutil.CollectAndCount(m.AliasResolutions)).To(Equal(0))
   })
   ```

3. **Sanity-check the imports** in `pkg/handler/metrics_test.go`: `testutil` may already be imported as `"github.com/prometheus/client_golang/prometheus/testutil"`. If so, reuse that alias; if not, add the import. Do not add a duplicate alias.

4. **Verify the test catches loop removal.** Manually delete the `for alias, resolved := range aliases` block in `pkg/handler/metrics.go`, run `go test ./pkg/handler/...`, confirm the new expectations FAIL with `Expected 2 to equal 0` (or similar). Put the loop back, re-run, confirm tests pass.

</requirements>

<constraints>
- Only change files in `.` (this repo)
- Do NOT commit — dark-factory handles git
- Do NOT delete the existing `NewMetrics returns non-nil collectors` / `ObserveAliasResolution` tests
- Use stdlib `fmt.Errorf` for any new error wrapping (project convention)
- Existing tests must still pass
</constraints>

<verification>
make precommit
</verification>

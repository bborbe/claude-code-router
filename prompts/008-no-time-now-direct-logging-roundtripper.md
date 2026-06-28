<summary>
- `/coding:code-review` whole-codebase audit found 1 remaining `no-time-now-direct` violation: `pkg/handler/logging-roundtripper.go:94`
- This is the deferred follow-up the bot raised on PR #12 and PR #18 — same pattern PR #20 fixed for `NewModelRouter`
- `loggingRoundTripper.RoundTrip` measures TTFB by calling `time.Now()` directly, making the TTFB calculation untestable
- Use the canonical `libtime.CurrentDateTimeGetter` injection pattern from PR #20: add a 3rd positional param to `NewLoggingRoundTripper`, wire `libtime.NewCurrentDateTime()` in factory
</summary>

<objective>
Replace the direct `time.Now()` call at `pkg/handler/logging-roundtripper.go:94` with an injected `libtime.CurrentDateTimeGetter`, following the exact pattern PR #20 established for `NewModelRouter`.
</objective>

<context>
Read `docs/dod.md` for Definition of Done criteria.

Files to read (all of them, before editing):
- `pkg/handler/logging-roundtripper.go` — `NewLoggingRoundTripper(inner http.RoundTripper, bodySampler liblog.Sampler) http.RoundTripper` (~L48); `loggingRoundTripper.RoundTrip` measures TTFB via `start := time.Now()` at ~L95 then `time.Since(start).Round(time.Millisecond)` at ~L97
- `pkg/handler/logging-roundtripper_test.go` — 7 distinct `NewLoggingRoundTripper(...)` call sites (need 3rd arg added)
- `pkg/factory/factory.go` — wires `handler.NewLoggingRoundTripper(...)` at ~L79; `libtime` already imported (~L16) and `libtime.NewCurrentDateTime()` already in scope from PR #20 (~L113 for ModelRouter) — reuse the SAME instance, don't construct a second one
- `pkg/handler/model-router.go` lines 78-89, 154 — canonical pattern PR #20 established for `libtime.CurrentDateTimeGetter` usage: `start := currentDateTime.Now().Time()`, `latency := currentDateTime.Now().Time().Sub(start).Round(time.Millisecond)`

Reference for the libtime API: `github.com/bborbe/time/time_current-datetime.go` — `CurrentDateTimeGetter` interface has `Now() DateTime`; `DateTime` is `type DateTime stdtime.Time` with `.Time()` method.
</context>

<requirements>

1. **`pkg/handler/logging-roundtripper.go::NewLoggingRoundTripper`**: add `currentDateTime libtime.CurrentDateTimeGetter` as a new positional parameter (last, to match PR #20's positional convention). Store on the struct.

2. **`pkg/handler/logging-roundtripper.go::RoundTrip`**: replace `start := time.Now()` with `start := l.currentDateTime.Now().Time()`. Replace `time.Since(start)` with `l.currentDateTime.Now().Time().Sub(start)`. Keep `time.Millisecond` rounding (it's a constant, not a clock call). Add `libtime "github.com/bborbe/time"` import.

3. **`pkg/factory/factory.go`**: pass `libtime.NewCurrentDateTime()` (or reuse an existing instance — there's already one in scope for `NewModelRouter`) as the new 3rd arg to `handler.NewLoggingRoundTripper(...)`.

4. **`pkg/handler/logging-roundtripper_test.go`**: 7 `NewLoggingRoundTripper(...)` call sites get a 3rd arg. Reuse the existing package-level `testDateTime` already declared in `pkg/handler/model-router_test.go:34` (same package `handler_test`, so visible) — do NOT declare a second one.

5. **CHANGELOG.md**: add an `## Unreleased` bullet:
   - `**Breaking**: NewLoggingRoundTripper signature gains a 3rd positional param currentDateTime libtime.CurrentDateTimeGetter (was bot-deferred follow-up from PR #12/PR #18 — closes the no-time-now-direct rule violation; factory + tests updated).`

6. **`make precommit` must pass** — golangci-lint, all tests, license headers.

</requirements>

<acceptance-criteria>

- [ ] `grep -n 'time.Now()' pkg/handler/logging-roundtripper.go` returns ZERO matches
- [ ] `NewLoggingRoundTripper` accepts `libtime.CurrentDateTimeGetter` as the last positional parameter
- [ ] `pkg/factory/factory.go` wires `libtime.NewCurrentDateTime()` into the call
- [ ] All test call sites compile and pass; TTFB measurement assertions still hold
- [ ] `make precommit` exits 0
- [ ] CHANGELOG `## Unreleased` documents the breaking sig change

</acceptance-criteria>

<scope-out>

- Do NOT touch `pkg/handler/model-router.go` (already done in PR #20)
- Do NOT touch `pkg/config.go` or `pkg/factory/factory.go::CreateServer/CreateRouterFromConfig` `fmt.Errorf` calls — that's a separate prompt (007)
- Do NOT introduce a mockable test using `CurrentDateTimeGetterFunc` unless an existing TTFB-value test demands it; the goal is to inject the dependency, not to add new tests
- Do NOT change the `[upstream]` log line format
- Do NOT change the `liblog.Sampler` parameter or sampling behavior

</scope-out>

<verification>

```bash
cd /workspace
grep -n 'time.Now()' pkg/handler/logging-roundtripper.go  # expect: no output
make precommit  # expect: exit 0
```

</verification>

---
status: completed
spec: ["007"]
summary: Added ccrouter_tokens_total counter, expanded status_class to 7-value enum, added ObserveTokens with drop rules, and updated all test call sites
execution_id: claude-code-router-tokens-exec-019-spec-007-metrics-collector
dark-factory-version: dev
created: "2026-07-01T10:55:00Z"
queued: "2026-07-01T11:03:34Z"
started: "2026-07-01T11:03:36Z"
completed: "2026-07-01T11:07:06Z"
---

<summary>
- The `Metrics` struct gains a new `ccrouter_tokens_total` counter labeled `provider`, `model`, `direction` — operators can chart LLM token throughput per provider and per model.
- A new `ObserveTokens(provider, model, direction string, count int)` method increments the counter while dropping zero, negative, and unknown-direction inputs at the seam so bad upstream data never reaches Prometheus.
- The `status_class` bucketing expands from 4 buckets (`2xx`/`3xx`/`4xx`/`5xx`) to a 7-value taxonomy (`2xx`, `3xx`, `4xx_auth`, `4xx_rate_limited`, `4xx_bad_request`, `5xx_upstream`, `5xx_router`) so 429 quota spikes and router-side rejections become distinct series.
- `ObserveRequest` gains a trailing `isRouterError bool` argument that flips 5xx between `5xx_upstream` (upstream fault) and `5xx_router` (router-side rejection).
- A new exported constant `UnknownModelLabel = "_unknown_"` provides the sentinel used to keep `model=""` empty labels out of the counter.
- All 8 existing `ObserveRequest` call sites in the metrics unit tests are updated to the new 5-arg signature; new unit tests cover the seven status_class values, the token-drop rules, and the model-unknown sentinel constant.
- This prompt is scoped to `pkg/handler/metrics.go` + `pkg/handler/metrics_test.go`, plus a mechanical one-line signature-only fix at `pkg/handler/model-router.go:160` (append `, false` as 5th arg) to keep `make precommit` green in isolation. All semantic wiring at that call site (isRouterError=true at the three router-side early-returns, tokens observation, sentinel-chain resolver) stays for prompt 2.
</summary>

<objective>
Land the collector shape + taxonomy split in isolation: add the tokens CounterVec, add `ObserveTokens` with zero/negative/unknown-direction drop, expand `statusClass` to a 7-value enum keyed on a new `isRouterError` argument, update every existing `metrics_test.go` call site to the new signature, and append `, false` to the single production call site at `model-router.go:160` so `make precommit` stays green. Pure unit-test scope for the semantic changes — no HTTP, no `NewModelRouter` semantic changes.
</objective>

<context>
Read first:
- `/workspace/specs/in-progress/007-metrics-tokens-and-error-classes.md` — Desired Behavior 1, 2; Failure Modes rows for `ObserveTokens` drops and sentinel; Constraints (frozen behavior of RequestDuration + AliasResolutions + pre-init pattern); Acceptance Criteria 5, 6, 7, 8, 9, 10, 11, 12, 15.
- `/workspace/pkg/handler/metrics.go` — the entire file. Current state: `Metrics` struct has three fields (`RequestsTotal`, `RequestDuration`, `AliasResolutions`); `NewMetrics(aliases map[string]string)`; `Register(reg prometheus.Registerer) error`; `ObserveRequest(provider, model string, status int, latencySeconds float64)`; `ObserveAliasResolution(alias, resolved string)`; `statusClass(status int) string` returns `"2xx"`/`"3xx"`/`"4xx"`/`"5xx"` or raw status. `LatencyBucketsSeconds` var is frozen.
- `/workspace/pkg/handler/metrics_test.go` — the entire file. Eight `ObserveRequest(...)` call sites at lines 42, 49, 56, 63, 72, 79, 86, 93. Each takes 4 args today.
- `/workspace/docs/dod.md` — DoD: GoDoc on exported symbols, `bborbe/errors` idioms, Ginkgo/Gomega, no `fmt.Printf`, `glog.V(n)` gating.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` — CounterVec construction, label ordering, pre-initialization, cardinality bookkeeping.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo table-driven pattern for the enum expansion; `testutil.ToFloat64` and `testutil.CollectAndCount` usage.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-enum-type-pattern.md` — for the shape of the expanded `statusClass` result set (bounded enum, seven-value contract).

Verify before writing code: the current `ObserveRequest` signature is `func (m *Metrics) ObserveRequest(provider, model string, status int, latencySeconds float64)` — the new signature adds a trailing `isRouterError bool` (5 args). The current `statusClass` signature is `func statusClass(status int) string` — the new signature is `func statusClass(status int, isRouterError bool) string` (2 args).
</context>

<requirements>

1. **Add the `UnknownModelLabel` exported constant.** In `/workspace/pkg/handler/metrics.go`, add:

   ```go
   // UnknownModelLabel is the sentinel used in place of an empty model
   // string when the request body has no `model` field (probe traffic,
   // misshapen bodies) or the router-side early-return paths reject
   // before body parse. The leading + trailing underscore avoids
   // collision with real Anthropic + OpenRouter model names, which
   // never start with `_`. This constant is a MODEL-label sentinel; the
   // same string is also used as the provider label value from the three
   // router-side early-return paths in NewModelRouter (see spec 007
   // Desired Behavior 6) — that reuse is deliberate and scoped, NOT a
   // general fallback for other labels.
   const UnknownModelLabel = "_unknown_"
   ```

   Place it above the `LatencyBucketsSeconds` var, next to the other package-level identifiers.

2. **Extend the `Metrics` struct with a `TokensTotal` field.** In the `Metrics` struct doc comment, revise the paragraph that describes the collectors and the cardinality budget to reflect the new counter and the 7-value status_class enum. Then add the field:

   ```go
   type Metrics struct {
       RequestsTotal    *prometheus.CounterVec
       RequestDuration  *prometheus.HistogramVec
       AliasResolutions *prometheus.CounterVec
       TokensTotal      *prometheus.CounterVec
   }
   ```

   Update the doc comment to state the new cardinality budget explicitly:

   > Cardinality budget: 5 providers × ~15 active models × 7 status_classes = 525 series ceiling for the requests counter (~450 in practice because some tuples never fire); 5 × 15 × 2 = 150 series for the tokens counter; histogram adds 5×15×len(buckets) = 750 series; aliases counter bounded by the YAML config (≤10). Total ~1.5k series — fine for a local Prometheus scrape.

3. **Construct `TokensTotal` in `NewMetrics`.** Extend the `NewMetrics` initializer block to include the new CounterVec (label order MUST be `provider`, `model`, `direction`):

   ```go
   TokensTotal: prometheus.NewCounterVec(
       prometheus.CounterOpts{
           Name: "ccrouter_tokens_total",
           Help: "Total number of LLM tokens observed on successful (2xx) /v1/* responses, labeled by provider, model, and direction (input|output). Fed from the ExtractUsage tee on the response tail; non-2xx responses do not increment this counter.",
       },
       []string{"provider", "model", "direction"},
   ),
   ```

   Do NOT pre-initialize any (provider, model, direction) tuple — the label space is bounded by real traffic and the alias pre-init pattern is model-only anyway. Do NOT touch the `aliases` range block.

4. **Register `TokensTotal` in `Register`.** Extend the collector slice inside `Register`:

   ```go
   for _, c := range []prometheus.Collector{m.RequestsTotal, m.RequestDuration, m.AliasResolutions, m.TokensTotal} {
       if err := reg.Register(c); err != nil {
           return err
       }
   }
   ```

   The first-error-wins short-circuit is unchanged.

5. **Expand `statusClass` to the 7-value enum keyed on `isRouterError`.** Replace the current `statusClass(status int) string` with:

   ```go
   // statusClass collapses raw HTTP status into a bounded 7-value enum
   // used as the ccrouter_requests_total{status_class} label:
   //
   //   status 2xx                       -> "2xx"
   //   status 3xx                       -> "3xx"
   //   status 401 or 403                -> "4xx_auth"
   //   status 429                       -> "4xx_rate_limited"
   //   any other 4xx                    -> "4xx_bad_request"
   //   5xx with isRouterError == false  -> "5xx_upstream"
   //   5xx with isRouterError == true   -> "5xx_router"
   //   anything else                    -> raw strconv.Itoa(status)
   //
   // Cardinality contract: exactly 7 distinct label values in normal
   // operation (plus the raw-code fallback for out-of-range status). No
   // per-status-code label (spec 007 Non-goals).
   //
   // The isRouterError argument is set true by the three router-side
   // early-return paths in NewModelRouter (body-too-large 413,
   // body-read-fail 400, alias-rewrite-fail 500) so a router-side 500
   // separates from an upstream 500 in Grafana.
   func statusClass(status int, isRouterError bool) string {
       switch {
       case status >= 200 && status < 300:
           return "2xx"
       case status >= 300 && status < 400:
           return "3xx"
       case status == 401 || status == 403:
           return "4xx_auth"
       case status == 429:
           return "4xx_rate_limited"
       case status >= 400 && status < 500:
           return "4xx_bad_request"
       case status >= 500 && status < 600:
           if isRouterError {
               return "5xx_router"
           }
           return "5xx_upstream"
       default:
           return strconv.Itoa(status)
       }
   }
   ```

   Note the ordering: `4xx_auth` and `4xx_rate_limited` must be checked BEFORE the generic `4xx_bad_request` fallthrough. The `5xx_router`/`5xx_upstream` split is inside the 5xx branch.

6. **Change `ObserveRequest` to the new 5-arg signature.** Replace:

   ```go
   func (m *Metrics) ObserveRequest(provider, model string, status int, latencySeconds float64) {
       m.RequestsTotal.WithLabelValues(provider, model, statusClass(status)).Inc()
       m.RequestDuration.WithLabelValues(provider, model).Observe(latencySeconds)
   }
   ```

   with:

   ```go
   // ObserveRequest is the call-site shorthand used by NewModelRouter
   // after each /v1/* dispatch: increments the request counter with the
   // 7-value status_class enum (see statusClass) and observes the latency
   // on the histogram. isRouterError distinguishes 5xx router-side
   // rejections (body-too-large, alias-rewrite-fail) from 5xx upstream
   // faults — see spec 007 Desired Behavior 2 and 6. Callers on the
   // happy path pass isRouterError=false.
   func (m *Metrics) ObserveRequest(provider, model string, status int, latencySeconds float64, isRouterError bool) {
       m.RequestsTotal.WithLabelValues(provider, model, statusClass(status, isRouterError)).Inc()
       m.RequestDuration.WithLabelValues(provider, model).Observe(latencySeconds)
   }
   ```

   The `RequestDuration.Observe` line is unchanged (histogram is untouched by this spec).

7. **Add `ObserveTokens` with the drop rules.** Append this new exported method after `ObserveRequest`:

   ```go
   // ObserveTokens increments the ccrouter_tokens_total counter by count
   // for the given (provider, model, direction) tuple. Drop rules — the
   // call is a no-op (no series created) when:
   //
   //   - count <= 0                                (zero-drop rule; zero is not a data point, negative is a schema violation)
   //   - direction is not "input" or "output"      (bounded-enum rule; keeps cardinality contract)
   //
   // These drops are silent: token counting is best-effort observability,
   // never on the request-serving critical path. See spec 007 Failure
   // Modes for the sentinel/negative/zero rows this method absorbs.
   func (m *Metrics) ObserveTokens(provider, model, direction string, count int) {
       if count <= 0 {
           return
       }
       if direction != "input" && direction != "output" {
           return
       }
       m.TokensTotal.WithLabelValues(provider, model, direction).Add(float64(count))
   }
   ```

   Use `Add(float64(count))`, not `Inc()` — a single 200 response reports N input + M output tokens as one increment of size N and one of size M, not one Inc per token.

8. **Update all 8 existing `ObserveRequest(...)` call sites in `/workspace/pkg/handler/metrics_test.go` to the 5-arg signature.** Every current 4-arg call site takes `isRouterError = false` (the specs currently exercise happy-path behavior + generic 4xx/5xx). Concrete rewrites:

   - Line 42: `m.ObserveRequest("p", "model", 200, 0.1)` → `m.ObserveRequest("p", "model", 200, 0.1, false)`
   - Line 49: `m.ObserveRequest("p", "model", 404, 0.1)` → `m.ObserveRequest("p", "model", 404, 0.1, false)`. **Expected label MUST change from `"4xx"` to `"4xx_bad_request"`** — update the `WithLabelValues("p", "model", "4xx")` assertion on the following line to `"4xx_bad_request"`, and update the `It("...")` description from `"increments 4xx status_class for status 404"` to `"increments 4xx_bad_request status_class for status 404"`.
   - Line 56: `m.ObserveRequest("p", "model", 500, 0.1)` → `m.ObserveRequest("p", "model", 500, 0.1, false)`. **Expected label MUST change from `"5xx"` to `"5xx_upstream"`**; update the assertion and description accordingly.
   - Line 63: `m.ObserveRequest("p", "model", 999, 0.1)` → `m.ObserveRequest("p", "model", 999, 0.1, false)`. Expected label stays `"999"` (raw status for out-of-range).
   - Line 72: `m.ObserveRequest("p", "m", 200, 0)` → `m.ObserveRequest("p", "m", 200, 0, false)`. Expected label stays `"2xx"`.
   - Line 79: `m.ObserveRequest("p", "m", 404, 0)` → `m.ObserveRequest("p", "m", 404, 0, false)`. Expected label changes to `"4xx_bad_request"`; update description from `"statusClass(404) maps to 4xx"` to `"statusClass(404) maps to 4xx_bad_request"`.
   - Line 86: `m.ObserveRequest("p", "m", 503, 0)` → `m.ObserveRequest("p", "m", 503, 0, false)`. Expected label changes to `"5xx_upstream"`; update description from `"statusClass(503) maps to 5xx"` to `"statusClass(503) maps to 5xx_upstream"`.
   - Line 93: `m.ObserveRequest("p", "m", 999, 0)` → `m.ObserveRequest("p", "m", 999, 0, false)`. Expected label stays `"999"`.

   After the edits: `grep -c 'ObserveRequest(' /workspace/pkg/handler/metrics_test.go` must return ≥8, AND `grep -E 'ObserveRequest\("p",\s*"m(odel)?"?\s*,\s*[0-9]+\s*,\s*[0-9.]+\)' /workspace/pkg/handler/metrics_test.go` must return zero matches (no 4-arg calls survive). AND `grep -nE 'status_class="4xx"|status_class="5xx"' /workspace/pkg/handler/metrics_test.go` must return zero matches (no bare 4xx/5xx assertions survive).

9. **Add new `statusClass` specs for the seven-value enum.** Inside the existing `Context("statusClass via ObserveRequest label", ...)` block, add specs covering the split cases. All continue to use `m.ObserveRequest(...)` (not a direct `statusClass` call — the function is unexported and reached via the label):

   ```go
   It("statusClass(401) maps to 4xx_auth", func() {
       m.ObserveRequest("p", "m", 401, 0, false)
       Expect(
           testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "4xx_auth")),
       ).To(Equal(float64(1)))
   })

   It("statusClass(403) maps to 4xx_auth", func() {
       m.ObserveRequest("p", "m", 403, 0, false)
       Expect(
           testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "4xx_auth")),
       ).To(Equal(float64(1)))
   })

   It("statusClass(429) maps to 4xx_rate_limited", func() {
       m.ObserveRequest("p", "m", 429, 0, false)
       Expect(
           testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "4xx_rate_limited")),
       ).To(Equal(float64(1)))
   })

   It("statusClass(500) with isRouterError=false maps to 5xx_upstream", func() {
       m.ObserveRequest("p", "m", 500, 0, false)
       Expect(
           testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "5xx_upstream")),
       ).To(Equal(float64(1)))
   })

   It("statusClass(500) with isRouterError=true maps to 5xx_router", func() {
       m.ObserveRequest("p", "m", 500, 0, true)
       Expect(
           testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "5xx_router")),
       ).To(Equal(float64(1)))
   })

   It("statusClass(502) with isRouterError=false maps to 5xx_upstream", func() {
       m.ObserveRequest("p", "m", 502, 0, false)
       Expect(
           testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "5xx_upstream")),
       ).To(Equal(float64(1)))
   })

   It("statusClass(413) maps to 4xx_bad_request", func() {
       m.ObserveRequest("p", "m", 413, 0, true)
       Expect(
           testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "4xx_bad_request")),
       ).To(Equal(float64(1)))
   })

   It("statusClass(400) maps to 4xx_bad_request", func() {
       m.ObserveRequest("p", "m", 400, 0, true)
       Expect(
           testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "4xx_bad_request")),
       ).To(Equal(float64(1)))
   })
   ```

   These cover Acceptance Criteria 5, 6, 7, 8.

10. **Add a new `Context("ObserveTokens", ...)` block** in `metrics_test.go`. Use a fresh `handler.NewMetrics(nil)` per spec (mirror the existing `BeforeEach`). Cover:

    ```go
    Context("ObserveTokens", func() {
        It("increments input-direction series with positive count", func() {
            m.ObserveTokens("anthropic-subscription", "claude-opus-4", "input", 42)
            Expect(
                testutil.ToFloat64(m.TokensTotal.WithLabelValues("anthropic-subscription", "claude-opus-4", "input")),
            ).To(Equal(float64(42)))
        })

        It("increments output-direction series with positive count", func() {
            m.ObserveTokens("anthropic-subscription", "claude-opus-4", "output", 17)
            Expect(
                testutil.ToFloat64(m.TokensTotal.WithLabelValues("anthropic-subscription", "claude-opus-4", "output")),
            ).To(Equal(float64(17)))
        })

        It("accumulates repeated Adds on the same tuple", func() {
            m.ObserveTokens("p", "m", "input", 10)
            m.ObserveTokens("p", "m", "input", 5)
            Expect(
                testutil.ToFloat64(m.TokensTotal.WithLabelValues("p", "m", "input")),
            ).To(Equal(float64(15)))
        })

        It("drops zero count without creating a series", func() {
            m.ObserveTokens("p", "m", "input", 0)
            Expect(testutil.CollectAndCount(m.TokensTotal)).To(Equal(0))
        })

        It("drops negative count without creating a series", func() {
            m.ObserveTokens("p", "m", "input", -1)
            Expect(testutil.CollectAndCount(m.TokensTotal)).To(Equal(0))
        })

        It("drops unknown direction (sideways) without creating a series", func() {
            m.ObserveTokens("p", "m", "sideways", 5)
            Expect(testutil.CollectAndCount(m.TokensTotal)).To(Equal(0))
        })

        It("drops empty direction without creating a series", func() {
            m.ObserveTokens("p", "m", "", 5)
            Expect(testutil.CollectAndCount(m.TokensTotal)).To(Equal(0))
        })
    })
    ```

    These cover Acceptance Criteria 9, 10, 11. **Anti-fake note (must appear as a Go comment above the block):** "// Anti-fake: token counts vary across specs (42, 17, 10, 5) so a hardcoded Add(1) implementation fails these assertions."

11. **Add a `Context("UnknownModelLabel constant", ...)` block** asserting the sentinel value:

    ```go
    Context("UnknownModelLabel constant", func() {
        It("resolves to '_unknown_' (leading + trailing underscore)", func() {
            Expect(handler.UnknownModelLabel).To(Equal("_unknown_"))
        })
    })
    ```

    Covers Acceptance Criterion 12 (partial — the model-resolution chain itself is exercised in prompt 2 against `NewModelRouter`, but this asserts the constant's value is available for prompt 2 to consume).

12. **Update the existing NewMetrics constructor doc comment.** Extend the paragraph that documents the alias pre-initialization to note that `TokensTotal` is NOT pre-initialized (there is no closed set of (provider, model, direction) tuples known at boot — cardinality is bounded by real traffic). Do NOT touch the actual pre-init loop.

13. **Update the requests-counter Help text.** The current `RequestsTotal` `Help:` says `... status_class (2xx/4xx/5xx).` — change it to the new enum:

    ```
    Help: "Total number of /v1/* requests routed, labeled by provider, model, and status_class (2xx/3xx/4xx_auth/4xx_rate_limited/4xx_bad_request/5xx_upstream/5xx_router).",
    ```

14. **Run `make precommit` in the repo root.** Fix any lint / format / addlicense / GoDoc issues. `make precommit` must exit 0.

15. **Bridge the single production call site + sibling entry-point check.** The `ObserveRequest` signature change is breaking. Two parts:

    **Part A — mechanical bridge (required to keep `make precommit` green):** update the single production call site at `/workspace/pkg/handler/model-router.go:160` by appending `, false` as the 5th argument. This is signature-only; all semantic wiring (isRouterError=true at the three router-side early-return paths, tokens observation, sentinel-chain resolver) stays for prompt 2:

    ```go
    // Before:
    metrics.ObserveRequest(providerName, origModel, status, latency.Seconds())
    // After (this prompt — mechanical only):
    metrics.ObserveRequest(providerName, origModel, status, latency.Seconds(), false)
    ```

    Do NOT change any other line of `model-router.go` in this prompt. The `origModel`-vs-sentinel bug fix and the three early-return additions are prompt 2's scope.

    **Part B — sibling entry-point check:** confirm no other production caller exists that this prompt would leave stale. Two greps, split by call-shape:

    ```bash
    # Production-side callers: expect exactly 1 (the model-router.go:160 line updated in Part A).
    grep -rn 'metrics\.ObserveRequest(' /workspace/pkg/ /workspace/main.go 2>&1
    # Test-side callers on the m.ObserveRequest receiver: expect exactly 16
    # (8 updated originals from step 8 + 8 new statusClass specs from step 9).
    grep -c 'm\.ObserveRequest(' /workspace/pkg/handler/metrics_test.go
    ```

    Expected: production grep returns exactly 1 hit at `pkg/handler/model-router.go:160` with `, false)`. Test grep returns exactly 16. If the production grep shows any OTHER caller (a factory, another handler, `main.go`), STOP and report — the decomposition splits collector from wiring, so an extra production caller means the decomposition itself must be revisited.

</requirements>

<constraints>
- **Frozen file/seam:** the entire `RequestDuration` HistogramVec + `LatencyBucketsSeconds` var + `AliasResolutions` CounterVec + `ObserveAliasResolution` + `NewMetrics` alias pre-init loop are UNTOUCHED. Verify with `git diff` before running `make precommit`.
- **Bounded enum for `status_class`.** Exactly seven values in normal operation: `2xx`, `3xx`, `4xx_auth`, `4xx_rate_limited`, `4xx_bad_request`, `5xx_upstream`, `5xx_router` (plus the raw-status-code fallback for anything outside 2xx–5xx). No per-status-code label (spec Non-goals).
- **Bounded enum for `direction`.** Exactly two values: `input`, `output`. Anything else is dropped in `ObserveTokens`.
- **No YAML config fields.** This spec adds NO fields to `pkg.Config`. Do NOT modify `pkg/config.go`.
- **Clean supersede.** `metrics_test.go` MUST NOT assert against bare `status_class="4xx"` or `status_class="5xx"` after this prompt lands. Any surviving 4-arg `ObserveRequest` call in a test is a regression.
- **Anti-fake (from spec DoD framing):** token counts in `Context("ObserveTokens", ...)` MUST vary across specs (42, 17, 10, 5 as spelled out in step 10). A hardcoded `Add(1)` implementation must fail the assertions.
- **DoD:** GoDoc on `UnknownModelLabel`, `TokensTotal`, `ObserveTokens`, updated `Metrics`, updated `ObserveRequest`, updated `statusClass`. Ginkgo/Gomega for tests. No `fmt.Printf`. No `replace`/`exclude` in `go.mod`. No `bare return err`.
- **Do NOT commit.** dark-factory handles git.
- **Existing tests must still pass** (after the mechanical `origModel` call-site fix in step 15). All existing `metrics_test.go` specs are updated in step 8 — none of the historical assertions survive against the new label values, so this constraint means "no unrelated regressions in the rest of the `pkg/handler/` suite".
</constraints>

<verification>

```bash
cd /workspace
make precommit
```

Must exit 0. (The mechanical single-line update to `model-router.go:160` in step 15 keeps the compile green.)

```bash
cd /workspace
grep -n 'const UnknownModelLabel' pkg/handler/metrics.go
grep -n 'TokensTotal' pkg/handler/metrics.go
grep -n 'ObserveTokens' pkg/handler/metrics.go
grep -nE 'func statusClass\(status int, isRouterError bool\)' pkg/handler/metrics.go
grep -nE 'func \(m \*Metrics\) ObserveRequest\(provider, model string, status int, latencySeconds float64, isRouterError bool\)' pkg/handler/metrics.go
```

Each grep must return ≥1 match.

```bash
cd /workspace
grep -cE 'ObserveRequest\("p",\s*"m(odel)?"?\s*,\s*[0-9]+\s*,\s*[0-9.]+\)' pkg/handler/metrics_test.go
```

Must return 0 (no surviving 4-arg calls).

```bash
cd /workspace
grep -nE 'status_class="4xx"|status_class="5xx"' pkg/handler/metrics_test.go
```

Must return zero lines (no bare 4xx/5xx assertions).

```bash
cd /workspace
go test ./pkg/handler/... -run TestSuite -ginkgo.v 2>&1 | tail -60
```

Expect: all specs PASS, including the new `Context("ObserveTokens", ...)` block and the new `statusClass` specs for 401/403/429/500-router/500-upstream/502/413/400.

```bash
cd /workspace
grep -rn 'metrics\.ObserveRequest(' pkg/ main.go 2>&1
```

Expect: exactly 1 production hit — `pkg/handler/model-router.go:160` with `, false)` as 5th arg.

```bash
cd /workspace
grep -c 'm\.ObserveRequest(' pkg/handler/metrics_test.go
```

Expect: exactly 16 (8 updated originals from step 8 + 8 new statusClass specs from step 9).

</verification>

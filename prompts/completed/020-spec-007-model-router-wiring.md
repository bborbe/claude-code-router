---
status: completed
spec: ["007"]
summary: 'Wire token counter and router-error taxonomy into NewModelRouter: ExtractUsage moved above sampler gate, three early-return paths call ObserveRequest(isRouterError=true), model label resolved via sentinel chain'
execution_id: claude-code-router-tokens-exec-020-spec-007-model-router-wiring
dark-factory-version: dev
created: "2026-07-01T10:55:00Z"
queued: "2026-07-01T11:03:34Z"
started: "2026-07-01T11:07:08Z"
completed: "2026-07-01T11:11:46Z"
---

<summary>
- On every successful (2xx) upstream call the router now increments the new `ccrouter_tokens_total` counter twice — once for input tokens, once for output tokens — sourced from the already-landed `ExtractUsage` tee.
- The three router-side early-return paths (body-too-large 413, body-read-failed 400, alias-rewrite-failed 500) now call the metrics counter with `isRouterError=true` so those previously-invisible failure paths become chartable in Grafana as `4xx_bad_request` and `5xx_router`.
- The `model` label passed to both counters now resolves through a sentinel chain (post-alias resolved model → pre-alias original → `_unknown_` sentinel) so `model=""` empty labels never reach Prometheus.
- Non-2xx responses do NOT increment the tokens counter — token counting is a strict success-path observation.
- `ExtractUsage` now runs for EVERY 2xx (not only for logged 2xx) so the sampler-suppressed 200 responses still contribute their token counts; the `[req]` log line still respects the sampler gate.
- The `[req]` log line format, `Unwrap()` chain, `ExtractUsage` behavior, alias-resolutions counter, and latency histogram are byte-identical to today.
- Ginkgo integration coverage: token counter increments on realistic Anthropic-shaped SSE and JSON 200 responses, no increment on 502, three early-return paths each hit their expected status_class series, sentinel-chain model resolution.
</summary>

<objective>
Wire the token counter and the router-error taxonomy into `NewModelRouter` at the single dispatch site. Consume the already-landed `ExtractUsage` tee on 2xx responses, route the three early-return paths through `ObserveRequest(..., isRouterError=true)`, and swap the buggy `origModel`-only label for a sentinel-chain resolver. Depends on prompt 1 (collector + signature).
</objective>

<context>
Read first:
- `/workspace/specs/in-progress/007-metrics-tokens-and-error-classes.md` — Desired Behavior 3, 4, 5, 6; Failure Modes rows for sentinel/empty/negative/zero token drops, the three router-side early-return rows, SSE-with-no-usage row; Constraints (frozen `ExtractUsage`, frozen `[req]` line, frozen `usageRecorder`/`statusRecorder` Unwrap chain, frozen 2 MiB tail buffer); Acceptance Criteria 2, 3, 4, 13, 14.
- `/workspace/pkg/handler/model-router.go` — the entire file. Line 90: `start := currentDateTime.Now().Time()`. Line 92–93: `rec := &statusRecorder{...}` + `ur := newUsageRecorder(rec)`. Line 95–110: `MaxBytesReader` + `io.ReadAll` — this is where the two early-returns for 413 (body-too-large) and 400 (body-read-failed) live. Line 115: `origModel := extractModel(body)`. Line 119–133: alias branch; line 122–124: the 500 (alias-rewrite-failed) early-return. Line 148: `target.ServeHTTP(ur, r)`. Line 150–158: status + latency computation. Line 160: `metrics.ObserveRequest(providerName, origModel, status, latency.Seconds(), false)` (updated by prompt 1 to the 5-arg signature — the `false` is the mechanical bridge; this prompt replaces `origModel` with the sentinel-chain resolver but keeps `false` for the happy path). Line 168–170: sampler gate. Line 171–178: `ExtractUsage(ur.Tail(), rec.Header().Get("Content-Type"), rec.Header().Get("Content-Encoding"))` — CURRENTLY runs only if the sampler admits the 200; this prompt moves the extraction ABOVE the sampler gate so metrics are counted for every 2xx. Line 179–191: two `glog.V(1).Infof` branches (alias variant + non-alias variant) — the format is FROZEN.
- `/workspace/pkg/handler/metrics.go` — the file prompt 1 landed. `Metrics.TokensTotal`, `ObserveTokens(provider, model, direction string, count int)`, `ObserveRequest(provider, model string, status int, latencySeconds float64, isRouterError bool)`, `UnknownModelLabel = "_unknown_"`. Consume these exports; do NOT modify this file.
- `/workspace/pkg/handler/usage-recorder.go` — `ExtractUsage(tail []byte, contentType, contentEncoding string) TokenUsage` (frozen); `TokenUsage.Input` / `TokenUsage.Output` are strings; `"-"` and empty string are the sentinel-absent values; a real zero from upstream renders as `"0"`; both fields render via `logLineValue()`. Do NOT modify.
- `/workspace/pkg/handler/model-router_test.go` — existing `alwaysSample`, `testMetrics = handler.NewMetrics(nil)`, `testDateTime`, `labelHandler`, `captureStderr`, `Context("ModelRouter", ...)` setup with `routes` + `mux`. Mirror this pattern for the new specs.
- `/workspace/pkg/factory/factory.go` — verify no wiring change is needed. The `NewModelRouter(...)` call site at line 181 already passes `metrics` (unchanged parameter list). Do NOT modify factory.go.
- `/workspace/docs/dod.md` — DoD: `bborbe/errors` idioms, no bare `return err`, `glog.V(n)` gating, no `fmt.Printf`, Ginkgo/Gomega.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-http-handler-refactoring-guide.md` — response-writer wrapper wiring reminder.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` — CounterVec + label-value observation from the request path.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo integration patterns; `prometheus/client_golang/prometheus/testutil.ToFloat64` + `testutil.CollectAndCount`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `bberrors.Wrapf(ctx, ...)` for any new error path (this prompt does not add error returns; noted for completeness).

**Dependency guard (fail-fast at prompt start):** verify prompt 1 landed by running:

```bash
grep -q 'const UnknownModelLabel = "_unknown_"' /workspace/pkg/handler/metrics.go && \
grep -q 'TokensTotal\s*\*prometheus.CounterVec' /workspace/pkg/handler/metrics.go && \
grep -q 'func (m \*Metrics) ObserveTokens' /workspace/pkg/handler/metrics.go && \
grep -q 'func statusClass(status int, isRouterError bool)' /workspace/pkg/handler/metrics.go && \
grep -q 'func (m \*Metrics) ObserveRequest(provider, model string, status int, latencySeconds float64, isRouterError bool)' /workspace/pkg/handler/metrics.go
```

If any of the five greps fails, STOP and report `dependency not yet deployed: prompt 1 (metrics-collector) has not landed — cannot wire NewModelRouter to a metrics API that does not exist`. Do not attempt to work around by re-adding those exports here; that duplicates prompt 1 and creates a merge conflict.
</context>

<requirements>

1. **Add a `resolveModelLabel` helper in `/workspace/pkg/handler/model-router.go`.** Place it near the bottom of the file next to `extractModel`. Signature and body:

   ```go
   // resolveModelLabel picks the label value to emit into the
   // ccrouter_requests_total and ccrouter_tokens_total counters for the
   // model dimension. Resolution order (spec 007 Desired Behavior 5):
   //
   //   1. resolvedModel (post-alias resolved model, or the pre-alias model
   //      when no alias hit fired) — the string the upstream actually saw.
   //   2. origModel (pre-alias, from extractModel) — used when the alias
   //      branch nulled the resolved value or the resolved is otherwise
   //      empty.
   //   3. UnknownModelLabel ("_unknown_") — the sentinel returned when
   //      both are empty (probe traffic, misshapen body, router-side
   //      early-return before body parse).
   //
   // Never returns the empty string — the goal is that no ccrouter_*
   // series ever carries model="" (spec 007 Goal).
   func resolveModelLabel(resolvedModel, origModel string) string {
       if resolvedModel != "" {
           return resolvedModel
       }
       if origModel != "" {
           return origModel
       }
       return UnknownModelLabel
   }
   ```

2. **Route the three router-side early-return paths through `metrics.ObserveRequest(..., isRouterError=true)`.** Currently the three early returns (body-too-large 413, body-read-failed 400, alias-rewrite-failed 500) do not touch metrics; add a metrics observation before each `return`.

   Latency at these early sites is `currentDateTime.Now().Time().Sub(start).Round(time.Millisecond).Seconds()`. Compute it inline.

   Model label at these sites uses the sentinel chain. For sites BEFORE `origModel := extractModel(body)` (the two `io.ReadAll` failure paths at lines ~97–110), `origModel` does not yet exist → the label resolves to `UnknownModelLabel` directly. For the alias-rewrite-fail site (line ~122), `origModel` is populated → use `resolveModelLabel("", origModel)`.

   Provider label at these sites: the route has not been matched yet → use `UnknownModelLabel` (per spec Desired Behavior 6: "where `provider` is `_unknown_` if unresolved"; the same sentinel string is reused deliberately for these router-error paths per prompt 1's constant doc).

   Concrete rewrites — the body-too-large branch:

   ```go
   // Before:
   if errors.As(err, &maxBytesErr) {
       glog.Warningf(
           "[model-router] request body too large: limit=%d bytes",
           maxBytesErr.Limit,
       )
       http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
       return
   }

   // After:
   if errors.As(err, &maxBytesErr) {
       glog.Warningf(
           "[model-router] request body too large: limit=%d bytes",
           maxBytesErr.Limit,
       )
       http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
       latency := currentDateTime.Now().Time().Sub(start).Round(time.Millisecond)
       metrics.ObserveRequest(UnknownModelLabel, UnknownModelLabel, http.StatusRequestEntityTooLarge, latency.Seconds(), true)
       return
   }
   ```

   The body-read-failed branch (still inside the `if err != nil` block, after the `errors.As` guard):

   ```go
   // Before:
   glog.Errorf("[model-router] read body failed: %v", err)
   http.Error(w, "read body failed", http.StatusBadRequest)
   return

   // After:
   glog.Errorf("[model-router] read body failed: %v", err)
   http.Error(w, "read body failed", http.StatusBadRequest)
   latency := currentDateTime.Now().Time().Sub(start).Round(time.Millisecond)
   metrics.ObserveRequest(UnknownModelLabel, UnknownModelLabel, http.StatusBadRequest, latency.Seconds(), true)
   return
   ```

   The alias-rewrite-failed branch (around line 122):

   ```go
   // Before:
   if rerr != nil {
       glog.Errorf("[alias] rewrite failed for %q -> %q: %v", model, resolved, rerr)
       http.Error(w, "alias rewrite failed", http.StatusInternalServerError)
       return
   }

   // After:
   if rerr != nil {
       glog.Errorf("[alias] rewrite failed for %q -> %q: %v", model, resolved, rerr)
       http.Error(w, "alias rewrite failed", http.StatusInternalServerError)
       latency := currentDateTime.Now().Time().Sub(start).Round(time.Millisecond)
       modelLabel := resolveModelLabel("", origModel)
       metrics.ObserveRequest(UnknownModelLabel, modelLabel, http.StatusInternalServerError, latency.Seconds(), true)
       return
   }
   ```

   Ordering matters: the `metrics.ObserveRequest` call MUST come AFTER `http.Error` (so status is already on the wire; the counter reflects what the client saw) and BEFORE `return`.

3. **Rewrite the main-path `metrics.ObserveRequest` call at line 160** to use the sentinel-chain resolver and to pass `isRouterError=false` explicitly:

   ```go
   // Before (post-prompt-1 mechanical bridge state):
   metrics.ObserveRequest(providerName, origModel, status, latency.Seconds(), false)

   // After:
   modelLabel := resolveModelLabel(model, origModel)
   metrics.ObserveRequest(providerName, modelLabel, status, latency.Seconds(), false)
   ```

   The `model` variable at this point in `NewModelRouter` holds the post-alias resolved model (or the pre-alias model when no alias fired, because line 116 does `model := origModel`). So `resolveModelLabel(model, origModel)` returns the upstream-facing model on the happy path, falls back to `origModel` if the alias branch somehow blanked it (defensive), and finally falls back to `_unknown_` for probe traffic. `modelLabel` is used ONLY for metrics labels — the `[req]` log line still prints `origModel` (frozen format, see step 5).

4. **Move `ExtractUsage` above the sampler gate; wire `ObserveTokens` on every 2xx.** Currently:

   ```go
   if status == http.StatusOK && !sampler.IsSample() {
       return
   }
   usage := noUsage
   if status == http.StatusOK {
       usage = ExtractUsage(
           ur.Tail(),
           rec.Header().Get("Content-Type"),
           rec.Header().Get("Content-Encoding"),
       )
   }
   in, out := usage.logLineValue()
   ```

   Replace with:

   ```go
   usage := noUsage
   if status == http.StatusOK {
       usage = ExtractUsage(
           ur.Tail(),
           rec.Header().Get("Content-Type"),
           rec.Header().Get("Content-Encoding"),
       )
       recordTokensFromUsage(metrics, providerName, modelLabel, usage)
   }
   if status == http.StatusOK && !sampler.IsSample() {
       return
   }
   in, out := usage.logLineValue()
   ```

   Rationale: token counts must be observed on every 2xx, not just on logged 2xx (the sampler suppresses ~9/10 of 200s in steady state; token metrics with sampler-gated extraction would systematically undercount). The `[req]` log line is still gated by the sampler (unchanged behavior).

5. **Add `recordTokensFromUsage` helper.** Place next to `resolveModelLabel`. Signature and body:

   ```go
   // recordTokensFromUsage parses the string-shaped input/output token
   // counts produced by ExtractUsage and increments the ccrouter_tokens_total
   // counter twice — once for direction=input, once for direction=output.
   //
   // Drop rules (spec 007 Failure Modes):
   //   - Empty string or "-" sentinel   -> that direction is not counted;
   //                                        the other direction (if valid)
   //                                        is counted independently.
   //   - Non-numeric string (schema drift) -> parse fails, that direction
   //                                        is dropped, glog.V(2) diagnostic.
   //   - Zero or negative count         -> absorbed by ObserveTokens'
   //                                        zero-drop rule (no series
   //                                        created).
   //
   // Token counting is best-effort observability: a parse failure never
   // affects the request-serving path.
   func recordTokensFromUsage(metrics *Metrics, provider, model string, usage TokenUsage) {
       recordTokenDirection(metrics, provider, model, "input", usage.Input)
       recordTokenDirection(metrics, provider, model, "output", usage.Output)
   }

   func recordTokenDirection(metrics *Metrics, provider, model, direction, raw string) {
       if raw == "" || raw == "-" {
           return
       }
       n, err := strconv.Atoi(raw)
       if err != nil {
           glog.V(2).Infof("[tokens] parse %s=%q failed: %v", direction, raw, err)
           return
       }
       metrics.ObserveTokens(provider, model, direction, n)
   }
   ```

   Import `strconv` at the top of `model-router.go` (verify it's not already imported; add to the import block if not). `glog` is already imported. `Metrics` and `TokenUsage` are already in the `handler` package — no import change for those.

6. **Do NOT change the `[req]` log line format.** Both `glog.V(1).Infof` calls (the alias variant and the non-alias variant) keep their exact format strings, argument order, and V(1) gating. Verify with a grep after editing:

   ```bash
   grep -c 'in=%s out=%s' /workspace/pkg/handler/model-router.go
   ```

   Must return 2 (unchanged from the pre-edit state).

7. **Add integration test coverage in `/workspace/pkg/handler/model-router_test.go`.** Create a new top-level `Describe("ModelRouter metrics wiring", ...)` block (sibling to the existing `Describe("ModelRouter", ...)`). Each spec constructs a FRESH `handler.NewMetrics(nil)` per spec (do NOT reuse the package-level `testMetrics` — token counter series must be isolated per spec). Mirror the existing `httptest.NewRecorder` + `httptest.NewRequest` + `mux.ServeHTTP` pattern from the existing `Describe("ModelRouter", ...)`.

   Anti-fake note (must appear as a Go comment above the block): "// Anti-fake: token counts vary across specs; a hardcoded Add(1) or missing sentinel-chain resolution fails these assertions."

   Required specs:

   - **It("increments ccrouter_tokens_total{direction=input} and {direction=output} on a 200 SSE response")** — upstream handler writes an SSE stream whose terminal `event: message_delta` carries `{"usage":{"input_tokens":42,"output_tokens":17}}` (mirror the pattern from the existing `Context("token usage in [req] line", ...)` block if present). Post-request, assert `testutil.ToFloat64(m.TokensTotal.WithLabelValues("minimax", "MiniMax-M3-highspeed", "input"))` equals `42.0` and the `output` series equals `17.0`.

   - **It("increments ccrouter_tokens_total on a 200 JSON response")** — upstream handler writes `{"id":"msg_01","usage":{"input_tokens":100,"output_tokens":5}}` with `Content-Type: application/json`. Assert input=100, output=5. Distinct numbers from the SSE case.

   - **It("does not increment ccrouter_tokens_total on a 502 upstream error")** — upstream handler writes `502`. Assert `testutil.CollectAndCount(m.TokensTotal)` equals 0 and `testutil.ToFloat64(m.RequestsTotal.WithLabelValues("<provider>", "<model>", "5xx_upstream"))` equals 1.

   - **It("does not increment ccrouter_tokens_total on a 200 with no parseable usage")** — upstream writes `{"ok":true}` (no `usage` block). Assert `testutil.CollectAndCount(m.TokensTotal)` equals 0.

   - **It("does not increment ccrouter_tokens_total on a 200 with zero-token usage")** — upstream writes `{"usage":{"input_tokens":0,"output_tokens":0}}`. Assert `testutil.CollectAndCount(m.TokensTotal)` equals 0 (zero-drop rule).

   - **It("increments only the positive direction when the other is missing")** — upstream writes an SSE stream whose `message_start` carries `input_tokens=7` but `message_delta` has no usage (`ExtractUsage` returns `{Input: "7", Output: ""}`). Assert `input` series equals 7, `output` series does not exist (`CollectAndCount` for the specific label tuple is 0). Use `testutil.CollectAndCount(m.TokensTotal)` returning 1 as the shape check.

   - **It("emits ccrouter_requests_total{status_class=4xx_bad_request} on a body-read-failed early-return")** — construct a request whose body reader returns an error before EOF (e.g. `httptest.NewRequest` with an `io.LimitReader` / a custom `io.ReadCloser` that returns `errors.New("boom")` on first `Read`). Assert `RequestsTotal.WithLabelValues("_unknown_", "_unknown_", "4xx_bad_request")` equals 1. Rationale: 400 with `isRouterError=true` maps to `4xx_bad_request` (not `_auth`/`_rate_limited`) per prompt 1's `statusClass` enum.

   - **It("emits ccrouter_requests_total{status_class=4xx_bad_request} on a body-too-large 413 early-return")** — construct a request whose body reader is a custom `io.ReadCloser` returning `&http.MaxBytesError{Limit: 1}` directly on first `Read`. Do NOT allocate 32 MB — the `MaxBytesReader` wrapper in `NewModelRouter` propagates any `*http.MaxBytesError` back through `errors.As`, so a mock reader is the clean path. Assert `RequestsTotal.WithLabelValues("_unknown_", "_unknown_", "4xx_bad_request")` equals 1.

   - **PIt("emits ccrouter_requests_total{status_class=5xx_router} on an alias-rewrite-failed early-return")** — mark this spec `PIt` (pending) with the comment: `// PIt: rewriteModelField failure requires a test-only seam (package-level var override) not yet plumbed. AC 13's "≥3 lines" evidence is satisfied by the production-code grep on model-router.go — this integration test is future work.` Rationale: `rewriteModelField` cannot fail on any input that reaches it (`extractModel` returns non-empty `Model` string only on JSON objects; `map[string]json.RawMessage` unmarshal + `json.Marshal` never fails on data that already round-tripped through `extractModel`). Reaching the 500 branch requires monkey-patching `rewriteModelField` via a package-level `var rewriteModelFieldFn = rewriteModelField` seam — deferred to a follow-up spec if AC 13 evidence is ever tightened from grep to runtime.

   - **It("increments ccrouter_tokens_total on a sampler-suppressed 200 — extraction runs above the sampler gate")** — critical regression guard for step 4's extract-above-gate move. Use a sampler that ALWAYS returns false (`liblog.NewSamplerFalse()` or an equivalent stub). Upstream handler writes an SSE stream with `input_tokens=13, output_tokens=7`. Post-request assertions:
     - `testutil.ToFloat64(m.TokensTotal.WithLabelValues("<provider>", "<model>", "input"))` equals `13.0` — proves tokens ARE counted even when the sampler suppresses the log.
     - `testutil.ToFloat64(m.TokensTotal.WithLabelValues("<provider>", "<model>", "output"))` equals `7.0`.
     - `captureStderr(...)` shows NO `[req]` line — the log-line sampler gate is unchanged.

     Without this spec, a future refactor could silently revert the extract-above-gate move (moving `ExtractUsage` back below the sampler `return`) and every existing spec would still pass because they all use `alwaysSample`.

   - **It("resolves model label via sentinel chain: resolved > orig > _unknown_")** — three sub-specs OR one table-driven spec:
     - Body `{"model":"m3"}` with `aliases: {"m3": "MiniMax-M3-highspeed"}` → post-request, `RequestsTotal.WithLabelValues("minimax", "MiniMax-M3-highspeed", "2xx")` equals 1 (resolved wins).
     - Body `{"model":"m3"}` with `aliases: nil` and no matching route → post-request, `RequestsTotal.WithLabelValues("default-fallback", "m3", "2xx")` equals 1 (origModel used when no alias resolves).
     - Body `{}` (no `model` field) → post-request, `RequestsTotal.WithLabelValues("default-fallback", "_unknown_", "2xx")` equals 1 (sentinel used when both empty).

   These specs cover Acceptance Criteria 2, 3, 4, 13, 14.

8. **Update the existing `alwaysSample`-based specs to accommodate the new extraction-above-sampler-gate structure.** The one existing spec that may regress is `"does not extract usage on a suppressed 200 (sampler returns false)"` from prompt 016 — it now MUST tolerate extraction happening on a suppressed 200 (since we moved `ExtractUsage` above the sampler gate). If that spec asserts "no `ExtractUsage` call occurred", weaken it to "no `[req]` line was emitted" (the sampler-gate behavior that spec really guards). Verify:

   ```bash
   grep -n 'does not extract usage on a suppressed' /workspace/pkg/handler/*_test.go
   ```

   If it exists, revise the assertion to check only the log-line absence via `captureStderr`; do NOT delete the spec (the sampler-gate log-suppression contract is still load-bearing).

9. **Sibling entry-point check.** Verify:

   ```bash
   grep -rn 'NewModelRouter(' /workspace/pkg/ /workspace/main.go 2>&1
   grep -rn 'metrics.ObserveRequest\|metrics.ObserveTokens' /workspace/pkg/ /workspace/main.go 2>&1
   ```

   Expected: `NewModelRouter` has one production call site (`pkg/factory/factory.go:181`, unchanged) and multiple test call sites in `model-router_test.go`. `metrics.ObserveRequest` appears in `model-router.go` at four sites (three early-return + one happy-path). `metrics.ObserveTokens` appears in `model-router.go` at exactly one site (inside `recordTokenDirection`). If any other production call site to `metrics.ObserveRequest` exists (e.g. a middleware), STOP and report — spec 007 Constraints froze the `model-router.go` seam as the single call site.

10. **Run `make precommit` in the repo root.** Must exit 0.

</requirements>

<constraints>
- **Frozen behavior — `ExtractUsage`.** Do NOT modify `pkg/handler/usage-recorder.go`. Consume its result as-is; parse `Input`/`Output` strings via `strconv.Atoi`.
- **Frozen behavior — 2 MiB tail buffer + `usageRecorder` + `statusRecorder` `Unwrap()` chain.** Do NOT touch. SSE flush passthrough is load-bearing.
- **Frozen behavior — `[req]` log line.** The two `glog.V(1).Infof` calls keep their exact format strings, argument order, and V(1) gating. `in=%s out=%s` appears at the END of both format strings.
- **Frozen behavior — AliasResolutions counter + pre-init pattern.** Do NOT touch.
- **Frozen behavior — RequestDuration HistogramVec + LatencyBucketsSeconds.** Do NOT touch.
- **Frozen listener — `0.0.0.0:8788` + `/metrics` via `promhttp.Handler()`.** No auth. Do NOT modify `pkg/factory/factory.go` mux wiring.
- **No new YAML config fields.** Do NOT modify `pkg/config.go`.
- **Non-200 responses do NOT touch the tokens counter.** The `if status == http.StatusOK` guard around the `ExtractUsage`+`recordTokensFromUsage` block is load-bearing (spec Desired Behavior 4, Non-goal "Do NOT record tokens on non-200 responses").
- **Sentinel is `UnknownModelLabel` from `pkg/handler/metrics.go`.** Do NOT redeclare or shadow the constant. Import path is the same package (`handler`) so it is referenced as `UnknownModelLabel` directly.
- **`isRouterError=true` on all three early-return paths.** No exception. `isRouterError=false` on the happy-path call at line 160.
- **`bborbe/errors` idioms + `glog.V(n)` gating.** No `fmt.Printf`. No bare `return err`. New helpers `resolveModelLabel`, `recordTokensFromUsage`, `recordTokenDirection` are unexported (`resolveModelLabel` starts lowercase); GoDoc still required.
- **Do NOT commit.** dark-factory handles git.
- **Existing tests must still pass.** The full `pkg/handler/...` suite, including the SSE-flush regression spec and the sampler-gating specs, still passes.
</constraints>

<verification>

```bash
cd /workspace
make precommit
```

Must exit 0.

```bash
cd /workspace
grep -c 'metrics.ObserveRequest' pkg/handler/model-router.go
```

Must return 4 (three early-return sites + one happy-path site).

```bash
cd /workspace
grep -c 'ObserveRequest.*, true)' pkg/handler/model-router.go
```

Must return exactly 3 (the three early-return sites — Acceptance Criterion 13's evidence). The happy-path passes `false` and does not match this pattern. Using `ObserveRequest.*, true)` (not just `, true)`) prevents matching any incidental `foo(bar, true)` in the file.

```bash
cd /workspace
grep -n 'metrics.ObserveTokens\|recordTokenDirection\|recordTokensFromUsage' pkg/handler/model-router.go
```

Must return ≥3 lines (the helpers + the call site).

```bash
cd /workspace
grep -n 'func resolveModelLabel' pkg/handler/model-router.go
```

Must return 1 line.

```bash
cd /workspace
grep -c 'in=%s out=%s' pkg/handler/model-router.go
```

Must return 2 (both `[req]` log variants unchanged).

```bash
cd /workspace
go test ./pkg/handler/... -run TestSuite -ginkgo.v 2>&1 | tail -80
```

Expect: all existing specs PASS + the new `Describe("ModelRouter metrics wiring", ...)` specs PASS.

```bash
cd /workspace
grep -rn 'NewModelRouter(' pkg/ main.go 2>&1
```

Expect: one call site in `pkg/factory/factory.go` (unchanged) + N call sites in `pkg/handler/model-router_test.go`.

</verification>

<!-- Operator manual verification (post-merge, outside the container's scope — belongs in the spec's Verification section, not the container agent's runnable set): after `make install` + launchd reload, hit `/v1/messages` with a real Anthropic key, then:

```
curl -s http://127.0.0.1:8788/metrics | grep ccrouter_tokens_total     # ≥1 input + ≥1 output, both >0
curl -s http://127.0.0.1:8788/metrics | grep 'model=""'                # zero lines
curl -s http://127.0.0.1:8788/metrics | grep -E 'status_class="(4xx|5xx)"'  # zero lines (clean supersede)
```
-->

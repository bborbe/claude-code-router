---
status: completed
spec: ["007"]
summary: Updated docs/metrics.md (Series table, status_class cardinality, Grafana queries, alert) and CHANGELOG.md (## Unreleased) to reflect ccrouter_tokens_total counter and 7-value status_class taxonomy shipped by prompts 1 and 2
execution_id: claude-code-router-tokens-exec-021-spec-007-docs-and-changelog
dark-factory-version: dev
created: "2026-07-01T10:55:00Z"
queued: "2026-07-01T11:03:34Z"
started: "2026-07-01T11:11:47Z"
completed: "2026-07-01T11:13:19Z"
---

<summary>
- `docs/metrics.md` documents the new `ccrouter_tokens_total` counter in the Series table with its labels and example value.
- `docs/metrics.md` documents the seven-value `status_class` enum (`2xx`, `3xx`, `4xx_auth`, `4xx_rate_limited`, `4xx_bad_request`, `5xx_upstream`, `5xx_router`) replacing the previous four-bucket description.
- `docs/metrics.md` updates the cardinality note from ~225 counter series to ~525 (ceiling; ~450 in practice) to reflect the seven-value split plus the new tokens counter.
- `docs/metrics.md` gains two new Grafana query examples — "tokens/s by provider and direction" and "error-class breakdown" — so operators have copy-paste dashboards for the new series.
- `docs/metrics.md` updates the `AnthropicQuotaNearExhaustion` alert to filter `status_class="4xx_rate_limited"` instead of the generic `"4xx"`.
- `CHANGELOG.md` gains a new `## Unreleased` entry describing the new counter, the taxonomy expansion, and the empty-model-label fix.
- This is a docs-only prompt — no code changes; depends on prompts 1 (label names) and 2 (wiring landed).
</summary>

<objective>
Bring `docs/metrics.md` and `CHANGELOG.md` in sync with the code shipped by prompts 1 and 2 so operators writing new Grafana panels or alert rules see the correct label taxonomy, the new tokens series, and updated cardinality. Docs-only — no Go code changes.
</objective>

<context>
Read first:
- `/workspace/specs/in-progress/007-metrics-tokens-and-error-classes.md` — Desired Behavior 7, 8; Acceptance Criteria 16, 17, 18, 19, 20, 21.
- `/workspace/docs/metrics.md` — current shape. Scrape config block, `## Series` table with three rows, cardinality paragraph, three Grafana query blocks, one alerting example. This file is 60 lines total; every content section will be touched.
- `/workspace/CHANGELOG.md` — top section is `## v0.17.2` (line 7). Prompt 018 has ALREADY LANDED that version. This prompt inserts a NEW `## Unreleased` section ABOVE `## v0.17.2`. Never rename an existing released version.
- `/workspace/pkg/handler/metrics.go` — verify the exact metric names and label ordering shipped by prompt 1 (`ccrouter_tokens_total` labels: `provider`, `model`, `direction`).
- `/workspace/pkg/handler/model-router.go` — verify the router-side error paths that populate `5xx_router` and `4xx_bad_request` so the docs cite the correct trigger conditions.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` placement + conventional-prefix bullet rules (feature branches use `## Unreleased`, never a version number; newest first, above the highest released `## vX.Y.Z`).

**Dependency guard (fail-fast at prompt start):** verify prompts 1 AND 2 landed:

```bash
grep -q 'TokensTotal' /workspace/pkg/handler/metrics.go && \
grep -q '4xx_rate_limited' /workspace/pkg/handler/metrics.go && \
grep -q 'ObserveTokens' /workspace/pkg/handler/model-router.go && \
grep -q 'resolveModelLabel' /workspace/pkg/handler/model-router.go
```

If any check fails, STOP and report `dependency not yet deployed: docs update requires prompts 1 and 2 to have landed the label names and wiring`. Do not attempt to write docs for a partial code state — the docs cite exact label names that must match the code.
</context>

<requirements>

1. **Update the `## Series` table in `/workspace/docs/metrics.md`.** Replace the current three-row table:

   ```markdown
   | Metric | Labels | Type | Example value |
   |---|---|---|---|
   | `ccrouter_requests_total` | `provider`, `model`, `status_class` | counter | `3` |
   | `ccrouter_request_duration_seconds` | `provider`, `model` | histogram | `0.842` (p95 bucket) |
   | `ccrouter_alias_resolutions_total` | `alias`, `resolved` | counter | `1` |
   ```

   with a four-row table adding the new `ccrouter_tokens_total` metric:

   ```markdown
   | Metric | Labels | Type | Example value |
   |---|---|---|---|
   | `ccrouter_requests_total` | `provider`, `model`, `status_class` | counter | `3` |
   | `ccrouter_request_duration_seconds` | `provider`, `model` | histogram | `0.842` (p95 bucket) |
   | `ccrouter_alias_resolutions_total` | `alias`, `resolved` | counter | `1` |
   | `ccrouter_tokens_total` | `provider`, `model`, `direction` | counter | `42` (input) / `17` (output) |
   ```

2. **Replace the `status_class` description paragraph** that currently reads:

   > `status_class` is one of `2xx`, `3xx`, `4xx`, `5xx`, or the raw status code for out-of-range values. Cardinality: ~1k series at 5 providers × 15 models × 3 status classes (225 counter series + 750 histogram bucket series + alias counter bounded by YAML config).

   with:

   > `status_class` is one of `2xx`, `3xx`, `4xx_auth` (401/403), `4xx_rate_limited` (429), `4xx_bad_request` (all other 4xx — including the router-side 413 body-too-large and 400 body-read-failed early returns), `5xx_upstream` (5xx from the upstream provider), `5xx_router` (5xx from a router-side rejection — currently the alias-rewrite-failed path), or the raw status code for out-of-range values. The `5xx_upstream`/`5xx_router` split is driven by an `isRouterError` argument at the `ObserveRequest` call site — see `pkg/handler/metrics.go`. Cardinality ceiling: 5 providers × 15 models × 7 status classes = 525 request-counter series (~450 in practice — some (provider, model, status_class) tuples never fire) + 5 × 15 × 2 directions = 150 tokens-counter series + 5 × 15 × len(buckets) = 750 histogram bucket series + alias counter bounded by YAML config = ~1.5k total. Operators sizing Prometheus retention should plan for the ~1.5k combined-series ceiling.
   >
   > `direction` on `ccrouter_tokens_total` is bounded to `input` or `output`; any other direction value is dropped at `ObserveTokens` and never reaches Prometheus. Non-2xx responses do not increment `ccrouter_tokens_total` (token counting is a strict success-path observation — a failed upstream call does not carry a trustworthy usage object).
   >
   > `model` on all `ccrouter_*` series resolves through a sentinel chain (post-alias resolved model → pre-alias original model → `_unknown_`), so no `model=""` empty label ever reaches Prometheus. The `_unknown_` sentinel also appears as the `provider` label value on the three router-side early-return paths (body-too-large, body-read-failed, alias-rewrite-failed) where routing never resolved a provider.

3. **Replace the three current Grafana query blocks with an expanded set.** Delete the existing three blocks (`Requests per second by provider`, `p95 latency by provider`, `4xx rate by provider`) and replace with:

   ```markdown
   ## Grafana queries

   **Requests per second by provider:**
   ```promql
   sum by (provider) (rate(ccrouter_requests_total[5m]))
   ```

   **p95 latency by provider:**
   ```promql
   histogram_quantile(0.95, sum by (le, provider) (rate(ccrouter_request_duration_seconds_bucket[5m])))
   ```

   **Error-class breakdown by provider** — distinguishes 429 quota exhaustion, 401/403 auth failures, generic 4xx client errors, upstream 5xx, and router-side 5xx:
   ```promql
   sum by (provider, status_class) (rate(ccrouter_requests_total{status_class=~"4xx_.*|5xx_.*"}[5m]))
   ```

   **429 rate specifically** (subscription-quota near-exhaustion signal):
   ```promql
   sum by (provider) (rate(ccrouter_requests_total{status_class="4xx_rate_limited"}[5m]))
   ```

   **Tokens/s by provider and direction** — LLM token throughput broken down by input vs output:
   ```promql
   sum by (provider, direction) (rate(ccrouter_tokens_total[5m]))
   ```

   **Tokens/s by model** — per-model verbosity comparison:
   ```promql
   sum by (model, direction) (rate(ccrouter_tokens_total[5m]))
   ```
   ```

   **Do not include** the historical note about "if you need 429-specifically, expand `status_class` into a per-status-code label" — that note is now obsolete; the 7-value enum already provides the 429 series.

4. **Update the `AnthropicQuotaNearExhaustion` alert.** Replace the current alert body:

   ```yaml
   - alert: AnthropicQuotaNearExhaustion
     expr: |
       sum by (provider) (
         rate(ccrouter_requests_total{provider="anthropic-subscription",status_class="4xx"}[5m])
       ) * 60 > 10
     for: 5m
     labels:
       severity: warning
     annotations:
       summary: "429 spike on {{ $labels.provider }} — >10 4xx/min for 5 min; quota may be near exhaustion"
   ```

   with:

   ```yaml
   - alert: AnthropicQuotaNearExhaustion
     expr: |
       sum by (provider) (
         rate(ccrouter_requests_total{provider="anthropic-subscription",status_class="4xx_rate_limited"}[5m])
       ) * 60 > 10
     for: 5m
     labels:
       severity: warning
     annotations:
       summary: "429 spike on {{ $labels.provider }} — >10 429/min for 5 min; subscription quota may be near exhaustion"
   ```

   The label-selector change (`4xx` → `4xx_rate_limited`) is the load-bearing edit; the summary text update is a follow-on (`4xx/min` → `429/min`) so the annotation matches the label filter.

5. **Insert `## Unreleased` in `/workspace/CHANGELOG.md`** directly above `## v0.17.2` (line 7). Do NOT modify the `# Changelog` header block or the SemVer preamble. Do NOT rename `## v0.17.2` or any prior version. New content:

   ```markdown
   ## Unreleased

   - feat: add `ccrouter_tokens_total{provider,model,direction}` counter fed from the already-landed `ExtractUsage` tee. Operators can chart `sum by (provider, direction) (rate(ccrouter_tokens_total[5m]))` to see LLM token throughput per provider and per model. Direction is bounded to `input`/`output`; zero, negative, non-numeric, and unknown-direction inputs are dropped at the `ObserveTokens` seam so bad upstream data never inflates Prometheus. Non-2xx responses do not increment the counter — token counting is a strict success-path observation.
   - feat: expand `ccrouter_requests_total{status_class}` from 4 buckets (`2xx`/`3xx`/`4xx`/`5xx`) to a 7-value taxonomy (`2xx`, `3xx`, `4xx_auth` for 401/403, `4xx_rate_limited` for 429, `4xx_bad_request` for other 4xx, `5xx_upstream` for upstream 5xx, `5xx_router` for router-side 5xx). Operators can now alert on `status_class="4xx_rate_limited"` specifically instead of any 4xx, distinguish auth failures from body-parse failures, and separate upstream faults from router-side rejections. See `docs/metrics.md` for updated Grafana + alerting examples. **Breaking:** this is a clean supersede — dashboards or alerts built against `status_class="4xx"` or `status_class="5xx"` will return empty on merge and must be updated to the 7-value enum.
   - feat: route the three router-side early-return paths (body-too-large 413, body-read-failed 400, alias-rewrite-failed 500) through `metrics.ObserveRequest(..., isRouterError=true)` so they emit `4xx_bad_request` / `5xx_router` in `ccrouter_requests_total`. Previously these paths bypassed metrics entirely — an operator staring at Grafana saw a healthy service while requests were being rejected at the door.
   - fix: model label on `ccrouter_requests_total` and `ccrouter_tokens_total` no longer emits `model=""` for probe traffic or misshapen bodies. New sentinel chain (post-alias resolved → pre-alias original → `_unknown_`) resolves the label at the call site; the exported `handler.UnknownModelLabel = "_unknown_"` constant provides the sentinel. Every "top-N models" Grafana panel now breaks down real model names — no empty-string row hiding the sum of no-model-field traffic.
   - **Breaking:** `handler.Metrics.ObserveRequest` signature gains a 5th positional argument `isRouterError bool`. The happy path at `NewModelRouter` passes `false`; the three router-side early-return paths pass `true`. Existing `metrics_test.go` call sites (8) were updated in lockstep.
   ```

   Per the changelog guide: feature branch uses `## Unreleased`, never a version number. Do NOT bump `## v0.17.2` or add a `## v0.18.0` — release tooling handles the version rename at cut time.

6. **Do NOT touch:**
   - The Prometheus scrape config YAML block at the top of `docs/metrics.md`.
   - The intro paragraph about `promhttp.Handler()` and the `ccrouter_` prefix.
   - Any Go source file in `pkg/`, `main.go`, or `pkg/factory/`.
   - Any test file (all covered by prompts 1 and 2).
   - `README.md`, `docs/config.md`, `docs/config.example.yaml` (no new YAML fields; nothing to add).
   - `docs/dod.md` (frozen).
   - Previously released `## vX.Y.Z` sections in `CHANGELOG.md`.

7. **Run `make precommit` in the repo root.** Docs-only changes should not trigger any Go compile step, but `make precommit` also runs `addlicense` and any markdown lint the repo enforces. Must exit 0.

</requirements>

<constraints>
- **Frozen files:** all `.go` files. This prompt is doc-only.
- **Frozen doc sections:** the Prometheus scrape config YAML block, the intro paragraph in `docs/metrics.md`.
- **`## Unreleased` placement (from changelog-guide.md):** ABOVE the highest released `## vX.Y.Z` (currently `## v0.17.2`). Never a version number on the feature branch.
- **Conventional-prefix bullets** in the changelog entry: `feat:`, `fix:`, `breaking:` per the changelog guide. `feat:` for new counter + taxonomy + router-error routing; `fix:` for the empty-label sentinel; `**Breaking:**` for the `ObserveRequest` signature change.
- **Label names in docs MUST match code exactly.** `ccrouter_tokens_total`, labels in order `provider,model,direction`, direction values `input`/`output`, status_class enum values as spelled out in step 2. A rename in prompt 1 (e.g. `directions="in"/"out"`) that this prompt does not mirror is a doc drift regression.
- **Do NOT commit.** dark-factory handles git.
- **Existing tests must still pass** (no code touched, so this is a trivial constraint — `make precommit`'s test phase should pass unchanged).
</constraints>

<verification>

```bash
cd /workspace
make precommit
```

Must exit 0.

```bash
cd /workspace
grep -c 'ccrouter_tokens_total' docs/metrics.md
```

Must return ≥3 (Series table row + at least two Grafana query blocks).

```bash
cd /workspace
grep -c '4xx_rate_limited\|4xx_auth\|4xx_bad_request\|5xx_upstream\|5xx_router' docs/metrics.md
```

Must return ≥5 (Acceptance Criterion 16 evidence).

```bash
cd /workspace
grep -n 'sum by (provider, direction)' docs/metrics.md
```

Must return ≥1 (Acceptance Criterion 19 evidence).

```bash
cd /workspace
grep -nE '525|~450' docs/metrics.md
```

Must return ≥1 (Acceptance Criterion 18 evidence — new cardinality note; the ceiling is 525, ~450 is the practical estimate).

```bash
cd /workspace
grep -nE '"4xx"|"5xx"' docs/metrics.md
```

Must return 0 (no surviving bare-4xx/bare-5xx references in queries or alerts).

```bash
cd /workspace
grep -n '4xx_rate_limited' docs/metrics.md
```

Must include at least one match in the alerting example (Acceptance Criterion 20 evidence).

```bash
cd /workspace
grep -n '## Unreleased' CHANGELOG.md
```

Must return line number ABOVE the current `## v0.17.2` line (verify: `grep -n '## v0.17.2' CHANGELOG.md` shows a larger line number).

```bash
cd /workspace
grep -ni 'ccrouter_tokens_total\|4xx_rate_limited\|status_class' CHANGELOG.md | head -5
```

Must return ≥1 line above `## v0.17.2` (Acceptance Criterion 21 evidence).

</verification>

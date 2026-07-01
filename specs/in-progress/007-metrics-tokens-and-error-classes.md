---
status: verifying
tags:
    - dark-factory
    - spec
approved: "2026-07-01T10:46:18Z"
generating: "2026-07-01T10:55:24Z"
prompted: "2026-07-01T10:55:24Z"
verifying: "2026-07-01T11:13:19Z"
branch: dark-factory/metrics-tokens-and-error-classes
---

## Summary

- Add a new `ccrouter_tokens_total{provider, model, direction}` counter (direction ∈ `input`, `output`) fed from the already-landed `ExtractUsage` tee, so operators can chart LLM token throughput per provider and per model without leaving Grafana.
- Split `status_class` from 4 buckets (`2xx`/`3xx`/`4xx`/`5xx`) into a 7-value taxonomy (`2xx`, `3xx`, `4xx_rate_limited`, `4xx_bad_request`, `4xx_auth`, `5xx_upstream`, `5xx_router`) so 429 quota spikes, upstream 500s, and router-side rejections are visible as first-class series.
- Fix the empty-string `model=""` label regression by resolving the label at the call site through a sentinel (`_unknown_`) when the request body has no `model` field.
- Route the three router-side early-return sites (body-too-large, body-read-fail, alias-rewrite-fail) through the metrics counter as `5xx_router` / `4xx_bad_request`, so these currently-invisible failure paths become chartable.
- Update `docs/metrics.md` (series table, cardinality note, Grafana examples, alerting example) so scrape config, dashboards, and alerts match the new label taxonomy.

## Problem

The current `/metrics` endpoint gives operators request counts, latency histograms, and alias resolutions — but it does not answer the two questions that show up most in day-to-day router operation:

1. **"How many tokens am I burning per provider and per model right now?"** — the extractor has been in place for two days (`ExtractUsage` from PR #30–#32) and its result is only logged on the `[req]` line. Operators cannot chart `tokens/s by provider`, cannot compare per-model verbosity, and cannot forecast subscription-quota exhaustion from Grafana.
2. **"Is this a 429 rate-limit spike, an auth failure, or my body-parsing code exploding?"** — every non-2xx currently collapses into `4xx` or `5xx`. A `429` quota alert has to look at the raw router log; a router-internal 500 (body-too-large, alias-rewrite fail, body-read fail) never reaches `ObserveRequest` at all because those early-return paths skip the metrics call.

On top of that, `model-router.go` passes the raw body-extracted model name to `ObserveRequest`. When a request arrives with no `model` field (probe traffic, misshapen bodies), Prometheus records a `model=""` empty-string label that pollutes every "top-N models" Grafana breakdown. The `metric_shipped 2 days ago` window means there are no external scrapers depending on the current 4-bucket `status_class` taxonomy — this is the last clean moment to supersede without a compat shim.

## Goal

After this work an operator can:

- Chart `sum by (provider, direction) (rate(ccrouter_tokens_total[5m]))` in Grafana and see input vs output tokens per provider.
- Chart `sum by (provider, status_class) (rate(ccrouter_requests_total{status_class=~"4xx_.*|5xx_.*"}[5m]))` and distinguish `4xx_rate_limited` (429 quota) from `4xx_auth` (401/403) from `5xx_upstream` (upstream fault) from `5xx_router` (router-side bug or body-too-large rejection).
- Alert on `status_class="4xx_rate_limited"` specifically instead of any 4xx.
- Trust that every "top-N models" panel breaks down real model names — no `model=""` empty series.

The `[req]` log line, `ExtractUsage` behavior, alias-resolutions counter, latency histogram, and existing operator toggles (`/enabletrace`, `/setloglevel`, SIGHUP hot reload) are unchanged.

## Non-goals

- Do NOT modify `ExtractUsage`, the tee buffer, or the `usageRecorder`/`statusRecorder` `Unwrap()` chain — landed as v0.17.0–v0.17.2 and is load-bearing for SSE flushing.
- Do NOT change the `[req]` log line format — `in=N out=M` already ships and downstream operator eyeballs depend on it.
- Do NOT add a config knob to disable token counting, error-class splitting, or the `_unknown_` sentinel — token counting is not opt-in; if a future consumer demands a variation, that is a separate spec.
- Do NOT add a per-status-code label (e.g. `status="429"`) — the bounded 7-value enum is the cardinality contract.
- Do NOT introduce a backward-compat shim that emits both `status_class="4xx"` and `status_class="4xx_rate_limited"` — this is a clean supersede on a 2-day-old metric with no external scrapers.
- Do NOT record tokens on non-200 responses — a failed upstream call does not carry a trustworthy usage object.
- Do NOT expose `_unknown_` as a general fallback for other labels (provider, status_class) — it is a model-label-only sentinel.
- Do NOT add auth, retention, or rotation to `/metrics` — operator-local trust model, same as v0.14.0.

## Desired Behavior

1. `Metrics` exposes a new `ccrouter_tokens_total` counter labeled `provider`, `model`, `direction` with `direction ∈ {input, output}`; the direction label is bounded (unknown direction values are dropped, not counted).
2. `ObserveRequest` accepts an explicit `isRouterError bool` argument at the call site so the taxonomy can be driven without inspecting `http.Transport` error chains; the 7-value enum resolves:
   - 2xx → `2xx`
   - 3xx → `3xx`
   - 401, 403 → `4xx_auth`
   - 429 → `4xx_rate_limited`
   - other 4xx → `4xx_bad_request`
   - 5xx with `isRouterError=false` → `5xx_upstream`
   - 5xx with `isRouterError=true` → `5xx_router`
   - anything else → raw status code string.
3. After a successful upstream call (status 200) the router parses the tee tail via the existing `ExtractUsage`, converts input and output token counts from string to int, drops the `"-"` sentinel and any zero-or-negative value, and increments the tokens counter twice (once for `direction=input`, once for `direction=output`).
4. Non-200 responses do not increment the tokens counter — token counting is a strict success-path observation.
5. The model label passed to both counters is resolved in this order: post-alias resolved model → pre-alias `origModel` → `UnknownModelLabel` sentinel (`_unknown_`). No `model=""` empty label ever reaches Prometheus.
6. The three router-side early-return paths in `NewModelRouter` (body-too-large 413, body-read-failed 400, alias-rewrite-failed 500) each call `ObserveRequest(provider, model, status, latency, isRouterError=true)` before returning — where `provider` is `"_unknown_"` if unresolved and `model` follows the same sentinel chain. These paths were previously invisible in metrics.
7. `docs/metrics.md` documents the 7-value `status_class` enum, the new `ccrouter_tokens_total` row, the updated ~525-series cardinality note, the "tokens/s by provider and direction" Grafana query, the error-class-breakdown query, and the updated `AnthropicQuotaNearExhaustion` alert filtering specifically on `status_class="4xx_rate_limited"`.
8. `CHANGELOG.md` gains an `## Unreleased` (or new version) entry describing the new counter, the taxonomy expansion, and the empty-label fix.

## Constraints

- Frozen file/seam: `pkg/handler/metrics.go` (`Metrics` struct, `NewMetrics`, `Register`, `ObserveRequest`, `statusClass`) is the single wiring seam for the counter + taxonomy change; `pkg/handler/model-router.go` around line 160 is the single call site for `ObserveRequest`.
- Frozen behavior: `ExtractUsage(tail, contentType, contentEncoding)` in `pkg/handler/usage-recorder.go` — do NOT modify. Consume its `TokenUsage.Input` / `TokenUsage.Output` string result as-is.
- Frozen behavior: bounded 2 MiB tail buffer + `usageRecorder`/`statusRecorder` `Unwrap()` chain — do NOT touch.
- Frozen behavior: `[req] in=N out=M` log line format in `pkg/handler/model-router.go` — must stay byte-identical.
- Frozen behavior: `AliasResolutions` counter + pre-init pattern from `NewMetrics(aliases)` — untouched.
- Frozen behavior: `RequestDuration` HistogramVec + `LatencyBucketsSeconds` — untouched.
- Frozen listener: `0.0.0.0:8788` with `/metrics` mounted via `promhttp.Handler()` against `prometheus.DefaultRegisterer` — no auth added.
- Frozen sentinel: `UnknownModelLabel = "_unknown_"` — the leading/trailing underscore avoids collision with real model names (Anthropic + OpenRouter model names never start with `_`).
- Cardinality budget: total request-counter series ≤ 5 providers × 15 models × 7 status classes = 525 (ceiling; ~450 in practice because some (provider, model, status_class) tuples never fire); tokens-counter series ≤ 5 × 15 × 2 = 150; combined counter series ≤ 675; the ~1.5k series budget noted in prior metrics work is preserved.
- Clean supersede: replacing `status_class="4xx"`/`"5xx"` values with the split emits no compat shim. Metric shipped 2 days ago via PR #10; no external scrapers exist. Any dashboard or alert built against the raw 4-bucket enum will break on merge and is expected to be updated.
- Ginkgo v2 + Gomega, bborbe/errors idioms, no bare `return err`, no `fmt.Printf`, `glog.V(n)` gating, `make precommit` gates all merges — per `docs/dod.md`.
- Related past specs: 001-add-model-aliases, 002-sighup-hot-reload, 002-trace-logging, 003-enabletrace-endpoint (all completed or in-progress); this spec builds on the metrics baseline shipped alongside those.
- Vault task: `[[Add Token Counters and Error-Class Labels to Claude Code Router Metrics]]` — carries operator success criteria and audit trail.
- Vault runbook: `[[Update Claude Code Router Config]]` — launchd reload mechanics for the post-merge verify step.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---------|-------------------|----------|
| `ExtractUsage` returns `"-"` sentinel or empty string for a direction | That direction is not counted; the other direction (if present and positive) is counted independently | Automatic — sentinel-drop rule holds per direction |
| `ExtractUsage` returns a non-numeric string (upstream schema drift) | Parse fails; that direction is dropped, counter untouched; `glog.V(2)` diagnostic emitted; request path continues unaffected | Automatic — token counting is best-effort observability, not on the request-serving critical path |
| Response body has `"usage": {"input_tokens": 0, "output_tokens": 0}` | Both directions dropped (zero is not a data point); no counter increment | Automatic — by-design zero-drop rule |
| Response body has negative token counts (schema violation) | Both directions dropped; no counter increment; `glog.V(2)` diagnostic | Automatic |
| Body-too-large 413 early-return (router-side) | `ObserveRequest(provider, model, 413, latency, isRouterError=true)` → `status_class="4xx_bad_request"`; tokens counter not touched | Chartable in Grafana under `4xx_bad_request`; operator raises body-size limit or investigates client |
| Body-read-failed 400 early-return (router-side) | `ObserveRequest(..., 400, ..., isRouterError=true)` → `status_class="4xx_bad_request"`; tokens counter not touched | Chartable in Grafana; operator inspects client |
| Alias-rewrite-failed 500 early-return (router-side) | `ObserveRequest(..., 500, ..., isRouterError=true)` → `status_class="5xx_router"`; tokens counter not touched | Chartable in Grafana; operator inspects alias config |
| Request arrives with no `model` field, no alias resolution possible | Both counters emit label `model="_unknown_"`; no `model=""` empty label reaches Prometheus | Automatic — sentinel resolves at call site |
| Upstream returns 200 with SSE that never carries a final `usage` block (protocol drift) | `ExtractUsage` returns sentinel/empty; no counter increment on either direction; request-counter still increments on `2xx` | Automatic — token counting degrades to zero data, request accounting unaffected |
| Concurrent successful requests to the same (provider, model) | Prometheus CounterVec is thread-safe; each `Inc()`/`Add()` is atomic; totals aggregate correctly | Automatic — Prometheus client library invariant |
| Prometheus registration collision at boot (double-Register) | `Register` returns the first error; `factory.go` startup path decides whether to abort — unchanged from today | Same as today — operator restarts with a clean registry |
| Gzip-decompression failure inside `ExtractUsage` (schema drift or corruption) | `ExtractUsage` returns sentinel; no counter increment; request-counter still records `2xx` | Automatic — extractor already handles this per v0.17.2 |
| Old Grafana dashboard/alert built against `status_class="4xx"` | Series stops emitting on merge; queries return empty; operator updates queries to the 7-value enum | Manual — expected consequence of clean supersede; documented in CHANGELOG |

## Security / Abuse Cases

- Attacker-controlled input: the response body from upstream drives token counts. A malicious upstream (or MITM) could emit forged `usage` objects with arbitrary integers. Impact is bounded — inflated Prometheus counters, no code execution, no exfiltration path. Mitigation: parse via the existing `ExtractUsage` (already hardened for gzip / SSE / JSON, landed v0.17.0–v0.17.2), drop negative values, drop non-numeric strings.
- Attacker-controlled input: the request-body `model` field is echoed into a Prometheus label. Cardinality attack surface: a client sending random `model` strings could balloon series count. Mitigation is out of scope for this spec (the same attack works against the current metric); the `_unknown_` sentinel only handles the missing-field case, not the arbitrary-string case. This matches today's behavior and is acceptable for an operator-local trust model.
- Trust boundary: `/metrics` is bound to `0.0.0.0:8788` (operator-local host). Same trust model as v0.14.0. No auth added.
- What can hang: no goroutine created by this change; all work is inline on the request path after `target.ServeHTTP`. Token-parse work is bounded by the 2 MiB tail buffer already in place.
- Data crossing trust boundaries: none. Token counts are integers; no secrets, no bodies, no headers reach Prometheus.

## Acceptance Criteria

- [ ] `make precommit` exits 0 in the repo root — evidence: exit code 0
- [ ] `/metrics` exposition includes at least one line matching `^ccrouter_tokens_total\{provider="[^"]+",model="[^"]+",direction="input"\} [1-9][0-9]*$` after one successful `/v1/messages` round-trip where the upstream returned a positive `input_tokens` value — evidence: `curl -s http://127.0.0.1:8788/metrics | grep ccrouter_tokens_total` shows ≥1 matching line
- [ ] `/metrics` exposition includes at least one line matching `^ccrouter_tokens_total\{...direction="output"\} [1-9][0-9]*$` after the same round-trip — evidence: same `grep` shows ≥1 output-direction line
- [ ] `/metrics` exposition contains NO line matching `model=""` after any round-trip (including a request whose body omits the `model` field) — evidence: `curl -s http://127.0.0.1:8788/metrics | grep 'model=""'` returns zero lines
- [ ] `/metrics` exposition contains a `ccrouter_requests_total{...,status_class="4xx_rate_limited"}` line after a synthetic 429 is observed — evidence: unit test on `statusClass(429)` returns `"4xx_rate_limited"` AND integration test via `httptest` returns a 429 and asserts the label value on the counter
- [ ] `/metrics` exposition contains a `ccrouter_requests_total{...,status_class="4xx_auth"}` line after a 401 or 403 is observed — evidence: unit test on `statusClass(401)` and `statusClass(403)` both return `"4xx_auth"`
- [ ] `/metrics` exposition contains a `ccrouter_requests_total{...,status_class="5xx_upstream"}` line after a 502 with `isRouterError=false` is observed — evidence: unit test on `ObserveRequest(..., 502, ..., false)` increments the `5xx_upstream` series
- [ ] `/metrics` exposition contains a `ccrouter_requests_total{...,status_class="5xx_router"}` line after a 500 with `isRouterError=true` is observed — evidence: unit test on `ObserveRequest(..., 500, ..., true)` increments the `5xx_router` series
- [ ] `/metrics` exposition contains NO `status_class="4xx"` bare-4xx value and NO `status_class="5xx"` bare-5xx value (clean supersede) — evidence: `curl -s http://127.0.0.1:8788/metrics | grep -E 'status_class="(4xx|5xx)"'` returns zero lines
- [ ] `ObserveTokens` (or equivalent method) drops zero-count directions — evidence: unit test calls the method with input=0 and asserts the `input` series was NOT created (`CollectAndCount` returns 0 for that label combination)
- [ ] `ObserveTokens` drops negative-count directions — evidence: unit test calls with input=-1 and asserts no `input` series created
- [ ] `ObserveTokens` drops unknown direction values — evidence: unit test calls with `direction="sideways"` and asserts no series created for that direction
- [ ] The three router-side early-return paths in `NewModelRouter` each call `ObserveRequest(..., isRouterError=true)` — evidence: `grep -n 'ObserveRequest.*true' pkg/handler/model-router.go` returns ≥3 lines; Ginkgo integration test triggers each early-return path and asserts the counter incremented on the expected `4xx_bad_request` / `5xx_router` label
- [ ] Model label resolution follows the sentinel chain (resolved → orig → `_unknown_`) — evidence: unit test asserts (a) resolved model wins when present, (b) `origModel` used when resolved is empty, (c) `_unknown_` used when both are empty
- [ ] All 8 existing `ObserveRequest(...)` test call sites in `pkg/handler/metrics_test.go` are updated to the 5-arg signature — evidence: `grep -c 'ObserveRequest(' pkg/handler/metrics_test.go` returns ≥8 AND no line matches the old 4-arg pattern
- [ ] `docs/metrics.md` documents the 7-value `status_class` enum — evidence: `grep -c '4xx_rate_limited\|4xx_auth\|4xx_bad_request\|5xx_upstream\|5xx_router' docs/metrics.md` returns ≥5
- [ ] `docs/metrics.md` documents the `ccrouter_tokens_total` metric in the Series table — evidence: `grep -n 'ccrouter_tokens_total' docs/metrics.md` returns ≥1 line inside the Series table
- [ ] `docs/metrics.md` cardinality note updated from 225 → 525 counter-series ceiling (~450 practical) — evidence: `grep -nE '525|~450' docs/metrics.md` returns ≥1 line in the cardinality paragraph
- [ ] `docs/metrics.md` contains a "Tokens/s by provider and direction" Grafana query — evidence: `grep -n 'sum by (provider, direction)' docs/metrics.md` returns ≥1 line
- [ ] `docs/metrics.md` `AnthropicQuotaNearExhaustion` alert filters `status_class="4xx_rate_limited"` — evidence: `grep -n '4xx_rate_limited' docs/metrics.md` returns ≥1 line inside the alerting example
- [ ] `CHANGELOG.md` gains an entry describing the new counter, taxonomy expansion, and empty-label fix — evidence: `grep -ni 'ccrouter_tokens_total\|4xx_rate_limited\|status_class' CHANGELOG.md` returns ≥1 line above the most recent released version

No new scenario file. Unit and Ginkgo integration tests reach every AC — no real Docker, no real `gh`, no cluster involved. Adding a scenario for a metrics endpoint would be an E2E test on the top of the pyramid with zero incremental risk-coverage over the integration tests above.

## Verification

```
make precommit
```

Live smoke (operator machine, router running under launchd on `127.0.0.1:8788`, after `make install` + `launchctl kickstart -k gui/$(id -u)/de.bborbe.claude-code-router`):

```
# baseline scrape
curl -s http://127.0.0.1:8788/metrics | grep -E '^ccrouter_' | wc -l   # expect ≥3 metric families

# trigger a real /v1/messages round-trip (any Claude Code session hitting the router)
# ...

# tokens counter shows non-zero for input and output
curl -s http://127.0.0.1:8788/metrics | grep 'ccrouter_tokens_total{'
# expect ≥1 line with direction="input" and ≥1 line with direction="output", both > 0

# no empty model label
curl -s http://127.0.0.1:8788/metrics | grep 'model=""' | wc -l   # expect 0

# no bare 4xx / 5xx label values
curl -s http://127.0.0.1:8788/metrics | grep -E 'status_class="(4xx|5xx)"' | wc -l   # expect 0

# 7-value enum visible when errors have occurred
curl -s http://127.0.0.1:8788/metrics | grep -oE 'status_class="[^"]+"' | sort -u
# expect subset of: 2xx, 3xx, 4xx_auth, 4xx_bad_request, 4xx_rate_limited, 5xx_router, 5xx_upstream
```

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | `Metrics` struct: add `TokensTotal` CounterVec, add `ObserveTokens(provider, model, direction, count int)` with zero/negative/unknown-direction drop, expand `statusClass` to 7-value enum, add `isRouterError bool` argument to `ObserveRequest`, update all 8 existing test call sites, `_unknown_` sentinel constant | 1, 2 | 5, 6, 7, 8, 9, 10, 11, 12, 15 | — |
| 2 | `model-router.go` wiring: parse tokens from `ur.Tail()` via `ExtractUsage` on `status == 200`, call `ObserveTokens` for each positive direction, resolve model label via sentinel chain, route the three early-return paths through `ObserveRequest(..., isRouterError=true)` with the sentinel provider/model | 3, 4, 5, 6 | 2, 3, 4, 13, 14 | prompt 1 |
| 3 | Docs + changelog: `docs/metrics.md` (series table row, cardinality note 225→450, tokens/s Grafana query, error-class-breakdown query, updated `AnthropicQuotaNearExhaustion` alert), `CHANGELOG.md` `## Unreleased` entry | 7, 8 | 16, 17, 18, 19, 20, 21 | prompts 1, 2 |

Rationale: prompt 1 lands the collector shape and taxonomy in isolation (pure unit tests, no HTTP), prompt 2 wires the HTTP call site consuming the already-landed `ExtractUsage` tee, prompt 3 is doc-only and depends on final label names. Splitting collector-from-wiring avoids the prompt-creator holding the CounterVec label contract + `ExtractUsage` return-shape + `NewModelRouter` early-return graph in memory at once.

## Do-Nothing Option

If we do nothing, the `ExtractUsage` tee stays observable only through the `[req]` log line — operators grep logs for token throughput, cannot chart per-provider or per-model breakdowns in Grafana, cannot alert on cumulative token spend. The `status_class` 4-bucket enum stays coarse — 429 rate-limit alerts have to be built against raw log lines, and the three router-side early-return paths stay invisible in metrics (`4xx`/`5xx` never emitted from those paths, so an operator staring at Grafana sees a healthy service while requests are being rejected at the door). The `model=""` empty-label bug stays — every "top-N models" panel has an empty-string row that is really the sum of all no-model-field probe traffic, misleading the operator. All three problems are workable via log-grep, but the point of the `/metrics` endpoint is that it is the machine-consumable observability contract; leaving it half-populated defeats its purpose.

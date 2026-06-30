---
status: verifying
approved: "2026-06-30T19:52:41Z"
generating: "2026-06-30T19:59:18Z"
prompted: "2026-06-30T19:59:18Z"
verifying: "2026-06-30T22:42:42Z"
branch: dark-factory/log-input-output-tokens
---

## Summary

- The `[req]` access log line emitted per request by the claude-code-router currently reports method, path, model, provider, status, and latency, but discards the upstream token usage that is already present in the response body.
- Append `in=<N> out=<N>` (input/output token counts) to the existing `[req]` line for successful responses, so the operator can see per-request token consumption in `/tmp/claude-code-router.log` without a separate metrics backend.
- Capture usage via a bounded tail buffer (≤ 64 KB) teed off the response writer — sufficient because the terminal `message_delta` SSE event carrying usage is always the last chunk of an Anthropic stream, and non-streaming JSON bodies fit the same tail.
- Error/cancelled paths (non-200, no parseable usage) emit `in=- out=-` rather than panicking or logging fabricated zeros.
- No new YAML fields, no migration to structured/JSON logging, no cost-in-dollars computation — the existing `[req]` key=value style is preserved.

## Problem

The operator has no per-request visibility into token consumption. The upstream provider response body already contains `usage.input_tokens` / `usage.output_tokens` (non-streaming: top-level `usage`; streaming SSE: the terminal `message_delta` event's `usage` field), but the response writer wrapper (`statusRecorder`) only captures the HTTP status code and never tees the body, so the data is discarded before the log line is emitted. Without token counts in the log, the operator cannot answer "how many tokens did this `/model X` turn cost me?" without re-issuing the request against a metering endpoint. The counts are right there in the response; this spec wires them into the line that already fires per request.

## Goal

After this work, every `[req]` log line at `V(1)` for a 200 response through the router includes the input and output token counts as observed in the upstream response body, and every error/cancelled path emits a well-formed `[req]` line with `in=- out=-` (or cleanly omits the counts) instead of panicking. The SSE streaming flush path is unchanged — the client still receives chunks incrementally with no added buffering latency.

## Non-goals

- **Aggregating or persisting token totals to a metrics backend** (Prometheus, etc.) — log line only. A future spec can add a `tokens_observed` metric; this one emits the counts to the log.
- **Cost-in-dollars computation** in the log line — token counts only, no price table.
- **Capturing the full response body** — bounded ≤ 64 KB tail buffer only; the buffer holds the last chunk(s), never the whole body.
- **Changes to provider selection, routing, auth, or alias logic** — this spec touches only the response-capture and log-emission tail of the request flow.
- **Structured/JSON log format migration** — keep the existing `[req]` key=value style; `in=` / `out=` are appended in the same format.
- **New YAML config fields** — no tunable buffer size, no opt-out flag. The buffer bound is a frozen constant (see Constraints). Do NOT add a `log_tokens` toggle — the invariant is that token counts always appear on the `[req]` line; an escape hatch on the Goal is itself a regression. If a future consumer demands variation, that is a separate spec.
- **Streaming-protocol support beyond Anthropic SSE** — the extractor handles the `message_delta` event shape and non-streaming JSON `usage`. Other streaming protocols (OpenAI-style `data: [DONE]` without a terminal usage event) are out of scope; they emit `in=- out=-` unless a top-level `usage` is present.

## Acceptance Criteria

- [ ] **`make precommit` exits 0 at the repo root.** Evidence: exit code 0 from `make precommit` run in `/Users/bborbe/Documents/workspaces/claude-code-router-log-tokens`.

- [ ] **SSE 200 response logs token counts matching upstream usage.** Evidence: a Ginkgo spec in `pkg/handler/usage-recorder_test.go` drives an `httptest` upstream that emits a multi-event SSE stream whose terminal `message_delta` event carries `{"usage":{"input_tokens":42,"output_tokens":17}}`; after the request completes, the captured `[req]` log output (glog captured via a test `logBridge` or buffer) contains the substring `in=42 out=17`. The logged counts equal the upstream-provided counts (asserted by parsing the captured log line, not by matching a hardcoded literal — the test must vary the upstream numbers across cases to defeat a hardcoded fake).

- [ ] **Non-streaming 200 response logs token counts matching the JSON body.** Evidence: a Ginkgo spec drives an `httptest` upstream returning `Content-Type: application/json` with body `{"id":"...","usage":{"input_tokens":100,"output_tokens":5}}`; the captured `[req]` log contains `in=100 out=5`. As above, the upstream numbers are varied across test cases.

- [ ] **Error/cancelled path emits `in=- out=-` without panic.** Evidence: a Ginkgo spec drives an upstream that returns 502 (context canceled) with a body containing no `usage`; the handler does not panic (test completes), and the captured `[req]` log line contains `status=502` and the substring `in=- out=-`.

- [ ] **Truncated buffer (response larger than 64 KB) still extracts usage when the terminal event is captured.** Evidence: a Ginkgo spec drives an SSE upstream that emits > 64 KB of preceding `content_block_delta` events followed by a terminal `message_delta` with usage; the tail buffer retains the terminal event and the logged line contains the correct `in=<N> out=<N>`. (If the terminal event itself is evicted by overflow, `in=- out=-` is logged — this is the bounded-buffer tradeoff, not a failure.)

- [ ] **Response with no parseable usage logs `in=- out=-`.** Evidence: a Ginkgo spec drives a 200 upstream returning a JSON body without a `usage` field (e.g. `{"ok":true}`); the captured `[req]` log contains `in=- out=-`.

- [ ] **`Unwrap()` chain remains intact so SSE flushes incrementally.** Evidence: a Ginkgo spec asserts that `http.NewResponseController(teeWriter).Flush()` returns no error and that flushing is observed on the underlying writer (the underlying writer is a recording `httptest.ResponseRecorder` or a custom writer whose `Flush` is observable). Additionally, `http.NewResponseController(teeWriter).Hijack()` resolves through the chain when the underlying writer implements `Hijacker`. This is the regression guard for the load-bearing `Unwrap()` documented at `status-recorder.go:45-51`.

- [ ] **`[req]` line format is unchanged except for the appended `in=/out=` fields.** Evidence: a Ginkgo spec asserts the captured `[req]` line still matches the existing field order (`[req] <method> <path> model=... provider=... status=... latency=...`) with `in=<N> out=<N>` appended at the end (for 200) or `in=- out=-` appended (for error paths). The alias-variant line (`alias=...`) likewise appends `in=/out=` at the end, with upstream numbers varied across both alias and non-alias cases to defeat a hardcoded append.

- [ ] **Post-install live smoke test.** Evidence: after merge + `go install github.com/bborbe/claude-code-router@latest`, one real request through the router (SSE default path) to a configured provider produces a `[req]` line in `/tmp/claude-code-router.log` containing `in=<N> out=<N>` where both `<N>` are integers ≥ 1 and match the `usage` field in the provider's response (verified by diffing against a `curl` of the same prompt). Manual grep, not an automated scenario.

## Verification

```bash
# Lint + tests + build gate
make precommit
# expected: exit code 0

# Ginkgo suite for the new capture/extract logic
go test ./pkg/handler/ -run UsageRecorder -ginkgo.v
# expected: all specs pass, exit code 0
```

## Desired Behavior

1. A tee writer wraps `statusRecorder` so that every byte written to the response is also copied into a bounded tail buffer (≤ 64 KB, retaining the last bytes written). The tee writer implements `Unwrap()` returning the wrapped `statusRecorder`, preserving the existing `http.NewResponseController` flush/hijack chain.

2. After the upstream handler returns, the router inspects the tail buffer: for SSE responses (`Content-Type: text/event-stream` or content matching `event: message_delta`), it scans for the terminal `message_delta` event and parses its `usage.input_tokens` / `usage.output_tokens`; for JSON responses, it parses the top-level `usage` object. Extraction is best-effort — parse failure or absence yields sentinel `-` values, never an error that aborts the log line.

3. The `[req]` log line (both the alias and non-alias variants) appends `in=<N> out=<N>` for 200 responses where usage was extracted, and `in=- out=-` for 200 responses with no parseable usage and for all non-200 responses. The append preserves the existing field order and key=value style.

4. On error/cancelled paths (non-200 status, empty body, context cancellation mid-stream), the router still emits the `[req]` line with `in=- out=-` and does not panic. A nil or malformed tail buffer is treated as "no usage."

5. The tail buffer never grows beyond the fixed 64 KB bound, regardless of response size — a 5 MB streaming response occupies ≤ 64 KB of additional memory for the buffer, and older bytes are evicted as new bytes arrive (ring/tail semantics).

6. SSE streaming to the client is not buffered or delayed — the tee writer writes through to the underlying `statusRecorder` on every `Write` call and copies to the buffer as a side effect; the client receives chunks at the same cadence as before. `http.NewResponseController(teeWriter).Flush()` reaches the underlying `Flusher` via the `Unwrap()` chain.

7. The `[req]` log line remains at glog verbosity `V(1)` — no change to the sampling logic (200s still sampled, non-200s always logged). The appended `in=/out=` fields inherit the same gating as the rest of the line.

## Constraints

- **`Unwrap()` chain must stay functional.** `statusRecorder.Unwrap()` (documented at `status-recorder.go:45-51`) is load-bearing for SSE flush via `http.NewResponseController`. The new tee writer wrapping it must also implement `Unwrap()` returning the `statusRecorder` (or equivalent chain) so the controller still reaches `Flusher`/`Hijacker`. This is the single highest-priority regression guard.
- **Bounded memory: the tail buffer is a fixed-size constant (≤ 64 KB).** No unbounded growth on large responses. The bound is a frozen constant in the handler package, not a config field.
- **No latency regression.** Extraction is a single scan over ≤ 64 KB at request end, performed after the upstream handler returns and before the `[req]` line is emitted — negligible against 2-30s upstream latency. No per-byte parsing during streaming.
- **No new YAML fields.** `docs/config.md` and `docs/config.example.yaml` are unchanged (DoD: docs updated only if YAML fields change).
- **Existing `[req]` field order and key=value style preserved.** `in=/out=` are appended at the end; no restructuring of the existing fields.
- **DoD compliance** per `docs/dod.md`: GoDoc comments on exported types/functions, `bborbe/errors` conventions for any error wrapping, `glog.V(n).Infof` (the `[req]` line stays at `V(1)`, no bare `glog.Info`), Ginkgo/Gomega tests in `pkg/handler/` next to the code, `pkg/handler/` layout, `CHANGELOG.md` `## Unreleased` entry, no `replace`/`exclude` in `go.mod`.
- **Logging conventions** per the dark-factory `logging-conventions` rule and the coding `go-glog-guide` rule: debug-shaped emissions gated behind `V(N)`; the `[req]` line at `V(1)` is operator-opt-in debug, already correct. No `fmt.Printf`/`println`.
- **Sampler logic unchanged.** The 200-sampling gate (`status == http.StatusOK && !sampler.IsSample()` → return) is not bypassed or modified; token fields ride on the same line with the same gating.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---------|-------------------|----------|
| Upstream returns non-200 (404/502/etc.) with no usage in body | `[req]` line still emits with `in=- out=-`; no panic. | Operator investigates upstream; router needs no restart. |
| Context cancelled mid-SSE stream (client disconnect) | Upstream handler returns; tail buffer holds whatever was flushed. No `message_delta` in buffer → `in=- out=-`. `[req]` line emits; no panic. | None — next request unaffected. |
| Tail buffer overflows (terminal `message_delta` evicted before extraction) | `in=- out=-` logged. Bounded-buffer tradeoff, not a crash. In SSE the terminal event is last, so eviction only happens if the response is truncated mid-stream before the terminal event arrives. | None by design. |
| Upstream returns malformed JSON / malformed SSE in the tail | Parse fails silently; `in=- out=-` logged. No panic, no error return that aborts the log line. | None — correct upstream responses parse fine. |
| Upstream returns 200 but `usage` field missing or zero | `in=- out=-` logged (or `in=0 out=0` only if the upstream literally sent zeros — the extractor reports what it parsed). | Operator checks upstream response shape. |
| Race: concurrent requests each allocate their own tail buffer | Per-request buffer is local to the handler invocation; no shared mutable state. No contention. | N/A — no cross-request state. |
| `Unwrap()` chain broken (regression) | `http.NewResponseController(w).Flush()` fails; SSE chunks buffer client-side; Claude Code spinners appear stuck. Caught by the `Unwrap`-chain AC test. | Revert; the chain is a frozen constraint. |

## Security / Abuse Cases

- **Attacker-controlled content crosses the trust boundary into the tail buffer:** the response body is LLM-generated content from an upstream provider. The tail buffer holds ≤ 64 KB of it in memory for the duration of the request, then is discarded. The buffer is never logged (only the extracted integer counts `in=<N> out=<N>` reach the log), never persisted, and never returned to any client. No prompt-injection vector is introduced — the extractor parses for a numeric `usage` field and ignores all other content.
- **Memory exhaustion via large responses:** bounded by the fixed 64 KB tail buffer. A malicious or runaway upstream emitting a 500 MB response cannot grow the buffer beyond the bound. This is an improvement over an unbounded `io.ReadAll` tee.
- **No new input validation surface:** the router already validates inbound request bodies (`MaxRequestBodyBytes`); the tee adds no inbound parsing. Outbound parsing is best-effort and failure-safe.
- **No credential exposure:** the tail buffer may contain response body content, but credentials live in request headers (not response bodies) and are not captured. The `[req]` line contains only counts, not body content.

## Suggested Decomposition

This spec touches three code layers (response-writer wrapper, usage extractor, log-line wiring) plus tests. Suggested prompt split:

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Tee response writer with bounded tail buffer + `Unwrap()` chain + Ginkgo tests for buffer semantics and `Unwrap`/Flush chain | 1, 5, 6 | AC 6, AC 7 | — |
| 2 | Usage extractor (SSE `message_delta` scan + non-stream JSON `usage` parse) + Ginkgo tests for SSE, non-stream, truncated, no-usage, multi-event cases | 2, 4 | AC 2, AC 3, AC 4, AC 5, AC 6 | prompt 1 (needs the buffer type) |
| 3 | Wire extractor into `[req]` log line (both alias + non-alias variants) with `in=/out=` append + sampler/`V(1)` gating preserved + CHANGELOG + format-preservation test + live smoke test instructions | 3, 7 | AC 1, AC 8, AC 9 | prompt 2 (needs the extractor) |

Rationale: prompt 1 establishes the bounded-buffer primitive and the load-bearing `Unwrap()` chain in isolation — the highest regression risk and the dependency for everything else. Prompt 2 builds the pure-function extractor against the buffer type from prompt 1, fully unit-testable without HTTP. Prompt 3 is the thin wiring into the existing log emission site and the format/sampler-regression tests. This ordering means the regression-critical `Unwrap()` chain is proven before any extractor logic is written, and the extractor is proven before the log line is touched.

## Do-Nothing Option

If we do nothing, the operator continues to have no per-request token visibility in the log. Token accounting requires either querying the upstream provider's metering API separately or instrumenting a Prometheus backend (explicitly out of scope here). The current `[req]` line is correct for latency/status debugging but silent on cost. The do-nothing cost is operational blindness to per-request token consumption — acceptable but not ideal, and the counts are already in the response body, so the work is capture-only, not new data generation.

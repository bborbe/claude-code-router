---
status: verifying
approved: "2026-07-01T06:46:59Z"
generating: "2026-07-01T06:54:57Z"
prompted: "2026-07-01T06:54:57Z"
verifying: "2026-07-01T07:08:23Z"
branch: dark-factory/bug-anthropic-tokens-not-extracted
---

## Summary

- Token-usage extraction added in v0.17.0 works for `minimax` (JSON responses, 452/457 → 99% success) but fails 100% on `anthropic-subscription` (0/19 200s extracted; every line logs `in=- out=-`).
- Live log from the running v0.17.0 binary (PID 78843) shows the split: minimax 200s consistently log `in=<N> out=<N>`; anthropic-subscription 200s consistently log `in=- out=-`.
- Root cause hypothesis: Anthropic's SSE stream splits `input_tokens` (in `message_start` event) from `output_tokens` (in `message_delta` event), and/or the `Content-Type` header sniffed via `rec.Header().Get("Content-Type")` after `target.ServeHTTP` returns is empty/wrong for reverse-proxied SSE responses — so the extractor falls to the JSON parser, which fails on multi-event SSE bytes, returning the `noUsage` sentinel.
- Fix scope covers BOTH hypothesis branches defensively: (a) detect SSE by content scan (`bytes.Contains(tail, "event: message_")`) in addition to `Content-Type`; (b) scan for BOTH `message_start` (for `input_tokens`) and terminal `message_delta` (for `output_tokens`), combining the two into `TokenUsage`; (c) add a unit test asserting that the tee actually receives the SSE bytes (drives an httptest reverse-proxy upstream through the real `usageRecorder` and inspects `Tail()` — proves the tee path is not bypassed for reverse-proxied SSE responses).

## Problem

v0.17.0 shipped token-count logging (`in=<N> out=<N>` on the `[req]` line) as spec 004. The feature was verified against `httptest` upstream fixtures with a single `event: message_delta` block carrying both `input_tokens` and `output_tokens` — a shape that matches `minimax` and passes the AC. Under production against `anthropic-subscription` (the real Anthropic API via subscription OAuth passthrough), the same fields are split across two SSE events, and the extractor's single-event scan misses the input tokens. Additionally, the extraction path may never engage at all — 100% failure rate for anthropic-subscription 200s suggests the SSE detection itself fails before parsing begins.

The operator has zero token visibility for anthropic-subscription requests (the highest-value provider for personal Claude Code usage), defeating the primary motivation of spec 004.

## Reproduction

**Environment:**
- Binary: `/Users/bborbe/Documents/workspaces/go/bin/claude-code-router` running v0.17.0 (PID 78843, launched 2026-06-30 23:18).
- Log: `/tmp/claude-code-router.log`.
- Config: `~/.claude-code-router/config.yaml` with `anthropic-subscription` provider routing Anthropic requests via OAuth passthrough (no `token:` field).

**Steps:**

1. Send a real Claude Code SSE request through the router to `anthropic-subscription` (e.g. use Claude Code with the router as its `ANTHROPIC_BASE_URL`).
2. Inspect the resulting `[req]` line in `/tmp/claude-code-router.log`.

**Observed (verbatim from live log):**

```
I0701 06:38:30.179119   78843 model-router.go:183] [req] POST /v1/messages model=claude-opus-4-7 provider=anthropic-subscription status=200 latency=5.564s in=- out=-
I0701 06:38:40.624638   78843 model-router.go:183] [req] POST /v1/messages model=claude-opus-4-7 provider=anthropic-subscription status=200 latency=1.592s in=- out=-
I0701 06:39:02.816489   78843 model-router.go:183] [req] POST /v1/messages model=claude-opus-4-7 provider=anthropic-subscription status=200 latency=11.592s in=- out=-
```

**Aggregate breakdown of `/tmp/claude-code-router.log`:**

| Provider | Status | `in=<N>` | `in=-` |
|---|---|---|---|
| minimax | 200 | 452 | 5 |
| anthropic-subscription | 200 | 0 | 19 |
| anthropic-subscription | 400/429/502 | 0 | 50 |

`minimax` 200 responses are extracted correctly (99% success). `anthropic-subscription` 200 responses fail 100%.

**dark-factory version:** N/A (this is a bug in `claude-code-router`, not `dark-factory` itself).
**claude-code-router version:** v0.17.0 (`814cf94`), the release that introduced the feature.

## Expected vs Actual

**Expected** (per spec 004 AC 1, cited: `specs/completed/004-log-input-output-tokens.md:33`):
> `[req]` log line includes `in=<N>` and `out=<N>` for every successful 200 response, both streaming (SSE) and non-streaming.

**Actual:** for `provider=anthropic-subscription status=200`, the `[req]` line always logs `in=- out=-` — the `noUsage` sentinel. 0 of 19 recorded 200 responses have extracted counts.

The spec's AC is not satisfied for the primary production provider.

## Why this is a bug

Spec 004 AC 1 says token counts appear for **every** successful 200 response on SSE. The verification (`/dark-factory:verify-spec 004-...`) walked the AC but exercised only the `minimax` path in the live smoke test — the acceptance criterion technically passed on that evidence, but did not exercise the `anthropic-subscription` path where Anthropic's SSE format differs. Real Anthropic SSE emits:

```
event: message_start
data: {"type":"message_start","message":{"id":"msg_...","usage":{"input_tokens":42,"output_tokens":1}}}

event: content_block_start
...
event: content_block_delta
...
event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":128}}

event: message_stop
```

`input_tokens` lives in `message_start`, `output_tokens` in `message_delta`. The current extractor (`pkg/handler/usage-recorder.go:243-301`) scans only `message_delta`, and even if the parse of `{"output_tokens":128}` succeeded, it would emit `in=0 out=128` — not `in=- out=-`. The observed 100% sentinel rate proves the SSE path either isn't detected (Content-Type sniffing fails → JSON fallback → parse failure) or the tail buffer never receives the SSE bytes (reverse-proxy write path bypasses the `usageRecorder.Write` tee).

## Acceptance Criteria

- [ ] **A live `anthropic-subscription` 200 response logs matching token counts.** Evidence: after installing the fix build, one real Claude Code request via the router produces a `[req]` line in `/tmp/claude-code-router.log` containing `in=<N> out=<N>` where both `<N>` are integers ≥ 1 and match the upstream response's `usage.input_tokens` + `usage.output_tokens` (verified by enabling `trace:` config for one request and comparing the trace file's `usage` to the log line).

- [ ] **Aggregate anthropic-subscription 200s in the log show ≥ 95% non-sentinel extraction.** Evidence: after the fix ships and the router runs for at least 10 real `anthropic-subscription` 200 requests, `grep 'provider=anthropic-subscription status=200' /tmp/claude-code-router.log | grep -c 'in=[0-9]'` divided by the total count of matching lines ≥ 0.95. Baseline before fix: 0/19 = 0%.

- [ ] **Ginkgo spec reproduces the pre-fix bug against a fixture matching real Anthropic SSE format.** Evidence: a new test case in `pkg/handler/usage-recorder_test.go` uses an httptest upstream that emits a multi-event SSE stream in Anthropic's actual format (`message_start` with `{"input_tokens":42,"output_tokens":1}` and terminal `message_delta` with `{"output_tokens":128}`) and asserts the extractor returns `TokenUsage{Input:"42", Output:"128"}`. Additional cases must use varied token numbers (e.g. `Input:"7", Output:"3"` and `Input:"1000", Output:"250"`) so a hardcoded constant fake fails at least one case. This test MUST fail on v0.17.0's code and pass on the fix. Run: `go test ./pkg/handler/ -run UsageRecorder -ginkgo.v` exits 0 on the fix build; exits non-0 on v0.17.0.

- [ ] **SSE detection is robust to missing/wrong Content-Type header.** Evidence: a new test case drives an SSE upstream that emits Anthropic-format events with an empty or wrong `Content-Type` (e.g. `""` or `"application/octet-stream"`). The extractor still detects SSE via content scan and extracts tokens correctly. This test MUST fail against v0.17.0's extractor and pass against the fix build.

- [ ] **minimax path continues to work.** Evidence: existing `minimax` JSON-shape tests in `usage-recorder_test.go` continue to pass with no regression. Aggregate log count for `provider=minimax status=200 in=[0-9]` after fix is ≥ pre-fix ratio (i.e. no drop below 452/457 ≈ 99%).

- [ ] **`make precommit` exits 0 in the repo root.**

- [ ] **`Unwrap()` chain remains intact.** Evidence: the existing SSE-flush regression specs in `model-router_test.go` and `usage-recorder_test.go` continue to pass. This bug fix touches extraction logic only, not the tee-writer / `Unwrap` chain.

## Verification

```bash
# Lint + tests + build gate
make precommit
# expected: exit code 0

# New anti-regression Ginkgo tests
go test ./pkg/handler/ -run UsageRecorder -ginkgo.v
# expected: all specs pass, including new Anthropic-format cases

# Post-install live smoke
go install github.com/bborbe/claude-code-router@latest
launchctl kickstart -k gui/$(id -u)/de.bborbe.claude-code-router
# Send at least 10 real Claude Code requests via the router to anthropic-subscription
# then:
grep 'provider=anthropic-subscription status=200' /tmp/claude-code-router.log | tail -20
# expected: ≥ 95% of the last 20 anthropic-subscription 200s show in=<N> out=<N> with N ≥ 1
```

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---------|-------------------|----------|
| `message_start` present but `message_delta` truncated (buffer overflow) | Log `in=<N> out=-` — input from `message_start` is real data and maximises information; output missing renders as the `-` sentinel. Do NOT fall back to `in=- out=-` when input is known. | None — bounded-buffer tradeoff |
| Neither `message_start` nor `message_delta` in tail (truncation past both) | Log `in=- out=-` | None — bounded-buffer tradeoff |
| `Content-Type` empty AND tail bytes are ambiguous (neither SSE markers nor valid JSON) | Log `in=- out=-`; no panic | None — best-effort by design |
| Anthropic changes SSE event shape (adds new fields to `usage`) | Extra fields ignored, `input_tokens`/`output_tokens` still parsed | None — forward-compatible |
| Non-Anthropic SSE stream that happens to contain `event: message_start` | Extractor may emit numeric counts based on that byte pattern. Detection risk is low: Anthropic's `event: message_start` marker is followed by `data: {"type":"message_start","message":{...`; a false positive requires the same marker + a JSON object with an `input_tokens` integer field. Detect via: `grep 'provider=<non-anthropic> .* in=[0-9]' /tmp/claude-code-router.log` — if a non-anthropic provider ever logs numeric `in=`, investigate that provider's response shape. | If confirmed, tighten the SSE marker check to require the full `event: message_start\ndata: {"type":"message_start"` sequence (a follow-up bug fix, not this spec). |

## Constraints

- No new YAML config fields.
- `Unwrap()` chain must stay functional (unchanged from v0.17.0).
- Bounded ≤ 64 KB tail buffer unchanged.
- `[req]` line format unchanged (still `... in=<N> out=<N>` appended).
- Existing `minimax` JSON-path extraction must not regress.
- Extraction remains best-effort (`defer recover()`) — no error path aborts the log line.
- `docs/config.md`, `docs/config.example.yaml` unchanged.

## Workaround

Until v0.17.1 lands, the operator has no per-request token visibility for anthropic-subscription requests. The upstream provider's dashboard (console.anthropic.com) shows aggregate usage but not per-request breakdown through the router.

## Suggested Decomposition

Single-prompt fix; the change is localized to `extractUsage` / `extractUsageSSE` in `pkg/handler/usage-recorder.go`, plus new tests in `usage-recorder_test.go`. Prompt scope:

1. Update `ExtractUsage` to detect SSE via `Content-Type` OR content scan (`bytes.Contains(tail, "event: message_")`).
2. Update `extractUsageSSE` to scan for `message_start` (for `input_tokens`) AND terminal `message_delta` (for `output_tokens`), combining them into `TokenUsage`.
3. Add Ginkgo test cases matching real Anthropic SSE format (separate events), plus edge cases (missing Content-Type, only `message_start`, only `message_delta`).
4. CHANGELOG `## Unreleased` entry: `fix: ...` (this is a bug fix, not a feature — use the `fix:` prefix).

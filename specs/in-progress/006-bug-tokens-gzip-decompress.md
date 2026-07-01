---
status: prompted
approved: "2026-07-01T09:33:40Z"
generating: "2026-07-01T09:40:53Z"
prompted: "2026-07-01T09:40:53Z"
branch: dark-factory/bug-tokens-gzip-decompress
---

## Summary

- v0.17.1 (spec 005) widened SSE detection and split-event scanning, but real-Anthropic 200 responses through `anthropic-subscription` still log `in=- out=-` (5/5 post-v0.17.1 install; 24/24 pre-fix). Post-install live smoke test failed.
- Live trace capture reveals the true root cause: Anthropic + Cloudflare send `Content-Encoding: gzip` on responses. The `usageRecorder` tee captures the compressed bytes; neither the JSON parser nor the SSE content-scan can find `usage`/`event: message_` strings in gzip garbage → `noUsage` → `in=- out=-`.
- Additionally, the bounded 64 KB tail is insufficient for real responses (traced non-streaming JSON responses are 500 KB+ gzipped) — the extractor never sees the start-of-stream needed to decompress.
- Fix: grow the buffer to a 2 MB cap that holds the full (compressed) body, detect `Content-Encoding`, decompress before extraction. Support `gzip` (Anthropic/Cloudflare default); `deflate` / `br` / `zstd` accepted by the client's `Accept-Encoding` may also appear — handle `gzip` first, others as follow-up if observed.
- Non-modification of the client's request is a fixed constraint (per operator preference: "router stays a router, no request modification"). We do NOT strip `Accept-Encoding`.

## Problem

The v0.17.0 → v0.17.1 arc addressed two hypothesized root causes (Content-Type sniffing fragility; Anthropic's split-event SSE). Both were real code issues, but neither was THE cause for anthropic-subscription's 100% failure rate. The real cause was invisible until a live `enabletrace` capture showed:

```
Content-Type: application/json
Content-Encoding: gzip
```

on a real Anthropic 200. The client sends `Accept-Encoding: gzip, deflate, br, zstd`; Anthropic honors gzip; the router's reverse-proxy passes both through; the tee captures compressed bytes. `usage-recorder.go`'s `ExtractUsage` scans for text markers (`event: message_`, top-level JSON `usage`), which don't exist in a compressed byte stream.

Non-streaming Anthropic 200s are additionally observed at ~500 KB gzipped — well beyond the 64 KB `TailBufferBytes` bound — so even if decompression was added, the tail alone would be mid-stream (gzip is not self-synchronizing; only decompressing from start-of-stream works). The buffer must grow to hold the full compressed body.

## Goal

`anthropic-subscription` 200 responses log `in=<N> out=<N>` on the `[req]` line when the upstream returns a `Content-Encoding: gzip` body up to 2 MiB. Existing minimax + uncompressed-SSE paths continue to work.

## Non-goals

- `deflate` / `br` / `zstd` support (out of scope; log `in=- out=-` and defer to follow-up bug if observed).
- Modifying the client's request (do NOT strip `Accept-Encoding`; router stays a router).
- New YAML config fields (the buffer size is a frozen constant).
- Chunked/streaming decompression across multiple frames (single-frame gzip only).

## Reproduction

**Environment:**
- Binary: `/Users/bborbe/Documents/workspaces/go/bin/claude-code-router` running v0.17.1 (PID 2765, installed 2026-07-01 10:39 via `make install` from master `f7ed420`).
- Log: `/tmp/claude-code-router.log`.
- Config: `~/.claude-code-router/config.yaml` with `anthropic-subscription` provider (Anthropic OAuth passthrough).
- Trace: `~/.claude-code-router/trace/` (enable via `curl -X POST http://127.0.0.1:8788/enabletrace`).

**Steps:**

1. Confirm v0.17.1 binary in use: `strings ~/Documents/workspaces/go/bin/claude-code-router | grep 'event: message_start'` returns matches (v0.17.0 does not contain this string).
2. Enable trace: `curl -X POST http://127.0.0.1:8788/enabletrace`.
3. Send one real Claude Code request through the router.
4. Inspect trace file: `latest=$(ls -t ~/.claude-code-router/trace/*.json | head -1); jq '.response.headers' "$latest"`.
5. Inspect log: `grep 'provider=anthropic-subscription status=200' /tmp/claude-code-router.log | tail -5`.

**Observed (verbatim):**

Trace response headers (2026-07-01 10:41):

```json
{
  "Content-Encoding": ["gzip"],
  "Content-Type": ["application/json"],
  "Cf-Ray": ["a1441d5d8aa73738-FRA"],
  "Server": ["cloudflare"]
}
```

Trace request headers:

```json
{
  "Accept-Encoding": "gzip, deflate, br, zstd"
}
```

Log lines from v0.17.1 binary (post-install):

```
I0701 08:39:59.408273    2765 model-router.go:183] [req] POST /v1/messages model=claude-opus-4-7 provider=anthropic-subscription status=400 latency=1.018s in=- out=-
I0701 08:40:03.429759    2765 model-router.go:183] [req] POST /v1/messages model=claude-opus-4-7 provider=anthropic-subscription status=200 latency=4.715s in=- out=-
I0701 08:40:23.712726    2765 model-router.go:183] [req] POST /v1/messages model=claude-opus-4-7 provider=anthropic-subscription status=200 latency=9.632s in=- out=-
I0701 08:40:34.374727    2765 model-router.go:183] [req] POST /v1/messages model=claude-opus-4-7 provider=anthropic-subscription status=200 latency=8.871s in=- out=-
I0701 08:40:47.417432    2765 model-router.go:183] [req] POST /v1/messages model=claude-opus-4-7 provider=anthropic-subscription status=200 latency=5.162s in=- out=-
```

5/5 anthropic-subscription 200s under v0.17.1 still log `in=- out=-`.

**claude-code-router version:** v0.17.1 (`39c6169`).

## Expected vs Actual

**Expected** (spec 004 AC 1, cited: `specs/completed/004-log-input-output-tokens.md:33`):
> `[req]` log line includes `in=<N>` and `out=<N>` for every successful 200 response, both streaming (SSE) and non-streaming.

**Expected** (spec 005 AC 2, cited: `specs/completed/005-bug-anthropic-tokens-not-extracted.md`):
> Aggregate anthropic-subscription 200s in the log show ≥ 95% non-sentinel extraction.

**Actual (v0.17.1):** 0/5 = 0% non-sentinel extraction for anthropic-subscription 200s. The prior spec's live smoke AC is not satisfied.

## Why this is a bug

Both prior specs left the invariant of AC 1 (spec 004) unmet for the primary production provider. Spec 005's verification passed because the verifier's `httptest` fixtures emit uncompressed SSE — a shape that never reaches production for anthropic-subscription. This is a **verification-gap bug in the AC design**, not just the code. The AC 3 (Ginkgo spec) in spec 005 explicitly required a fixture "matching real Anthropic SSE format" but did not require the fixture to include `Content-Encoding: gzip` — so the fix was verified against synthetic bytes that don't reflect the wire.

The fix scope for THIS spec is code + verification realism: the AC MUST include a fixture that emits gzip-encoded bytes, and the live-smoke AC must be exercised before spec close.

## Acceptance Criteria

- [ ] **Live `anthropic-subscription` 200 responses log matching token counts under gzip.** Evidence: after the fix ships, install via `cd ~/Documents/workspaces/claude-code-router && make install && launchctl kickstart -k gui/$(id -u)/de.bborbe.claude-code-router`, then let the router serve ≥ 10 real Claude Code requests. `grep 'provider=anthropic-subscription status=200' /tmp/claude-code-router.log | tail -20 | grep -c 'in=[0-9]'` divided by the total count of matching lines ≥ 0.95. Baseline pre-fix: 0/5 = 0%.

- [ ] **Ginkgo spec: gzip-encoded JSON response with `Content-Encoding: gzip` is decoded and extracted.** Evidence: a new test case in `pkg/handler/usage-recorder_test.go` drives an `httptest` upstream that emits a JSON body wrapped in `gzip.NewWriter` with header `Content-Encoding: gzip`. The extractor decodes and returns `TokenUsage{Input:"42", Output:"128"}` (numbers varied across cases: `"7","3"`, `"1000","250"`, `"55","66"` — anti-fake). This test MUST fail against v0.17.1's extractor and pass against the fix build.

- [ ] **Ginkgo spec: gzip-encoded SSE response with `Content-Encoding: gzip` is decoded and split-event scan works.** Evidence: a new test case emits a gzipped multi-event SSE stream (Anthropic format: `message_start` + `message_delta`) with headers `Content-Type: text/event-stream` and `Content-Encoding: gzip`. Decompressed bytes are fed to `extractUsageSSE`; extraction returns the expected split-event counts.

- [ ] **Large-body decompression: 500 KB+ gzipped body still yields extraction.** Evidence: a Ginkgo spec builds a JSON body ≥ 500 KB (padded content field) with the `usage` field near the end, gzips it, drives the extractor, and asserts extraction succeeds. This closes the "tail-buffer too small" root-cause branch.

- [ ] **Buffer bound is grown to 2 MiB (`2 << 20` = 2097152 bytes) and is a frozen constant.** Evidence: two Ginkgo tests establish the size boundary — (a) a 1.5 MiB gzipped body (below bound) extracts `TokenUsage{Input:"200", Output:"400"}` correctly; (b) a 3 MiB gzipped body (above bound) returns `noUsage` cleanly (the buffer truncated the body; decompression from a truncated gzip yields no valid usage; `defer recover()` catches). Also: `grep -n 'TailBufferBytes' pkg/handler/usage-recorder.go` returns `2 << 20`; no new YAML config field is added.

- [ ] **Missing / `identity` `Content-Encoding` uses the identity path.** Evidence: a Ginkgo test with no `Content-Encoding` header and an uncompressed JSON body extracts `TokenUsage{Input:"33", Output:"77"}`. A second test with `Content-Encoding: identity` and the same body also extracts. Both prove the existing minimax path is unchanged.

- [ ] **Unsupported `Content-Encoding` (br / deflate / zstd) logs `in=- out=-` without panic.** Evidence: a Ginkgo test with `Content-Encoding: br` and gzipped bytes (or any bytes) does NOT attempt decompression, returns `noUsage`. Extraction returns cleanly; `defer recover()` is not triggered because the code path never enters an unknown-encoding branch.

- [ ] **Corrupt / truncated gzip yields `noUsage` without panic.** Evidence: a test case feeds truncated gzip bytes to `ExtractUsage`; the `defer recover()` in `ExtractUsage` catches the decompression error and returns `noUsage`. `Expect` does not fail; log line still emits.

- [ ] **`Unwrap()` chain remains intact.** Evidence: existing SSE-flush regression specs continue to pass. This fix touches extraction only, not the tee-writer's write path.

- [ ] **`make precommit` exits 0.**

- [ ] **Existing minimax + Anthropic split-event tests (from v0.17.0 + v0.17.1) continue to pass.** Evidence: `go test ./pkg/handler/ -run UsageRecorder -ginkgo.v` exits 0.

## Verification

```bash
# Lint + tests
make precommit

# New anti-regression Ginkgo tests
go test ./pkg/handler/ -run UsageRecorder -ginkgo.v

# Post-install live smoke (MANDATORY before spec close)
cd ~/Documents/workspaces/claude-code-router && make install
launchctl kickstart -k gui/$(id -u)/de.bborbe.claude-code-router
# Send ≥ 10 real Claude Code requests via the router, then:
grep 'provider=anthropic-subscription status=200' /tmp/claude-code-router.log | tail -20
# expected: ≥ 95% show in=<N> out=<N> with N ≥ 1

# Trace-file verification (single-request sanity check)
curl -X POST http://127.0.0.1:8788/enabletrace
# Send one request, then:
latest=$(ls -t ~/.claude-code-router/trace/*.json | head -1)
# Extract usage from decompressed trace body; assert it matches the [req] log line's in=/out=.
```

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---------|-------------------|----------|
| `Content-Encoding: gzip` header present, body decompresses to valid JSON | Extract usage from decompressed bytes | None — happy path |
| `Content-Encoding: gzip` header present, body is truncated/corrupt (buffer overflow, mid-stream) | `defer recover()` catches; log `in=- out=-`; no panic | Bounded-buffer tradeoff |
| `Content-Encoding: gzip` header present but buffer size < response size (unexpected — 2 MB cap should be enough) | Same as truncated gzip: `in=- out=-` | None — 2 MB is defended in Constraints |
| No `Content-Encoding` header | Fall through to existing identity path (JSON or SSE scan) | None — existing behavior |
| `Content-Encoding: identity` | Same as no header (identity is a no-op encoding) | None |
| `Content-Encoding: br` / `deflate` / `zstd` | Log `in=- out=-`; document as follow-up (out of scope for this spec) | Add support in follow-up bug if observed in `/tmp/claude-code-router.log` across providers |
| Multiple encodings chained (`Content-Encoding: gzip, br`) | Log `in=- out=-` (extractor handles single gzip only) | Rare; deferred |

## Constraints

- **No request modification.** The proxy passes `Accept-Encoding` and every other request header through unchanged. The fix must operate on the RESPONSE side only. (Explicit operator preference: "router stays a router, no request modification.")
- **Buffer bound grows from 64 KiB to a frozen constant (2 MiB (`2 << 20` = 2097152 bytes)) — no config field.** Documented in code as the bound-that-holds-full-body for extraction purposes. Still bounded (bounded memory is a spec 004 constraint).
- **`Unwrap()` chain unchanged.**
- **`[req]` line format unchanged** (still `... in=<N> out=<N>` appended).
- **Existing minimax + Anthropic-SSE paths must not regress.**
- **`defer recover()` in `ExtractUsage` stays** — decompression errors must never abort the log line.
- **Support gzip in this spec.** Other encodings (`deflate`, `br`, `zstd`) are out of scope; log `in=- out=-` and document as follow-up.
- **DoD compliance** per `docs/dod.md`.
- **No new YAML fields.**

## Workaround

Until v0.17.2 ships, the operator has no per-request token visibility for anthropic-subscription in `/tmp/claude-code-router.log`. The Anthropic dashboard shows aggregate usage but not per-request breakdown through the router.

## Do-Nothing Option

Spec 004's AC 1 ("`[req]` includes `in=<N> out=<N>` for every successful 200 response") remains permanently unmet for the primary production provider. Operator token visibility is aggregate-only via the Anthropic dashboard; per-request cost attribution, runaway-context detection, and prompt-regression signal remain absent. v0.17.0 and v0.17.1 investment (SSE detection widening, split-event scan) becomes narrow-purpose — useful for uncompressed providers (minimax) but silent on the highest-value provider. Cost of doing nothing: operational blindness continues; the two prior specs are visibly incomplete in production despite passing their own ACs.

## Suggested Decomposition

Single-prompt fix. Touch surface:
- `pkg/handler/usage-recorder.go`: bump `TailBufferBytes` to 2 MiB; add `decodeIfEncoded(tail []byte, contentEncoding string) []byte` helper that returns decompressed bytes on `gzip`, or `tail` unchanged otherwise; call it at the top of `ExtractUsage` before Content-Type dispatch.
- `pkg/handler/usage-recorder_test.go`: add gzip-JSON, gzip-SSE, large-body, no-encoding, unsupported-encoding, and corrupt-gzip test cases per AC list.
- `CHANGELOG.md`: `fix:` entry under `## Unreleased`.

Rationale for single prompt: all changes are localized to two files and one function, with a shared decompression helper. No independent test-only prompt is needed because the fix and tests share the same fixtures.

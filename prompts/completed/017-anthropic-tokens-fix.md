---
status: completed
spec: ["005"]
summary: 'Fixed Anthropic SSE token extraction: widened SSE detection to Content-Type OR content-scan fallback, rewrote extractUsageSSE to scan for message_start (input_tokens) + message_delta (output_tokens) and combine them, added Ginkgo specs for split-event, wrong/empty Content-Type, and reverse-proxy tee paths, added UsageLogLineValue export, added CHANGELOG Unreleased fix entry.'
execution_id: claude-code-router-bug-token-anthropic-exec-017-anthropic-tokens-fix
dark-factory-version: dev
created: "2026-07-01T07:15:00Z"
queued: "2026-07-01T06:54:57Z"
started: "2026-07-01T06:54:59Z"
completed: "2026-07-01T07:08:23Z"
---

<summary>
- Token counts now appear in the `[req]` log line for real Anthropic SSE responses, not just for `minimax` JSON responses.
- SSE detection no longer relies solely on the `Content-Type` header — a content-scan fallback catches reverse-proxied SSE responses whose sniffed Content-Type is empty or wrong.
- The extractor handles Anthropic's real streaming shape where `input_tokens` lives in the `message_start` event and `output_tokens` lives in the terminal `message_delta` event, combining them into a single `TokenUsage`.
- Partial-data behavior is explicit: `message_start` only renders as `in=<N> out=-`; `message_delta` only renders as `in=- out=<M>`; neither renders as `in=- out=-`.
- New Ginkgo cases use varied token numbers (defeats hardcoded-constant fakes) and cover missing/wrong Content-Type plus a reverse-proxy tee-reception path that proves the tail buffer receives SSE bytes end-to-end.
- The pre-existing `minimax` JSON path and the `Unwrap()` chain are unchanged; the bounded 64 KB tail buffer is unchanged; the `[req]` log line format is unchanged.
- CHANGELOG gains a `## Unreleased` entry with a `fix:` prefix documenting the extraction bug and its resolution.
</summary>

<objective>
Fix the v0.17.0 bug where `provider=anthropic-subscription status=200` responses log `in=- out=-` on 100% of production requests. Root cause is defensive: (a) the reverse-proxied SSE `Content-Type` sniffed via `rec.Header().Get("Content-Type")` is unreliable, and (b) Anthropic splits `input_tokens` (`message_start` event) from `output_tokens` (`message_delta` event) while the current extractor scans only the terminal `message_delta`. The fix covers BOTH branches so that after ship, ≥ 95% of live anthropic-subscription 200s log matching token counts.
</objective>

<context>
Read first:
- `/workspace/specs/in-progress/005-bug-anthropic-tokens-not-extracted.md` — the approved spec. Acceptance Criteria 1–7, Failure Modes table (mandatory: every row maps to a requirement here), Constraints, and Reproduction section verbatim.
- `/workspace/pkg/handler/usage-recorder.go` — current extractor. `ExtractUsage(tail, contentType)` at line 218; `extractUsageSSE(tail)` at line 238 (the single-event scan that this prompt replaces); `extractUsageJSON(tail)` at line 299 (unchanged); `TokenUsage{Input, Output string}` at line 163; `noUsage = TokenUsage{Input:"-", Output:"-"}` at line 172; `logLineValue` at line 177 (unchanged). The `usageRecorder` tee-writer + `Unwrap` chain (lines 43–110) MUST stay untouched.
- `/workspace/pkg/handler/usage-recorder_test.go` — existing Ginkgo specs. The `Describe("content-type routing", ...)` block at line 326–336 asserts the OLD behavior (SSE tail + JSON content-type → noUsage); this prompt INVERTS that assertion (the new content-scan fallback should extract tokens) — see requirement 6.
- `/workspace/pkg/handler/export_test.go` — the `export_test.go` re-exports. Add `UsageLogLineValue` here if the test asserts the rendered form directly.
- `/workspace/pkg/handler/model-router.go` — the wiring call site at line 173: `usage = ExtractUsage(ur.Tail(), rec.Header().Get("Content-Type"))`. Not modified; the fix lives inside `ExtractUsage`/`extractUsageSSE`.
- `/workspace/pkg/handler/status-recorder.go` — the wrapped writer. Not modified.
- `/workspace/CHANGELOG.md` — `## v0.17.0` lives at line 7. Add a NEW `## Unreleased` section ABOVE it (see requirement 8).
- `/workspace/docs/dod.md` — Definition of Done: GoDoc on exported items, `glog.V(n).Infof` (this prompt adds no logging), Ginkgo/Gomega in `handler_test`, no `replace`/`exclude` in `go.mod`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo table-test structure.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — glog conventions (no `Info`; use `V(n).Infof`).
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — CHANGELOG entry style (single bullet under `## Unreleased`, `fix:` prefix for bugs).

<!-- OPEN QUESTION (resolved here, not left for the executor): the spec's Failure Modes row for "Non-Anthropic SSE stream that happens to contain `event: message_start`" is acknowledged as a low-risk false-positive path. This prompt DOES emit numeric counts for any tail whose bytes contain the marker `event: message_` — a tightening (require full `event: message_start\ndata: {"type":"message_start"` sequence) is a follow-up bug fix, NOT this prompt. Do not tighten here. -->

<!-- DESIGN NOTE: the sample-fallback rule for `Input` — when `message_start` has no usage but a terminal `message_delta` carries `input_tokens > 0`, use that as `Input`. This preserves the existing single-event minimax-shape test at usage-recorder_test.go:234-244 (input_tokens=300 in `message_delta` only) and matches the observed behavior of some non-Anthropic providers that ship both counts in one event. Anthropic itself puts `input_tokens` in `message_start`; the fallback only kicks in when start has no `usage` block. -->
</context>

<requirements>

## Extractor changes (`pkg/handler/usage-recorder.go`)

1. **Widen SSE detection in `ExtractUsage` to a Content-Type OR content-scan check.** Replace the current branch at lines 230–233:

   ```go
   if strings.Contains(contentType, "text/event-stream") {
       return extractUsageSSE(tail)
   }
   return extractUsageJSON(tail)
   ```

   with:

   ```go
   // SSE detection: primary signal is Content-Type, but reverse-proxied
   // Anthropic responses have been observed with an empty or wrong
   // Content-Type on the sniffed rec.Header() (see spec 005). Fall back
   // to a content scan for the `event: message_` marker prefix — cheap
   // (single bytes.Contains over ≤ 64 KB) and specific enough to
   // Anthropic's protocol to keep the false-positive risk low.
   if strings.Contains(contentType, "text/event-stream") ||
       bytes.Contains(tail, []byte("event: message_")) {
       return extractUsageSSE(tail)
   }
   return extractUsageJSON(tail)
   ```

   Update the GoDoc block above `ExtractUsage` (currently lines 191–213) to describe the OR-detection and the split-event scan below. Do NOT delete the panic-recover guard (`defer func() { if r := recover(); r != nil { usage = noUsage } }()`) — it stays at the top of `ExtractUsage`.

2. **Rewrite `extractUsageSSE` to scan for BOTH `message_start` and terminal `message_delta`, combining `input_tokens` and `output_tokens`.** Replace the entire body of `extractUsageSSE` (lines 236–296) with the split-event algorithm:

   ```go
   // extractUsageSSE scans tail for Anthropic-shape SSE events and
   // combines input_tokens from message_start with output_tokens from
   // the terminal message_delta. Anthropic emits:
   //
   //   event: message_start
   //   data: {"type":"message_start","message":{...,"usage":{"input_tokens":42,"output_tokens":1}}}
   //   ...
   //   event: message_delta
   //   data: {"type":"message_delta","delta":{...},"usage":{"output_tokens":128}}
   //
   // Partial-data policy (spec 005 Failure Modes):
   //   - Both events parseable            -> TokenUsage{Input:"<N>", Output:"<M>"}
   //   - Only message_start parseable     -> TokenUsage{Input:"<N>", Output:""}  (logs "in=<N> out=-")
   //   - Only message_delta parseable     -> TokenUsage{Input:"",   Output:"<M>"} (logs "in=- out=<M>")
   //   - Neither parseable                -> noUsage                              (logs "in=- out=-")
   //
   // Fallback: if message_start carries no usage block but the terminal
   // message_delta's data has a positive input_tokens field, use that as
   // Input. This preserves the minimax-shape / single-event Anthropic-shape
   // where both counts live in one event, and is a superset of v0.17.0
   // behavior.
   func extractUsageSSE(tail []byte) TokenUsage {
       inputTokens, haveInput := extractSSEEventUsage(tail, []byte("event: message_start"), bytes.Index)
       outputTokens, haveOutput := extractSSEEventUsage(tail, []byte("event: message_delta"), bytes.LastIndex)

       // Anthropic splits: input in start, output in delta. But if start
       // has no usage and delta does carry input_tokens, promote it.
       var (
           inStr  string
           outStr string
       )
       if haveInput.inputPresent {
           inStr = strconv.Itoa(inputTokens.input)
       } else if haveOutput.inputPresent && haveOutput.input > 0 {
           inStr = strconv.Itoa(haveOutput.input)
       }
       if haveOutput.outputPresent {
           outStr = strconv.Itoa(outputTokens.output)
       }
       if inStr == "" && outStr == "" {
           return noUsage
       }
       return TokenUsage{Input: inStr, Output: outStr}
   }
   ```

   The above sketch names `inputTokens` and `outputTokens` for clarity but the actual implementation SHOULD use a single helper that returns per-field presence flags — pick whichever internal shape reads cleanest. The load-bearing contract is:

   - Extract `usage.input_tokens` from the LAST `event: message_start` block whose `data:` payload has a `usage` field.
   - Extract `usage.output_tokens` from the LAST `event: message_delta` block whose `data:` payload has a `usage` field.
   - Field presence is tracked separately from value: an absent `input_tokens` inside a present `usage` block is different from an absent `usage` block, and a real `"input_tokens":0` MUST be reported as `"0"` (not `""`).
   - Presence detection uses the same `json.RawMessage` idiom as the current code (`if usageCheck.Usage == nil || bytes.Equal(usageCheck.Usage, []byte("null"))`).
   - `bytes.LastIndex` for `message_delta` (terminal event); `bytes.LastIndex` for `message_start` is also fine (Anthropic emits it once at the top of a stream, but LastIndex is robust to accidental repeats). Alternatively `bytes.Index` for `message_start` — both correct in practice; pick one and note in the GoDoc.

3. **Introduce a small internal helper** (name it whatever reads cleanly — `scanSSEEventUsage`, `readSSEUsage`, etc.) that takes:
   - `tail []byte`
   - `eventMarker []byte` (e.g. `"event: message_start"` or `"event: message_delta"`)
   - a locator function or a fixed strategy for picking the FIRST vs LAST occurrence

   and returns the parsed `usage.input_tokens` / `usage.output_tokens` PLUS presence flags. The helper is unexported. Its body follows the current `extractUsageSSE` shape (locate `event:` marker → find following `\ndata: ` or `\r\ndata: ` → find event terminator `\n\n` → JSON-unmarshal → check `usage` RawMessage presence → parse ints), just parameterized on the marker.

   Rationale: the SSE parsing scaffolding is identical for `message_start` and `message_delta`; only the marker and the "which field do I care about" differ. Extracting a helper avoids two 40-line copies of the same nested-if scan.

4. **Preserve the `defer recover()` panic guard in `ExtractUsage`.** Do not move it into the helper or into `extractUsageSSE` — leaving it in the exported function is intentional so that a panic in EITHER the SSE or JSON branch is caught at one place. The helper itself may return early on any parse error (no panic guard needed inside it).

5. **Retain `extractUsageJSON` unchanged.** The minimax JSON path (lines 298–324) must not regress. Do not touch that function's body.

## Test changes (`pkg/handler/usage-recorder_test.go`)

Anti-fake reminder: upstream token numbers are DIFFERENT in every new case so a hardcoded-constant fake fails at least one assertion. Quote this in a comment above the new `Describe` block.

6. **INVERT the existing `Describe("content-type routing", ...)` block at lines 326–336.** The current spec asserts `noUsage` when SSE bytes arrive under `application/json`; the fix REVERSES this behavior (content-scan detects SSE regardless of Content-Type). Change the assertion:

   OLD (delete this test body):
   ```go
   It("returns noUsage when SSE body has JSON content-type", func() {
       tail := []byte(
           "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":42,\"output_tokens\":17}}\n\n",
       )
       usage := handler.ExtractUsage(tail, "application/json")
       Expect(usage.Input).To(Equal("-"))
       Expect(usage.Output).To(Equal("-"))
   })
   ```

   NEW:
   ```go
   It("detects SSE via content scan when Content-Type is wrong (e.g. application/json)", func() {
       // spec 005 root cause (a): reverse-proxied SSE responses may present
       // an empty or wrong Content-Type on the sniffed rec.Header(). The
       // fix falls back to a content scan for the "event: message_" marker.
       tail := []byte(
           "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":42,\"output_tokens\":17}}\n\n",
       )
       usage := handler.ExtractUsage(tail, "application/json")
       Expect(usage.Input).To(Equal("42"))
       Expect(usage.Output).To(Equal("17"))
   })

   It("detects SSE via content scan when Content-Type is empty", func() {
       // Different numbers to defeat hardcoded fakes.
       tail := []byte(
           "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1000,\"output_tokens\":1}}}\n\n" +
               "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":250}}\n\n",
       )
       usage := handler.ExtractUsage(tail, "")
       Expect(usage.Input).To(Equal("1000"))
       Expect(usage.Output).To(Equal("250"))
   })

   It("detects SSE via content scan when Content-Type is application/octet-stream", func() {
       // Different numbers again.
       tail := []byte(
           "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":55,\"output_tokens\":1}}}\n\n" +
               "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":66}}\n\n",
       )
       usage := handler.ExtractUsage(tail, "application/octet-stream")
       Expect(usage.Input).To(Equal("55"))
       Expect(usage.Output).To(Equal("66"))
   })
   ```

7. **Add a new `Describe("Anthropic split-event SSE", ...)` block** inside the existing `Describe("extractUsage", ...)`. Each case uses different token numbers.

   ```go
   Describe("Anthropic split-event SSE (input in message_start, output in message_delta)", func() {
       It("combines input_tokens from message_start with output_tokens from message_delta", func() {
           tail := []byte(
               "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_01\",\"usage\":{\"input_tokens\":42,\"output_tokens\":1}}}\n\n" +
                   "event: content_block_start\ndata: {\"type\":\"content_block_start\"}\n\n" +
                   "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"hi\"}}\n\n" +
                   "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":128}}\n\n" +
                   "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
           )
           usage := handler.ExtractUsage(tail, "text/event-stream")
           Expect(usage.Input).To(Equal("42"))
           Expect(usage.Output).To(Equal("128"))
       })

       It("logs 'in=<N> out=-' when only message_start survives in the tail (message_delta evicted / truncated)", func() {
           // Only the message_start block is present; the terminal message_delta was truncated by buffer overflow.
           tail := []byte(
               "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":42,\"output_tokens\":1}}}\n\n",
           )
           usage := handler.ExtractUsage(tail, "text/event-stream")
           Expect(usage.Input).To(Equal("42"))
           Expect(usage.Output).To(Equal(""))
           in, out := handler.UsageLogLineValue(usage)
           Expect(in).To(Equal("42"))
           Expect(out).To(Equal("-"))
       })

       It("logs 'in=- out=<M>' when only message_delta survives in the tail (message_start evicted)", func() {
           // Only the terminal message_delta block is present.
           tail := []byte(
               "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":77}}\n\n",
           )
           usage := handler.ExtractUsage(tail, "text/event-stream")
           Expect(usage.Input).To(Equal(""))
           Expect(usage.Output).To(Equal("77"))
           in, out := handler.UsageLogLineValue(usage)
           Expect(in).To(Equal("-"))
           Expect(out).To(Equal("77"))
       })

       It("logs 'in=- out=-' when neither event survives in the tail", func() {
           tail := []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"filler\"}}\n\n")
           usage := handler.ExtractUsage(tail, "text/event-stream")
           Expect(usage.Input).To(Equal("-"))
           Expect(usage.Output).To(Equal("-"))
       })
   })
   ```

8. **Add a reverse-proxy tee-reception integration test** as a new sibling `Describe("reverse-proxy tee reception", ...)` block. This proves that the tee path (spec 005 root cause (b)) is not bypassed for reverse-proxied SSE responses — i.e. `usageRecorder.Write` really receives the SSE bytes when a real `httputil.ReverseProxy` writes into it.

   ```go
   Describe("reverse-proxy tee reception (spec 005 root cause b)", func() {
       It("receives SSE bytes through a real httputil.ReverseProxy and extracts tokens", func() {
           body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":11,\"output_tokens\":1}}}\n\n" +
               "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":22}}\n\n"

           upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
               w.Header().Set("Content-Type", "text/event-stream")
               _, _ = w.Write([]byte(body))
           }))
           defer upstream.Close()

           upstreamURL, err := url.Parse(upstream.URL)
           Expect(err).NotTo(HaveOccurred())

           proxy := httputil.NewSingleHostReverseProxy(upstreamURL)

           rr := httptest.NewRecorder()
           ur := handler.NewUsageRecorder(rr)
           req := httptest.NewRequest(http.MethodPost, "http://router.local/v1/messages", strings.NewReader(""))
           proxy.ServeHTTP(ur, req)

           tail := handler.UsageRecorderTail(ur)
           Expect(string(tail)).To(ContainSubstring("event: message_start"))
           Expect(string(tail)).To(ContainSubstring("event: message_delta"))

           // Content-Type at the outer recorder should be forwarded by the proxy,
           // but the fix must also work if it was NOT — use whatever the recorder saw.
           usage := handler.ExtractUsage(tail, rr.Header().Get("Content-Type"))
           Expect(usage.Input).To(Equal("11"))
           Expect(usage.Output).To(Equal("22"))
       })
   })
   ```

   Add these imports to `usage-recorder_test.go` if not already present:
   - `"net/http/httputil"`
   - `"net/url"`
   (`net/http`, `net/http/httptest`, `strings` are already imported.)

9. **Retain ALL existing specs in `usage-recorder_test.go`** — tail-buffer specs, `Unwrap` chain specs, single-event `message_delta` spec (line 225), multi-event `message_delta` spec (line 234), truncated-buffer specs, non-streaming JSON specs, panic-safety spec. The only edit is the content-type-routing block per requirement 6. Confirm the multi-event spec at lines 234–244 still passes with the new algorithm (`message_start` has no `usage`; input is promoted from `message_delta`'s `input_tokens=300` via the fallback described in requirement 2).

## Export test helper (`pkg/handler/export_test.go`)

10. **Add an exported wrapper for `logLineValue`** so the tests can assert the rendered form directly. Append to `export_test.go`:

    ```go
    // UsageLogLineValue exposes (TokenUsage).logLineValue for handler_test.
    func UsageLogLineValue(u TokenUsage) (in, out string) {
        return u.logLineValue()
    }
    ```

    This is a one-liner passthrough — no logic. It exists so the split-event tests can prove that `TokenUsage{Input:"42", Output:""}` renders as `("42", "-")` and `TokenUsage{Input:"", Output:"77"}` renders as `("-", "77")`, matching what the `[req]` log line will show.

## CHANGELOG (`CHANGELOG.md`)

11. **Add a `## Unreleased` section above `## v0.17.0`** (currently line 7). Single bullet with `fix:` prefix, per the project's changelog-guide convention for bug fixes:

    ```
    ## Unreleased

    - fix: extract token counts for Anthropic SSE responses. v0.17.0's extractor worked for JSON responses (`minimax`, 452/457 = 99% success) but returned the `noUsage` sentinel for 100% of `anthropic-subscription` 200 responses (0/19) because (a) `Content-Type` sniffing via `rec.Header()` was unreliable for reverse-proxied SSE responses and (b) Anthropic splits `input_tokens` (in the `message_start` event) from `output_tokens` (in the terminal `message_delta` event), while the extractor scanned only the terminal event. Fix: detect SSE via `Content-Type` OR a content scan for the `event: message_` marker, and scan for BOTH `message_start` (for `input_tokens`) and terminal `message_delta` (for `output_tokens`), combining the two into a single `TokenUsage`. Partial-data behavior: `message_start` only → `in=<N> out=-`; `message_delta` only → `in=- out=<M>`; neither → `in=- out=-`. The `Unwrap()` chain, the 64 KB tail buffer, and the `[req]` line format are unchanged; the `minimax` JSON path is unchanged. See [specs/in-progress/005-bug-anthropic-tokens-not-extracted.md](specs/in-progress/005-bug-anthropic-tokens-not-extracted.md).
    ```

    Do NOT bump the version number. Do NOT add a section header for the fix — a single bullet under `## Unreleased`.

## Failure-mode coverage checklist (from spec 005)

Every row in the spec's Failure Modes table must map to a requirement above. Verify:

| Spec Failure Mode row | Covered by |
|-----------------------|------------|
| `message_start` present, `message_delta` truncated → `in=<N> out=-` | Requirement 2 (Output empty when message_delta absent) + Requirement 7 test "logs 'in=<N> out=-'" |
| Neither event in tail → `in=- out=-` | Requirement 2 (return `noUsage` when both empty) + Requirement 7 test "logs 'in=- out=-'" |
| Content-Type empty AND bytes ambiguous → `in=- out=-`, no panic | Requirement 1 (content-scan falls through to JSON path which fails cleanly) + Requirement 4 (panic guard) + existing malformed-JSON spec |
| Anthropic adds new fields to `usage` → still parses | Existing JSON struct unmarshal ignores unknown fields (Go default). No new requirement needed. |
| Non-Anthropic SSE stream containing `event: message_start` marker → may emit numeric counts | Acknowledged in the OPEN QUESTION note in `<context>`; not tightened here per spec's follow-up disposition. |

</requirements>

<constraints>
- **`Unwrap()` chain untouched.** Do not modify `(*usageRecorder).Unwrap` or `(*statusRecorder).Unwrap` or the wrapping in `newUsageRecorder`. The existing `Describe("Unwrap chain", ...)` specs in `usage-recorder_test.go` (lines 144–170) and the SSE-flush regression specs in `model-router_test.go` must continue to pass unchanged.
- **Bounded 64 KB tail buffer unchanged.** `TailBufferBytes = 64 << 10`. Do not change the constant, the `tailBuffer` type, its `write`/`Tail` methods, or the `usageRecorder.Write` tee.
- **`[req]` log line format unchanged.** Field order and label names in the `glog.V(1).Infof("[req] ...")` calls in `model-router.go` stay exactly as they are. This prompt does NOT touch `model-router.go`.
- **`minimax` JSON path unchanged.** `extractUsageJSON` (lines 298–324) is not modified. The existing `Describe("non-streaming JSON responses", ...)` specs must continue to pass.
- **No new YAML fields.** `docs/config.md` and `docs/config.example.yaml` unchanged. Spec 005 Non-goals: extraction knobs are not configurable.
- **`defer recover()` stays.** The panic guard at the top of `ExtractUsage` (currently lines 219–223) is mandatory. Never abort the `[req]` log line on a parse or scan panic.
- **Anti-fake tokens vary across cases.** Every new test case uses DIFFERENT integer values for `input_tokens` and `output_tokens`. A hardcoded-constant fake extractor must fail at least one assertion. Quote this in a comment above the new `Describe("Anthropic split-event SSE", ...)` block.
- **Zero is a real value.** A present `"input_tokens":0` is reported as `"0"` (not the `"-"` sentinel), matching the existing behavior for zero-token JSON responses (usage-recorder_test.go:310-316). Presence is tracked separately from value.
- **DoD compliance.** GoDoc on any new exported item; `glog.V(n).Infof` (no bare `Info` — this prompt adds no logging); Ginkgo/Gomega in `handler_test`; no `replace`/`exclude` in `go.mod`.
- **CHANGELOG entry uses `fix:` prefix.** Per bug-workflow convention (this is a bug fix, not a feature).
- **Do NOT commit.** dark-factory handles git.
- **Existing tests must still pass.** Every spec in `usage-recorder_test.go` (except the one edited by requirement 6) continues to pass; every spec in `model-router_test.go` continues to pass.
</constraints>

<verification>

```bash
cd /workspace
make precommit
```
Must exit 0.

```bash
cd /workspace
go test ./pkg/handler/ -run TestSuite -ginkgo.v -ginkgo.focus "extractUsage|reverse-proxy tee|Anthropic split-event" 2>&1 | tail -80
```
Expect: all new specs PASS. Specifically:
- `combines input_tokens from message_start with output_tokens from message_delta` PASS with `Input=="42", Output=="128"`.
- `logs 'in=<N> out=-' when only message_start survives` PASS.
- `logs 'in=- out=<M>' when only message_delta survives` PASS.
- `logs 'in=- out=-' when neither event survives` PASS.
- `detects SSE via content scan when Content-Type is wrong` PASS with `Input=="42", Output=="17"`.
- `detects SSE via content scan when Content-Type is empty` PASS with `Input=="1000", Output=="250"`.
- `detects SSE via content scan when Content-Type is application/octet-stream` PASS with `Input=="55", Output=="66"`.
- `receives SSE bytes through a real httputil.ReverseProxy` PASS with `Input=="11", Output=="22"`.

```bash
cd /workspace
go test ./pkg/handler/ -run TestSuite -ginkgo.v 2>&1 | tail -40
```
Expect: entire handler suite PASSes (no regression in tail-buffer, `Unwrap` chain, minimax JSON, single-event / multi-event `message_delta`, truncated, panic-safety, or model-router SSE-flush specs).

```bash
grep -n 'recover()' /workspace/pkg/handler/usage-recorder.go
```
Expect at least one match inside `ExtractUsage` (the top-level panic guard is preserved).

```bash
grep -n 'bytes.Contains(tail, \[\]byte("event: message_"))' /workspace/pkg/handler/usage-recorder.go
```
Expect one match — the content-scan fallback added in requirement 1.

```bash
grep -n 'event: message_start\|event: message_delta' /workspace/pkg/handler/usage-recorder.go
```
Expect BOTH markers referenced in the extractor (requirement 2).

```bash
grep -n 'func (u \*usageRecorder) Unwrap\|func (s \*statusRecorder) Unwrap' /workspace/pkg/handler/*.go
```
Expect both methods still present and unchanged.

```bash
head -12 /workspace/CHANGELOG.md
```
Expect: `## Unreleased` heading appears above `## v0.17.0`, with a single `- fix: ...` bullet.

```bash
grep -n 'UsageLogLineValue' /workspace/pkg/handler/export_test.go
```
Expect one match — the exported wrapper added in requirement 10.

</verification>

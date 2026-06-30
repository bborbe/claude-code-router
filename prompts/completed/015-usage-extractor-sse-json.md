---
status: completed
spec: [004-log-input-output-tokens]
summary: Built pure TokenUsage extractor (SSE message_delta scan + non-stream JSON parse) with full Ginkgo coverage, deferred recover guard, and CHANGELOG entry
execution_id: claude-code-router-log-tokens-exec-015-usage-extractor-sse-json
dark-factory-version: dev
created: "2026-06-30T20:01:00Z"
queued: "2026-06-30T20:06:31Z"
started: "2026-06-30T22:37:41Z"
completed: "2026-06-30T22:42:42Z"
---

<summary>
- A pure-function usage extractor reads the bounded tail buffer produced by prompt 1's `usageRecorder` and pulls out input/output token counts from either an Anthropic SSE stream's terminal `message_delta` event or a non-streaming JSON response's top-level `usage` object.
- SSE detection is by `Content-Type: text/event-stream`; the extractor scans the tail for the terminal `message_delta` event and parses its `usage.input_tokens` / `usage.output_tokens`.
- Non-streaming JSON is detected when the tail parses as a JSON object with a top-level `usage` field; the extractor reads `usage.input_tokens` / `usage.output_tokens`.
- Every failure path (truncated buffer where the terminal event was evicted, malformed JSON/SSE, missing `usage`, zero-token usage) yields sentinel `-` values, never a panic and never an error that aborts the caller's log line.
- New Ginkgo specs cover SSE, non-streaming, truncated-buffer, no-usage, and multi-event-ordering cases, with upstream token numbers varied across cases to defeat any hardcoded fake.
- The extractor is a pure function of `(tail []byte, contentType string)` — fully unit-testable without HTTP, depending only on the buffer type from prompt 1.
</summary>

<objective>
Build the token-usage extraction logic as a pure function over the tail buffer from prompt 1's `usageRecorder`, plus full Ginkgo coverage of the SSE, JSON, truncated, and no-usage paths. This is the dependency for prompt 3's log-line wiring.
</objective>

<context>
Read first:
- `/workspace/specs/in-progress/004-log-input-output-tokens.md` — Desired Behaviors 2 and 4; Constraints (best-effort extraction, no panic); Failure Modes rows "Tail buffer overflows", "malformed JSON / malformed SSE", "usage field missing or zero"; AC 2, AC 3, AC 4, AC 5, AC 6.
- `/workspace/pkg/handler/usage-recorder.go` — created by prompt 1. Contains the `usageRecorder` type, the `TailBufferBytes` constant (`64 << 10`), the `newUsageRecorder` constructor, and the `Tail() []byte` method on `usageRecorder`. This prompt adds the extractor to the SAME file and consumes `Tail()`.
- `/workspace/pkg/handler/usage-recorder_test.go` — created by prompt 1. This prompt ADDS its specs to the SAME file (or a sibling `usage-extractor_test.go` if you prefer; either is fine, keep the `handler_test` package).
- `/workspace/pkg/handler/model-router.go` — note `rewriteModelField` uses `map[string]json.RawMessage` for lossless JSON field access; the non-streaming JSON usage parse can use the same idiom or a small struct `struct{ Usage struct{ InputTokens, OutputTokens int `json:"input_tokens","output_tokens"` } `json:"usage"` }`. Use whichever is clearer.
- `/workspace/pkg/handler/anthropic-proxy.go` — the upstream response is an Anthropic reverse-proxy response; the SSE shape is Anthropic's streaming protocol (`event: message_delta\ndata: {"type":"message_delta","delta":{...},"usage":{"input_tokens":N,"output_tokens":N}}`).
- `/workspace/docs/dod.md` — DoD: GoDoc, `bborbe/errors`, `glog.V(n).Infof`, Ginkgo/Gomega, no `replace`/`exclude`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — glog conventions (this prompt adds no logging; the `[req]` append is prompt 3).
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo table-test structure.

<!-- OPEN QUESTION (resolved by the implementer, justified here): The spec's Desired Behavior 2 mentions BOTH "Content-Type: text/event-stream" and "content matching `event: message_delta`" as SSE signals. Prompt 2 picks Content-Type detection as the primary signal because: (a) the upstream sets `Content-Type: text/event-stream` on every Anthropic SSE response, so it is reliable; (b) content-scanning for `event:` requires partial-line state and is fragile when the tail buffer is truncated mid-event. Justify this choice in the extractor's GoDoc comment. As a defensive fallback, the JSON parse path also runs when Content-Type is NOT `text/event-stream` — so a mislabeled SSE body with no Content-Type still falls through to "no usage" (`-`/`-`) rather than panicking. -->
</context>

<requirements>

1. **Add the extraction types and function to `/workspace/pkg/handler/usage-recorder.go`** (the same file prompt 1 created). Define the result type and the pure extractor:

   ```go
   // TokenUsage holds the input/output token counts extracted from an
   // upstream response body. When extraction fails or no usage is
   // present, Input and Output are the empty string and the caller logs
   // the sentinel "-" for each (see logLineValue below). The empty
   // string is the "no data" signal — a real zero-token count from the
   // upstream is reported as "0" (the extractor reports what it parsed).
   type TokenUsage struct {
       Input  string
       Output string
   }

   // noUsage is the sentinel returned when no parseable usage was found.
   // Its fields render as "-" in the [req] log line (in=/out=).
   var noUsage = TokenUsage{Input: "-", Output: "-"}

   // logLineValue renders a token count for the [req] line: the parsed
   // value, or "-" when extraction yielded nothing.
   //
   // (This helper exists so prompt 3 has a single call site; it is
   // defined here next to the type it renders.)
   func (u TokenUsage) logLineValue() (in, out string) {
       if u.Input == "" {
           in = "-"
       } else {
           in = u.Input
       }
       if u.Output == "" {
           out = "-"
       } else {
           out = u.Output
       }
       return in, out
   }
   ```

   Note: `noUsage` uses `var` (not `const`) because Go consts cannot be initialized by a function call or composite literal of a struct with string fields in a way that satisfies all linters — `var` is the correct form for a struct value. This is a frozen package-level value, not a config knob.

2. **Define the pure extractor function:**

   ```go
   // extractUsage pulls input/output token counts out of a response-body
   // tail buffer. SSE responses (Content-Type: text/event-stream) are
   // scanned for the terminal `message_delta` event whose `usage` field
   // carries input_tokens/output_tokens; the terminal event is always the
   // last chunk of an Anthropic stream, so it lives in the tail. JSON
   // responses are parsed for a top-level `usage` object.
   //
   // Detection is by Content-Type (see the open-question note in the
   // spec context for the justification). Extraction is best-effort:
   // truncated tails, malformed JSON/SSE, missing usage, or zero-token
   // usage all yield the noUsage sentinel ("-" / "-") and never an error
   // — the caller's [req] log line must never be aborted by a parse
   // failure.
   func extractUsage(tail []byte, contentType string) TokenUsage {
       // ... implemented in steps 3-4
   }
   ```

   The function signature is `func extractUsage(tail []byte, contentType string) TokenUsage` — a pure function of bytes + content type, no I/O, no panic.

3. **Implement the SSE path.** When `strings.Contains(contentType, "text/event-stream")`:
   - Scan `tail` for the LAST occurrence of the substring `"event: message_delta"` (the terminal event). Anthropic SSE events are `event: <type>\ndata: <json>\n\n`; the `message_delta` event is terminal so it is the last one in the buffer, but scanning for the LAST occurrence is robust to a stray earlier reference.
   - From the byte offset of that `"event: message_delta"`, find the following `data: ` line within the same event block (scan forward for `\ndata:` or `\r\n` variants; Anthropic uses `\n`).
   - Parse the JSON after `data: ` into a struct that captures the `usage` field. A minimal shape:

     ```go
     var sseEvent struct {
         Usage struct {
             InputTokens  int `json:"input_tokens"`
             OutputTokens int `json:"output_tokens"`
         } `json:"usage"`
     }
     if err := json.Unmarshal(dataBytes, &sseEvent); err != nil {
         return noUsage
     }
     ```

   - If `usage.input_tokens` or `usage.output_tokens` is absent, the JSON struct fields stay zero — but per the spec's Failure Modes row "usage field missing or zero", a real upstream zero IS reported as "0". Distinguish "field absent" from "field present and zero" only if you can do so cheaply (e.g. unmarshal `usage` into `json.RawMessage` first; if it is `null` or absent, return `noUsage`; otherwise parse the integers). If the cheap distinction is not feasible, reporting a present-zero as "0" is acceptable (the spec allows this). Document which behavior you chose in the GoDoc.
   - Return `TokenUsage{Input: strconv.Itoa(input), Output: strconv.Itoa(output)}`.
   - On ANY parse error, missing `data:` line, missing `event: message_delta`, or empty tail: return `noUsage`. No panic, no error return.

4. **Implement the JSON (non-streaming) path.** When the Content-Type is NOT `text/event-stream`:
   - Attempt `json.Unmarshal(tail, &obj)` where `obj` is a struct with a top-level `usage` field of the same shape as the SSE `usage`. Use the `map[string]json.RawMessage` idiom from `rewriteModelField` if you want to detect field-presence cheaply; otherwise a struct unmarshal is fine.
   - If the tail is not valid JSON, or has no `usage` field, or the `usage` object lacks the token fields: return `noUsage`.
   - On success return `TokenUsage{Input: strconv.Itoa(input), Output: strconv.Itoa(output)}`.
   - Add `"strconv"` and `"strings"` and `"encoding/json"` to the import block of `usage-recorder.go` if not already present.

5. **Recover from any panic defensively.** Wrap the body of `extractUsage` in a `defer` that recovers a panic and returns `noUsage`. This is the belt-and-suspenders guard for the spec's "never an error that aborts the log line" / "does not panic" constraint — a malformed input that surprises the JSON parser or the SSE scanner must not crash the request log path. Example:

   ```go
   func extractUsage(tail []byte, contentType string) (usage TokenUsage) {
       defer func() {
           if r := recover(); r != nil {
               usage = noUsage
           }
       }()
       // ... SSE / JSON branches ...
   }
   ```

6. **Create or extend `/workspace/pkg/handler/usage-recorder_test.go`** with a `Describe("extractUsage", ...)` block. Use Ginkgo table-driven specs where the cases share a shape. ALL test cases MUST use DIFFERENT token numbers (vary input and output across cases) so a hardcoded `in=0 out=0` or single-constant extractor fails the suite. **Quote this requirement in a comment above the test block:** "Anti-fake: upstream token numbers are varied across all cases — a hardcoded constant extractor must fail these specs (spec 004 AC 2/3)."

   Cases (each asserts the returned `TokenUsage.Input`/`Output` equal the expected decimal strings):

   - **SSE single-event with usage** — tail is a complete `event: message_delta\ndata: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":42,"output_tokens":17}}\n\n`; contentType `text/event-stream`. Expect `Input=="42"`, `Output=="17"`.

   - **SSE multi-event where message_delta is terminal** — tail contains `event: message_start\ndata: {...}\n\n` + `event: content_block_delta\ndata: {...}\n\n` + the terminal `event: message_delta\ndata: {...,"usage":{"input_tokens":300,"output_tokens":99}}\n\n`. Use DIFFERENT numbers from the single-event case. Expect `Input=="300"`, `Output=="99"`. Assert the extractor picked the `message_delta` event, not the first event's data.

   - **SSE with > 64 KB of preceding content (truncated-buffer simulation)** — build a tail that is EXACTLY `TailBufferBytes` long: fill with `(TailBufferBytes - len(terminalEvent))` bytes of `event: content_block_delta\ndata: {"text":"..."}` filler, then append the terminal `event: message_delta\ndata: {...,"usage":{"input_tokens":7,"output_tokens":3}}\n\n`. Use distinct numbers. Expect `Input=="7"`, `Output=="3"`. This proves the terminal event survives in the tail and is found even when preceded by a full buffer of filler.

   - **SSE terminal event evicted by overflow** — build a tail of length `TailBufferBytes` where the LAST `TailBufferBytes` bytes are ALL filler (the terminal `message_delta` was written but then evicted by subsequent filler writes). Expect `noUsage` (`Input=="-"`, `Output=="-"`). This is the bounded-buffer tradeoff from the spec's Failure Modes row "Tail buffer overflows".

   - **Non-streaming JSON with usage** — tail `{"id":"msg_01","usage":{"input_tokens":100,"output_tokens":5}}`; contentType `application/json`. Expect `Input=="100"`, `Output=="5"`.

   - **Non-streaming JSON without usage** — tail `{"ok":true}`; contentType `application/json`. Expect `noUsage`.

   - **Non-streaming JSON with usage present and zero** — tail `{"usage":{"input_tokens":0,"output_tokens":0}}`; contentType `application/json`. Expect `Input=="0"`, `Output=="0"` (the upstream literally sent zeros — the extractor reports what it parsed, NOT the sentinel). This is the spec's "usage field missing or zero" row: zero is a real value.

   - **Malformed JSON tail** — tail `{not json`; contentType `application/json`. Expect `noUsage`, no panic.

   - **Malformed SSE tail (data line truncated mid-JSON)** — tail `event: message_delta\ndata: {"type":"message_delta","usage":{"input_tokens":42` (truncated); contentType `text/event-stream`. Expect `noUsage`, no panic.

   - **Empty tail** — tail `nil`; contentType `text/event-stream`. Expect `noUsage`.

   - **Content-Type mismatch (SSE body but JSON content-type)** — tail is a valid `message_delta` SSE block but contentType is `application/json`. Expect `noUsage` (the SSE path does not run; the JSON parse of the raw SSE text fails). This documents the detection contract.

   - **Panic safety** — feed the extractor a pathological input that would panic a naive parser (e.g. a deeply nested JSON or a `data: ` line pointing at an offset past the buffer end via a crafted tail). Assert `noUsage` and no panic. Use Ginkgo's `DeferCleanup`/`recover`-aware assertion: the spec itself must not abort, so wrap the call in a function that recovers and asserts the recovered value matches `noUsage`.

7. **Run `make precommit`** in the repo root. Fix any lint / format / addlicense issues. The extractor is not yet wired into the `[req]` log line (prompt 3); these specs call `extractUsage` directly as a pure function.

</requirements>

<constraints>
- **Best-effort extraction (from spec).** Every failure path returns the `noUsage` sentinel (`"-"`/`"-"`) — never an error that aborts the caller's log line, never a panic. The `defer recover()` guard is mandatory.
- **No latency regression (from spec).** `extractUsage` is a single scan over ≤ `TailBufferBytes` (64 KB); no per-byte parsing during streaming. Called once after the upstream handler returns (prompt 3 does the call).
- **SSE detection by Content-Type (implementer decision, justified in GoDoc).** Primary signal: `strings.Contains(contentType, "text/event-stream")`. The JSON path is the fallback for any other content type.
- **Anti-fake (load-bearing — from spec AC 2/3):** upstream token numbers MUST vary across ALL test cases so a hardcoded `in=0 out=0` or single-constant extractor fails the suite. Quote this in a comment above the test block.
- **Zero is a real value (from spec Failure Modes).** A present `usage.input_tokens: 0` is reported as `"0"`, NOT as the `"-"` sentinel. Document which presence-detection strategy you used in the GoDoc.
- **`Unwrap()` chain must stay functional (from spec).** This prompt does NOT touch `usageRecorder.Unwrap` or `statusRecorder.Unwrap`; the chain from prompt 1 is unchanged. Do not regress it.
- **Bounded memory (from spec).** The extractor reads the tail via `Tail()`; it does NOT allocate an unbounded copy. If you copy the tail for scanning, the copy is ≤ `TailBufferBytes`.
- **Do NOT wire into `NewModelRouter` in this prompt.** Wiring is prompt 3.
- **No new YAML fields (from spec).** `docs/config.md` and `docs/config.example.yaml` unchanged.
- **DoD compliance (from spec):** GoDoc on `TokenUsage`, `extractUsage`, `noUsage`; `bborbe/errors` only if error wrapping is needed (the extractor returns no error, so likely unused here); `glog.V(n).Infof` (this prompt adds no logging); Ginkgo/Gomega in `pkg/handler/`; no `replace`/`exclude` in `go.mod`.
- **Do NOT commit.** dark-factory handles git.
- **Existing tests must still pass.** Prompt 1's specs continue to pass; this prompt only adds to the same test file.
</constraints>

<verification>
```bash
cd /workspace
make precommit
```
Must exit 0.

```bash
cd /workspace
go test ./pkg/handler/ -run TestSuite -ginkgo.v -ginkgo.focus "extractUsage" 2>&1 | tail -60
```
Expect: all `extractUsage` specs PASS, plus prompt 1's `usageRecorder tail buffer` and `Unwrap chain` specs still PASS.

Confirm the panic-safety guard is present:
```bash
grep -n 'recover()' /workspace/pkg/handler/usage-recorder.go
```
Expect at least one match inside `extractUsage`.

Confirm the `Unwrap` chain from prompt 1 is untouched:
```bash
grep -n 'func (u \*usageRecorder) Unwrap\|func (s \*statusRecorder) Unwrap' /workspace/pkg/handler/*.go
```
Expect both methods still present and unchanged.
</verification>

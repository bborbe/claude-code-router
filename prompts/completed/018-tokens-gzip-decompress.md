---
status: completed
spec: ["006"]
summary: 'Implemented gzip decompression for token usage extraction: grew TailBufferBytes to 2 MiB, added decodeIfEncoded helper, grew ExtractUsage to 3 args, wired Content-Encoding header, updated all test call sites, added 11 new gzip Ginkgo specs, updated CHANGELOG.'
execution_id: claude-code-router-bug-gzip-exec-018-tokens-gzip-decompress
dark-factory-version: dev
created: "2026-07-01T09:45:00Z"
queued: "2026-07-01T09:40:53Z"
started: "2026-07-01T09:40:54Z"
completed: "2026-07-01T09:44:12Z"
---

<summary>
- Router now correctly extracts token counts from real Anthropic responses that arrive with `Content-Encoding: gzip` (both JSON and split-event SSE shapes).
- The retained response-tail buffer grows from 64 KiB to 2 MiB so the full compressed body fits — gzip is not self-synchronizing, so decompression requires start-of-stream bytes.
- Any `Content-Encoding` header equal to `gzip` (case-insensitive, trimmed) triggers decompression; empty, `identity`, and every other encoding (`br`, `deflate`, `zstd`) fall through unchanged and produce the `noUsage` sentinel cleanly when they can't be scanned.
- Corrupt or truncated gzip never panics or aborts the `[req]` log line; the existing `defer recover()` inside `ExtractUsage` catches it and yields `in=- out=-`.
- New Ginkgo cases exercise: gzip-JSON extraction, gzip-SSE split-event extraction, 1.5 MiB gzip body within the 2 MiB cap, 3 MiB gzip body truncated at the cap (returns `noUsage`), missing encoding, `identity` encoding, unsupported `br` encoding, and corrupt gzip bytes. Every case uses distinct token numbers so a hardcoded fake fails.
- The router's request path is untouched — no changes to `Accept-Encoding` on the client's inbound request; the router stays a router.
- The `Unwrap()` chain, the `[req]` log line format, and the pre-existing minimax + Anthropic split-event SSE tests remain intact.
- CHANGELOG gets a `## Unreleased` `fix:` entry describing why v0.17.0–v0.17.1 was blind to gzipped responses.
</summary>

<objective>
Make `provider=anthropic-subscription status=200` responses log `in=<N> out=<N>` on the `[req]` line when the upstream returns `Content-Encoding: gzip` bodies up to 2 MiB. Live baseline pre-fix: 0/5 = 0%. The extractor must decompress single-frame gzip before running the existing SSE/JSON scans; the tail buffer must grow to hold the full body so the gzip stream starts at offset 0.
</objective>

<context>
Read first:
- `/workspace/specs/in-progress/006-bug-tokens-gzip-decompress.md` — the approved bug spec. Acceptance Criteria (all 10 boxes), Failure Modes table, Constraints, Reproduction, and the trace-file evidence showing `Content-Encoding: gzip` on live Anthropic 200s.
- `/workspace/pkg/handler/usage-recorder.go` — current extractor:
  - `const TailBufferBytes = 64 << 10` at line 22 (grow to `2 << 20`).
  - `func ExtractUsage(tail []byte, contentType string) (usage TokenUsage)` at line 228 (grows a third param).
  - `defer func() { if r := recover(); r != nil { usage = noUsage } }()` at lines 229–233 — this MUST stay at the top of `ExtractUsage` and it catches decompression panics as well as parse panics.
  - `extractUsageSSE` at line 362 and `extractUsageJSON` at line 387 — bodies unchanged; both receive the (possibly decompressed) bytes.
  - `scanSSEEvent` helper at line 270 — unchanged.
  - `noUsage = TokenUsage{Input:"-", Output:"-"}` at line 172.
- `/workspace/pkg/handler/model-router.go` — call site at line 173: `usage = ExtractUsage(ur.Tail(), rec.Header().Get("Content-Type"))`. Add `rec.Header().Get("Content-Encoding")` as the third arg.
- `/workspace/pkg/handler/usage-recorder_test.go` — Ginkgo patterns and existing test call sites (~19 call sites at lines 231, 243, 261, 274, 280, 290, 300, 307, 315, 322, 339, 351, 362, 386, 410, 424, 441, 455, 495). Every one grows a third argument (default `""`).
- `/workspace/pkg/handler/export_test.go` — `ExtractUsage` is already exported directly (no alias), so no wrapper change is needed there; however, if any test uses a helper wrapper (grep confirms none for `ExtractUsage` — direct calls only), still leave `export_test.go` unchanged unless a new export is genuinely needed.
- `/workspace/CHANGELOG.md` — `## v0.17.1` at line 7. Insert a new `## Unreleased` section above it.
- `/workspace/docs/dod.md` — Definition of Done (GoDoc on exported items, Ginkgo/Gomega tests, `## Unreleased` CHANGELOG entry, no `replace`/`exclude` in `go.mod`).
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo table-test conventions.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — no bare `Info`; this prompt adds no logging.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` above the top released version, `fix:` prefix for bugs, single bullet.

<!-- DESIGN NOTE (resolved here, not left for executor): the `decodeIfEncoded` helper returns `nil` (not `tail`) on gzip decompression error. Rationale: if the header explicitly says `gzip` and decompression fails, the raw compressed bytes are not valid JSON/SSE either, so falling back to identity on them just wastes a scan and produces the same `noUsage` result. Returning `nil` short-circuits to the `len(tail) == 0` guard at the top of `ExtractUsage`, which returns `noUsage` immediately. For unknown encodings (not gzip, not identity, not empty) the helper returns `nil` too — the spec explicitly defers `br`/`deflate`/`zstd` to a follow-up and requires `in=- out=-`. -->

<!-- DESIGN NOTE: the 8 MiB decompressed cap inside the gzip reader is a defensive DoS-bound — a hostile 2 MiB gzip payload can decompress to gigabytes. Use `io.LimitReader(gz, 8<<20)` or `io.ReadAll` with a `io.LimitReader` wrapper. Do NOT plumb this cap through YAML — it's a frozen constant. -->

<!-- OPEN QUESTION (deferred, NOT addressed here): if operators ever see recurring `br`/`deflate`/`zstd` encodings in production logs, a follow-up bug spec should add those decoders. Spec 006 Non-goals says defer. Do not add them here. -->
</context>

<requirements>

## Buffer bump + decode helper (`pkg/handler/usage-recorder.go`)

1. **Bump `TailBufferBytes` from `64 << 10` to `2 << 20`** (= 2 MiB, 2097152 bytes). Update the GoDoc block immediately above the const (currently lines 15–22) to explain why the bound grew: real Anthropic non-streaming JSON responses have been observed at ~500 KB gzipped and Cloudflare emits `Content-Encoding: gzip`; the tail must hold the FULL compressed body because gzip is not self-synchronizing (only start-of-stream can be decompressed). The constant is still frozen and NOT a config field — spec 006 Non-goals bans a YAML knob.

   Old:
   ```go
   // TailBufferBytes caps the number of response-body bytes retained for
   // post-request usage extraction. ...
   const TailBufferBytes = 64 << 10 // 64 KB
   ```

   New:
   ```go
   // TailBufferBytes caps the number of response-body bytes retained for
   // post-request usage extraction. The bound must hold the FULL response
   // body when the upstream sends `Content-Encoding: gzip` because gzip is
   // not self-synchronizing — decompression requires the start-of-stream
   // bytes, so a mid-stream tail is unrecoverable. Real Anthropic 200
   // responses have been observed at ~500 KB gzipped through
   // Cloudflare (spec 006 Reproduction). 2 MiB is a frozen constant
   // chosen to cover the observed p99 with headroom; it is NOT a YAML
   // field (spec 006 Non-goals). A full 5 MB streaming response therefore
   // occupies at most TailBufferBytes of additional memory.
   const TailBufferBytes = 2 << 20 // 2 MiB
   ```

   All existing tail-buffer specs that reference `handler.TailBufferBytes` (e.g. sliding-window eviction, terminal-chunk retention) continue to work because they compute sizes off the constant.

2. **Add a `decodeIfEncoded` unexported helper** placed near the top of `usage-recorder.go`, immediately below the const block and above `type usageRecorder struct`. Import `compress/gzip`, `io`, and `bytes` (bytes is already imported). Add `strings` (already imported).

   ```go
   // decodedTailCapBytes bounds the number of decompressed bytes read from
   // a gzip stream during usage extraction. A hostile ~2 MiB gzip payload
   // can inflate to gigabytes; this bound keeps memory + CPU predictable.
   // Frozen constant, NOT a config field (spec 006 Non-goals).
   const decodedTailCapBytes = 8 << 20 // 8 MiB

   // decodeIfEncoded returns tail decompressed when contentEncoding names a
   // supported encoding (currently: gzip only), or tail unchanged when the
   // encoding is empty or "identity" (both are no-op encodings per RFC 9110
   // §8.4). For any other encoding value ("br", "deflate", "zstd", chained
   // encodings, unknown tokens) the helper returns nil so the caller's
   // len(tail) == 0 guard short-circuits to noUsage — spec 006 defers those
   // encodings and requires the [req] line to log in=- out=-.
   //
   // Decompression is bounded by decodedTailCapBytes to defend against
   // decompression bombs. A truncated gzip stream, corrupt bytes, or an
   // I/O error returns nil (again short-circuiting to noUsage via the
   // caller's empty-tail guard). This function itself does not panic;
   // io.ReadAll and gzip.NewReader return errors on every failure shape
   // observed on real Anthropic + Cloudflare traffic.
   func decodeIfEncoded(tail []byte, contentEncoding string) []byte {
       enc := strings.ToLower(strings.TrimSpace(contentEncoding))
       switch enc {
       case "", "identity":
           return tail
       case "gzip":
           gz, err := gzip.NewReader(bytes.NewReader(tail))
           if err != nil {
               return nil
           }
           defer func() { _ = gz.Close() }()
           decoded, err := io.ReadAll(io.LimitReader(gz, decodedTailCapBytes))
           if err != nil {
               return nil
           }
           return decoded
       default:
           return nil
       }
   }
   ```

   GoDoc requirements:
   - Document the return-nil-on-error contract explicitly.
   - Document that only `gzip` is supported and that empty/`identity` are no-ops.
   - Document the frozen 8 MiB decompressed cap.

3. **Grow `ExtractUsage` to accept `contentEncoding string` as the third parameter** and call `decodeIfEncoded` at the top, BEFORE the empty-tail guard's second check. The final signature:

   ```go
   func ExtractUsage(tail []byte, contentType, contentEncoding string) (usage TokenUsage) {
       defer func() {
           if r := recover(); r != nil {
               usage = noUsage
           }
       }()

       // Empty tail: nothing to parse.
       if len(tail) == 0 {
           return noUsage
       }

       // Decode Content-Encoding (currently: gzip only). See spec 006:
       // Cloudflare + Anthropic send gzipped bodies to the router, and
       // text scans over compressed bytes cannot find `usage` or
       // `event: message_` markers. decodeIfEncoded returns tail unchanged
       // when the encoding is empty or "identity"; returns decompressed
       // bytes for gzip; returns nil for unsupported encodings or on
       // gzip error (defers to the empty-check that follows).
       tail = decodeIfEncoded(tail, contentEncoding)
       if len(tail) == 0 {
           return noUsage
       }

       if strings.Contains(contentType, "text/event-stream") ||
           bytes.Contains(tail, []byte("event: message_")) {
           return extractUsageSSE(tail)
       }
       return extractUsageJSON(tail)
   }
   ```

   - Keep the `defer recover()` guard EXACTLY where it is (top of function). Its purpose expands here to also catch any latent panic inside `gzip.NewReader` / `io.ReadAll` (none observed in practice, but the guard is our belt-and-braces per spec 006 constraint "defer recover() stays").
   - Update the GoDoc block above `ExtractUsage` (currently lines 191–227) to document the new parameter and the decode-first flow. Insert a sentence: "The contentEncoding parameter carries the upstream's `Content-Encoding` header; only `gzip` is decoded (spec 006), empty and `identity` values are no-ops, any other encoding aborts extraction to `noUsage`."
   - Do NOT delete the panic-recover guard.
   - Do NOT modify `extractUsageSSE` or `extractUsageJSON` bodies — they receive the decompressed bytes and continue to work unchanged.

## Wiring update (`pkg/handler/model-router.go`)

4. **Pass `Content-Encoding` as the third argument at the call site** (around line 173). Replace:

   ```go
   usage = ExtractUsage(ur.Tail(), rec.Header().Get("Content-Type"))
   ```

   with:

   ```go
   usage = ExtractUsage(
       ur.Tail(),
       rec.Header().Get("Content-Type"),
       rec.Header().Get("Content-Encoding"),
   )
   ```

   No other line in `model-router.go` changes. Do NOT touch the `[req]` log line format, the sampler gate, the `V(1)` gating, the wrapping of `rec` and `ur`, or the `Unwrap()` chain.

## Test call-site updates + new gzip cases (`pkg/handler/usage-recorder_test.go`)

5. **Update every existing `handler.ExtractUsage(...)` call site to pass the third argument.** Every existing call becomes a call with `""` (empty encoding) as the third argument — this preserves the existing "identity path" behavior verbatim. Grep confirms 19 call sites at lines 231, 243, 261, 274, 280, 290, 300, 307, 315, 322, 339, 351, 362, 386, 410, 424, 441, 455, 495.

   Example:
   ```go
   // BEFORE
   usage := handler.ExtractUsage(tail, "text/event-stream")
   // AFTER
   usage := handler.ExtractUsage(tail, "text/event-stream", "")
   ```

   The 3-arg-with-`""` form MUST behave identically to the current 2-arg form; that identity is itself asserted by the "no encoding" and "identity encoding" cases added in requirement 6 below.

6. **Add a new `Describe("Content-Encoding: gzip decompression (spec 006)", ...)` block** as a sibling to the existing `Describe` blocks inside the `Describe("extractUsage", ...)` parent. Add these imports at the top of `usage-recorder_test.go` (some may already be present):
   - `"bytes"`
   - `"compress/gzip"`
   - `"strings"` (already present)

   Anti-fake: EVERY case uses distinct token numbers so a hardcoded fake fails at least one assertion. Quote this in a comment above the new `Describe` block. Do not reuse `42/128`, `7/3`, `1000/250`, `55/66`, `11/22`, `300/99`, `100/5`, `999/1`, `77`, `33/77` (these are already claimed by earlier cases). Pick new distinct pairs per case.

   Add a small helper closure (defined inside the `Describe` `BeforeEach` or as a package-level `func` — either works, the closure is preferred to keep the fixture code inside the `Describe`):

   ```go
   gzipBytes := func(raw []byte) []byte {
       var buf bytes.Buffer
       gw := gzip.NewWriter(&buf)
       _, err := gw.Write(raw)
       Expect(err).NotTo(HaveOccurred())
       Expect(gw.Close()).To(Succeed())
       return buf.Bytes()
   }
   ```

   Then add these `It` cases:

   **6a. Gzip JSON body extracts usage.**
   ```go
   It("decompresses gzipped JSON and extracts usage", func() {
       // Anti-fake: input=142, output=271 — distinct from every other case.
       raw := []byte(`{"id":"msg_g1","usage":{"input_tokens":142,"output_tokens":271}}`)
       tail := gzipBytes(raw)
       usage := handler.ExtractUsage(tail, "application/json", "gzip")
       Expect(usage.Input).To(Equal("142"))
       Expect(usage.Output).To(Equal("271"))
   })
   ```

   **6b. Gzip SSE split-event body extracts combined counts.**
   ```go
   It("decompresses gzipped split-event SSE and combines input+output tokens", func() {
       // Anti-fake: input=513, output=624.
       raw := []byte(
           "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_g2\",\"usage\":{\"input_tokens\":513,\"output_tokens\":1}}}\n\n" +
               "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"x\"}}\n\n" +
               "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":624}}\n\n",
       )
       tail := gzipBytes(raw)
       usage := handler.ExtractUsage(tail, "text/event-stream", "gzip")
       Expect(usage.Input).To(Equal("513"))
       Expect(usage.Output).To(Equal("624"))
   })
   ```

   **6c. Case-insensitive header value: `GZIP` also decodes.**
   ```go
   It("treats Content-Encoding case-insensitively (GZIP == gzip)", func() {
       // Anti-fake: input=88, output=911.
       raw := []byte(`{"usage":{"input_tokens":88,"output_tokens":911}}`)
       tail := gzipBytes(raw)
       usage := handler.ExtractUsage(tail, "application/json", "GZIP")
       Expect(usage.Input).To(Equal("88"))
       Expect(usage.Output).To(Equal("911"))
   })
   ```

   **6d. Whitespace around header value is trimmed.**
   ```go
   It("trims surrounding whitespace on Content-Encoding", func() {
       // Anti-fake: input=17, output=29.
       raw := []byte(`{"usage":{"input_tokens":17,"output_tokens":29}}`)
       tail := gzipBytes(raw)
       usage := handler.ExtractUsage(tail, "application/json", "  gzip  ")
       Expect(usage.Input).To(Equal("17"))
       Expect(usage.Output).To(Equal("29"))
   })
   ```

   **6e. Large body within 2 MiB cap: 1.5 MiB uncompressed → gzip → extract.**
   ```go
   It("decompresses a large gzipped body (1.5 MiB uncompressed) that fits under the 2 MiB tail cap", func() {
       // Anti-fake: input=2048, output=3072.
       // Pad the JSON with a large filler string; keep the "usage" object
       // near the end so extractUsageJSON finds it in the top-level parse.
       const padSize = 1 << 20 + (1 << 19) // 1.5 MiB of filler
       padding := strings.Repeat("x", padSize)
       raw := []byte(`{"pad":"` + padding + `","usage":{"input_tokens":2048,"output_tokens":3072}}`)
       tail := gzipBytes(raw)
       // Sanity: compressed size should comfortably fit under 2 MiB.
       Expect(len(tail)).To(BeNumerically("<", handler.TailBufferBytes))
       usage := handler.ExtractUsage(tail, "application/json", "gzip")
       Expect(usage.Input).To(Equal("2048"))
       Expect(usage.Output).To(Equal("3072"))
   })
   ```

   **6f. Oversize body: 3 MiB uncompressed → gzipped → truncated to 2 MiB → noUsage cleanly.**
   ```go
   It("returns noUsage cleanly when the gzipped body was truncated by the tail buffer", func() {
       // A 3 MiB uncompressed JSON gzipped will exceed the 2 MiB tail cap
       // in some compressible-content scenarios; simulate by truncating.
       // If the truncated bytes still start with a valid gzip header, the
       // reader will return an unexpected EOF partway; if they do not,
       // gzip.NewReader itself errors. Either way: noUsage, no panic.
       const rawSize = 3 << 20
       // Use incompressible-ish random-looking content so gzip doesn't
       // shrink it below the cap (anti-fake: a repeated byte compresses
       // to ~0.1% and would falsely fit).
       raw := make([]byte, rawSize)
       for i := range raw {
           raw[i] = byte((i*31 + 7) & 0xff)
       }
       // Prepend a JSON prefix so if decompression somehow succeeds, the
       // JSON parse still fails and returns noUsage — belt-and-braces.
       compressed := gzipBytes(raw)
       // Force truncation to <= TailBufferBytes.
       if len(compressed) > handler.TailBufferBytes {
           compressed = compressed[:handler.TailBufferBytes]
       }
       usage := handler.ExtractUsage(compressed, "application/json", "gzip")
       Expect(usage.Input).To(Equal("-"))
       Expect(usage.Output).To(Equal("-"))
   })
   ```

   **6g. No Content-Encoding header: identity path preserved.**
   ```go
   It("preserves the identity path when Content-Encoding is empty", func() {
       // Anti-fake: input=61, output=83.
       tail := []byte(`{"usage":{"input_tokens":61,"output_tokens":83}}`)
       usage := handler.ExtractUsage(tail, "application/json", "")
       Expect(usage.Input).To(Equal("61"))
       Expect(usage.Output).To(Equal("83"))
   })
   ```

   **6h. `identity` Content-Encoding: same as no header.**
   ```go
   It("treats Content-Encoding: identity as a no-op", func() {
       // Anti-fake: input=71, output=89.
       tail := []byte(`{"usage":{"input_tokens":71,"output_tokens":89}}`)
       usage := handler.ExtractUsage(tail, "application/json", "identity")
       Expect(usage.Input).To(Equal("71"))
       Expect(usage.Output).To(Equal("89"))
   })
   ```

   **6i. Unsupported `br` encoding: noUsage, no panic, no decompression attempt.**
   ```go
   It("returns noUsage for unsupported encodings (br)", func() {
       // Even if the bytes are structurally valid JSON with a usage block,
       // an unsupported encoding must not extract — spec 006 defers br/
       // deflate/zstd. The [req] line logs in=- out=-.
       raw := []byte(`{"usage":{"input_tokens":31,"output_tokens":47}}`)
       usage := handler.ExtractUsage(raw, "application/json", "br")
       Expect(usage.Input).To(Equal("-"))
       Expect(usage.Output).To(Equal("-"))
   })
   ```

   **6j. Corrupt gzip: noUsage via the defer recover() + empty-check path.**
   ```go
   It("returns noUsage on corrupt gzip bytes (no panic)", func() {
       // Random bytes that are not a valid gzip header.
       corrupt := []byte{0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
       usage := handler.ExtractUsage(corrupt, "application/json", "gzip")
       Expect(usage.Input).To(Equal("-"))
       Expect(usage.Output).To(Equal("-"))
   })
   ```

   **6k. Chained encoding (`gzip, br`): noUsage cleanly.**
   ```go
   It("returns noUsage for chained encodings (gzip, br)", func() {
       // Anthropic + Cloudflare have not been observed emitting chained
       // encodings, but RFC 9110 permits `Content-Encoding: gzip, br`.
       // The decoder currently supports only single-encoding gzip; a
       // chained header is treated as an unknown encoding and yields
       // noUsage (spec 006 Failure Modes).
       raw := []byte(`{"usage":{"input_tokens":13,"output_tokens":19}}`)
       usage := handler.ExtractUsage(raw, "application/json", "gzip, br")
       Expect(usage.Input).To(Equal("-"))
       Expect(usage.Output).To(Equal("-"))
   })
   ```

7. **Retain ALL existing specs** in `usage-recorder_test.go`. The only structural edit outside the new `Describe` block is the mechanical 2-arg → 3-arg call-site update from requirement 5. Do NOT delete, reorder, or rewrite the tail-buffer, `Unwrap` chain, single-event `message_delta`, split-event `message_start` + `message_delta`, panic-safety, or reverse-proxy tee-reception specs.

## `export_test.go`

8. **No change required to `export_test.go`.** `ExtractUsage` is already exported directly (its identifier starts with a capital letter; a `//nolint:revive` marker at line 227 acknowledges the intentional export). Tests call `handler.ExtractUsage(...)` directly. Since the signature grows but the symbol stays exported, no re-export wrapper needs adjustment.

   Verify (mechanical grep): `grep -n 'ExtractUsage' /workspace/pkg/handler/export_test.go` returns zero matches. If it does return matches (i.e. a re-export was added since spec 006 was drafted), update those wrappers in lockstep to the new 3-arg signature.

## CHANGELOG (`CHANGELOG.md`)

9. **Insert a new `## Unreleased` section above `## v0.17.1`** (which currently sits at line 7). Single bullet with `fix:` prefix, per `changelog-guide.md`:

   ```
   ## Unreleased

   - fix: extract token counts for gzip-encoded upstream responses. v0.17.0's extractor + v0.17.1's split-event widening both scanned text markers over raw response bytes; live trace of the primary production provider (`anthropic-subscription` via Cloudflare) revealed that Anthropic serves `Content-Encoding: gzip` on both JSON and SSE responses, so `usage` / `event: message_` never appear in the tail and 5/5 post-v0.17.1 200s still logged `in=- out=-`. Additionally, real non-streaming JSON responses reach ~500 KB gzipped — far beyond the previous 64 KB tail bound, and gzip is not self-synchronizing so a mid-stream tail is unrecoverable. Fix: grow `TailBufferBytes` from 64 KiB to 2 MiB (frozen constant, no YAML field), decode `Content-Encoding: gzip` (case-insensitive, whitespace-trimmed) before running the existing SSE/JSON scans, bound decompression at 8 MiB to defend against decompression bombs, and pass `Content-Encoding` from the reverse-proxied response through the extractor. Missing header and `identity` behave as no-ops; `br` / `deflate` / `zstd` / chained encodings log `in=- out=-` (deferred to a follow-up bug if observed in production). Corrupt or truncated gzip returns `noUsage` cleanly via the pre-existing `defer recover()` and empty-tail guard. The `Unwrap()` chain, the `[req]` line format, and the minimax + uncompressed-SSE paths are unchanged; the request path (including `Accept-Encoding`) is untouched. See [specs/in-progress/006-bug-tokens-gzip-decompress.md](specs/in-progress/006-bug-tokens-gzip-decompress.md).
   ```

   Do NOT bump the version number. Do NOT add a `feat:` sub-heading. Single bullet under `## Unreleased`.

## Failure-mode coverage checklist (from spec 006 § Failure Modes)

Every row in the spec's Failure Modes table must map to a requirement above. Verify:

| Spec Failure Mode row | Covered by |
|-----------------------|------------|
| `Content-Encoding: gzip` header + valid gzip body → extract usage | Requirement 2 (`gzip` branch) + Requirement 3 (call site) + Requirement 6a/6b tests |
| `Content-Encoding: gzip` header + truncated/corrupt body → `in=- out=-`, no panic | Requirement 2 (return nil on `gzip.NewReader` or `io.ReadAll` error) + Requirement 3 (empty-tail short-circuit) + Requirement 6f/6j tests |
| `Content-Encoding: gzip` + body > 2 MiB → same as truncated | Requirement 1 (2 MiB cap on tail buffer, gzip mid-stream is unrecoverable) + Requirement 6f test |
| No `Content-Encoding` header → identity path | Requirement 2 (empty case) + Requirement 6g test |
| `Content-Encoding: identity` → same as no header | Requirement 2 (identity case) + Requirement 6h test |
| `Content-Encoding: br` / `deflate` / `zstd` → `in=- out=-` | Requirement 2 (`default` returns nil) + Requirement 6i test |
| Chained encodings `gzip, br` → `in=- out=-` | Requirement 2 (`default` matches the chained string) + Requirement 6k test |

</requirements>

<constraints>
- **No request modification.** The proxy passes `Accept-Encoding` and every other request header through unchanged. The fix operates on the RESPONSE side only. (Explicit operator preference: "router stays a router, no request modification.")
- **`TailBufferBytes` is a frozen constant** at `2 << 20` (2 MiB, 2097152 bytes) — NO new YAML field, no env var, no CLI flag. Spec 006 Non-goals bans this.
- **`decodedTailCapBytes` is a frozen constant** at `8 << 20` (8 MiB) — same rationale.
- **`Unwrap()` chain unchanged.** Do not modify `(*usageRecorder).Unwrap`, `(*statusRecorder).Unwrap`, or the wrapping in `newUsageRecorder`. Existing `Unwrap` chain specs and SSE-flush regression specs continue to pass.
- **`[req]` log line format unchanged** — still `... in=<N> out=<N>` appended. No `model-router.go` change beyond the one call site.
- **`defer recover()` in `ExtractUsage` stays.** Decompression errors, malformed JSON, and panics inside the SSE scanner all still land on `noUsage`. Never abort the `[req]` log line on a parse failure.
- **Only `gzip` is supported in this spec.** `deflate` / `br` / `zstd` are explicitly deferred; they must log `in=- out=-` without attempting decompression.
- **Existing minimax + Anthropic split-event tests continue to pass.** The 2-arg → 3-arg call-site edit is mechanical; every existing assertion must still hold.
- **Anti-fake tokens vary across cases.** Every new test case uses DIFFERENT integer values for `input_tokens` and `output_tokens`. Do not reuse the token pairs claimed by the pre-existing specs (`42/128`, `7/3`, `1000/250`, `55/66`, `11/22`, `300/99`, `100/5`, `999`, `77`, `33/77`, `42/17`, `0/0`).
- **Zero is a real value.** Presence detection is unchanged: a present `"input_tokens":0` is reported as `"0"` (not `"-"`).
- **DoD compliance.** GoDoc on every new exported item (`TailBufferBytes` doc-update, `decodeIfEncoded` doc, `ExtractUsage` doc-update); GoDoc on the new `decodedTailCapBytes` const; Ginkgo/Gomega tests in `handler_test`; no `replace` / `exclude` in `go.mod`.
- **CHANGELOG entry uses `fix:` prefix** — this is a bug fix, not a feature. Single bullet under `## Unreleased`.
- **Do NOT commit.** dark-factory handles git.
</constraints>

<verification>

```bash
cd /workspace
make precommit
```
Must exit 0.

```bash
cd /workspace
go test ./pkg/handler/ -run TestSuite -ginkgo.v -ginkgo.focus "Content-Encoding: gzip" 2>&1 | tail -80
```
Expect: all new specs PASS with the varied token values asserted in requirement 6.

```bash
cd /workspace
go test ./pkg/handler/ -run TestSuite -ginkgo.v 2>&1 | tail -60
```
Expect: entire handler suite PASSes — no regression in tail-buffer, `Unwrap` chain, minimax JSON, single-event / multi-event `message_delta`, split-event `message_start` + `message_delta`, panic-safety, or reverse-proxy tee-reception specs. `SUCCESS!` line at the bottom.

```bash
grep -n 'TailBufferBytes = 2 << 20' /workspace/pkg/handler/usage-recorder.go
```
Expect exactly one match — the bumped constant.

```bash
grep -n 'func decodeIfEncoded' /workspace/pkg/handler/usage-recorder.go
```
Expect exactly one match — the new helper.

```bash
grep -n 'func ExtractUsage' /workspace/pkg/handler/usage-recorder.go
```
Expect exactly one match with three parameters: `tail []byte, contentType, contentEncoding string`.

```bash
grep -n 'ExtractUsage(' /workspace/pkg/handler/model-router.go
```
Expect one match passing three arguments including `rec.Header().Get("Content-Encoding")`.

```bash
grep -c 'ExtractUsage(' /workspace/pkg/handler/usage-recorder_test.go
```
Expect a count ≥ 29 (19 pre-existing + 10 new gzip cases from requirement 6a–6k, minus 6c/6d/6k if you chose different naming — but at least 27).

```bash
grep -n 'recover()' /workspace/pkg/handler/usage-recorder.go
```
Expect at least one match inside `ExtractUsage` — the top-level panic guard is preserved.

```bash
grep -n 'gzip.NewReader' /workspace/pkg/handler/usage-recorder.go
```
Expect one match — inside `decodeIfEncoded`.

```bash
grep -n 'compress/gzip' /workspace/pkg/handler/usage-recorder.go /workspace/pkg/handler/usage-recorder_test.go
```
Expect matches in BOTH files — the extractor imports it for decompression, the test imports it for fixture compression.

```bash
head -15 /workspace/CHANGELOG.md
```
Expect: `## Unreleased` heading appears above `## v0.17.1`, with a single `- fix: extract token counts for gzip-encoded upstream responses. ...` bullet.

```bash
grep -n 'func (u \*usageRecorder) Unwrap\|func (s \*statusRecorder) Unwrap' /workspace/pkg/handler/*.go
```
Expect both methods still present and unchanged.

```bash
grep -rn 'ExtractUsage' /workspace/pkg/handler/ /workspace/cmd/ /workspace/main.go 2>/dev/null | grep -v _test.go
```
Expect matches only in `usage-recorder.go` (declaration) and `model-router.go` (call site). No other production call sites.

</verification>
</content>
</invoke>
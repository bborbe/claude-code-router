---
status: completed
spec: [004-log-input-output-tokens]
summary: Added usageRecorder tee response-writer with bounded 64KB tail buffer and Unwrap chain, plus Ginkgo specs proving buffer semantics and the Flush/Hijack regression guard
execution_id: claude-code-router-log-tokens-exec-014-tee-response-writer-bounded-buffer
dark-factory-version: dev
created: "2026-06-30T20:00:00Z"
queued: "2026-06-30T19:59:21Z"
started: "2026-06-30T19:59:22Z"
completed: "2026-06-30T20:06:22Z"
---

<summary>
- A new response-writer wrapper tees every byte written to the upstream-facing response into a bounded tail buffer that retains only the last ≤ 64 KB written, so the terminal SSE `message_delta` event (always the last chunk of an Anthropic stream) survives for later usage extraction.
- The wrapper preserves the existing `http.NewResponseController` flush/hijack chain by implementing `Unwrap()` that returns the wrapped `statusRecorder` — SSE chunks continue to flush incrementally to the client with no added latency.
- Large (> 64 KB) responses evict older bytes as new bytes arrive (ring/tail semantics); the buffer never grows beyond the frozen 64 KB constant regardless of response size.
- The buffer is never logged or persisted; only the later extractor reads integer token counts out of it.
- New Ginkgo specs prove buffer semantics (last-bytes retention, overflow eviction, small-body passthrough) and the `Unwrap`/`Flush`/`Hijack` chain regression guard.
- No existing behavior changes: the wrapper is added to the package but not yet wired into `NewModelRouter` (wiring is prompt 3).
</summary>

<objective>
Establish the bounded-tail-buffer response-writer primitive and prove the load-bearing `Unwrap()` chain stays functional, in isolation from extraction and log wiring. This is the highest-regression-risk piece and the dependency for prompts 2 and 3.
</objective>

<context>
Read first:
- `/workspace/specs/in-progress/004-log-input-output-tokens.md` — full spec; Desired Behaviors 1, 5, 6; Constraints (Unwrap chain, bounded memory, no latency regression); Failure Modes rows "Tail buffer overflows" and "Unwrap() chain broken"; AC 6 and AC 7.
- `/workspace/pkg/handler/status-recorder.go` — the existing `statusRecorder` struct this tee writer wraps. Note the embedded `http.ResponseWriter`, the `status int` / `wroteHeader bool` fields, and the `Unwrap() http.ResponseWriter` method at the bottom that is load-bearing for SSE flush (documented in its doc comment).
- `/workspace/pkg/handler/model-router.go` — line ~92 shows `rec := &statusRecorder{ResponseWriter: w}`; the tee writer will wrap `rec` (prompt 3 does the wiring; this prompt only builds the type).
- `/workspace/pkg/handler/model-router_test.go` — the existing `flushTrackingWriter` struct (bottom of file) and the `Context("SSE flush passthrough (regression)")` spec are the pattern to mirror for the Unwrap/Flush chain test. Also note `captureStderr` helper and the `handler_test` package convention.
- `/workspace/pkg/handler/handler_suite_test.go` — Ginkgo suite entry (`RunSpecs`, `RegisterFailHandler`).
- `/workspace/docs/dod.md` — DoD: GoDoc on exported types/functions, `bborbe/errors` for error wrapping, `glog.V(n).Infof` (no `fmt.Printf`), Ginkgo/Gomega tests next to the code, no `replace`/`exclude` in `go.mod`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-http-handler-refactoring-guide.md` — response-writer wrapper + `Unwrap()`/`http.NewResponseController` patterns.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — glog verbosity conventions (the `[req]` line this enables stays at `V(1)`).
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega test structure.

<!-- OPEN QUESTION (resolved by the implementer, noted here per spec): The spec says "ring/tail semantics" for the buffer but leaves the concrete type to the implementer. Acceptable choices: (a) a fixed-size `[]byte` ring buffer with start/length indices that overwrites oldest bytes on overflow, or (b) a sliding window that keeps a `[]byte` and trims the front when it exceeds the bound. Both satisfy "retain the last ≤ 64 KB written". Pick whichever is simpler to reason about and test; the extractor (prompt 2) only calls a method that returns the current tail contents as `[]byte`. -->
</context>

<requirements>

1. **Create `/workspace/pkg/handler/usage-recorder.go`** containing a new response-writer wrapper type. Name the file `usage-recorder.go` (the extractor lands in the same file in prompt 2; the test file `usage-recorder_test.go` is created in step 5). Package `handler`.

   Define a frozen constant for the tail-buffer bound:

   ```go
   // TailBufferBytes caps the number of response-body bytes retained for
   // post-request usage extraction. The terminal Anthropic SSE
   // `message_delta` event (which carries usage) is always the last chunk
   // of a stream, and non-streaming JSON usage bodies fit comfortably
   // within this bound; a full 5 MB streaming response therefore occupies
   // at most TailBufferBytes of additional memory. This is a frozen
   // constant, NOT a config field — see spec 004 Non-goals.
   const TailBufferBytes = 64 << 10 // 64 KB
   ```

2. **Define the tee response-writer type.** It wraps a `*statusRecorder` (so the status-capture behavior and the existing `Unwrap()` chain are preserved) and holds the bounded tail buffer:

   ```go
   // usageRecorder wraps a *statusRecorder and tees every byte written to
   // the response into a bounded tail buffer (≤ TailBufferBytes) that
   // retains the LAST bytes written. The buffer is read by
   // extractUsage (see prompt 2) after the upstream handler returns, to
   // pull input/output token counts out of the terminal SSE
   // `message_delta` event or the non-streaming JSON `usage` object.
   //
   // The write-through path is unchanged: every Write call writes to the
   // underlying statusRecorder first and copies to the buffer as a side
   // effect, so SSE chunks flush to the client at the same cadence as
   // before (no added buffering latency). The buffer never holds more
   // than TailBufferBytes; older bytes are evicted as new bytes arrive.
   //
   // Unwrap returns the wrapped *statusRecorder so
   // http.NewResponseController reaches the underlying Flusher / Hijacker
   // through the existing statusRecorder.Unwrap() chain. Breaking this
   // chain regresses SSE flush (Claude Code spinners "stuck" mid-stream)
   // — see status-recorder.go doc comment and the spec's "Unwrap() chain
   // must stay functional" constraint.
   type usageRecorder struct {
       rec    *statusRecorder
       // tail holds the last ≤ TailBufferBytes bytes written.
       // Concrete representation is the implementer's choice (ring buffer
       // or sliding window); see the open-question note in the spec context.
       tail   // implemented in step 3
   }
   ```

3. **Implement the tail buffer.** Provide a constructor `newUsageRecorder(w http.ResponseWriter) *usageRecorder` that wraps `&statusRecorder{ResponseWriter: w}`. Implement the tail storage so that:
   - It starts empty.
   - Each `Write` appends bytes and, if the total would exceed `TailBufferBytes`, evicts the oldest bytes so the stored content is always the last `≤ TailBufferBytes` bytes written (never grows beyond the bound).
   - A method `Tail() []byte` returns the current retained bytes (the extractor in prompt 2 calls this). The returned slice must be safe to read after the handler returns; it must not alias internal storage that a subsequent `Write` would mutate (copy on return, or document that the buffer is quiescent after the handler returns — the latter is true because extraction happens after `target.ServeHTTP` returns, before which no further `Write` occurs).

   Whichever concrete type you pick (ring buffer with start/length indices, or a `[]byte` that trims the front on overflow), the observable contract is: after `N` bytes are written where `N > TailBufferBytes`, `Tail()` returns the last `TailBufferBytes` bytes in write order.

4. **Implement the `http.ResponseWriter` methods on `usageRecorder`:**

   - `WriteHeader(code int)` — delegate to `rec.WriteHeader(code)`. The tee does not need to capture the status (the `statusRecorder` already does).
   - `Write(b []byte) (int, error)` — write through to `rec.Write(b)` first; on success, copy `b` into the tail buffer. Return the count and error from `rec.Write`. If `rec.Write` returns an error, do NOT copy to the buffer (the write-through to the client is the primary path; the buffer is best-effort). If `rec.Write` succeeds partially (returns `n < len(b)` with no error, or `n` with an error), copy only the first `n` bytes that were actually written to the client.
   - `Unwrap() http.ResponseWriter` — return `rec` (the `*statusRecorder`). This preserves the existing chain: `http.NewResponseController(usageRecorder).Unwrap()` → `*statusRecorder` → `statusRecorder.Unwrap()` → underlying `http.ResponseWriter`. Do NOT skip the `statusRecorder` (its `Write`/`WriteHeader` overrides must stay in the path).

   Add GoDoc comments per DoD. Use `bborbe/errors` (`bberrors "github.com/bborbe/errors"`) only if you wrap an error; if `rec.Write` returns an error directly, returning it unwrapped is acceptable (this is a passthrough, not a new error condition).

5. **Create `/workspace/pkg/handler/usage-recorder_test.go`** (package `handler_test`, mirroring `model-router_test.go`) with a `Describe("usageRecorder tail buffer", ...)` block containing these Ginkgo specs:

   - **It("retains the last bytes written when total is under the bound")** — write `[]byte("hello world")` (11 bytes) to a `usageRecorder` wrapping a `*statusRecorder` wrapping an `httptest.ResponseRecorder`. Assert `Tail()` returns `[]byte("hello world")` and the underlying recorder received the same bytes (write-through).

   - **It("evicts oldest bytes and retains the tail when writes exceed the bound")** — write `TailBufferBytes + 100` bytes of distinct, predictable content (e.g. fill with a repeating non-constant pattern so a truncated copy is detectable — see step 6 anti-fake requirement). Assert `len(Tail()) == TailBufferBytes` and the retained bytes equal the last `TailBufferBytes` bytes written. Assert the underlying recorder received ALL bytes (the bound only limits the buffer, not the client).

   - **It("retains the terminal chunk after a sequence of overflow writes")** — model the SSE scenario: write `TailBufferBytes - 50` bytes of filler, then write a final 80-byte "terminal event" chunk. Assert `Tail()` contains the full terminal chunk at its end (the terminal chunk fits because it's small and arrives last). This is the core precondition prompt 2 relies on.

   - **It("never grows beyond TailBufferBytes regardless of total written")** — write 5 MB of filler in `TailBufferBytes/4`-sized chunks. After each chunk assert `len(Tail()) <= TailBufferBytes`. Final assertion: `len(Tail()) == TailBufferBytes`.

   - **It("Write returns the count and error from the underlying writer")** — wrap a writer whose `Write` returns an error after K bytes; assert `usageRecorder.Write` propagates that error and that count, and that the buffer copied only the successfully-written prefix.

6. **Add a `Context("Unwrap chain", ...)` block** (same file) with the load-bearing `Unwrap`/`Flush`/`Hijack` regression specs — this is AC 7:

   - **It("http.NewResponseController(usageRecorder).Flush() reaches the underlying Flusher")** — build a `usageRecorder` wrapping a `*statusRecorder` wrapping a `flushTrackingWriter` (reuse the struct already defined in `model-router_test.go`; it is in the same `handler_test` package so it is visible). Call `http.NewResponseController(ur).Flush()` and assert it returns no error and `spy.flushed > 0`. This mirrors the existing `Context("SSE flush passthrough (regression)")` spec in `model-router_test.go` but adds the extra `usageRecorder` wrapper layer — if `usageRecorder.Unwrap` is missing or returns the wrong target, the flush fails to reach the spy.

   - **It("http.NewResponseController(usageRecorder).Hijack() resolves through the chain")** — define a small `hijackTrackingWriter` (in the test file) that embeds `*httptest.ResponseRecorder` and implements `http.Hijacker` with a `Hijack() (net.Conn, *bufio.ReadWriter, error)` that returns a sentinel non-nil conn + nil error (use `net.Pipe()` or a no-op conn). Build `usageRecorder` → `*statusRecorder` → `hijackTrackingWriter`. Assert `http.NewResponseController(ur).Hijack()` returns no error and a non-nil conn. If `Unwrap` is broken, `NewResponseController` cannot reach the `Hijacker` and returns an error.

   - **It("WriteHeader and Write delegate to the underlying statusRecorder")** — assert that calling `ur.WriteHeader(http.StatusTeapot)` sets the `statusRecorder`'s captured status (expose via the existing test — note `statusRecorder.status` is unexported; either add an export in `export_test.go` like `var StatusRecorderStatus = func(s *statusRecorder) int { return s.status }` OR assert via the underlying `httptest.ResponseRecorder.Result().StatusCode`). Prefer the `httptest.ResponseRecorder.Result().StatusCode` path to avoid touching `export_test.go`.

7. **Anti-fake requirement (load-bearing — from spec ACs):** the test data for the overflow/eviction specs MUST NOT be a single repeated byte or a constant fill that a buggy truncation could pass by accident. Use a pattern where each chunk's content is distinguishable (e.g. byte `i` set to `byte(i % 256)` across the full write stream, or sequential ASCII chunks like `"AAAA..."`, `"BBBB..."`). A hardcoded `make([]byte, TailBufferBytes)` zero-fill implementation must FAIL the eviction spec. Quote this in the test as a comment near the eviction spec.

8. **Run `make precommit`** in the repo root. Fix any lint / format / addlicense issues. The new type is not yet wired into `NewModelRouter` (that is prompt 3) — the specs in this prompt test `usageRecorder` directly, not through the router.

</requirements>

<constraints>
- **`Unwrap()` chain must stay functional (from spec).** `usageRecorder.Unwrap()` MUST return the wrapped `*statusRecorder`; the existing `statusRecorder.Unwrap()` MUST remain the next hop. Do NOT remove or alter `statusRecorder.Unwrap()`. This is the single highest-priority regression guard.
- **Bounded memory: the tail buffer is a fixed-size constant (≤ 64 KB) (from spec).** `TailBufferBytes` is a frozen `const` in package `handler`, NOT a config field. No tunable buffer size, no opt-out flag, no `log_tokens` toggle.
- **No latency regression (from spec).** The write-through path writes to the underlying writer first; the buffer copy is a side effect. No per-byte parsing during streaming. Extraction (prompt 2) runs once after the handler returns — not in this prompt.
- **Do NOT wire into `NewModelRouter` in this prompt.** Wiring is prompt 3. This prompt only adds the type + its direct unit specs.
- **No new YAML fields (from spec).** `docs/config.md` and `docs/config.example.yaml` are unchanged.
- **DoD compliance (from spec):** GoDoc on exported `usageRecorder` + `TailBufferBytes` + `newUsageRecorder` (if exported); `bborbe/errors` only if error wrapping is needed; `glog.V(n).Infof` (no `fmt.Printf`/`println` — this prompt adds no logging, the `[req]` line is prompt 3); Ginkgo/Gomega in `pkg/handler/` next to the code; no `replace`/`exclude` in `go.mod`.
- **Do NOT commit.** dark-factory handles git.
- **Existing tests must still pass.** No existing file's behavior changes; the new type is additive.
</constraints>

<verification>
```bash
cd /workspace
make precommit
```
Must exit 0.

```bash
cd /workspace
go test ./pkg/handler/ -run TestSuite -ginkgo.v 2>&1 | tail -60
```
Expect: all existing specs PASS plus the new `usageRecorder tail buffer` and `Unwrap chain` specs PASS.

Confirm the `Unwrap` chain has two hops (usageRecorder → statusRecorder → underlying):
```bash
grep -n 'func (u \*usageRecorder) Unwrap\|func (s \*statusRecorder) Unwrap' /workspace/pkg/handler/*.go
```
Expect both methods present; `statusRecorder.Unwrap` unchanged from its current form.
</verification>

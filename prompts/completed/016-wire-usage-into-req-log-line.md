---
status: completed
spec: [004-log-input-output-tokens]
summary: Wired usageRecorder tee writer and ExtractUsage into NewModelRouter, appending in=/out= to both [req] log variants, with format-preservation Ginkgo specs and CHANGELOG entry
execution_id: claude-code-router-log-tokens-exec-016-wire-usage-into-req-log-line
dark-factory-version: dev
created: "2026-06-30T20:02:00Z"
queued: "2026-06-30T22:43:57Z"
started: "2026-06-30T22:43:58Z"
completed: "2026-06-30T22:46:53Z"
---

<summary>
- The `[req]` access log line emitted per request now appends `in=<N> out=<N>` (input/output token counts) at the END of the existing line, for 200 responses where usage was extracted, and `in=- out=-` for 200 responses with no parseable usage and for all non-200 responses.
- Both the alias variant (`... alias=... provider=... status=... latency=...`) and the non-alias variant (`... model=... provider=... status=... latency=...`) get the appended fields, with the existing field order and key=value style preserved.
- The bounded-tail-buffer tee writer from prompt 1 is wired into `NewModelRouter` so every upstream response body is teed; the extractor from prompt 2 runs once after the handler returns.
- The SSE flush path is unchanged — the `Unwrap()` chain stays functional; sampling logic (200-sampling gate) and `V(1)` gating are preserved; `in=/out=` ride on the same line with the same gating.
- A CHANGELOG.md `## Unreleased` entry is added; a format-preservation Ginkgo spec asserts the existing field order with the appended `in=/out=`; live smoke-test instructions are documented for post-merge verification.
</summary>

<objective>
Wire the tee writer (prompt 1) and usage extractor (prompt 2) into the existing `[req]` log emission site in `NewModelRouter`, appending `in=/out=` to both log variants, and add the format-preservation regression test + CHANGELOG entry. This is the thin final wiring layer.
</objective>

<context>
Read first:
- `/workspace/specs/in-progress/004-log-input-output-tokens.md` — Desired Behavior 3 and 7; Constraints (sampler logic unchanged, `[req]` at `V(1)`, field order preserved, `in=/out=` appended at end); Failure Modes rows "non-200 with no usage", "context cancelled mid-SSE"; AC 1, AC 8, AC 9.
- `/workspace/pkg/handler/model-router.go` — the exact log emission site. The alias variant is at the `if aliasResolved != ""` branch (currently `glog.V(1).Infof("[req] %s %s model=%s alias=%s provider=%s status=%d latency=%s", ...)`); the non-alias variant follows it (`glog.V(1).Infof("[req] %s %s model=%s provider=%s status=%d latency=%s", ...)`). The sampler gate is `if status == http.StatusOK && !sampler.IsSample() { return }` immediately above. The `statusRecorder` is constructed at `rec := &statusRecorder{ResponseWriter: w}` and `target.ServeHTTP(rec, r)` dispatches the upstream.
- `/workspace/pkg/handler/usage-recorder.go` — created by prompts 1+2. Contains `usageRecorder`, `newUsageRecorder(w)`, `Tail()`, `extractUsage(tail []byte, contentType string) TokenUsage`, `TokenUsage.logLineValue() (in, out string)`, `noUsage`.
- `/workspace/pkg/handler/usage-recorder_test.go` — created by prompts 1+2. ADD the format-preservation + wiring specs to this file (or `model-router_test.go` for the end-to-end log specs — either is fine; the existing `[req]` specs live in `model-router_test.go`, so prefer ADDING the wiring specs there to keep them next to the existing log-line assertions, and keep the pure-extractor specs in `usage-recorder_test.go`).
- `/workspace/pkg/handler/model-router_test.go` — the existing `Context("structured request log")` block with `captureStderr`, `alwaysSample`, and the existing `[req]` MatchRegexp assertions. Mirror this pattern. Note `flushTrackingWriter` at the bottom of the file.
- `/workspace/pkg/handler/status-recorder.go` — `Unwrap()` chain (do NOT modify).
- `/workspace/CHANGELOG.md` — the top section currently starts with `## v0.16.0`. Insert `## Unreleased` ABOVE `## v0.16.0` (per `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`: feature branches use `## Unreleased`, never a version number; newest first, directly above the highest `## vX.Y.Z`).
- `/workspace/docs/dod.md` — DoD: GoDoc, `glog.V(n).Infof` (the `[req]` line stays at `V(1)`), CHANGELOG `## Unreleased` entry, no `replace`/`exclude`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` placement + conventional-prefix bullet rules.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — `glog.V(1).Infof` conventions.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-http-handler-refactoring-guide.md` — response-writer wrapper wiring.
</context>

<requirements>

1. **Wire the tee writer into `NewModelRouter`.** In `/workspace/pkg/handler/model-router.go`, replace the `statusRecorder` construction + dispatch so the upstream writes through the `usageRecorder` (which wraps the `statusRecorder`):

   Old (around line 92 and 147):
   ```go
   rec := &statusRecorder{ResponseWriter: w}
   // ...
   target.ServeHTTP(rec, r)
   ```

   New:
   ```go
   rec := &statusRecorder{ResponseWriter: w}
   ur := newUsageRecorder(rec)
   // ...
   target.ServeHTTP(ur, r)
   ```

   Then `status := rec.status` continues to read from the `statusRecorder` (the `usageRecorder` delegates `WriteHeader` to `rec`, so `rec.status` is still populated). The `ur` variable is in scope for the extraction call in step 3. Keep `rec` as the named variable the rest of the function already uses for status.

   IMPORTANT: do NOT change the `status` read (`status := rec.status; if status == 0 { status = http.StatusOK }`) — the status capture is unchanged.

2. **Extract usage after the handler returns, before the log line.** Compute the usage once after the sampler gate but before the two log branches — OR inside each branch. The cleanest placement is right after `latency` is computed and `metrics.ObserveRequest` is called, computing a `usage := ...` that both branches consume. The extraction must run regardless of sampler outcome ONLY IF you place it before the sampler-return; but the log line only emits when the sampler allows it (200) or always (non-200). To avoid extracting on suppressed 200s (wasted work), place the extraction AFTER the sampler gate, right before the `if aliasResolved != ""` branch:

   ```go
   // After the sampler gate (so suppressed 200s skip the extraction scan):
   if status == http.StatusOK && !sampler.IsSample() {
       return
   }
   usage := noUsage
   if status == http.StatusOK {
       usage = extractUsage(ur.Tail(), rec.Header().Get("Content-Type"))
   }
   in, out := usage.logLineValue()
   ```

   Rationale: non-200 paths always log `in=- out=-` (no extraction needed — the spec's Failure Modes row "non-200 with no usage" mandates `in=- out=-`); 200 paths extract from the tail. The `rec.Header().Get("Content-Type")` is the response's Content-Type as set by the upstream (the `statusRecorder` embeds `http.ResponseWriter`, so `Header()` is the live response header map). This is the boundary the extractor crosses: the real `Content-Type` the upstream set.

   NOTE: if `extractUsage` panics despite prompt 2's `defer recover()` guard, the request log path must not crash — but prompt 2's guard already ensures `extractUsage` cannot panic. Do not add a second recover here; trust the extractor's contract.

3. **Append `in=/out=` to BOTH log variants.** Modify the two `glog.V(1).Infof` calls to append `in=%s out=%s` and pass `in, out`:

   Alias variant:
   ```go
   glog.V(1).Infof(
       "[req] %s %s model=%s alias=%s provider=%s status=%d latency=%s in=%s out=%s",
       r.Method, r.URL.Path, origModel, aliasResolved, providerName, status, latency, in, out,
   )
   ```

   Non-alias variant:
   ```go
   glog.V(1).Infof(
       "[req] %s %s model=%s provider=%s status=%d latency=%s in=%s out=%s",
       r.Method, r.URL.Path, origModel, providerName, status, latency, in, out,
   )
   ```

   The `in=/out=` fields go at the END of the existing line, after `latency=...`. The existing field order and key=value style are preserved. Both branches already `return` after their `Infof`, so no control-flow change is needed.

4. **Add the format-preservation + token-append Ginkgo specs** to `/workspace/pkg/handler/model-router_test.go` in a new `Context("token usage in [req] line", ...)` block (under the existing `Context("structured request log")` if you want it grouped, or as a sibling). Use the existing `captureStderr` + `alwaysSample` + `httptest` upstream-handler pattern. The upstream handlers in these specs MUST emit realistic Anthropic-shaped response bodies and set `Content-Type`. ALL upstream token numbers MUST be varied across cases. **Quote this requirement in a comment above the block:** "Anti-fake: upstream token numbers are varied across all cases — a hardcoded append must fail these specs (spec 004 AC 8)."

   Specs:

   - **It("appends in=<N> out=<N> for an SSE 200 response matching upstream usage")** — upstream handler sets `Content-Type: text/event-stream`, writes a multi-event SSE stream whose terminal `event: message_delta` carries `{"usage":{"input_tokens":42,"output_tokens":17}}`. Assert the captured `[req]` line contains `in=42 out=17` at the END of the line. Parse the line with a regex capturing the `in=` and `out=` values and assert they equal `"42"`/`"17"` (NOT a hardcoded substring match — vary the numbers so a constant append fails).

   - **It("appends in=<N> out=<N> for a non-streaming JSON 200 response")** — upstream handler sets `Content-Type: application/json`, writes `{"id":"msg_01","usage":{"input_tokens":100,"output_tokens":5}}`. Assert the captured `[req]` line contains `in=100 out=5` at the end. Use DIFFERENT numbers from the SSE case.

   - **It("appends in=- out=- for a 200 response with no parseable usage")** — upstream handler sets `Content-Type: application/json`, writes `{"ok":true}` (no `usage`). Assert the captured `[req]` line contains `in=- out=-` at the end.

   - **It("appends in=- out=- for a non-200 (502) response")** — upstream handler writes `502` with no usage body. Assert the captured `[req]` line contains `status=502` AND `in=- out=-`. Use `never := liblog.SamplerFunc(func() bool { return false })` is NOT needed here — non-200 always logs regardless of sampler; mirror the existing "always logs non-200 even when the sampler returns false" spec's setup.

   - **It("appends in=/out= for an alias-hit 200 SSE response")** — set up aliases so `alias=...` is present, upstream emits SSE with usage `{"input_tokens":7,"output_tokens":3}` (distinct numbers). Assert the captured line matches the alias-variant field order: `[req] POST /v1/messages model=... alias=... provider=... status=200 latency=... in=7 out=3`. This is AC 8's alias-variant requirement.

   - **It("preserves the existing [req] field order with in=/out= appended at the end")** — the format-preservation regression (AC 8). For a 200 SSE response, assert the captured line matches the regex `\[req\] POST /v1/messages model=\S+ provider=\S+ status=200 latency=\d+m?s in=\d+ out=\d+` (non-alias) — i.e. `in=/out=` come AFTER `latency=` and the prior fields are unchanged. For the alias variant assert `... alias=\S+ ... latency=... in=\d+ out=\d+`. This is the spec's "field order unchanged except for the appended `in=/out=`" AC.

   - **It("does not extract usage on a suppressed 200 (sampler returns false)")** — use `never := liblog.SamplerFunc(func() bool { return false })`; a 200 SSE response with usage. Assert NO `[req]` line is emitted (the sampler suppresses the whole line, including `in=/out=`). This preserves the sampler-gating constraint — `in=/out=` ride on the same line with the same gating, they are NOT emitted independently.

5. **Add the CHANGELOG entry.** In `/workspace/CHANGELOG.md`, insert a new `## Unreleased` section directly ABOVE `## v0.16.0` (do NOT modify the `# Changelog` header block or the SemVer preamble). Add one bullet with the `feat:` conventional prefix:

   ```markdown
   ## Unreleased

   - feat: append `in=<N> out=<N>` (input/output token counts) to the `[req]` access log line for 200 responses, sourced from the upstream Anthropic response body (SSE terminal `message_delta` event or non-streaming JSON `usage`). Error/no-usage paths emit `in=- out=-`. The counts are captured via a bounded ≤ 64 KB tail buffer teed off the response writer; the SSE flush `Unwrap()` chain, the `V(1)` log gating, and the 200-sampling gate are unchanged. No new YAML fields.
   ```

   Per the changelog guide: feature branch uses `## Unreleased`, never a version number. Do NOT bump `## v0.16.0` or add a `## v0.17.0`.

6. **Document the live smoke test (AC 9).** Add a short comment block at the top of the `Context("token usage in [req] line", ...)` test block (or in the CHANGELOG bullet) with the manual verification steps:

   ```
   // Live smoke test (AC 9, manual — not automated):
   //   go install github.com/bborbe/claude-code-router@latest
   //   <run router with a configured provider>
   //   <one real /v1/messages SSE request through the router>
   //   grep '\[req\]' /tmp/claude-code-router.log | tail -1
   //   -> expect "... in=<N> out=<N>" where both <N> are integers >= 1
   //      matching the usage field in the provider's response
   //      (verify by diffing against a curl of the same prompt).
   ```

   This is documentation, not an automated spec — AC 9 is explicitly manual.

7. **Run `make precommit`** in the repo root. Fix any lint / format / addlicense issues.

8. **Sibling entry-point check (Go):** this spec touches `NewModelRouter`'s body but NOT its signature (no new parameter) and NOT any `factory.Create*` call site. Verify no other caller constructs a `statusRecorder` directly that would bypass the new tee:

   ```bash
   grep -rn 'statusRecorder{' /workspace/pkg/ /workspace/main.go
   ```

   Expect exactly the one site in `model-router.go` (the one this prompt modifies). If another site exists, it is out-of-scope for this spec (the spec's Non-goals say "touches only the response-capture and log-emission tail of the request flow") — note it as a follow-up in the CHANGELOG bullet's comment but do NOT modify it.

</requirements>

<constraints>
- **Sampler logic unchanged (from spec).** The `if status == http.StatusOK && !sampler.IsSample() { return }` gate is NOT bypassed or modified. `in=/out=` ride on the same line with the same gating; suppressed 200s emit no line at all (no independent `in=/out=` emission).
- **`[req]` line stays at `V(1)` (from spec).** Both log variants remain `glog.V(1).Infof(...)`. No bare `glog.Info`. No `fmt.Printf`/`println`.
- **Field order preserved (from spec).** `in=/out=` are appended at the END, after `latency=...`. The existing fields (`method`, `path`, `model=`, `alias=`, `provider=`, `status=`, `latency=`) keep their order and key=value style.
- **`Unwrap()` chain must stay functional (from spec).** Wiring the `usageRecorder` MUST NOT break the chain. `usageRecorder.Unwrap()` → `*statusRecorder` → `statusRecorder.Unwrap()` → underlying writer. Do NOT modify `statusRecorder.Unwrap()`. The existing `Context("SSE flush passthrough (regression)")` spec must still pass (it now exercises the `usageRecorder` layer too, since the router dispatches through `ur`).
- **Bounded ≤ 64 KB tail buffer is a frozen constant (from spec).** NOT a config field. No `log_tokens` toggle.
- **No new YAML fields (from spec).** `docs/config.md` and `docs/config.example.yaml` are unchanged.
- **Non-200 paths emit `in=- out=-` (from spec Failure Modes).** No extraction scan on non-200 (wasted work); `usage` stays `noUsage`, rendering `in=- out=-`.
- **Anti-fake (load-bearing — from spec AC 8):** upstream token numbers MUST vary across ALL test cases so a hardcoded append fails the specs. Quote this in a comment above the test block.
- **CHANGELOG (from spec DoD):** `## Unreleased` entry above `## v0.16.0` with a `feat:` prefix bullet.
- **DoD compliance (from spec):** GoDoc on any newly exported symbols (none expected in this prompt — wiring reuses prompt 1/2 exports); `glog.V(1).Infof`; Ginkgo/Gomega in `pkg/handler/`; no `replace`/`exclude` in `go.mod`.
- **Do NOT commit.** dark-factory handles git.
- **Existing tests must still pass.** All existing `model-router_test.go` specs (including the SSE-flush regression spec and the sampler-gating specs) continue to pass. The existing `[req]` MatchRegexp assertions may need updating IF they asserted the line ENDS at `latency=...` — check and update them to allow the appended `in=/out=` (use `MatchRegexp` with the `in=` suffix OR loosen the existing assertions to `ContainSubstring` if they currently assert end-of-line). Do NOT weaken assertions that check the prefix field order.
</constraints>

<verification>
```bash
cd /workspace
make precommit
```
Must exit 0.

```bash
cd /workspace
go test ./pkg/handler/ -run TestSuite -ginkgo.v 2>&1 | tail -80
```
Expect: ALL existing specs PASS (including the SSE-flush regression spec, which now exercises the `usageRecorder` layer) PLUS the new `Context("token usage in [req] line")` specs PASS.

Confirm the `[req]` line now appends `in=/out=`:
```bash
grep -n 'in=%s out=%s' /workspace/pkg/handler/model-router.go
```
Expect exactly two matches (alias variant + non-alias variant).

Confirm the `Unwrap` chain is intact (now three hops visible: usageRecorder + statusRecorder):
```bash
grep -n 'func (u \*usageRecorder) Unwrap\|func (s \*statusRecorder) Unwrap' /workspace/pkg/handler/*.go
```
Expect both methods present; `statusRecorder.Unwrap` unchanged.

Confirm the sampler gate is unchanged:
```bash
grep -n 'sampler.IsSample()' /workspace/pkg/handler/model-router.go
```
Expect exactly one match (the existing 200-sampling gate).

Confirm the CHANGELOG has a new `## Unreleased` above `## v0.16.0`:
```bash
grep -n '## Unreleased\|## v0.16.0' /workspace/CHANGELOG.md
```
Expect `## Unreleased` to appear BEFORE `## v0.16.0` (lower line number).

Live smoke test (AC 9, manual — run after merge, not in CI):
```bash
go install github.com/bborbe/claude-code-router@latest
# <run router with a configured provider, make one /v1/messages SSE request>
grep '\[req\]' /tmp/claude-code-router.log | tail -1
# expect: "... in=<N> out=<N>" with both <N> integers >= 1
```
</verification>

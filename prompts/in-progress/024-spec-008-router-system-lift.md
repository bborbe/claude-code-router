---
status: approved
spec: ["008"]
created: "2026-08-15T10:40:00Z"
queued: "2026-08-15T10:45:24Z"
---

<summary>
- When a request is routed to a model the operator flagged as "rejects a system message that isn't first", the router moves every out-of-place system message into the request's dedicated top-level system block before forwarding.
- The moved content keeps its original order, nothing is dropped, merged, deduplicated or summarised, and the remaining conversation entries stay in their original relative order.
- System content written as a plain string becomes a single text block; content already written as a list of blocks is appended block for block.
- A system message that is already the first entry is left exactly where it is and is not copied anywhere.
- Requests to models the operator did not flag, and requests that have nothing out of place, reach the upstream as the exact same bytes the client sent.
- If the body is shaped in a way the transform cannot interpret, the request is forwarded untouched with a single warning — it is never turned into a router-generated error.
- One detail log line records that the transform fired, naming the model and how many entries moved, and never containing any system-message text.
- Test coverage exercises both the transform in isolation and the full router path against a capturing test upstream, including hostile body shapes.
</summary>

<objective>
Teach the model router to lift non-leading system-role messages into the top-level system block for models matching a route's `requiresLeadingSystem` patterns, preserving order and content, degrading to a byte-identical forward on any body shape it cannot interpret. Handler-package only — the factory that populates the patterns from config lands in prompt 3.
</objective>

<context>
Read first:
- `/workspace/specs/in-progress/008-requires-leading-system-message.md` — Desired Behavior 3, 4, 5, 6; Acceptance Criteria 3, 4, 5, 6, 7, 8, 9; Constraints (byte fidelity on the no-transform path, ordering is content-preserving, Content-Length correctness, no regression of alias / `[1m]` / glob routing / fallback / metrics / trace / body-size limit); Failure Modes rows for unparsable body, no-misplaced-entry, string-form top-level system field, hostile shapes; Security section (log hygiene: model name and count only, never content).
- `/workspace/pkg/handler/model-router.go` — the whole file. Anchors:
  - `type ModelRoute struct { Pattern string; ProviderName string; Handler http.Handler }` — gains one field in this prompt.
  - `func NewModelRouter(routes []ModelRoute, defaultProviderName string, defaultHandler http.Handler, aliases map[string]string, sampler liblog.Sampler, metrics *Metrics, currentDateTime libtime.CurrentDateTimeGetter) http.Handler` — signature is FROZEN in this prompt (≈40 call sites in tests); the new data rides on `ModelRoute`.
  - `body, err := io.ReadAll(r.Body)` and the `r.Body = io.NopCloser(bytes.NewReader(body))` / `r.ContentLength = int64(len(body))` replay idiom — reuse it verbatim for the transformed body.
  - the alias branch (`if resolved, ok := aliases[model]; ok && model != ""`) with its `glog.V(2).Infof("[alias] %s -> %s", model, resolved)` detail line.
  - the `[1m]` branch with `glog.V(2).Infof("[1m-strip] %s -> %s", model, cleaned)`.
  - the route-match loop `for _, route := range routes { ok, _ := path.Match(route.Pattern, model); ... }` followed by `target.ServeHTTP(ur, r)` — the transform goes between those two.
  - `func rewriteModelField(ctx context.Context, body []byte, resolved string) ([]byte, error)` — the existing `map[string]json.RawMessage` round-trip idiom the new transform mirrors.
  - stdlib `path` and `encoding/json` are already imported; `bberrors "github.com/bborbe/errors"` is the local alias for the bborbe errors package (plain `errors` is the stdlib one in this file).
- `/workspace/pkg/handler/model-router_test.go` — existing Ginkgo patterns to mirror: `alwaysSample`, `testMetrics`, `testDateTime`, `labelHandler`, `captureStderr(fn)` (these ARE package-level), and the capturing-upstream idiom.
  **`post(body string) *http.Request` is NOT package-level** — it is a closure declared inside `Describe("ModelRouter")` at `:97` and re-declared inside `Describe("ModelRouter metrics wiring")` at `:962`. A new sibling top-level `Describe` cannot see either. Declare your own local `post := func(body string) *http.Request {...}` inside the new Describe, mirroring `:962`. Referencing it directly is a compile error.
  ```go
  capturing := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
      capturedBody, _ = io.ReadAll(r.Body)
      capturedContentLength = r.ContentLength
  })
  ```
  (see `Context("alias resolution")`), and the `flag.Set("logtostderr", "true")` + `flag.Set("v", "2")` BeforeEach used by `Context("structured request log")` to make V(2) lines visible to `captureStderr`.
- `/workspace/pkg/handler/export_test.go` — the re-export pattern for unexported symbols (`var TraceTTLFromEnv = traceTTLFromEnv`, `func NewUsageRecorder(...)`). The new transform gets one entry here.
- `/workspace/docs/dod.md` — GoDoc on exported items, `bborbe/errors` conventions, `glog.V(n)` instead of `fmt.Printf`, Ginkgo/Gomega coverage ≥ 80% on changed packages.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-http-handler-refactoring-guide.md` — request-body replay and handler-wiring conventions.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `bberrors.Wrapf(ctx, err, ...)` / `bberrors.New(ctx, msg)`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo spec structure and table-driven specs.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — `glog.V(2).Infof` vs `glog.Warningf` levels.

**Dependency guard (fail-fast at prompt start):** verify prompt 1 landed:

```bash
grep -q 'RequiresLeadingSystem \[\]string `yaml:"requiresLeadingSystem,omitempty"`' /workspace/pkg/config.go && \
grep -q 'invalid requiresLeadingSystem glob' /workspace/pkg/config.go
```

If either grep fails, STOP and report `dependency not yet deployed: prompt 1 (config requiresLeadingSystem) has not landed`. Do not add the config field here — that duplicates prompt 1 and creates a merge conflict.
</context>

<requirements>

1. **Create `/workspace/pkg/handler/system-lift.go`** with the standard 3-line BSD copyright header used by every file in this package, `package handler`, and imports `bytes`, `context`, `encoding/json`, `path`, and `bberrors "github.com/bborbe/errors"`.

   Contents:

   ```go
   // systemTextBlock is the normalized shape a plain-string system
   // content value is converted into when lifted into the top-level
   // system block. Field order is load-bearing: json.Marshal emits
   // struct fields in declaration order, so a lifted string "hello"
   // renders as exactly {"type":"text","text":"hello"}.
   type systemTextBlock struct {
       Type string `json:"type"`
       Text string `json:"text"`
   }

   // matchesAnyPattern reports whether model matches at least one of the
   // glob patterns (path.Match syntax, same as the route globs). A nil
   // or empty pattern list never matches — that is the "operator did not
   // opt in" path and it must stay allocation- and transform-free.
   // Malformed patterns are treated as non-matching here; config.Validate
   // already rejects them at load time.
   func matchesAnyPattern(patterns []string, model string) bool {
       for _, pattern := range patterns {
           if ok, _ := path.Match(pattern, model); ok {
               return true
           }
       }
       return false
   }

   // liftSystemMessages moves every system-role entry that is NOT at
   // index 0 of the body's `messages` list into the body's top-level
   // `system` block, preserving the order the entries appeared in and
   // the relative order of the surviving messages.
   //
   // Returns (transformed body, number of entries moved, nil) when at
   // least one entry moved. Returns (nil, 0, nil) when there is nothing
   // to do — no `messages` key, or no system entry outside index 0 — and
   // the caller MUST then forward the original bytes untouched (spec 008
   // Constraint: byte fidelity on the no-transform path; a re-marshal
   // would reorder map keys).
   //
   // Returns (nil, 0, err) for any shape it cannot interpret: body not a
   // JSON object, `messages` not a list, a message entry that is not a
   // JSON object, a `role` that is not a string, or a system `content` /
   // top-level `system` value that is neither a string nor a list. The
   // caller degrades by forwarding the original body plus one warning —
   // it never fails the request (spec 008 Desired Behavior 6).
   //
   // The returned error never carries message content: system prompts
   // routinely hold private repository context (spec 008 Security §
   // log hygiene).
   //
   // A system entry already at index 0 is left in place and is NOT
   // copied into the top-level block, even when a top-level system block
   // also exists — deduplicating the two is explicitly out of scope.
   func liftSystemMessages(ctx context.Context, body []byte) ([]byte, int, error) {
       var obj map[string]json.RawMessage
       if err := json.Unmarshal(body, &obj); err != nil {
           return nil, 0, bberrors.Wrapf(ctx, err, "parse body as JSON object")
       }
       rawMessages, ok := obj["messages"]
       if !ok {
           return nil, 0, nil
       }
       var messages []json.RawMessage
       if err := json.Unmarshal(rawMessages, &messages); err != nil {
           return nil, 0, bberrors.Wrapf(ctx, err, "parse messages as a list")
       }
       kept := make([]json.RawMessage, 0, len(messages))
       lifted := make([]json.RawMessage, 0, len(messages))
       moved := 0
       for i, rawMessage := range messages {
           var message map[string]json.RawMessage
           if err := json.Unmarshal(rawMessage, &message); err != nil {
               return nil, 0, bberrors.Wrapf(ctx, err, "parse message %d as a JSON object", i)
           }
           rawRole, hasRole := message["role"]
           if !hasRole || i == 0 {
               kept = append(kept, rawMessage)
               continue
           }
           var role string
           if err := json.Unmarshal(rawRole, &role); err != nil {
               return nil, 0, bberrors.Wrapf(ctx, err, "parse role of message %d as a string", i)
           }
           if role != "system" {
               kept = append(kept, rawMessage)
               continue
           }
           blocks, err := normalizeToBlocks(ctx, message["content"])
           if err != nil {
               return nil, 0, bberrors.Wrapf(ctx, err, "normalize content of message %d", i)
           }
           lifted = append(lifted, blocks...)
           moved++
       }
       if moved == 0 {
           return nil, 0, nil
       }
       system, err := normalizeToBlocks(ctx, obj["system"])
       if err != nil {
           return nil, 0, bberrors.Wrapf(ctx, err, "normalize top-level system block")
       }
       system = append(system, lifted...)
       systemJSON, err := json.Marshal(system)
       if err != nil {
           return nil, 0, bberrors.Wrapf(ctx, err, "marshal system block")
       }
       messagesJSON, err := json.Marshal(kept)
       if err != nil {
           return nil, 0, bberrors.Wrapf(ctx, err, "marshal message list")
       }
       obj["system"] = systemJSON
       obj["messages"] = messagesJSON
       out, err := json.Marshal(obj)
       if err != nil {
           return nil, 0, bberrors.Wrapf(ctx, err, "re-marshal body")
       }
       return out, moved, nil
   }

   // normalizeToBlocks converts a system content value into a list of
   // content blocks:
   //
   //   - absent (nil raw) or JSON null -> zero blocks
   //   - JSON string                   -> exactly one systemTextBlock
   //   - JSON list                     -> its elements, unchanged, block
   //                                      for block
   //   - anything else                 -> error (caller degrades)
   //
   // The error message deliberately carries no content, only the fact
   // that the shape was unsupported.
   func normalizeToBlocks(ctx context.Context, raw json.RawMessage) ([]json.RawMessage, error) {
       trimmed := bytes.TrimSpace(raw)
       if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
           return []json.RawMessage{}, nil
       }
       var text string
       if err := json.Unmarshal(trimmed, &text); err == nil {
           block, merr := json.Marshal(systemTextBlock{Type: "text", Text: text})
           if merr != nil {
               return nil, bberrors.Wrapf(ctx, merr, "marshal text block")
           }
           return []json.RawMessage{block}, nil
       }
       var blocks []json.RawMessage
       if err := json.Unmarshal(trimmed, &blocks); err == nil {
           return blocks, nil
       }
       return nil, bberrors.New(ctx, "unsupported system content shape")
   }
   ```

2. **Add the `RequiresLeadingSystem` field to `ModelRoute` in `/workspace/pkg/handler/model-router.go`.** Existing call sites all use keyed struct literals, so adding a field is source-compatible — do NOT change `NewModelRouter`'s parameter list.

   ```go
   // ModelRoute pairs a glob pattern (filepath.Match syntax) with the
   // provider name + handler to invoke when an incoming request's `model`
   // field matches. ProviderName is what appears in the structured log
   // (`provider=minimax`) and is the same key as in the YAML config's
   // `providers:` map.
   type ModelRoute struct {
       Pattern      string
       ProviderName string
       Handler      http.Handler
       // RequiresLeadingSystem carries this route's provider's
       // `requiresLeadingSystem` config globs. When the resolved model
       // name matches one of them, the router lifts non-leading
       // system-role messages into the top-level system block before
       // dispatching. Nil or empty (the default for every route built
       // from a config that omits the field) means: never transform.
       RequiresLeadingSystem []string
   }
   ```

3. **Wire the transform into `NewModelRouter`, between the route-match loop and `target.ServeHTTP(ur, r)`.** Capture the matched route's patterns inside the existing loop, then run the transform. Exact rewrite:

   ```go
   // Before:
   providerName := defaultProviderName
   target := defaultHandler
   for _, route := range routes {
       ok, _ := path.Match(route.Pattern, model)
       if ok {
           providerName = route.ProviderName
           target = route.Handler
           glog.V(2).
               Infof("[route] model=%q matched %q -> provider=%s", model, route.Pattern, providerName)
           break
       }
   }

   target.ServeHTTP(ur, r)

   // After:
   providerName := defaultProviderName
   target := defaultHandler
   var requiresLeadingSystem []string
   for _, route := range routes {
       ok, _ := path.Match(route.Pattern, model)
       if ok {
           providerName = route.ProviderName
           target = route.Handler
           requiresLeadingSystem = route.RequiresLeadingSystem
           glog.V(2).
               Infof("[route] model=%q matched %q -> provider=%s", model, route.Pattern, providerName)
           break
       }
   }

   // Models whose chat template rejects a system message that is not
   // first (spec 008): lift the misplaced entries into the top-level
   // system block. Runs AFTER alias resolution and [1m] stripping so
   // the pattern is matched against the model name the upstream sees.
   // Any body shape the transform cannot interpret is forwarded
   // unchanged with one warning — never a router-generated 5xx.
   if matchesAnyPattern(requiresLeadingSystem, model) {
       lifted, moved, lerr := liftSystemMessages(r.Context(), body)
       switch {
       case lerr != nil:
           glog.Warningf("[system-lift] skipped model=%s: %v", model, lerr)
       case moved > 0:
           glog.V(2).Infof("[system-lift] model=%s moved=%d", model, moved)
           body = lifted
           r.Body = io.NopCloser(bytes.NewReader(body))
           r.ContentLength = int64(len(body))
       }
   }

   target.ServeHTTP(ur, r)
   ```

   Placement rules that are load-bearing:
   - AFTER the alias branch and the `[1m]`-strip branch, so `model` is the fully resolved name (spec Desired Behavior 3).
   - AFTER the route loop, so the matched route's patterns are known.
   - BEFORE `target.ServeHTTP`, so the upstream sees the transformed body.
   - The `moved == 0` case falls through both switch arms: no log line, no body swap, byte-identical forward.
   - The log prefix `[system-lift]` is a frozen literal — do not rename, do not add a second prefix variant.
   - The success line is `glog.V(2)` (same verbosity as `[alias]` and `[1m-strip]`); the degrade line is `glog.Warningf` (always on).

4. **Re-export the transform for unit tests in `/workspace/pkg/handler/export_test.go`.** Add the `context` import and:

   ```go
   // LiftSystemMessages exposes liftSystemMessages for handler_test.
   func LiftSystemMessages(ctx context.Context, body []byte) ([]byte, int, error) {
       return liftSystemMessages(ctx, body)
   }

   // MatchesAnyPattern exposes matchesAnyPattern for handler_test.
   func MatchesAnyPattern(patterns []string, model string) bool {
       return matchesAnyPattern(patterns, model)
   }
   ```

5. **Create `/workspace/pkg/handler/system-lift_test.go`** (package `handler_test`, standard copyright header) with unit specs calling `handler.LiftSystemMessages(context.Background(), []byte(body))` directly. These are the level-1 boundary tests for the transform itself; the router-level specs in step 6 are the level-2 integration tests. Required specs:

   - **It("lifts non-leading system entries into the top-level system block in order")** — input body:
     ```json
     {"model":"qwen3.8:27b-mlx","max_tokens":64,"system":[{"type":"text","text":"top"}],"messages":[{"role":"user","content":"hi"},{"role":"system","content":"A"},{"role":"assistant","content":"ok"},{"role":"system","content":"B"}]}
     ```
     Assert `moved == 2`, no error, and on the re-parsed output: `messages` has length 2 with roles `user` then `assistant`, and `system` has exactly 3 blocks whose `text` values are `top`, `A`, `B` in that order.

   - **It("renders a string-form system content as exactly {\"type\":\"text\",\"text\":\"hello\"}")** — a single misplaced system entry with `"content":"hello"` and no top-level `system` key. Unmarshal the output's `system` into `[]json.RawMessage` and assert `string(blocks[0])` equals the literal `{"type":"text","text":"hello"}` — a byte-exact assertion that pins the key order produced by `systemTextBlock` (Acceptance Criterion 8).

   - **It("appends an already-block-list system content block for block")** — a misplaced system entry whose content is `[{"type":"text","text":"x"},{"type":"text","text":"y"}]`. Assert the output `system` has exactly those two blocks, unchanged, in order.

   - **It("normalizes a string-form top-level system field into a text block")** — body with `"system":"top"` plus one misplaced system entry with content `"A"`. Assert the output `system` is `[{"type":"text","text":"top"},{"type":"text","text":"A"}]` (Failure Modes row: Claude Code sending a string-form top-level system field).

   - **It("returns moved=0 and no error when a system entry is already first")** — messages `[{"role":"system","content":"first"},{"role":"user","content":"hi"}]`. Assert `moved == 0`, `err` is nil, and the returned body slice is nil (caller forwards the original).

   - **It("returns moved=0 when there is no system entry at all")** and **It("returns moved=0 when the body has no messages key")**.

   - **DescribeTable("degrades on shapes it cannot interpret", ...)** — one row per hostile body, each asserting `err` is non-nil, `moved == 0`, and the returned body is nil. (The "error message must not contain the private marker" assertion applies **only** to the `secretmarker` row below, which is the only row carrying one — do not assert it table-wide.) Rows:
     - `messages` is a JSON string: `{"model":"m","messages":"nope"}`
     - `messages` list contains a non-object entry: `{"model":"m","messages":[{"role":"user","content":"hi"},42]}`
     - body is not a JSON object: `[1,2,3]`
     - a `role` that is not a string: `{"model":"m","messages":[{"role":"u"},{"role":7}]}`
     - a misplaced system entry whose content is a number: `{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"system","content":5}]}`
     - a misplaced system entry whose content is an object: `{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"system","content":{"secretmarker":"x"}}]}` — additionally assert `err.Error()` does NOT contain `secretmarker` (spec Security § log hygiene).
     None of these rows may panic — a panic fails the spec, which is the Security § hostile-shapes evidence.

   - **`It("treats a null message entry as a non-system entry and leaves it in place")`** — body `{"model":"m","messages":[{"role":"user","content":"hi"},null]}`, asserting `err` is **nil**, `moved == 0`, the returned body is nil, and no panic.

     This row is deliberately NOT in the degrade table above. `json.Unmarshal([]byte("null"), &map[string]json.RawMessage{})` returns a **nil error** and leaves the map empty — JSON `null` is a documented no-op for maps — so the entry takes the `!hasRole` branch and is kept in place. Asserting `err != nil` here would fail against a correct implementation. Verified empirically 2026-08-15: `err=<nil> map=map[] len=0`. Do NOT add a special-case null guard to `liftSystemMessages` to force an error — leaving the entry untouched already satisfies spec DB6 (degrade == forward unchanged), and the no-panic evidence is preserved.

   - **DescribeTable("matchesAnyPattern", ...)** — `(nil, "qwen3.8:27b-mlx") == false`, `([]string{}, "qwen3.8:27b-mlx") == false`, `([]string{"qwen3.8*"}, "qwen3.8:27b-mlx") == true`, `([]string{"qwen3.8*"}, "qwen3.6:35b-a3b-coding-nvfp4") == false`, `([]string{"nope*","qwen3.8*"}, "qwen3.8:27b-mlx") == true`.

6. **Add a `Describe("ModelRouter system lift", ...)` block to `/workspace/pkg/handler/model-router_test.go`**, sibling to the existing `Describe("ModelRouter metrics wiring", ...)`. These are the level-2 integration specs: they go through the real `NewModelRouter` dispatch path into a capturing upstream handler, which is the same path production traffic takes.

   Shared fixture requirements — all of them are load-bearing for the byte-identity assertions:
   - `aliases` is `nil` and the request `model` carries no trailing `[1m]`, so neither pre-existing rewrite path fires (both re-marshal and Go map marshalling reorders keys — byte-identity would be destroyed by design, not by this change).
   - The capturing upstream records both `capturedBody, _ = io.ReadAll(r.Body)` and `capturedContentLength = r.ContentLength`, and writes a distinctive status (e.g. `http.StatusTeapot`) so specs can assert the client received the upstream's own status.
   - A `BeforeEach` setting `flag.Set("logtostderr", "true")` and `flag.Set("v", "2")` for the specs that assert on captured glog output.
   - Routes:
     ```go
     routes := []handler.ModelRoute{
         {
             Pattern:               "qwen*",
             ProviderName:          "ollama-local",
             Handler:               capturing,
             RequiresLeadingSystem: []string{"qwen3.8*"},
         },
     }
     ```
   - The canonical request body (reused across specs, declared once as a `const`):
     ```go
     const liftBody = `{"model":"qwen3.8:27b-mlx","max_tokens":64,"system":[{"type":"text","text":"top"}],"messages":[{"role":"user","content":"hi"},{"role":"system","content":"A"},{"role":"assistant","content":"ok"},{"role":"system","content":"B"}]}`
     ```

   Required specs:

   - **It("lifts non-leading system entries for a matching model, preserving order")** (AC 3) — POST `liftBody`. On the captured JSON: zero entries with `role == "system"` remain in `messages`, the surviving entries are `user` then `assistant` in that order, and `system` is exactly three text blocks with texts `top`, `A`, `B`. Also assert `capturedContentLength == int64(len(capturedBody))` (spec Constraint: Content-Length correctness).

   - **It("forwards byte-identically for a non-matching model on the same provider")** (AC 4) — the same body with `"model":"qwen3.6:35b-a3b-coding-nvfp4"` (matches the `qwen*` route, does not match `qwen3.8*`). Assert `Expect(capturedBody).To(Equal([]byte(original)))` and `capturedContentLength == int64(len(original))`.

   - **It("forwards byte-identically when the route declares no requiresLeadingSystem")** (AC 5, absent case) — same route with the field omitted entirely, model `qwen3.8:27b-mlx`. Assert byte-identity and `Expect(strings.Count(out, "[system-lift]")).To(Equal(0))` on the `captureStderr` output.

   - **It("forwards byte-identically when requiresLeadingSystem is an explicit empty list")** (AC 5, empty case) — `RequiresLeadingSystem: []string{}`. Same two assertions.

   - **It("forwards byte-identically and warns once when messages is not a list")** (AC 6) — matching model, body `{"model":"qwen3.8:27b-mlx","messages":"nope"}`. Assert: captured body is byte-identical to the sent body; `rec.Code` equals the upstream's status (`http.StatusTeapot`), NOT a 5xx; `strings.Count(out, "[system-lift]")` equals 1; and the captured line matches `MatchRegexp("W\\d{4} .*\\[system-lift\\] skipped")` (glog warning lines start with `W`).

   - **It("forwards byte-identically and warns once when a message entry is not an object")** (AC 6, second case) — body `{"model":"qwen3.8:27b-mlx","messages":[{"role":"user","content":"hi"},42]}`. Same three assertions.

   - **It("emits exactly one [system-lift] line naming the model and moved count, with no content")** (AC 7) — POST `liftBody` inside `captureStderr`. Extract the line with `regexp.MustCompile(`\[system-lift\][^\n]*`)`, assert exactly one match, that it contains `model=qwen3.8:27b-mlx` and `moved=2`, and that it contains neither `A` nor `B` (`Expect(line).NotTo(ContainSubstring("A"))`, same for `"B"`). Anti-fake note as a Go comment above the spec: `// Anti-fake: moved=2 is derived from the fixture's two misplaced entries — a hardcoded moved=1 or a content-echoing format string fails this spec.`

   - **It("leaves a system entry that is already first in place and forwards byte-identically")** (AC 9) — matching model, body `{"model":"qwen3.8:27b-mlx","system":[{"type":"text","text":"top"}],"messages":[{"role":"system","content":"first"},{"role":"user","content":"hi"}]}`. Assert the captured body is byte-identical to the sent body, the parsed `messages` still has length 2 with `messages[0].role == "system"`, the parsed `system` still has exactly one block, and `strings.Count(out, "[system-lift]")` equals 0.

   - **It("normalizes string and block-list system content through the full router path")** (AC 8 at router level) — matching model, one misplaced system entry with `"content":"hello"` and a second with a two-block list. Assert the captured `system` is `[{hello},{x},{y}]` in that order.

   - **It("does not transform a request that fell through to the default provider")** — model `gemini-3-pro` (matches no route) with the misplaced-system body; the default handler is the capturing one. Assert byte-identity and zero `[system-lift]` occurrences. This pins the documented scope: the transform keys off the MATCHED route, so fallback traffic is never rewritten (see prompt 3's docs requirement).

   - **It("still routes, strips [1m], and resolves aliases unchanged alongside the transform")** — regression guard: route `{Pattern: "qwen*", ProviderName: "ollama-local", Handler: capturing, RequiresLeadingSystem: []string{"qwen3.8*"}}` with `aliases: map[string]string{"q38": "qwen3.8:27b-mlx[1m]"}` and body `{"model":"q38","messages":[{"role":"user","content":"hi"},{"role":"system","content":"A"}]}`. Assert the captured body's `model` is `qwen3.8:27b-mlx` (alias resolved, `[1m]` stripped) AND that the transform still fired (zero system-role entries left, one text block `A` in `system`) — proving the pattern is matched against the fully resolved name.

7. **Do NOT change** the `NewModelRouter` parameter list, the `[req]` log line format, `rewriteModelField`, `extractModel`, `resolveModelLabel`, `recordTokensFromUsage`, the metrics call sites, `MaxRequestBodyBytes`, or any file under `/workspace/pkg/factory/`, `/workspace/pkg/config.go`, `/workspace/pkg/reloader/`. Populating `ModelRoute.RequiresLeadingSystem` from the parsed config is prompt 3's job — this prompt only adds the field and its consumer.

8. **Docs and CHANGELOG are deferred to prompt 3 of this spec** (`3-spec-008-wiring-and-docs.md`). Do NOT edit `docs/config.md`, `docs/config.example.yaml`, `README.md`, or `CHANGELOG.md` here. If the Definition-of-Done review flags them, report deferred-by-design naming prompt 3, not a blocker. **Concretely: report `status: success` and note the deferral in the completion notes — do NOT report `status: partial`.** `docs/dod.md` instructs you to report any unmet criterion as a blocker; that is overridden here by this explicit decomposition, and a `partial` self-report fails a correct prompt.

9. **Run `make precommit` in `/workspace`.** Must exit 0.

</requirements>

<constraints>
- **Frozen literal:** the log prefix is `[system-lift]`. Both the V(2) success line and the warning line carry it; no other spelling.
- **Byte fidelity on the no-transform path.** When the model does not match, when the pattern list is empty, when nothing is misplaced, or when the body cannot be interpreted, the bytes forwarded upstream are the exact bytes received (subject to the pre-existing alias / `[1m]` rewrites, which are unchanged by this work). Never call `liftSystemMessages`' output when `moved == 0`.
- **Ordering is content-preserving.** No system text may be dropped, merged, deduplicated, summarised, reordered relative to other lifted entries, or truncated. A system entry already at index 0 stays there and is NOT copied into the top-level block.
- **Content-Length correctness.** Whenever the body changes, `r.ContentLength` is set to the new length and `r.Body` replaced via `io.NopCloser(bytes.NewReader(body))`, exactly as the alias and `[1m]` paths already do.
- **Never fail the request.** An uninterpretable body forwards unchanged plus one `glog.Warningf`; the client receives the upstream's own status. No new `http.Error` call, no new early return, no panic.
- **Log hygiene.** The `[system-lift]` lines and every error message from `liftSystemMessages` / `normalizeToBlocks` carry the model name and counts only — never message content.
- **Model-scoped, never provider-scoped.** The decision uses the resolved model name against the matched route's pattern list only. Do NOT add a provider-level boolean, a global toggle, an environment variable, a CLI flag, or a per-request opt-out header (spec Non-goals).
- **No auto-detection, no upstream probing, no retry-on-500, no content-based routing** (spec Non-goals).
- **`NewModelRouter`'s parameter list is frozen** in this prompt. The new data rides on `ModelRoute`, which existing keyed struct literals extend source-compatibly.
- **Existing behaviour must not regress:** alias resolution, `[1m]` suffix stripping, glob routing, default-provider fallback, metrics labels, token extraction, trace capture, SSE flush passthrough, and the 32 MB request-body limit all keep working, and every existing spec in `pkg/handler/` keeps passing.
- **`bborbe/errors` conventions** (`bberrors.Wrapf(ctx, ...)`, `bberrors.New(ctx, ...)`), `glog.V(n)` gating, no `fmt.Printf`, GoDoc on every new declaration (including unexported ones, per this file's existing style).
- **No `exclude` or `replace` directives in `go.mod`.**
- **Do NOT commit** — dark-factory handles git.
</constraints>

<verification>

```bash
cd /workspace
make precommit
```

Must exit 0.

```bash
cd /workspace
grep -c '\[system-lift\]' pkg/handler/model-router.go
```

Must return exactly 2 (the V(2) success line and the Warningf degrade line).

```bash
cd /workspace
grep -n 'glog.V(2).Infof("\[system-lift\] model=%s moved=%d", model, moved)' pkg/handler/model-router.go
```

Must return exactly 1 line.

```bash
cd /workspace
grep -n 'RequiresLeadingSystem \[\]string' pkg/handler/model-router.go
```

Must return exactly 1 line (the new `ModelRoute` field).

```bash
cd /workspace
grep -n 'func NewModelRouter(' -A 9 pkg/handler/model-router.go | grep -c 'currentDateTime libtime.CurrentDateTimeGetter'
```

Must return 1 — the signature is unchanged (no extra parameter added).

```bash
cd /workspace
grep -rn 'http.Error' pkg/handler/model-router.go | wc -l
```

Must return 4 (the four pre-existing early-return paths: 413 oversize at `:106`, 400 read-failure at `:118`, 500 alias-rewrite at `:141`, 500 `[1m]`-strip at `:174` — the transform adds no new failure response). Do NOT delete an existing `http.Error` to make this number smaller; 4 is the correct baseline, verified against the current tree.

```bash
cd /workspace
go test ./pkg/handler/... -run TestSuite -ginkgo.v 2>&1 | tail -100
```

Expect: every existing spec PASSES plus the new `system-lift_test.go` specs and the new `Describe("ModelRouter system lift", ...)` specs.

```bash
cd /workspace
git diff --name-only
```

Expect exactly: `pkg/handler/model-router.go`, `pkg/handler/model-router_test.go`, `pkg/handler/export_test.go`, plus the two new files `pkg/handler/system-lift.go` and `pkg/handler/system-lift_test.go`.

</verification>

---
status: approved
spec: ["001"]
created: "2026-06-28T10:00:00Z"
queued: "2026-06-28T09:46:54Z"
---

<summary>
- The model router now accepts an alias map and rewrites the JSON body's top-level `model` field before glob-routing.
- On a hit, the upstream sees the full resolved model name (e.g. `qwen3.6:35b-...`) and the router logs `[alias] qwen -> qwen3.6:35b-...` at glog `V(1)`.
- On a miss (or nil/empty alias map, non-JSON body, no model field), the body is forwarded unchanged — no `[alias]` log line.
- A failed body-rewrite (corrupt JSON mid-flight, marshal error) returns 500 to the client and logs the error.
- All existing model-router behavior (glob routing, fallback, body preservation) continues unchanged.
</summary>

<objective>
Extend `pkg/handler.NewModelRouter` so it consults an `aliases` map and rewrites the request body's `.model` field on a hit before falling through to the existing glob-routing logic. The upstream always sees the resolved full model name. This prompt produces only the handler change + tests — wiring through the factory happens in prompt 3.
</objective>

<context>
Read first:
- `/workspace/specs/in-progress/001-add-model-aliases.md` — full spec, especially the "Desired Behavior" section item 4 and the "Failure Modes" table.
- `/workspace/pkg/handler/model-router.go` — current `NewModelRouter`, `ModelRoute`, `extractModel`. Note the body is already fully read into `body` and replayed via `r.Body = io.NopCloser(bytes.NewReader(body))` and `r.ContentLength = int64(len(body))`. The alias rewrite slots in BETWEEN the body read and the route walk.
- `/workspace/pkg/handler/model-router_test.go` — existing Ginkgo specs. Note the constructor call sites: line ~47 (`handler.NewModelRouter(routes, fallback)`) and line ~100 (capturing-body spec). These two call sites MUST be updated when the signature gains a third parameter.
- `/workspace/pkg/handler/anthropic-proxy.go` — for the surrounding `http.Handler` style and glog pattern.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-http-handler-refactoring-guide.md` — for the body-rewrite + ContentLength pattern.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — for `glog.V(1).Infof` and `glog.Errorf` conventions.
</context>

<requirements>

1. **Change the `NewModelRouter` signature** in `pkg/handler/model-router.go` to accept an `aliases` map as the third parameter:

   ```go
   // NewModelRouter returns an HTTP handler that body-parses each request's
   // JSON `model` field, resolves it through the aliases map (single-hop,
   // case-sensitive exact match), then dispatches to the first matching
   // ModelRoute. Unmatched models (and non-JSON / no-model requests) fall
   // through to defaultHandler. The body is fully read and replayed for
   // the downstream handler — fine for /v1/messages JSON payloads
   // (typically <100 KB); not suitable for unbounded upload bodies.
   //
   // aliases may be nil or empty — both mean "no alias rewriting", same
   // as today's behavior. On a hit, the body's top-level .model field is
   // re-marshaled to the resolved value before route dispatch, so the
   // upstream sees the full model name. A single glog.V(1) line is
   // emitted on hit: "[alias] <short> -> <resolved>".
   func NewModelRouter(
       routes []ModelRoute,
       defaultHandler http.Handler,
       aliases map[string]string,
   ) http.Handler {
   ```

   Place `aliases` as the THIRD parameter (after `defaultHandler`). This matches the conceptual layering: routes + fallback are the dispatch table, aliases is the pre-dispatch rewrite.

2. **Insert the alias-rewrite block** between the existing body read and the route walk. After:

   ```go
   _ = r.Body.Close()
   r.Body = io.NopCloser(bytes.NewReader(body))
   r.ContentLength = int64(len(body))

   model := extractModel(body)
   ```

   add:

   ```go
   if resolved, ok := aliases[model]; ok && model != "" {
       rewritten, rerr := rewriteModelField(body, resolved)
       if rerr != nil {
           glog.Errorf("[alias] rewrite failed for %q -> %q: %v", model, resolved, rerr)
           http.Error(w, "alias rewrite failed", http.StatusInternalServerError)
           return
       }
       glog.V(1).Infof("[alias] %s -> %s", model, resolved)
       body = rewritten
       r.Body = io.NopCloser(bytes.NewReader(body))
       r.ContentLength = int64(len(body))
       model = resolved
   }
   ```

   Notes:
   - The `model != ""` guard prevents a degenerate `aliases[""]` entry from firing on bodies with no model field (the existing `extractModel` returns `""` for non-JSON / no-model).
   - `aliases` is `map[string]string`; reading from a nil map returns the zero value `("", false)` and is safe — no nil check needed.
   - On hit, replace `model` with `resolved` BEFORE entering the route walk so glob matching uses the resolved name.
   - On rewrite failure, return 500 (matches the spec's Failure Modes table row for "Body JSON-rewrite fails mid-flight").

3. **Add the `rewriteModelField` helper** to `pkg/handler/model-router.go`:

   ```go
   // rewriteModelField parses body as a JSON object, sets the top-level
   // "model" field to resolved, and returns the re-marshaled bytes. All
   // other top-level fields are preserved (their values are kept as
   // json.RawMessage to avoid lossy re-encoding of nested structures and
   // numbers). Returns an error if body is not a JSON object.
   func rewriteModelField(body []byte, resolved string) ([]byte, error) {
       var obj map[string]json.RawMessage
       if err := json.Unmarshal(body, &obj); err != nil {
           return nil, fmt.Errorf("parse body as JSON object: %w", err)
       }
       resolvedJSON, err := json.Marshal(resolved)
       if err != nil {
           // Should never happen — string marshal is infallible.
           return nil, fmt.Errorf("marshal resolved model: %w", err)
       }
       obj["model"] = resolvedJSON
       out, err := json.Marshal(obj)
       if err != nil {
           return nil, fmt.Errorf("re-marshal body: %w", err)
       }
       return out, nil
   }
   ```

   Add `"fmt"` to the import block if not already present. Using `map[string]json.RawMessage` preserves arbitrary nested JSON without lossy `map[string]any` round-trips.

   **Caveat for the spec's "Body preservation" constraint:** Go's `encoding/json` sorts map keys alphabetically and uses canonical numeric encoding. This means the rewritten body is byte-different from the original even for non-model fields. The spec's body-preservation constraint says "Only the top-level `.model` field changes; all other fields, key order in non-JSON contexts, and byte fidelity outside the JSON object are out of scope (this is a JSON-only path)." — semantic preservation (round-trippable to the same JSON value) is what matters, not byte fidelity. Tests must assert on parsed-JSON equality, not byte-for-byte equality.

4. **Update the two call sites in `pkg/handler/model-router_test.go`** to pass `nil` for the existing specs (so behavior is identical to today):

   - Line ~47 (`BeforeEach`): `mux = handler.NewModelRouter(routes, fallback, nil)`
   - Line ~100 (preservation spec): `mux = handler.NewModelRouter([]handler.ModelRoute{...}, fallback, nil)`

   These existing specs continue to assert today's behavior. They MUST still pass unchanged in their assertions.

5. **Add a new `Context("alias resolution", func() { ... })` block** to `pkg/handler/model-router_test.go` with these specs:

   - **It("rewrites the request body's .model field when an alias matches")** — set up a capturing handler that records the request body it sees:
     ```go
     var capturedBody []byte
     var capturedContentLength int64
     capturing := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
         capturedBody, _ = io.ReadAll(r.Body)
         capturedContentLength = r.ContentLength
     })
     aliases := map[string]string{"qwen": "qwen3.6:35b-a3b-coding-nvfp4"}
     mux := handler.NewModelRouter(
         []handler.ModelRoute{{Pattern: "qwen*", Handler: capturing}},
         fallback,
         aliases,
     )
     mux.ServeHTTP(rec, post(`{"model":"qwen"}`))

     var seen map[string]any
     Expect(json.Unmarshal(capturedBody, &seen)).To(Succeed())
     Expect(seen["model"]).To(Equal("qwen3.6:35b-a3b-coding-nvfp4"))
     Expect(capturedContentLength).To(Equal(int64(len(capturedBody))))
     ```
     Add `"encoding/json"` to the test file imports if not already present.

   - **It("routes the rewritten body to the matching glob")** — same setup as above; assert that the capturing handler (whose route is `"qwen*"`) actually receives the request. This proves the alias-resolved name participates in glob routing, not the original short name. Use a `labelHandler` already in the file for the assertion variant: `mux := handler.NewModelRouter([]handler.ModelRoute{{Pattern: "qwen*", Handler: labelHandler("ollama")}}, fallback, aliases)`; assert `rec.Body.String() == "ollama"`.

   - **It("preserves other top-level body fields across the rewrite")** — body `{"model":"qwen","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`. After rewrite, parse `capturedBody` into `map[string]any` and assert:
     - `seen["model"] == "qwen3.6:35b-a3b-coding-nvfp4"`
     - `seen["max_tokens"]` round-trips to `float64(100)` (encoding/json's default numeric type).
     - `seen["messages"]` is a non-empty slice and the first element's `role == "user"`.

   - **It("does not rewrite on alias miss")** — body `{"model":"claude-opus-4-7"}` with aliases `{"qwen": "qwen3.6:..."}`. Assert byte-equality of captured body to the original (`{"model":"claude-opus-4-7"}`) AND `capturedContentLength == int64(len(originalBody))` (no rewrite happened).

   - **It("does not rewrite when aliases map is nil")** — same as miss test but `aliases = nil`. Assert byte-equality of captured body to original AND `capturedContentLength == int64(len(originalBody))`.

   - **It("does not rewrite when body has no model field")** — body `{"other":"thing"}` with `aliases = {"": "should-not-fire"}`. Assert byte-equality of captured body to the original AND `capturedContentLength == int64(len(originalBody))` (the `model != ""` guard must prevent the rewrite). Route this through `fallback` since no glob matches an empty model — the body assertion needs the capturing handler to be wired as the fallback:
     ```go
     mux := handler.NewModelRouter(nil, capturing, map[string]string{"": "x"})
     mux.ServeHTTP(rec, post(`{"other":"thing"}`))
     Expect(string(capturedBody)).To(Equal(`{"other":"thing"}`))
     Expect(capturedContentLength).To(Equal(int64(len(`{"other":"thing"}`))))
     ```

   No 500-path spec. `rewriteModelField` is unreachable in practice for valid JSON (any body `extractModel` parses as `struct{Model string}` also parses as `map[string]json.RawMessage`). Document the defensive nature in `rewriteModelField`'s doc comment: `// rewriteModelField is best-effort; a JSON body that extractModel accepted will always re-marshal. The error return is defensive for unforeseen input shapes.`

6. **Run `make precommit`** in the repo root. Fix any lint / format / addlicense issues. Verify all existing handler specs continue to pass alongside the new alias specs.

</requirements>

<constraints>

- **Backward compatibility (from spec).** A nil `aliases` map MUST be a no-op. Existing model-router specs (which now pass `nil`) MUST continue to pass with identical assertions.
- **Single-hop only (from spec).** If `aliases = {"a": "b", "b": "c"}` and the request body has `model: "a"`, the rewritten body has `model: "b"` (NOT `"c"`). Do NOT loop or recurse.
- **Body preservation (from spec).** Top-level fields other than `model` MUST round-trip through JSON parsing to equivalent values. Byte-for-byte fidelity is NOT required (Go's json marshal sorts map keys). Assert on parsed-JSON values in tests, not byte equality, for the rewrite-hit specs.
- **ContentLength (from spec).** After rewrite, `r.ContentLength` MUST equal `len(rewrittenBody)`. The downstream proxy depends on this for Content-Length headers.
- **Glob comparison case-sensitivity (from spec).** Alias key lookup is case-sensitive — `aliases["Qwen"]` and `aliases["qwen"]` are distinct keys. This is automatic for Go map lookups; no normalization.
- **Log format (from spec).** The hit log line MUST match the literal format string `[alias] %s -> %s` at glog `V(1)` level — the deploy_check in AC 6 greps the compiled binary for `[alias] %s -> %s`. Do NOT change the format string casing, spacing, or arrow style.
- **Do NOT commit.** dark-factory handles git.
- **Existing tests must still pass.** All 8 existing specs in `model-router_test.go` continue to pass after the `nil`-aliases-arg update.

</constraints>

<verification>

```bash
cd /workspace
make precommit
```

Must pass. Additionally:

```bash
cd /workspace
go test ./pkg/handler/ -v -run TestSuite 2>&1 | tail -60
```

Expect: existing 8 specs PASS, plus the 6 new `Context("alias resolution")` specs PASS.

Confirm the log-format literal is present in the source (the AC 6 `deploy_check` greps the compiled binary for it):

```bash
grep -n '\[alias\] %s -> %s' /workspace/pkg/handler/model-router.go
```

Expect exactly one match on the `glog.V(1).Infof` line.

Confirm both pre-existing call sites in the test file pass `nil`:

```bash
grep -n 'NewModelRouter' /workspace/pkg/handler/model-router_test.go
```

Expect all `NewModelRouter(...)` invocations to have 3 args (the third being either `nil` or a real `aliases` map).

</verification>

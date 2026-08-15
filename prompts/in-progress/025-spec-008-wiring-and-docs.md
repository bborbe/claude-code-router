---
status: approved
spec: ["008"]
created: "2026-08-15T10:40:00Z"
queued: "2026-08-15T10:45:24Z"
---

<summary>
- Connects the new per-provider setting to the running router, so what an operator writes in the config file actually takes effect on live traffic.
- An end-to-end test drives a real request through the fully assembled router into a real test upstream and checks the rewritten payload arrives.
- The config reference documents the setting: its glob semantics, that leaving it out or empty means nothing is ever rewritten, and the log line that proves it fired.
- The config reference records WHY the setting is per model rather than per provider — the restriction lives in each model's chat template, so two models behind one provider legitimately disagree.
- The documented scope is explicit: requests that fall through to the default provider without matching any model pattern are never rewritten.
- The copy-paste example config and the README install example both gain the new field, so a new operator sees it without reading the reference.
- The changelog gains a conventionally-prefixed feature entry naming the setting.
</summary>

<objective>
Wire each provider's `requiresLeadingSystem` patterns from the parsed config into the routes the factory builds, and ship the operator-facing documentation (`docs/config.md` including the model-scoped rationale, `docs/config.example.yaml`, `README.md`, `CHANGELOG.md`). This is the prompt that makes the feature reachable from a real config file.
</objective>

<context>
Read first:
- `/workspace/specs/in-progress/008-requires-leading-system-message.md` — Desired Behavior 3 (wiring half); Acceptance Criterion 10 (`docs/config.md` records the semantics AND the reason the field is model-scoped); Constraints (CHANGELOG prefix — the repo auto-releases and an unprefixed bullet was rejected by bot review on PR #36); the Verification section's container-executable grep list.
- `/workspace/pkg/factory/factory.go` — `func CreateRouterFromConfig(ctx context.Context, cfg *pkg.Config, opts ...RouterOption) (http.Handler, error)`. The provider loop builds `handler.ModelRoute{Pattern: pattern, ProviderName: name, Handler: proxy}` for each `pattern` in `prov.Models`; that literal gains one field. `CreateServer` and the SIGHUP reload path both funnel through `CreateRouterFromConfig`, so there is exactly one wiring site.
- `/workspace/pkg/factory/trace_wiring_test.go` — the factory-level integration pattern to mirror: package `factory_test`, `factory.CreateRouterFromConfig(context.Background(), cfg, factory.WithMetricsRegisterer(prometheus.NewRegistry()))`, then `handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))`. Note the isolated-registry option is REQUIRED — without it the factory's `metrics.Register` races on the process-global `prometheus.DefaultRegisterer` shared with the other suites in the same test binary.
- `/workspace/pkg/config.go` — `Provider.RequiresLeadingSystem []string` (landed by prompt 1).
- `/workspace/pkg/handler/model-router.go` — `ModelRoute.RequiresLeadingSystem []string` (landed by prompt 2).
- `/workspace/docs/config.md` — current shape: `## Schema` fenced block, `## Routing`, `## Aliases` (with `### Semantics` and `### Validation` sub-sections), `## Auth`, `## Trace`, `## Example — all four providers`, `## Switching mid-session`, `## Reload`, `## Related`.
- `/workspace/docs/config.example.yaml` — the copy-paste config; `ollama-local` block is at the bottom of `providers:`.
- `/workspace/README.md` — step 2's fenced `yaml` example config block (`ollama-local` block sits just above the `aliases:` comment).
- `/workspace/CHANGELOG.md` — `## Unreleased` ALREADY EXISTS at line 7 with two `fix:` bullets, above `## v0.19.1`. Append to that section; never create a second `## Unreleased`, never rename a released `## vX.Y.Z`.
- `/workspace/docs/dod.md` — the Documentation section (`README.md` / `docs/config.md` / `docs/config.example.yaml` / `CHANGELOG.md` updated; all repo references to config fields updated) is what this prompt discharges for the whole spec.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` placement and conventional-prefix bullet rules.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md` — the `Create*` wiring convention this repo follows.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo integration patterns.

**Dependency guard (fail-fast at prompt start):** verify prompts 1 AND 2 landed:

```bash
grep -q 'RequiresLeadingSystem \[\]string `yaml:"requiresLeadingSystem,omitempty"`' /workspace/pkg/config.go && \
grep -q 'RequiresLeadingSystem \[\]string' /workspace/pkg/handler/model-router.go && \
grep -q 'func liftSystemMessages' /workspace/pkg/handler/system-lift.go
```

If any check fails, STOP and report `dependency not yet deployed: wiring + docs require prompt 1 (config field) and prompt 2 (router transform) to have landed`. Do not re-add either — that duplicates the earlier prompts and creates a merge conflict.
</context>

<requirements>

1. **Wire the patterns in `/workspace/pkg/factory/factory.go`.** In the `for name, prov := range cfg.Providers` loop of `CreateRouterFromConfig`, extend the `ModelRoute` literal:

   ```go
   // Before:
   for _, pattern := range prov.Models {
       routes = append(routes, handler.ModelRoute{
           Pattern:      pattern,
           ProviderName: name,
           Handler:      proxy,
       })
   }

   // After:
   for _, pattern := range prov.Models {
       routes = append(routes, handler.ModelRoute{
           Pattern:               pattern,
           ProviderName:          name,
           Handler:               proxy,
           RequiresLeadingSystem: prov.RequiresLeadingSystem,
       })
   }
   ```

   This is the ONLY production wiring site: `CreateServer` and the SIGHUP reload closure both call `CreateRouterFromConfig`. Confirm with `grep -rn 'ModelRoute{' /workspace/pkg /workspace/main.go` — every other hit must be in a `_test.go` file. If a second production construction site exists, STOP and report it.

2. **Add `/workspace/pkg/factory/system_lift_wiring_test.go`** (package `factory_test`, standard 3-line BSD copyright header). This is the end-to-end integration test proving a YAML-shaped config reaches the transform through the real handler tree — a shape test on the `ModelRoute` literal does NOT satisfy this requirement.

   Structure: an `httptest.NewServer` upstream that records `capturedBody, _ = io.ReadAll(r.Body)` and replies `200 {"ok":true}`; a `*pkg.Config` whose `ollama-local` provider has `Upstream: srv.URL`, `Models: []string{"qwen*"}`, `RequiresLeadingSystem: []string{"qwen3.8*"}`, with `Router: pkg.Router{DefaultProvider: "ollama-local"}`; the handler from `factory.CreateRouterFromConfig(context.Background(), cfg, factory.WithMetricsRegisterer(prometheus.NewRegistry()))`. Close the test server in `AfterEach`.

   Required specs:

   - **It("lifts non-leading system messages for a model matching the provider's requiresLeadingSystem")** — POST `/v1/messages` with
     ```json
     {"model":"qwen3.8:27b-mlx","max_tokens":64,"system":[{"type":"text","text":"top"}],"messages":[{"role":"user","content":"hi"},{"role":"system","content":"A"},{"role":"assistant","content":"ok"},{"role":"system","content":"B"}]}
     ```
     Assert on the JSON the upstream received: `messages` has length 2 with roles `user` then `assistant`, no `system`-role entry remains, and `system` has exactly three text blocks with texts `top`, `A`, `B` in that order.

   - **It("forwards byte-identically for a model that matches the provider glob but not requiresLeadingSystem")** — the same body with `"model":"qwen3.6:35b-a3b-coding-nvfp4"`. Assert `Expect(capturedBody).To(Equal([]byte(original)))`. The config declares no `Aliases` and the model carries no `[1m]` suffix, so neither pre-existing rewrite path fires and byte-identity is meaningful.

   - **It("forwards byte-identically when the provider omits requiresLeadingSystem")** — same config with the field unset, model `qwen3.8:27b-mlx`. Assert byte-identity (the backwards-compatibility guarantee at factory level).

3. **Update `/workspace/docs/config.md`.**

   a. **Schema block** — extend the provider entry in the `## Schema` fenced block:

   ```yaml
   providers:
     <provider-key>:
       upstream: <URL>                    # required; e.g. https://api.anthropic.com
       token: <string>                    # optional; if absent, client's Authorization header passes through
       models:                            # filepath.Match glob patterns
         - "<pattern>"
         - ...
       requiresLeadingSystem:             # optional; glob patterns for models whose chat template rejects a non-leading system message (see ## Requires leading system)
         - "<pattern>"
         - ...
   ```

   b. **New `## Requires leading system` section**, inserted between the `## Aliases` section and `## Auth`. It must contain all of the following (Acceptance Criterion 10 greps for `requiresLeadingSystem`, `qwen3.6`, and `chat template` in this section):

   - The problem in one paragraph: Claude Code puts a `system`-role entry inside the message list, after the first user turn, in addition to the dedicated top-level `system` block. Some models' chat templates reject any system entry that is not first and answer `HTTP 500 {"type":"error","error":{"type":"api_error","message":"system message must be at the beginning"}}` before inference starts.
   - The example block:
     ```yaml
     providers:
       ollama-local:
         upstream: http://localhost:11434
         token: ollama
         models:
           - "qwen*"
         requiresLeadingSystem:
           - "qwen3.8*"
     ```
   - A `### Semantics` sub-list:
     - **Glob syntax.** Same `filepath.Match` syntax as `models:` — `*`, `?`, `[abc]`. Matched against the FULLY RESOLVED model name, i.e. after alias resolution and after the `[1m]` suffix is stripped.
     - **Opt-in, default off.** Absent, `null`, and an empty list are equivalent and all mean: never transform anything for this provider. A config that does not mention the field routes byte-for-byte as before.
     - **What the transform does.** Every `system`-role entry that is not at index 0 of `messages` is removed from the list and its content appended to the top-level `system` block, in the order the entries appeared. Content given as a plain string becomes one `{"type":"text","text":"..."}` block; content already given as a block list is appended block for block. The surviving messages keep their relative order.
     - **What it does not do.** A `system` entry already at index 0 stays there and is NOT copied into the top-level block, even when a top-level block also exists — the upstream then receives system content in both places, which is the shape Claude Code already sends today and which upstreams accept. No merging, deduplication, summarisation, or reordering beyond moving entries.
     - **Untransformed paths.** A request whose model does not match, or that has no misplaced system entry, is forwarded as the exact bytes received. Same for a body the transform cannot interpret (`messages` not a list, an entry that is not an object, a system `content` that is neither a string nor a block list): the request is forwarded unchanged with one `WARNING` line and the client gets the upstream's own status — the transform never turns a request into a router-generated error.
     - **Scope is the matched route.** The patterns come from the provider whose `models:` glob matched. A request that matches no provider glob and falls through to `default_provider` is never transformed — if you need the transform for such a model, give that provider a `models:` glob that matches it.
     - **Log line.** On a fire, the router logs `[system-lift] model=qwen3.8:27b-mlx moved=2` at glog `V(2)` — the same verbosity as the `[alias]` and `[1m-strip]` detail lines, so it is invisible at the always-on `V(1)` default. Raise the level with `curl http://127.0.0.1:8788/setloglevel/2` (auto-reverts after 5 minutes) before grepping `/tmp/claude-code-router.log` for it. The line carries the model name and a count only — never system-message content.
   - A `### Why per model and not per provider` sub-section (Acceptance Criterion 10's load-bearing half, and the piece that otherwise dies with the spec): ollama's system-position restriction is a property of each MODEL's chat template, not of the ollama server, so two models behind the same provider legitimately disagree. Verified 2026-08-15 with identical curl payloads against the same ollama instance: `qwen3.6:35b-a3b-coding-nvfp4` → 200, `qwen3.8:27b-mlx` → 500, `qwen3.8:27b-mtp-q4_K_M` → 500 — all three match the `ollama-local` provider's `qwen*` glob. A provider-wide boolean switch would therefore silently rewrite prompts for models that never needed it, which is why there is no such switch and no global toggle, environment variable, or CLI flag.
   - A `### Validation` sub-list: a malformed pattern (e.g. `[`) makes the config fail to load with `provider "<name>": invalid requiresLeadingSystem glob "["` and the router refuses to start — the same load-time check the `models:` globs get. A well-formed pattern that matches no model the operator actually uses is NOT an error and produces no warning: the symptom is that no `[system-lift]` line ever appears while the upstream keeps returning 500, and the fix is to widen the pattern.

   c. **`## Example — all four providers`** — add `requiresLeadingSystem` to the `ollama-local` block:

   ```yaml
     ollama-local:
       upstream: http://localhost:11434
       token: "ollama"                   # Ollama's literal-string convention
       models:
         - "qwen*"
       requiresLeadingSystem:            # qwen3.8's chat template rejects a non-leading system message
         - "qwen3.8*"
   ```

4. **Update `/workspace/docs/config.example.yaml`** — extend the `ollama-local` provider block with a commented field:

   ```yaml
     ollama-local:
       upstream: http://localhost:11434
       token: "ollama"
       models:
         - "qwen*"
       # Models whose chat template rejects a system message that is not the
       # first entry (qwen3.8 does; qwen3.6 on the same server does not).
       # The router lifts misplaced system messages into the top-level system
       # block for matching models. Omit or leave empty for no transform.
       requiresLeadingSystem:
         - "qwen3.8*"
   ```

5. **Update `/workspace/README.md`** — in the step-2 example config fenced block, add the same field to the `ollama-local` provider (README uses 3-space base indentation inside the numbered list — match the surrounding lines exactly):

   ```yaml
     ollama-local:
       upstream: http://localhost:11434
       token: "ollama"
       models:
         - "qwen*"
       # Lift misplaced system messages for models whose chat template
       # rejects them (qwen3.8 does; qwen3.6 does not). See docs/config.md.
       requiresLeadingSystem:
         - "qwen3.8*"
   ```

   Do not restructure the README section or add a new top-level heading — the fenced example plus its comment is the whole change there.

6. **Append one bullet to the EXISTING `## Unreleased` section in `/workspace/CHANGELOG.md`** (below the two `fix:` bullets already there, above `## v0.19.1`). The entry MUST start with the conventional prefix `feat:` — the repo auto-releases and an unprefixed bullet was rejected by bot review on PR #36:

   ```markdown
   - feat: add the optional per-provider `requiresLeadingSystem` config field — a list of model-name glob patterns naming models whose chat template rejects a `system`-role message that is not the first entry of the conversation. For a matching model the router lifts every misplaced system message out of the `messages` list and appends its content to the top-level `system` block, preserving order (string content becomes one text block; a block list is appended block for block), then logs `[system-lift] model=<model> moved=<n>` at V(2). Fixes qwen3.8 through ollama, which returned `HTTP 500 system message must be at the beginning` on every Claude Code request. Scoped per model, not per provider: the restriction lives in each model's chat template, so `qwen3.6` (200) and `qwen3.8` (500) disagree behind one provider. Absent or empty means no transform and byte-identical forwarding — every existing config is unaffected. An uninterpretable body is forwarded unchanged with one warning rather than failing the request.
   ```

   Do NOT create a second `## Unreleased` heading, do NOT rename `## v0.19.1` or any released section, do NOT bump a version number.

7. **Sweep for any other reference to provider config fields** (Definition of Done: "grep the entire repo and update all references in `docs/`, `README.md`, examples, and comments"):

   ```bash
   cd /workspace
   grep -rln 'upstream:' --include='*.md' --include='*.yaml' . | grep -v vendor
   ```

   Expected hits: `README.md`, `docs/config.md`, `docs/config.example.yaml`, plus files under `specs/` and `prompts/` (historical records — do NOT edit those). If a file outside that set lists provider fields, update it too.

8. **Do NOT touch** `/workspace/pkg/config.go`, `/workspace/pkg/handler/` (both landed in prompts 1 and 2), `/workspace/pkg/reloader/`, `/workspace/main.go`, `/workspace/docs/dod.md`, `/workspace/docs/metrics.md`, or `/workspace/docs/debug.md`.

9. **Run `make precommit` in `/workspace`.** Must exit 0.

</requirements>

<constraints>
- **Frozen literals:** the YAML field is `requiresLeadingSystem`; the log prefix quoted in the docs is `[system-lift]`. Documentation must match the code exactly — a doc that names a different field or prefix is doc drift, not a doc.
- **CHANGELOG prefix.** The entry carries the conventional `feat:` prefix and mentions `requiresLeadingSystem`. It goes under the existing `## Unreleased`, never under a released version.
- **Backwards compatibility is absolute.** The factory change must leave every existing config building an identical route set (a config without the field yields `RequiresLeadingSystem: nil` on every route, which never matches).
- **Model-scoped, never provider-scoped.** Documentation must state and justify this; do not describe or introduce a provider-wide boolean, a global toggle, an environment variable, a CLI flag, or a per-request opt-out header (spec Non-goals).
- **No auto-detection or upstream probing** described or implemented (spec Non-goal): the docs describe declarative config only, never "try and retry on 500".
- **Existing behaviour must not regress:** alias resolution, `[1m]` stripping, glob routing, default-provider fallback, metrics registration (including the isolated-registry option in the new factory test), SIGHUP reload, and trace wiring all keep working; every existing spec in `pkg/factory/` and `pkg/handler/` keeps passing.
- **All `Create*` wiring lives in `pkg/factory/`** — no route construction anywhere else.
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
grep -n 'RequiresLeadingSystem: prov.RequiresLeadingSystem' pkg/factory/factory.go
```

Must return exactly 1 line.

```bash
cd /workspace
grep -c 'requiresLeadingSystem' docs/config.md docs/config.example.yaml README.md
```

Each file must report ≥1 (spec Verification block; `docs/config.md` should report ≥4 — schema, section heading body, example, validation).

```bash
cd /workspace
grep -n 'qwen3.6' docs/config.md && grep -n 'chat template' docs/config.md
```

Both must return ≥1 line, inside the `## Requires leading system` section (Acceptance Criterion 10 — the model-scoped rationale).

```bash
cd /workspace
grep -n '^- feat:' CHANGELOG.md | head -3
```

Must return ≥1 line under `## Unreleased` (verify with `grep -n '## Unreleased\|## v0.19.1' CHANGELOG.md` that the new bullet's line number is between the two).

```bash
cd /workspace
grep -n 'requiresLeadingSystem' CHANGELOG.md
```

Must return ≥1 line.

```bash
cd /workspace
grep -c '## Unreleased' CHANGELOG.md
```

Must return exactly 1.

```bash
cd /workspace
git diff --quiet -- go.mod go.sum && echo "go.mod/go.sum unchanged"
```

Must print `go.mod/go.sum unchanged` — this change touches no dependencies.

Do **NOT** grep `go.mod` for `exclude|replace` and expect zero matches: `go.mod:52` carries a pre-existing `exclude (cloud.google.com/go v0.26.0)` block unrelated to this spec. Such a gate false-fails immediately, and the only way to satisfy it would be deleting that block — which requirement 8 forbids and which the file-list gate below would then flag. The DoD's "no exclude/replace" line means *do not add new ones*, which the no-drift check above enforces correctly.

```bash
cd /workspace
go test ./pkg/factory/... -run TestSuite -ginkgo.v 2>&1 | tail -60
```

Expect: existing trace-wiring specs PASS plus the three new `system_lift_wiring_test.go` specs PASS.

```bash
cd /workspace
git status --short -- pkg/ docs/ README.md CHANGELOG.md
```

Expect exactly six entries: modified `pkg/factory/factory.go`, `docs/config.md`, `docs/config.example.yaml`, `README.md`, `CHANGELOG.md`, and untracked (`??`) `pkg/factory/system_lift_wiring_test.go`.

Use `git status --short`, not `git diff --name-only` — the new test file is untracked and never appears in `git diff`. The pathspec also excludes dark-factory's own bookkeeping churn under `prompts/`.

</verification>


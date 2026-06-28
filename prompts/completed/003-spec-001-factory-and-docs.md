---
status: completed
spec: ["001"]
summary: Wire cfg.Aliases into NewModelRouter and ship operator docs (config.md, config.example.yaml, CHANGELOG.md)
execution_id: claude-code-router-aliases-exec-003-spec-001-factory-and-docs
dark-factory-version: v0.187.11
created: "2026-06-28T10:00:00Z"
queued: "2026-06-28T09:46:54Z"
started: "2026-06-28T09:52:25Z"
completed: "2026-06-28T09:54:07Z"
---

<summary>
- Wires the loaded `cfg.Aliases` from prompt 1 into the `NewModelRouter` constructor from prompt 2 inside `factory.CreateRouterFromConfig`.
- Updates `docs/config.md` with an `aliases:` schema section, semantics (single-hop, case-sensitive, validation behavior), and an updated full example.
- Updates `docs/config.example.yaml` with a commented `aliases:` block showing the four canonical short names.
- Adds a `## Unreleased` CHANGELOG bullet describing the operator-facing feature.
- After this prompt, the full feature is wired end-to-end and `make precommit` from prompt 1 + 2 still passes — no new test specs required here (the integration is exercised through `factory.CreateServer` boot in real use, and the unit tests from prompts 1 + 2 cover the seams).
</summary>

<objective>
Connect the config-side `cfg.Aliases` to the handler-side `NewModelRouter` via `factory.CreateRouterFromConfig`, and ship the operator-facing documentation (config reference, example config, changelog) so adding new aliases is a self-serve config edit. This is the final prompt for spec 001.
</objective>

<context>
Read first:
- `/workspace/specs/in-progress/001-add-model-aliases.md` — the full spec, especially "Desired Behavior" item 5 and "Acceptance Criteria" item 6 (Post-Install).
- `/workspace/pkg/factory/factory.go` — current `CreateRouterFromConfig`. The call `handler.NewModelRouter(routes, defaultHandler)` (line ~63) is the one wiring change.
- `/workspace/docs/config.md` — current schema, routing, auth, example. The new `aliases:` section slots in after "Routing" and before "Auth".
- `/workspace/docs/config.example.yaml` — current example. The `aliases:` block goes at the bottom (or top — see requirement 3 for placement guidance).
- `/workspace/CHANGELOG.md` — `## Unreleased` section already exists; append a new bullet, do NOT create a new version header.
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — for the bullet style this project uses.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md` — for factory-wiring conventions if you need a reference.
</context>

<requirements>

1. **Wire `cfg.Aliases` into `NewModelRouter`** in `pkg/factory/factory.go`. Change the existing line (around line 63):

   ```go
   modelRouter := handler.NewModelRouter(routes, defaultHandler)
   ```

   to:

   ```go
   modelRouter := handler.NewModelRouter(routes, defaultHandler, cfg.Aliases)
   ```

   That is the only code change in this file. `cfg.Aliases` may be nil (no `aliases:` block in the config) — `NewModelRouter` from prompt 2 handles nil identically to empty map.

2. **Confirm there are no other `NewModelRouter` call sites in non-test code.** Run:

   ```bash
   grep -rn 'NewModelRouter' /workspace --include='*.go'
   ```

   Expect exactly these matches:
   - `pkg/handler/model-router.go` (declaration + doc-comment mentions)
   - `pkg/handler/model-router_test.go` (test call sites — already updated in prompt 2)
   - `pkg/factory/factory.go` (the one production call site — updated in step 1 above)

   If a NEW call site appears (e.g. a `cmd/run-once/main.go` or similar), update it too with `nil` for the aliases argument and note it in your commit message context.

3. **Update `docs/config.md`** — add a new `## Aliases` section between the existing `## Routing` and `## Auth` sections. Use this exact content (you may adjust prose for flow but preserve the schema, semantics list, and example):

   ````markdown
   ## Aliases

   The optional top-level `aliases:` block maps short operator-typed model names to the full model identifier the upstream expects. Aliases are resolved router-side BEFORE provider routing — the upstream always sees the full model name.

   ```yaml
   aliases:
     qwen: qwen3.6:35b-a3b-coding-nvfp4
     minimax: MiniMax-M3-highspeed
     deepseek: deepseek-v4-flash-2025-12-01
     opus: claude-opus-4-7
   ```

   Then in any Claude Code session:

   ```
   > /model qwen      # router sees "qwen", rewrites body .model to "qwen3.6:35b-a3b-coding-nvfp4", routes via qwen* glob to ollama-local
   ```

   ### Semantics

   - **Single-hop.** If `aliases: {a: b, b: c}` and the request uses `model: a`, the upstream receives `model: b` — NOT `c`. The router resolves once and forwards.
   - **Case-sensitive.** `aliases["Qwen"]` and `aliases["qwen"]` are distinct entries (same byte-exact match as provider glob keys).
   - **Optional.** Configs without an `aliases:` block route exactly as before. Backward-compatible.
   - **Log line.** On a hit, the router logs `[alias] qwen -> qwen3.6:35b-a3b-coding-nvfp4` at glog `V(1)` — visible in `/tmp/claude-code-router.log` when the router runs with `-v=1` or higher.

   ### Validation

   | Condition | Behavior |
   |---|---|
   | Alias key equals a provider name (e.g. `aliases: { minimax: ... }` AND `providers: { minimax: ... }`) | **Error** at `config.Load` — daemon refuses to start. Operator must rename the alias key or the provider. |
   | Alias target matches no provider's `models:` glob (e.g. `aliases: { foo: typo-name }` where no provider lists `typo-name*`) | **Warning** at startup via glog (`[config] alias target "typo-name" (from alias key "foo") matches no provider glob`); config still loads. At runtime, requests using that alias get rewritten to the typo string and fall through to `default_provider`, which likely returns 404. Operator notices the warning at startup. |
   ````

   **Update the "Example — all four providers" section** to include an `aliases:` block. Append after the `ollama-local` provider block, at the same top level as `router:` and `providers:`:

   ```yaml
   aliases:
     qwen: qwen3.6:35b-a3b-coding-nvfp4
     minimax: MiniMax-M3-highspeed
     deepseek: deepseek-v4-flash-2025-12-01
     opus: claude-opus-4-7
   ```

   **Update the "Switching mid-session" section** — replace the existing three bullet lines under `/model` with:

   ```
   > /model qwen                   # alias → next request rewritten to qwen3.6:35b-a3b-coding-nvfp4, routed to ollama-local
   > /model minimax                # alias → next request rewritten to MiniMax-M3-highspeed, routed to minimax
   > /model claude-opus-4-7        # no alias match, glob routes to anthropic-subscription
   ```

   Keep the surrounding prose ("No router restart, no Claude Code restart...") unchanged.

4. **Update `docs/config.example.yaml`** — append an `aliases:` block at the bottom (after `ollama-local`):

   ```yaml

   # Short names that map to full model identifiers.
   # Operator-facing only — the upstream always sees the resolved full name.
   # Single-hop, case-sensitive. Add new entries here without code changes.
   aliases:
     qwen: qwen3.6:35b-a3b-coding-nvfp4
     minimax: MiniMax-M3-highspeed
     deepseek: deepseek-v4-flash-2025-12-01
     opus: claude-opus-4-7
   ```

   The blank line before `aliases:` is intentional — matches the file's spacing convention between top-level blocks.

5. **Add a `## Unreleased` CHANGELOG bullet.** Open `/workspace/CHANGELOG.md`; the `## Unreleased` section already exists (currently has 3 bullets about README and service docs). Append a new bullet at the END of the existing Unreleased bullets (before `## v0.4.0`):

   ```markdown
   - **Model aliases.** New optional `aliases:` block in `~/.claude-code-router/config.yaml` maps short names to full model identifiers (e.g. `qwen: qwen3.6:35b-a3b-coding-nvfp4`). Operator types `/model qwen`; the router rewrites the request body's `.model` field to the full name single-hop, before provider routing — the upstream always sees the full name. Validation: hard error on alias-key colliding with a provider name; glog warning when an alias target matches no provider glob. Configs without `aliases:` continue to load unchanged. See [docs/config.md#aliases](docs/config.md#aliases).
   ```

   Style matches the existing "Multi-provider routing via YAML config" bullet from v0.4.0 — bold lead phrase, then operator-facing behavior. Implementation detail (which packages changed) belongs in the commit message, not the changelog.

6. **Run `make precommit`** in the repo root. This is the final integration check: the config from prompt 1, the handler from prompt 2, and the factory wiring from this prompt all compose. Fix any lint / format / addlicense issues. The build must succeed and all tests must pass. **No additional manual smoke test in this prompt** — the wiring correctness is verified by the unit tests in prompts 1 + 2 (config parses with aliases; handler rewrites body when alias matches) plus the type-checker proving `cfg.Aliases` flows into `NewModelRouter`. The end-to-end operator smoke (real Claude Code session, real Ollama) is spec AC #6, scheduled post-merge.

</requirements>

<constraints>

- **Backward compatibility (from spec).** Configs without an `aliases:` block MUST continue to load and route exactly as today. `cfg.Aliases == nil` is the normal case; `NewModelRouter` treats nil and empty map identically.
- **No new test files in this prompt.** Prompts 1 + 2 cover the unit-level seams (config validation, handler rewrite). The factory wiring is a single line whose correctness is verified by the type checker (`cfg.Aliases` flows into `NewModelRouter`) plus the unit tests from prompts 1 + 2 exercising both ends of that pipe. Resist the urge to add a factory integration test — `CreateRouterFromConfig` returns an `http.Handler` whose alias path is already covered by `model-router_test.go`.
- **Doc accuracy.** The model identifiers in `docs/config.example.yaml` and `docs/config.md` MUST match real upstream model names — `qwen3.6:35b-a3b-coding-nvfp4`, `MiniMax-M3-highspeed`, `deepseek-v4-flash-2025-12-01`, `claude-opus-4-7` are the names cited in the spec. Do not invent placeholder names like `<MODEL>` or `your-model-here` — operators copy-paste from the example.
- **Do NOT commit.** dark-factory handles git.
- **Post-Install AC is operator-side.** The spec's AC 6 ("Live smoke test on the operator's macOS launchd-managed router after merge") is NOT in scope for this prompt — it runs after the PR is merged. This prompt's verification stops at `make precommit` passing.

</constraints>

<verification>

```bash
cd /workspace
make precommit
```

Must pass. Additionally:

```bash
cd /workspace
grep -n 'cfg.Aliases' /workspace/pkg/factory/factory.go
```

Expect one match: the `NewModelRouter(routes, defaultHandler, cfg.Aliases)` line.

```bash
grep -n '## Aliases\|^aliases:' /workspace/docs/config.md /workspace/docs/config.example.yaml
```

Expect: `## Aliases` section heading in `config.md`, `aliases:` block in both files.

```bash
grep -n 'Model aliases' /workspace/CHANGELOG.md
```

Expect one match — the new Unreleased bullet lead.

</verification>

---
status: completed
spec: [008-requires-leading-system-message]
summary: Added RequiresLeadingSystem []string field to Provider struct with load-time glob validation and five Ginkgo specs covering parse, backward compat, empty list, malformed pattern error, and regression guard
execution_id: claude-code-router-qwen38-system-exec-023-spec-008-config-requires-leading-system
dark-factory-version: dev
created: "2026-08-15T10:40:00Z"
queued: "2026-08-15T10:45:24Z"
started: "2026-08-15T10:45:25Z"
completed: "2026-08-15T10:48:22Z"
---

<summary>
- Operators can list, per provider, which model names reject a system message that is not the very first one in the conversation.
- The list is written as model-name glob patterns, the same syntax already used for provider model routing.
- A config that does not mention the new setting loads and behaves exactly as it does today; an empty list means the same as no list.
- A malformed pattern is caught when the config is loaded, not at request time: the router refuses to start and names both the provider and the offending pattern.
- Nothing about routing behaviour changes yet — this prompt only lands the config contract that the router change consumes.
- Test coverage proves the field parses, that absent and empty lists are equivalent, and that the bad-pattern error text carries the provider name and the pattern.
</summary>

<objective>
Add an optional per-provider list of model-name glob patterns named `requiresLeadingSystem` to the parsed config, validated at load time in the same place and style as the existing `models` globs. Config-layer only — no handler, factory, or docs changes in this prompt.
</objective>

<context>
Read first:
- `/workspace/specs/in-progress/008-requires-leading-system-message.md` — Desired Behavior 1 and 2; Acceptance Criteria 1 and 2; Constraints (backwards compatibility is absolute; config single source of truth).
- `/workspace/pkg/config.go` — the whole file. `Provider` struct (`Upstream string \`yaml:"upstream"\``, `Token string \`yaml:"token,omitempty"\``, `Models []string \`yaml:"models"\``) and `func (c *Config) Validate(ctx context.Context) error`, whose provider loop already validates each `prov.Models` entry with `path.Match(pattern, "")` and wraps the failure as `errors.Wrapf(ctx, err, "provider %q: invalid model glob %q", name, pattern)`. `errors` is `github.com/bborbe/errors`; `path` is the stdlib `path` package (NOT `path/filepath`, even though the GoDoc comment says filepath.Match syntax — the two are identical for patterns without separators).
- `/workspace/pkg/config_test.go` — existing Ginkgo patterns: package `pkg_test`, `write(yaml string) string` helper writing to a temp dir, `pkgcfg.Load(context.Background(), p)`, `Expect(err).To(MatchError(ContainSubstring(...)))`. The `Context("aliases", ...)` block is the closest model for the new block (parses-present / absent-backward-compat / error cases).
- `/workspace/docs/dod.md` — this project's Definition of Done, applied to every prompt: GoDoc on exported items, `bborbe/errors` conventions, Ginkgo/Gomega coverage, no `fmt.Printf`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `errors.Wrapf(ctx, err, ...)` idiom used throughout this file.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega spec structure.
</context>

<requirements>

1. **Add the field to the `Provider` struct in `/workspace/pkg/config.go`.** Append it after `Models`, with GoDoc:

   ```go
   // Provider describes one upstream LLM API.
   type Provider struct {
       // Upstream is the base URL, e.g. https://api.anthropic.com.
       Upstream string `yaml:"upstream"`
       // Token, if set, replaces the client's Authorization header with
       // "Bearer <Token>". If empty, the client's Authorization is
       // forwarded verbatim — used for the subscription-OAuth case.
       Token string `yaml:"token,omitempty"`
       // Models is the list of glob patterns (filepath.Match syntax) the
       // router uses to match request body's `model` field. Examples:
       // "claude-opus-*", "MiniMax-*", "qwen*".
       Models []string `yaml:"models"`
       // RequiresLeadingSystem lists glob patterns (same syntax as
       // Models) naming models behind this provider whose chat template
       // rejects a system-role message that is not the first entry of
       // the conversation. When the resolved model name matches one of
       // these patterns, the router lifts every out-of-place system
       // message into the top-level system block before forwarding.
       //
       // Scoped per model, never per provider: ollama's system-position
       // restriction lives in each model's chat template, so qwen3.6 and
       // qwen3.8 behave differently behind one provider (verified
       // 2026-08-15 with identical curl payloads against the same ollama
       // instance: qwen3.6 -> 200, qwen3.8 -> 500).
       //
       // Absent, nil, and empty are equivalent and all mean "never
       // transform anything for this provider".
       RequiresLeadingSystem []string `yaml:"requiresLeadingSystem,omitempty"`
   }
   ```

   The YAML key is the frozen literal `requiresLeadingSystem` — camelCase, deliberately unlike the snake_case `default_provider`. Do NOT rename it to `requires_leading_system`.

2. **Validate the patterns in `Validate`.** Inside the existing `for name, prov := range c.Providers` loop in `func (c *Config) Validate(ctx context.Context) error`, directly after the existing `for _, pattern := range prov.Models { ... }` block, add:

   ```go
   for _, pattern := range prov.RequiresLeadingSystem {
       // path.Match validates pattern syntax against a dummy string.
       if _, err := path.Match(pattern, ""); err != nil {
           return errors.Wrapf(ctx, err,
               "provider %q: invalid requiresLeadingSystem glob %q",
               name, pattern,
           )
       }
   }
   ```

   The error string must contain the provider name, the literal substring `requiresLeadingSystem`, and the offending pattern (Acceptance Criterion 2). Do not add a new validation helper function — this belongs in the same loop as the `Models` validation (spec Constraint: config single source of truth, validated by the config's own validation step).

3. **Do NOT add any warning for a pattern that matches no model.** The spec's Failure Modes row for "field present but matching no model the operator actually uses" specifies detection via the absence of `[system-lift]` log lines at runtime, not a startup warning. Do not extend `validateAliases` or add an analogous `validateRequiresLeadingSystem` warning pass.

4. **Add a `Context("requiresLeadingSystem", ...)` block to `/workspace/pkg/config_test.go`**, placed after the existing `Context("aliases", ...)` block, mirroring its structure (`write(...)` + `pkgcfg.Load(context.Background(), p)`). Required specs:

   - **It("parses a per-provider requiresLeadingSystem list")** — load exactly the config from Acceptance Criterion 1:

     ```yaml
     router:
       default_provider: anthropic-subscription
     providers:
       anthropic-subscription:
         upstream: https://api.anthropic.com
         models: ["claude-opus-*"]
       ollama-local:
         upstream: http://localhost:11434
         token: ollama
         models:
           - "qwen*"
         requiresLeadingSystem:
           - "qwen3.8*"
     ```

     Assert `err` is nil and `cfg.Providers["ollama-local"].RequiresLeadingSystem` equals `[]string{"qwen3.8*"}` (use `Expect(...).To(Equal([]string{"qwen3.8*"}))`, not just a length check — the exact value is the Acceptance Criterion 1 evidence). Also assert `cfg.Providers["anthropic-subscription"].RequiresLeadingSystem` is empty, proving the field is per-provider and does not bleed across providers.

   - **It("loads a config without requiresLeadingSystem — backward compat")** — a config where no provider declares the field loads with no error and `cfg.Providers["<p>"].RequiresLeadingSystem` is `BeEmpty()`.

   - **It("treats an explicit empty list as no patterns")** — `requiresLeadingSystem: []` loads with no error and the parsed value is `BeEmpty()` (spec Constraint: absent and empty list are equivalent).

   - **It("errors on a malformed requiresLeadingSystem pattern")** — a provider named `ollama-local` with `requiresLeadingSystem: ["["]`. Assert the returned error is non-nil and, in three separate `Expect` assertions, that its message contains `requiresLeadingSystem`, `[`, and `ollama-local`. Use `Expect(err).To(MatchError(ContainSubstring("requiresLeadingSystem")))` style, matching the existing malformed-glob spec at `Context("Load")`.

   - **It("still rejects a malformed models glob")** — regression guard: the pre-existing `models: ["[invalid"]` path still errors with `invalid model glob` and NOT with `requiresLeadingSystem`, proving the two validation loops emit distinct messages.

5. **Docs and CHANGELOG are deferred to prompt 3 of this spec** (`3-spec-008-wiring-and-docs.md`), which lands on the same branch and updates `docs/config.md`, `docs/config.example.yaml`, `README.md`, and `CHANGELOG.md` in one place. Do NOT edit any of those four files here — a partial entry would conflict with prompt 3's rewrite. If the Definition-of-Done review flags the missing documentation for this new YAML field, report it as deferred-by-design (naming prompt 3), not as a blocker. **Concretely: report `status: success` and note the deferral in the completion notes — do NOT report `status: partial`.** `docs/dod.md` opens by instructing you to "report any unmet criterion as a blocker", and its Documentation section will fire on this change; that instruction is overridden here by this explicit decomposition. A `partial` self-report fails a correct prompt.

6. **Do NOT touch** `/workspace/pkg/handler/`, `/workspace/pkg/factory/`, `/workspace/pkg/reloader/`, `/workspace/pkg/cli.go`, or `/workspace/main.go`. This prompt is config-layer only; the router-side consumer lands in prompt 2 and the factory wiring in prompt 3.

7. **Run `make precommit` in `/workspace`.** Must exit 0.

</requirements>

<constraints>
- **Frozen literal:** the YAML field name is `requiresLeadingSystem`. Do not rename, do not add an alias key, do not accept a snake_case spelling.
- **Backwards compatibility is absolute.** Every config in operator use today omits the new field and MUST keep loading and routing identically. Absent, nil, and empty list are equivalent.
- **Model-scoped, never provider-scoped.** Do NOT add a boolean provider-level switch (e.g. `fixSystemMessages: true`) — spec Non-goal, verified wrong: qwen3.6 and qwen3.8 share the `ollama-local` provider and disagree.
- **No global toggle, no environment variable, no CLI flag, no per-request opt-out header.** The pattern list is the only control surface (spec Non-goals).
- **Config single source of truth:** the field lives on the `Provider` struct and is validated inside `Config.Validate`; no parsing or validation logic anywhere else.
- **`bborbe/errors` conventions:** use `errors.Wrapf(ctx, err, ...)`; no bare `return err`, no `fmt.Errorf`.
- **GoDoc required** on the new exported struct field.
- **Existing behaviour must not regress:** alias validation, `default_provider` checks, tilde expansion, `FindConfigDir`, and every existing spec in `pkg/config_test.go` keep passing.
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
grep -n 'RequiresLeadingSystem \[\]string `yaml:"requiresLeadingSystem,omitempty"`' pkg/config.go
```

Must return exactly 1 line.

```bash
cd /workspace
grep -n 'invalid requiresLeadingSystem glob' pkg/config.go
```

Must return exactly 1 line (the validation error in `Validate`).

```bash
cd /workspace
grep -c 'requiresLeadingSystem' pkg/config_test.go
```

Must return ≥4 (the four new specs' YAML fixtures plus the error-substring assertion).

```bash
cd /workspace
go test ./pkg/... -run TestSuite -ginkgo.v 2>&1 | tail -40
```

Expect: all existing specs PASS plus the five new `Context("requiresLeadingSystem", ...)` specs PASS.

```bash
cd /workspace
git diff --name-only -- pkg/
```

Expect exactly two files: `pkg/config.go` and `pkg/config_test.go`.

Scope to `pkg/` deliberately — an unscoped `git diff` also reports dark-factory's own bookkeeping churn (it rewrites spec refs in `prompts/completed/*.md` on startup) and `make precommit` side effects (`go mod tidy -e`, `rm -rf mocks && go generate`, `addlicense -y $(date +%Y)`), any of which would make this gate report extra files and read as a failure on a correct implementation.

</verification>

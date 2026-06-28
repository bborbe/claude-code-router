---
status: completed
spec: ["001"]
summary: 'Added aliases: YAML block to pkg/config with collision validation and orphan-target warning, plus 4 Ginkgo specs'
execution_id: claude-code-router-aliases-exec-001-spec-001-config-aliases
dark-factory-version: v0.187.11
created: "2026-06-28T10:00:00Z"
queued: "2026-06-28T09:46:54Z"
started: "2026-06-28T09:46:55Z"
completed: "2026-06-28T09:49:49Z"
---

<summary>
- Adds an `aliases:` YAML block to the router config so operators can map short names to full model strings.
- Existing configs (no `aliases:` block) keep loading unchanged — the field is optional.
- Hard error if an alias key collides with a provider name (the router would not know which to dispatch to).
- Soft warning emitted via `glog.Warningf` (config still loads) if an alias target matches no provider glob — the operator sees the typo at startup, not at first request.
- Ginkgo specs cover: happy path, collision error, orphan-target warning (captured via stderr-redirected glog output, per spec AC #3), backward-compat with absent block.
</summary>

<objective>
Extend `pkg/config` to accept and validate a declarative `aliases: <short>: <full-model>` map in the YAML config. The map is consumed in later prompts by the model router. This prompt produces only the schema + validation + tests — no handler or factory wiring.
</objective>

<context>
Read first (in this order):
- `/workspace/CLAUDE.md` (if present) for project conventions.
- `/workspace/specs/in-progress/001-add-model-aliases.md` — the full spec.
- `/workspace/pkg/config/config.go` — current `Config`, `Router`, `Provider`, `Load`, `Validate`.
- `/workspace/pkg/config/config_test.go` — existing Ginkgo style for this package (`Describe("Config")`, `Context("Load")`, helpers `write(yaml)`, etc.).
- `/workspace/pkg/config/config_suite_test.go` — Ginkgo bootstrap (external test package `config_test`).
- `/workspace/pkg/handler/model-router.go` — to understand the `model`-field glob matching semantics that `path.Match` uses (mirror the same `path.Match` call in alias-target validation).
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — for `glog.Warningf` usage conventions.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo + Gomega conventions used in this repo.
</context>

<requirements>

1. **Add the `Aliases` field to `Config`** in `pkg/config/config.go`:

   ```go
   // Aliases maps a short operator-typed model name to the full
   // model string the upstream expects. Resolved single-hop before
   // glob-routing: a request body `{"model":"qwen"}` becomes
   // `{"model":"qwen3.6:35b-a3b-coding-nvfp4"}` before the router
   // walks providers' models globs. Nil / empty map = no-op.
   Aliases map[string]string `yaml:"aliases,omitempty"`
   ```

   Add the field after `Providers`. Keep the YAML tag as shown.

2. **Extend `Validate()` inline — no helper extraction.** Open `pkg/config/config.go`, find the existing `func (c *Config) Validate() error`. Append the alias checks at the bottom of the existing function body, BEFORE the trailing `return nil`. Do NOT introduce a `validateWithWarnings` helper — the spec's AC #3 explicitly demands the warning be asserted "via captured glog output," which requires the glog emission path to be traversed by the test. A helper that returns a `[]string` of warnings dodges that boundary. Keep `Validate` as a single function that emits `glog.Warningf` directly for non-fatal config smells and returns `error` for fatal ones.

3. **Alias-key collision check (fatal)** — append to `Validate`:

   ```go
   for aliasKey := range c.Aliases {
       if _, collides := c.Providers[aliasKey]; collides {
           return fmt.Errorf(
               "alias key %q collides with provider name", aliasKey,
           )
       }
   }
   ```

   Case-sensitive exact-string match on the YAML keys.

4. **Orphan alias-target warning (non-fatal)** — append to `Validate` after the collision loop:

   ```go
   for aliasKey, target := range c.Aliases {
       matched := false
       for _, prov := range c.Providers {
           for _, pattern := range prov.Models {
               if ok, _ := path.Match(pattern, target); ok {
                   matched = true
                   break
               }
           }
           if matched {
               break
           }
       }
       if !matched {
           glog.Warningf(
               `alias target %q (from alias key %q) matches no provider glob`,
               target, aliasKey,
           )
       }
   }
   ```

   Use this format string exactly — the spec's AC #3 demands the substring `alias target "<value>"` (test will `ContainSubstring` on the rendered output) and the substring `matches no provider`.

   Add the `"github.com/golang/glog"` import to `pkg/config/config.go`.

5. **Add Ginkgo specs** to `pkg/config/config_test.go` inside the existing `Describe("Config")` block. Add a new `Context("aliases", func() { ... })` block with these specs:

   - **It("loads a config with an aliases block")** — write a config containing:
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
         models: ["qwen*"]
     aliases:
       qwen: qwen3.6:35b-a3b-coding-nvfp4
       opus: claude-opus-4-7
     ```
     Assert `err == nil`, `cfg.Aliases["qwen"] == "qwen3.6:35b-a3b-coding-nvfp4"`, `cfg.Aliases["opus"] == "claude-opus-4-7"`. (Both targets match their provider globs — no glog warning expected; tested separately below.)

   - **It("loads a config without an aliases block — backward compat")** — write a config with no `aliases:` key at all. Assert `Expect(cfg.Aliases).To(BeEmpty())`.

   - **It("errors when an alias key collides with a provider name")** — write a config with `aliases: { minimax: MiniMax-M3-highspeed }` AND `providers: { minimax: { upstream: ..., models: [MiniMax-*] } }`. Assert:
     ```go
     _, err := config.Load(p)
     Expect(err).To(MatchError(ContainSubstring(`alias key "minimax" collides with provider name`)))
     ```

   - **It("logs a glog warning when an alias target matches no provider glob")** — captures `os.Stderr` for the duration of the `config.Load` call, asserts the captured output contains both `alias target "bar-1"` and `matches no provider`. Pattern:

     ```go
     It("logs a glog warning when an alias target matches no provider glob", func() {
         // Force glog to stderr for this test (default is also stderr in tests,
         // but be explicit so the assertion is hermetic).
         _ = flag.Set("logtostderr", "true")

         // Redirect os.Stderr to a pipe we can read.
         oldStderr := os.Stderr
         r, w, err := os.Pipe()
         Expect(err).NotTo(HaveOccurred())
         os.Stderr = w

         p := write(`
router:
  default_provider: anthropic
providers:
  anthropic:
    upstream: https://api.anthropic.com
    models: ["claude-*"]
aliases:
  foo: bar-1
`)
         _, loadErr := config.Load(p)
         glog.Flush()

         // Restore stderr + drain the pipe.
         Expect(w.Close()).To(Succeed())
         os.Stderr = oldStderr
         captured, _ := io.ReadAll(r)

         Expect(loadErr).NotTo(HaveOccurred())
         Expect(string(captured)).To(ContainSubstring(`alias target "bar-1"`))
         Expect(string(captured)).To(ContainSubstring("matches no provider"))
     })
     ```

     Add imports to `config_test.go` as needed: `"flag"`, `"io"`, `"os"`, `"github.com/golang/glog"`. If `goimports-reviser` re-sorts them during `make precommit`, that's fine — accept the canonical ordering.

6. **Run `make precommit`** in the repo root. Fix any lint / format / addlicense issues that surface. The file-header BSD copyright comment is auto-added by `make precommit`'s addlicense step — do not write it manually.

</requirements>

<constraints>

- **Backward compatibility (from spec).** Configs without an `aliases:` block MUST continue to load and route exactly as before. `cfg.Aliases == nil` is the normal case; downstream code must treat nil and empty map identically.
- **Single-hop only (from spec).** Validation does NOT check or resolve alias-of-alias chains. If `aliases: {a: b, b: c}`, both targets get the orphan-target check independently against provider globs — `b` is not resolved to `c` during validation.
- **Case-sensitive (from spec).** Alias key collision check and `path.Match` glob check are both byte-exact / case-sensitive — same semantics as existing provider routing.
- **No `validateWithWarnings` helper.** The spec's AC #3 boundary is `glog.Warningf`. A returns-warnings helper bypasses that boundary; reject the refactor even if it would be more unit-test-friendly.
- **No new dependencies.** `glog` and `path` are already in scope via the handler package or stdlib. Do NOT add a new module.
- **Do NOT commit.** dark-factory handles git.
- **Existing tests must still pass.** The existing `Context("Load")` specs in `config_test.go` must continue to pass unchanged after the `Aliases` field + validation additions.

</constraints>

<verification>

```bash
cd /workspace
make precommit
```

Must pass. Additionally verify:

```bash
cd /workspace
go test ./pkg/config/ -v -count=1 2>&1 | tail -50
```

Expect: all existing Ginkgo specs pass + the 4 new alias specs pass (loads with aliases, loads without aliases, collision error, glog warning captured).

Confirm `cfg.Aliases` is wired via a quick grep:

```bash
grep -n 'Aliases' /workspace/pkg/config/config.go
```

Expect at least the struct field declaration and the two validation loops referencing `c.Aliases`. NO occurrences of `validateWithWarnings` (that helper is explicitly rejected).

</verification>

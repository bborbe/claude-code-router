---
status: completed
spec: [013-model-pools]
summary: 'Added the model_pools: config contract — ModelPoolMember type, Config.ModelPools field, and validateModelPools with normative error messages (empty pool, unknown provider, negative weight, per-pool duplicate pairs) wired into Config.Validate, plus 10 yaml-boundary Ginkgo rows and a ## Unreleased CHANGELOG entry'
execution_id: claude-code-router-session-pinning-exec-042-spec-013-config-model-pools
dark-factory-version: dev
created: "2026-08-19T18:00:00Z"
queued: "2026-08-19T15:50:36Z"
started: "2026-08-19T16:15:43Z"
completed: "2026-08-19T16:20:32Z"
---

# Config: `model_pools:` schema + `ModelPoolMember` + validation

<summary>
- Operators can now declare a top-level `model_pools:` block that maps an invented model name (e.g. `coding`) to an ordered list of members, each naming a provider, a fixed concrete model string, an optional weight, and an optional overflow flag.
- Existing configs load unchanged: the new block is optional, the `aliases:` block keeps parsing exactly as before, and nothing in this prompt changes routing behavior.
- A member whose `provider` does not exist in `providers:` fails config load with an error naming both the pool and the provider.
- A member with a negative weight fails config load; an absent or zero weight becomes the default 1 (the same int-type resolution the sibling `upstreams:` weight already uses).
- Two members of the same pool declaring the same `(provider, model)` pair fail config load — an ambiguous pool is rejected up front, never silently resolved.
- A pool with an empty member list fails config load, so runtime resolution can never be asked to select from zero members.
- The `aliases:` block's "one name → one model" invariant is untouched; `model_pools:` is the separate "one name → a choice of models" concern.
</summary>

<objective>
Add the `model_pools:` config contract — the `ModelPoolMember` type, the `Config.ModelPools` map, and its validation rules (unknown provider, negative weight, duplicate pairs, empty pool) — so an operator can hand clients one stable invented model name backed by a configured choice of providers, while every existing config keeps loading byte-for-byte as before.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- Read `pkg/config.go` — the `Config` struct (fields: `Router`, `Providers`, `Aliases`, `Trace`, `Auth`, `AllowedApiKeys`, `ProviderOrder`; `Aliases` currently sits between `Providers` and `Trace`), the `Provider` struct, and `Config.Validate(ctx context.Context) error` with its per-provider loop and the `validateAllowedApiKeyClaims` / `validateAliases` helpers. The new `ModelPoolMember` type, `Config.ModelPools` field, and `validateModelPools` helper all live here. Error construction uses `errors.New(ctx, ...)` / `errors.Errorf(ctx, ...)` from `github.com/bborbe/errors` — never `fmt.Errorf` (see the existing `errors.New(ctx, fmt.Sprintf(...))` forms in `Config.Validate`). This prompt does NOT touch the `Provider`/`Upstream` machinery from spec 012 — that is already shipped.
- Read `pkg/config_test.go` — the `write()` helper, the `Context("Load")` / `Context("aliases")` / `Context("requiresLeadingSystem")` row shapes (`pkgcfg.Load(context.Background(), p)` then field assertions). The new `Context("model_pools")` rows follow the same shape.
- Read `docs/dod.md` — GoDoc on every new exported identifier, `bborbe/errors` conventions, Ginkgo/Gomega coverage.
- Coding plugin docs (in-container paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors` wrapping idiom.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — external `_test` packages, Ginkgo v2 + Gomega.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — GoDoc conventions.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-validation-framework-guide.md` — config validation placement in `Validate`.
</context>

<requirements>
1. **New `ModelPoolMember` type + `Config.ModelPools` field in `pkg/config.go`.** Add a new exported type and a new field to the `Config` struct (place `ModelPools` immediately AFTER the `Aliases` field, before `Trace`). Exact shape (comment text may be reworded; field names, types, and yaml tags are fixed):

   ```go
   // ModelPoolMember is one candidate of a model pool: the provider to
   // route through, the fixed concrete model string that provider sees,
   // the weight for session-pinned member selection (default 1), and
   // whether the member may overflow to a sibling when its provider is
   // saturated (default false). Model is the concrete string sent to
   // that provider — it may itself match the provider's models globs,
   // which is the normal case (spec 013).
   type ModelPoolMember struct {
       Provider string `yaml:"provider"`
       Model    string `yaml:"model"`
       Weight   int    `yaml:"weight,omitempty"`
       Overflow bool   `yaml:"overflow,omitempty"`
   }
   ```

   ```go
   // ModelPools maps an invented model name to an ordered list of
   // members (spec 013). A client that sends `model: <poolname>` gets
   // the request body's model field rewritten to one member's concrete
   // model and routed through that member's provider — the router picks
   // the member per session, the client never sees it. Unlike Aliases
   // (one name -> one model), a pool name maps to a choice of models.
   // Nil / empty map = no-op.
   ModelPools map[string][]ModelPoolMember `yaml:"model_pools,omitempty"`
   ```

   Do NOT add any other field, flag, or threshold (spec Non-goals: no health checks, no complexity-based routing, no per-request switching knobs).

2. **`validateModelPools` in `pkg/config.go` + call from `Config.Validate`.** Add `func (c *Config) validateModelPools(ctx context.Context) error` and call it from `Config.Validate` after the per-provider loop, mirroring the existing `validateAllowedApiKeyClaims` call site exactly:

   ```go
   if err := c.validateModelPools(ctx); err != nil {
       return errors.Wrapf(ctx, err, "validate model_pools")
   }
   ```

   Contract (spec AC 1 + Failure Modes rows 2 and 3):
   - Iterate `c.ModelPools` (Go map iteration; error messages name the pool, never rely on order). Use the same per-iteration `ctx.Done()` check the existing loops use.
   - **Empty member list** → return `errors.New(ctx, fmt.Sprintf("model pool %q: must declare at least one member", name))` (defensive guard so the resolver in prompt 2 never selects from zero members — a config mistake fails load instead of degrading silently).
   - For each member:
     - **Unknown provider** (spec DB row 2) → if `_, ok := c.Providers[member.Provider]; !ok` → return `errors.New(ctx, fmt.Sprintf("model pool %q: unknown provider %q", name, member.Provider))`. This message is normative — the verification greps it.
     - **Weight** → if `member.Weight == 0` set `member.Weight = 1` — write back into the slice element IN PLACE by index (`c.ModelPools[name][i].Weight = 1`; iterate by index, NOT `for _, member := range` — a range copy loses the mutation and the defaulted weight would not persist into the config the tests assert on). Absent / zero means the default — with a plain `int` field yaml.v3 cannot distinguish `weight: 0` from an absent key, exactly the same resolution the sibling `Upstream.Weight` uses; an explicitly declared `weight: 0` is therefore the default 1, NOT an error. If `member.Weight < 0` → return `errors.New(ctx, fmt.Sprintf("model pool %q: member weight must be > 0 (got %d)", name, member.Weight))`. This message is normative — the verification greps it.
     - **Duplicate (provider, model) pair** (spec DB row 3) → if the pair `(member.Provider, member.Model)` was already seen in THIS pool → return `errors.New(ctx, fmt.Sprintf("model pool %q: duplicate member (provider %q, model %q)", name, member.Provider, member.Model))`. A pair repeated across TWO different pools is not a duplicate — the scope is per-pool.
   - Do NOT validate `member.Model` against any provider glob — the member's model is the concrete string sent to that provider directly and needs no glob relation (spec Constraints). Do NOT touch `Aliases` validation — `validateAliases` stays byte-for-byte as-is.

3. **Config tests in `pkg/config_test.go`** (package `pkg_test`, Ginkgo v2 + Gomega, using the existing `write()` helper and `pkgcfg.Load`). Add a new `Context("model_pools")` block. These are yaml-boundary tests — a wrong tag would silently leave `ModelPools` nil or a member field zeroed, so they MUST go through `Load`, not struct literals. A valid-pool fixture needs the referenced providers declared in `providers:` (validation checks they exist), e.g.:
   ```yaml
   router:
     default_provider: anthropic
   providers:
     anthropic:
       upstream: https://api.anthropic.com
       models: ["claude-*"]
     deepseek-pool:
       upstream: https://vllm.example.com
       token: "vllm-token"
       models: ["deepseek-*"]
     minimax-pool:
       upstream: https://api.minimax.io/anthropic
       token: "minimax-token"
       models: ["MiniMax-*"]
   model_pools:
     coding:
       - provider: deepseek-pool
         model: deepseek-v4-flash
         weight: 2
         overflow: true
       - provider: minimax-pool
         model: MiniMax-2.7
   ```
   Rows:
   - **AC 1 — parses a valid `model_pools:` block:** the fixture above loads with no error; `cfg.ModelPools["coding"]` has length 2; member 0 is `{Provider: "deepseek-pool", Model: "deepseek-v4-flash", Weight: 2, Overflow: true}`; member 1 is `{Provider: "minimax-pool", Model: "MiniMax-2.7", Weight: 1, Overflow: false}` (both defaulted).
   - **AC 1 — absent weight defaults to 1 / absent overflow defaults to false:** a pool with two members, one declaring `weight: 1` and one declaring no weight at all → both `Weight == 1`; a member with no `overflow:` → `Overflow == false`.
   - **AC 1 — `weight: 0` is the default 1, not an error:** a member with `weight: 0` loads with `Weight == 1` (same int-type resolution as the sibling `upstreams:` weight — documented rationale in the row comment).
   - **AC 1 — backward compat:** a config with BOTH an `aliases:` block and a `model_pools:` block loads both correctly (aliases parse exactly as today); a config with neither block loads unchanged (`ModelPools` nil, `Aliases` empty).
   - **DB 2 — unknown provider rejected:** change one member's `provider` to `nope` → load error containing the pool name and `nope` (assert the `unknown provider` substring and the pool name).
   - **DB 3 — duplicate (provider, model) pair rejected:** two members both `{provider: deepseek-pool, model: deepseek-v4-flash}` (different weights) → load error containing `duplicate` and the pool name.
   - **Weight misconfiguration rejected:** a member with `weight: -1` → load error containing `weight` and the pool name (assert the `weight must be > 0` substring).
   - **Empty pool rejected:** `coding: []` → load error containing `at least one member`.
   - **Same pair in TWO different pools is NOT a duplicate:** pool `coding` and pool `review` both declare `(deepseek-pool, deepseek-v4-flash)` → loads successfully (the duplicate check is per-pool, not global).
   - Existing `Context("Load")`, `Context("aliases")`, `Context("requiresLeadingSystem")`, and every other row must still pass unchanged (the new field and validation must not disturb them).
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Config schema is fixed by the spec: `Config` gains `ModelPools map[string][]ModelPoolMember` (yaml `model_pools`, `omitempty`); `ModelPoolMember` = `{Provider string, Model string, Weight int, Overflow bool}` (yaml `overflow`, `omitempty`) (spec Constraints). Do NOT add other fields, flags, or thresholds (spec Non-goals: no health checks / circuit breakers, no complexity-based routing, no per-request switching).
- `model_pools:` is a separate concern from `aliases:` — the aliases invariant "one name → one model" is preserved; do NOT merge, alias, or cross-validate the two blocks beyond what requirement 2 states (spec Summary, Non-goals).
- `weight` defaults to 1; a negative weight is rejected at validation. Because `Weight` is a plain `int`, `weight: 0` is indistinguishable from an absent key and therefore means the default 1 — only negative weights fail load (spec AC 1 "weight ≤ 0 rejected; absent weight defaults to 1" read against the `int` type, same resolution the sibling spec-012 `Upstream.Weight` already uses).
- A pool member's `provider` must name an existing provider (validated); a member's `model` is the concrete string sent to that provider and may itself match that provider's globs — that is the normal case, NOT a validation error (spec Constraints).
- This is the config-contract prompt only — do NOT touch `pkg/handler/`, `pkg/factory/factory.go`, `main.go`, or `pkg/cli.go`. Resolution and factory wiring are spec-013 prompt 2.
- Spec 013 builds on the sibling spec-012 session-pinning-pools machinery, whose prompts (`1-`..`4-spec-012-*`) execute first in this queue — `model_pools:` config is independent of that machinery, but keep the resolution-layer prompts' contracts in mind: the `Weight` field feeds the prompt-2 weighted ring hash.
- Use `github.com/bborbe/errors` for error construction; never `fmt.Errorf` directly for wrapped context (mirror the `errors.New(ctx, fmt.Sprintf(...))` pattern already used in `Config.Validate`).
- No new dependencies — `gopkg.in/yaml.v3` is already in use.
- No AI attribution in code or comments.
- `make precommit` must remain green — run it before declaring done.
- Follow `docs/dod.md` (GoDoc on every new exported identifier).
- Do NOT touch `docs/` or `CHANGELOG.md` in this prompt — spec-013 prompt 3 owns documentation.
</constraints>

<verification>
make precommit

# AC 1 — schema landed:
grep -n 'type ModelPoolMember struct\|ModelPools map\[string\]\[\]ModelPoolMember' pkg/config.go

# AC 1 — validation landed:
grep -n 'func (c \*Config) validateModelPools\|unknown provider\|weight must be > 0\|duplicate member\|at least one member' pkg/config.go

# Config tests reference model_pools (spec AC 1 evidence):
grep -c 'model_pools' pkg/config_test.go   # expect >=1

# model_pools rows pass (ginkgo focus):
go test -mod=mod -count=1 ./pkg/ -ginkgo.focus='model_pools'

# Full suite:
go test -mod=mod -count=1 ./pkg/...
</verification>

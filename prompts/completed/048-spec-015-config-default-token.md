---
status: completed
spec: [015-global-default-provider-token]
summary: Landed the spec-015 top-level DefaultToken config field (default_token:) on Config in pkg/config.go with the frozen yaml tag + display:"length" redaction tag and zero validation change, plus six yaml-boundary Load rows in pkg/config_test.go (valid parse, empty string, non-scalar mapping/list rejection, backward-compat, coexistence with a provider token); per the prompt's explicit constraint, docs/ and CHANGELOG.md were left for spec-015 prompt 3.
execution_id: claude-code-router-global-token-exec-048-spec-015-config-default-token
dark-factory-version: dev
created: "2026-08-20T14:50:00Z"
queued: "2026-08-20T15:11:09Z"
started: "2026-08-20T15:22:27Z"
completed: "2026-08-20T15:28:33Z"
---

# Config contract: top-level `default_token:` (global default outbound key)

<summary>
- The config gains an optional top-level `DefaultToken` field (`default_token:` yaml key) — one shared outbound bearer key that prompt 2 wires as the fallback for every provider / pool member that declares no `token:` of its own.
- The field lives on the `Config` struct alongside the other top-level keys (`allowedApiKeys`, `trace`, `model_pools`) — NOT on `Router`, NOT on `Provider`, NOT on `Upstream` (the spec freezes the canonical location).
- The field carries the `display:"length"` redaction tag like every other secret config field (`Token`, `AllowedApiKeys`, `Upstream.Token`), so the key is never printed in startup logs.
- No validation changes: absent or empty = today's behavior (no global default); a non-scalar value (nested mapping or list) fails `Load` as a yaml parse error for free — `gopkg.in/yaml.v3` refuses to unmarshal a `!!map` / `!!seq` node into a `string` field.
- Existing configs load byte-identically with or without the field — the field is a plain optional string with no required-with interactions.
- Config tests: valid parse, empty-string, non-scalar mapping rejection, non-scalar list rejection, backward-compat absent, and coexistence with a provider-level `token:` — all via `Load` (yaml-boundary tests), satisfying AC 1.
- No factory, handler, docs, or CHANGELOG changes in this prompt — those are spec-015 prompts 2 and 3.
</summary>

<objective>
Land the spec-015 config contract: the optional top-level `DefaultToken` field (`default_token:`), its parse semantics (a non-scalar value fails `Load` with a parse error), and backward-compatible loading — with no change to validation and no interaction with existing provider / `Upstream` tokens.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- Read `pkg/config.go` — the `Config` struct (top-level fields `Router`, `Providers`, `Aliases`, `ModelPools`, `Trace`, `Auth`, `AllowedApiKeys`, `ProviderOrder`), the `UnmarshalYAML` method (captures `ProviderOrder`; decodes via `value.Decode((*plain)(c))` so any yaml decode error — including a non-scalar `default_token:` — propagates), `Load(ctx, rawPath)` (reads, `yaml.Unmarshal`, `Validate`; the `display:"length"` tag convention on `Token`, `AllowedApiKeys`, `AuthConfig.Key`, `Upstream.Token`), and `Validate` (this prompt makes NO change to it). Error construction uses `errors.New(ctx, ...)` / `errors.Wrapf(ctx, err, ...)` from `github.com/bborbe/errors` — but note this prompt adds no error paths at all, only the field.
- Read `pkg/config_test.go` — the `write()` YAML-file helper, `pkgcfg.Load(context.Background(), p)` rows, and especially the `Context("model_pools")` / `Context("allowedApiKeys")` / `Context("upstreams")` blocks, whose shape the new `Context("default_token")` block mirrors (yaml-boundary tests go through `Load`, never struct literals — a wrong yaml tag would silently leave the field empty). The file's import alias is `pkgcfg "github.com/bborbe/claude-code-router/pkg"`.
- The `display` struct tag is consumed by `github.com/bborbe/argument` (argument printing) and is the repo's convention for secret fields (v0.38.1 changelog: "add `display:\"length\"` redaction tags to every secret config field"). `DefaultToken` is a secret — it gets the tag.
- Coding plugin docs (in-container paths — the YOLO container has the coding plugin at these paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-parse-pattern.md` — yaml config parsing conventions.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — GoDoc conventions.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, external `_test` packages.
</context>

<requirements>
1. **New `DefaultToken` field on `Config` in `pkg/config.go`.** Add it to the `Config` struct between the `Providers` field and the `Aliases` field (field name, type, and yaml tag are FIXED by the spec; the comment text may be reworded):
   ```go
   // DefaultToken is the optional top-level shared outbound bearer key
   // (spec 015). Every provider — and every Upstream pool member — that
   // declares no token: of its own resolves its outbound Authorization to
   // Bearer <DefaultToken>; a provider/member token: overrides it; with
   // neither set, the client's Authorization header passes through
   // unchanged. Absent or empty = no global default, today's behavior.
   // The key is operator config read only at wiring — never from client
   // input — and flows only in the outbound Authorization header, never
   // into logs or trace files (redacted like every other token).
   DefaultToken string `yaml:"default_token,omitempty" display:"length"`
   ```
   Do NOT add the field anywhere else (spec Constraints: the canonical location is the top-level `default_token:`, NOT `token:` on the router block, NOT on `Provider`, NOT on `Upstream`). Do NOT add any other field, flag, opt-out, or threshold (spec Non-goals: no per-provider opt-out to force passthrough while a global default is set; no rotation; no secrets-management integration).

2. **No validation change in `pkg/config.go`.** Do NOT add any `Validate` rule for `DefaultToken`. The spec is explicit: "validation adds no required-with interactions", "Configs without `default_token:` load byte-identically", and "Absent or empty = no global default". A non-scalar `default_token:` (a nested mapping or a list) fails `Load` as a yaml PARSE error with zero code: `gopkg.in/yaml.v3` returns `cannot unmarshal !!map into string` / `cannot unmarshal !!seq into string` from `yaml.Unmarshal`, which `Load` wraps as "parse config <path>". Do NOT write a custom `UnmarshalYAML` for `Config` or `DefaultToken` — the stock decoder already rejects non-scalars.

3. **Config tests in `pkg/config_test.go`** (package `pkg_test`, Ginkgo v2 + Gomega, the existing `write()` helper + `pkgcfg.Load(context.Background(), p)`). Add a new `Context("default_token")` block. These are yaml-boundary tests — a wrong yaml tag would silently leave `DefaultToken` empty, so every fixture goes through `Load`, not struct literals. Fixture providers use `https://a.example` style URLs. Rows (each is one `It`):
   - **AC 1 — valid parse:** a config with `default_token: "sk-global-123"` at the top level (same indentation as `providers:` / `router:`) and two providers (one with a `token:`, one without) loads with no error; `cfg.DefaultToken` equals `"sk-global-123"`. (This row plus the fixtures below is what makes the AC 1 `grep -c 'default_token' pkg/config_test.go` evidence fire — the literal string `default_token` appears in the YAML fixture.)
   - **AC 1 — empty string:** `default_token: ""` loads with no error and leaves `cfg.DefaultToken` empty (empty = no global default, spec DB 1).
   - **AC 1 — non-scalar (nested mapping) rejected:** `default_token:` followed by an indented nested key (`default_token:\n  foo: bar`) → `Load` returns an error (the yaml parse fails; assert the error occurred, do not over-constrain the message — mirror the 014 window row's "assert the error occurred and names the offending value or reason" guidance).
   - **AC 1 — non-scalar (list) rejected:** `default_token:\n  - "a"\n  - "b"` (a YAML sequence) → `Load` returns an error.
   - **AC 1 — backward compat:** a config with NO `default_token:` anywhere loads unchanged with `cfg.DefaultToken` empty, and every existing provider field parses exactly as before (assert one provider's `Token` and one `Upstreams` member are unaffected).
   - **DB 1 — coexists with a provider token:** a config with BOTH a top-level `default_token:` AND a provider `token:` loads with both values intact (`cfg.DefaultToken == "sk-global-123"` and `cfg.Providers["x"].Token == "provider-key"`) — the two are independent at load time; the resolution (provider wins) happens at wiring in prompt 2, NOT here.
   - Existing `Context("Load")`, `Context("upstreams")`, `Context("model_pools")`, `Context("window")`, and every other row must still pass unchanged (the new field must not disturb them).

   IMPORTANT: this prompt must leave `pkg/config_test.go` containing at least one occurrence of the literal string `default_token` (the AC 1 evidence is `grep -c 'default_token' pkg/config_test.go` ≥ 1) — the YAML fixtures in the rows above guarantee this.

4. **Compile and format.** `go build ./...`, `go vet ./...`, and `gofmt` must be clean after this prompt. Do NOT add any new import to `pkg/config.go` (the field uses only the builtin `string` type and the existing `yaml`/struct-tag machinery).
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Config schema is fixed by the spec: `Config` gains top-level `DefaultToken string yaml:"default_token,omitempty"` — the canonical location. The alternative `token:` on the router block is NOT used, and the field does NOT go on `Provider` or `Upstream` (spec Constraints).
- Resolution order (provider/member `token:` WINS → global default → client Authorization passthrough) is FROZEN but is NOT implemented here — that is spec-015 prompt 2's factory wiring. This prompt is parse/load semantics only. Do NOT touch `pkg/factory/factory.go`, `pkg/handler/`, `main.go`, or `pkg/cli.go` (spec Suggested Decomposition: this is prompt 1, config contract).
- Do NOT add validation rules, a per-provider opt-out flag, or any other knob — the spec's Non-goals section hard-vetoes them ("If a consumer demands it, that is a separate spec").
- `DefaultToken` is a secret — it MUST carry `display:"length"` like `Token`, `AllowedApiKeys`, `AuthConfig.Key`, and `Upstream.Token` (v0.38.1 convention).
- The key never appears in logs, trace files, or /metrics — that redaction invariant is already enforced for every token-bearing field and is documented in `## Trace` / `## Auth`; this prompt adds the field to the same class (spec Security).
- Do NOT touch `docs/` or `CHANGELOG.md` in this prompt — spec-015 prompt 3 owns documentation.
- No AI attribution in code or comments. `make precommit` must remain green — run it before declaring done. Follow `docs/dod.md` (GoDoc on the new exported field).
</constraints>

<verification>
make precommit

# AC 1 — field landed on Config with the frozen yaml tag + redaction tag:
grep -n 'DefaultToken string `yaml:"default_token,omitempty" display:"length"`' pkg/config.go

# AC 1 evidence — config tests reference default_token:
grep -c 'default_token' pkg/config_test.go   # expect >=1

# default_token rows pass (ginkgo focus):
go test -count=1 ./pkg/ -ginkgo.focus='default_token'

# Full suite:
go test -mod=mod -count=1 ./pkg/...
</verification>

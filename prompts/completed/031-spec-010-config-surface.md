---
status: completed
spec: ["010"]
summary: 'Landed the config surface for spec 010 prompt 1: top-level Config.AllowedApiKeys and per-provider Provider.AllowedApiKeys fields, the registry-wins-wholesale/else-union AllowedApiKeySet() helper, and a duplicate-claim check in Config.Validate (via a private validateAllowedApiKeyClaims method) that rejects a key claimed by two providers with an error naming both, with 11 new Ginkgo boundary tests, docs (config.md, config.example.yaml, README) and a CHANGELOG ## Unreleased feat entry'
execution_id: claude-code-router-exec-031-spec-010-config-surface
dark-factory-version: dev
created: "2026-08-17T19:12:47Z"
queued: "2026-08-17T19:50:37Z"
started: "2026-08-17T19:50:39Z"
completed: "2026-08-17T19:59:30Z"
---

# Config surface: allowedApiKeys registry + per-provider override + validation

<summary>
- Operators can declare a top-level `allowedApiKeys` list (the auth registry) and an optional per-provider `allowedApiKeys` list (the routing pin) in the YAML config.
- A config with no `allowedApiKeys` anywhere loads and behaves exactly as it does today — no key enforcement, no key routing (feature-off default, non-breaking for the existing single-user localhost setup).
- The valid inbound key set is defined once: the top-level registry when non-empty, else the union of all providers' lists. A helper exposes this set so the auth gate (prompt 2) and routing (prompt 3) consume the same semantics.
- Two providers claiming the same key in their `allowedApiKeys` lists fail config load with an error naming the key and both providers — never a silent first-wins at runtime.
- A key may appear in both the top-level registry and a provider's list — that is the intended rotation/claim pattern and is not an error.
- The config keeps parsing the legacy `auth:` block in this prompt (no behavior change yet); rejecting it is prompt 2's migration guard.
</summary>

<objective>
Land the two `allowedApiKeys` config fields, the registry-union semantics helper, and the duplicate-claim validation in `Config.Validate()`, so prompts 2 (auth gate) and 3 (routing by key) consume a stable, tested config seam.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- Read `pkg/config.go` — the `Config` struct (line 29), the `Provider` struct (line 79), and `Config.Validate(ctx)` (line 130). The new fields and the set helper live here.
- Read `pkg/config_test.go` — the `Context("Load")` block's `write()` helper (line 34) and the `Context("auth")` block (line 341). The new fixtures follow the same shape. Do NOT modify the existing `auth` fixtures in this prompt — prompt 2 owns the legacy-auth rejection.
- Read `docs/dod.md` — single-source-of-truth rule: all config validation lives in `Config.Validate(ctx)`, never inline elsewhere.
- Error wrapping: use `github.com/bborbe/errors` (`errors.New(ctx, ...)` / `errors.Errorf(ctx, ...)` / `errors.Wrapf(ctx, err, ...)`), never `fmt.Errorf` — follow the existing style in `Config.Validate` and `Config.validateAliases` in `pkg/config.go`.
- Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `bborbe/errors` conventions.
- Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — external `_test` package, Ginkgo v2 + Gomega conventions.
- Do NOT touch `pkg/handler`, `pkg/factory`, or the request path in this prompt — this is config surface only.
</context>

<requirements>
1. Add a top-level `AllowedApiKeys` field to the `Config` struct in `pkg/config.go`, right after the `Auth` field (line 49). Exact shape:

   ```go
   // AllowedApiKeys is the top-level registry of API keys that authenticate
   // non-loopback /v1/* requests. It is also the single rotation point: a
   // key that appears here (or in any provider's list) authenticates the
   // caller, and a per-provider claim pins routing. Absent, null, and empty
   // are equivalent and all mean: no key enforcement and no key routing —
   // the /v1/* path behaves exactly as it does today. Keys are literal
   // strings, like provider token: fields.
   AllowedApiKeys []string `yaml:"allowedApiKeys,omitempty"`
   ```

2. Add a per-provider `AllowedApiKeys` field to the `Provider` struct in `pkg/config.go`, after `RequiresLeadingSystem` (line 105). Exact shape:

   ```go
   // AllowedApiKeys is this provider's routing pin: a request whose
   // presented x-api-key is in this list is dispatched to this provider
   // (its outbound token), overriding model-glob selection. A key may
   // appear in both the top-level registry and a provider's list — the
   // registry is the auth superset, the provider claim is the routing pin.
   // A key must NOT be claimed by more than one provider (validation
   // error, see Config.Validate). Absent, null, and empty all mean: this
   // provider claims no keys, so it is only reachable via glob routing.
   AllowedApiKeys []string `yaml:"allowedApiKeys,omitempty"`
   ```

3. Add a method on `*Config` (package `pkg`, in `pkg/config.go`) that computes the valid inbound key set per spec DB 2 — the top-level registry when non-empty, else the union of all providers' `allowedApiKeys`. Exact signature:

   ```go
   // AllowedApiKeySet returns the set of keys that authenticate
   // non-loopback /v1/* requests: the top-level registry when non-empty,
   // else the union of every provider's allowedApiKeys. The empty set
   // means auth is disabled and no key routing applies. This is the single
   // definition the auth middleware (prompt 2) and the key router (prompt 3)
   // consume — do not recompute the union elsewhere.
   func (c *Config) AllowedApiKeySet() map[string]struct{}
   ```

   The top-level registry wins wholesale when non-empty; it is NOT merged with provider lists (spec DB 2). Provider lists are only consulted when the top-level registry is empty.

4. Extend `Config.Validate(ctx)` in `pkg/config.go` with the duplicate-claim check (spec DB 1, AC 2): walk `c.Providers`, collect every provider's `AllowedApiKeys` into a `map[string]string` of key → first-claiming provider; when a key is seen a second time (in a DIFFERENT provider), return an error that names the key and BOTH providers. Use `errors.Errorf(ctx, ...)` per `go-error-wrapping-guide.md` (the repo's existing `router.default_provider` check at `pkg/config.go:137-141` uses `errors.New` + `fmt.Sprintf`; either passes the linters, but `errors.Errorf` is the guide's sanctioned form for a formatted new error). Error message shape, e.g. `allowedApiKeys key %q claimed by providers %q and %q` (formatted with the key, the first provider, the second provider). Order of iteration over `c.Providers` is a Go map and therefore random — the error must still name both providers regardless of which is encountered first, and the test must not depend on iteration order.
   - A key appearing in the top-level registry AND a provider's list is NOT a duplicate (spec DB 1) — only cross-provider claims are ambiguous. Do not reject it.
   - The same provider listing the same key twice in its own list is not a duplicate either (a single provider owning a key is not ambiguous) — only across distinct providers.
   - Do NOT reject whitespace-only or empty-string keys. Empty strings never match a presented non-empty header, and a whitespace-only entry is a literal key consistent with provider `token:` semantics — treat them as literals.

5. Tests in `pkg/config_test.go` (package `pkg_test`, Ginkgo v2 + Gomega, using the existing `write()` helper at line 34 and `pkgcfg.Load(context.Background(), p)`). These are the boundary tests — a struct-literal test does NOT satisfy them, because a wrong yaml tag would silently leave the field nil:
   - **Empty registry / feature-off default (AC 1, DB 1):** YAML with no `allowedApiKeys` anywhere loads with no error; `cfg.AllowedApiKeySet()` is empty; `cfg.AllowedApiKeys` is nil; every provider's `AllowedApiKeys` is nil.
   - **Explicit `null` and empty list are equivalent to absent:** YAML with `allowedApiKeys: null` at top level, and YAML with `allowedApiKeys: []` at top level, both load with an empty set and no error.
   - **Populated top-level registry (AC 1):** YAML with `allowedApiKeys: ["alpha", "beta"]` loads with `cfg.AllowedApiKeys == []string{"alpha", "beta"}` and `cfg.AllowedApiKeySet()` equal to `{"alpha", "beta"}`.
   - **Per-provider list only (AC 1):** YAML with no top-level registry but `providers.minimax.allowedApiKeys: ["dark-factory-key"]` loads; `cfg.AllowedApiKeySet()` equals `{"dark-factory-key"}` (the union of providers when the top-level registry is empty — spec DB 2).
   - **Both together (AC 1):** YAML with a populated top-level registry AND a per-provider list loads; `cfg.AllowedApiKeySet()` equals exactly the top-level set (the registry wins wholesale; provider lists are NOT merged in — spec DB 2).
   - **Union across multiple providers:** YAML with two providers each listing distinct keys and no top-level registry → `AllowedApiKeySet()` is the union of both lists.
   - **Duplicate claim rejected (AC 2):** YAML with two providers both listing `"k"` fails `Load` with an error whose text contains the key `"k"` AND both provider names. Also cover the inverted fixture where the second provider encountered (map order is random) is the other one, asserting the same two provider names appear regardless — do not assert on a positional string beyond "both names present".
   - **Top-level + per-provider same key is NOT rejected:** YAML with top-level `allowedApiKeys: ["k"]` and `providers.minimax.allowedApiKeys: ["k"]` loads with no error (spec DB 1).
   - **Same provider listing a key twice is NOT rejected:** YAML with `providers.minimax.allowedApiKeys: ["k", "k"]` loads with no error.
   - **Existing behavior unchanged:** the existing fixtures in `pkg/config_test.go` (multi-provider, requiresLeadingSystem, aliases, glob validation) must still pass. Do not alter the `Context("auth")` fixtures at line 341 — prompt 2 owns their migration.
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Do NOT touch `pkg/handler`, `pkg/factory`, `main.go`, or `pkg/cli.go` — no request-path behavior changes in this prompt. The fields, the set helper, and validation are the entire scope.
- Do NOT modify the legacy `Auth`/`AuthConfig` types or the `Context("auth")` fixtures in `pkg/config_test.go`. Rejecting a legacy `auth:` block is prompt 2's migration guard.
- Do NOT add validation beyond the duplicate-claim check — no key-format checks, no length limits, no opt-out flags, no per-key IDs, no rotation machinery (spec Non-goals).
- Use `github.com/bborbe/errors` for error wrapping. Never `fmt.Errorf`.
- Do NOT log any key value at any level.
- Do NOT add a new external dependency. Stdlib and the existing `bborbe/errors` are sufficient.
- `make precommit` must remain green. Run it before declaring done.
- Follow `docs/dod.md`: single-source-of-truth validation, GoDoc on every new exported identifier, Ginkgo/Gomega test coverage.
</constraints>

<verification>
make precommit

# Confirm the fields and their yaml tags landed:
grep -n 'allowedApiKeys' pkg/config.go

# Confirm the set helper exists:
grep -n 'func (c \*Config) AllowedApiKeySet' pkg/config.go

# Confirm the duplicate-claim check is in Validate (both provider names + the key):
grep -n 'claimed by providers' pkg/config.go

# Full config suite:
go test ./pkg/... -count=1
</verification>

---
status: completed
spec: [012-session-pinning-pools]
summary: Added Provider.upstreams pool schema (Upstream type, normalizeUpstreams in Config.Validate, UpstreamList synthesis) with backward-compatible legacy single-upstream normalization, full Ginkgo test coverage, and CHANGELOG Unreleased entry
execution_id: claude-code-router-session-pinning-exec-038-spec-012-config-upstreams
dark-factory-version: dev
created: "2026-08-19T17:10:00Z"
queued: "2026-08-19T15:50:36Z"
started: "2026-08-19T15:51:51Z"
completed: "2026-08-19T15:54:26Z"
---

# Config: `Provider.upstreams:` pool schema + legacy one-entry sugar

<summary>
- A provider can now declare a list of upstreams (`upstreams:`) instead of a single `upstream:`; each entry carries its own URL, token, weight, and per-server concurrency caps.
- Existing single-`upstream:` configs load unchanged: the provider-level `upstream:` / `token:` / `maxConcurrentRequests` / `maxConcurrentWaitSeconds` values become a one-entry pool with weight 1, so nothing an operator already has needs editing.
- Declaring both `upstream:` and `upstreams:` on the same provider is rejected at config load — the two forms are mutually exclusive.
- A weight of 1 is the default; a negative weight is rejected at config load so a typo cannot silently produce a broken pool.
- After load, every provider's pool is normalized and visible on the `Provider.Upstreams` field — tests and the factory can rely on a non-empty pool.
- No routing behavior changes yet: this prompt only establishes the config contract. Selection, concurrency, and factory wiring are later prompts in the same spec.
</summary>

<objective>
Add the `Provider.upstreams:` pool schema and the `Upstream` type to the config so a provider can describe multiple servers, while keeping every legacy single-`upstream:` config loading unchanged as a one-entry pool — this is the config contract the routing prompts build on.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- Read `pkg/config.go` — the `Provider` struct (fields: `Upstream`, `Token`, `Models`, `RequiresLeadingSystem`, `AllowedApiKeys`, `MaxConcurrentRequests`, `MaxConcurrentWaitSeconds`; `MaxConcurrentWaitSeconds` is currently the last field) and `Config.Validate(ctx context.Context) error` (its per-provider loop currently checks `prov.Upstream == ""` with the error `provider %q: upstream is required`, then validates the `Models` and `RequiresLeadingSystem` globs). The new `Upstream` type, `Provider.Upstreams` field, normalization step, and weight validation all live here.
- Read `pkg/config_test.go` — the `write()` helper, the `Context("Load")` and `Context("maxConcurrentRequests")` row shapes (`pkgcfg.Load(context.Background(), p)` then field assertions). The new `Context("upstreams")` rows follow the same shape.
- Read `docs/dod.md` — GoDoc on every new exported identifier, `bborbe/errors` conventions (`errors.New(ctx, ...)` / `errors.Errorf(ctx, ...)` / `errors.Wrapf(ctx, err, ...)`, never `fmt.Errorf` — see `Config.Validate` for the exact in-file forms), Ginkgo/Gomega coverage.
- Coding plugin docs (in-container paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors` wrapping idiom.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — external `_test` packages, Ginkgo v2 + Gomega.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — GoDoc conventions.
</context>

<requirements>
1. **New `Upstream` type + `Provider.Upstreams` field in `pkg/config.go`.** Add a new exported type and a new field to the `Provider` struct (place `Upstreams` after `MaxConcurrentWaitSeconds`, the current last field). Exact shape (comment text may be reworded; field names, types, and yaml tags are fixed):

   ```go
   // Upstream is one server in a provider's pool. When a provider
   // declares an `upstreams:` list, every /v1/* request for that
   // provider is dispatched to exactly one member: a request carrying an
   // x-session-id header is pinned to the same member every time
   // (weighted ring hash of the session id), a request without one goes
   // to the least-loaded member, and each member independently enforces
   // its own MaxConcurrentRequests cap. Weight defaults to 1 when absent
   // or 0; a negative weight is rejected at validation. The legacy single
   // `upstream:` form is sugar for a one-entry pool with Weight 1 whose
   // caps are the provider-level values (spec 012).
   type Upstream struct {
       Upstream                 string `yaml:"upstream"`
       Token                    string `yaml:"token,omitempty"`
       Weight                   int    `yaml:"weight,omitempty"`
       MaxConcurrentRequests    int    `yaml:"maxConcurrentRequests,omitempty"`
       MaxConcurrentWaitSeconds int    `yaml:"maxConcurrentWaitSeconds,omitempty"`
   }
   ```

   ```go
   // Upstreams is the pool of servers this provider routes to. When
   // present it wins over the legacy single Upstream field; validation
   // rejects a provider that sets both. Absent, the legacy form is
   // synthesized into a one-entry pool by normalizeUpstreams, so after
   // Load every provider has a non-empty Upstreams (spec 012).
   Upstreams []Upstream `yaml:"upstreams,omitempty"`
   ```

   Do NOT add any other field, flag, or threshold (spec Non-goals — no health checks, no weights beyond static ints, no pool-level failover).

2. **Normalize the legacy form in `pkg/config.go`.** Add `func (c *Config) normalizeUpstreams(ctx context.Context) error` and call it as the FIRST step of `Config.Validate` (before the existing auth/providers/default_provider checks and before the per-provider loop). Contract:
   - For each provider in `c.Providers`, in map order with the same per-iteration `ctx.Done()` check the existing loop uses:
     - If `prov.Upstream != "" && len(prov.Upstreams) > 0` → return `errors.New(ctx, ...)` with the exact message `provider %q: specify either `upstream` or `upstreams`, not both` (normative — the verification grep matches it).
     - If `len(prov.Upstreams) == 0` → synthesize the one-entry pool: `[]Upstream{{Upstream: prov.Upstream, Token: prov.Token, Weight: 1, MaxConcurrentRequests: prov.MaxConcurrentRequests, MaxConcurrentWaitSeconds: prov.MaxConcurrentWaitSeconds}}` and write it back into `c.Providers[name]`.
   - For every explicitly-declared `Upstreams` entry with `Weight == 0`, set `Weight = 1` (absent / zero weight means the default — with a plain `int` field yaml.v3 cannot distinguish `weight: 0` from an absent key, so 0 is the default, not a misconfiguration).
   - After this step every provider in `c.Providers` has a non-empty `Upstreams`, and the per-provider validation loop below only ever validates `Upstreams` entries.
   - This step is what makes `Load`-based configs expose the normalized pool: after `pkgcfg.Load`, `cfg.Providers[name].Upstreams` is non-empty even for a legacy single-`upstream:` config (spec AC 1).

3. **Rework the per-provider upstream check in `Config.Validate`.** Replace the existing `if prov.Upstream == "" { return errors.New(ctx, fmt.Sprintf("provider %q: upstream is required", name)) }` check with a loop over `prov.Upstreams` (normalization guarantees it is non-empty at this point). For each entry, with the same per-iteration `ctx.Done()` check:
   - If `up.Upstream == ""` → return the SAME error text as today, `provider %q: upstream is required` (existing config tests assert this substring — do not reword it).
   - If `up.Weight < 0` → return `errors.New(ctx, ...)` naming the provider and the offending weight, with the exact message `provider %q: upstream weight must be > 0 (got %d)` (normative — the verification grep matches it). Zero is NOT rejected — it was already normalized to the default 1 in requirement 2. Keep the `Models` and `RequiresLeadingSystem` glob checks exactly as they are.

4. **Add `Provider.UpstreamList()` in `pkg/config.go`** — the single-source synthesis the factory consumes (this prompt adds it; the factory wiring that calls it is prompt 3):
   ```go
   // UpstreamList returns the pool of upstreams this provider routes to:
   // the configured Upstreams when present, else the legacy single
   // upstream synthesized as a one-entry pool with Weight 1 and the
   // provider-level caps. Config.Validate already normalizes Load-ed
   // configs, so this is always the configured list there; the fallback
   // keeps programmatically-built configs (tests and direct
   // CreateRouterFromConfig callers that bypass Load) working with the
   // legacy single-upstream form.
   func (p Provider) UpstreamList() []Upstream
   ```

5. **Config tests in `pkg/config_test.go`** (package `pkg_test`, Ginkgo v2 + Gomega, using the existing `write()` helper and `pkgcfg.Load`). Add a new `Context("upstreams")` block. These are yaml-boundary tests — a wrong tag would silently leave `Upstreams` nil, so they MUST go through `Load`, not struct literals:
   - **AC 1 — legacy single upstream + provider caps load as a one-entry pool:** a provider block with `upstream: https://a.example`, `maxConcurrentRequests: 8`, `maxConcurrentWaitSeconds: 30`, and no `upstreams:` → loads with no error; `cfg.Providers["x"].Upstreams` has length 1; `Upstreams[0].Upstream == "https://a.example"`, `Upstreams[0].Weight == 1`, `Upstreams[0].MaxConcurrentRequests == 8`, `Upstreams[0].MaxConcurrentWaitSeconds == 30` (spec AC 1: "its caps land on the single entry").
   - **AC 1 — legacy single upstream with NO provider caps:** the single entry carries Weight 1 and zero caps (unlimited), matching today's uncapped behavior.
   - **AC 1 — a two-member `upstreams:` list:** each entry's `upstream`, `token`, `weight`, `maxConcurrentRequests`, `maxConcurrentWaitSeconds` parse into the right fields; the provider-level `Upstream` stays empty.
   - **Weight defaults to 1:** an entry with `weight: 0` (and one with the key absent) both load with `Upstreams[i].Weight == 1`.
   - **Weight misconfiguration is rejected:** an entry with `weight: -1` → load error containing `weight` and the provider name.
   - **Both forms rejected together:** a provider with both `upstream:` and `upstreams:` → load error containing the provider name (assert the conflict message substring).
   - **Provider with neither form:** still fails with `upstream is required` (the legacy single-entry path now surfaces it through the per-entry check).
   - **Boundary — `Provider.UpstreamList()` on a programmatically-built legacy provider:** `pkg.Provider{Upstream: "https://a.example", MaxConcurrentRequests: 8}.UpstreamList()` returns one entry `{Upstream: "https://a.example", Weight: 1, MaxConcurrentRequests: 8}` — this is the factory-side fallback for configs that bypass `Load` (every programmatic wiring test depends on it), so it is tested at the single-source synthesis directly, not only through `Load`'s normalization.
   - Existing `Context("Load")`, `Context("maxConcurrentRequests")`, and every other row must still pass unchanged (the provider-level cap fields keep parsing exactly as today).
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Config schema is fixed by the spec: `Provider` gains `Upstreams []Upstream` (yaml `upstreams`, `omitempty`); `Upstream` = `{Upstream string, Token string, Weight int, MaxConcurrentRequests int, MaxConcurrentWaitSeconds int}` (spec Constraints). Do NOT add other fields, flags, or thresholds (spec Non-goals).
- Legacy single-`upstream:` form is sugar for a one-entry pool — its provider-level `maxConcurrentRequests` / `maxConcurrentWaitSeconds` become the single entry's caps. When `upstreams:` is present it wins; validation rejects both set together (spec Constraints).
- `weight` defaults to 1; a negative weight is rejected at validation. Because `Weight` is a plain `int`, `weight: 0` is indistinguishable from an absent key and therefore means the default 1 — only negative weights fail load (spec DB 1 read with the `int` type).
- The per-upstream `maxConcurrentRequests` / `maxConcurrentWaitSeconds` semantics are the spec-011 ones (absent/0/negative = unlimited, wait ≤ 0 = 30s default), resolved at factory wiring, NOT validated at Load — do NOT add fail-closed checks on them (spec 011 precedent, spec 012 DB 5).
- No routing behavior changes in this prompt — do NOT touch `pkg/factory/factory.go`, `pkg/handler/`, `main.go`, or `pkg/cli.go`. Factory wiring of the pool is prompt 3.
- Use `github.com/bborbe/errors` for error construction; never `fmt.Errorf` directly for wrapped context (mirror the `errors.New(ctx, fmt.Sprintf(...))` pattern already used in `Config.Validate`).
- No new dependencies — `yaml.v3` is already in use.
- No AI attribution in code or comments.
- `make precommit` must remain green — run it before declaring done.
- Follow `docs/dod.md` (GoDoc on every new exported identifier).
- Do NOT touch `docs/` or `CHANGELOG.md` in this prompt — prompt 4 owns documentation.
</constraints>

<verification>
make precommit

# AC 1 — schema landed:
grep -n 'Upstreams \[\]Upstream\|type Upstream struct' pkg/config.go
grep -n 'func (c \*Config) normalizeUpstreams\|func (p Provider) UpstreamList' pkg/config.go

# AC 1 — backward-compat: legacy caps land on the single entry in Load-ed configs:
go test -mod=mod -count=1 ./pkg/ -ginkgo.focus='upstreams'

# Config tests reference the Upstreams field (spec AC 1 evidence):
grep -c 'Upstreams' pkg/config_test.go   # expect >=1

# Both-forms and negative-weight rejections exist:
grep -c 'not both\|weight must be > 0' pkg/config.go   # expect >=2

# Full suite:
go test -mod=mod -count=1 ./pkg/...
</verification>

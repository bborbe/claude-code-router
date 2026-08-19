---
status: completed
spec: [014-time-windowed-upstreams]
summary: 'Landed the spec-014 config contract: Window type, Upstream.Window and Provider.Window fields, normalizeUpstreams/UpstreamList window synthesis, provider-window-with-upstreams and both-boundaries validation, plus 11 Ginkgo config tests and a CHANGELOG Unreleased entry'
execution_id: claude-code-router-time-window-exec-045-spec-014-config-window
dark-factory-version: dev
created: "2026-08-19T20:25:00Z"
queued: "2026-08-19T20:42:15Z"
started: "2026-08-19T20:51:49Z"
completed: "2026-08-19T20:58:15Z"
---

# Config contract: per-upstream `window:` (time-of-day eligibility)

<summary>
- An `Upstream` pool entry can now declare an optional `window:` block with `from` / `until` values in the `"HH:MM <location>"` form (e.g. `"18:00 Europe/Berlin"`), each parsed by the existing `libtime.TimeOfDay` text unmarshaller — no separate timezone field, no default-location decision.
- The legacy single-`upstream:` provider form gains the same optional `window:` field, which `normalizeUpstreams` copies onto its implicit single member exactly like it already copies the provider-level caps — a one-member pool stays a pool.
- Malformed times (e.g. `"25:00 Europe/Berlin"`) and unknown IANA locations (e.g. `"18:00 Mars/Olympus"`) are rejected at config load, so a config mistake fails closed instead of silently misrouting.
- Overnight wrap (`from: "22:00"` `until: "06:00"`) parses and validates cleanly — wrapping is legal, not an error.
- A `window:` that declares only one boundary is rejected at load (both `from` and `until` are required when the block is present), and a provider-level `window:` combined with an `upstreams:` list is rejected (windows live on pool members; the provider-level field exists only for the legacy single-upstream form).
- Configs without any `window:` load byte-for-byte as today — the new fields are optional pointers, and every existing parse/validation row keeps passing.
- The programmatic `Provider.UpstreamList()` fallback also carries the provider-level window onto its synthesized member, so tests and direct `CreateRouterFromConfig` callers that bypass `Load` get the same shape.
- This prompt is config-contract only: parsing, normalization, and validation. No routing behavior changes; the eligibility filter is prompt 2.
</summary>

<objective>
Land the spec-014 config contract: the `Window` type, the optional `window:` block on both `Upstream` entries and the legacy provider form, its normalization onto the implicit single member, and load-time validation (malformed time, unknown location, missing boundary, provider-window-with-upstreams rejected) — with backward-compatible loading for configs that omit `window:`.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- Read `pkg/config.go` — the `Config` / `Router` / `Provider` / `Upstream` / `ModelPoolMember` structs, `Config.Validate(ctx context.Context) error` with its per-provider validation loop (the `for _, up := range prov.Upstreams` block that already rejects an empty `up.Upstream` and a negative `up.Weight`), `normalizeUpstreams(ctx)` (spec 012 — the legacy single-upstream synthesis and the `upstream` + `upstreams` mutual-exclusion check), and `Provider.UpstreamList()`. This prompt extends `Upstream` and `Provider` with the new window field, extends `normalizeUpstreams`, `UpstreamList`, and the per-upstream validation loop. Error construction uses `errors.New(ctx, ...)` / `errors.Errorf(ctx, ...)` from `github.com/bborbe/errors`, wrapping with `errors.Wrapf(ctx, err, ...)` — never bare `fmt.Errorf` (see the existing `errors.New(ctx, fmt.Sprintf(...))` forms). Every provider/upstream loop uses the per-iteration `ctx.Done()` check — mirror it.
- Read `pkg/config_test.go` — the `write()` YAML helper, `pkgcfg.Load(context.Background(), p)` rows, and especially the existing `Context("upstreams")` rows (lines ~890–1064) that assert the legacy-synthesis and per-entry field parsing. The new `Context("window")` rows follow the exact same shape.
- Read `docs/dod.md` — GoDoc on every new exported identifier, `bborbe/errors` conventions, Ginkgo/Gomega coverage.
- The `libtime` API (`github.com/bborbe/time` v1.27.6, already in go.mod): `libtime.TimeOfDay` is `struct { Hour, Minute, Second, Nanosecond int; Location *time.Location }` and implements `encoding.TextUnmarshaler` via `(*TimeOfDay).UnmarshalText([]byte)`, which routes through `libtime.ParseTimeOfDay(ctx, ...)` — a two-part string `"HH:MM <location>"` is parsed with the location attached to the value (the `"15:04"` / `"15:04Z07:00"` etc. layouts also parse). `libtime.LoadLocation(ctx, name)` wraps `time.LoadLocation` and errors on an unknown zone (e.g. `Mars/Olympus`). The zero value `libtime.TimeOfDay{}` has `Location == nil`, so a parsed midnight-in-UTC `"00:00 UTC"` (Location = UTC, non-nil) is distinguishable from an ABSENT field (nil Location) via `== (libtime.TimeOfDay{})` — this is how the "both boundaries required" check works.
- Coding plugin docs (in-container paths — the YOLO container has the coding plugin at these paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors` wrapping idiom.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-validation-framework-guide.md` — config validation placement in `Validate`.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — external `_test` packages, Ginkgo v2 + Gomega.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — GoDoc conventions.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md` — `github.com/bborbe/time` conventions.
</context>

<requirements>
1. **New `Window` type + `Upstream.Window` + `Provider.Window` in `pkg/config.go`.** Add `libtime "github.com/bborbe/time"` to the imports. Exact shapes (comment text may be reworded; field names, types, and yaml tags are fixed):
   ```go
   // Window is an optional per-upstream time-of-day eligibility window
   // (spec 014). A member is eligible for a dispatch only while "now" (the
   // router's injected clock, evaluated in the value's attached IANA
   // location) is inside [From, Until). From > Until wraps overnight (e.g.
   // 22:00 -> 06:00 covers 02:00 and excludes 14:00). A nil Window on an
   // Upstream means always eligible — today's behavior. Each value carries
   // its IANA location inline in the "HH:MM <location>" form (e.g. "18:00
   // Europe/Berlin"); libtime.ParseTimeOfDay handles it — there is no
   // separate timezone field and no default-location decision. Malformed
   // times and unknown locations fail at yaml parse; a Window missing
   // either boundary fails validation.
   type Window struct {
       From  libtime.TimeOfDay `yaml:"from"`
       Until libtime.TimeOfDay `yaml:"until"`
   }
   ```
   Add to the `Upstream` struct (after `MaxConcurrentWaitSeconds`, keeping the existing yaml tags pattern):
   ```go
       // Window, when set, restricts when this member is eligible: a member
       // whose window does not contain "now" is excluded from session
       // pinning and least-loaded selection (spec 014). Absent = always
       // eligible, today's behavior.
       Window *Window `yaml:"window,omitempty"`
   ```
   Add to the `Provider` struct (after `Upstreams`, keeping the existing comment style):
   ```go
       // Window is the legacy single-upstream form's eligibility window
       // (spec 014): when set, normalizeUpstreams copies it onto the
       // synthesized single member (a one-member pool is still a pool).
       // Providers that declare an upstreams: list carry windows per entry —
       // setting a provider-level window AND upstreams: is rejected.
       Window *Window `yaml:"window,omitempty"`
   ```
   Do NOT add any other field, flag, or threshold (spec Non-goals: no provider-level-window-for-upstreams-lists, no dynamic window changes, no clock plumbing).

2. **`normalizeUpstreams` changes in `pkg/config.go` (spec DB 6, Constraints).**
   a. Reject a provider-level window combined with an `upstreams:` list. In the existing per-provider loop, next to the `prov.Upstream != "" && len(prov.Upstreams) > 0` mutual-exclusion check, add:
      ```go
      if prov.Window != nil && len(prov.Upstreams) > 0 {
          return errors.New(ctx, fmt.Sprintf(
              "provider %q: window applies only to the legacy upstream form; set window on each upstreams entry instead",
              name,
          ))
      }
      ```
      (This error message is normative — a config test asserts a substring of it.)
   b. In the legacy single-upstream synthesis (the `len(prov.Upstreams) == 0` branch), add `Window: prov.Window` to the synthesized member, so the provider-level window lands on the implicit single member exactly like the provider-level caps already do:
      ```go
      prov.Upstreams = []Upstream{{
          Upstream:                 prov.Upstream,
          Token:                    prov.Token,
          Weight:                   1,
          MaxConcurrentRequests:    prov.MaxConcurrentRequests,
          MaxConcurrentWaitSeconds: prov.MaxConcurrentWaitSeconds,
          Window:                   prov.Window,
      }}
      ```
   c. Do NOT touch the existing `upstream` + `upstreams` mutual-exclusion check or the `Weight == 0` defaulting.

3. **`Provider.UpstreamList()` change in `pkg/config.go`.** In the programmatic fallback (the `len(p.Upstreams) == 0` branch), add `Window: p.Window` to the synthesized member so direct `CreateRouterFromConfig` callers and tests that bypass `Load` get the same window on the implicit single member as `Load`-normalized configs.

4. **Both-boundaries validation in `pkg/config.go` (spec AC 1).** In `Config.Validate`'s per-upstream loop (the `for _, up := range prov.Upstreams` block that already validates `up.Upstream` and `up.Weight`, with its `ctx.Done()` check), after the existing `up.Weight < 0` check add:
   ```go
   if up.Window != nil {
       if up.Window.From == (libtime.TimeOfDay{}) {
           return errors.New(ctx, fmt.Sprintf("provider %q: window.from is required", name))
       }
       if up.Window.Until == (libtime.TimeOfDay{}) {
           return errors.New(ctx, fmt.Sprintf("provider %q: window.until is required", name))
       }
   }
   ```
   Rationale for the zero-value test (document in a short comment): `libtime.TimeOfDay` is a comparable struct (`int` fields + `*time.Location` pointer); the zero value has `Location == nil`, which only an ABSENT yaml key produces — a parsed `"00:00 UTC"` carries `Location = UTC` (non-nil) and correctly passes. Malformed times and unknown locations need NO explicit check here — `libtime.TimeOfDay.UnmarshalText` already rejects them during `yaml.Unmarshal`, so `Load` fails before `Validate` runs. Do NOT add a "From == Until" rejection — `[From, Until)` with equal boundaries is an empty window (never eligible), defined in prompt 2.

5. **Config tests in `pkg/config_test.go`** (package `pkg_test`, Ginkgo v2 + Gomega, the existing `write()` helper + `pkgcfg.Load(context.Background(), p)`). Add a new `Context("window")` block. These are yaml-boundary tests — a wrong yaml tag would silently leave `Window` nil, so they MUST go through `Load`, not struct literals. Fixture providers use `https://a.example` style URLs. Rows (each is one `It`):
   - **AC 1 — parses a `window:` on an upstreams entry:** two-member `upstreams:` list, member 0 carries `window: {from: "08:00 Europe/Berlin", until: "18:00 Europe/Berlin"}`, member 1 has no window. Load succeeds; `cfg.Providers["x"].Upstreams[0].Window` is non-nil with `From.Hour == 8`, `From.Minute == 0`, `From.Location.String() == "Europe/Berlin"`, `Until.Hour == 18`, `Until.Location.String() == "Europe/Berlin"`; `Upstreams[1].Window` is nil. (This row is what makes the AC 1 `grep -c 'Window' pkg/config_test.go` evidence fire — reference the field `.Window` / type `pkgcfg.Window` here and below.)
   - **AC 1 / DB 6 — legacy single-upstream window:** a provider with only `upstream:` + `window: {from: "18:00 Europe/Berlin", until: "08:00 Europe/Berlin"}` loads; its normalized `Upstreams` has length 1 and `Upstreams[0].Window` is non-nil (From 18, Until 8, both Europe/Berlin), alongside the already-defaulted `Weight: 1`.
   - **AC 1 — backward compat:** a provider with no `window:` anywhere loads unchanged; every `Upstreams[i].Window` is nil.
   - **AC 1 — malformed time rejected:** `from: "25:00 Europe/Berlin"` (invalid hour) → `Load` errors (the yaml parse fails; assert the error occurred and names the offending value or reason — do not over-constrain the message).
   - **AC 1 — unknown IANA location rejected:** `until: "18:00 Mars/Olympus"` → `Load` errors (unknown time zone).
   - **AC 4 — overnight wrap accepted:** `window: {from: "22:00 Europe/Berlin", until: "06:00 Europe/Berlin"}` loads with no error (From 22, Until 6).
   - **Both boundaries required:** `window: {from: "08:00 Europe/Berlin"}` (no `until`) → `Load` error containing `window.until is required`; `window: {until: "18:00 Europe/Berlin"}` (no `from`) → `Load` error containing `window.from is required`.
   - **Provider-level window + upstreams rejected:** a provider declaring both `upstreams:` (a two-member list) and a provider-level `window:` → `Load` error naming the provider and containing `window applies only to the legacy upstream form`.
   - **`UpstreamList()` carries the provider window:** programmatic `pkgcfg.Provider{Upstream: "https://a.example", Window: &pkgcfg.Window{From: <parsed "08:00 Europe/Berlin">, Until: <parsed "18:00 Europe/Berlin">}}` → `prov.UpstreamList()[0].Window` is the same window (use `libtime.ParseTimeOfDay` from `github.com/bborbe/time` to build the values, or build them via a tiny helper). Also assert a window-less programmatic provider's `UpstreamList()[0].Window` is nil.
   - Existing `Context("Load")`, `Context("upstreams")`, `Context("model_pools")`, and every other row must still pass unchanged (the new fields and validation must not disturb them).

   IMPORTANT: this prompt must leave `pkg/config_test.go` containing at least one occurrence of the literal string `Window` (the AC 1 evidence is `grep -c 'Window' pkg/config_test.go` ≥ 1) — the `.Window` field references in the rows above guarantee this.

6. **`Window`/`libtime` import and compile.** `pkg/config.go` imports `libtime "github.com/bborbe/time"`. Do NOT add the stdlib `time` import in this prompt — the `Contains` method (prompt 2) is the only consumer of `time.UTC` and lives in prompt 2. `go build ./...` and `go vet ./...` must be clean after this prompt.
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Config schema is fixed by the spec: `Upstream` gains `Window *Window` (yaml `window`, `omitempty`); `Window = {From libtime.TimeOfDay, Until libtime.TimeOfDay}` (yaml `from` / `until`); the legacy single-`upstream:` form's window becomes the single member's window (normalized like caps in `normalizeUpstreams`, spec 012) (spec Constraints). Do NOT add other fields, flags, or thresholds (spec Non-goals).
- The window check (prompt 2) uses the router's existing injected `libtime.CurrentDateTimeGetter` — this prompt adds NO clock plumbing; the window is pure parsed data here.
- `window:` is eligibility-only config — it never widens access or bypasses auth (`allowedApiKeys` unchanged); a config mistake (malformed time / unknown location / missing boundary) fails validation at load, never silently misroutes (spec Security).
- No new dependencies — `github.com/bborbe/time` v1.27.6 is already in go.mod (spec Constraints). Do NOT touch `go.mod` / `go.sum`.
- This is the config-contract prompt only — do NOT touch `pkg/handler/`, `pkg/factory/factory.go`, `main.go`, or `pkg/cli.go`. The eligibility filter (`pkg.Window.Contains`, pool selection, provider fall-through) is spec-014 prompt 2; docs/CHANGELOG is prompt 3.
- Use `github.com/bborbe/errors` for error construction; never `fmt.Errorf` directly (mirror the existing `errors.New(ctx, fmt.Sprintf(...))` pattern in `Config.Validate`). Preserve the per-iteration `ctx.Done()` checks in every loop you touch.
- Do NOT add a "From == Until" validation — equal boundaries are an empty (never-eligible) window per the `[From, Until)` definition, handled in prompt 2's `Contains`.
- No AI attribution in code or comments.
- `make precommit` must remain green — run it before declaring done.
- Follow `docs/dod.md` (GoDoc on every new exported identifier).
- Do NOT touch `docs/` or `CHANGELOG.md` in this prompt — spec-014 prompt 3 owns documentation.
</constraints>

<verification>
make precommit

# AC 1 — schema landed:
grep -n 'type Window struct\|Window \*Window' pkg/config.go

# Validation landed (both-boundaries + legacy-form rejection + synthesis + UpstreamList):
grep -n 'window.from is required\|window.until is required\|window applies only to the legacy upstream form\|Window:                   prov.Window\|Window:                   p.Window' pkg/config.go

# AC 1 evidence — config tests reference Window:
grep -c 'Window' pkg/config_test.go   # expect >=1

# window rows pass (ginkgo focus):
go test -count=1 ./pkg/ -ginkgo.focus='window'

# Full suite:
go test -count=1 ./pkg/...
</verification>

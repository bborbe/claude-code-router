---
status: completed
spec: [017-weekday-window-upstreams]
summary: 'Landed the spec-017 days: config contract — added Days type with UnmarshalText + Contains, Upstream/Provider Days fields, normalizeUpstreams synthesis + rejection, UpstreamList fallback, and fail-closed days-only validation, with Ginkgo coverage and a CHANGELOG Unreleased entry'
execution_id: claude-code-router-weekday-exec-052-spec-017-config-days
dark-factory-version: dev
created: "2026-08-22T12:00:00Z"
queued: "2026-08-22T12:40:46Z"
started: "2026-08-22T12:40:48Z"
completed: "2026-08-22T12:44:59Z"
branch: dark-factory/weekday-window-upstreams
---

# Config contract: per-upstream `days:` (weekday eligibility)

<summary>
- An `Upstream` pool entry can now declare an optional `days:` weekday allow-list — a comma-separated list of lowercase English weekday names (`monday`..`sunday`) with an optional trailing inline IANA location, e.g. `"saturday, sunday Europe/Berlin"` — the weekday sibling of the spec-014 `window:` block.
- The value is parsed at config load into a structured `Days` set (which weekdays + which IANA location), exactly as the spec-014 `window:` parses its `"HH:MM <location>"` values at load — the trailing token is tried as a `time.LoadLocation`; if it loads it is the location, otherwise the whole value is the name list.
- Unknown weekday names (e.g. `funday`), an empty value (`days: ""`), and an empty name in the list are all rejected at config load, so a config typo fails closed instead of silently misrouting. A bare `days:` (null) stays absent = all days, byte-for-byte today.
- The legacy single-`upstream:` provider form gains the same optional `days:` field, copied onto its implicit single member by `normalizeUpstreams` and by `Provider.UpstreamList()` — the exact same synthesis as the spec-014 provider-level `window:`; a provider-level `days:` combined with an `upstreams:` list is rejected.
- A fail-closed location rule: a member with `days:` and no `window:` must carry the inline location on its `days:` value — config load rejects a days-only member whose `days:` has no location, so a days-only member can never silently resolve its weekday in UTC and drift from its sibling members' calendar.
- The config type gains `Days.Contains(now, window)` — the weekday-eligibility predicate that resolves the weekday in the attached location (inline `days:` location, else the member's `window:` from/until location, else UTC) — the prompt-2 eligibility filter and every fixed-clock test build on it.
- Configs without any `days:` load byte-for-byte as today; every existing parse/validation row keeps passing. This prompt is config-contract only — no routing behavior changes; the selection-time wiring is prompt 2.
</summary>

<objective>
Land the spec-017 config contract: the `Days` type, the optional `days:` block on both `Upstream` entries and the legacy provider form, its normalization onto the implicit single member, load-time validation (unknown name, empty value, fail-closed days-only location rule, provider-days-with-upstreams rejected), and the `Days.Contains` weekday predicate — with backward-compatible loading for configs that omit `days:`.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- Read `pkg/config.go` — the `Config` / `Router` / `Provider` / `Upstream` / `Window` structs, `Config.Validate(ctx context.Context) error` with its per-provider/per-upstream validation loops (the `for _, up := range prov.Upstreams` block that already rejects an empty `up.Upstream`, a negative `up.Weight`, and a window missing either boundary, each with a `ctx.Done()` check), `normalizeUpstreams(ctx)` (the legacy single-upstream synthesis with `Window: prov.Window` and the provider-window-with-upstreams rejection), and `Provider.UpstreamList()` (its fallback member with `Window: p.Window`). This prompt extends `Upstream` and `Provider` with the new `Days` field, extends `normalizeUpstreams`, `UpstreamList`, and the per-upstream validation loop, and adds the `Days` type + `UnmarshalText` + `Contains` next to `Window`/`Window.Contains`. Error construction uses `errors.New(ctx, ...)` / `errors.Errorf(ctx, ...)` / `errors.Wrapf(ctx, err, ...)` from `github.com/bborbe/errors` — never bare `fmt.Errorf` in pkg/ logic (inside the ctx-free `UnmarshalText`, use `errors.New(context.Background(), ...)` / `errors.Errorf(context.Background(), ...)` — the exact pattern `libtime.TimeOfDay.UnmarshalText` in `github.com/bborbe/time` uses, see `time_time-of-day.go`).
- Read `pkg/config_test.go` — the `write()` YAML helper, `pkgcfg.Load(context.Background(), p)` rows, `mustTOD`, `nowAt`, `berlinLoc`, and especially the `Context("window")` block (lines ~1187–1481) with its yaml-boundary rows and `DescribeTable("Contains", ...)` for `Window.Contains`. The new `Context("days")` rows and a `DescribeTable("Days.Contains", ...)` follow the exact same shape.
- The `libtime` API (`github.com/bborbe/time` v1.27.9, already in go.mod — do NOT touch go.mod/go.sum):
  - `type Weekdays []Weekday` with `func (w Weekdays) Contains(value Weekday) bool` and `func (w Weekdays) Weekdays() []stdtime.Weekday`.
  - `type Weekday stdtime.Weekday` with exported constants `Sunday` (0) .. `Saturday` (6) (file `time_weekday.go`) — the canonical set is the 7 lowercase English names `monday`..`sunday`, matching `time.Weekday.String()` lowercased.
  - `func (d DateTime) Weekday() Weekday` and `func (d DateTime) Time() stdtime.Time` (file `time_date-time.go`).
  - Precedent for a ctx-free config unmarshaler: `libtime.TimeOfDay.UnmarshalText` (file `time_time-of-day.go`) calls `ParseTimeOfDay(context.Background(), str)` and wraps errors with `errors.Wrapf(context.Background(), ...)`.
- Coding plugin docs (in-container paths — the YOLO container has the coding plugin at these paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors` wrapping idiom.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-validation-framework-guide.md` — config validation placement in `Validate`.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-enum-type-pattern.md` — set-of-values representation (`Weekdays` is the repo's `github.com/bborbe/time` set type; use it, do not invent a new set type).
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — external `_test` packages, Ginkgo v2 + Gomega.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — GoDoc conventions.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md` — `github.com/bborbe/time` conventions.
- Read `docs/dod.md` — GoDoc on every new exported identifier, `bborbe/errors` conventions, Ginkgo/Gomega coverage.
</context>

<requirements>
1. **New `Days` type + `UnmarshalText` + `Contains` in `pkg/config.go`.** Place them directly below the `Window` type / `Window.Contains` method. `pkg/config.go` already imports `stdtime "time"`, `strings`, `context`, and `libtime "github.com/bborbe/time"` — no import changes. Exact shapes (comment text may be reworded; type names, field names, method signatures, and yaml tags are fixed):
   ```go
   // weekdayNames maps the canonical lowercase English weekday names
   // (spec 017: Go time.Weekday.String() lowercased) to libtime.Weekday
   // values. No abbreviations, no ranges, no numeric indices — validation
   // rejects anything else at config load.
   var weekdayNames = map[string]libtime.Weekday{
       "sunday":    libtime.Sunday,
       "monday":    libtime.Monday,
       "tuesday":   libtime.Tuesday,
       "wednesday": libtime.Wednesday,
       "thursday":  libtime.Thursday,
       "friday":    libtime.Friday,
       "saturday":  libtime.Saturday,
   }

   // Days is an optional per-upstream weekday eligibility set (spec 017).
   // A member is eligible for a dispatch only while the weekday of "now"
   // (the router's injected clock) is in the set, where the weekday is
   // resolved in an explicit IANA location: the inline location on the
   // days value, else the member's window from/until location, else UTC.
   // The YAML value is a comma-separated list of lowercase English
   // weekday names (monday..sunday) with an optional trailing inline IANA
   // location, e.g. "saturday, sunday Europe/Berlin". A nil Days on an
   // Upstream means every day — today's behavior. Unknown names and an
   // empty value fail at yaml parse; a days-only member (no window:)
   // whose value carries no inline location fails validation (fail-closed,
   // so a days-only member can never silently resolve its weekday in UTC
   // and drift from its sibling members' calendar).
   type Days struct {
       // Weekdays is the allowed weekday set (spec 017).
       Weekdays libtime.Weekdays
       // Location is the IANA location the weekday is resolved in, from
       // the inline value ("saturday, sunday Europe/Berlin" ->
       // Europe/Berlin). Nil inherits the member's window location at
       // selection time (else UTC).
       Location *stdtime.Location
   }

   // UnmarshalText parses the "comma-separated weekday names, optional
   // trailing IANA location" form (spec 017 Constraints): the value is
   // split on the last whitespace and the last token is tried as a
   // stdtime.LoadLocation — if it loads it is the location and the
   // remainder is the name list; if it does not load the whole value is
   // the name list. Names are comma-separated, trimmed, and matched
   // against the 7 canonical lowercase names. An empty value and an
   // unknown name are errors, so the yaml parse fails and Load rejects
   // the config.
   func (d *Days) UnmarshalText(text []byte) error {
       value := strings.TrimSpace(string(text))
       if value == "" {
           return errors.New(context.Background(), "days: value is required — comma-separated weekday names (monday..sunday) with an optional trailing IANA location")
       }
       names := value
       if idx := strings.LastIndexAny(value, " \t"); idx >= 0 {
           if loc, err := stdtime.LoadLocation(strings.TrimSpace(value[idx+1:])); err == nil {
               d.Location = loc
               names = strings.TrimSpace(value[:idx])
           }
       }
       var weekdays libtime.Weekdays
       for _, part := range strings.Split(names, ",") {
           name := strings.TrimSpace(part)
           if name == "" {
               return errors.New(context.Background(), "days: empty weekday name in list")
           }
           weekday, ok := weekdayNames[name]
           if !ok {
               return errors.Errorf(context.Background(), "days: unknown weekday name %q (use monday..sunday)", name)
           }
           weekdays = append(weekdays, weekday)
       }
       d.Weekdays = weekdays
       return nil
   }

   // Contains reports whether the weekday of now is in the allowed set.
   // The weekday is resolved in the attached IANA location, in precedence
   // order: the inline days location, else the member's window from/until
   // location, else UTC — so the boundary is the location's calendar,
   // never the router host's local day (spec 017 DB 3). window may be
   // nil; callers only invoke this on a non-nil Days (a nil Days means
   // all days and is never consulted).
   func (d *Days) Contains(now libtime.DateTime, window *Window) bool {
       loc := d.Location
       if loc == nil && window != nil {
           loc = window.From.Location
       }
       if loc == nil && window != nil {
           loc = window.Until.Location
       }
       if loc == nil {
           loc = stdtime.UTC
       }
       return d.Weekdays.Contains(libtime.Weekday(now.Time().In(loc).Weekday()))
   }
   ```
   Note: `yaml.v3` decodes a scalar `days: "..."` into a `*Days` field by calling `(*Days).UnmarshalText` — the same mechanism that already parses `Window.From`/`Until` (`libtime.TimeOfDay`) — and leaves the field nil for an absent or null `days:`. Do NOT add any other field, flag, threshold, or `MarshalText` (configs are load-only; the reloader never marshals).

2. **`Upstream.Days` + `Provider.Days` fields in `pkg/config.go`.** Add to the `Upstream` struct after `Window`:
   ```go
       // Days, when set, restricts this member's eligibility to a weekday
       // subset: a comma-separated list of lowercase English weekday names
       // (monday..sunday) with an optional trailing inline IANA location,
       // e.g. "saturday, sunday Europe/Berlin". A member whose weekday is
       // not in the set is excluded from session pinning and least-loaded
       // selection (spec 017). Absent = every day, today's behavior. A
       // member with days: but no window: must carry the inline location —
       // validation rejects one without it.
       Days *Days `yaml:"days,omitempty"`
   ```
   Add to the `Provider` struct after `Window`:
   ```go
       // Days is the legacy single-upstream form's weekday eligibility set
       // (spec 017): when set, normalizeUpstreams copies it onto the
       // synthesized single member (a one-member pool is still a pool).
       // Providers that declare an upstreams: list carry days per entry —
       // setting a provider-level days AND upstreams: is rejected.
       Days *Days `yaml:"days,omitempty"`
   ```

3. **`normalizeUpstreams` changes in `pkg/config.go` (spec DB 5, "same synthesis as the spec-014 window").**
   a. Next to the existing provider-window-with-upstreams rejection, add the parallel days rejection:
      ```go
      if prov.Days != nil && len(prov.Upstreams) > 0 {
          return errors.New(ctx, fmt.Sprintf(
              "provider %q: days applies only to the legacy upstream form; set days on each upstreams entry instead",
              name,
          ))
      }
      ```
      (This error message is normative — a config test asserts a substring of it.)
   b. In the legacy single-upstream synthesis (`len(prov.Upstreams) == 0` branch), add `Days: prov.Days` to the synthesized member so the provider-level days lands on the implicit single member exactly like the provider-level window already does.
   c. Do NOT touch the existing `upstream` + `upstreams` mutual-exclusion check, the `Weight == 0` defaulting, or the window rejection.

4. **`Provider.UpstreamList()` change in `pkg/config.go`.** In the programmatic fallback (the `len(p.Upstreams) == 0` branch), add `Days: p.Days` to the synthesized member so direct `CreateRouterFromConfig` callers and tests that bypass `Load` get the same days on the implicit single member as `Load`-normalized configs.

5. **Fail-closed days-only rule in `Config.Validate` (spec AC 1, Constraints "Fail-closed location rule").** In `Config.Validate`'s per-upstream loop (the `for _, up := range prov.Upstreams` block that already validates `up.Upstream`, `up.Weight`, and `up.Window`), after the existing window both-boundaries checks, add:
   ```go
   if up.Days != nil && up.Window == nil && up.Days.Location == nil {
       return errors.New(ctx, fmt.Sprintf(
           "provider %q: days without a window requires an inline location (e.g. \"saturday, sunday Europe/Berlin\")",
           name,
       ))
   }
   ```
   (This message is normative — the AC evidence requires an error naming the missing location.) `up.Days.Location == nil` means the value carried no inline location: `"saturday, sunday"` (names only) or a value whose trailing token failed to load as a location. A member with `days:` AND a `window:` may omit the inline location — the window's `from`/`until` location governs at selection time — so the check is conditional on `up.Window == nil`. A legacy single-`upstream:` provider whose provider-level `days:` has no location and no provider-level `window:` is caught here through its synthesized member (requirement 3b).

6. **Config tests in `pkg/config_test.go`** (package `pkg_test`, Ginkgo v2 + Gomega, the existing `write()` helper + `pkgcfg.Load(context.Background(), p)`). Add a new `Context("days")` block AFTER the `Context("window")` block. These are yaml-boundary tests — a wrong yaml tag would silently leave `Days` nil, so they MUST go through `Load`, not struct literals. Fixture providers use `https://a.example` style URLs. Rows (each is one `It`), asserting `Days.Weekdays.Contains(...)` and `Days.Location`:
   - **AC 1 — parses `days:` on an upstreams entry:** two-member `upstreams:` list, member 0 carries `days: "saturday, sunday Europe/Berlin"`, member 1 has no days. Load succeeds; `cfg.Providers["x"].Upstreams[0].Days` non-nil with `Days.Weekdays.Contains(libtime.Saturday)` true, `Contains(libtime.Monday)` false, `Contains(libtime.Sunday)` true, and `Days.Location.String() == "Europe/Berlin"`; `Upstreams[1].Days` nil.
   - **AC 1 / DB 5 — legacy single-upstream days:** a provider with only `upstream: https://a.example` + provider-level `days: "monday, friday Europe/Berlin"` loads; its normalized `Upstreams` has length 1 and `Upstreams[0].Days` non-nil (`Contains(libtime.Monday)` true, `Contains(libtime.Saturday)` false, `Location.String() == "Europe/Berlin"`), alongside the already-defaulted `Weight: 1`.
   - **AC 1 — backward compat:** a provider with no `days:` anywhere loads unchanged; every `Upstreams[i].Days` is nil.
   - **AC 1 — unknown weekday name rejected:** `days: "funday Europe/Berlin"` on an upstreams entry → `Load` errors; `err.Error()` contains `funday` (the error names the invalid name).
   - **AC 1 — empty value rejected:** `days: ""` on an upstreams entry → `Load` errors (the yaml parse fails); `err.Error()` contains `days: value is required`.
   - **AC 1 / Constraints — days-only member without inline location rejected:** a member with `days: "saturday, sunday"` (no location) and NO `window:` → `Load` error containing `location`.
   - **DB / weekend all-day accepted:** a member with `days: "saturday, sunday Europe/Berlin"` and NO `window:` → Load succeeds; `Days` non-nil with `Location.String() == "Europe/Berlin"` and `Window` nil.
   - **DB / location inheritance accepted:** a member with `days: "monday, friday"` (no location) AND `window: {from: "08:00 Europe/Berlin", until: "18:00 Europe/Berlin"}` → Load succeeds; `Days` non-nil, `Days.Location` nil (the window's location governs at selection time), `Window` non-nil.
   - **DB / fail-closed through the legacy form:** a provider with only `upstream: https://a.example` + provider-level `days: "saturday, sunday"` (no window, no location) → `Load` error containing `location`.
   - **DB — provider-level days + upstreams rejected:** a provider declaring both `upstreams:` (a two-member list) and a provider-level `days:` → `Load` error naming the provider and containing `days applies only to the legacy upstream form`.
   - **DB — `UpstreamList()` carries the provider days:** programmatic `pkgcfg.Provider{Upstream: "https://a.example", Days: <parsed "saturday, sunday Europe/Berlin">}` where the `*pkgcfg.Days` is built via `(&pkgcfg.Days{}).UnmarshalText([]byte("saturday, sunday Europe/Berlin"))` → `prov.UpstreamList()[0].Days` non-nil with `Contains(libtime.Saturday)` true and `Location.String() == "Europe/Berlin"`. Also assert a days-less programmatic provider's `UpstreamList()[0].Days` is nil.
   - Existing `Context("Load")`, `Context("upstreams")`, `Context("window")`, `Context("model_pools")`, and every other row must still pass unchanged.

7. **`Days.Contains` unit tests in `pkg/config_test.go`** — extend the new `Context("days")` block with a `DescribeTable("Days.Contains", ...)` driving the pure predicate (mirroring the existing `Window.Contains` table). Add a date-aware `now` helper next to `nowAt`:
   ```go
   // atDate returns a fixed-clock DateTime for the given date/time in loc,
   // so weekday tests never depend on the wall clock.
   func atDate(y, mo, d, h, min int, loc *stdtime.Location) libtime.DateTime {
       return libtime.DateTime(stdtime.Date(y, stdtime.Month(mo), d, h, min, 0, 0, loc))
   }
   ```
   Table rows (fixed dates — 2026-08-21 is Friday, 2026-08-22 Saturday, 2026-08-23 Sunday, 2026-08-24 Monday; build the `*pkgcfg.Days` via `(&pkgcfg.Days{}).UnmarshalText([]byte(value))` inside the table function; build the `*pkgcfg.Window` via the existing `mustTOD` helper):
   - `"saturday, sunday Europe/Berlin"`, nil window, now `atDate(2026, 8, 22, 10, 0, berlinLoc)` (Sat) → true; same set, now `atDate(2026, 8, 24, 10, 0, berlinLoc)` (Mon) → false; same set, now `atDate(2026, 8, 23, 0, 1, berlinLoc)` (Sun 00:01) → true; same set, now `atDate(2026, 8, 21, 23, 59, berlinLoc)` (Fri 23:59) → false.
   - **Location inheritance:** `"monday, friday"` (no inline location) + `&pkgcfg.Window{From: mustTOD("08:00 Europe/Berlin"), Until: mustTOD("18:00 Europe/Berlin")}` → now `atDate(2026, 8, 24, 10, 0, berlinLoc)` (Mon) → true; now `atDate(2026, 8, 22, 10, 0, berlinLoc)` (Sat) → false (the window's Berlin location governs the weekday).
   - **IANA boundary:** `"sunday Europe/Berlin"`, nil window, now `atDate(2026, 8, 22, 22, 30, stdtime.UTC)` → true (UTC Sat 22:30 is Berlin Sun 00:30); now `atDate(2026, 8, 23, 22, 30, stdtime.UTC)` → false (UTC Sun 22:30 is Berlin Mon 00:30).
   - **UTC fallback (defensive, precedence case 3):** `"saturday"` (no location), nil window, now `atDate(2026, 8, 22, 10, 0, stdtime.UTC)` → true (resolves in UTC; unreachable for Load-ed configs because the fail-closed rule rejects a location-less days-only member — the fallback exists for programmatic construction and defensive completeness).

8. **Compile + coverage gate.** `go build ./...` and `go vet ./...` must be clean. New `pkg/config.go` code (the `Days` type, `UnmarshalText`, `Contains`, the two struct fields, the normalize/validate branches) must be covered by the rows in requirements 6–7 (target ≥ 80% on the changed paths). `grep -n 'days' pkg/config.go` must return ≥ 1 line (the AC 1 evidence — the `days,omitempty` tags and `Days` references guarantee it).
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Config schema is fixed by the spec: `Upstream` gains `Days *Days` (yaml `days`, `omitempty`); `Provider` gains the same for the legacy single-upstream form, copied onto the implicit single member by `normalizeUpstreams` and `UpstreamList` (same synthesis as the spec-014 `window:`); the YAML value is a string of comma-separated lowercase English weekday names (`monday`..`sunday`) with an optional trailing inline IANA location. Do NOT add other fields, flags, opt-out knobs, or thresholds (spec Non-goals: no time range inside `days:`, no `monday..friday` range sugar, no separate `timezone:` field, no per-day windows). The weekday set is a plain comma-separated list.
- Fail-closed location rule (spec Constraints): a member with `days:` and no `window:` must carry an inline location on `days:` — config load rejects a days-only member whose `days:` has no location; a member with `days:` AND a `window:` may omit it (the window's `from`/`until` location governs). Absent `days:` is byte-for-byte today — no behavior change for members without it (spec Non-goal: "Any change to spec-014 semantics for members without `days:`").
- Canonical names are the 7 lowercase English names `monday` `tuesday` `wednesday` `thursday` `friday` `saturday` `sunday` (Go `time.Weekday.String()` lowercased). No abbreviations, no ranges, no numeric indices — validation rejects anything else at load. Parsing splits on the last whitespace: the last token is tried as a `time.LoadLocation` — if it loads it is the location and the remainder is the name list; if it does not load the whole value is the name list.
- `days:` is eligibility-only config — it never widens access or bypasses `allowedApiKeys`; a config mistake (unknown name / empty value / missing location) fails at load, never silently misroutes (spec Security). The value is parsed at config load only and never logged.
- No new dependencies — `github.com/bborbe/time` v1.27.9 is already in go.mod. Do NOT touch `go.mod` / `go.sum`.
- This is the config-contract prompt only — do NOT touch `pkg/handler/`, `pkg/factory/factory.go`, `main.go`, or `pkg/cli.go`. The selection-time eligibility AND (`pkg.Days.Contains` consumed in `memberEligible`), the pool wiring, the fall-through path, and the fixed-clock tests are spec-017 prompt 2; docs/CHANGELOG is prompt 3.
- Use `github.com/bborbe/errors` for error construction — never `fmt.Errorf` directly (mirror the existing `errors.New(ctx, fmt.Sprintf(...))` pattern in `Config.Validate`). Inside `Days.UnmarshalText` (no ctx), use `errors.New(context.Background(), ...)` / `errors.Errorf(context.Background(), ...)` — the exact pattern `libtime.TimeOfDay.UnmarshalText` uses. Preserve the per-iteration `ctx.Done()` checks in every loop you touch.
- Do NOT add a `MarshalText` to `Days` — configs are load-only and the reloader never marshals them.
- No AI attribution in code or comments. `make precommit` must remain green — run it before declaring done. Follow `docs/dod.md` (GoDoc on every new exported identifier).
- Do NOT touch `docs/` or `CHANGELOG.md` in this prompt — spec-017 prompt 3 owns documentation.
</constraints>

<verification>
make precommit

# AC 1 — schema landed (the AC evidence grep):
grep -n 'days' pkg/config.go   # expect >=1 line

# Days type + predicate + validation landed:
grep -n 'type Days struct\|func (d \*Days) UnmarshalText\|func (d \*Days) Contains' pkg/config.go
grep -n 'days without a window requires an inline location\|days applies only to the legacy upstream form' pkg/config.go

# AC 1 evidence — config tests reference days:
grep -c 'days' pkg/config_test.go   # expect >=1

# days rows pass (ginkgo focus):
go test -mod=mod -count=1 ./pkg/ -ginkgo.focus='days'

# Full suite:
go test -mod=mod -count=1 ./pkg/...
</verification>

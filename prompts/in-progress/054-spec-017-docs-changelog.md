---
status: approved
spec: [017-weekday-window-upstreams]
created: "2026-08-22T12:10:00Z"
queued: "2026-08-22T12:40:46Z"
branch: dark-factory/weekday-window-upstreams
---

# Docs + changelog: per-upstream `days:` weekday eligibility

<summary>
- The configuration reference documents the new optional `days:` weekday allow-list on an `upstreams:` pool entry (and on the legacy single `upstream:` provider form): a comma-separated list of lowercase English weekday names (`monday`..`sunday`) with an optional trailing inline IANA location, e.g. `"saturday, sunday Europe/Berlin"`.
- The time-of-day windows section explains the eligibility AND: a member is eligible only while `(window absent OR window contains now) AND (days absent OR today's weekday is in days)`, that the weekday is resolved in the member's attached IANA location (the inline `days:` location, else the `window:` from/until location, else UTC) — never the router host's local day — and that a member with `days:` but no `window:` is eligible all day on those days.
- The docs cover absent = all days (byte-for-byte today), the fail-closed rule (a days-only member must carry the inline location; unknown names and empty values are rejected at load), the SIGHUP reload applying a `days:` change, and the unchanged provider fall-through (`[route] ... window=closed -> <fallback>`, never an error or 429).
- A new worked example shows the live three-member complementary config this feature exists for: a weekend member (`days: "saturday, sunday Europe/Berlin"`, unlimited key, cap 50, no window) plus the weekday day/night members (full Monday–Friday list + their existing windows), so the unlimited key serves all day on weekends and the day/night keys own Monday–Friday.
- The example config gains commented `days:` examples (per-entry on an `upstreams:` member and provider-level on the legacy form), so copying it does not change behavior.
- The changelog gains a feature entry under a newly created `## Unreleased` section describing the `days:` schema, the weekday eligibility semantics, the fail-closed rule, and the SIGHUP behavior. No Go source is touched — this prompt documents what prompts 1–2 shipped.
</summary>

<objective>
Document the per-upstream `days:` weekday eligibility block and its routing semantics in the operator-facing config reference, the example config, and the changelog, so an operator can make a pool member serve only on a weekday subset — e.g. the unlimited off-peak key all day on weekends, with no operator action at any day/time boundary.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- Read `docs/config.md` — section order: `## Schema` (the YAML block, per-entry commented `# window:` lines at ~46–55) → `## Routing` → `## Aliases` → `## Model pools` → `## Requires leading system` → `## Concurrency limit` → `## Upstream pools` (line ~190) → `## Time-of-day windows` (line ~234, with its "What it is" / "Eligibility semantics" / "Overnight wrap" / "Provider fall-through" / "Session re-resolution" / "Observability" / "Validation" / "SIGHUP" / "Security" bullets and the two-member complementary-window worked example at ~268–286) → `## Auth` → the rest. The `days:` documentation extends the existing `## Time-of-day windows` section (the AC evidence names that section) — do NOT create a new top-level section.
- Read `docs/config.example.yaml` — the `minimax` legacy single-`upstream:` provider's commented `# window:` block (lines ~36–38) is where the provider-level `# days:` example goes; the `ollama-local` provider's commented `# upstreams:` block (lines ~60–70) is where the per-entry `# days:` example goes.
- Read `CHANGELOG.md` — the file currently starts with `## v0.43.0` (NO `## Unreleased` heading exists). Create the `## Unreleased` heading immediately ABOVE `## v0.43.0` (after the SemVer preamble, per the changelog guide's file structure), then add the new feature bullet under it. Released sections (`## v0.43.0` and below) are frozen — never edit them. The spec's AC 8 evidence is `awk '/^## /{n++} n==1' CHANGELOG.md | grep -c 'weekday'` → ≥ 1, which means `## Unreleased` must be the FIRST `## ` heading in the file and the bullet under it MUST contain the literal word `weekday`.
- Read the prompt 1–2 results (the behavior being documented — no forward-referencing): `pkg/config.go` (`Days{Weekdays libtime.Weekdays, Location *stdtime.Location}`, `Upstream.Days` / `Provider.Days` with the `days:` yaml string form, `(*Days).UnmarshalText` parse rules — split on last whitespace, last token tried as `time.LoadLocation`, unknown-name/empty rejection — the fail-closed rule that a days-only member must carry the inline location, the provider-level-days-with-upstreams rejection, and `(*Days).Contains(now, window)` resolving the weekday in the inline location → window from/until location → UTC), `pkg/handler/upstream-pool-handler.go` (`UpstreamMember.Days`, the window AND days conjunction in `memberEligible`, days-ineligible members excluded from pinning and least-loaded), and `pkg/factory/factory.go` (`Days: up.Days` wiring, SIGHUP rebuild applies a `days:` change). Document only what those actually ship.
- Read `docs/dod.md` — `docs/config.md` / `docs/config.example.yaml` / `CHANGELOG.md` update rules.
- Coding plugin docs (in-container paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — CHANGELOG entry placement and phrasing (the `## Unreleased` heading goes above the newest released section, after the SemVer preamble; conventional `feat:` prefix).
  - `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — docs conventions.
</context>

<requirements>
1. **`docs/config.md` — schema block.** In the `## Schema` YAML block, extend the commented per-entry `# window:` block under `# upstreams:` (lines ~53–55) with a per-entry `# days:` line, and add a provider-level `# days:` comment next to the legacy `# window:` block (lines ~46–48). Follow the existing `#`-comment style, e.g. per-entry:
   ```yaml
   #     days:                     # optional; per-member weekday eligibility allow-list (see ## Time-of-day windows). Comma-separated lowercase weekday names (monday..sunday) with an optional trailing IANA location, e.g. "saturday, sunday Europe/Berlin". A member outside its days is ineligible for dispatch.
   ```
   and at provider level:
   ```yaml
   # days:                        # optional; legacy single-upstream form only — applies to the implicit single member (see ## Time-of-day windows). Cannot be combined with an upstreams: list.
   ```
2. **`docs/config.md` — extend the `## Time-of-day windows` section with `days:`.** Add content covering, at the same care level as the neighboring bullets:
   - **What it is.** A member can declare an optional `days:` weekday allow-list as a sibling of `window:` — a comma-separated list of lowercase English weekday names (`monday`..`sunday`) with an optional trailing inline IANA location, e.g. `"saturday, sunday Europe/Berlin"`. Absent = every day (byte-for-byte today's behavior). The legacy single `upstream:` provider form carries the same `days:` at provider level, applied to its implicit single member, exactly like the provider-level `window:`.
   - **Eligibility AND.** A member is ELIGIBLE for a dispatch only while BOTH conditions hold: `(window absent OR window contains "now") AND (days absent OR today's weekday is in days)`. The weekday is resolved in the member's attached IANA location, in precedence order: the inline location on the `days:` value, else the `window:` `from`/`until` location, else UTC — so the boundary is the location's calendar, never the router host's local day. A member with `days:` but no `window:` is eligible ALL DAY on those weekdays (this is how the weekend use case below expresses "all day on Saturday and Sunday" — the `window:` block has no all-day value).
   - **Location + fail-closed rule.** A member with `days:` and no `window:` MUST carry the inline location on its `days:` value — config load rejects a days-only member whose `days:` has no location, so it can never silently resolve its weekday in UTC and drift from its sibling members' calendar. A member with `days:` AND a `window:` may omit the inline location — the window's `from`/`until` location governs both boundaries.
   - **Validation.** Unknown weekday names (e.g. `funday`) and an empty value (`days: ""`) are rejected at config load, as is a provider-level `days:` combined with an `upstreams:` list. The 7 canonical names are `monday` `tuesday` `wednesday` `thursday` `friday` `saturday` `sunday` (lowercase; no abbreviations, no `monday..friday` ranges, no numeric indices). A valid-but-wrong location (e.g. `Europe/London` for `Europe/Berlin`) passes validation and shifts both the weekday and time boundaries by its offset — verify the `[route]` lines against the expected boundary and correct via SIGHUP.
   - **Ineligibility semantics (unchanged from `window:`).** A member outside its `days:` is excluded from BOTH session pinning (the weighted ring hash only considers eligible members) and keyless least-loaded selection; when no member of a provider's pool is eligible the provider falls through declaration order to the next matching provider or `default_provider`, logged as `[route] provider=<p> window=closed -> <fallback>` at V(2) — eligibility, never an error, never a 429 (the fall-through line keeps the `window=closed` wording).
   - **SIGHUP.** A change to a member's `days:` (or the member list) applies on SIGHUP without a restart — the reloader rebuilds the pool tree from the edited config (see ## Reload).
   - **Security.** `days:` is server-side config + the router's injected clock, evaluated per request; a client cannot influence which member applies, and it never widens access or bypasses `allowedApiKeys`.
   - **Worked example — the weekend use case this feature exists for.** Extend the section with a three-member complementary example (the operator pattern from the spec): one provider serving the same endpoint with three members — the weekday-day member (`days: "monday, tuesday, wednesday, thursday, friday"`, normal-rate key, cap 16, `window: 08:00–18:00 Europe/Berlin`), the weekday-night member (same full weekday list, unlimited key, cap 50, `window: 18:00–08:00 Europe/Berlin`), and the weekend member (`days: "saturday, sunday Europe/Berlin"`, the SAME unlimited off-peak key, cap 50, NO `window:`) — so the unlimited key serves all day on weekends and the day/night keys own Monday–Friday, with exactly one eligible member per (day, time) and no operator action at any day/time boundary. Note the full weekday list is written out (`monday, tuesday, ...`) because the format has no `monday..friday` range sugar. The `[route]` lines name the weekend member all day Sat+Sun, the day member during Mon–Fri business hours, and the night member Mon–Fri off-peak.
   - A short note tying back: the per-entry `days:` sits alongside the per-entry `window:` / `weight` / `maxConcurrentRequests` fields documented in `## Upstream pools`, and the provider-level `days:` on the legacy single-`upstream:` form behaves exactly like the provider-level `window:` (copied onto the implicit single member).
3. **`docs/config.example.yaml`.** Add commented `days:` examples (they MUST be commented — an operator copying this example must not get an unexpected weekday restriction):
   - In the `minimax` legacy single-`upstream:` provider, below the commented `# window:` block (lines ~36–38), add a commented provider-level `# days:` line noting it applies to the implicit single member and cannot be combined with `upstreams:`.
   - In the `ollama-local` provider's commented `# upstreams:` block, extend the first entry with a commented `# days:` line alongside the existing `# window:` lines (line ~64), in the same style.
4. **`CHANGELOG.md`.** Create the `## Unreleased` heading immediately ABOVE `## v0.43.0` (after the SemVer preamble; per `changelog-guide.md`), then add ONE `feat:` bullet under it. The bullet MUST contain the literal `weekday` (the spec AC 8 evidence is `awk '/^## /{n++} n==1' CHANGELOG.md | grep -c 'weekday'` → ≥ 1, which requires `## Unreleased` to be the first heading and the bullet to contain the word). Cover, at the detail level of the neighboring entries (e.g. the `## v0.40.0` / `## v0.41.0` entries): the optional per-upstream `days:` weekday allow-list (a comma-separated list of lowercase English weekday names `monday`..`sunday` with an optional trailing inline IANA location, e.g. `"saturday, sunday Europe/Berlin"`) on `upstreams:` entries and on the legacy single-`upstream:` provider form (copied onto the implicit single member like the provider-level `window:`); a member is eligible only while `(window absent OR window contains "now") AND (days absent OR today's weekday is in days)`, with the weekday resolved in the member's attached IANA location (inline `days:` location, else the `window:` from/until location, else UTC) — never the router host's local day; a member with `days:` but no `window:` is eligible all day on those days (the weekend unlimited-key use case); absent `days:` is byte-for-byte today; a days-only member whose `days:` carries no inline location is rejected at config load (fail-closed), as are unknown weekday names, empty values, and a provider-level `days:` combined with an `upstreams:` list; the unchanged provider fall-through (`[route] ... window=closed -> <fallback>`, eligibility never an error or 429) and SIGHUP applying a `days:` change without a restart. Phrase the bullet with the repo's existing `feat:` style.
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Do NOT touch any Go source in this prompt — prompts 1–2 implemented the behavior; this is documentation and changelog only.
- Do NOT invent config knobs, headers, endpoints, or behavior beyond what the spec and prompts 1–2 define — `days:` (comma-separated lowercase weekday names with an optional trailing inline IANA location) on `Upstream` entries and on the legacy provider form are the entire surface (spec Non-goals: no time range inside `days:`, no `monday..friday` range sugar — the docs must show the full weekday list written out, no separate `timezone:` field, no per-day windows, no changes to key/quota limits or caps).
- The docs must describe behavior prompts 1–2 actually shipped — no forward-referencing unbuilt features.
- This prompt depends on prompts 1–2: do not approve or execute until prompt 2 has completed and shipped the eligibility + factory wiring — the `<context>` reads them.
- Do NOT edit released `CHANGELOG.md` sections (`## v0.43.0` and below) — they are frozen history. The `## Unreleased` heading is created ABOVE `## v0.43.0`, after the SemVer preamble, exactly per `changelog-guide.md`. The Unreleased bullet MUST contain the literal word `weekday` (spec AC 8 evidence).
- `days:` examples in `docs/config.example.yaml` MUST stay commented — copying the example must not change behavior.
- No AI attribution. `make precommit` must remain green.
</constraints>

<verification>
make precommit

# AC docs — days documented in both files:
grep -c 'days:' docs/config.md           # expect >=1
grep -c 'days' docs/config.example.yaml  # expect >=1

# The days content lives in the section the AC names:
grep -c '^## Time-of-day windows' docs/config.md   # expect >=1 (section still present)

# Example stays behavior-neutral (days commented only) — fail if any UNCOMMENTED occurrence exists:
! grep -nE '^[[:space:]]*days:' docs/config.example.yaml   # comment lines start with '#' and do not match

# AC 8 evidence — Unreleased is the first heading and its bullet contains 'weekday':
awk '/^## /{n++} n==1' CHANGELOG.md | grep -c 'weekday'   # expect >=1
</verification>

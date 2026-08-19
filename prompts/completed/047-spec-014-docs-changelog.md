---
status: completed
spec: [014-time-windowed-upstreams]
summary: 'Documented the per-upstream window: time-of-day eligibility block in docs/config.md (new ## Time-of-day windows section + schema comments), docs/config.example.yaml (commented examples), and CHANGELOG.md (## Unreleased feat entry)'
execution_id: claude-code-router-time-window-exec-047-spec-014-docs-changelog
dark-factory-version: dev
created: "2026-08-19T20:27:00Z"
queued: "2026-08-19T20:42:15Z"
started: "2026-08-19T21:12:44Z"
completed: "2026-08-19T21:15:20Z"
---

# Docs + changelog: per-upstream `window:` time-of-day eligibility

<summary>
- The configuration reference documents the new optional `window:` block on an `upstreams:` pool entry (and on the legacy single `upstream:` provider form): `from` / `until` values in the `"HH:MM <location>"` form, each evaluated in its attached IANA location.
- A new "Time-of-day windows" section explains that a member whose window does not contain "now" is ineligible for that dispatch — excluded from session pinning and keyless least-loaded selection — and that when no member of a provider's pool is eligible, the router falls through to the next matching provider or `default_provider`, with a closed window being eligibility only (never an error or 429).
- The section covers the complementary-window operator pattern (a day member with a normal-rate key at 08:00–18:00 Europe/Berlin and an off-peak member with the unlimited key at 18:00–08:00), overnight wrap (`22:00`–`06:00`), the `[route] ... window=closed` observability line, SIGHUP reload applying window changes, and the validation rules (malformed time / unknown IANA location / missing boundary / provider-window-with-upstreams all rejected at load).
- The example config gains commented `window:` examples on an `upstreams:` entry and on a legacy provider, so copying it does not change behavior.
- The changelog gains a feature entry under a newly created `## Unreleased` section describing the `window:` schema, the eligibility semantics, the provider fall-through, and the never-error/never-429 guarantee.
- No Go source is touched — this prompt documents what prompts 1–2 shipped.
</summary>

<objective>
Document the per-upstream `window:` time-of-day eligibility block and its routing semantics in the operator-facing config reference, the example config, and the changelog, so an operator can serve the same endpoint with different keys at different times of day (day-rate key during business hours, unlimited off-peak key at night) without any operator action.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- Read `docs/config.md` — section order `## Schema` (the YAML block with the commented `# upstreams:` per-entry lines at lines ~44–49) → `## Routing` → `## Aliases` → `## Model pools` → `## Requires leading system` → `## Concurrency limit` → `## Upstream pools` (line ~182, with its "Per-entry fields" bullet list, "Session pinning", "Keyless least-loaded", "Observability" `[route] session=<id> upstream=<url>`, and "SIGHUP applies changes" bullets) → `## Auth` → `## Routing by API key` → `## Trace` → `## Example — all four providers` → `## Switching mid-session` → `## Reload` → `## Related`. The new `## Time-of-day windows` section slots in right after `## Upstream pools` and before `## Auth` (it extends the pool-member contract that `## Upstream pools` defines).
- Read `docs/config.example.yaml` — the `ollama-local` provider's commented `# upstreams:` block (lines ~55–62) is where the per-entry `# window:` example goes; the legacy `minimax` provider (single `upstream:`) is where the provider-level commented `# window:` example goes.
- Read `CHANGELOG.md` — the file currently starts with `## v0.38.1` (NO `## Unreleased` heading exists). Create the `## Unreleased` heading above `## v0.38.1` per the changelog guide's file structure, then add the new feature bullet under it. Released sections (`## v0.38.1` and below) are frozen — never edit them.
- Read the prompt 1–2 results (the behavior being documented): `pkg/config.go` (`Window{From, Until}` with `libtime.TimeOfDay`, `Upstream.Window` / `Provider.Window`, `normalizeUpstreams` legacy synthesis, `Config.Validate` both-boundaries rule, the `[From, Until)` + overnight-wrap + attached-location semantics of `Window.Contains`), `pkg/handler/upstream-pool-handler.go` + `pkg/handler/model-pool.go` (ineligible members excluded from pinning + least-loaded), `pkg/handler/model-router.go` (the `[route] provider=<p> window=closed -> <fallback>` line and the provider fall-through to the next matching provider / `default_provider`), and `pkg/factory/factory.go` (`WithCurrentDateTime` + the member wiring + SIGHUP rebuild). Document only what those actually ship — no forward-referencing.
- Read `docs/dod.md` — `docs/config.md` / `docs/config.example.yaml` / `CHANGELOG.md` update rules.
- Coding plugin docs (in-container paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — CHANGELOG entry placement and phrasing (the `## Unreleased` heading goes above the newest released section, after the SemVer preamble; conventional `feat:` prefix).
  - `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — docs conventions.
</context>

<requirements>
1. **`docs/config.md` — schema block.** In the `## Schema` YAML block, extend the commented `# upstreams:` per-entry block (lines ~44–49) with a per-entry `# window:` line, and add a provider-level `# window:` comment next to the legacy `# maxConcurrentWaitSeconds:` line. Follow the existing `#`-comment style, e.g.:
   ```yaml
   #     window:                    # optional; per-member time-of-day eligibility window (see ## Time-of-day windows). Values are "HH:MM <location>", e.g. "18:00 Europe/Berlin". from/until required when the block is present. A member outside its window is ineligible for dispatch.
   #       from: "08:00 Europe/Berlin"
   #       until: "18:00 Europe/Berlin"
   ```
   and at provider level:
   ```yaml
   # window:                    # optional; legacy single-upstream form only — applies to the implicit single member (see ## Time-of-day windows). Cannot be combined with an upstreams: list.
   #   from: "08:00 Europe/Berlin"
   #   until: "18:00 Europe/Berlin"
   ```

2. **`docs/config.md` — new `## Time-of-day windows` section.** Insert after the `## Upstream pools` section (before `## Auth`). Document, at the same care level as the neighboring sections (all of DB 1–7 and AC 2–5 must be documented):
   - **What it is.** A provider pool member can declare an optional `window:` block with `from` / `until` values in the `"HH:MM <location>"` form (e.g. `"18:00 Europe/Berlin"`) — each value carries its IANA location inline, so the boundary is the location's wall clock, never the router host's local time. The legacy single `upstream:` provider form carries the same `window:` at provider level, applied to its implicit single member.
   - **Eligibility semantics.** A member is ELIGIBLE for a dispatch only while "now" is inside `[from, until)` — the `from` boundary is inclusive, the `until` boundary exclusive. A member whose window does not contain "now" is INELIGIBLE: it is skipped by BOTH session pinning (the weighted ring hash only considers eligible members) and keyless least-loaded selection. A member with no `window:` is always eligible (today's behavior). Two members whose windows overlap at the same moment are BOTH eligible and selected by the normal pinning / least-loaded rules (see `## Upstream pools`).
   - **Overnight wrap.** `from` after `until` wraps overnight: `from: "22:00"` `until: "06:00"` covers 02:00 and excludes 14:00. `from` == `until` is an empty window (never eligible) — avoid it.
   - **Provider fall-through.** When no member of a provider's pool is eligible, the provider itself is ineligible for that dispatch: the model falls through declaration order to the next matching provider that has an eligible member, then to `default_provider`. A closed window is ELIGIBILITY, never a failure — no router error, no HTTP 429, no health check, no probing. (The operator's complementary-window config below guarantees at least one eligible member per period, so the fall-through is the safety net, not the normal path.)
   - **Session re-resolution.** Pinning is stateless: a session pinned to a member whose window closes mid-session re-resolves to an eligible member on its next request (the cache on the old member is lost — unavoidable, its key is unusable); a stream already dispatched completes even if the boundary passes mid-request.
   - **Observability.** When the router falls through because the first matching provider's pool is fully closed, it logs `[route] provider=<p> window=closed -> <fallback>` at glog V(2) (same verbosity as the existing `[route] session=<id> upstream=<url>` and `[route] model=... matched ...` detail lines) — the operator evidence that the window boundary is behaving as configured. Each dispatch still logs the normal `[route]` line naming the serving member.
   - **Validation.** Malformed times (e.g. `"25:00 Europe/Berlin"`) and unknown IANA locations (e.g. `"18:00 Mars/Olympus"`) are rejected at config load, as is a `window:` with only one boundary. A provider-level `window:` combined with an `upstreams:` list is rejected — windows live on pool members. Malformed values and unknown locations fail validation; a valid-but-wrong location (e.g. `Europe/London` for `Europe/Berlin`) passes validation and shifts the boundary by its offset — the operator verifies the `[route] … window=closed` lines against the expected boundary (see Observability) and corrects the value via SIGHUP.
   - **SIGHUP.** A change to a member's `window:` (or the member list) applies on SIGHUP without a restart — the reloader rebuilds the pool tree from the edited config (see `## Reload`).
   - **Security.** The window is server-side config + server clock, evaluated per request; a client cannot influence which window applies, and the window never widens access or bypasses `allowedApiKeys`. The operator pattern below guarantees the off-peak unlimited key is only ever used inside its window.
   - **Worked example matching the spec's operator rung.** One provider serving the same endpoint with two keys, each restricted to its own period:
     ```yaml
     providers:
       seibert-vllm-default:
         upstreams:
           - upstream: http://vllm:8000
             token: "<normal-rate-key>"
             maxConcurrentRequests: 16
             window:
               from: "08:00 Europe/Berlin"
               until: "18:00 Europe/Berlin"
           - upstream: http://vllm:8000
             token: "<unlimited-off-peak-key>"
             maxConcurrentRequests: 50
             window:
               from: "18:00 Europe/Berlin"
               until: "08:00 Europe/Berlin"
         models:
           - "deepseek-v4-*"
     ```
     With complementary windows, business hours (08:00–18:00 Europe/Berlin) are served by the day member/key at its 16-request cap, and off-peak by the night member/key at its 50-request cap — exactly one eligible member per period, so the unlimited key is never touched during business hours and no operator action is needed at the boundary. The `[route]` lines name the day member during business hours and the night member off-peak.
   - A short note tying back to the existing sections: the per-entry `window:` sits alongside the per-entry `weight` / `maxConcurrentRequests` fields documented in `## Upstream pools`, and the provider-level `window:` on the legacy single-upstream form behaves exactly like the provider-level caps (copied onto the implicit single member).

3. **`docs/config.example.yaml`.** Add commented `window:` examples (they MUST be commented — an operator copying this example must not get an unexpected window):
   - In the `ollama-local` provider's commented `# upstreams:` block, extend the first entry with `# window:` lines showing `from` / `until` in the `"HH:MM <location>"` form, e.g.:
     ```yaml
     #   - upstream: http://localhost:11434
     #     token: "ollama"
     #     weight: 1
     #     window:               # optional; per-member time-of-day eligibility window (see docs/config.md ## Time-of-day windows). from/until in "HH:MM <location>" form.
     #       from: "08:00 Europe/Berlin"
     #       until: "18:00 Europe/Berlin"
     #     maxConcurrentRequests: 8
     #     maxConcurrentWaitSeconds: 30
     ```
   - On a legacy single-upstream provider (e.g. `minimax`), add a commented provider-level `# window:` block (two lines) right below the `token:` line, with a comment noting it applies to the implicit single member and cannot be combined with `upstreams:`.

4. **`CHANGELOG.md`.** Create the `## Unreleased` heading immediately ABOVE `## v0.38.1` (after the SemVer preamble; per `changelog-guide.md`), then add ONE `feat:` bullet under it. The bullet must contain the literal `window` (the spec Verification is `sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c 'window'` → ≥ 1) and cover, at the detail level of the neighboring entries: the optional per-upstream `window:` block (`from` / `until` as `"HH:MM <location>"` time-of-day values, each carrying its IANA location inline, no separate timezone field) on `upstreams:` entries and on the legacy single-`upstream:` provider form (copied onto the implicit single member like the caps); a member whose window does not contain "now" (the injected clock, evaluated in the value's attached location) is ineligible for that dispatch — excluded from session pinning and keyless least-loaded selection; when no member of a provider's pool is eligible the provider is ineligible and the model falls through declaration order to the next matching provider or `default_provider`, with a closed window being eligibility only (never a router error, never a 429) and the fall-through logged as `[route] provider=<p> window=closed -> <fallback>`; overnight windows (`from` > `until`) wrap; a session pinned to a member whose window closes re-resolves on its next request while an in-flight request completes; config validation rejects malformed times, unknown IANA locations, a window with only one boundary, and a provider-level window combined with an `upstreams:` list; SIGHUP reload rebuilds the pool tree so window changes are live without a restart. Phrase the bullet with the repo's existing `feat:` style (e.g. the `## v0.36.0` / `## v0.38.0` entries' detail level).
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Do NOT touch any Go source in this prompt — prompts 1–2 implemented the behavior; this is documentation and changelog only.
- Do NOT invent config knobs, headers, endpoints, or behavior beyond what the spec and prompts 1–2 define — `window:` + `from` / `until` (each `"HH:MM <location>"`) on `Upstream` entries and on the legacy provider form are the entire surface (spec Non-goals: no separate provider-level window for `upstreams:` lists, no health checks / circuit breakers, no complexity-based routing, no dynamic window changes, no new clock mechanism).
- The docs must describe behavior prompts 1–2 actually shipped — no forward-referencing unbuilt features.
- This prompt depends on prompts 1–2: do not approve or execute until prompt 2 has completed and shipped the eligibility + factory wiring — the `<context>` reads them.
- Do NOT edit released `CHANGELOG.md` sections (`## v0.38.1` and below) — they are frozen history. The `## Unreleased` heading is created ABOVE `## v0.38.1`, after the SemVer preamble, exactly per `changelog-guide.md`.
- `make precommit` must remain green.
</constraints>

<verification>
make precommit

# AC docs — window documented in both files:
grep -c 'window:' docs/config.md           # expect >=1 (feature-specific, not trace/context-window)
grep -c 'window' docs/config.example.yaml  # expect >=1

# The new section exists:
grep -c '^## Time-of-day windows' docs/config.md   # expect >=1

# Example stays behavior-neutral (window commented only) — fail if any UNCOMMENTED occurrence exists:
! grep -nE '^[[:space:]]*window:' docs/config.example.yaml   # comment lines start with '#' and do not match

# Changelog bullet under ## Unreleased:
sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c 'window'   # expect >=1
</verification>

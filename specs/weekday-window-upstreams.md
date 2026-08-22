---
status: draft
---

## Summary

- Add an optional `days:` weekday allow-list to an upstream pool member (sibling of the spec-014 `window:` block) so a member's eligibility can vary by weekday, not just time of day.
- Eligibility becomes `(window absent OR window contains now) AND (days absent OR today's weekday is in days)`, with today's weekday resolved in an explicit IANA location — the member's `days:` inline location when present, else the member's `window:` `from`/`until` location.
- `days:` is a string of comma-separated lowercase English weekday names (`monday`..`sunday`) with an optional trailing inline IANA location — `"saturday, sunday Europe/Berlin"` — mirroring the `"HH:MM <location>"` idiom of spec 014. Absent = all days (byte-for-byte today's behavior); validation rejects unknown names, empty values, and a days-only member (no `window:`) whose `days:` carries no location.
- With `days:` a pool member can express a full-day window on a weekday subset — impossible today because `window:` has no all-day value (`from == until` is the empty window and `24:00` does not parse).
- Live result: `seibert-vllm-default` and `seibert-dark-factory` each gain a third weekend member (`days: "saturday, sunday Europe/Berlin"`, unlimited key, cap 50) so Sat+Sun traffic uses the unlimited off-peak key all day instead of the normal-rate day key (cap 16) during 08:00–18:00.

## Problem

The spec-014 `window:` block bounds an upstream member's eligibility by time of day only, repeating every day of the week. The operator holds a DeepSeek key that is contractually unrestricted on weekends (Sat+Sun all day) but the router cannot express "this member may serve all day, but only on Saturday and Sunday." So on a weekend, 08:00–18:00 traffic to `vllm.seibert.tools` matches the normal-rate day member (cap 16) while the unlimited off-peak member (cap 50) sits idle — weekend concurrency is throttled to a quarter of what the unlimited key allows, and the normal-rate quota that carries the working week is burned on traffic that should be free. A cron-driven config swap would fix it but adds 4 fragile transitions per week and violates the router's "no operator action at the boundary" design (spec 014).

## Goal

After this work, a pool member can restrict its eligibility to a weekday subset, so the two vLLM providers each serve exactly one member per (day, time): the unlimited key all day on weekends, and the existing day/night complementary windows Monday–Friday. The operator edits the config once, SIGHUPs, and no further action is needed at any day/time boundary.

## Non-goals

- A time range form (`from`/`until`) inside `days:` or a range syntax (`monday..friday`) — the weekday set is a plain comma-separated list of names; a `..` range is sugar the config does not need (weekday = 5 names, weekend = 2).
- A separate `timezone:` field or per-day time windows — weekday resolution reuses the existing inline-`location` mechanism; `window:` stays purely time-of-day.
- Changing key/quota limits or caps (cap 16 / cap 50 are upstream-side contracts, unchanged).
- cron-based config swapping, smart auto-routing, or multi-user rollout.
- Any change to spec-014 semantics for members without `days:` — absent `days:` is byte-for-byte today.
- All-day weekday members — a member with `days:` and no `window:` is eligible all day on those days (that is exactly the weekend use case); nothing prevents a weekday all-day member, but this spec's config only adds the weekend member.

## Acceptance Criteria

- [ ] **Schema + validation:** `Upstream` gains an optional `days:` string — comma-separated lowercase English weekday names (`monday`..`sunday`) with an optional trailing inline IANA location (`"saturday, sunday Europe/Berlin"`); absent = all days; unknown weekday names, an empty value, and a days-only member (no `window:`) whose `days:` carries no location are all rejected at config load; the legacy single-`upstream:` provider carries a provider-level `days:` on its implicit single member (same synthesis as the spec-014 `window:`); existing configs without `days:` load unchanged. Evidence: `go test -count=1 ./pkg/` passes a new Ginkgo suite; `grep -n 'days' pkg/config.go` returns ≥1 line; a config with `days: "funday Europe/Berlin"` fails `config.Load` with an error naming the invalid name, and one with `days: "saturday, sunday"` on a member with no `window:` fails with an error naming the missing location.
- [ ] **Eligibility AND:** a member is eligible only while `(window absent OR window.Contains(now)) AND (days absent OR days.Contains(weekday(now)))`, where `weekday(now)` is derived from `now` converted to the member's attached IANA location (the same `from`/`until` location resolution as spec 014). A member outside its `days:` is ineligible — skipped by session pinning and keyless least-loaded; when no member of a provider's pool is eligible the provider falls through declaration order to the next matching provider or `default_provider` (eligibility, never an error or 429). Evidence: `go test -count=1 ./pkg/handler/` passes fixed-clock Ginkgo rows at Sat 10:00 and Mon 10:00 selecting the expected member; a fully-closed provider logs `[route] provider=<p> window=closed -> <fallback>`.
- [ ] **Weekend all-day without a window:** a member with `days: "saturday, sunday Europe/Berlin"` and NO `window:` block is eligible all day on Berlin's Saturday and Sunday and ineligible Monday–Friday. Evidence: fixed-clock Ginkgo rows, all instants expressed in the member's resolution location (`Europe/Berlin`): Sat 00:01 / Sat 10:00 / Sat 23:59 / Sun 00:01 / Sun 23:59 (eligible) and Mon 00:01 / Mon 10:00 / Fri 23:59 (ineligible) on that member, through the real dispatch path — plus the offset-boundary rows Sat 00:30 and Mon 00:30 (a Berlin-Saturday instant must resolve on the weekend member even though UTC is still Friday; a Berlin-Monday instant must NOT resolve on the weekend member even though UTC is still Sunday).
- [ ] **Three-member complementary pool:** a pool with (1) weekend member `days: "saturday, sunday Europe/Berlin"` no window, (2) weekday-day member `days: "monday, friday"` + `window: 08:00–18:00 Europe/Berlin`, (3) weekday-night member `days: "monday, friday"` + `window: 18:00–08:00 Europe/Berlin` — yields exactly one eligible member per (day, time), including the offset boundary: Mon 10:00 → day member, Mon 22:00 → night member, Mon 00:30 → night member (Berlin Monday; the weekend member must NOT also be eligible), Sat 00:30 → weekend member (Berlin Saturday; the night member must NOT also be eligible), Sat 10:00 → weekend member, Sat 22:00 → weekend member, Sun 10:00 → weekend member, Fri 23:30 → night member. Evidence: `go test -count=1 ./pkg/handler/` passes a fixed-clock table over those eight (day, time) points (instants expressed in `Europe/Berlin`) asserting exactly one eligible member and its index; each member carries a distinct `maxConcurrentRequests` (16/50/50) so the assertion also proves the cap travels with the member.
- [ ] **Backward compatibility — absent `days:`:** every existing window test and the live two-member day/off-peak config behave byte-for-byte as before (a member with only `window:` is eligible exactly as spec 014 defines). Evidence: the pre-existing `pkg/handler/time-window_test.go` and `pkg/config_test.go` window specs pass unmodified; `grep -c 'days:' pkg/handler/time-window_test.go` on the pre-existing rows is 0 (new rows carry the new field).
- [ ] **Weekday resolves in the attached location, not host time:** a member whose `days:`/`window:` location is `Europe/Berlin` flips eligibility on Berlin's weekday, even when the router host clock is on a different weekday (e.g. UTC Sunday evening = Berlin Monday 00:30). Evidence: `go test -count=1 ./pkg/handler/` passes a fixed-clock row comparing `Europe/Berlin` vs UTC at a boundary instant.
- [ ] **SIGHUP applies a changed `days:` without restart:** adding/removing a weekday from a member's `days:` (or the member list) applies on SIGHUP. Evidence: `go test -count=1 ./pkg/factory/` passes the existing time-window wiring SIGHUP test extended with a `days:` change; no new failure path.
- [ ] **Docs + CHANGELOG:** `docs/config.md` `## Time-of-day windows` documents `days:` (schema, eligibility AND, absent = all days, weekend all-day example), `docs/config.example.yaml` gains a commented `days:` example, and a new `## Unreleased` heading at the top of `CHANGELOG.md` gains a feat bullet naming weekday-aware windows — never appended to a released `## vX.Y.Z` section (changelog fold guard; repo is `autoRelease: true`). Evidence: `grep -n 'days:' docs/config.md docs/config.example.yaml` returns ≥1 line per file; `awk '/^## /{n++} n==1' CHANGELOG.md | grep -c 'weekday'` returns ≥1.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format / lint / vet / vulncheck clean, exits 0
- `make test` — full Go suite passes including the new Ginkgo rows named in the ACs
- `go test -race -count=1 ./pkg/handler/` — eligibility is race-clean under concurrent requests
- `grep -n 'days' pkg/config.go` → ≥1 (AC 1)
- `awk '/^## /{n++} n==1' CHANGELOG.md | grep -c 'weekday'` → ≥1 (AC 8)

### Operator-executable (runs on the host after PR merge + release + install)

- `make install`; restart the launchd service (`de.bborbe.claude-code-router`).
- Apply the three-member config to `~/.config/claude-code-router/config.yaml` for `seibert-vllm-default` and `seibert-dark-factory`; SIGHUP (`kill -HUP $(pgrep claude-code-router)`); log shows `config reloaded`.
- Weekend path (Sat/Sun): `[route] ... provider=<name>/<idx>` and the V(2) `[route] upstream=` line name the weekend member (unlimited key, cap 50) all day — verify on a real weekend day OR, for the check on a weekday, via the injected-clock Ginkgo evidence (AC 3/4) as the live proxy; on a real weekend the router log is the evidence.
- Weekday daytime (Mon–Fri 08:00–18:00): `[req] ... provider=<name>/<idx>` names the day member (cap 16) — unchanged from today.
- `curl -fsS -X POST http://127.0.0.1:8788/v1/messages -H "content-type: application/json" -d '{"model":"deepseek-v4-flash","max_tokens":16,"messages":[{"role":"user","content":"ping"}]}'` returns 200 on both paths.

## Desired Behavior

1. `Upstream` carries an optional `days:` field — a string of comma-separated lowercase English weekday names (`monday`..`sunday`), optionally followed by an inline IANA location. Absent means all days; an empty value and unknown names are rejected at config load.
2. A member is eligible for a dispatch only while both its time condition and its weekday condition hold: the spec-014 `window:` (absent = always time-eligible) AND the `days:` set (absent = always weekday-eligible). A member with `days:` but no `window:` is eligible all day on its listed days.
3. Today's weekday is computed from the router's injected clock converted to an explicit IANA location, resolved in precedence order: (1) the inline location on the member's `days:` value (`"saturday, sunday Europe/Berlin"` → `Europe/Berlin`), else (2) the member's `window:` `from`/`until` location (spec-014 values always carry one), else (3) UTC. A days-only member (no `window:`) is rejected at config load unless its `days:` carries an inline location, so a days-only member can never silently resolve its weekday in UTC and drift from its sibling members' calendar. The weekday boundary is the location's calendar, never the router host's local day.
4. Weekday eligibility composes with every existing selection rule unchanged: session pinning's weighted ring hash considers only eligible members, keyless least-loaded considers only eligible members, and an ineligible member is not a valid overflow target. When no member of a provider's pool is eligible, the provider falls through declaration order (existing `window=closed` path — the log line and the no-error/no-429 contract are unchanged).
5. The legacy single-`upstream:` provider form accepts a provider-level `days:` copied onto the implicit single member, exactly as spec 014 copies the provider-level `window:`.
6. A `days:` change (or member-list change) applies on SIGHUP without a restart, like every other pool-member field.
7. Docs: `docs/config.md` `## Time-of-day windows` and `docs/config.example.yaml` document `days:` with the weekend all-day example; `CHANGELOG.md` `## Unreleased` gains a feat bullet.

## Constraints

- `days:` is an optional string field on the `Upstream` pool member, a sibling of `window:` (not nested inside it) — this is what lets a member be all-day on a weekday subset without inventing a fake `window:` (there is no all-day `window:` value: `from == until` is the empty window and `24:00` does not parse in `libtime.ParseTimeOfDay`).
- `days:` format: comma-separated lowercase English weekday names, optional whitespace, then an optional trailing IANA location — `"saturday, sunday Europe/Berlin"` (location) or `"monday, friday"` (no location, inherits the member's `window:` location). Parsing splits on the last whitespace: the last token is tried as a `time.LoadLocation` — if it loads, it is the location and the remainder is the name list; if it does not load, the whole value is the name list. Canonical names: the 7 lowercase English names `monday` `tuesday` `wednesday` `thursday` `friday` `saturday` `sunday` (Go `time.Weekday.String()` lowercased). No abbreviations, no ranges, no numeric indices — validation rejects anything else at config load.
- Fail-closed location rule: a member with `days:` and no `window:` must carry an inline location on `days:`; config load rejects a days-only member whose `days:` has no location (the all-day case is exactly where the UTC fallback would drift the weekday off its sibling members' calendar). A member with `days:` AND a `window:` may omit the inline location — the `window:` `from`/`until` location governs both the time and weekday boundaries.
- Eligibility is the AND of the time and weekday conditions; a member with only `window:` (no `days:`) is byte-for-byte spec 014; a member with only `days:` (no `window:`) is all-day on those weekdays.
- Weekday resolution uses the injected clock (`libtime.CurrentDateTimeGetter`) and the resolved location — the router's clock is the only time source, never `time.Now()` in business logic; derive the weekday with the same location-resolution discipline as `Window.Contains` (from/until location, else UTC — with the fail-closed rule above making UTC unreachable for days-only members).
- The complementary three-member config is applied to BOTH `seibert-vllm-default` and `seibert-dark-factory` (the two providers share the `vllm.seibert.tools` endpoint; dark-factory keeps its key pinning).
- No new dependencies; no AI attribution; tests follow the repo's Ginkgo convention; the `[req]`/`[route]` line contracts and all metrics are unchanged.
- The live weekday-day member keeps the normal-rate key and cap 16; the live weekday-night and weekend members use the SAME unlimited off-peak key and cap 50 (the weekend member is not a new key — it is the off-peak key made legal all day on Sat/Sun per the contract).

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Unknown weekday name in `days:` (typo, e.g. `saterday`) | Config load fails with an error naming the invalid name; old config stays active; daemon refuses to start | Operator corrects the name and re-SIGHUPs; `[config]` error names the offending value |
| Wrong location on a member's `days:`/`window:` (e.g. `Europe/London` for `Europe/Berlin`) | Weekday AND time boundaries both shift by the offset — a Saturday-evening request in Berlin may resolve to Sunday in London | Operator verifies the `[route]` lines against the expected boundary (same verification as spec 014) and corrects via SIGHUP |
| Days-only member with no inline location on `days:` | Config load rejects the member with an error naming the missing location (fail-closed — prevents a silent UTC weekday that would drift off the sibling members' Berlin calendar) | Operator adds the location (`days: "saturday, sunday Europe/Berlin"`) and re-SIGHUPs |
| Overlapping members (e.g. a weekend member accidentally also eligible on a weekday) | Both members eligible; pinning/least-loaded selects per the normal rules — same overlap semantics as spec 014; no error | Config review + `[route]`/`[req]` observation; correct the `days:`/`window:` and SIGHUP |
| Provider fully ineligible (no member's `days:`/`window:` contains now) | Model falls through declaration order to the next matching provider or `default_provider`; `[route] provider=<p> window=closed -> <fallback>`; never an error, never a 429 | If the fall-through is unintended, verify the `[route]` lines name the fallback and correct the member's `days:`/`window:` via SIGHUP; complementary windows make it non-normal on the vLLM providers |
| DST transition in the attached location (Europe/Berlin) | Weekday is a calendar property — Sat/Sun boundaries are unaffected by DST; the time-of-day boundary follows existing spec-014 semantics | None — no new behavior; existing window tests cover the boundary |

## Security / Abuse

`days:` is server-side config + the router's injected clock, evaluated per request; a client cannot influence which member or window applies, and it never widens access or bypasses `allowedApiKeys`. The field adds no new input surface — it is parsed at config load like `window:` and carries the same no-logs/trace redaction invariants for tokens.

## Suggested Decomposition

Prompts should be generated in this order — each row is a single prompt with a clear scope.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | `days:` field on `Upstream` + parsing/validation (unknown names, empty value, inline-location + fail-closed days-only rule, provider-level synthesis) + weekday eligibility on the config type | 1, 2, 3, 5 | 1, 2, 5, 6 | — |
| 2 | Selection-time wiring in the pool handler (AND of window+days eligibility, fall-through path) + fixed-clock Ginkgo rows (weekend all-day, three-member table incl. offset-boundary instants, location boundary, SIGHUP `days:` change, race) | 2, 3, 4, 6 | 2, 3, 4, 6, 7 | prompt 1 |
| 3 | Docs + example + CHANGELOG: `docs/config.md`, `docs/config.example.yaml`, `## Unreleased` feat bullet | 7 | 8 | — |

Rationale: prompt 1 establishes the schema + validation contract; prompt 2 wires eligibility and proves the (day, time) table; prompt 3 is docs-only and independent.

## Do-Nothing Option

Weekend 08:00–18:00 traffic keeps hitting the normal-rate day member at cap 16, the unlimited off-peak key stays idle on weekend days, and parallel weekend agents serialize under the day cap. The cost is small for a single interactive session but real for unattended weekend workloads (dark-factory agents, batch jobs) that today would run 4× faster on the unlimited key — and the weekday normal-rate quota keeps being spent on traffic that should be free.

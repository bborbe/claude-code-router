---
status: prompted
approved: "2026-08-19T20:08:31Z"
generating: "2026-08-19T20:41:25Z"
prompted: "2026-08-19T20:41:25Z"
branch: dark-factory/time-windowed-upstreams
---

## Summary

- A provider's pool members (spec 012 `Upstream`) today are always eligible — a member with an off-peak-only API key would be used whenever traffic matches, violating the key's usage restriction.
- This spec adds an optional `window:` block to an `Upstream` entry: `from` / `until` as `libtime.TimeOfDay`, each carrying its IANA location inline (e.g. `"18:00 Europe/Berlin"`) — so no separate `timezone:` field.
- At selection time a member whose window does not contain "now" is INELIGIBLE: skipped by session pinning and keyless least-loaded; when no member of the provider's pool is eligible, the provider falls through declaration order to the next matching provider, then `default_provider`. A closed window is *eligibility*, never a failure — never an error, never a 429.
- The concrete shape: ONE seibert-vllm provider with two members pointing at the same URL — a day member (08:00–18:00 Europe/Berlin, normal-rate key, `maxConcurrentRequests: 16`) and an off-peak member (18:00–08:00, unlimited key, `maxConcurrentRequests: 50`). Business hours use the day member/key; off-peak the night member/key — the unlimited key is never touched during business hours.
- Legacy single-`upstream:` providers carry their window on the implicit single member; SIGHUP hot-reload applies window changes.

## Problem

The operator holds a DeepSeek key with unlimited usage that is contractually restricted to OUTSIDE primary business hours (18:00–08:00 Europe/Berlin). Spec 012 shipped the upstream pool and session pinning, so a provider can now have several members — but every member is always eligible, and the router would use the off-peak key whenever a request matched, regardless of the time — violating the restriction. The router is the natural place to gate: it already resolves "now" (injected clock), selects members by pinning/least-loaded, and swaps per-member tokens. Adding a per-member time window lets one pool serve the same endpoint with different keys at different times — the off-peak key only ever used inside its window.

## Goal

An `Upstream` member can declare a time-of-day window; the router treats a member whose window does not contain "now" as ineligible for that dispatch — excluded from session pinning and least-loaded selection — and when no member of a provider's pool is eligible, falls through to the next matching provider or `default_provider`. Complementary windows on a single provider's members (day key + off-peak unlimited key) yield exactly one eligible member per period, so the off-peak key is used only at night, at its higher concurrency cap, without any operator action.

## Non-goals

- A separate provider-level `window:` config field — the window lives on the pool member; the legacy single-`upstream:` provider gets it on its implicit single member (a one-member pool is still a pool).
- Health checks / circuit breakers — a closed window is eligibility, not a failure mode; no probing, no rotation.
- Complexity-based routing (cost / latency-aware per-turn selection) — separate backlog goal.
- Dynamic window changes — SIGHUP is the only change channel; no runtime clock-driven re-evaluation of the window definition itself.
- Changing the injected-clock mechanism — the window check uses the router's existing `libtime.CurrentDateTimeGetter`, already injected (spec 012/013 pattern); no new clock plumbing.

## Acceptance Criteria

- [ ] **Config parse + validation:** optional `window:` on `Upstream` with `from` / `until` as `libtime.TimeOfDay` parses; malformed times and unknown IANA locations are rejected at config load; the legacy single-`upstream:` provider carries its window on its implicit single member; configs without `window:` load unchanged. Evidence: `go test -count=1 ./pkg/` passes (new Ginkgo rows assert each rule); `grep -c 'Window' pkg/config_test.go` ≥1.
- [ ] **Eligibility:** a member whose window does not contain "now" is ineligible — skipped by session pinning and keyless least-loaded; when no member of the provider's pool is eligible, the provider falls through to the next declaration-order matching provider or `default_provider`. Evidence: `go test -count=1 ./pkg/handler/` passes (new Ginkgo row asserts a request during a member's closed window is served by the fallback/next provider); router log line `[route] provider=<p> window=closed -> <fallback>`.
- [ ] **Complementary windows:** a pool with a day member (window 08:00–18:00 Europe/Berlin, cap 16) and an off-peak member (window 18:00–08:00, cap 50) serves business-hours requests on the day member/key only and off-peak requests on the night member/key only. Evidence: fixed-clock test at 10:00 (day member selected, cap 16) and 22:00 (night member selected, cap 50), both through the real dispatch path; a fixed-clock transition row pins a session to the day member at 17:59 and asserts the next request at 18:01 lands on the off-peak member (re-resolve on window close) while an in-flight request started before the boundary completes; `go test -count=1 ./pkg/handler/` passes.
- [ ] **Overnight wrap:** `from: "22:00"` `until: "06:00"` treats 02:00 as inside and 14:00 as outside. Evidence: fixed-clock test asserting both directions; `go test -count=1 ./pkg/handler/` passes.
- [ ] **IANA timezone:** the window is evaluated in the `from`/`until` values' attached location (e.g. `"18:00 Europe/Berlin"`), not the router host's local time. Evidence: fixed-clock test comparing `Europe/Berlin` vs UTC at the boundary; `go test -count=1 ./pkg/handler/` passes.
- [ ] **SIGHUP reload:** a second `CreateRouterFromConfig` with a changed `window:` (or member list) rebuilds the pool tree and the rebuilt tree enforces the new window. Evidence: `go test -count=1 ./pkg/factory/` passes (new wiring row asserts the rebuilt tree enforces the new window); `grep -c 'window' pkg/factory/*_test.go` ≥1.
- [ ] **Never an error/429:** a request during a member's closed window returns a normal 200 served by the fallback/eligible member, with no router error and no 429 logged. Evidence: negative evidence — `grep -c 'status=429' <test log>` returns 0 AND `grep -cE 'ERROR|error' <test log>` returns 0 during the closed-window requests; `go test -count=1 ./pkg/handler/` passes.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format / lint / vet / vulncheck clean
- `make test` — full Go suite passes, including the new Ginkgo rows named in the ACs
- `grep -c 'window' docs/config.md docs/config.example.yaml` → ≥1 per file
- `sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c 'window'` → ≥1

### Operator-executable (runs on the host after PR merge + release + install)

- `make install`; restart the launchd service (`de.bborbe.claude-code-router`).
- Give `seibert-vllm-default` (or the chosen provider) an `upstreams:` list with a day member (window `"08:00 Europe/Berlin"`–`"18:00 Europe/Berlin"`, normal-rate key, `maxConcurrentRequests: 16`) and an off-peak member (window `"18:00 Europe/Berlin"`–`"08:00 Europe/Berlin"`, unlimited key, `maxConcurrentRequests: 50`); `kill -HUP $(pgrep claude-code-router)`.
- With a FAKE/limited window for the live test (NOT the real unlimited key during business hours — e.g. temporarily set the off-peak window to a span containing "now" and the day window away from it), `/tmp/claude-code-router.log` shows the off-peak member serving inside its window (`[route] session=<id> upstream=<off-peak-url>`) and the day/fallback member serving outside it; the off-peak key is never used outside its window.
- SIGHUP applies a changed window (move the off-peak window boundary, confirm the next request's `[route]` line reflects it).

## Desired Behavior

1. `Upstream` gains an optional `Window` field: `From` / `Until` as `libtime.TimeOfDay`. Each value carries its IANA location inline in the `"HH:MM <location>"` form (`libtime.ParseTimeOfDay` handles it — no separate timezone field, no default-location decision). Absent `window:` = always eligible (today's behavior).
2. At dispatch, a member is ELIGIBLE iff "now" (from the injected `libtime.CurrentDateTimeGetter`, resolved in the member's attached location) is inside `[From, Until)`. `From > Until` wraps overnight.
3. A member that is ineligible is excluded from BOTH selection paths: session pinning (weighted ring hash only considers eligible members) and keyless least-loaded.
4. When no member of a provider's pool is eligible, the provider itself is ineligible for that dispatch — the model falls through declaration order to the next matching provider, then `default_provider`. This is eligibility, never failure: no error, no 429.
5. A session pinned to a member whose window closes mid-session re-resolves to an eligible member on the next request (cache lost — unavoidable, the key is unusable). A stream already dispatched continues even if the window closes mid-request.
6. Legacy single-`upstream:` providers: the window (if set) applies to the implicit single member; without a window, behavior is byte-for-byte today's.
7. SIGHUP hot-reload rebuilds the pool tree from config: added/removed members, changed windows and caps apply without a process restart (existing reloader).

## Constraints

- Config schema: `Upstream` gains `Window *Window` (yaml `window`, `omitempty`); `Window = {From libtime.TimeOfDay, Until libtime.TimeOfDay}`. The legacy single-`upstream:` form's window becomes the single member's window (normalized like caps in `normalizeUpstreams`, spec 012).
- The window check uses the injected `libtime.CurrentDateTimeGetter` (already the router's time source, spec 012/013) — the check is deterministic and testable with a fixed clock.
- Time-of-day comparison uses `libtime.TimeOfDay.Before/After/Equal` (wall-clock in the attached location; `from > until` wraps overnight). No new dependencies — `github.com/bborbe/time` v1.27.6 is already in go.mod.
- A closed window never produces a router error or 429 — it only changes eligibility/selection. The `[route]` line may note the fall-through (`window=closed -> <fallback>`).
- Pinning stays stateless; a session pinned to a now-ineligible member just re-resolves among eligible members (no session→member map to invalidate).
- Tests follow the repo's Ginkgo convention; fixed-clock tests must not depend on real wall-clock time; no real 30s waits. No AI attribution.
- Docs: `docs/config.md` + `docs/config.example.yaml` document `window:`; CHANGELOG `## Unreleased` bullet.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Pinned member's window closes mid-session | Next request re-resolves to an eligible member (cache lost, unavoidable — key unusable) | None — selection handles it |
| All of a provider's members closed | Provider ineligible → falls through to next matching provider / `default_provider`; no error, no 429 | None — fall-through is the recovery; `[route] ... window=closed -> <fallback>` confirms |
| Malformed time / unknown IANA location | Config rejected at load | Correct the value; SIGHUP re-applies |
| Overlapping windows (both members eligible) | Weighted ring + least-loaded split between them (existing spec 012 selection) | None — normal pool behavior |
| Config reload mid-request | Old tree finishes in-flight; new tree serves subsequent (existing reloader semantics) | None |
| Clock skew / valid-but-wrong IANA location (e.g. `Europe/London` instead of `Europe/Berlin`) | Eligibility boundary shifts by the offset — the off-peak unlimited key could be used inside business hours (the one failure with contractual consequence) | Operator verifies the `[route] ... window=closed` lines against the expected boundary, fixes the location, SIGHUPs |

## Security / Abuse

- The window is eligibility-only: it never widens access or bypasses auth (`allowedApiKeys` unchanged). A client cannot influence which window applies — it is server-side config + server clock.
- The off-peak unlimited key is used only within its configured window; malformed values and unknown locations fail validation, while a valid-but-wrong location (e.g. `Europe/London` for `Europe/Berlin`) shifts the boundary by its offset — the operator verifies the `[route] … window=closed` lines against the expected boundary and corrects the value via SIGHUP.
- No internal state (window definitions, member keys) is exposed in responses or logs beyond the intended `[route]` line (which names the upstream URL, already the existing log shape).

## Suggested Decomposition

Prompts generated in this order — each row is one prompt.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Config: `Upstream.Window` type + `libtime.TimeOfDay` parsing + validation (malformed/unknown location rejected, overnight wrap accepted, legacy single-`upstream:` window normalization) + backward-compat load rows | 1, 6 | 1 | — |
| 2 | Eligibility filter in pool selection: `window.Contains(now)` using the injected clock; exclude ineligible members from pinning + least-loaded; provider fall-through when no member eligible; `[route] ... window=closed` line; fixed-clock tests (complementary windows, overnight wrap, IANA boundary, never-429) | 2, 3, 4, 5, 7 | 2, 3, 4, 5, 7 | prompt 1 |
| 3 | Docs + CHANGELOG: `docs/config.md`, `docs/config.example.yaml`, `## Unreleased` bullet | 1 (documented) | docs greps only | prompts 1–2 |

Rationale: prompt 1 lands the config contract and parsing; prompt 2 wires the eligibility filter into the existing spec-012 pool selection (pinning + least-loaded) with the fixed-clock test matrix; prompt 3 documents what 1–2 shipped and owns no acceptance criterion itself.

## Do-Nothing Option

The off-peak unlimited DeepSeek key stays unusable (it can't be added to the pool without risking use during business hours, violating the restriction), or the operator manually swaps provider configs at window boundaries twice a day. The fix is a config field + an eligibility check in the existing pool selection — deferring keeps the unlimited key idle off-peak and the day key throttled, when one pool entry would serve both.

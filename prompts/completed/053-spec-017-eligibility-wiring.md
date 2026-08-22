---
status: completed
spec: [017-weekday-window-upstreams]
summary: 'Wired the spec-017 weekday days: filter into the spec-014 eligibility path: UpstreamMember gained Days, memberEligible became the window AND days conjunction, the factory copies each member''s days: onto the runtime pool member (SIGHUP-rebuild live), and fixed-clock Ginkgo rows prove the weekend all-day, offset-boundary, location-boundary, complementary three-member, keyless least-loaded, and provider fall-through behaviors through the real dispatch path — make precommit green.'
execution_id: claude-code-router-weekday-exec-053-spec-017-eligibility-wiring
dark-factory-version: dev
created: "2026-08-22T12:05:00Z"
queued: "2026-08-22T12:40:46Z"
started: "2026-08-22T12:45:00Z"
completed: "2026-08-22T12:54:34Z"
branch: dark-factory/weekday-window-upstreams
---

# Eligibility AND: `days:` weekday filter in pool selection + factory wiring + fixed-clock tests

<summary>
- A member whose weekday is not in its `days:` set is INELIGIBLE for that dispatch — skipped by BOTH session pinning (the weighted ring hash is computed over eligible members only) and keyless least-loaded selection, exactly like the spec-014 time-window filter it composes with.
- Eligibility is the AND of the two conditions: a member is eligible only while `(window absent OR window.Contains(now)) AND (days absent OR days.Contains(weekday(now)))`; a member with `days:` but no `window:` is eligible all day on its listed days.
- The weekday is resolved in the member's attached IANA location (inline `days:` location, else the `window:` from/until location, else UTC) — never the router host's local day, so a Berlin Saturday resolves on the weekend member even when the injected clock reads UTC Friday.
- A provider whose pool has no eligible member (every member's window AND days exclude now) falls through declaration order to the next matching provider or `default_provider` with the existing `[route] ... window=closed -> <fallback>` line — eligibility, never an error or 429, no router code changes (the days filter flows through the existing `HasEligibleMember` gate).
- The factory copies each configured member's `days:` onto the runtime pool member (same direct `*pkg.Days` copy as the `window:`), so a `days:` change applies on SIGHUP — a rebuilt `CreateRouterFromConfig` enforces the edited days without a restart.
- Fixed-clock Ginkgo rows prove the full behavior through the real dispatch path: a days-only weekend member eligible all day Sat+Sun (Berlin) and ineligible Mon–Fri, the offset-boundary instants (UTC Friday evening = Berlin Saturday, UTC Sunday evening = Berlin Monday), the three-member complementary table (weekend / weekday-day / weekday-night) yielding exactly one eligible member per (day, time) with the distinct 16/50/50 caps traveling with each member, the Berlin-vs-UTC location comparison, the keyless least-loaded skip, and the full-path provider fall-through with never-429 coverage.
- The model pool and model router need NO functional changes — the weekday filter is entirely inside the upstream pool handler's eligibility scan, which the existing `WindowEligible`/`HasEligibleMember` seam already gates on.
</summary>

<objective>
Wire the spec-017 weekday filter into the shipped spec-014 eligibility path: `UpstreamMember` gains a `Days` field, `memberEligible` becomes the window AND days conjunction, the factory copies each member's `days:` + clock onto the runtime member (SIGHUP-rebuild applies a `days:` change), and fixed-clock Ginkgo rows prove the weekend all-day, three-member complementary, offset-boundary, and location-boundary behaviors through the real dispatch path.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- Read `pkg/config.go` — the `Days` type (`Weekdays libtime.Weekdays`, `Location *stdtime.Location`), `(*Days).UnmarshalText`, and `(*Days).Contains(now libtime.DateTime, window *Window) bool` from spec-017 prompt 1 (already in the tree). Prompt 2 consumes `Contains` in the pool handler.
- Read `pkg/handler/upstream-pool-handler.go` — `UpstreamMember{Upstream, Handler, Weight, InFlight, Window *pkg.Window, Now func() libtime.DateTime}`, the `WindowEligible` interface + `windowEligible(h http.Handler)` helper, `realNow`, `memberEligible(i int)` / `eligibleIndices(ctx)` / `HasEligibleMember()`, and `NewUpstreamPoolHandler`'s doc comment. This prompt adds the `Days` field to `UpstreamMember`, reworks `memberEligible` into the window AND days conjunction, and updates the affected doc comments. `pinSlot` / `leastLoaded` / `ServeHTTP` / `inFlight` do NOT change — they already operate over the eligible subset, which now simply excludes days-ineligible members too.
- Read `pkg/handler/model-pool.go` and `pkg/handler/model-router.go` — NO functional changes here: the model pool's `memberEligible` delegates to `windowEligible(p.members[i].Handler)` and the router's fall-through gate consults `windowEligible(route.Handler)` → `HasEligibleMember()`, so a days-closed provider falls through with the existing `[route] ... window=closed -> <fallback>` line (spec AC 2 — the log line format is unchanged). Only the `WindowEligible` interface's doc comment in `upstream-pool-handler.go` mentions "time windows" and should be widened to "eligibility (time windows and weekday sets)".
- Read `pkg/factory/factory.go` — the `handler.UpstreamMember{...}` literal in `CreateRouterFromConfig`'s per-upstream wiring loop (currently `Window: up.Window, Now: o.currentDateTime.Now`). This prompt adds `Days: up.Days`. The `WithCurrentDateTime` option seam already exists — no new option, no signature changes.
- Read the shared test helpers (all package-level and reusable across files in their packages): `pkg/handler/time-window_test.go` (`mustLoadLocation`, `berlin`, `mustTOD`, `at(h, min, loc)` fixed to 2026-08-19, `pinSlotID`, `pinID`, `windowedMember`, `sessionedReq`, `windowStub`), `pkg/handler/model-router_test.go` (`labelHandler`, `captureStderr`, `alwaysSample`, `testMetrics`, `testDateTime`), `pkg/handler/upstream-pool-handler_test.go` (`countingHandler` + `invocations()`), `pkg/handler/concurrency-limiter_test.go` (`blockingHandler` / `newBlockingHandler` / `entryCount` / `release`), `pkg/factory/time_window_wiring_test.go` (`mustTOD`, `newMessagesRequest`, `sessionedRequest`, `serveAsync`, `buildRouter` — NOTE: `newMessagesRequest`, `sessionedRequest`, `serveAsync`, `buildRouter` are LOCAL closures inside the file's Describe block, so the new days rows go INSIDE that same file/Describe to reuse them), `pkg/factory/upstream_pool_wiring_test.go` (`poolUpstream` / `newPoolUpstream`, package-level), `pkg/factory/model_pool_wiring_test.go` (`sessionPinnedToSlot`, package-level), `pkg/factory/auth_middleware_wiring_test.go` (`isolatedRegistry`, package-level), `pkg/factory/trace_wiring_test.go` (`captureStderr`, package-level).
- The `libtime` API (`github.com/bborbe/time` v1.27.9): `libtime.CurrentDateTimeGetter` = `interface{ Now() DateTime }`; `libtime.NewCurrentDateTime()` returns a getter with `SetNow(DateTime)`; `libtime.DateTime` is `type DateTime stdtime.Time` with `func (d DateTime) Time() stdtime.Time`; `libtime.Weekdays.Contains(libtime.Weekday) bool` and the `libtime.Sunday`..`libtime.Saturday` constants (spec-017 prompt 1 used these). Weekday resolution uses the injected clock ONLY — never `time.Now()` in business logic.
- Coding plugin docs (in-container paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md` — `github.com/bborbe/time` injection, `libtime.NewCurrentDateTime()`.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md` — factory wiring + option seams (no new seam needed here).
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — glog verbosity (the `[route] ... window=closed` line stays V(2), unchanged).
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, `Eventually` / `Consistently` with small explicit waits.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` — selection runs in the request goroutine; goroutines allowed in `_test.go`.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md` — the eligibility scan keeps its per-iteration `ctx.Done()` checks.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — GoDoc conventions.
- Read `docs/dod.md` — GoDoc on every new exported identifier; test coverage ≥ 80% on changed code.
</context>

<requirements>
1. **`UpstreamMember.Days` + `memberEligible` rework in `pkg/handler/upstream-pool-handler.go`** (spec DB 2/3, AC 2/3/4/5). `pkg/config.go`'s `Days` is already imported via the existing `pkg "github.com/bborbe/claude-code-router/pkg"` import — no import changes.
   a. Add to the `UpstreamMember` struct, after `Window`:
      ```go
      // Days, when non-nil, is the member's weekday eligibility set: the
      // member is eligible only while Days.Contains(now, Window) holds
      // (spec 017). Nil = every day.
      Days *pkg.Days
      ```
   b. Rework `memberEligible` into the window AND days conjunction (spec Desired Behavior 2). The current method short-circuits `if m.Window == nil { return true }` — that shortcut must now also check `m.Days == nil`. Exact replacement:
      ```go
      // memberEligible reports whether member i is eligible for a dispatch:
      // (window absent OR window.Contains(now)) AND (days absent OR
      // days.Contains(now)). The clock is read only when a member carries a
      // window or a days set; a member with neither is always eligible,
      // byte-for-byte today (spec 014 / 017).
      func (p *upstreamPoolHandler) memberEligible(i int) bool {
          m := p.members[i]
          if m.Window == nil && m.Days == nil {
              return true
          }
          now := m.Now
          if now == nil {
              now = realNow
          }
          if m.Window != nil && !m.Window.Contains(now()) {
              return false
          }
          if m.Days != nil && !m.Days.Contains(now(), m.Window) {
              return false
          }
          return true
      }
      ```
   c. Widen the `WindowEligible` interface's doc comment to cover weekday sets (the interface name and signature stay unchanged): replace "because none of their pool members' time windows contain \"now\"" with wording like "because none of their pool members are eligible — their time windows and/or weekday sets exclude \"now\"". Also extend the `UpstreamMember` struct doc comment and `NewUpstreamPoolHandler`'s doc comment with one clause noting a member whose weekday is not in its days set is excluded from both selection paths (spec 017). Do NOT change `pinSlot`, `leastLoaded`, `ServeHTTP`, `HasEligibleMember`, `eligibleIndices`, or `inFlight` — they already operate over the eligible subset.
   d. Do NOT touch `pkg/handler/model-pool.go` or `pkg/handler/model-router.go` — days ineligibility flows through the existing `windowEligible`/`HasEligibleMember` gate with no functional change, and the `[route] ... window=closed -> <fallback>` log line stays byte-for-byte (spec AC 2 evidence).

2. **Factory wiring in `pkg/factory/factory.go`** (spec AC 7, "a `days:` change applies on SIGHUP"). In `CreateRouterFromConfig`'s per-upstream wiring loop, in the `handler.UpstreamMember{...}` literal, add `Days: up.Days` next to `Window: up.Window` (the runtime field is the SAME `*pkg.Days` type as the config `Upstream.Days`, so this is a direct copy; nil stays nil = every day). No new option, no signature change; `main.go` / `pkg/cli.go` / `CreateServer` untouched.

3. **Handler tests — new rows in `pkg/handler/time-window_test.go`** (package `handler_test`, reusing `mustTOD`, `berlin`, `pinID`, `sessionedReq`, `countingHandler`, `labelHandler`, `captureStderr`, `alwaysSample`, `testMetrics`, `testDateTime`, `windowStub`). Add package-local helpers first:
   ```go
   // mustDays parses a "comma-separated weekday names, optional location"
   // value into a *pkg.Days, failing the test on a malformed one.
   func mustDays(s string) *pkg.Days {
       d := &pkg.Days{}
       Expect(d.UnmarshalText([]byte(s))).To(Succeed())
       return d
   }

   // daysMember builds an UpstreamMember carrying the given days set and
   // the fixed clock's Now, mirroring the factory wiring. window may be
   // nil (a days-only member).
   func daysMember(
       upstream, days string,
       h http.Handler,
       clock libtime.CurrentDateTimeGetter,
       window *pkg.Window,
   ) handler.UpstreamMember {
       return handler.UpstreamMember{
           Upstream: upstream,
           Handler:  h,
           Weight:   1,
           Window:   window,
           Days:     mustDays(days),
           Now:      clock.Now,
       }
   }

   // poolEligible reports whether a pool over the given members has at
   // least one eligible member right now, mirroring the router's
   // windowEligible gate.
   func poolEligible(members ...handler.UpstreamMember) bool {
       pool := handler.NewUpstreamPoolHandler(context.Background(), members)
       e, ok := pool.(interface{ HasEligibleMember() bool })
       if !ok {
           return true
       }
       return e.HasEligibleMember()
   }

   // atDate returns a fixed-clock DateTime for the given date/time in loc,
   // so weekday tests never depend on the wall clock. Fixed dates used
   // below: 2026-08-19 Wednesday, 2026-08-21 Friday, 2026-08-22 Saturday,
   // 2026-08-23 Sunday, 2026-08-24 Monday.
   func atDate(y, mo, d, h, min int, loc *stdtime.Location) libtime.DateTime {
       return libtime.DateTime(stdtime.Date(y, stdtime.Month(mo), d, h, min, 0, 0, loc))
   }
   ```
   Add a new `Describe("UpstreamPoolHandler weekday eligibility (days)", ...)` block. Rows (each `It`):
   - **AC 3 — a days-only weekend member is eligible all day Sat+Sun (Berlin) and ineligible Mon–Fri, through the real dispatch path:** drive each instant through a FRESH pool with fresh counters (the `countingHandler` type has no reset — build a new weekend/open pair per instant so the assertions are clean). `id := pinID(0, 1, 1)` (pinned to member 0 over the full ring). For each instant: `clock.SetNow(now)` with `pool := handler.NewUpstreamPoolHandler(context.Background(), []handler.UpstreamMember{daysMember("https://weekend", "saturday, sunday Europe/Berlin", &countingHandler{}, clock, nil), {Upstream: "https://open", Handler: &countingHandler{}, Weight: 1}})`; then fire one `sessionedReq(id)` AND one keyless request (`httptest.NewRequest(http.MethodPost, "/v1/messages", nil)`), and assert every `rec.Code == 200`, the weekend member served BOTH requests (`weekend.invocations() == 2`), and the open member served none (`open.invocations() == 0`). ELIGIBLE Berlin instants: Sat 00:01 (`atDate(2026,8,22,0,1,berlin)`), Sat 10:00, Sat 23:59, Sun 00:01 (`atDate(2026,8,23,0,1,berlin)`), Sun 23:59. INELIGIBLE Berlin instants (the same pinned id + keyless request must BOTH serve the open member — `open.invocations() == 2`, `weekend.invocations() == 0`): Mon 00:01 (`atDate(2026,8,24,0,1,berlin)`), Mon 10:00, Fri 23:59 (`atDate(2026,8,21,23,59,berlin)`). This row is the AC 3 "eligible all day on Sat/Sun, ineligible Mon–Fri" evidence.
   - **AC 3 — the boundary is the attached location's calendar, not host UTC (offset boundary):** same weekend/open pool. `clock.SetNow(libtime.DateTime(stdtime.Date(2026, 8, 21, 22, 30, 0, 0, stdtime.UTC)))` — UTC Friday 22:30 IS Berlin Saturday 00:30 — fire `sessionedReq(id)` → serves weekend (weekend counter 1, open 0). Then `clock.SetNow(libtime.DateTime(stdtime.Date(2026, 8, 23, 22, 30, 0, 0, stdtime.UTC)))` — UTC Sunday 22:30 IS Berlin Monday 00:30 — fire the same id → serves open (open counter 1, weekend unchanged). These are the AC 3 offset-boundary rows.
   - **AC 5 — a member whose location is Europe/Berlin flips eligibility on Berlin's weekday while UTC disagrees:** members `berlin-sun` = `daysMember("https://berlin", "sunday Europe/Berlin", labelHandler("berlin-sun"), clock, nil)` and `utc-sun` = `daysMember("https://utc", "sunday UTC", labelHandler("utc-sun"), clock, nil)`; `id := pinID(0, 1, 1)`. `clock.SetNow(libtime.DateTime(stdtime.Date(2026, 8, 22, 22, 30, 0, 0, stdtime.UTC)))` (UTC Sat 22:30 = Berlin Sun 00:30) → serves `berlin-sun` (assert `rec.Body.String() == "berlin-sun"`). `clock.SetNow(libtime.DateTime(stdtime.Date(2026, 8, 23, 22, 30, 0, 0, stdtime.UTC)))` (UTC Sun 22:30 = Berlin Mon 00:30) → serves `utc-sun`. This is the AC 5 evidence (Europe/Berlin vs UTC at a boundary instant).
   - **AC 4 — complementary three-member pool: exactly one eligible member per (day, time) with the distinct cap traveling with each member:** three members, each `labelHandler`-backed and wrapped in its own concurrency limiter so the cap travels with the member: `weekend` = `daysMember("https://weekend", "saturday, sunday Europe/Berlin", handler.NewConcurrencyLimiter(labelHandler("weekend"), 50, stdtime.Second), clock, nil)`; `day` = `handler.UpstreamMember{Upstream: "https://day", Handler: handler.NewConcurrencyLimiter(labelHandler("day"), 16, stdtime.Second), Weight: 1, Window: &pkg.Window{From: mustTOD("08:00 Europe/Berlin"), Until: mustTOD("18:00 Europe/Berlin")}, Days: mustDays("monday, friday"), Now: clock.Now}`; `night` = the mirror with window `{From: mustTOD("18:00 Europe/Berlin"), Until: mustTOD("08:00 Europe/Berlin")}`, cap 50, label `"night"`. `pool := handler.NewUpstreamPoolHandler(context.Background(), []handler.UpstreamMember{weekend, day, night})`. Table of eight (day, time) points, instants expressed in Berlin:
     | instant (Berlin) | eligible member |
     |---|---|
     | Mon 10:00 (`atDate(2026,8,24,10,0,berlin)`) | day |
     | Mon 22:00 | night |
     | Mon 00:30 | night (weekend must NOT be eligible) |
     | Sat 00:30 (`atDate(2026,8,22,0,30,berlin)`) | weekend (night must NOT be eligible) |
     | Sat 10:00 | weekend |
     | Sat 22:00 | weekend |
     | Sun 10:00 (`atDate(2026,8,23,10,0,berlin)`) | weekend |
     | Fri 23:30 (`atDate(2026,8,21,23,30,berlin)`) | night |
     For each point: `clock.SetNow(now)`; assert EXACTLY ONE of `poolEligible(weekend)` / `poolEligible(day)` / `poolEligible(night)` is true and it is the expected member (the `poolEligible` helper builds a single-member pool per member — the real dispatch-path eligibility scan); then fire `sessionedReq(pinID(0, 1, 1, 1))` AND a keyless request through the real three-member pool and assert both `rec.Body.String() == <expected label>` (the distinct 16/50/50 limiter caps are on the members' handlers, so the serving label proves the cap travels with the member). This is the AC 4 evidence.
   - **AC 2 — a member outside its days is skipped by keyless least-loaded selection:** members `a` = `daysMember("https://a", "monday Europe/Berlin", &countingHandler{}, clock, nil)` (ineligible on a Saturday) and `b` = `{Upstream: "https://b", Handler: &countingHandler{}, Weight: 1, InFlight: func() int { return 5 }}`. `clock.SetNow(atDate(2026, 8, 22, 10, 0, berlin))` (Saturday). Fire a keyless request → serves `b` even though `a` reports less load (`a.invocations() == 0`, `b.invocations() == 1`) — the least-loaded scan only considers eligible members (mirror of the spec-014 DB 3 row, with days).
   - **AC 2 — provider fall-through: a provider whose only member is outside its days logs `window=closed` and falls through, never 429:** build a REAL `handler.NewUpstreamPoolHandler` single-member pool `weekday-pool` = `daysMember("https://weekday", "monday, friday Europe/Berlin", &countingHandler{}, clock, nil)` (ineligible at the fixed now). `clock.SetNow(atDate(2026, 8, 22, 10, 0, berlin))` (Saturday). `mux := handler.NewModelRouter([]handler.ModelRoute{{Pattern: "deepseek-*", ProviderName: "weekday-pool", Handler: <the pool>}}, "default", labelHandler("fallback"), nil, alwaysSample, testMetrics, testDateTime)`. With `flag.Set("logtostderr","true")` + `flag.Set("v","2")` + `captureStderr`, fire `httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"deepseek-x"}`))` → `rec.Code == 200`, `rec.Body.String() == "fallback"`, the weekday-pool counter is 0, captured stderr contains `[route] provider=weekday-pool window=closed -> default`, and does NOT contain `status=429` or `ERROR`.
   - **DB 1 — a member with no window and no days behaves byte-for-byte as before:** covered by the pre-existing `DB 1: a window-less pool behaves byte-for-byte as before` row (unchanged) — do not modify it, and do not add `days:` to any pre-existing row (spec AC 6: `grep -c 'days:' pkg/handler/time-window_test.go` on the PRE-EXISTING rows stays 0; only the new rows reference `days:`).

4. **Factory wiring test — new rows in `pkg/factory/time_window_wiring_test.go`** (package `factory_test`; the file's `mustTOD`, `newMessagesRequest`, `sessionedRequest`, `serveAsync`, `buildRouter` closures and the package-level `poolUpstream`/`newPoolUpstream`, `sessionPinnedToSlot`, `isolatedRegistry`, `captureStderr` are all in scope — add the rows INSIDE the existing `Describe("CreateRouterFromConfig time-window wiring", ...)` block so the local closures are usable). Add a package-local `mustDays` helper (same implementation as the handler test — `(&pkg.Days{}).UnmarshalText`), and a package-local fixed Berlin location:
   ```go
   // berlin is the fixed IANA location used by the weekday wiring rows.
   var berlin = func() *stdtime.Location {
       l, err := stdtime.LoadLocation("Europe/Berlin")
       if err != nil {
           panic(err)
       }
       return l
   }()
   ```
   Rows:
   - **AC 7 — SIGHUP applies a changed `days:` (a rebuilt pool tree enforces the edited days):** fixed clock `clock := libtime.NewCurrentDateTime(); clock.SetNow(libtime.DateTime(stdtime.Date(2026, 8, 21, 10, 0, 0, 0, berlin)))` (Friday). cfg1: provider `pool` with `Upstreams` [A = server `a` with `Days: mustDays("monday, friday Europe/Berlin")` (eligible Friday), B = server `b` no days], `Models: ["m*"]`, `Router.DefaultProvider: "pool"`. `router1 := buildRouter(cfg1, clock)`. `id := sessionPinnedToSlot(0, 1, 1)` (pinned to A over the full ring). Fire `sessionedRequest(id, "m1")` through router1 → `Eventually` server A's in-flight is 1, B's is 0 (A is eligible at Friday 10:00). Then cfg2 — a SECOND `CreateRouterFromConfig` exactly mirrors the reloader's SIGHUP rebuild — same provider but A's days changed to `mustDays("saturday, sunday Europe/Berlin")` (closed at Friday 10:00). `router2 := buildRouter(cfg2, clock)`. Fire the SAME session id through router2 → only B is eligible, so it serves B (A's in-flight stays 0, B's rises to 1). Unblock/close the upstreams in `AfterEach` as the harness does. Name the `It(...)` with a description containing the word "rebuilt" (e.g. "rebuilds the pool tree — a rebuilt pool enforces a changed days set on SIGHUP").
   - **AC 2 — full-path fall-through + never 429 with a days-closed provider:** fixed clock at a Wednesday: `clock.SetNow(libtime.DateTime(stdtime.Date(2026, 8, 19, 10, 0, 0, 0, stdtime.UTC)))` (2026-08-19 is Wednesday; a `monday, friday` member is closed). Provider `pool` = single member A with `Days: mustDays("monday, friday Europe/Berlin")` (closed at the fixed now), `Models: ["m*"]`; `Router.DefaultProvider: "fallback"` where provider `fallback` = server `b` with `Models: ["*"]` (always eligible); `ProviderOrder: []string{"pool", "fallback"}` so the closed pool is considered before the fallback (programmatic configs sort keys otherwise). With `flag.Set("v","2")` + `flag.Set("logtostderr","true")` + `captureStderr` (mirror the flag save/restore pattern of the existing fall-through row in this file), fire `newMessagesRequest("m1")` through `buildRouter(cfg, clock)` → 200 served by fallback (server B), captured output contains `[route] provider=pool window=closed -> fallback`, does NOT contain `status=429` or `ERROR`. Unblock/close the upstreams in `AfterEach` as the harness does.

5. **No further changes.** Do NOT add Prometheus metrics, config knobs, opt-out flags, or tunable thresholds (spec Constraints/Non-goals — the observability surface is the unchanged `[route]`/`[req]` log lines). Do NOT touch `pkg/config.go`'s parse/validation beyond what prompt 1 shipped, `pkg/handler/model-pool.go`, `pkg/handler/model-router.go`, `pkg/handler/session-id.go`, `pkg/handler/concurrency-limiter.go`, `main.go`, `pkg/cli.go`, `docs/`, or `CHANGELOG.md`. The exported `NewUpstreamPoolHandler`, `NewModelRouter`, and `NewModelRouterWithPools` signatures stay byte-for-byte unchanged.
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- EXECUTION-ORDER DEPENDENCIES: depends on spec-017 prompt 1 (`pkg.Days` + `Upstream.Days` / `Provider.Days` + parsing/validation + `Days.Contains`) and on the shipped spec-014 machinery (`UpstreamMember.Window/Now`, `memberEligible`, `eligibleIndices`, `WithCurrentDateTime`, `Window.Contains`) being in the tree.
- Eligibility is the AND of the time and weekday conditions (spec Desired Behavior 2): a member is eligible only while `(window absent OR window.Contains(now)) AND (days absent OR days.Contains(weekday(now)))`. A member with only `window:` is byte-for-byte spec 014; a member with only `days:` is all-day on those weekdays; absent both = always eligible, byte-for-byte today (spec Non-goal: no change to spec-014 semantics for members without `days:`).
- Weekday resolution uses the router's existing injected `libtime.CurrentDateTimeGetter` and the resolved location — the router's clock is the only time source, never `time.Now()` in business logic; the weekday boundary is the location's calendar, never the router host's local day (spec Constraints, AC 5). No new clock mechanism, no new factory option, no clock field on Config.
- A provider whose pool has no eligible member (window AND days both exclude now) is INELIGIBLE: the model falls through declaration order to the next matching provider or `default_provider` with the existing V(2) `[route] ... window=closed -> <fallback>` line — never an error, never a 429, no health check, no probe (spec Failure Modes row "Provider fully ineligible"; the `[route]`/`[req]` line contracts and all metrics are unchanged).
- Pinning stays STATELESS: selection recomputes the eligible ring per request from the injected clock — no session→member map to invalidate (spec DB 6). Overlapping members (e.g. a weekend member accidentally also eligible on a weekday) fall back to the normal spec-014 overlap semantics — both eligible, pinning/least-loaded selects per the normal rules, no error (spec Failure Modes row "Overlapping members"; no new handling).
- Do NOT add Prometheus metrics, config knobs, opt-out flags, or tunable thresholds — the spec's observability surface is the `[route]`/`[req]` log lines, unchanged (spec Non-goals; the 039-style metric-invention incident is a hard-reject precedent).
- Tests follow the repo's Ginkgo convention and must NOT depend on real wall-clock time — every weekday row drives a fixed `libtime.NewCurrentDateTime()` via `SetNow` and asserts with small explicit waits (spec Constraints "fixed-clock tests must not depend on real wall-clock time"). Fixed dates used: 2026-08-19 Wednesday, 2026-08-21 Friday, 2026-08-22 Saturday, 2026-08-23 Sunday, 2026-08-24 Monday (instants are expressed in the member's resolution location, `Europe/Berlin`, per the ACs; the offset-boundary rows set the clock to a UTC instant whose Berlin weekday differs).
- `days:` is server-side config + the router's injected clock, evaluated per request; a client cannot influence which member applies and it never widens access or bypasses `allowedApiKeys` (spec Security). The days value is never logged.
- No AI attribution in code or comments. `make precommit` must remain green — run it before declaring done. Follow `docs/dod.md` (GoDoc on every new exported identifier; ≥ 80% coverage on changed code).
- Do NOT touch `docs/` or `CHANGELOG.md` in this prompt — spec-017 prompt 3 owns documentation.
</constraints>

<verification>
make precommit

# Wiring landed:
grep -n 'Days \*pkg.Days\|Days.Contains(now(), m.Window)' pkg/handler/upstream-pool-handler.go
grep -n 'Days: up.Days' pkg/factory/factory.go

# AC 3/4/5 evidence — handler rows reference days:
grep -c 'days' pkg/handler/time-window_test.go           # expect >=1
grep -c 'saturday' pkg/handler/time-window_test.go       # expect >=1 (weekend rows)
grep -c 'window=closed' pkg/handler/time-window_test.go  # expect >=1 (AC 2 fall-through evidence)

# AC 7 evidence — factory SIGHUP days row:
grep -c 'saturday' pkg/factory/time_window_wiring_test.go  # expect >=1 (the days SIGHUP + fall-through rows)
grep -c 'rebuilt' pkg/factory/time_window_wiring_test.go   # expect >=1

# Full suite + race (spec Verification container-executable):
go test -mod=mod -count=1 ./pkg/handler/...
go test -mod=mod -count=1 ./pkg/factory/...
go test -mod=mod -race -count=1 ./pkg/handler/...
go test -mod=mod -count=1 ./pkg/...
</verification>

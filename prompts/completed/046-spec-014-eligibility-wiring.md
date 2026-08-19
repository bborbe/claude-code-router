---
status: completed
spec: [014-time-windowed-upstreams]
summary: 'Wired the spec-014 time-window eligibility filter into pool selection: pkg.Window.Contains, per-request eligible-subset pinning/least-loaded in the upstream pool handler and model pool, provider fall-through with window=closed logging in the model router, WithCurrentDateTime factory option wiring member windows + clock (SIGHUP-rebuild enforces edited windows), and fixed-clock tests through the real dispatch path'
execution_id: claude-code-router-time-window-exec-046-spec-014-eligibility-wiring
dark-factory-version: dev
created: "2026-08-19T20:26:00Z"
queued: "2026-08-19T20:42:15Z"
started: "2026-08-19T20:58:19Z"
completed: "2026-08-19T21:12:40Z"
---

# Eligibility filter: `window.Contains(now)` in pool selection + provider fall-through + fixed-clock tests

<summary>
- A member whose time window does not contain "now" (from the injected `libtime.CurrentDateTimeGetter`, evaluated in the window value's attached IANA location) is INELIGIBLE for that dispatch — it is skipped by BOTH session pinning (the weighted ring hash is computed over eligible members only) and keyless least-loaded selection, in the upstream pool handler and the model pool.
- When NO member of a provider's pool is eligible, the provider itself is ineligible: the model router falls through declaration order to the next glob-matching provider that has an eligible member, then to `default_provider`, emitting `[route] provider=<p> window=closed -> <fallback>` at V(2) — a closed window is eligibility, never an error, never a 429.
- A session pinned to a member whose window closes mid-session re-resolves to an eligible member on the next request (selection recomputes eligibility per request — stateless, no session→member map to invalidate); a stream already dispatched completes even if the boundary passes.
- Complementary windows (day member 08:00–18:00 Europe/Berlin, cap 16; off-peak member 18:00–08:00, cap 50) on one provider yield exactly one eligible member per period, verified through the real dispatch path with a fixed clock at 10:00 (day member serves) and 22:00 (off-peak member serves).
- Overnight wrap (`from: "22:00"` `until: "06:00"`) treats 02:00 as inside and 14:00 as outside; the window is evaluated in the value's attached location (a 15:30 UTC "now" is inside a 17:00–18:00 Europe/Berlin window) — never the router host's local time.
- The factory gains a `WithCurrentDateTime(clock)` test seam (mirroring `WithMetricsRegisterer`), wires each member's window + clock into the pool, uses the injected clock for the router, and a rebuilt `CreateRouterFromConfig` (the SIGHUP path) enforces a changed `window:` — plus a full-path fall-through row proving no 429/no error when a provider is fully closed.
</summary>

<objective>
Wire the spec-014 eligibility filter into the shipped spec-012/013 pool selection: `pkg.Window.Contains(now)` evaluates a member's window against the injected clock; the upstream pool handler and model pool exclude ineligible members from pinning and least-loaded; the model router falls through to the next eligible provider or `default_provider` (logging `window=closed`) when a provider has no eligible member — all proven by fixed-clock tests through the real dispatch path, with SIGHUP-rebuild and never-429 coverage.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- Read `pkg/config.go` — the `Window` type (`From` / `Until` as `libtime.TimeOfDay`) and `Upstream.Window` / `Provider.Window` from spec-014 prompt 1 (already in the tree). Prompt 2 adds the `Contains` method to `pkg.Window` here.
- Read `pkg/handler/upstream-pool-handler.go` (spec-012, shipped) — `UpstreamMember{Upstream, Handler, Weight, InFlight}`, `NewUpstreamPoolHandler` (precomputes `cumulative` / `totalWeight`), and `pinSlot` / `leastLoaded` (weighted FNV-1a ring + round-robin least-loaded). This prompt adds the window fields, the eligibility filter, and `HasEligibleMember`, and reworks `pinSlot` / `leastLoaded` to compute the ring over eligible members per request.
- Read `pkg/handler/model-pool.go` (spec-013, shipped) — `ModelPoolMember` / `ModelPool` with `pinSlot` / `leastLoaded` / `overflowTarget`. This prompt adds the eligibility filter (a member whose provider pool has no eligible member is skipped by all three).
- Read `pkg/handler/model-router.go` — the key-routing + glob-walk block (currently `matchedByKey` then `for _, route := range routes { ok, _ := path.Match(...); if ok { ...; break } }`) inside `if !matchedByPool`. This prompt adds the eligibility gate and the `window=closed` fall-through.
- Read `pkg/factory/factory.go` — `CreateRouterFromConfig`'s per-upstream wiring loop (the `handler.UpstreamMember{Upstream, Handler, Weight, InFlight}` construction), the `RouterOptionFunc` / `routerOptions` / `WithMetricsRegisterer` option seam, and the `handler.NewModelRouterWithPools(..., libtime.NewCurrentDateTime())` call. This prompt adds `WithCurrentDateTime`, wires each member's `Window` + `Now`, and switches the router to the injected clock.
- Read the shared test helpers: `pkg/handler/model-router_test.go` (`labelHandler`, `captureStderr`, `alwaysSample`, `testMetrics`, `testDateTime`), `pkg/handler/model-pool_test.go` (`poolPinSlot` / `sessionPinnedTo`), `pkg/factory/upstream_pool_wiring_test.go` (the `poolUpstream` blocking-upstream harness, `probePinnedTo`, `sessionedRequest`, `serveAsync`), `pkg/factory/model_pool_wiring_test.go` (`poolSlot` / `sessionPinnedToSlot`, `newMessagesRequest`), and `pkg/factory/trace_wiring_test.go` or `pkg/factory/auth_middleware_wiring_test.go` (`isolatedRegistry()`, `captureStderr`). All of these are package-`handler_test` / `factory_test` and reusable across files.
- `libtime` API (`github.com/bborbe/time` v1.27.6): `libtime.CurrentDateTimeGetter` = `interface{ Now() DateTime }`; `libtime.DateTime` is `type DateTime stdtime.Time` with `func (d DateTime) Time() stdtime.Time`; `libtime.NewCurrentDateTime()` returns a `CurrentDateTime` (getter + `SetNow(DateTime)` setter) — the fixed-clock seam for tests; `libtime.TimeOfDayFromTime(t stdtime.Time) TimeOfDay`; `(*TimeOfDay).Before/After/Equal(TimeOfDay) bool` compare the values as instants on a fixed date in each value's own attached location. `github.com/bborbe/time` test helpers also exist at `/home/node/.claude/plugins/marketplaces/coding/docs/../go-time-injection.md` (in-container: `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md`).
- Coding plugin docs (in-container paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-time-injection.md` — `github.com/bborbe/time` injection, `libtime.NewCurrentDateTime()`.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md` — factory wiring + option seams.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` — selection runs in the request goroutine; goroutines allowed in `_test.go`.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — glog verbosity (the `[route]` / `window=closed` lines are V(2), matching the existing `[route]` lines).
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, `Eventually` / `Consistently` with small explicit waits (no real 30s waits).
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `errors.Wrapf` in the factory.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — GoDoc conventions.
</context>

<requirements>
1. **`pkg.Window.Contains(now libtime.DateTime) bool` in `pkg/config.go`** (spec DB 2, AC 4/5). Add `stdtime "time"` (stdlib) to the imports alongside `libtime "github.com/bborbe/time"`, and add the method below the `Window` type. This is the single source of truth for window semantics, used by the pool handler (requirement 2). Exact contract:
   ```go
   // Contains reports whether now falls inside the window. Eligibility is
   // half-open: [From, Until). From > Until wraps overnight (e.g. 22:00 ->
   // 06:00 covers 02:00 and excludes 14:00). From == Until is an empty
   // window — no time is eligible. now is evaluated in the window's
   // attached location (From.Location, else Until.Location, else UTC), so
   // the boundary is the IANA wall clock of the config value, never the
   // router host's local time (spec DB 2, AC 5).
   func (w *Window) Contains(now libtime.DateTime) bool {
       loc := w.From.Location
       if loc == nil {
           loc = w.Until.Location
       }
       if loc == nil {
           loc = stdtime.UTC
       }
       tod := libtime.TimeOfDayFromTime(now.Time().In(loc))
       switch {
       case w.From.Equal(w.Until):
           return false
       case w.From.Before(w.Until):
           return (tod.Equal(w.From) || tod.After(w.From)) && tod.Before(w.Until)
       default:
           return tod.Equal(w.From) || tod.After(w.From) || tod.Before(w.Until)
       }
   }
   ```
   Do NOT add any other method, field, or clock — the clock is passed in by the pool handler.

2. **Eligibility in `pkg/handler/upstream-pool-handler.go`** (spec DB 3, AC 2/3/4/5/7). Add `libtime "github.com/bborbe/time"`, `stdtime "time"`, and `"github.com/bborbe/claude-code-router/pkg"` to the imports.
   a. **New interface + helper** (shared by the model pool and model router):
      ```go
      // WindowEligible is implemented by provider handlers that can be
      // ineligible for a dispatch because none of their pool members' time
      // windows contain "now" (spec 014). A handler that does not implement
      // the interface is always eligible.
      type WindowEligible interface {
          HasEligibleMember() bool
      }

      // windowEligible reports whether a provider handler has at least one
      // eligible pool member. Handlers without time windows are always
      // eligible.
      func windowEligible(h http.Handler) bool {
          if e, ok := h.(WindowEligible); ok {
              return e.HasEligibleMember()
          }
          return true
      }
      ```
   b. **`UpstreamMember` gains two fields** (exact names/types; nil Window = always eligible, today's behavior):
      ```go
      // Window, when non-nil, is the member's time-of-day eligibility
      // window: the member is eligible only while Window.Contains(now)
      // holds (spec 014). Nil = always eligible.
      Window *pkg.Window
      // Now returns the router's current time (the injected
      // libtime.CurrentDateTimeGetter). Consulted only when Window is
      // non-nil; nil Now falls back to the real clock.
      Now func() libtime.DateTime
      ```
      Add a package-level defensive fallback `var realNow = func() libtime.DateTime { return libtime.DateTime(stdtime.Now()) }` — the factory ALWAYS sets `Now` via the `WithCurrentDateTime` option default (requirement 5), so this fallback is only reached by direct-construction callers/tests; do not call `stdtime.Now()` anywhere else.
   c. **`memberEligible` / `eligibleIndices`** on `upstreamPoolHandler`:
      ```go
      // memberEligible reports whether member i's window contains "now"
      // (nil window = always eligible). The window check is the only place
      // the clock is read; selection below operates purely on the eligible
      // subset, recomputed per request.
      func (p *upstreamPoolHandler) memberEligible(i int) bool {
          m := p.members[i]
          if m.Window == nil {
              return true
          }
          now := m.Now
          if now == nil {
              now = realNow
          }
          return m.Window.Contains(now())
      }

      // eligibleIndices returns the indices of members eligible right now.
      func (p *upstreamPoolHandler) eligibleIndices() []int {
          idx := make([]int, 0, len(p.members))
          for i := range p.members {
              if p.memberEligible(i) {
                  idx = append(idx, i)
              }
          }
          return idx
      }
      ```
   d. **`HasEligibleMember`** (the `WindowEligible` implementation the router and model pool consult):
      ```go
      // HasEligibleMember reports whether at least one pool member is
      // eligible right now (spec 014 DB 4). A provider whose pool returns
      // false is ineligible for the dispatch and the model router falls
      // through to the next provider / default_provider.
      func (p *upstreamPoolHandler) HasEligibleMember() bool {
          return len(p.eligibleIndices()) > 0
      }
      ```
   e. **Rework `NewUpstreamPoolHandler`, `pinSlot`, `leastLoaded`** to compute the ring over the eligible subset per request. REMOVE the precomputed `cumulative` / `totalWeight` fields from the struct and stop computing them in `NewUpstreamPoolHandler` (eligibility is per-request, so the ring can no longer be precomputed; with every member eligible the result is identical to today's precomputed ring, so all existing tests keep passing). `pinSlot`:
      ```go
      func (p *upstreamPoolHandler) pinSlot(ctx context.Context, sessionID string) int {
          idx := p.eligibleIndices()
          if len(idx) == 0 {
              return 0
          }
          total := 0
          cumulative := make([]int, len(idx))
          for i, mi := range idx {
              total += p.members[mi].Weight
              cumulative[i] = total
          }
          h := fnv.New64a()
          _, _ = h.Write([]byte(sessionID))
          // #nosec G115 -- weights are config-validated positive ints; the
          // int->uint64 conversion is overflow-safe.
          slot := h.Sum64() % uint64(total)
          for i, c := range cumulative {
              select {
              case <-ctx.Done():
                  return 0
              default:
              }
              // #nosec G115 -- see above.
              if uint64(c) > slot {
                  return idx[i]
              }
          }
          return idx[len(idx)-1]
      }
      ```
      `leastLoaded`:
      ```go
      func (p *upstreamPoolHandler) leastLoaded(ctx context.Context) int {
          idx := p.eligibleIndices()
          if len(idx) == 0 {
              return 0
          }
          min := p.inFlight(idx[0])
          for i := 1; i < len(idx); i++ {
              select {
              case <-ctx.Done():
                  return 0
              default:
              }
              if load := p.inFlight(idx[i]); load < min {
                  min = load
              }
          }
          ties := make([]int, 0, len(idx))
          for _, mi := range idx {
              select {
              case <-ctx.Done():
                  return 0
              default:
              }
              if p.inFlight(mi) == min {
                  ties = append(ties, mi)
              }
          }
          rr := atomic.AddUint64(&p.rr, 1)
          return ties[(rr-1)%uint64(len(ties))]
      }
      ```
      Keep the existing per-iteration `ctx.Done()` checks inside both loops, exactly as today (on cancel return 0). Update the struct + `NewUpstreamPoolHandler` doc comments to describe per-request eligible-subset selection. The `inFlight` helper and the `[route] session=<id> upstream=<url>` line in `ServeHTTP` stay unchanged. Returning index 0 when zero members are eligible is the defensive last-resort (mirrors the existing `ctx.Done()` fallback); the router gate (requirement 4) makes it unreachable for glob/key routing.

3. **Eligibility in `pkg/handler/model-pool.go`** (task scope: eligibility extends `pinSlot`/`leastLoaded` in model-pool.go too; spec DB 3). Add `memberEligible` / `eligibleIndices` on `ModelPool`:
   ```go
   // memberEligible reports whether member i's provider pool has at least
   // one eligible upstream member right now (spec 014). A provider handler
   // that does not implement WindowEligible is always eligible.
   func (p *ModelPool) memberEligible(i int) bool {
       return windowEligible(p.members[i].Handler)
   }

   // eligibleIndices returns the indices of members whose provider is
   // eligible right now.
   func (p *ModelPool) eligibleIndices() []int {
       idx := make([]int, 0, len(p.members))
       for i := range p.members {
           if p.memberEligible(i) {
               idx = append(idx, i)
           }
       }
       return idx
   }
   ```
   Rework `pinSlot` and `leastLoaded` to compute the ring / least-loaded over `eligibleIndices()` only, exactly mirroring requirement 2e's structure (remove the precomputed `cumulative` / `totalWeight` fields from `ModelPool`; return 0 defensively when none eligible; keep `ctx.Done()` checks and the `#nosec G115` comments; keep the `rr` round-robin). Also rework `overflowTarget` to skip ineligible members (overflow to an eligible sibling). Note the pinned member is ALWAYS eligible — `pinSlot` only selects among `eligibleIndices()` — so the "excluded member is ineligible" edge is unreachable; the existing single-member fallback applies when no other eligible sibling exists. With every member eligible the behavior is identical to today, so all existing model-pool tests keep passing.

4. **Provider fall-through in `pkg/handler/model-router.go`** (spec DB 4, AC 2, Failure Modes rows "All of a provider's members closed"). Replace the existing key-routing + glob-walk block inside `if !matchedByPool` (anchor: the `matchedByKey := false` line) with the following. The `window=closed` log line formats are NORMATIVE — the AC evidence and tests grep them.
   ```go
   if !matchedByPool {
       var closedProvider string
       matchedByKey := false
       if presentedKey := PresentedApiKeyFromContext(r.Context()); presentedKey != "" {
           for _, route := range routes {
               if containsString(route.AllowedApiKeys, presentedKey) {
                   if !windowEligible(route.Handler) {
                       // The key-pinned provider has no eligible member: fall
                       // through to the glob walk / default (spec 014 DB 4).
                       closedProvider = route.ProviderName
                       break
                   }
                   providerName = route.ProviderName
                   target = route.Handler
                   requiresLeadingSystem = route.RequiresLeadingSystem
                   glog.V(2).Infof("[route] key matched provider=%s", providerName)
                   matchedByKey = true
                   break
               }
           }
       }
       for _, route := range routes {
           if matchedByKey {
               break
           }
           ok, _ := path.Match(route.Pattern, model)
           if !ok {
               continue
           }
           if !windowEligible(route.Handler) {
               if closedProvider == "" {
                   closedProvider = route.ProviderName
               }
               continue
           }
           providerName = route.ProviderName
           target = route.Handler
           requiresLeadingSystem = route.RequiresLeadingSystem
           glog.V(2).Infof("[route] model=%q matched %q -> provider=%s", model, route.Pattern, providerName)
           if closedProvider != "" && closedProvider != providerName {
               glog.V(2).Infof("[route] provider=%s window=closed -> %s", closedProvider, providerName)
           }
           break
       }
       if target == defaultHandler && closedProvider != "" {
           if closedProvider != defaultProviderName {
               glog.V(2).Infof("[route] provider=%s window=closed -> %s", closedProvider, defaultProviderName)
           } else {
               // Last-resort: the default provider itself is closed — serve it
               // anyway, never an error/429 (spec: closed window is eligibility,
               // never a failure). The line is the operator's signal.
               glog.V(2).Infof("[route] provider=%s window=closed", closedProvider)
           }
       }
   }
   ```
   Semantics: a provider whose pool has no eligible member is skipped by the glob walk and by key routing; the request is served by the next declaration-order matching provider with an eligible member, else `default_provider`. When `default_provider` is itself the only candidate and closed, it still serves (last resort, no error, no 429 — the operator's complementary-window config guarantees an eligible member always exists in practice). `liftSystemMessages`, dispatch, metrics, usage, and the `[req]` lines stay unchanged. Do NOT change the `matchedByPool` (model-pool) branch — a pool member's provider handler enforces member windows internally; model-pool-resolved requests are out of scope for the router-level fall-through.

5. **Factory wiring in `pkg/factory/factory.go`** (spec AC 7, "no new clock plumbing" — reuse `libtime.CurrentDateTimeGetter` via the existing option seam).
   a. Add to `routerOptions`:
      ```go
      type routerOptions struct {
          metricsRegisterer prometheus.Registerer
          currentDateTime   libtime.CurrentDateTimeGetter
      }
      ```
      Add the option (mirror `WithMetricsRegisterer`):
      ```go
      // WithCurrentDateTime overrides the clock used for time-window
      // eligibility and the router's timestamps. Defaults to
      // libtime.NewCurrentDateTime(). Tests pass a fixed clock
      // (libtime.NewCurrentDateTime() + SetNow) for deterministic window
      // checks (spec 014).
      func WithCurrentDateTime(clock libtime.CurrentDateTimeGetter) RouterOptionFunc {
          return func(o *routerOptions) {
              o.currentDateTime = clock
          }
      }
      ```
      In `CreateRouterFromConfig`, set the default next to `metricsRegisterer`: `o := &routerOptions{metricsRegisterer: prometheus.DefaultRegisterer, currentDateTime: libtime.NewCurrentDateTime()}`.
   b. In the per-upstream wiring loop, set the member's window + clock. The runtime `UpstreamMember.Window` field is the SAME `*pkg.Window` type as the config `Upstream.Window`, so this is a direct copy (nil stays nil = always eligible). In the `handler.UpstreamMember{...}` literal add:
      ```go
      Window: up.Window,
      Now:    o.currentDateTime.Now,
      ```
   c. Switch the model-router construction's clock from the hardcoded `libtime.NewCurrentDateTime()` to `o.currentDateTime` (the `handler.NewModelRouterWithPools(...)` call's last argument).
   d. Do NOT change the per-member `NewLoggingRoundTripper` clocks (they are unrelated to window eligibility) and do NOT change `main.go` / `pkg/cli.go` / `CreateServer` (no signature change).

6. **Handler tests — new `pkg/handler/time-window_test.go`** (package `handler_test`, sharing `labelHandler`, `captureStderr`, `alwaysSample`, `testMetrics`, `testDateTime` from `model-router_test.go`). Imports: `stdtime "time"`, `libtime "github.com/bborbe/time"`, `pkg "github.com/bborbe/claude-code-router/pkg"`, `handler "github.com/bborbe/claude-code-router/pkg/handler"`. Add package-local helpers:
   - `var berlin = mustLoadLocation("Europe/Berlin")` (stdlib `time.LoadLocation`).
   - `mustTOD(s string) libtime.TimeOfDay` — `v, err := libtime.ParseTimeOfDay(context.Background(), s); Expect(err).NotTo(HaveOccurred()); return *v`.
   - `fixedClock` — `libtime.NewCurrentDateTime()` (has `SetNow`); build a "now" with `libtime.DateTime(stdtime.Date(y, m, d, h, min, 0, 0, loc))`.
   - a `pinID(target int, weights ...int) string` helper computing the FNV-1a 64 slot (mirror the production `hash/fnv` computation) to find a session id pinned to `target` over the given weights (like `poolPinSlot` / `sessionPinnedTo` in `model-pool_test.go`).
   - `windowedMember(upstream string, from, until string, h http.Handler, clock libtime.CurrentDateTimeGetter) handler.UpstreamMember` building `handler.UpstreamMember{Upstream: upstream, Handler: h, Weight: 1, Window: &pkg.Window{From: mustTOD(from), Until: mustTOD(until)}, Now: clock.Now}`.
   Rows (each `It`):
   - **AC 3 — complementary windows through the real dispatch path (fixed clock):** build two distinct `&countingHandler{}` instances `dayCounter` / `nightCounter` (the existing `countingHandler` type from `upstream-pool-handler_test.go` counts invocations); members `day` = `windowedMember("https://day", "08:00 Europe/Berlin", "18:00 Europe/Berlin", handler.NewConcurrencyLimiter(dayCounter, 16, stdtime.Second), clock)` and `night` = `windowedMember("https://night", "18:00 Europe/Berlin", "08:00 Europe/Berlin", handler.NewConcurrencyLimiter(nightCounter, 50, stdtime.Second), clock)`. `pool := handler.NewUpstreamPoolHandler(context.Background(), []handler.UpstreamMember{day, night})`. With `clock.SetNow(10:00 Europe/Berlin)`, fire a BOUNDED batch of sessioned + keyless requests — 4–8 total, well below the day member's cap of 16, so no genuine 429 can occur (`handler.ContextWithSessionID` seam + plain requests) → ALL land on the day member (cap 16), the night member (cap 50) sees zero (`dayCounter.invocations() > 0`, `nightCounter.invocations() == 0`). Move `clock.SetNow(22:00 Europe/Berlin)` → the same bounded batch all lands on the night member, day sees zero. Assert via the counters. This row proves the business-hours request is served by the day member/key only and off-peak by the night member/key only, at each member's real cap.
   - **AC 3 — transition row (re-resolve on window close; in-flight completes):** day [08:00,18:00), night [18:00,08:00). `clock.SetNow(17:59 Berlin)`. Find a session id pinned to the day member over the full ring (`pinID(0, 1, 1)`). Give the day member a blocking handler (a handler-test-local `blockingHandler` that bumps an atomic in-flight counter on entry and blocks on a `release` channel until closed). Dispatch request 1 (that session id) in a goroutine (`go pool.ServeHTTP(rec1, req1)`); `Eventually` the day handler's in-flight is 1. `clock.SetNow(18:01 Berlin)`. Dispatch request 2 (same session id) synchronously → the day window is now closed, so the ring over eligible members selects the night member → request 2 served by night (`nightCounter.invocations() == 1`, day's in-flight still 1), response 200. Close `release` → request 1 completes 200. Asserts: next request re-resolves on window close, and the in-flight request started before the boundary completes.
   - **AC 4 — overnight wrap:** members `night` [22:00,06:00) Berlin + `open` (no window). `clock.SetNow(02:00 Berlin)` → a session id pinned to `night` over the full ring serves `night` (02:00 is inside). `clock.SetNow(14:00 Berlin)` → the same session id serves `open` (14:00 is outside, night ineligible). Both directions asserted through `ServeHTTP`.
   - **AC 5 — IANA boundary (evaluated in the attached location, not host local):** member `berlin-win` [17:00,18:00) Europe/Berlin + `open` (no window). `clock.SetNow(stdtime.Date(2026, 8, 19, 15, 30, 0, 0, stdtime.UTC))` — 15:30 UTC IS 17:30 Berlin, inside → the pinned session serves `berlin-win`. `clock.SetNow(stdtime.Date(2026, 8, 19, 17, 30, 0, 0, stdtime.UTC))` — 17:30 UTC is 19:30 Berlin, outside → the same session serves `open`. `clock.SetNow(stdtime.Date(2026, 8, 19, 16, 0, 0, 0, stdtime.UTC))` — 16:00 UTC is exactly 18:00 Berlin, and `[From, Until)` excludes `Until` → serves `open`.
   - **DB 3 — a closed member is skipped by pinning:** member A has a window that excludes `now` (e.g. [00:00,06:00) with `clock` at 12:00), member B has none. A session id that WOULD pin to A over the full [A,B] ring (`pinID(0, 1, 1)`) is served by B (A.invocations()==0, B.invocations()==1). Add a sibling row where A is closed with `InFlight: func() int { return 0 }` and B open with `InFlight: func() int { return 5 }` → a keyless request goes to B (least-loaded among eligible), proving closed members are excluded from least-loaded too.
   - **AC 7 — never 429 / no error (negative evidence):** within the complementary-window row at 10:00, capture stderr (`flag.Set("v","2")` + `captureStderr`) across the closed-window requests and assert every `rec.Code == 200`, the captured output does NOT contain `status=429`, and does NOT match `ERROR` / `error`. (The `[route]` lines name the day member only.)
   - **DB 1 — no window = always eligible:** a two-member pool with NO windows behaves byte-for-byte as today: a session id pinned to member 0 stays on member 0 across requests and a keyless request round-robins/least-loads exactly as before (assert pinning stability, mirroring the existing spec-012 rows).

7. **Router fall-through tests in `pkg/handler/time-window_test.go`** (spec AC 2, Failure Modes row "All of a provider's members closed"). Build REAL provider handlers (`handler.NewUpstreamPoolHandler`) with real windows + the fixed clock, not stubs:
   - **Next matching provider:** provider `day-pool` = single-member pool with window [08:00,18:00) Berlin, `night-pool` = single-member pool with NO window, both routes glob `deepseek-*` (declaration order day-pool first), `default` = a plain `labelHandler`. `clock.SetNow(22:00 Berlin)` (day closed). Build `handler.NewModelRouter(routes, "default", defaultHandler, nil, alwaysSample, testMetrics, testDateTime)`. Request `model: deepseek-x` → served by `night-pool` (assert via the pool's inner counting handler); captured stderr contains `[route] provider=day-pool window=closed -> night-pool`.
   - **Fall-through to default_provider:** one provider `day-pool` (window closed at now) globbing `deepseek-*`, default = `labelHandler("fallback")` (always eligible). Request `model: deepseek-x` → served by fallback; stderr contains `[route] provider=day-pool window=closed -> default` (the `defaultProviderName` you pass). Assert `rec.Code == 200` and stderr does NOT contain `status=429`.
   - **Model pool skips a member whose provider is closed:** `handler.NewModelPool` over two `ModelPoolMember`s whose `Handler`s are `&windowStub{eligible: false}` / `&windowStub{eligible: true}` (a tiny `struct` embedding `http.Handler` and implementing `HasEligibleMember() bool`), weights 1,1. A session id pinned to member 0 over the full ring resolves to member 1 (`Resolve`), and an idless request also resolves to member 1.

8. **Factory wiring test — new `pkg/factory/time_window_wiring_test.go`** (package `factory_test`, reusing the `poolUpstream` blocking-upstream harness, `poolSlot` / `sessionPinnedToSlot`, `isolatedRegistry`, `captureStderr` from the sibling wiring-test files). IMPORTANT: `newMessagesRequest`, `sessionedRequest`, `serveAsync`, and `probePinnedTo` are LOCAL closures inside the existing `upstream_pool_wiring_test.go` Describe block — NOT visible from this new file. Re-declare the small request/closure helpers locally in `time_window_wiring_test.go` (copy their implementations — each is a few lines) or add the window rows inside that existing Describe block instead of a new file; only `poolUpstream`/`newPoolUpstream`, `poolSlot`/`sessionPinnedToSlot`, `isolatedRegistry`, and `captureStderr` are package-level and reusable as-is. This file defines its own package-local `mustTOD(s string) libtime.TimeOfDay` helper (same implementation as the handler test file — `libtime.ParseTimeOfDay(context.Background(), s)`). MUST contain at least one literal lowercase `window` (the spec AC 7 evidence is `grep -c 'window' pkg/factory/*_test.go` ≥ 1) — the fall-through assertion below uses `window=closed`, which satisfies it.
   - **AC 7 — SIGHUP-rebuilt pool tree enforces the new window:** fixed clock `clock := libtime.NewCurrentDateTime(); clock.SetNow(<10:00 Europe/Berlin>)`. cfg1: provider `pool` with `Upstreams` [A = server `a` with `Window{From: mustTOD("08:00 Europe/Berlin"), Until: mustTOD("18:00 Europe/Berlin")}`, B = server `b` no window], `Models: ["m*"]`, `Router.DefaultProvider: "pool"`. `router1 := factory.CreateRouterFromConfig(context.Background(), cfg1, isolatedRegistry(), factory.WithCurrentDateTime(clock))`. Pick `id := sessionPinnedToSlot(0, 1, 1)` (pinned to A over the full ring). Fire `sessionedRequest(id, "m1")` through router1 → `Eventually` server A's in-flight is 1, B's is 0 (A is eligible at 10:00). Then cfg2 — a SECOND `CreateRouterFromConfig` exactly mirrors the reloader's SIGHUP rebuild — same provider but A's window changed to `{From: mustTOD("20:00 Europe/Berlin"), Until: mustTOD("22:00 Europe/Berlin")}` (closed at 10:00). `router2 := ...WithCurrentDateTime(clock)`. Fire the SAME session id through router2 → only B is eligible, so it serves B (A's in-flight stays 0, B's rises to 1). Name this `It(...)` so its description contains the word "rebuilt" (e.g. "rebuilds the pool tree — a rebuilt pool enforces the new window").
   - **Full-path fall-through + never 429 (contains the lowercase `window` literal):** provider `pool` = single member A with window [20:00,22:00) Berlin (closed at the fixed 10:00 now), `Models: ["m*"]`; `Router.DefaultProvider: "fallback"` where provider `fallback` = server `b` with `Models: ["*"]` (always eligible). With `flag.Set("v","2")` + `flag.Set("logtostderr","true")` + `captureStderr`, fire `newMessagesRequest("m1")` through `CreateRouterFromConfig(..., WithCurrentDateTime(clock))` → 200 served by fallback (server B), captured output contains `[route] provider=pool window=closed -> fallback` and does NOT contain `status=429` or `ERROR`. Unblock/close the upstreams in `AfterEach` as the harness does.

9. **`pkg.Window.Contains` unit tests in `pkg/config_test.go`** — extend the `Context("window")` block from prompt 1 with a `DescribeTable("Contains", ...)` driving the pure logic (these belong next to the type): (a) window [08:00,18:00) Berlin, `now` = 10:00 Berlin → true, `now` = 08:00 Berlin → true (inclusive `From`), `now` = 18:00 Berlin → false (exclusive `Until`), `now` = 07:59 Berlin → false; (b) overnight [22:00,06:00) Berlin, `now` = 02:00 Berlin → true, 14:00 Berlin → false, 22:00 Berlin → true; (c) IANA — window [17:00,18:00) Europe/Berlin, `now` = 15:30 UTC → true (17:30 Berlin), `now` = 17:30 UTC → false (19:30 Berlin); (d) From == Until (both `08:00 Europe/Berlin`) → false for every `now`. Build `now` as `libtime.DateTime(stdtime.Date(2026, 8, 19, h, m, 0, 0, loc))`. Use the same `mustTOD` helper style as the handler tests (or a local `libtime.ParseTimeOfDay` wrapper).
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- The window check uses the router's existing injected `libtime.CurrentDateTimeGetter` — do NOT introduce a new clock mechanism, a separate clock field on Config, or any other clock plumbing (spec Non-goals: "Changing the injected-clock mechanism"). The `WithCurrentDateTime` RouterOptionFunc is the same test-seam pattern as `WithMetricsRegisterer` — production default is `libtime.NewCurrentDateTime()`.
- A closed window is ELIGIBILITY, never a failure: no router error, no 429, no health check, no probe, no rotation (spec Non-goals). The only new log surface is the V(2) `[route] ... window=closed -> <fallback>` line (spec Constraints, AC 2 evidence).
- Pinning stays STATELESS: selection recomputes the eligible ring per request from the injected clock — no session→member map to invalidate, no runtime clock-driven re-evaluation of the window DEFINITION itself (only per-request eligibility of members, spec Non-goal "Dynamic window changes" / DB 5).
- Do NOT add Prometheus metrics, config knobs, opt-out flags, or tunable thresholds — the spec's observability surface is the `[route]`/`[req]` log lines (spec Non-goals; the 039-style metric-invention incident is a hard-reject precedent).
- Do NOT touch `pkg/config.go`'s parse/validation beyond adding `Contains` (requirement 1), `pkg/handler/session-id.go`, `pkg/handler/session-middleware.go`, `pkg/handler/concurrency-limiter.go`, `main.go`, `pkg/cli.go`, or `docs/`/`CHANGELOG.md` in this prompt. The exported `NewModelRouter` / `NewModelRouterWithPools` signatures stay byte-for-byte unchanged — do NOT touch existing test call sites. `NewUpstreamPoolHandler`'s signature is unchanged (fields added to `UpstreamMember`, not the constructor).
- EXECUTION-ORDER DEPENDENCIES: depends on spec-014 prompt 1 (`pkg.Window` + `Upstream.Window` / `Provider.Window` + normalization/validation) and on the shipped spec-012/013 machinery (`UpstreamMember`, `ModelPool`, `NewModelRouterWithPools`, the factory per-upstream wiring, `WithMetricsRegisterer`) being in the tree.
- Tests follow the repo's Ginkgo convention and must NOT depend on real wall-clock time or real waits — every window test drives a fixed `libtime.NewCurrentDateTime()` via `SetNow`, and asserts with small explicit waits (spec Constraints "fixed-clock tests must not depend on real wall-clock time; no real 30s waits"). The `handler` and `factory` suites set `time.Local = time.UTC`; the window evaluation must NOT rely on `time.Local` — it uses each value's attached location (spec AC 5).
- Overlapping windows (both members eligible) fall back to the existing spec-012 weighted-ring + least-loaded split — normal pool behavior, no new handling (spec Failure Modes row "Overlapping windows").
- Use `github.com/bborbe/errors` for wrapping in the factory; never `fmt.Errorf` directly. Preserve the `//nolint` comments on `CreateRouterFromConfig` and `newModelRouter`.
- No AI attribution in code or comments. `make precommit` must remain green — run it before declaring done. Follow `docs/dod.md` (GoDoc on every new exported identifier).
</constraints>

<verification>
make precommit

# Eligibility landed:
grep -n 'func (w \*Window) Contains\|func windowEligible\|HasEligibleMember' pkg/config.go pkg/handler/upstream-pool-handler.go pkg/handler/model-pool.go

# Fall-through line landed in the router:
grep -n 'window=closed' pkg/handler/model-router.go

# Factory option + wiring landed:
grep -n 'WithCurrentDateTime' pkg/factory/factory.go

# AC 3/4/5 evidence — handler rows:
grep -c 'window' pkg/handler/time-window_test.go            # expect >=1
grep -c 'SetNow\|15:30\|02:00' pkg/handler/time-window_test.go   # expect >=1 (fixed-clock rows)
grep -c 'window=closed' pkg/handler/time-window_test.go    # expect >=1 (AC 2 evidence)

# AC 7 evidence — factory row contains lowercase window:
grep -c 'window' pkg/factory/*_test.go                      # expect >=1
grep -c 'rebuilt' pkg/factory/time_window_wiring_test.go    # expect >=1

# Full suite:
go test -mod=mod -count=1 ./pkg/...
go test -mod=mod -count=1 ./pkg/handler/...
go test -mod=mod -count=1 ./pkg/factory/...
</verification>

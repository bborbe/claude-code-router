---
status: completed
spec: [013-model-pools]
execution_id: claude-code-router-session-pinning-exec-043-spec-013-resolution-wiring
dark-factory-version: dev
created: "2026-08-19T18:05:00Z"
queued: "2026-08-19T15:50:36Z"
started: "2026-08-19T16:20:36Z"
completed: "2026-08-19T16:38:49Z"
---

# Resolution + rewrite: session-pinned pool pre-step + factory wiring

<summary>
- The router now resolves a client-sent `model: <poolname>` as a pre-step BEFORE any provider-glob matching: it selects one pool member, rewrites the request body's `model` field to that member's fixed concrete model, and routes through that member's provider — the glob walk is never reached for pool names.
- The same session id selects the same pool member on every request (weighted ring hash, deterministic and stateless — no in-memory session→member map), so that member's prompt cache stays warm.
- A request with no session id is sent to the least-loaded pool member, with round-robin spread among equally-loaded members so an idless burst never stacks on the first-declared member.
- Heavier-weighted members receive proportionally more pinned sessions (a 2:1 weight ratio gives the heavier member roughly two thirds of sessions).
- A pinned member whose provider is saturated and whose config declares `overflow: true` fails the request over to the least-loaded sibling member, served and logged with the actual provider and concrete model; the default (`overflow: false`) keeps the request on its pinned member, where the provider's own concurrency semantics apply.
- A model name that is not a pool name falls through to today's alias + key + glob routing unchanged — aliases and pools do not interact.
- The factory wires the pool table from config (member → that provider's handler plus live load/saturation signals) and rebuilds it on SIGHUP reload, so adding, removing, or re-weighting a pool member is live without a restart.
- Full-path tests prove resolution, body rewrite, session pinning, weighted spread, least-loaded idless dispatch, overflow failover, and the SIGHUP-rebuilt table resolving a new member.
</summary>

<objective>
Add the `model_pools:` resolution layer — session-pinned weighted member selection, body rewrite to the member's concrete model, idless least-loaded fallback, and `overflow: true` failover — inside the model router's request flow, and wire the pool table (with real provider load/saturation signals) in the factory so a pool name resolves deterministically per session and SIGHUP reload rebuilds the table.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- Read `pkg/handler/model-router.go` — `NewModelRouter`'s request flow in order: `extractModel` → `model := origModel` → alias resolution (the `aliases[model]` block with `rewriteModelField`) → `[1m]` strip → `providerName := defaultProviderName` / `target := defaultHandler` / the `requiresLeadingSystem` default-provider seeding loop → key routing (`PresentedApiKeyFromContext` + `matchedByKey`) → glob walk → `liftSystemMessages` → dispatch + metrics + `[req]`. The pool pre-step slots in as a branch that guards alias/key/glob and reuses `rewriteModelField`. The V(2) `[route]` detail lines live here.
- Read `pkg/handler/session-id.go` and `pkg/handler/upstream-pool-handler.go` (spec-012 prompt 2 outputs — already in the tree) — `SessionIDFromContext(ctx) string`, the `UpstreamMember{Upstream, Handler, Weight, InFlight}` type, and the weighted ring-hash + least-loaded/round-robin selection they implement. The model-pool selection mirrors this mechanism exactly (`hash/fnv` FNV-1a 64a mod cumulative weight, stateless; the only mutable state is a round-robin tie-break counter).
- Read `pkg/config.go` — `Config.ModelPools map[string][]ModelPoolMember` and `ModelPoolMember{Provider, Model, Weight, Overflow}` (spec-013 prompt 1 output). Validation guarantees provider existence, positive weights, no duplicate pairs, non-empty pools.
- Read `pkg/factory/factory.go` — the spec-012 per-upstream wiring loop (per provider: `url.Parse(up.Upstream)`, token-swap transport, `NewAnthropicProxyHandler`, `NewConcurrencyLimiter(proxy, up.MaxConcurrentRequests, ...)`, the `memberHandler.(interface{ InFlight() int })` type assertion that binds `inFlight`, then `handler.NewUpstreamPoolHandler(members)` → `providerHandlers[name]`), and the `handler.NewModelRouter(...)` call site. This prompt extends that loop to also record each upstream's cap + in-flight func per provider for the pool closures, then builds the pool table and switches the router call to `NewModelRouterWithPools`.
- Read `pkg/handler/model-router_test.go` — the shared `labelHandler`, `captureStderr`, `alwaysSample`, `testMetrics`, `testDateTime` helpers (package `handler_test`), and `pkg/handler/model-router_key_routing_test.go` for the `flag.Set("v", "2")` + `captureStderr` log-assertion pattern and the context-seam injection shape (mirror `postWithKey`'s `req.WithContext(...)` for session ids).
- Read `pkg/factory/concurrency_limiter_wiring_test.go` and `pkg/factory/auth_middleware_wiring_test.go` — the full `factory_test` harness: `isolatedRegistry()`, `newMessagesRequest(model)`, `serveAsync`, blocking upstreams with atomic in-flight + `release chan struct{}`, `lowerCaseKeys`. The new pool wiring tests extend this harness with upstream servers that also RECORD the received request body (to assert the rewritten `model` field).
- Coding plugin docs (in-container paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md` — factory wiring conventions.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` — the selection runs in the request goroutine; goroutines allowed in `_test.go`.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `errors.Wrapf(ctx, err, ...)` (already the factory's style).
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, `Eventually` / `Consistently` with small explicit waits.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — glog verbosity levels (the new `[route]` detail line is V(2), matching the existing `[route]` lines).
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — GoDoc conventions.
</context>

<requirements>
1. **New `pkg/handler/model-pool.go`** (package `handler`) — the runtime pool types and the selection method. Exported types (comment text may be reworded; names and shapes are fixed):
   ```go
   // ModelPoolMember is one candidate of a model pool at runtime: the
   // provider to route through, the fixed concrete model string that
   // provider sees, the weight for session-pinned member selection, and
   // whether the member may overflow to a sibling when its provider is
   // saturated. Handler is the member provider's request handler (its
   // upstream pool handler). InFlight reports the provider's current
   // in-flight load; may be nil, meaning "always 0". Saturated reports
   // whether the provider is at capacity; may be nil, meaning "never
   // saturated". The factory fills all of these from config (spec 013).
   type ModelPoolMember struct {
       Provider  string
       Model     string
       Weight    int
       Overflow  bool
       Handler   http.Handler
       InFlight  func() int
       Saturated func() bool
   }
   ```
   ```go
   // NewModelPool returns a pool over members. Config validation (spec
   // 013 prompt 1) guarantees members is non-empty and every weight is
   // positive; like the upstream pool handler (spec 012), no defensive
   // validation is repeated here.
   func NewModelPool(members []ModelPoolMember) *ModelPool
   ```
   ```go
   // Resolve selects the member that serves one request for this pool.
   // A request carrying a non-empty session id (from the session
   // middleware's context) is pinned to the same member every time via a
   // weighted ring hash of the id — deterministic and stateless,
   // recomputable from the id, no session->member map — so the session's
   // prompt cache stays warm on that member. A request with an empty
   // session id goes to the least-loaded member, with round-robin
   // tie-breaking among equally-loaded members so an idless burst
   // spreads instead of stacking on the first member. When the pinned
   // member's provider is saturated (Saturated returns true) and the
   // member declares Overflow, the request falls over to the
   // least-loaded sibling member (declaration-order tie-break) so
   // availability wins over cache warmth; otherwise the pinned member is
   // served and its provider's own concurrency semantics apply (spec 013
   // DB 4/5).
   func (p *ModelPool) Resolve(sessionID string) ModelPoolMember
   ```
   Implementation contract (spec DB 2–4, Constraints):
   - The unexported struct holds `members []ModelPoolMember`, a precomputed `cumulative []int` (cumulative[i] = sum of members[0..i].Weight) and `totalWeight` (= last cumulative value), plus an `atomic.Uint64` round-robin counter. `hash/fnv` is the Go standard library — no new dependency.
   - **Pinned path (non-empty sessionID):** compute `h := fnv.New64a(); h.Write([]byte(sessionID)); slot := h.Sum64() % uint64(totalWeight)`, pick the first index i with `uint64(cumulative[i]) > slot`. This is the exact same mechanism the spec-012 upstream pool handler uses — same FNV-1a 64a, same ring of cumulative weights, so a fixed session id over a fixed pool is stable across requests and restarts (spec Constraints "stateless").
   - **Overflow:** after the pinned member `m` is chosen, if `m.Overflow && m.Saturated != nil && m.Saturated()` → pick the member with the minimum load (`InFlight()`, nil → 0) among all members EXCEPT `m`; ties broken by declaration order (stable, deterministic). If `m.Saturated == nil` (never saturated) or `m.Overflow == false`, serve `m` — the provider's own limiter handles saturation (spec DB 4).
   - **Idless path (empty sessionID):** compute each member's load as `InFlight()` (nil → 0); find the minimum; collect the indices at that minimum; pick `ties[(rr-1) % len(ties)]` where `rr = atomic.AddUint64(&p.rr, 1)`. Round-robin among equally-loaded members is what keeps an all-idle burst from stacking on the first-declared member (spec AC 4).
   - Do NOT log anything here and do NOT add Prometheus metrics — the `[route]` log line for pool resolution is emitted by the model router (requirement 2), matching the spec's observability surface.

2. **Model-router refactor + pool pre-step in `pkg/handler/model-router.go`.** Keep the exported `NewModelRouter` signature byte-for-byte unchanged — existing call sites (the ~50 handler-test rows and the 012 factory wiring) must keep compiling without edits:
   - Rename the body of `NewModelRouter` into an unexported `func newModelRouter(routes []ModelRoute, defaultProviderName string, defaultHandler http.Handler, aliases map[string]string, modelPools map[string]*ModelPool, sampler liblog.Sampler, metrics *Metrics, currentDateTime libtime.CurrentDateTimeGetter) http.Handler` (keep the `//nolint:gocognit,funlen,maintidx` comment on it).
   - `NewModelRouter(...)` (existing signature) becomes a one-line delegate passing `nil` for `modelPools`.
   - Add a new exported `func NewModelRouterWithPools(routes []ModelRoute, defaultProviderName string, defaultHandler http.Handler, aliases map[string]string, modelPools map[string]*ModelPool, sampler liblog.Sampler, metrics *Metrics, currentDateTime libtime.CurrentDateTimeGetter) http.Handler` delegating with the given `modelPools`. GoDoc on both exported constructors. `modelPools` may be nil or empty — both mean "no pool resolution".
   - Inside the handler body, restructure the top of the request flow as follows (anchor by the `providerName := defaultProviderName` / `target := defaultHandler` / `requiresLeadingSystem` seeding lines — hoist them ABOVE the alias block so the pool branch can set them):
     a. After `model := origModel` and `var aliasResolved string`, hoist the dispatch defaults: `providerName := defaultProviderName`, `target := defaultHandler`, and the `requiresLeadingSystem` default-provider seeding loop (exactly as they currently are, just moved earlier).
     b. **Pool pre-step** immediately after the hoisted defaults and before the alias `if`:
        ```go
        matchedByPool := false
        if modelPools != nil {
            if pool, ok := modelPools[model]; ok {
                member := pool.Resolve(SessionIDFromContext(r.Context()))
                rewritten, rerr := rewriteModelField(r.Context(), body, member.Model)
                if rerr != nil {
                    glog.Errorf("[pool] rewrite failed for %q -> %q: %v", model, member.Model, rerr)
                    http.Error(w, "pool rewrite failed", http.StatusInternalServerError)
                    // mirror the alias-rewrite failure path exactly (same metrics shape + return);
                    // if `latency` is not yet computed here, compute it as the alias path does:
                    // latency := currentDateTime.Now().Time().Sub(start).Round(time.Millisecond)
                    metrics.ObserveRequest(UnknownModelLabel, UnknownModelLabel, http.StatusInternalServerError, latency.Seconds(), true)
                    return
                }
                body = rewritten
                r.Body = io.NopCloser(bytes.NewReader(body))
                r.ContentLength = int64(len(body))
                model = member.Model
                providerName = member.Provider
                target = member.Handler
                for _, route := range routes {
                    if route.ProviderName == member.Provider {
                        requiresLeadingSystem = route.RequiresLeadingSystem
                        break
                    }
                }
                glog.V(2).Infof("[route] model=%s -> provider=%s model=%s", origModel, member.Provider, member.Model)
                matchedByPool = true
            }
        }
        ```
        The `[route]` line format is normative — the handler-test evidence greps it. The rewrite failure path mirrors the existing alias-rewrite failure exactly (same `metrics.ObserveRequest(..., http.StatusInternalServerError, ..., true)` + return shape).
     c. **Guard the alias block** with `if !matchedByPool { ... }` — a pool-resolved model is already concrete and must not be re-rewritten through aliases (spec Desired Behavior 5: pools and aliases do not interact).
     d. The `[1m]` strip block stays unguarded — it still applies to the concrete member model (uniform with today; a concrete model carrying a `[1m]` suffix is stripped). Pool names are matched VERBATIM and must not carry the `[1m]` suffix: a `model: <poolname>[1m]` request misses the pool lookup (the strip runs after pool lookup) and falls through to today's routing — document this in the operator docs so pool-name clients send the bare pool name.
     e. **Guard the key-routing block** with `if !matchedByPool { ... }` — the pool pre-step wins over key pinning (the model field explicitly names a pool; the pool branch runs first). Keep `matchedByKey` logic as-is inside.
     f. **Guard the glob walk** with `if !matchedByPool { ... }` — a pool-resolved model never reaches the glob matcher (spec Constraints).
     g. The `liftSystemMessages` block, the dispatch, `resolveModelLabel`, `metrics.ObserveRequest`, usage recording, and the `[req]` lines stay unchanged — pool requests flow through them uniformly (the `[req]` line shows `model=<poolname>` and `provider=<member provider>`; the metric model label resolves to the concrete member model, the string the upstream saw).
   - Do NOT change `rewriteModelField`, `extractModel`, or any other helper.

3. **Factory wiring in `pkg/factory/factory.go`.** Extend the spec-012 per-provider wiring and the router construction:
   - During the existing per-upstream loop, ALSO record per provider in two local maps keyed by provider name — `upCaps map[string][]int` (append `up.MaxConcurrentRequests` per member) and `upInFlight map[string][]func() int` (append the `interface{ InFlight() int }` assertion result — nil for uncapped members, exactly where the current code already does that assertion). Do not change what is already wired into `providerHandlers`.
   - After the provider loop and after `defaultHandler` is resolved, build the pool table from `cfg.ModelPools` before the `handler.NewModelRouter` call:
     - `modelPools := make(map[string]*handler.ModelPool, len(cfg.ModelPools))`.
     - For each pool name + config members (with the same per-iteration `ctx.Done()` check): for each `cfg.ModelPoolMember` `m`, look up `providerHandlers[m.Provider]`; if absent (defensive — `Config.Validate` already guarantees it), return `errors.New(ctx, fmt.Sprintf("model pool %q: provider %q has no handler", name, m.Provider))`. Build the runtime member:
       ```go
       handler.ModelPoolMember{
           Provider: m.Provider,
           Model:    m.Model,
           Weight:   m.Weight,
           Overflow: m.Overflow,
           Handler:  providerHandlers[m.Provider],
           InFlight: <sum of the provider's upInFlight, nil entries as 0>,
           Saturated: <see below>,
       }
       ```
     - `InFlight` closure = the provider's total in-flight: sum over `upInFlight[provider]` of `fn != nil ? fn() : 0`. This is the load the pool's least-loaded and overflow selection reads.
     - `Saturated` closure = the provider is at capacity: `len(caps) > 0 &&` for every `i`: `caps[i] > 0 && upInFlight[i] != nil && upInFlight[i]() >= caps[i]` (every capped upstream is at its cap; a single uncapped upstream makes it never saturated). This is the signal spec DB 4/5 read for overflow.
     - `modelPools[name] = handler.NewModelPool(members)`.
   - Switch the router construction to `handler.NewModelRouterWithPools(routes, cfg.Router.DefaultProvider, defaultHandler, cfg.Aliases, modelPools, liblog.DefaultSamplerFactory.Sampler(), metrics, libtime.NewCurrentDateTime())` (pass the built map; when no pools are configured it is empty, which is a no-op). The metrics/usage/logging tail is unchanged.

4. **Handler tests in new `pkg/handler/model-pool_test.go`** (package `handler_test`, sharing `labelHandler` / `captureStderr` / `alwaysSample` / `testMetrics` / `testDateTime` from `model-router_test.go`). Build the mux via `handler.NewModelRouterWithPools(routes, "default-fallback", fallback, nil, pools, alwaysSample, testMetrics, testDateTime)`; session ids are injected via `handler.ContextWithSessionID(req.Context(), id)` (the spec-012 context seam — the middleware is not run in these unit tests). Add a `recordingHandler` helper that reads `r.Body` into a buffer, records it, and writes its label to the response (so a row can assert BOTH which member served AND the exact rewritten body). Pool members are `handler.ModelPoolMember{Provider: "deepseek-pool", Model: "deepseek-v4-flash", Weight: n, Overflow: o, Handler: <recording labelHandler>, InFlight: <fake>, Saturated: <fake>}`. Rows:
   - **AC 2 — resolution + rewrite:** pool `coding` → [deepseek-pool/deepseek-v4-flash/1, minimax-pool/MiniMax-2.7/1]. Find two session ids that the pool maps to DIFFERENT members (compute the FNV-1a 64a slot `fnv.New64a()` over candidate ids mod totalWeight — deterministic — or probe by serving; do not hard-code). For each session: the request is served by THAT member's handler AND the recorded body's top-level `model` equals that member's concrete model (`{"model":"deepseek-v4-flash"}` or `{"model":"MiniMax-2.7"}`). With `flag.Set("v", "2")` + `captureStderr`, the log contains `[route] model=coding -> provider=deepseek-pool model=deepseek-v4-flash` for one session and the minimax equivalent for the other (assert exact substrings).
   - **AC 3 — session-pinned member:** same 1:1 pool, one session id served N=5 times → the SAME member handles every one. Build a SECOND `NewModelRouterWithPools` over identical pool members → the same session id picks the same member there too (proves stateless, recomputable from the id, no in-memory map).
   - **AC 5 — weighted distribution (spec evidence):** pool `coding` → [A/weight 2, B/weight 1] (deepseek-pool/deepseek-v4-flash + minimax-pool/MiniMax-2.7), members labelled so the heavier one is distinguishable. Serve N=100 distinct session ids (`"session-0"` .. `"session-99"`, injected via context) and count per member. Assert the heavier member served ≥55 of the 100 (spec AC 5 threshold; verified: FNV-1a 64a over `"session-0".."session-99"` mod 3 gives 66/34, same mechanism as the sibling spec).
   - **AC 4 — idless least-loaded, never the first-declared:** two members, `InFlight` returning 1 for member A and 0 for member B → an idless request (no session id) is served by B. One row with a member built `InFlight: nil` (production default for uncapped) asserting it is treated as load 0 and never panics. Then with both `InFlight` returning 0, fire N=8 idless requests and assert via `captureStderr` + `flag.Set("v", "2")` that the `[route] model=coding -> provider=...` lines show ≥2 distinct provider values (round-robin spread, never first-declared stacking — spec AC 4 evidence).
   - **AC 6 — overflow on:** session pinned to member A (pick via the FNV slot); A has `Saturated: func() bool { return true }` and `Overflow: true`, B idle → the request is served by B and the captured log line names B's provider+model (`[route] model=coding -> provider=minimax-pool model=MiniMax-2.7`).
   - **DB 4 — overflow off (default):** session pinned to A with `Saturated: func() bool { return true }` but `Overflow: false` → the request is served by A (no failover; the 429 semantics belong to A's own provider limiter, covered by spec-012 tests).
   - **DB 1 — pool name not configured falls through:** a pool table WITHOUT `coding` (or a request for `model: claude-opus-4-7` against a routes list containing `claude-*`) → glob routing happens exactly as today: served by the glob-matched handler, `[route] model=... matched ...` line, no pool `[route]` line.

5. **Factory wiring tests in new `pkg/factory/model_pool_wiring_test.go`** (package `factory_test`, same harness as `concurrency_limiter_wiring_test.go`: `isolatedRegistry()`, `newMessagesRequest(model)`, `serveAsync`, blocking upstreams with atomic in-flight + `release chan struct{}`). Extend the upstream servers to ALSO record the received request body (read `r.Body` into a buffer under a mutex) so rows can assert the rewritten `model` field at the real dispatch boundary. Session ids are injected via `handler.ContextWithSessionID(req.Context(), id)` (import `"github.com/bborbe/claude-code-router/pkg/handler"`). Configs are programmatic `*pkg.Config` values: `pkg.Provider{Upstreams: []pkg.Upstream{{Upstream: <serverURL>, Weight: 1, MaxConcurrentRequests: n, MaxConcurrentWaitSeconds: n}}, Models: []string{"*"}}` and `ModelPools: map[string][]pkg.ModelPoolMember{...}` (the YAML→config boundary is covered in spec-013 prompt 1). To find a session id that pins to a given pool member, compute the weighted-ring slot the same way the code does (`fnv.New64a()` over the id mod totalWeight) rather than probing — deterministic. Rows:
   - **AC 7 — the SIGHUP-rebuilt table resolves the new member (spec evidence):** build router1 from a config whose `ModelPools` `coding` → [deepseek-pool/deepseek-v4-flash] (provider deepseek-pool's upstream = serverA). Then build router2 from a SECOND config, same providers, with `coding` → [deepseek-pool/deepseek-v4-flash, minimax-pool/MiniMax-2.7] (minimax-pool's upstream = serverB) — a fresh `CreateRouterFromConfig` exactly mirrors the reloader's rebuild (`CreateServer`'s SIGHUP callback). Pick a session id that pins to the NEW member (minimax-pool) in router2 (slot 1 of totalWeight 2). Fire `newMessagesRequest("coding")` with that id through router2 → serverB receives it and its recorded body's `model` == `MiniMax-2.7`. Name this row's `It(...)` so its description contains the word "rebuilt" (e.g. "rebuilds the model pool table ... a rebuilt pool resolves the new member") — spec AC 7 evidence greps for it. Also ensure this file contains at least one literal `model_pools` string (e.g. a comment noting the YAML key `model_pools:` is parsed in prompt 1) so the spec's `grep -c 'model_pools' pkg/factory/*_test.go` evidence check fires — the filename `model_pool_wiring_test.go` and the Go field `ModelPools:` do NOT contain that literal.
   - **Full-path resolution + rewrite:** single-pool config `coding` → [deepseek-pool/deepseek-v4-flash], provider deepseek-pool upstream = serverA. Fire `newMessagesRequest("coding")` (keyless) through `CreateRouterFromConfig` → serverA receives it, recorded body `model` == `deepseek-v4-flash`, and captured stderr (`flag.Set("v", "2")` + the `captureStderr` helper from `trace_wiring_test.go`) contains `[route] model=coding -> provider=deepseek-pool model=deepseek-v4-flash`. A second request `newMessagesRequest("deepseek-v4-flash")` (concrete, non-pool) routes normally through the same provider — the pool pre-step is a no-op for non-pool names.
   - **AC 6 overflow through the real dispatch path (the Saturated/InFlight closure boundary):** two providers A (serverA) and B (serverB), each one upstream member, each `MaxConcurrentRequests: 1, MaxConcurrentWaitSeconds: 1`; `ModelPools` `coding` → [A/deepseek-v4-flash/overflow:true, B/MiniMax-2.7]. Pick a session id S that pins to A at the pool level (FNV slot 0). Fire request 1 (session S, `model: coding`) via `serveAsync` → `Eventually` serverA's in-flight is 1 (it blocks on `release`). Now A's provider `Saturated` closure reads serverA in-flight 1 ≥ cap 1. Fire request 2 (session S, `model: coding`) synchronously → the pool resolves A, sees A saturated + overflow, picks B → serverB receives request 2 and its recorded body `model` == `MiniMax-2.7`; assert `rec2.Code == 200` and the captured `[route]` line names `provider=minimax-pool model=MiniMax-2.7`; assert serverA's in-flight stayed 1 (request 2 never touched A). Close `release`; `Eventually` request 1 completes 200.
   - Keep every existing `factory_test` row passing unchanged — the legacy single-`Upstream:` configs and the 012 pool rows must be unaffected by the `NewModelRouterWithPools` switch (nil/empty pools = no-op).
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- `model_pools:` is resolved as a pre-step BEFORE the glob walk — a `model` that matches a pool name never reaches the glob matcher. Rewrite reuses the existing `rewriteModelField` machinery (spec Constraints). Pool resolution wins over key routing and over aliases (the pool branch runs first; the concrete member model is not re-aliased) — "pool names do not interact with aliases" (spec Desired Behavior 5).
- A pool member's `provider` must name an existing provider (validated in prompt 1); a member's `model` is the concrete string sent to that provider and may itself match the provider's globs — that is the normal case (spec Constraints).
- Pinning is STATELESS: weighted ring hash keyed on session id only — the same deterministic mechanism as the sibling session-pinning-pools spec. NO in-memory session→member map, no TTL, no memory growth. The only mutable state in `ModelPool` is the round-robin tie-break counter for idless dispatch (spec Constraints).
- `x-session-id` is used only as a selection key, never for auth, and is never written to the request body or a response (spec Constraints, Security). The only new log surface is the `[route] model=<pool> -> provider=<p> model=<concrete>` V(2) detail line.
- 429/failover semantics come from the underlying provider's concurrency config (session-pinning-pools spec) — this spec adds the resolution layer only. `overflow: false` (the default) means the request stays on its pinned member and its provider's own limiter answers (spec Constraints, DB 4). No health checks / circuit breakers / probe-and-rotate (spec Non-goals) — a dead member fails its requests.
- Do NOT add Prometheus metrics, config knobs, opt-out flags, or tunable thresholds — the spec's observability surface for this change is the `[route]` log line and the existing `[req]` line (spec Non-goals).
- Do NOT change `pkg/handler/session-id.go`, `pkg/handler/session-middleware.go`, `pkg/handler/upstream-pool-handler.go`, `pkg/handler/concurrency-limiter.go`, `pkg/config.go`, `main.go`, or `pkg/cli.go` in this prompt. The exported `NewModelRouter` signature stays byte-for-byte unchanged — do NOT touch existing test call sites.
- EXECUTION-ORDER DEPENDENCY: this prompt depends on the sibling spec-012 prompts executing first — `SessionIDFromContext` / `ContextWithSessionID` (012 prompt 2), `NewUpstreamPoolHandler` + the per-upstream `InFlight()` seam (012 prompts 2–3), and the factory's per-upstream pool wiring (012 prompt 3) must already be in the tree before this prompt runs. Also depends on spec-013 prompt 1 (`Config.ModelPools` / `ModelPoolMember` + validation) shipping first.
- Use `github.com/bborbe/errors` for error wrapping; never `fmt.Errorf` directly.
- Tests follow the repo's Ginkgo convention and must not depend on real 30s waits — use small explicit waits (spec Constraints). No new dependencies — `hash/fnv` is the Go standard library.
- No AI attribution in code or comments.
- `make precommit` must remain green — run it before declaring done.
- Follow `docs/dod.md` (GoDoc on every new exported identifier).
- Do NOT touch `docs/` or `CHANGELOG.md` in this prompt — spec-013 prompt 3 owns documentation.
</constraints>

<verification>
make precommit

# Resolution landed:
grep -n 'func NewModelPool\|func (p \*ModelPool) Resolve\|type ModelPoolMember struct' pkg/handler/model-pool.go
grep -n 'func NewModelRouterWithPools\|\[route\] model=%s -> provider=%s model=%s' pkg/handler/model-router.go

# Handler tests cover resolution + rewrite + pinning + least-loaded + overflow:
grep -c '\[route\] model=coding' pkg/handler/model-pool_test.go    # expect >=1 (spec AC 2 evidence)
grep -c 'ContextWithSessionID' pkg/handler/model-pool_test.go     # expect >=1

# Factory wires the pool table + SIGHUP-rebuilt row:
grep -n 'NewModelRouterWithPools' pkg/factory/factory.go
grep -c 'model_pools' pkg/factory/*_test.go                      # expect >=1 (spec AC 7 evidence)
grep -c 'rebuilt' pkg/factory/model_pool_wiring_test.go          # expect >=1 (spec AC 7)

# Full suite:
go test -mod=mod -count=1 ./pkg/handler/...
go test -mod=mod -count=1 ./pkg/factory/...
go test -mod=mod -count=1 ./pkg/...
</verification>

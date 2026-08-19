---
status: completed
spec: [012-session-pinning-pools]
summary: Generalized pkg/handler/concurrency-limiter.go with InFlight(), wired per-upstream pools into the factory (per-member proxy/token/limiter + least-loaded via real semaphore counts), mounted the session middleware on /v1/*, and added full-path pool wiring tests including the SIGHUP-rebuilt pool and outbound header-strip boundary
execution_id: claude-code-router-session-pinning-exec-040-spec-012-per-upstream-concurrency-wiring
dark-factory-version: dev
created: "2026-08-19T17:12:00Z"
queued: "2026-08-19T15:50:36Z"
started: "2026-08-19T16:01:02Z"
completed: "2026-08-19T16:12:49Z"
---

# Per-upstream concurrency + factory pool wiring + SIGHUP-rebuilt pool

<summary>
- The router's route tree now builds a real upstream pool per provider: each member gets its own proxy, token swap, and independent concurrency limiter, wrapped in the pool-selection handler.
- The `maxConcurrentRequests` cap moves down from the provider to each pool member, so two servers that each allow 8 do not share one global cap of 8 — each member enforces its own limit and answers 429 with an Anthropic-shaped `rate_limit_error` body when a queued request times out.
- Least-loaded keyless dispatch now reads the real per-member semaphore occupancy, so the router spreads anonymous load by actual in-flight counts, not configuration order.
- The `x-session-id` middleware is mounted on the `/v1/*` path, so session pinning and the outbound header strip are live end-to-end.
- Legacy single-`upstream:` providers (including every programmatically-built config in existing tests) wire as a one-member pool and behave byte-for-byte as before.
- A second router build from a changed `upstreams:` list / weight / cap rebuilds the pool tree — the SIGHUP reload path — and the rebuilt tree enforces the new pool.
- Full-path tests prove: a capped member holds at most its cap with the next request 429'd, keyless traffic lands on the least-loaded member, sessions stay pinned to one server, and the rebuilt pool enforces new caps.
</summary>

<objective>
Generalize the per-upstream concurrency limiter and wire the full pool into the factory — per-member proxy + limiter, least-loaded via real semaphore counts, mounted session middleware — so a provider's pool enforces per-server caps and serves pinned sessions and keyless load correctly, with SIGHUP reload rebuilding the pool tree.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- Read `pkg/handler/concurrency-limiter.go` — `NewConcurrencyLimiter(next http.Handler, maxConcurrentRequests int, maxConcurrentWait time.Duration) http.Handler` and the private `concurrencyLimiter` struct (fields `next`, `sem chan struct{}`, `wait time.Duration`; `ServeHTTP` acquires `l.sem <- struct{}{}` and releases via `defer func() { <-l.sem }()`). The new `InFlight()` method reads `len(l.sem)`. The no-op contract — `maxConcurrentRequests <= 0` returns `next` unchanged — must NOT change.
- Read `pkg/handler/upstream-pool-handler.go` and `pkg/handler/session-middleware.go` (prompt 2) — the `UpstreamMember{Upstream, Handler, Weight, InFlight}` type and `NewUpstreamPoolHandler` / `NewSessionMiddleware`, which this prompt wires.
- Read `pkg/factory/factory.go` — `CreateRouterFromConfig`'s per-provider loop (currently: `url.Parse(prov.Upstream)`, `handler.NewLoggingRoundTripper(handler.NewAuthSwapTransport(handler.DefaultProxyTransport(), prov.Token), liblog.SamplerList{liblog.NewSampleTime(time.Second), liblog.NewSamplerGlogLevel(5)}, libtime.NewCurrentDateTime())`, `handler.NewAnthropicProxyHandler(upstream, transport)`, the `defaultMaxConcurrentWaitSeconds = 30` const, `handler.NewConcurrencyLimiter(proxy, prov.MaxConcurrentRequests, ...)`, then `providerHandlers[name] = providerHandler` and `Handler: providerHandler` on each `handler.ModelRoute`). Also read `buildMux`'s `/v1/` wrapping (`v1Handler := http.Handler(modelRouter)` then `handler.NewAuthMiddleware` then trace) — the session middleware mounts here.
- Read `pkg/config.go` — `Provider.UpstreamList()` (prompt 1), the single-source synthesis the factory consumes.
- Read `pkg/factory/concurrency_limiter_wiring_test.go` — the full `factory_test` harness to copy: `httptest.NewServer` blocking upstream with an atomic in-flight counter and `release chan struct{}`, `serveAsync`, `newMessagesRequest(model)`, `makeConfig(pkg.Provider)`, `isolatedRegistry()` (defined in `pkg/factory/auth_middleware_wiring_test.go`). The new pool wiring tests extend this harness to N upstream servers.
- Read `pkg/handler/concurrency-limiter_test.go` — the `blockingHandler` shape for the added `InFlight()` test.
- Coding plugin docs (in-container paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md` — factory wiring conventions.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` — the limiter runs in the request goroutine; goroutines allowed in `_test.go`.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `errors.Wrapf(ctx, err, ...)` (already the factory's style).
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, `Eventually` / `Consistently` with small explicit waits.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — GoDoc conventions.
</context>

<requirements>
1. **Generalize `pkg/handler/concurrency-limiter.go` — add the in-flight counter.** Add a method on the private `concurrencyLimiter`:
   ```go
   // InFlight returns the number of slots currently held — the number of
   // requests this limiter is currently serving. It is the per-upstream
   // in-flight count the pool handler's least-loaded selection reads
   // (spec 012 DB 4). Only valid on a real limiter (constructed with
   // maxConcurrentRequests > 0); the <= 0 no-op path returns next
   // unchanged and has no limiter, and the pool handler treats a missing
   // counter as 0.
   func (l *concurrencyLimiter) InFlight() int
   ```
   Implementation is `return len(l.sem)`. Do NOT change `NewConcurrencyLimiter`'s signature or its `<= 0 → return next` no-op contract — the byte-for-byte passthrough for uncapped providers is load-bearing (spec 011 + spec 012 AC 5). Update the `NewConcurrencyLimiter` GoDoc only to mention that `InFlight()` exposes the semaphore occupancy for least-loaded selection.

2. **Factory pool wiring in `pkg/factory/factory.go`.** Replace the per-provider loop's single-`prov.Upstream` wiring with a per-member loop over `prov.UpstreamList()`. For each provider, for each upstream in `prov.UpstreamList()` (with the same per-iteration `ctx.Done()` checks the current loop uses):
   - `upstream, err := url.Parse(up.Upstream)`; on error return `errors.Wrapf(ctx, err, "provider %q: parse upstream %q", name, up.Upstream)`.
   - `transport := handler.NewLoggingRoundTripper(handler.NewAuthSwapTransport(handler.DefaultProxyTransport(), up.Token), liblog.SamplerList{liblog.NewSampleTime(time.Second), liblog.NewSamplerGlogLevel(5)}, libtime.NewCurrentDateTime())` — the member's own token swap (per-member `Token`).
   - `proxy := handler.NewAnthropicProxyHandler(upstream, transport)`.
   - Resolve the wait default per member: `waitSeconds := up.MaxConcurrentWaitSeconds; if waitSeconds <= 0 { waitSeconds = defaultMaxConcurrentWaitSeconds }`.
   - `memberHandler := handler.NewConcurrencyLimiter(proxy, up.MaxConcurrentRequests, time.Duration(waitSeconds)*time.Second)`.
   - Bind the least-loaded counter only when a real limiter exists:
     ```go
     var inFlight func() int
     if limiter, ok := memberHandler.(interface{ InFlight() int }); ok {
         inFlight = limiter.InFlight
     }
     ```
     (capped member → `*concurrencyLimiter.InFlight`; uncapped → the proxy, no counter, so `inFlight` stays nil and the pool handler reads it as 0.)
   - Append `handler.UpstreamMember{Upstream: up.Upstream, Handler: memberHandler, Weight: up.Weight, InFlight: inFlight}`.
   - After the member loop: `providerHandler := handler.NewUpstreamPoolHandler(members)`; `providerHandlers[name] = providerHandler`; and each `handler.ModelRoute` in the `prov.Models` loop carries `Handler: providerHandler` (exactly where the current code uses the wrapped `providerHandler`). `defaultHandler` is read from `providerHandlers[cfg.Router.DefaultProvider]`, so the default-provider path goes through the pool by construction — do not add a separate wrap.
   - The legacy `Provider{Upstream: ...}` shape (programmatic configs bypassing `Load`) flows through `UpstreamList()` as a one-member pool, so every existing wiring test that builds `pkg.Provider{Upstream: srv.URL, ...}` keeps compiling and behaving identically — do not change those tests.

3. **Mount the session middleware in `buildMux` (`pkg/factory/factory.go`).** In the `/v1/` wrapping, change:
   ```go
   v1Handler := http.Handler(modelRouter)
   v1Handler = handler.NewAuthMiddleware(v1Handler, allowedKeys)
   ```
   to:
   ```go
   v1Handler := http.Handler(handler.NewSessionMiddleware(modelRouter))
   v1Handler = handler.NewAuthMiddleware(v1Handler, allowedKeys)
   ```
   The session middleware wraps the model router directly (deepest — under auth and trace, i.e. trace → auth → session → modelRouter) so `x-session-id` is read and stripped before dispatch and never reaches an upstream; auth behavior is unchanged. Trace still sees the original header — `x-session-id` is not a credential and is NOT redacted (spec Security only requires auth header redaction).

4. **`InFlight()` unit test in `pkg/handler/concurrency-limiter_test.go`** (package `handler_test`, reuse `blockingHandler` / `serveAsync` / `newMessagesRequest`): cap 1, wait 1s. Fire request 1 in a goroutine → `Eventually` entry count is 1, and the limiter's `InFlight()` is 1 (via `limiter.(interface{ InFlight() int }).InFlight()`). Close `release` → `Eventually` request 1 completes and `InFlight()` is 0. This is the counter the pool's least-loaded selection reads — it must track actual slot occupancy.

5. **Wiring tests in new `pkg/factory/upstream_pool_wiring_test.go`** (package `factory_test`, same harness as `concurrency_limiter_wiring_test.go`: `isolatedRegistry()`, `newMessagesRequest(model)`, `serveAsync`, blocking upstreams with atomic in-flight + `release chan struct{}` + context-cancellation teardown). Extend the harness with a helper that creates TWO independent blocking upstream servers (each its own URL, in-flight counter, received-headers recorder as in `auth_middleware_wiring_test.go`'s `receivedHdrs`, and `release`), so a pool can point member A at server A and member B at server B, and the header-strip boundary row can assert per-server recorded headers. Session ids are injected into request context via `handler.ContextWithSessionID(req.Context(), id)` (the middleware is not run in these wiring tests — its own behavior is covered in prompt 2). Configs are programmatic `*pkg.Config` values with `pkg.Provider{Upstreams: []pkg.Upstream{...}}` (each `pkg.Upstream{Upstream: <serverURL>, Weight: n, MaxConcurrentRequests: n, MaxConcurrentWaitSeconds: n}`); the YAML→config boundary is covered in prompt 1. Rows:
   - **AC 5 — a member holds at most its cap; the next pinned request is 429'd through the real dispatch path:** pool of two members, both `MaxConcurrentRequests: 1`, `MaxConcurrentWaitSeconds: 1`, weights 1:1. Pick a session id that the pool pins to member A (probe by firing one sessioned request and observing which server's in-flight rose — the test then uses that id for the rest of the row). Fire request 1 with that id in a goroutine → `Eventually` A's in-flight is 1. Fire request 2 with the SAME id synchronously → assert `rec.Code == 429`, body contains `rate_limit_error`, and BOTH servers' in-flight stay as-is (B never saw it — the cap is per-member, and B is idle, so this proves the cap pinned to A, not a shared pool cap). Close A's release; assert request 1 completes 200.
   - **AC 3 full-path — keyless dispatch goes to the least-loaded member, never the first-declared:** same two-member pool. Saturate member A (sessioned request pinned to A, blocking). Fire a KEYLESS request synchronously → it must be served by B (least-loaded): assert B's in-flight became 1 while A's stayed 1, and the captured `[route]` log (`flag.Set("v", "2")` + stderr capture, as in `pkg/factory/trace_wiring_test.go`'s `captureStderr`) shows `[route] session= upstream=<B.URL>`. Close both releases; assert all requests complete.
   - **AC 2 full-path — a session id stays on one member across requests:** pool of two members 1:1. Fire three requests with session id S concurrently via `serveAsync` (they block on the member's `release`, so the per-server counter reaches 3) → all three hit the same server (per-server counters show 3 on one, 0 on the other). Fire three with session id T (chosen so it pins to the OTHER member — probe candidates as above) → all three hit the other server. Close releases; assert all complete 200.
   - **AC 5 — the legacy single-upstream form wires as a one-member pool:** config with `pkg.Provider{Upstream: serverA.URL, MaxConcurrentRequests: 1}` (NO `Upstreams` — the programmatic legacy shape every existing wiring test uses). A sessioned and a keyless request both reach serverA. This row guards the backward-compat path the sibling wiring tests depend on.
   - **AC 6 — reload row (the rebuilt pool enforces the new cap):** build handler1 from a config whose provider has ONE `Upstreams` member (server A, `MaxConcurrentRequests: 1`, `MaxConcurrentWaitSeconds: 5`). Pick a session id that pins to member A of the one-member pool. Fire request A (glob-matched) pinned to A → in-flight 1. Fire request B pinned to A → `Consistently` (~300ms) in-flight stays 1 (capped). Build handler2 from a SECOND config, same provider key, with `Upstreams` of TWO members (server A and server B, each `MaxConcurrentRequests: 2`, `MaxConcurrentWaitSeconds: 5`) — a fresh `CreateRouterFromConfig` exactly mirrors the reloader's rebuild (`CreateServer`'s SIGHUP callback). Fire requests C and D against handler2 pinned to A — probe candidate session ids against the TWO-member pool and pick ones that pin to member A (an id that pinned to A in the one-member pool need NOT pin to A in the rebuilt two-member pool — the ring changed) → `Eventually` A's in-flight reaches 3 (A + C + D) — the rebuilt tree's member now enforces the new cap of 2. Name this row's `It(...)` description so it contains the word "rebuilt" (e.g. "rebuilds the pool tree ... a rebuilt pool enforces the new cap") — spec AC 6 evidence greps for it. Close releases; assert all four requests complete 200.
   - **Boundary — the outbound header strip through the real path:** through the full mux (`router.ServeHTTP`), a request carrying an `X-Session-Id` header is served by the pinned member AND the receiving `httptest` server observes NO `X-Session-Id` header on the request it got (the middleware stripped it before the proxy forwarded — the header strip boundary, spec DB 2). Assert per-server recorded headers.
   - Keep the existing `concurrency_limiter_wiring_test.go`, `system_lift_wiring_test.go`, `auth_middleware_wiring_test.go`, `provider_order_wiring_test.go`, `trace_wiring_test.go`, `admin_loopback_guard_wiring_test.go`, `hello_wiring_test.go` rows passing unchanged — they build legacy `Provider{Upstream: ...}` configs and must keep working through the one-member pool.
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- The per-upstream semaphore is a generalized `pkg/handler/concurrency-limiter.go` (spec 011): one instance per pool member instead of one per provider. Existing provider-level cap behavior on single-upstream providers is unchanged (spec Constraints).
- Per-upstream cap semantics are spec 011: `maxConcurrentRequests` absent/0/negative = unlimited; `maxConcurrentWaitSeconds` default 30 when absent/0/negative (resolved by the factory). A request that times out gets HTTP 429 with the Anthropic-shaped `rate_limit_error` body — never a 5xx (spec 011 constraint, spec 012 DB 5).
- The 429 body stays generic: no queue depth, no upstream URL, no provider name (spec Security). The existing `limiter429Body` constant in `pkg/handler/concurrency-limiter.go` is unchanged and reused.
- Session pinning stays stateless (weighted ring hash, prompt 2); the factory must NOT add a session→member map. Least-loaded reads the per-member semaphore's `InFlight()` (spec DB 4).
- No pool-level overflow failover: a saturated pinned member is 429'd by its own limiter, never routed to a sibling (spec Non-goals).
- No health checks / circuit breakers / probe-and-rotate (spec Non-goals) — a dead upstream fails the request with the existing sanitized 502 behavior.
- No new Prometheus metrics — router-issued 429s already classify via the model router's `[req] ... status=429` line and `4xx_rate_limited` (spec 011 Non-goals). No new log lines beyond the pool handler's existing `[route]` detail line.
- Do NOT change `pkg/handler/model-router.go`, `pkg/handler/auth-middleware.go`, `pkg/handler/session-middleware.go`, `pkg/handler/upstream-pool-handler.go`, `pkg/config.go`, `main.go`, or `pkg/cli.go` in this prompt.
- Tests must not depend on real wall-clock 30s waits — use small explicit waits (spec Constraints). No new dependencies.
- Use `github.com/bborbe/errors` for error wrapping; never `fmt.Errorf`.
- No AI attribution in code or comments.
- `make precommit` must remain green — run it before declaring done.
- Follow `docs/dod.md` (GoDoc on every new exported identifier).
- Do NOT touch `docs/` or `CHANGELOG.md` in this prompt — prompt 4 owns documentation.
</constraints>

<verification>
make precommit

# Limiter generalization landed:
grep -n 'func (l \*concurrencyLimiter) InFlight' pkg/handler/concurrency-limiter.go

# Factory wires the pool per provider + mounts the session middleware:
grep -n 'NewUpstreamPoolHandler\|NewSessionMiddleware' pkg/factory/factory.go

# AC 5/6 evidence:
grep -c 'rate_limit_error' pkg/factory/upstream_pool_wiring_test.go   # expect >=1
grep -c 'rebuilt' pkg/factory/*_test.go                               # expect >=1 (spec AC 6)

# Session pinning wired into the route tree:
grep -c 'NewSessionMiddleware' pkg/factory/factory.go                 # expect >=1

# Full suite:
go test -mod=mod -count=1 ./pkg/handler/...
go test -mod=mod -count=1 ./pkg/factory/...
go test -mod=mod -count=1 ./pkg/...
</verification>

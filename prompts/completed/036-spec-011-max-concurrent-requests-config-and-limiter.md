---
status: completed
spec: [011-max-concurrent-requests]
summary: 'Added per-provider concurrency limiting: maxConcurrentRequests/maxConcurrentWaitSeconds config fields, in-router buffered-channel semaphore handler answering Anthropic-shaped 429s on queue timeout, factory wiring with 30s default wait, plus yaml-boundary config tests, handler tests, and full-path/reload factory wiring tests'
execution_id: claude-code-router-max-concurrent-exec-036-spec-011-max-concurrent-requests-config-and-limiter
dark-factory-version: dev
created: "2026-08-18T20:05:00Z"
queued: "2026-08-18T20:18:51Z"
started: "2026-08-18T20:18:52Z"
completed: "2026-08-18T20:27:18Z"
---

# Per-provider concurrency limiting: config surface, limiter handler, factory wiring

<summary>
- Operators can cap how many `/v1/*` requests a single provider forwards upstream at the same time, via an optional `maxConcurrentRequests` provider field; absent or 0 keeps today's unlimited behavior byte-for-byte.
- A second optional field, `maxConcurrentWaitSeconds`, controls how long an excess request queues for a free slot; a capped provider that omits it (or sets it to 0) waits up to 30 seconds by default.
- Negative values are lenient: a negative `maxConcurrentRequests` is treated as unlimited and a negative `maxConcurrentWaitSeconds` falls back to the 30s default — the config always loads.
- The router throttles inside itself: excess requests queue in a bounded per-provider semaphore instead of being pushed at the upstream (so the seibert vllm's own `max 8 per user` 429 is never hit).
- A queued request that frees a slot in time is forwarded normally, unchanged; one still waiting when the queue wait elapses gets HTTP 429 with an Anthropic-shaped `rate_limit_error` JSON body so Claude Code's own backoff retries cleanly.
- The slot is held for the full request duration including streaming SSE responses, and each provider's cap is independent — saturating one capped provider neither blocks nor is blocked by another, even when two share an upstream.
- A client that disconnects while queued never holds a slot, so no concurrency is lost to dead connections.
- Reloading the config (the SIGHUP path) rebuilds the per-provider limiters with the new values, without a process restart.
- No new Prometheus metrics and no new dependencies: router-issued 429s flow through the existing model-router `[req] ... status=429` log line and the existing `4xx_rate_limited` status class.
</summary>

<objective>
Add the per-provider concurrency config fields and an in-router buffered-channel semaphore that caps each provider's in-flight requests — queueing excess and answering with a clean retryable 429 on timeout — so bursts are absorbed inside the router instead of hammering vllm's own per-user ceiling.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- Read `pkg/config.go` — the `Provider` struct (its last field today is `AllowedApiKeys`, `yaml:"allowedApiKeys,omitempty"`) and `Config.Validate(ctx context.Context) error` (its per-provider loop currently checks `Upstream`, the `Models` globs, and the `RequiresLeadingSystem` globs). The new fields live here; their lenient (no-fail-closed) semantics are resolved at wiring, not at Load (see requirement 2).
- Read `pkg/factory/factory.go` — `CreateRouterFromConfig`'s per-provider loop (currently: `proxy := handler.NewAnthropicProxyHandler(upstream, transport)` then `providerHandlers[name] = proxy` and `Handler: proxy` on every `handler.ModelRoute`). This is the single wiring site; `defaultHandler` is read from `providerHandlers[cfg.Router.DefaultProvider]`, so wrapping the value stored in `providerHandlers[name]` caps the default-provider path too.
- Read `pkg/handler/auth-middleware.go` — the small-handler "no-op when disabled" constructor pattern (`NewAuthMiddleware` returns `next` unchanged when `len(allowedKeys) == 0`). The new limiter constructor mirrors this shape for `maxConcurrentRequests <= 0`.
- Read `pkg/handler/healthz.go` and `pkg/handler/loopback.go` — the minimal-handler style in `pkg/handler`.
- Read `pkg/handler/model-router.go` — `NewModelRouter`'s dispatch calls `target.ServeHTTP(ur, r)` where `target` is the route's `Handler`; the limiter becomes that target, so the model router's existing `[req]` line and `metrics.ObserveRequest(providerName, modelLabel, status, latency, false)` fire for limiter-issued 429s with no changes (status 429 → `statusClass` returns `4xx_rate_limited`, see `pkg/handler/metrics.go`).
- Read `pkg/config_test.go` — the `write()` helper and the `Context("Load")` rows; new load rows follow the same shape.
- Read `pkg/handler/handler_suite_test.go` (package `handler_test`) and `pkg/factory/system_lift_wiring_test.go` / `pkg/factory/auth_middleware_wiring_test.go` (package `factory_test`, `isolatedRegistry()` helper, `factory.CreateRouterFromConfig` + `httptest.NewServer` upstream + `httptest.NewRequest` served via `mux.ServeHTTP`). The new wiring test file follows this shape.
- Read `docs/dod.md` — Definition of Done (GoDoc on every new exported identifier; `bborbe/errors` conventions; Ginkgo/Gomega coverage; no new dependencies).
- Coding plugin docs (in-container paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors` (`errors.New(ctx, ...)` / `errors.Errorf(ctx, ...)` / `errors.Wrapf(ctx, err, ...)`), never `fmt.Errorf` (the repo's `Config.Validate` uses exactly these forms).
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — external `_test` packages, Ginkgo v2 + Gomega.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` — raw `go func()` is exempt in `*_test.go`; the limiter itself must NOT spawn goroutines (it runs in the request goroutine).
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — GoDoc conventions.
</context>

<requirements>
1. **Config fields in `pkg/config.go`.** Add two fields to the `Provider` struct, after `AllowedApiKeys` (its current last field). Exact shape (comment text may be reworded, but the field names, types, yaml tags, and zero-value semantics are fixed):

   ```go
   // MaxConcurrentRequests, when > 0, caps how many /v1/* requests this
   // provider forwards upstream at the same time. Requests beyond the cap
   // queue for up to MaxConcurrentWaitSeconds; a request still waiting
   // when the queue wait elapses is answered HTTP 429 with an
   // Anthropic-shaped rate_limit_error body so the client's own backoff
   // retries cleanly. Absent, 0, or negative means unlimited — no
   // queueing, no router-issued 429, byte-for-byte current behavior.
   MaxConcurrentRequests int `yaml:"maxConcurrentRequests,omitempty"`
   // MaxConcurrentWaitSeconds is how long a queued request waits for a
   // free slot before the router answers HTTP 429. Only consulted on a
   // capped provider (MaxConcurrentRequests > 0); absent, 0, or negative
   // resolves to the 30s default at wiring.
   MaxConcurrentWaitSeconds int `yaml:"maxConcurrentWaitSeconds,omitempty"`
   ```

   Both are plain `int` with `omitempty` — a config block without either field must unmarshal to the zero value and behave exactly as today (spec AC 1). Do NOT add any other field, flag, or threshold (spec Non-goals).

2. **No new validation in `Config.Validate` (`pkg/config.go`).** Do NOT add any check on these fields — validation is lenient (spec AC 2, operator decision 2026-08-18): a negative `maxConcurrentRequests` is treated as unlimited and a negative `maxConcurrentWaitSeconds` as the 30s default, both resolved at wiring (requirement 4), so no value ever fails `config.Load`. Zero is NOT rejected either — 0 on `maxConcurrentRequests` means uncapped, 0 on `maxConcurrentWaitSeconds` means "use the default". No upper bound, no cross-field checks, no non-integer handling (yaml.v3 rejects non-int at unmarshal).

3. **New handler `pkg/handler/concurrency-limiter.go`** (package `handler`, matching the repo's small-handler style — `auth-middleware.go`, `healthz.go`). Exact exported constructor signature:

   ```go
   // NewConcurrencyLimiter returns a handler that caps how many /v1/*
   // requests reach next at the same time. When maxConcurrentRequests is
   // <= 0 the wrapper is a no-op and returns next unchanged — the request
   // path is byte-for-byte identical to a release without concurrency
   // limiting (feature-off default). Excess requests queue in a
   // buffered-channel semaphore; a request that cannot acquire a slot
   // within maxConcurrentWait is answered HTTP 429 with an Anthropic-shaped
   // rate_limit_error JSON body so the client's backoff retries cleanly,
   // and a client that disconnects while queued never acquires a slot.
   // The slot is held for the full duration of next.ServeHTTP — including
   // streaming SSE responses — and released only when it returns.
   // maxConcurrentWait must be > 0; the factory resolves the 30s default
   // (spec DB 5) before constructing.
   func NewConcurrencyLimiter(next http.Handler, maxConcurrentRequests int, maxConcurrentWait time.Duration) http.Handler
   ```

   Implementation contract (spec Constraints: "buffered-channel semaphore"; Desired Behaviors 1-6; Failure Modes):
   - `if maxConcurrentRequests <= 0 { return next }` — mirrors `NewAuthMiddleware`'s no-op-when-disabled pattern (spec DB 6 / AC 5: byte-for-byte passthrough for absent/0).
   - A private struct holding `next http.Handler`, `sem chan struct{}` (buffered, capacity `maxConcurrentRequests`), and `wait time.Duration`.
   - `ServeHTTP` runs ENTIRELY in the request goroutine — do NOT spawn goroutines and do NOT call `next.ServeHTTP` in a child goroutine. The slot is held by the synchronous `next.ServeHTTP` call inside the same goroutine, so the slot lifetime equals the full request duration including streaming SSE (spec DB 1).
   - Acquire a slot with a three-case `select`:
     - `case l.sem <- struct{}{}:` — acquired; release with `defer func() { <-l.sem }()`; then `l.next.ServeHTTP(w, r)`; return. The slot is released exactly when the upstream round-trip (including SSE stream) returns.
     - `case <-timer.C:` where `timer := time.NewTimer(l.wait)` with `defer timer.Stop()` — queue timeout: write the HTTP 429 (below) and return. 429 ONLY, never a 5xx (spec Constraints: 429 is client-retryable, 5xx is not).
     - `case <-r.Context().Done():` — the client disconnected while queued: return WITHOUT acquiring a slot and without forwarding. No response write is needed (the write would fail harmlessly on the dead connection). This is spec Failure Modes "Client disconnects while queued".
   - The 429 response is EXACTLY the Anthropic error envelope (spec DB 4, Security — the client parses this shape, so the shape is load-bearing and is NOT the bborbe canonical `{error:{code,message,details}}` shape):
     - `w.Header().Set("Content-Type", "application/json")`
     - `w.WriteHeader(http.StatusTooManyRequests)`
     - Body bytes exactly: `{"type":"error","error":{"type":"rate_limit_error","message":"too many concurrent requests, please retry"}}`
     - The message is a static constant string — it MUST NOT contain queue depth, the upstream URL, or the provider name (spec Security: no internal state leaked). Do not add any header, body field, or log line beyond this.
   - Do NOT add Prometheus metrics, a dedicated log line, or an opt-out flag — the model router's existing `[req] ... status=429` line and `statusClass(429) -> "4xx_rate_limited"` already classify limiter-issued 429s (spec Non-goals). The limiter needs no provider-name knowledge.

4. **Factory wiring in `pkg/factory/factory.go`.** The limiter sits between route dispatch and the upstream proxy (spec Constraints). Add a package-level constant near `CreateRouterFromConfig`:

   ```go
   // defaultMaxConcurrentWaitSeconds is the queue timeout applied when a
   // capped provider's maxConcurrentWaitSeconds is absent, 0, or negative
   // (spec DB 5).
   const defaultMaxConcurrentWaitSeconds = 30
   ```

   In `CreateRouterFromConfig`'s per-provider loop, replace:

   ```go
   proxy := handler.NewAnthropicProxyHandler(upstream, transport)
   providerHandlers[name] = proxy
   ```

   with:

   ```go
   proxy := handler.NewAnthropicProxyHandler(upstream, transport)
   waitSeconds := prov.MaxConcurrentWaitSeconds
   if waitSeconds <= 0 {
       waitSeconds = defaultMaxConcurrentWaitSeconds
   }
   providerHandler := handler.NewConcurrencyLimiter(
       proxy,
       prov.MaxConcurrentRequests,
       time.Duration(waitSeconds)*time.Second,
   )
   providerHandlers[name] = providerHandler
   ```

   And in the same loop's `routes = append(...)`, change `Handler: proxy` to `Handler: providerHandler` (every `handler.ModelRoute` for the provider must carry the WRAPPED handler — spec Constraints "wrap each provider's proxy"). `defaultHandler` is read from `providerHandlers[cfg.Router.DefaultProvider]`, so the default-provider path is capped by construction — do not add a separate wrap for it. `time` is already imported in `pkg/factory/factory.go`. For an uncapped provider, `NewConcurrencyLimiter` returns `proxy` unchanged, so nothing else changes.

5. **Config tests in `pkg/config_test.go`** (package `pkg_test`, Ginkgo v2 + Gomega, using the existing `write()` helper and `pkgcfg.Load(context.Background(), p)`). Add a new `Context("maxConcurrentRequests")` block. These are yaml-boundary tests — a wrong tag would silently leave the field zero, so they MUST go through `Load`, not struct literals:
   - **AC 1 (load with both fields):** a provider block with `maxConcurrentRequests: 8` and `maxConcurrentWaitSeconds: 30` loads; assert `cfg.Providers["x"].MaxConcurrentRequests == 8` and `.MaxConcurrentWaitSeconds == 30`.
   - **AC 1 (absent = identical to today):** a provider block with neither field loads; assert both fields are 0 and no error.
   - **AC 1 (partial):** only `maxConcurrentRequests: 8` set → loads; `MaxConcurrentRequests == 8`, `MaxConcurrentWaitSeconds == 0` (the 0 resolves to the 30s default at wiring, not at load).
   - **AC 2 (negative maxConcurrentRequests):** `maxConcurrentRequests: -1` loads with no error; assert `MaxConcurrentRequests == -1` (the factory resolves ≤ 0 → unlimited at wiring, requirement 4).
   - **AC 2 (negative maxConcurrentWaitSeconds):** `maxConcurrentWaitSeconds: -1` loads with no error; assert `MaxConcurrentWaitSeconds == -1` (the factory resolves ≤ 0 → 30s default at wiring, requirement 4).
   - **Boundary (0 is valid):** `maxConcurrentRequests: 0` and `maxConcurrentWaitSeconds: 0` load with no error (uncapped / default-resolved respectively).
   - Existing `Context("Load")` rows must still pass unchanged.

6. **Handler tests in a new `pkg/handler/concurrency-limiter_test.go`** (package `handler_test`, Ginkgo v2 + Gomega). Use `handler.NewConcurrencyLimiter` directly with small explicit waits (never a real 30s — spec Constraints). A blocking `next` handler: count entries atomically (or via a channel), then block on a `release chan struct{}` until the test closes it, then write `200` with body `"ok"`. Goroutines for concurrent request firiing are allowed in `_test.go` (see `go-concurrency-patterns.md` exemption). Rows:
   - **AC 5 — uncapped is a byte-for-byte no-op:** `handler.NewConcurrencyLimiter(next, 0, time.Second)` must return `next` unchanged (`Expect(limiter).To(BeIdenticalTo(next))`), and a request served through it reaches `next` immediately with 200.
   - **AC 3 / DB 3 — at most N in flight; the queued request completes after the first releases its slot:** cap 1, wait 1s. Fire request 1 in a goroutine → `Eventually` the entry count is 1. Fire request 2 in a goroutine → `Consistently` (~200ms) the entry count stays 1 (the second is held, and the slot is held for the full duration of request 1's `ServeHTTP` — DB 1). Close `release` → `Eventually` BOTH requests complete with 200 and body `"ok"` (the queued request was forwarded normally, not 429'd — DB 3).
   - **AC 4 — queue timeout answers 429 with the Anthropic shape:** cap 1, wait 50ms. Request 1 in a goroutine holds the slot (blocking). Serve request 2 synchronously → assert `rec.Code == 429`, `rec.Header().Get("Content-Type")` contains `application/json`, and the body parses as JSON with `type == "error"`, `error.type == "rate_limit_error"`, and a non-empty `error.message`. Also assert the body does NOT contain the upstream URL / a provider name / a queue-depth number (spec Security — use a distinctive fixture value, e.g. assert the message equals the static generic string from requirement 3). Close `release` so request 1 finishes (no leaked goroutine).
   - **AC 6 / DB 7 — per-provider independence:** two independent limiters, each cap 1. Saturate limiter A's slot (blocking `next`, entry count 1). Serve a request through limiter B → completes 200 immediately while A remains saturated. Assert B's entry count reached 1 and A's did not change.
   - **Failure Modes — client disconnects while queued:** cap 1, wait 1s. Request 1 holds the slot (blocking). Serve request 2 with an ALREADY-cancelled request context (`r = r.WithContext(cancelledCtx)`) → `ServeHTTP` returns promptly and `next`'s entry count stays 1 (no slot acquired, nothing forwarded). Then close `release` and assert request 1 completes.

7. **Wiring + reload tests in a new `pkg/factory/concurrency_limiter_wiring_test.go`** (package `factory_test`, same shape as `system_lift_wiring_test.go` / `auth_middleware_wiring_test.go`: `factory.CreateRouterFromConfig` + an `httptest.NewServer` blocking upstream + `httptest.NewRequest`/`httptest.NewRecorder` served via the returned handler, `isolatedRegistry()` helper, `makeConfig` helper building `*pkg.Config` programmatically). The blocking upstream increments an atomic in-flight count and blocks on a shared `release chan struct{}` (selecting on `r.Context().Done()` too so teardown is leak-free), returning 200 `{"ok":true}` after release. Rows:
   - **Full-path 429 through the real dispatch boundary (level-2):** config with one capped provider (`MaxConcurrentRequests: 1`, `MaxConcurrentWaitSeconds: 1` — short but longer than the assertion window), `Models: ["m*"]`, `DefaultProvider` = that provider. Fire request 1 (body `{"model":"m1","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`) in a goroutine → `Eventually` upstream in-flight count is 1. Serve request 2 synchronously → assert 429, `Content-Type` contains `application/json`, body contains `rate_limit_error`. Assert the upstream in-flight count stays 1 (request 2 never reached the upstream). Then serve request 3 with a model that matches NO glob (e.g. `{"model":"nomatch-xyz",...}`) → assert 429 too, proving the `default_provider` handler is also the wrapped limiter. Close `release`; assert request 1 completes 200.
   - **DB 5 — absent wait resolves to the default 30s, not an instant timeout:** config with `MaxConcurrentRequests: 1` and `MaxConcurrentWaitSeconds: 0` (zero = absent at the factory). Fire request 1 (in flight, blocking). Fire request 2 in a goroutine that records completion on a channel; `Consistently` (~300ms) request 2 has NOT completed and no 429 has been written — proving the factory resolved 0 → 30s instead of passing 0 as an instant-timeout wait. Close `release`; `Eventually` request 2 completes 200.
   - **AC 7 / DB 8 — reload row (second `CreateRouterFromConfig` with a changed cap):** build handler1 from a config with `MaxConcurrentRequests: 1`, `MaxConcurrentWaitSeconds: 5`. Fire request A (glob-matched) → in-flight 1. Fire request B (glob-matched) → `Consistently` (~300ms) in-flight stays 1 (capped at 1). Build handler2 from a SECOND config, same provider key but `MaxConcurrentRequests: 2` — a fresh `CreateRouterFromConfig` call exactly mirrors the reloader's rebuild (`CreateServer`'s SIGHUP callback in `pkg/factory/factory.go`). Fire requests C and D against handler2 concurrently → `Eventually` in-flight reaches 3 (A + C + D) — the rebuilt tree's limiter enforces the NEW cap of 2. Close `release`; assert all four requests eventually complete 200.

   These rows use programmatic `*pkg.Config` values (matching the sibling wiring tests); the YAML→config boundary is covered by requirement 5, and the full YAML→SIGHUP→rebuild loop is already covered by the existing `pkg/reloader` suite — do not add a YAML-loading wiring test here.
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Config schema is fixed: `Provider` gains `maxConcurrentRequests int` (yaml `maxConcurrentRequests`, `omitempty`) and `maxConcurrentWaitSeconds int` (yaml `maxConcurrentWaitSeconds`, `omitempty`); zero-value semantics MUST remain current behavior (spec Constraints).
- Validation is lenient: a negative `maxConcurrentRequests` is treated as unlimited and a negative `maxConcurrentWaitSeconds` as the 30s default — no fail-closed rejection, the config always loads (spec Constraints, AC 2, operator decision 2026-08-18).
- The limiter sits between route dispatch and the upstream proxy (wrap each provider's proxy in `pkg/factory/factory.go`); the model router's body-read, alias, `[1m]`-strip, key-routing, and system-lift flow are untouched (spec Constraints). Do NOT modify `pkg/handler/model-router.go`, `pkg/handler/auth-middleware.go`, `main.go`, or `pkg/cli.go`.
- The limiter is a buffered-channel semaphore in the new `pkg/handler/concurrency-limiter.go`, matching the repo's small-handler style (spec Constraints). It must NOT spawn goroutines (it runs in the request goroutine).
- Queue timeout produces HTTP 429 only — never a 5xx (a 429 is client-retryable; a 5xx is not) (spec Constraints).
- The 429 body must be the Anthropic envelope `{"type":"error","error":{"type":"rate_limit_error","message": ...}}` with a generic static message — no internal state leaked (no queue depth, upstream URL, or provider name) (spec Security / Abuse).
- The limiter sits inside the existing auth + routing path; it neither bypasses nor extends auth (spec Security / Abuse).
- No new Prometheus metrics — the existing `[req] ... status=429` log line and the `4xx_rate_limited` status class already classify router-issued 429s (spec Non-goals). Do NOT add a config knob, opt-out flag, or tunable beyond the two spec fields (spec Non-goals).
- No shared/global semaphore across providers — caps are per-provider and independent (spec Non-goals).
- No new dependencies — the Go standard library suffices for a channel semaphore + timer (spec Constraints).
- Tests must not depend on real wall-clock 30s waits — use small explicit waits (spec Constraints).
- Use `github.com/bborbe/errors` for any error wrapping; never `fmt.Errorf` (go-error-wrapping-guide.md).
- No AI attribution in code or comments.
- `make precommit` must remain green — run it before declaring done.
- Follow `docs/dod.md` (GoDoc on every new exported identifier, single-source-of-truth validation in `Config.Validate`).
- Do NOT touch `docs/` or `CHANGELOG.md` in this prompt — prompt 2 owns documentation.
</constraints>

<verification>
make precommit

# AC 1 — fields + yaml tags landed:
grep -n 'MaxConcurrentRequests\|maxConcurrentRequests' pkg/config.go
grep -n 'MaxConcurrentWaitSeconds\|maxConcurrentWaitSeconds' pkg/config.go

# AC 2 — lenient validation: no fail-closed negative checks added:
grep -c 'must not be negative' pkg/config.go   # expect 0

# Handler constructor + Anthropic-shaped 429:
grep -n 'func NewConcurrencyLimiter' pkg/handler/concurrency-limiter.go
grep -n 'rate_limit_error' pkg/handler/concurrency-limiter.go

# Factory wraps every provider proxy + resolves the 30s default:
grep -n 'NewConcurrencyLimiter\|defaultMaxConcurrentWaitSeconds' pkg/factory/factory.go

# AC 1/2 — config test rows exist:
grep -c 'maxConcurrentRequests' pkg/config_test.go   # expect >=1

# No new metrics/log surface invented:
grep -rn 'prometheus\|ObserveRequest\|statusClass' pkg/handler/concurrency-limiter.go   # expect 0 lines

# Full suite:
go test -mod=mod -count=1 ./pkg/...
</verification>

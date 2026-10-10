---
status: completed
spec: [019-per-host-concurrency-cap]
summary: 'Added per-upstream-host concurrency cap (spec 019): lenient upstreamHostLimits config map, UpstreamHostKey derivation, a shared HostLimiter reusing the concurrency limiter over one semaphore, factory wiring composing it outside every per-member limiter, plus config/handler/factory tests.'
execution_id: claude-code-router-hostcap-exec-057-spec-019-host-limit-config-and-wiring
dark-factory-version: v0.196.0
created: "2026-10-10T15:30:08Z"
queued: "2026-10-10T16:10:53Z"
started: "2026-10-10T16:12:38Z"
completed: "2026-10-10T16:17:37Z"
---

# Per-host concurrency cap: upstreamHostLimits config, host-key derivation, shared host limiter wiring

<summary>
- Operators can cap the concurrent `/v1/*` requests reaching one upstream server with a new optional top-level `upstreamHostLimits` map, keyed by host. A config without the key loads and behaves exactly as today.
- Every provider and every pool member whose upstream resolves to a capped host shares that host's single budget, so no combination of provider or member caps can push one server past its ceiling.
- A host is identified by its upstream URL's lowercased host name, plus the port only when the port is not the scheme's default. `https://vllm.seibert.tools` and `https://vllm.seibert.tools/v1` are the same host; `http://127.0.0.1:8317` keys as `127.0.0.1:8317`.
- When a host is at its cap, a request waits in a bounded queue. If it waits longer than the host's wait window it gets the same clean, retryable HTTP 429 the provider-level cap already returns, and never a 5xx.
- Per-provider and per-member caps still apply inside the host cap. A request must hold both a host slot and its own member slot, and it takes the host slot first.
- A host not named in the map is never queued and never rejected by the router. It only counts its in-flight requests, which the next prompt exposes as a gauge.
- Validation is lenient. A negative host cap means unlimited, a negative host wait means the 30s default, and no value ever stops the config loading.
- A SIGHUP reload rebuilds host limiters along with everything else. A raised cap takes effect at once, and a host removed from the map becomes unlimited.
</summary>

<objective>
Add the per-upstream-host concurrency cap (spec 019). That means a lenient `upstreamHostLimits` config map, one shared host key derived from each upstream URL, and factory wiring that builds exactly ONE host limiter per distinct host and composes it OUTSIDE every per-member limiter resolving to that host. The point is that the total concurrency reaching one server is bounded no matter how many providers or pool members point at it. This is the fix for the 2026-10-10 `vllm.seibert.tools` outage, where individually-correct provider caps summed to ~32 to 48 concurrent requests against one host.
</objective>

<context>
- Repo root is the current working directory. Use repo-relative paths only. This is a single-module repo (`go.mod` at root). `make test` and `make precommit` are root targets.
- Read `CLAUDE.md` (if present) and `docs/dod.md`. The Definition of Done requires: GoDoc on every new exported identifier, `github.com/bborbe/errors` wrapping, Ginkgo/Gomega coverage, and config parsing kept single-source-of-truth in `pkg/config.go`.
- Read `pkg/config.go`:
  - The `Config` struct. Its fields are `Router`, `Providers`, `DefaultToken`, `Aliases`, `ModelPools`, `Trace`, `Auth`, `AllowedApiKeys` and `ProviderOrder` (``yaml:"-"``). `Config.UnmarshalYAML` decodes through `type plain Config`, so a new tagged field needs no UnmarshalYAML change.
  - `Provider`, `Upstream`, `normalizeUpstreams`, `Provider.UpstreamList()`, and `Config.Validate`. These are context only. Do NOT modify `Validate` (see requirement 3).
  - The imports are `context`, `fmt`, `os`, `path`, `path/filepath`, `strings`, `stdtime "time"`, `github.com/bborbe/errors`, `libtime`, `glog`, and `yaml.v3`. `net` and `net/url` are NOT imported yet.
- Read `pkg/factory/factory.go`, `CreateRouterFromConfig`:
  - Before the provider loop it declares `providerHandlers`, `upCaps`, `upInFlight`, `routes` and `metrics := handler.NewMetrics(cfg.Aliases)`.
  - The per-upstream inner loop (`for _, up := range upstreams`, ~L246-314) does `upstream, err := url.Parse(up.Upstream)`, then builds `proxy := handler.NewAnthropicProxyHandler(upstream, transport)`, then resolves `waitSeconds` against `defaultMaxConcurrentWaitSeconds` (const = 30, ~L184), then `memberHandler := handler.NewConcurrencyLimiter(proxy, up.MaxConcurrentRequests, time.Duration(waitSeconds)*time.Second)`.
  - It then type-asserts `memberHandler.(interface{ InFlight() int })` into `inFlight` (the member's own occupancy, read by least-loaded selection and the model-pool closures), and appends `handler.UpstreamMember{Upstream: up.Upstream, Handler: memberHandler, Weight: up.Weight, InFlight: inFlight, Window: up.Window, Days: up.Days, Now: o.currentDateTime.Now}`.
  - After the inner loop it builds `NewUpstreamPoolHandler` and wraps it in `NewThrottleGate`. Those are context only and stay unchanged.
  - The function carries `//nolint:funlen,gocognit`. `buildModelPools` was extracted "to keep that function's complexity under the maintidx gate", so put new logic in a small helper rather than inline.
- Read `pkg/handler/concurrency-limiter.go`. It holds:
  - `limiter429Body` (the static Anthropic-shaped body).
  - `NewConcurrencyLimiter(next http.Handler, maxConcurrentRequests int, maxConcurrentWait time.Duration) http.Handler`, which returns `next` unchanged when `maxConcurrentRequests <= 0`, else `&concurrencyLimiter{next, sem: make(chan struct{}, n), wait}`.
  - The unexported `concurrencyLimiter{next http.Handler; sem chan struct{}; wait time.Duration}`.
  - `(*concurrencyLimiter).InFlight() int { return len(l.sem) }`.
  - The three-case `ServeHTTP` select: slot acquired → `defer` release → `next.ServeHTTP`; timer → 429 with `limiter429Body`; `r.Context().Done()` → return without acquiring.

  `NewConcurrencyLimiter` allocates a FRESH semaphore per call. That is why the host budget cannot be built by calling it once per member: each call would get its own budget. Requirement 4 reuses the same `concurrencyLimiter` type and `ServeHTTP` over ONE shared semaphore instead.
- Read `pkg/handler/upstream-pool-handler.go`. `UpstreamMember` has `InFlight func() int`, where nil means 0. `leastLoaded` picks the eligible member with the fewest in-flight requests, breaking ties round-robin. The wiring test in requirement 6 relies on this to steer two keyless requests onto two different members.
- Read `pkg/handler/concurrency-limiter_test.go` (package `handler_test`). It defines package-level `expected429Body`, `blockingHandler` / `newBlockingHandler()` / `entryCount()`, `serveAsync(...)`, and `newMessagesRequest()`. These are already visible to every `handler_test` file. REUSE them in the new handler test, and do NOT redeclare them.
- Read `pkg/factory/concurrency_limiter_wiring_test.go` (package `factory_test`). It shows the shapes the new wiring test mirrors: the closure helpers `newMessagesRequest(model)`, `serveAsync`, `makeConfig`; the `release` / `closeOnce` / atomic `inFlight` upstream; and the reload row that calls `CreateRouterFromConfig` twice. `isolatedRegistry()` is a package-level helper in `pkg/factory/auth_middleware_wiring_test.go`, so reuse it.
- Read `pkg/config_test.go`. It has the `write()` helper and the `Context("maxConcurrentRequests")` block (~L945), whose `loadProvider` closure + `pkgcfg.Load` row shape the new Context mirrors. The test package is `pkg_test`, and it imports the package as `pkgcfg`.
- Read `pkg/reloader/reloader.go` for context only. It rebuilds through the factory callback, so it needs NO change (spec Constraints: "no reloader-specific host-cap code").
- Coding plugin docs (in-container paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md`: shared state via channels and atomics. A raw `go func()` is fine in `*_test.go`.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`: external `_test` packages, Ginkgo v2 + Gomega, `DescribeTable`.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md`: GoDoc conventions.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`: `errors.Wrapf(ctx, err, ...)` from `github.com/bborbe/errors`, never `fmt.Errorf`.
</context>

<requirements>
1. **Config schema in `pkg/config.go`.**
   - Add a new exported type next to `Upstream`. The field names, types, yaml tags and zero-value semantics below are fixed. You may reword the comment text.

     ```go
     // HostLimit caps the concurrent /v1/* requests reaching one upstream
     // host, summed across every provider and pool member whose upstream
     // resolves to that host (spec 019). The fields carry the same
     // absent/zero/negative semantics as the provider-level fields:
     // MaxConcurrentRequests absent, 0, or negative = unlimited (the host is
     // never queued and the router never issues a host-cap 429);
     // MaxConcurrentWaitSeconds absent, 0, or negative resolves to the 30s
     // default at wiring. Validation is lenient — no value fails Load.
     type HostLimit struct {
     	MaxConcurrentRequests    int `yaml:"maxConcurrentRequests,omitempty"`
     	MaxConcurrentWaitSeconds int `yaml:"maxConcurrentWaitSeconds,omitempty"`
     }
     ```

   - Add a field to `Config`, placed after `AllowedApiKeys` and before `ProviderOrder`:

     ```go
     // UpstreamHostLimits caps concurrency per upstream HOST (spec 019),
     // keyed by the host key UpstreamHostKey derives from an upstream URL
     // (lowercased host, plus ":port" only for a non-default port). Every
     // provider and pool member resolving to a named host draws from that
     // host's one shared budget; a host not named here is unlimited. Keys
     // are matched by exact string equality. A key naming a host no
     // provider resolves to loads and is inert. Nil / empty = no host caps,
     // byte-for-byte today's behavior.
     UpstreamHostLimits map[string]HostLimit `yaml:"upstreamHostLimits,omitempty"`
     ```

   - Do NOT add any other field, flag, or knob (spec Non-goals). Do not add a per-host log toggle, auto-derived caps, or a key-normalization option.

2. **Host-key derivation in `pkg/config.go`.** Add an exported function. This is the single source of truth for the host key: the factory (requirement 5), the tests, and prompt 2's gauge label all use it.

   ```go
   // UpstreamHostKey returns the upstreamHostLimits key an upstream URL
   // resolves to (spec 019): the URL's host name lowercased, with the port
   // appended as host:port only when it is explicit and not the scheme's
   // default (80 for http, 443 for https). The path is ignored, so
   // "https://vllm.seibert.tools" and "https://vllm.seibert.tools/v1" both
   // key "vllm.seibert.tools", and "http://127.0.0.1:8317" keys
   // "127.0.0.1:8317". IPv6 literals keep their brackets when a port is
   // appended (net.JoinHostPort).
   func UpstreamHostKey(u *url.URL) string
   ```

   - Build the key from `u.Hostname()` lowercased with `strings.ToLower`, and `u.Port()`.
   - If the port is empty, or is `"80"` with scheme `http`, or is `"443"` with scheme `https`, return the bare host. Otherwise return `net.JoinHostPort(host, port)`.
   - `url.Parse` already lowercases the scheme.
   - Add the `net` and `net/url` imports.
   - Keep it a pure function: no error return, no logging.

3. **No validation change.** Do NOT add any check on `UpstreamHostLimits` in `Config.Validate` or `normalizeUpstreams`. Validation is lenient (spec AC 2 and Constraints):
   - A negative `maxConcurrentRequests` is unlimited and a negative `maxConcurrentWaitSeconds` is the 30s default. Both are resolved at wiring (requirements 4 and 5).
   - An empty-string key, or a key naming no provider's host, loads and is inert.
   - Do NOT lowercase or otherwise rewrite the map keys. The spec mandates exact string equality against the derived key.

4. **Shared host limiter in a new file `pkg/handler/host-limiter.go`** (package `handler`, with the license header the sibling files use). This reuses the existing `concurrencyLimiter` type and its `ServeHTTP` (queue, wait, `limiter429Body`, disconnect handling) over ONE shared semaphore. Do NOT write a second queueing mechanism, and do NOT define a second 429 body.

   ```go
   // HostLimiter is the one shared concurrency budget for an upstream host
   // (spec 019). The factory builds exactly one per distinct host and wraps
   // every member handler resolving to that host via Wrap, so all of them
   // draw from the same budget. A capped HostLimiter (maxConcurrentRequests
   // > 0) shares one buffered-channel semaphore across every wrapped
   // handler and reuses the concurrency limiter's queue/wait/429 semantics
   // unchanged: excess requests queue for up to maxConcurrentWait, a
   // request still waiting is answered HTTP 429 with the static
   // Anthropic-shaped rate_limit_error body (limiter429Body — no host name,
   // queue depth, or provider name), and a client that disconnects while
   // queued never acquires a slot. The slot is held for the full request,
   // including streaming SSE responses. An unlimited HostLimiter
   // (maxConcurrentRequests <= 0) never queues and never answers 429; its
   // wrapped handlers only count the requests in flight, so InFlight is
   // meaningful for every host.
   type HostLimiter struct {
   	sem      chan struct{} // nil when unlimited
   	wait     time.Duration
   	inFlight atomic.Int32 // in-flight count, used only when unlimited
   }

   // NewHostLimiter returns the shared budget for one upstream host.
   // maxConcurrentRequests <= 0 means unlimited. maxConcurrentWait must be
   // > 0 on a capped limiter; the factory resolves the 30s default before
   // constructing.
   func NewHostLimiter(maxConcurrentRequests int, maxConcurrentWait time.Duration) *HostLimiter

   // Wrap returns next guarded by this host's shared budget. Every handler
   // returned by Wrap on the same HostLimiter shares one budget.
   func (h *HostLimiter) Wrap(next http.Handler) http.Handler

   // InFlight returns the host's current in-flight request count: the
   // shared semaphore's occupancy when capped, the live in-flight count
   // when unlimited.
   func (h *HostLimiter) InFlight() int
   ```

   Implementation contract:
   - `NewHostLimiter`: when `maxConcurrentRequests > 0`, set `sem: make(chan struct{}, maxConcurrentRequests)`. Otherwise leave `sem` nil. Always store `wait`.
   - `Wrap` on a capped limiter returns `&concurrencyLimiter{next: next, sem: h.sem, wait: h.wait}`. This is the SAME type and `ServeHTTP` as `NewConcurrencyLimiter`, sharing `h.sem`.
   - `Wrap` on an unlimited limiter returns `&hostInFlightCounter{next: next, inFlight: &h.inFlight}`. This is a new unexported type whose `ServeHTTP` does `c.inFlight.Add(1)`, `defer c.inFlight.Add(-1)`, then `c.next.ServeHTTP(w, r)`. It runs in the request goroutine, has no timer, no queue and no response rewriting, and does not wrap `w`, so SSE flushing is untouched. The `defer` guarantees the count is released even if `next` panics (spec Failure Mode "request goroutine dies mid-request").
   - `InFlight` returns `len(h.sem)` when `h.sem != nil`, else `int(h.inFlight.Load())`. `int32` → `int` is a widening conversion, so no gosec suppression is needed.
   - Name new identifiers with a `host` prefix (`HostLimiter`, `NewHostLimiter`, `hostInFlightCounter`). Do NOT create a file named `inflight.go`, and do NOT use generic names like `inflightTracker` or `InflightGauge`. A sibling branch is adding `pkg/handler/inflight.go` with per-provider in-flight symbols, and the names must not collide.
   - Do NOT modify `NewConcurrencyLimiter`, `concurrencyLimiter`, its `ServeHTTP`, its `InFlight`, or `limiter429Body`. Their behavior and the 429 body stay byte-identical (spec Constraints).

5. **Factory wiring in `pkg/factory/factory.go`.**
   - Before the provider loop, next to `upCaps` / `upInFlight`, declare:

     ```go
     // One shared HostLimiter per distinct upstream host (spec 019), keyed
     // by pkg.UpstreamHostKey. Every member resolving to the same host —
     // across providers and across pool members — is wrapped by the same
     // instance, so they draw from one budget.
     hostLimiters := make(map[string]*handler.HostLimiter)
     ```

   - Add a small package-level helper. It keeps `CreateRouterFromConfig` under the complexity gates and gives the reload semantics one home.

     ```go
     // hostLimiterFor returns the shared HostLimiter for hostKey, building it
     // on first use from limits[hostKey] (absent key = unlimited). A host's
     // maxConcurrentWaitSeconds <= 0 resolves to the 30s default
     // (defaultMaxConcurrentWaitSeconds), and maxConcurrentRequests <= 0
     // yields an unlimited limiter (spec 019 lenient validation).
     func hostLimiterFor(
     	hostLimiters map[string]*handler.HostLimiter,
     	limits map[string]pkg.HostLimit,
     	hostKey string,
     ) *handler.HostLimiter
     ```

     The body looks up `hostLimiters[hostKey]` and returns it if present. Otherwise it reads `limit := limits[hostKey]` (a nil map read is safe and yields the zero `HostLimit`), resolves the wait, calls `handler.NewHostLimiter(limit.MaxConcurrentRequests, time.Duration(waitSeconds)*time.Second)`, stores the result and returns it.
   - In the per-upstream inner loop, AFTER `memberHandler` is built and AFTER the existing `memberHandler.(interface{ InFlight() int })` assertion has captured `inFlight`, wrap the member:

     ```go
     // Host cap OUTSIDE the per-member limiter (spec 019): a request takes
     // its host slot first, then its member slot. inFlight above was
     // captured from the unwrapped member limiter on purpose — least-loaded
     // selection and model-pool saturation read the MEMBER's occupancy,
     // not the host's.
     memberHandler = hostLimiterFor(hostLimiters, cfg.UpstreamHostLimits, pkg.UpstreamHostKey(upstream)).Wrap(memberHandler)
     ```

     `upstream` is the `*url.URL` the loop already parsed, so do NOT parse the URL a second time. The appended `handler.UpstreamMember` keeps `Handler: memberHandler` (now the host-wrapped handler) and `InFlight: inFlight` (unchanged, the member's own).
   - The ordering is load-bearing. If the `InFlight` assertion ran on the wrapped handler, a capped host's `*concurrencyLimiter` wrapper would report the HOST's occupancy as the member's load and corrupt least-loaded selection.
   - Leave `NewUpstreamPoolHandler`, `NewThrottleGate`, `providerHandlers`, `routes`, `buildModelPools`, `buildPoolMember` and the metrics calls unchanged. Leave `hostLimiters` as a local variable: prompt 2 will hand it to a metrics collector. Do NOT register any collector or touch metrics in this prompt.
   - A `CreateRouterFromConfig` call builds a FRESH `hostLimiters` map, so a SIGHUP rebuild applies changed, added and removed `upstreamHostLimits` entries automatically. A removed host gets an unlimited limiter on the new tree, and in-flight requests finish on the old tree. Do NOT add anything to `pkg/reloader`.

6. **Tests.** Use Ginkgo v2 + Gomega and follow the existing shapes. Every row named below maps to a spec AC.

   **6a. `pkg/config_test.go`**: a new `Context("upstreamHostLimits")` (package `pkg_test`). These are yaml-boundary rows, so they go through `pkgcfg.Load` via `write()`. The base config is the `loadProvider` shape from `Context("maxConcurrentRequests")`, with the extra YAML appended at TOP level (zero indent):
   - **AC 1, one host loads:** `upstreamHostLimits:` / `  vllm.seibert.tools:` / `    maxConcurrentRequests: 8` / `    maxConcurrentWaitSeconds: 30` loads with no error, and `cfg.UpstreamHostLimits["vllm.seibert.tools"]` equals `pkgcfg.HostLimit{MaxConcurrentRequests: 8, MaxConcurrentWaitSeconds: 30}`.
   - **AC 1, absent key = today:** the config with no `upstreamHostLimits` key loads, and `cfg.UpstreamHostLimits` is empty (`BeEmpty()`).
   - **AC 2, negative values load:** `maxConcurrentRequests: -1` and `maxConcurrentWaitSeconds: -1` load with no error and are preserved as `-1`. The comment states the factory resolves `<= 0` to unlimited / the 30s default at wiring, and that the behavioral fallback is asserted in 6b/6c.
   - **Zeroes load:** explicit `0` / `0` loads with no error.
   - **Inert key:** a key naming a host no provider uses (e.g. `unused.example`) loads with no error.
   - **Host key derivation:** in a NEW file `pkg/upstream_host_key_test.go` (package `pkg_test`; `pkg/config_test.go` is at 1852 lines against revive's 2000-line `file-length-limit`, so do not grow it with this table), a `DescribeTable` over `pkgcfg.UpstreamHostKey(mustParseURL(raw))`. Add a small local `mustParseURL` helper that calls `url.Parse` and `Expect(err).NotTo(HaveOccurred())`. Rows:
     - `https://vllm.seibert.tools` → `vllm.seibert.tools`
     - `https://vllm.seibert.tools/v1` → `vllm.seibert.tools`
     - `https://VLLM.Seibert.Tools` → `vllm.seibert.tools`
     - `http://127.0.0.1:8317` → `127.0.0.1:8317`
     - `https://vllm.seibert.tools:443` → `vllm.seibert.tools`
     - `http://ollama.local:80` → `ollama.local`
     - `http://ollama.local:443` → `ollama.local:443` (443 is not http's default)
     - `https://api.example:8443/v1` → `api.example:8443`
     - `http://[::1]:8080` → `[::1]:8080`

   **6b. New `pkg/handler/host-limiter_test.go`** (package `handler_test`). Reuse `blockingHandler`, `newBlockingHandler`, `serveAsync`, `newMessagesRequest` and `expected429Body` from `concurrency-limiter_test.go`, and do NOT redeclare them.
   - **AC 3 (handler level), shared budget across wrapped handlers:** `hl := handler.NewHostLimiter(1, time.Second)`, `innerA`, `innerB`, `wrapA := hl.Wrap(innerA)`, `wrapB := hl.Wrap(innerB)`.
     - Serve via `wrapA` async, then `Eventually` `innerA.entryCount() == 1` and `hl.InFlight() == 1`.
     - Serve via `wrapB` async, then `Consistently` (~200ms) `innerB.entryCount() == 0`. B is held by A's slot even though B wraps a different handler.
     - Close `innerA.release` and `innerB.release`. `Eventually` both done and both 200, and `hl.InFlight() == 0`.
   - **AC 5, queue timeout → 429, never 5xx:**
     - Set up `hl := handler.NewHostLimiter(1, 50*time.Millisecond)` and hold a slot via `hl.Wrap(innerA)`.
     - A request via `hl.Wrap(innerB)` returns `http.StatusTooManyRequests` with `Content-Type` containing `application/json`.
     - The body equals `expected429Body` EXACTLY (byte-identical to the provider limiter, with no host name) and contains `rate_limit_error`.
     - Assert `Expect(rec.Code).To(BeNumerically("<", 500))`, and assert `innerB.entryCount() == 0`.
     - Release and clean up.
   - **AC 6 (handler level), unlimited passthrough:**
     - For both `handler.NewHostLimiter(0, time.Second)` and `handler.NewHostLimiter(-1, time.Second)` (use a `DescribeTable` or two `It`s), wrap one `blockingHandler` and fire 5 requests async.
     - `Eventually` `entryCount() == 5`. All five entered at once, with no queueing. Also assert `hl.InFlight() == 5`.
     - Close `release`. `Eventually` all done with 200 (none 429), and `hl.InFlight() == 0`.
   - **InFlight occupancy, capped:** `NewHostLimiter(2, time.Second)` reports `0` at rest, `1` while one request is held, and `0` after it returns.
   - **Disconnect while queued:** with the host slot held, a request with an already-cancelled context (mirror the concurrency limiter's disconnect row) returns without entering its inner handler. `hl.InFlight()` stays `1`.
   - **No slot leak when the inner handler panics (spec Failure Mode "request goroutine dies mid-request"):** for both `NewHostLimiter(1, time.Second)` and `NewHostLimiter(0, time.Second)`, serve once through `hl.Wrap(panicky)` where `panicky` panics, inside a func that `recover()`s around `ServeHTTP`; then assert `hl.InFlight() == 0`, and for the capped limiter that a following request still acquires the slot and returns 200.

   **6c. New `pkg/factory/host_limiter_wiring_test.go`** (package `factory_test`). Model it on `concurrency_limiter_wiring_test.go`, with local closures `newMessagesRequest(model)`, `serveAsync`, `release`/`closeOnce`. The `httptest.NewServer` upstream must track BOTH the current in-flight count and the PEAK in-flight count: an atomic increment, then a CAS loop raising `peak`, then a `defer` decrement, then block on `release` or `r.Context().Done()`, then write 200 `{"ok":true}`. Compute the host key from the test server with `pkg.UpstreamHostKey(u)` where `u, _ := url.Parse(srv.URL)`. That is `127.0.0.1:<port>`, and it traverses the real derivation. Build every router with `factory.CreateRouterFromConfig(context.Background(), cfg, isolatedRegistry())`, using programmatic `*pkg.Config` values. Rows:
   - **AC 3, shared budget across providers:**
     - Two providers, `"a"` (`Models: ["a*"]`) and `"b"` (`Models: ["b*"]`). Both have `Upstream: srv.URL`, `MaxConcurrentRequests: 4` and `MaxConcurrentWaitSeconds: 5`, so each provider's own cap is ABOVE the host cap. Set `Router.DefaultProvider: "a"` and `UpstreamHostLimits: {hostKey: {MaxConcurrentRequests: 2, MaxConcurrentWaitSeconds: 5}}`.
     - Fire `a1`, `a2`, `b1`, `b2` async. `Eventually` in-flight == 2, then `Consistently` (~300ms) in-flight == 2.
     - Close `release`. `Eventually` all four done with 200.
     - Finally assert `peak == 2`: N, not 2N or 4.
   - **AC 4, shared budget across pool members (one host limiter for the host, not one per member):**
     - One provider `"p"` with `Upstreams: []pkg.Upstream{{Upstream: srv.URL, Weight: 1, MaxConcurrentRequests: 1, MaxConcurrentWaitSeconds: 1}, {Upstream: srv.URL + "/v1", Weight: 1, MaxConcurrentRequests: 1, MaxConcurrentWaitSeconds: 1}}`. Both members key the same host, and the path is ignored. Set `UpstreamHostLimits: {hostKey: {MaxConcurrentRequests: 1, MaxConcurrentWaitSeconds: 1}}`.
     - Fire request 1 async (keyless, no `x-session-id`). `Eventually` in-flight == 1.
     - Serve request 2 synchronously. Least-loaded steers it to the OTHER member, because the first member's own InFlight is 1, so only a SHARED host budget can hold it. Assert `rec2.Code == 429`, the body contains `rate_limit_error`, and `peak == 1`.
     - Close `release`. `rec1.Code == 200`.
     - Add a comment explaining that with one host limiter per member, request 2 would have reached the upstream and `peak` would be 2. This behavioral assertion is the evidence for "one host limiter instance per host".
   - **AC 6, uncapped host passthrough:**
     - Provider on `srv.URL` with NO member cap. `UpstreamHostLimits` names a DIFFERENT host only (`{"other.example": {MaxConcurrentRequests: 1}}`). That entry is inert, which covers the spec Failure Mode "config names a host no provider resolves to".
     - Fire 3 requests async. `Eventually` in-flight == 3, with no queueing.
     - Close `release`. All three are 200 and none is 429.
   - **AC 7, member cap still enforced inside the host cap:**
     - Provider `MaxConcurrentRequests: 2`, `MaxConcurrentWaitSeconds: 5`, and host cap 8 (`MaxConcurrentWaitSeconds: 5`).
     - Fire 4 async. `Eventually` in-flight == 2, then `Consistently` (~300ms) in-flight == 2.
     - Close `release`. All four are 200, and `peak == 2`.
   - **AC 2 behavior, negative host values fall back:**
     - **(i)** Host `MaxConcurrentRequests: -1` behaves as unlimited. With 3 async requests on an uncapped provider, `Eventually` in-flight == 3.
     - **(ii)** Host `MaxConcurrentRequests: 1`, `MaxConcurrentWaitSeconds: -1` resolves to the 30s default, not an instant timeout. Mirror the existing "resolves an absent maxConcurrentWaitSeconds to the 30s default" row: request 2 is still pending after a ~300ms `Consistently`. Then release, and request 2 completes with 200.
   - **AC 9, reload (two directions, mirroring the SIGHUP rebuild):**
     - `cfg1` has host cap 1 and wait 5, on a provider with no member cap. Build `handler1`. Fire A async, `Eventually` in-flight 1. Fire B async, `Consistently` (~300ms) in-flight 1.
     - `cfg2` is the same provider with host cap 2. Build `handler2` with a second `CreateRouterFromConfig`. Fire C and D async. `Eventually` in-flight == 3: A on handler1 plus C and D on handler2, so the new cap of 2 is enforced on a fresh limiter.
     - `cfg3` is the same provider with `UpstreamHostLimits: nil` (host removed). Build `handler3`. Fire E, F and G async. `Eventually` in-flight == 6: all three admitted at once, so the host is unlimited.
     - Close `release`. `Eventually` A through G all done with 200. B was forwarded after A finished, within its 5s wait.

7. **Self-check before finishing.** Run `make precommit` and confirm it passes. Then walk spec ACs 1–7 and 9 against the tests above, and confirm each has a row that would FAIL against the pre-change code. In particular, the AC 3 and AC 4 peak assertions must fail if the host limiter were built per member or composed inside the member limiter.
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- The config schema is fixed: a top-level `upstreamHostLimits` key, `map[string]HostLimit` (yaml `upstreamHostLimits,omitempty`). `HostLimit` carries `maxConcurrentRequests int` (yaml `maxConcurrentRequests,omitempty`) and `maxConcurrentWaitSeconds int` (yaml `maxConcurrentWaitSeconds,omitempty`). A zero value for the whole key must keep today's behavior.
- Host key derivation: the upstream URL's host, lowercased, with an explicit non-default port appended as `host:port`. Matching against the map is exact string equality on that value.
- Validation is lenient and matches the provider-level fields: a negative host `maxConcurrentRequests` is unlimited, a negative host `maxConcurrentWaitSeconds` is the 30s default, and no value fails `config.Load`.
- The host limiter is composed OUTSIDE the existing per-provider / per-member limiter, so a request acquires its host slot first. The existing concurrency limiter (`concurrencyLimiter` type + `ServeHTTP` in `pkg/handler/concurrency-limiter.go`) is reused for the host budget over a shared semaphore, and no second queueing mechanism is written. Its 429 body and wait semantics are unchanged.
- The 429 body stays byte-identical to `limiter429Body`. The host limiter adds no field naming the host, queue depth or provider.
- There is no change to per-provider / per-member `maxConcurrentRequests` / `maxConcurrentWaitSeconds` semantics. They keep their own independent budgets inside the host cap. This spec deliberately supersedes spec 011's "no shared/global semaphore" non-goal, but only through the host cap. The per-provider caps are not relaxed.
- No automatic derivation of host caps from provider config. A host is capped only when the operator names it.
- No change to auth, model routing, alias resolution, key routing, system-lift, body handling, the throttle gate, or the upstream pool handler. Do NOT modify `pkg/handler/model-router.go`, `pkg/handler/throttle-gate.go`, `pkg/handler/upstream-pool-handler.go`, `pkg/handler/auth-middleware.go`, `pkg/reloader/`, `main.go`, or `pkg/cli.go`.
- An enabled throttle gate observes a host-cap 429 exactly as it already observes a member-cap 429, because both happen beneath the pool handler it wraps. This is pre-existing composition, so do not special-case it.
- The reloader rebuilds host limiters through the same factory path as provider limiters, with no reloader-specific host-cap code.
- Do NOT touch `pkg/handler/metrics.go`, any `docs/` file, or `CHANGELOG.md`. Prompt 2 owns the `ccrouter_upstream_inflight` gauge and prompt 3 owns docs and the changelog. Do NOT register any Prometheus collector in this prompt.
- Do NOT add, rename or reference the per-provider gauges `ccrouter_inflight_requests` / `ccrouter_inflight_requests_peak`, and do NOT create `pkg/handler/inflight.go`. A sibling branch owns those.
- No start-rate limiting, latency backoff, circuit breaker, or 5xx throttle. No new queue-depth, wait-time or rejection metrics.
- No new dependencies. The standard library (`net`, `net/url`, `sync/atomic`) plus existing libs are enough.
- Tests must not depend on real wall-clock waits beyond small explicit windows (50–300ms `Consistently`, and the 1s host wait in the AC 4 row).
- Go style, `github.com/bborbe/errors` wrapping, GoDoc on exported items, and the repo's Ginkgo/Gomega conventions apply unchanged (`docs/dod.md`).
- No AI attribution in code or comments.
- Existing tests must still pass. `make precommit` must be green.
</constraints>

<verification>
make precommit

# Schema + key derivation landed in the config package:
grep -n 'UpstreamHostLimits map\[string\]HostLimit' pkg/config.go
grep -n 'yaml:"upstreamHostLimits,omitempty"' pkg/config.go
grep -n 'func UpstreamHostKey' pkg/config.go

# Shared host limiter reuses the concurrency limiter type (no second queue, no second 429 body):
grep -n 'func NewHostLimiter\|func (h \*HostLimiter) Wrap\|func (h \*HostLimiter) InFlight' pkg/handler/host-limiter.go
grep -n '&concurrencyLimiter{' pkg/handler/host-limiter.go
! grep -n 'too many concurrent requests' pkg/handler/host-limiter.go

# Factory builds one limiter per host and wraps members:
grep -n 'hostLimiterFor\|UpstreamHostKey' pkg/factory/factory.go

# Prompt boundary — metrics, docs, changelog untouched by this prompt:
! grep -n 'ccrouter_upstream_inflight' pkg/handler/metrics.go

# AC 1 evidence — new config rows exist:
grep -c 'upstreamHostLimits' pkg/config_test.go

# Targeted suites:
go test -mod=mod -count=1 ./pkg/ ./pkg/handler/ ./pkg/factory/
</verification>

---
status: completed
spec: [018-adaptive-429-delay-gate]
summary: 'Adaptive 429 delay gate shipped: the per-provider config fields, AIMD pacing handler, additive ccrouter_throttled_total counter, and factory wiring were verified in place; added the full handler test suite (AC 3-10 + disconnect), the factory wiring/reload test (AC 11 + AC 10 negative evidence), and the metrics registration-contract row; refactored observe into helpers to clear gocognit/nestif lint gates. CHANGELOG.md and docs deliberately left to prompt 2 (spec-018-adaptive-429-docs-changelog.md) per the prompt''s explicit constraint.'
execution_id: claude-code-router-adaptive-429-exec-055-spec-018-adaptive-429-config-and-gate
dark-factory-version: dev
created: "2026-08-27T19:00:00Z"
queued: "2026-08-27T18:52:17Z"
started: "2026-08-27T18:57:13Z"
completed: "2026-08-27T19:08:41Z"
branch: dark-factory/adaptive-429-delay-gate
---

# Adaptive 429 delay gate: config surface, gate handler, metrics, factory wiring

<summary>
- Operators can enable adaptive pacing per provider with two optional YAML fields (`throttle429Threshold`, `throttleMaxDelaySeconds`); a provider with neither field loads identically to today.
- When a windowed count of upstream 429 responses reaches the threshold, the gate starts delaying subsequent requests to that provider before forwarding — the 429'd request is never retried, only later requests are paced.
- The pacing delay follows AIMD: 1s on entry, ×2 per observed 429, ÷2 per clean 60s window, capped at `throttleMaxDelaySeconds`, and the provider exits throttle when the delay decays below 1s.
- The pacing queue is bounded (fixed capacity); a request that cannot acquire a pacing slot within the max delay is answered HTTP 429 with the exact Anthropic-shaped `rate_limit_error` body — never a hang, no internal state leaked.
- Disabled (threshold absent, 0, or negative) is a byte-for-byte no-op: the gate constructor returns the inner handler unchanged, no delay, no 429, no counter.
- Each provider's throttle state is fully independent — throttling one provider neither delays nor blocks another.
- A new additive `ccrouter_throttled_total{provider}` counter increments once per paced request; the existing `status_class` 7-value enum and `4xx_rate_limited` classification are untouched.
- Entry/exit log lines (`[throttle] provider=<name> state=on/off`) and a V(4) per-paced-request line give the operator log observability; the model router's `latency=` already includes the pacing delay.
- SIGHUP reload rebuilds each gate with changed values (in-memory per-provider state resets on rebuild, re-accumulates on the next 429s).
</summary>

<objective>
Add the per-provider adaptive 429 delay gate: two lenient config fields, a bounded pacing handler that delays forwarding to a provider under sustained 429 pressure (AIMD delay, overflow 429, per-provider independence), an additive throttled counter, and the factory wiring that wraps each provider's upstream pool — so an operator can turn the gate on for a rate-limited provider with two YAML knobs and observe pacing in the router log.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only. Run `make test` / `make precommit` in the repo root — this is a single-module repo (`go.mod` at root), and both targets are root targets.
- Read `pkg/config.go` — the `Provider` struct (current last field is `Days`, spec 017) and `Config.Validate(ctx context.Context) error`. The two new fields live here; their lenient (no-fail-closed) semantics are resolved at wiring, not at Load (see requirement 2).
- Read `pkg/factory/factory.go` — `CreateRouterFromConfig`'s per-provider loop: line ~287 `providerHandler := handler.NewUpstreamPoolHandler(ctx, members)`, then `providerHandlers[name] = providerHandler` and every `handler.ModelRoute` carries `Handler: providerHandler`. `defaultHandler` is read from `providerHandlers[cfg.Router.DefaultProvider]` (line ~305), so wrapping the value stored in `providerHandlers[name]` caps the default-provider path too. `metrics := handler.NewMetrics(cfg.Aliases)` is currently created AFTER the loop (line ~331) — it must move before the loop so the gate can receive `metrics.ThrottledTotal` (requirement 6). The `defaultMaxConcurrentWaitSeconds = 30` constant (line ~166) is the pattern for the new max-delay default. `o.currentDateTime` is `libtime.CurrentDateTimeGetter`; `o.currentDateTime.Now` is a `func() libtime.DateTime` method value — the pool handler passes it into each `UpstreamMember.Now` (spec 014/017 precedent for the injected clock).
- Read `pkg/handler/concurrency-limiter.go` — the exact pattern this gate mirrors: `limiter429Body` (line 18, the static Anthropic-shaped body the overflow 429 reuses verbatim), the `NewConcurrencyLimiter` no-op-when-disabled constructor (`if maxConcurrentRequests <= 0 { return next }`), the unexported-struct + exported-accessor shape (`InFlight()`), and the three-case select in `ServeHTTP` (slot / timeout → 429 / `r.Context().Done()`).
- Read `pkg/handler/status-recorder.go` — `statusRecorder` (unexported, same package): the gate wraps `w` in it before calling the wrapped handler so the observed status feeds the detector; its `Unwrap()` keeps SSE-safe flushing working through `http.NewResponseController` (the constraint explicitly requires wrapping the forward in `statusRecorder`).
- Read `pkg/handler/upstream-pool-handler.go` — the `realNow` fallback clock (`var realNow = func() libtime.DateTime { return libtime.DateTime(stdtime.Now()) }`, package `handler`) the gate reuses when `now` is nil; the `Now func() libtime.DateTime` field shape.
- Read `pkg/handler/metrics.go` — `Metrics` struct (4 collectors), `NewMetrics`, and `Register` (the collector loop to extend). The `4xx_rate_limited` status-class value (line ~188 in `statusClass`) is NOT touched (spec Non-goals).
- Read `pkg/handler/export_test.go` — the existing re-export pattern (`package handler`) for exposing unexported symbols to `handler_test` (package `handler_test`). New additions go here (requirement 5).
- Read `pkg/config_test.go` — the `write()` helper and `Context("Load")` rows; new load rows follow the same shape (yaml-boundary tests through `pkgcfg.Load`).
- Read `pkg/handler/concurrency-limiter_test.go` — the `blockingHandler` / `serveAsync` / `expected429Body` helpers and row shapes the new gate test file mirrors.
- Read `pkg/factory/concurrency_limiter_wiring_test.go` — the `makeConfig` / `newMessagesRequest` / `serveAsync` / `isolatedRegistry()` helper shapes and the reload row (`CreateRouterFromConfig` twice with a fresh config) the new wiring test mirrors.
- Read `docs/dod.md` — Definition of Done (GoDoc on every new exported identifier; `bborbe/errors` conventions; Ginkgo/Gomega coverage).
- Coding plugin docs (in-container paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` — CounterVec naming/labels, registration in `Register`, `testutil.ToFloat64` assertion pattern.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md` — glog levels: unconditional INFO via `glog.Infof`, V(4) for per-item detail.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` — mutex-guarded shared state; raw `go func()` is exempt in `*_test.go`.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — external `_test` packages, Ginkgo v2 + Gomega.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — GoDoc conventions.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors` for any error wrapping in the factory (`errors.Wrapf(ctx, err, ...)`); never `fmt.Errorf`.
</context>

<requirements>
1. **Config fields in `pkg/config.go`.** Add two fields to the `Provider` struct, after `Days` (its current last field, spec 017). Exact shape (comment text may be reworded, but the field names, types, yaml tags, and zero-value semantics are fixed):

   ```go
   // Throttle429Threshold, when > 0, enables the adaptive 429 delay gate
   // for this provider (spec 018): once the windowed count of 429
   // responses from the upstream within the 60s observation window reaches
   // this threshold, subsequent /v1/* requests to this provider are
   // delayed before forwarding (AIMD pacing), giving a rate-limited
   // upstream a breathing window to recover. The 429'd request itself is
   // never retried — only subsequent requests are paced. Absent, 0, or
   // negative disables the gate: no delay, no pacing 429, no throttled
   // counter, byte-for-byte current behavior. Read at provider level only
   // — NOT copied onto upstream members (unlike MaxConcurrentRequests).
   Throttle429Threshold int `yaml:"throttle429Threshold,omitempty"`
   // ThrottleMaxDelaySeconds is the upper bound of the pacing delay while
   // throttled (spec 018): the delay starts at 1s and doubles on each
   // observed 429, never exceeding this value. Only consulted on an
   // enabled provider (Throttle429Threshold > 0); absent, 0, or negative
   // resolves to the 30s default at wiring.
   ThrottleMaxDelaySeconds int `yaml:"throttleMaxDelaySeconds,omitempty"`
   ```

   Both are plain `int` with `omitempty` — a config block without either field must unmarshal to the zero value and behave exactly as today (spec AC 1). Do NOT add the fields to the `Upstream` struct (spec Constraints: "they are NOT copied into upstream members"). Do NOT add any other field, flag, or threshold (spec Non-goals).

2. **No new validation in `Config.Validate` (`pkg/config.go`).** Do NOT add any check on these fields — validation is lenient (spec AC 2): a negative `throttle429Threshold` is treated as disabled (same as absent/0) and a negative `throttleMaxDelaySeconds` as the 30s default, both resolved at wiring (requirement 6), so no value ever fails `config.Load`. Zero is NOT rejected either. No upper bound, no cross-field checks, no non-integer handling (yaml.v3 rejects non-int at unmarshal).

3. **New handler `pkg/handler/throttle-gate.go`** (package `handler`, matching the repo's small-handler style — `concurrency-limiter.go`, `auth-middleware.go`). This is the full file shape; keep the license header and package doc conventions, and make the GoDoc complete per `docs/dod.md`:

   ```go
   // Copyright (c) 2026 Benjamin Borbe All rights reserved.
   // Use of this source code is governed by a BSD-style
   // license that can be found in the LICENSE file.

   package handler

   import (
   	"net/http"
   	"sync"
   	stdtime "time"

   	libtime "github.com/bborbe/time"
   	"github.com/golang/glog"
   	"github.com/prometheus/client_golang/prometheus"
   )

   const (
   	// throttleObservationWindow is the fixed 60s window over which 429
   	// responses are counted (spec 018 DB 2). Fixed internal constant,
   	// not a config knob.
   	throttleObservationWindow = 60 * stdtime.Second
   	// throttleInitialDelay is the pacing delay a provider starts with on
   	// entering throttle (spec 018 DB 2).
   	throttleInitialDelay = stdtime.Second
   	// throttleDelayMultiplier is the AIMD increase: each observed 429
   	// while throttled multiplies the delay by 2, capped at the max.
   	throttleDelayMultiplier = 2
   	// throttleDelayDivisor is the AIMD decrease: each clean 60s window
   	// divides the delay by 2.
   	throttleDelayDivisor = 2
   	// throttleRecoveryFloor is the delay below which a throttled
   	// provider exits throttle and forwards undelayed (spec 018 DB 2).
   	throttleRecoveryFloor = stdtime.Second
   	// throttleMaxPacedRequests is the bounded pacing-queue capacity
   	// (spec 018 DB 3): while throttled, at most this many requests wait
   	// their pacing turn; a request that cannot acquire a pacing slot
   	// within the max delay is answered HTTP 429 with the static
   	// Anthropic-shaped rate_limit_error body. Fixed internal constant,
   	// not a config knob.
   	throttleMaxPacedRequests = 32
   )

   // NewThrottleGate returns a handler that, when threshold is > 0, delays
   // /v1/* requests destined for the wrapped provider once a windowed count
   // of upstream 429 responses reaches the threshold (spec 018): while
   // throttled, each request waits the current pacing delay before
   // forwarding. The delay follows AIMD — it starts at 1s (or maxDelay,
   // whichever is smaller), doubles on each observed 429, and is capped at
   // maxDelay; each clean 60s window (no 429) halves it, and below the 1s
   // recovery floor the provider exits throttle and requests forward
   // undelayed. The 429'd request is never retried — the response passes
   // through unchanged and the status is observed only to adjust future
   // pacing. When threshold is <= 0 the wrapper is a no-op and returns next
   // unchanged — the request path is byte-for-byte identical to a release
   // without the gate (feature-off default).
   //
   // The pacing queue is bounded: while throttled, at most
   // throttleMaxPacedRequests requests wait their pacing turn; a request
   // that cannot acquire a pacing slot within maxDelay is answered HTTP 429
   // with the Anthropic-shaped rate_limit_error body (limiter429Body), and
   // a client that disconnects while waiting neither holds a pacing slot
   // nor is forwarded.
   //
   // maxDelay must be > 0; the factory resolves the 30s default (spec DB 4)
   // before constructing. throttled is the ccrouter_throttled_total
   // CounterVec labeled by provider, incremented once per paced (delayed)
   // request; it must be non-nil. now must be non-nil (the router's
   // injected clock; the factory passes o.currentDateTime.Now) and falls
   // back to the real clock when nil.
   func NewThrottleGate(
   	next http.Handler,
   	provider string,
   	threshold int,
   	maxDelay stdtime.Duration,
   	now func() libtime.DateTime,
   	throttled *prometheus.CounterVec,
   ) http.Handler {
   	if threshold <= 0 {
   		return next
   	}
   	if now == nil {
   		now = realNow
   	}
   	return &throttleGate{
   		next:      next,
   		provider:  provider,
   		threshold: threshold,
   		maxDelay:  maxDelay,
   		now:       now,
   		throttled: throttled,
   		pace:      make(chan struct{}, throttleMaxPacedRequests),
   	}
   }

   type throttleGate struct {
   	next      http.Handler
   	provider  string
   	threshold int
   	maxDelay  stdtime.Duration
   	now       func() libtime.DateTime
   	throttled *prometheus.CounterVec
   	pace      chan struct{}

   	mu       sync.Mutex
   	window   []stdtime.Time // 429 observation timestamps within the 60s window
   	delay    stdtime.Duration
   	on       bool
   	lastHalf stdtime.Time
   }
   ```

   Implementation contract (spec Desired Behaviors 1-8; Failure Modes; Security):

   - `ServeHTTP` runs ENTIRELY in the request goroutine — do NOT spawn goroutines. It snapshots `on` and `delay` under the mutex at entry, then:
     - Not throttled: forward immediately (call `forward`, below).
     - Throttled: acquire a pacing slot from `g.pace` (buffered channel, capacity `throttleMaxPacedRequests`) with a three-case `select` mirroring the concurrency limiter:
       - `case g.pace <- struct{}{}:` — acquired; `defer func() { <-g.pace }()` holds the slot through the delay AND the forward (released when `ServeHTTP` returns). Then wait the pacing delay with its own select:
         - `case <-delayTimer.C:` where `delayTimer := time.NewTimer(delay)` with `defer delayTimer.Stop()` — the delay elapsed: increment the counter (`g.throttled.WithLabelValues(g.provider).Inc()`), log the paced line at V(4) (`glog.V(4).Infof("[throttle] provider=%s paced delay=%s", g.provider, delay)`), then call `g.forward(w, r)`.
         - `case <-r.Context().Done():` — client disconnected while waiting its pacing turn: return WITHOUT forwarding; the defer releases the pacing slot. This is spec Security "A client that disconnects while waiting for its pacing turn must never hold a pacing slot or be forwarded".
       - `case <-queueTimer.C:` where `queueTimer := time.NewTimer(g.maxDelay)` with `defer queueTimer.Stop()` — could not acquire a pacing slot within the max delay: the pacing queue is saturated; write the HTTP 429 (below) and return. 429 ONLY, never a 5xx.
       - `case <-r.Context().Done():` — client disconnected while waiting for a pacing slot: return without acquiring and without forwarding.
     - The pacing slot is held through the forward (not released before it) so a saturated queue deterministically sheds new arrivals with HTTP 429 instead of accumulating goroutines (spec DB 3 / Failure Mode "Request rate exceeds the pacing queue").
   - `forward(w, r)` (private method): wrap `w` in `rec := &statusRecorder{ResponseWriter: w}`, call `g.next.ServeHTTP(rec, r)`, then `g.observe(rec.status, g.now())`. The `statusRecorder` wrapper is REQUIRED by the spec constraint ("the gate's forward is wrapped in the existing statusRecorder") — its `Unwrap()` keeps SSE-safe flushing through `http.NewResponseController`. The upstream's 429 passes through unchanged (never retried); only the status feeds the detector.
   - `observe(status int, at libtime.DateTime)` (private method): the per-provider detector. All state transitions under `g.mu`:
     - Prune `g.window` to the 60s observation window: drop every timestamp `t` with `at.Time().Sub(t) >= throttleObservationWindow` (in-place filter over the backing array).
     - `status == http.StatusTooManyRequests`: append `at.Time()` to the window. If not throttled and `len(g.window) >= g.threshold`, enter throttle: `g.on = true`, `g.delay = throttleInitialDelay` then cap `if g.delay > g.maxDelay { g.delay = g.maxDelay }`, set `g.lastHalf = at.Time()`, and log `glog.Infof("[throttle] provider=%s state=on", g.provider)`. If already throttled, AIMD increase: `next := g.delay * throttleDelayMultiplier`; `if next > g.maxDelay || next < g.delay { g.delay = g.maxDelay } else { g.delay = next }` — the `next < g.delay` term catches int64 wraparound, so the ×2 is never applied past the cap (spec Constraints "Delay arithmetic is safe against overflow").
     - Any other status (clean response): if throttled AND `len(g.window) == 0` (the last 60s had no 429 — a clean window) AND `at.Time().Sub(g.lastHalf) >= throttleObservationWindow` (at least one clean window since the last halving), halve `g.delay /= throttleDelayDivisor`, set `g.lastHalf = at.Time()`; if `g.delay < throttleRecoveryFloor`, exit throttle: `g.on = false`, `g.delay = 0`, log `glog.Infof("[throttle] provider=%s state=off", g.provider)`.
     - The halving is gated on `lastHalf` so one clean window produces exactly one halving (spec DB 2 "each clean 60s window (no 429) halves it").
   - `Delay()` and `Throttled()` accessors on `*throttleGate` (mirroring the concurrency limiter's `InFlight` — exported methods on the unexported type, used by tests via interface type-assertion):

     ```go
     // Delay returns the current pacing delay (0 when not throttled). Only
     // valid on a real gate (threshold > 0); the no-op path returns next
     // unchanged and has no gate (mirrors the concurrency limiter's
     // InFlight accessor).
     func (g *throttleGate) Delay() stdtime.Duration {
     	g.mu.Lock()
     	defer g.mu.Unlock()
     	return g.delay
     }

     // Throttled reports whether the provider is currently throttled.
     func (g *throttleGate) Throttled() bool {
     	g.mu.Lock()
     	defer g.mu.Unlock()
     	return g.on
     }
     ```
   - The overflow 429 response is EXACTLY the Anthropic envelope, reusing the existing static `limiter429Body` (do NOT define a second body string):
     - `w.Header().Set("Content-Type", "application/json")`
     - `w.WriteHeader(http.StatusTooManyRequests)`
     - `_, _ = w.Write([]byte(limiter429Body))`
     - The body MUST NOT contain queue depth, the upstream URL, or the provider name (spec Security — `limiter429Body` is already the static generic message; do not interpolate anything into it).

4. **Metrics in `pkg/handler/metrics.go`.** Add one additive `CounterVec` (spec Desired Behavior 7 / AC 10; the `status_class` 7-value enum and `4xx_rate_limited` classification are untouched — do NOT modify `statusClass`):
   - Add to the `Metrics` struct: `ThrottledTotal *prometheus.CounterVec` (after `TokensTotal`).
   - In `NewMetrics`, construct it:
     ```go
     ThrottledTotal: prometheus.NewCounterVec(
     	prometheus.CounterOpts{
     		Name: "ccrouter_throttled_total",
     		Help: "Number of /v1/* requests delayed (paced) by the adaptive 429 delay gate before forwarding, labeled by provider. Paced = the request waited the pacing delay and was forwarded; overflow 429s and non-paced requests do not increment it.",
     	},
     	[]string{"provider"},
     ),
     ```
   - In `Register`, extend the collector loop to `[]prometheus.Collector{m.RequestsTotal, m.RequestDuration, m.AliasResolutions, m.TokensTotal, m.ThrottledTotal}`.
   - Do NOT pre-initialize per-provider series (matches `RequestsTotal`'s precedent — cardinality is bounded by real traffic). Do NOT add an `ObserveThrottled` method — the gate holds the CounterVec directly and calls `WithLabelValues(...).Inc()`.
   - Positive registration contract: a metrics Ginkgo row asserts `Register` on a fresh `prometheus.NewRegistry()` succeeds and that `ccrouter_throttled_total` is registered (e.g. `CollectAndCount(m.ThrottledTotal)` is callable without panic and gathering the registry yields the `ccrouter_throttled_total` family with no series) — locking the register-loop boundary so the new collector is actually exposed on `/metrics`.

5. **Test seam in `pkg/handler/export_test.go`.** Add two re-exports (package `handler`, following the existing pattern):
   ```go
   // ThrottleGateObserve drives the detector of a real throttle gate with a
   // single observed response status at the given clock time (spec 018): the
   // AIMD and recovery rows drive the detector directly so a growing pacing
   // delay never sleeps wall-clock. h must be a real gate (threshold > 0);
   // the disabled path returns next unchanged and has no gate.
   func ThrottleGateObserve(h http.Handler, status int, at libtime.DateTime) {
   	g, ok := h.(interface {
   		observe(status int, at libtime.DateTime)
   	})
   	if !ok {
   		panic("ThrottleGateObserve: handler is not a real throttle gate")
   	}
   	g.observe(status, at)
   }

   // ThrottleMaxPacedRequests exposes the bounded pacing-queue capacity so
   // the overflow row can saturate the queue deterministically (spec 018
   // DB 3).
   var ThrottleMaxPacedRequests = throttleMaxPacedRequests
   ```
   Add the import `libtime "github.com/bborbe/time"` to `export_test.go`.

6. **Factory wiring in `pkg/factory/factory.go`.**
   - Add a package-level constant near `defaultMaxConcurrentWaitSeconds`:
     ```go
     // defaultThrottleMaxDelaySeconds is the pacing-delay upper bound applied
     // when an enabled provider's throttleMaxDelaySeconds is absent, 0, or
     // negative (spec DB 4).
     const defaultThrottleMaxDelaySeconds = 30
     ```
   - Move `metrics := handler.NewMetrics(cfg.Aliases)` from its current position (after the per-provider loop) to BEFORE the per-provider loop (e.g. right after `var routes []handler.ModelRoute`), so the loop can pass `metrics.ThrottledTotal` into the gate. `NewMetrics` is side-effect-free (builds collectors, pre-initializes aliases) so moving it earlier is safe; the `metrics.Register(o.metricsRegisterer)` call and the `NewModelRouterWithPools(..., metrics, ...)` call stay where they are and keep referencing `metrics`.
   - In the per-provider loop, replace:
     ```go
     providerHandler := handler.NewUpstreamPoolHandler(ctx, members)
     providerHandlers[name] = providerHandler
     ```
     with:
     ```go
     providerHandler := handler.NewUpstreamPoolHandler(ctx, members)
     maxDelaySeconds := prov.ThrottleMaxDelaySeconds
     if maxDelaySeconds <= 0 {
     	maxDelaySeconds = defaultThrottleMaxDelaySeconds
     }
     providerHandler = handler.NewThrottleGate(
     	providerHandler,
     	name,
     	prov.Throttle429Threshold,
     	time.Duration(maxDelaySeconds)*time.Second,
     	o.currentDateTime.Now,
     	metrics.ThrottledTotal,
     )
     providerHandlers[name] = providerHandler
     ```
   - The `routes` loop already carries `Handler: providerHandler` (the wrapped value), and `defaultHandler` is read from `providerHandlers[cfg.Router.DefaultProvider]` (the wrapped value), so both glob-routed and default-provider traffic pass through the gate by construction — do NOT add a separate wrap for the default handler. For a disabled provider (`Throttle429Threshold <= 0`), `NewThrottleGate` returns the pool handler unchanged, so nothing else changes. `time` is already imported in `pkg/factory/factory.go`.

7. **Config tests in `pkg/config_test.go`** (package `pkg_test`, Ginkgo v2 + Gomega, using the existing `write()` helper and `pkgcfg.Load(context.Background(), p)`). Add a new `Context("throttle429Threshold")` block. These are yaml-boundary tests — a wrong tag would silently leave the field zero, so they MUST go through `Load`, not struct literals:
   - **AC 1 (load with both fields):** a provider block with `throttle429Threshold: 3` and `throttleMaxDelaySeconds: 30` loads; assert `cfg.Providers["x"].Throttle429Threshold == 3` and `.ThrottleMaxDelaySeconds == 30`.
   - **AC 1 (absent = identical to today):** a provider block with neither field loads; assert both fields are 0 and no error.
   - **AC 1 (partial):** only `throttle429Threshold: 3` set → loads; `Throttle429Threshold == 3`, `ThrottleMaxDelaySeconds == 0` (the 0 resolves to the 30s default at wiring, not at load).
   - **AC 2 (negative threshold):** `throttle429Threshold: -1` loads with no error; assert `Throttle429Threshold == -1` (the factory resolves ≤ 0 → disabled at wiring, requirement 6).
   - **AC 2 (negative max delay):** `throttleMaxDelaySeconds: -1` loads with no error; assert `ThrottleMaxDelaySeconds == -1` (the factory resolves ≤ 0 → 30s default at wiring, requirement 6).
   - **Boundary (0 is valid):** `throttle429Threshold: 0` and `throttleMaxDelaySeconds: 0` load with no error (disabled / default-resolved respectively).
   - Existing `Context("Load")` rows must still pass unchanged.

8. **Handler tests in a new `pkg/handler/throttle-gate_test.go`** (package `handler_test`, Ginkgo v2 + Gomega). Reuse the `serveAsync` pattern from `concurrency-limiter_test.go` and add:
   - A `newClock()` helper: `libtime.NewCurrentDateTime()` plus `clock.SetNow(libtime.DateTime(stdtime.Date(2026, 8, 27, 12, 0, 0, 0, stdtime.UTC)))` (the fixed `T0` used by all injected-clock rows; the recovery row advances it in 60s steps).
   - A `newCounterVec()` helper: `prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ccrouter_throttled_total", Help: "test"}, []string{"provider"})`.
   - A configurable `next` stub handler that records each invocation in an atomic counter, can return a configurable status + body, and can optionally block on a `release chan struct{}` after recording entry (selecting on `r.Context().Done()` too so teardown is leak-free) — shape it like `blockingHandler` in `concurrency-limiter_test.go` but with a status/body parameter.
   - A `throttled := func(h http.Handler) bool` / `delay := func(h http.Handler) stdtime.Duration` helper via interface type-assertion: `h.(interface{ Throttled() bool }).Throttled()` and `h.(interface{ Delay() stdtime.Duration }).Delay()`.
   - The local `expected429Body` constant (copy the one from `concurrency-limiter_test.go` — it equals `limiter429Body`; asserting exact equality is the security check).
   Rows:
   - **AC 3 — disabled is a byte-for-byte no-op:** `handler.NewThrottleGate(inner, "p", 0, stdtime.Second, clock.Now, cv)` must return `inner` unchanged (`Expect(gate).To(BeIdenticalTo(http.Handler(inner)))`); repeat with threshold `-1`. A request served through it reaches `inner` and returns 200 with no counter series (`testutil.CollectAndCount(cv) == 0`).
   - **AC 4 — throttle trigger + delay applied:** threshold 3, maxDelay 100ms. The `next` stub returns 429 for its first 3 invocations, then 200. Serve req1..req3 synchronously → each `rec.Code == 429` (passed through; not throttled yet). Assert `throttled(gate) == true` and `delay(gate) == 100ms` (entry 1s capped at maxDelay). Serve req4 in a goroutine → `Consistently` (~50ms) the stub's call count stays 3 (req4 held for the pacing delay), `Eventually` (~500ms) it reaches 4 and `rec4.Code == 200` — this row proves the sleep actually gates forwarding (the spec-mandated small explicit max delay).
   - **AC 5 — no router-side retry, 429 passed through unchanged:** engage throttle (threshold 3, maxDelay 50ms; drive 3× via `handler.ThrottleGateObserve(gate, 429, T0)` — no real waits). The `next` stub returns 429 with a distinctive body `{"type":"error","error":{"type":"rate_limit_error","message":"zai upstream says no"}}`. Serve one request in a goroutine → `Eventually` the stub call count is 1; assert `rec.Code == 429`, `rec.Body.String()` equals the distinctive upstream body EXACTLY (the router did not rewrite it, and it is not `limiter429Body`), and a `Consistently` (~200ms) window shows the stub call count stays 1 — the request was passed through, never re-sent.
   - **AC 6 — AIMD increase, bounded (no wall-clock sleep through the growing delay):** threshold 3, maxDelay 5s, fixed clock T0. Drive `handler.ThrottleGateObserve(gate, 429, T0)` three times → `throttled(gate) == true`, `delay(gate) == 1s`. Drive it once more → `delay(gate) == 2s`; once more → `4s`; drive it three more times → `delay(gate) == 5s` (capped at maxDelay, never exceeding it). Assert `delay(gate) == 5s`.
   - **AC 7 — recovery (clean window halves, below the floor exits):** threshold 3, maxDelay 5s, fixed clock T0. Drive 429×4 at T0 → throttled, `delay(gate) == 2s`. Drive `handler.ThrottleGateObserve(gate, 200, T0+60s)` → `delay(gate) == 1s`, still throttled (exactly one halving per clean window). Drive `handler.ThrottleGateObserve(gate, 200, T0+120s)` → `delay(gate) == 0`, `throttled(gate) == false`. Then serve one real request through the gate → it forwards immediately (stub call count reaches 1 promptly, `rec.Code == 200` — no pacing delay).
   - **AC 8 — overflow 429 (bounded pacing queue):** threshold 1, maxDelay 100ms; engage via one `handler.ThrottleGateObserve(gate, 429, T0)` (→ throttled, `delay(gate) == 100ms`). The `next` stub blocks on `release` (recording each entry). Fire `handler.ThrottleMaxPacedRequests + 1` (33) concurrent requests in goroutines, each with its own recorder + done channel. `Eventually` (~1s) exactly one done channel is closed and its recorder has `Code == 429`, `Content-Type` containing `application/json`, and `Body.String()` EXACTLY equal to `expected429Body` (the security check — no internal state leaks). Assert the entry count SEPARATELY with its own `Eventually(...).Should(Equal(32))` AFTER the 429 fires (decoupled from the 429-done close — the 32 slot-holders and the shed request reach their ~100ms deadlines simultaneously, so the entry count at the instant the 429's done closes is a race; the intent is that the shed request never reached the upstream, asserted by count 32 at steady state). Close `release` → `Eventually` all 33 done; the 32 remaining recorders have `Code == 200`.
   - **AC 9 — per-provider independence:** two independent gates A and B (threshold 3, maxDelay 100ms), separate stub upstreams. Throttle A via 3× `ThrottleGateObserve(429, T0)`; A's stub blocks on release. Fire an A request in a goroutine (held). Serve a B request synchronously → it completes immediately with 200, B's stub call count == 1, and A's stub call count stays 0 (B was never delayed or blocked by A's throttle). Close A's release; `Eventually` the A request completes.
   - **AC 10 — metric additive:** a gate with a fresh `newCounterVec()`, threshold 3, maxDelay 50ms, `next` stub returns 200. Drive 3× `ThrottleGateObserve(429, T0)` → throttled. Assert `testutil.CollectAndCount(cv) == 0` (no series before any paced request). Serve one real request → it waits the 50ms pacing delay, forwards, and `testutil.ToFloat64(cv.WithLabelValues("p")) == 1`. Serve a second → `== 2` — each paced request increments by exactly 1.
   - **Spec Security — client disconnects while waiting its pacing turn:** a throttled gate (threshold 1, maxDelay 50ms, engaged via `ThrottleGateObserve(429, T0)`). Serve a request with an already-cancelled context (`r = r.WithContext(cancelledCtx)`, mirroring the concurrency limiter's disconnect row) → `ServeHTTP` returns promptly and the stub's call count stays 0 (never forwarded, no pacing slot held). Also assert the gate still functions afterwards: serve one normal request → it forwards (stub call count 1).

9. **Wiring + reload tests in a new `pkg/factory/throttle_gate_wiring_test.go`** (package `factory_test`, same shape as `concurrency_limiter_wiring_test.go`: `makeConfig` building `*pkg.Config` programmatically, `newMessagesRequest`, `serveAsync`, `isolatedRegistry()`). The upstream is an `httptest.NewServer` handler that returns a configurable status (429 for these rows) and increments an atomic call count. A fixed injected clock `clock := libtime.NewCurrentDateTime(); clock.SetNow(...)` is passed via `factory.WithCurrentDateTime(clock)` (the gate's windowed counting uses it; a fixed clock means observations never expire — fine for these rows). Rows:
   - **AC 11 — reload / wiring (real dispatch boundary):** build via `CreateRouterFromConfig(ctx, cfg1, factory.WithMetricsRegisterer(reg), factory.WithCurrentDateTime(clock))` with `reg := prometheus.NewRegistry()` explicit (the isolated registerer is a contract of these rows — do NOT rely on the process-global default). cfg1: provider "t" with `Upstream: srv.URL`, `Models: ["m*"]`, `Throttle429Threshold: 1`, `ThrottleMaxDelaySeconds: 1`, `Router.DefaultProvider: "t"`. Build handler1. Serve req1 in a goroutine → `Eventually` upstream call count 1, `rec1.Code == 429` (the 1st 429 engages throttle; the request itself was forwarded before the observation). Serve req2 in a goroutine → `Consistently` (~300ms) upstream call count stays 1 (req2 held for the 1s pacing delay), `Eventually` (~2s) it reaches 2, `rec2.Code == 429` — the rebuilt-free tree paces through the real dispatch path. Build cfg2 — same provider key, `Throttle429Threshold: 2` (new threshold), `ThrottleMaxDelaySeconds: 1` — and handler2 via a SECOND `CreateRouterFromConfig` call (exactly the reloader's rebuild; throttle state is in-memory per provider and resets to not-throttled on rebuild, spec Failure Mode "Router restart / SIGHUP while throttled"). Serve req3 synchronously → it returns 429 promptly (upstream call count 3; with the NEW threshold of 2 the first 429 does NOT engage throttle, so req3 is not paced — a synchronous return proves no 1s delay). Serve req4 synchronously → 429 (upstream call count 4; count 2 ≥ 2 → throttled). Serve req5 in a goroutine → `Consistently` (~300ms) upstream call count stays 4, `Eventually` (~2s) it reaches 5, `rec5.Code == 429` — the rebuilt tree enforces the NEW threshold and still paces.
   - **AC 10 negative evidence — a 429 still records through the unchanged `4xx_rate_limited` path:** `reg := prometheus.NewRegistry()`; build a handler with `Throttle429Threshold: 1`, `ThrottleMaxDelaySeconds: 1`, upstream returning 429, via `CreateRouterFromConfig(ctx, cfg, factory.WithMetricsRegisterer(reg), factory.WithCurrentDateTime(clock))`. Serve one request → `rec.Code == 429` (the upstream's 429 passed through; not paced). Gather `families, err := reg.Gather()` and assert: the `ccrouter_requests_total` family contains a series whose `status_class` label equals `"4xx_rate_limited"` with counter value ≥ 1 (the unchanged classification path — no new status-class value), and the `ccrouter_throttled_total` family has NO series (the additive counter is untouched by a non-paced 429). This row's negative evidence is the spec-mandated check that the gate adds no new `status_class` value and no spurious throttled series.

   These rows use programmatic `*pkg.Config` values (matching the sibling wiring tests); the YAML→config boundary is covered by requirement 7, and the full YAML→SIGHUP→rebuild loop is already covered by the existing `pkg/reloader` suite — do not add a YAML-loading wiring test here.
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Config schema is fixed: `Provider` gains `throttle429Threshold int` (yaml `throttle429Threshold,omitempty`) and `throttleMaxDelaySeconds int` (yaml `throttleMaxDelaySeconds,omitempty`); zero-value semantics MUST remain current behavior. The knobs are read at provider level only — unlike `MaxConcurrentRequests` (per-upstream member caps), they are NOT copied into upstream members (spec Constraints). Do NOT add fields to the `Upstream` struct.
- Keying decision (spec Constraints): the gate wraps the per-provider pool handler (`NewUpstreamPoolHandler` result in `pkg/factory/factory.go`), so throttle state and knobs are per provider. A provider with an `upstreams:` pool is throttled as one unit; per-member throttle is out of scope.
- The gate sits between route dispatch and the upstream pool (wrap the `NewUpstreamPoolHandler` result); the model router's body-read, alias, `[1m]`-strip, key-routing, and system-lift flow are untouched. Do NOT modify `pkg/handler/model-router.go`, `pkg/handler/auth-middleware.go`, `main.go`, or `pkg/cli.go`.
- The gate's forward is wrapped in the existing `statusRecorder` (its `Unwrap()` keeps SSE-safe flushing through `http.NewResponseController`); the observed status feeds the per-provider detector. The pacing 429 reuses `limiter429Body` (`pkg/handler/concurrency-limiter.go:18`) — the same static generic message, no internal state (queue depth, upstream URL, provider name) leaked.
- Validation is lenient: a negative `throttle429Threshold` is treated as disabled (same as absent/0); a negative `throttleMaxDelaySeconds` is treated as the 30s default. No value fails `config.Load` (spec Constraints, AC 2).
- Fixed internal constants (documented defaults, not knobs — spec Non-goals): 60s observation window, 1s initial delay, ×2 / ÷2 AIMD multipliers, 1s recovery floor, and the bounded pacing-queue capacity (implementation choice; the observable contract is saturation → 429). Do NOT add config knobs, opt-out flags, or tunable thresholds beyond the two spec fields. Delay arithmetic is safe against overflow (×2 only applied while the delay is below the max).
- The gate never turns a provider off: a request to a throttled provider is always eventually forwarded (after the delay) or answered 429 on pacing-queue overflow — never dropped, never 5xx (spec Constraints).
- No new Prometheus `status_class` value: the `throttled` counter is a separate `CounterVec` labeled `{provider}`; `statusClass` is unchanged (spec Non-goals / AC 10).
- Per-provider independence: each gate's throttle state is strictly per provider; no shared/global throttle state (spec Non-goals).
- No router-side retry of the 429'd request — the response passes through unchanged (spec Non-goals). No hard circuit-breaker / provider disable (soft pacing only).
- No new dependencies — the Go standard library plus existing `bborbe/*` libs and `prometheus/client_golang` suffice (spec Constraints).
- Tests must not depend on real wall-clock waits beyond small explicit waits (50–200ms windows; the 100ms–1s pacing delays in the AC-mandated trigger/overflow/reload rows are the small explicit max delays the spec explicitly allows). Windowed 429 counting uses the injected clock (`WithCurrentDateTime`); AIMD/recovery rows assert the computed delay via the `Delay()` accessor and `ThrottleGateObserve` so they never sleep through growing delays.
- No AI attribution in code or comments.
- `make precommit` must remain green — run it before declaring done.
- Follow `docs/dod.md` (GoDoc on every new exported identifier; single-source-of-truth validation in `Config.Validate`).
- Do NOT touch `docs/` or `CHANGELOG.md` in this prompt — prompt 2 (`spec-018-adaptive-429-docs-changelog.md`) owns documentation.
</constraints>

<verification>
make precommit

# AC 1 — fields + yaml tags landed:
grep -n 'Throttle429Threshold\|throttle429Threshold' pkg/config.go
grep -n 'ThrottleMaxDelaySeconds\|throttleMaxDelaySeconds' pkg/config.go

grep -c 'must not be negative' pkg/config.go   # expect 0
grep -c 'must be positive' pkg/config.go       # expect 0

# Gate constructor + static 429 body reuse + log lines:
grep -n 'func NewThrottleGate' pkg/handler/throttle-gate.go
grep -n 'limiter429Body' pkg/handler/throttle-gate.go
grep -n '\[throttle\] provider=' pkg/handler/throttle-gate.go
grep -n 'func (g \*throttleGate) Delay\|func (g \*throttleGate) Throttled' pkg/handler/throttle-gate.go

# Metrics — additive counter, statusClass untouched:
grep -n 'ccrouter_throttled_total' pkg/handler/metrics.go
grep -c 'func statusClass' pkg/handler/metrics.go   # expect 1 (unchanged)
grep -c '4xx_rate_limited' pkg/handler/metrics.go   # expect >=1 (unchanged classification)

# Factory wraps every provider pool handler + resolves the 30s default:
grep -n 'NewThrottleGate\|defaultThrottleMaxDelaySeconds' pkg/factory/factory.go

# AC 1/2 — config test rows exist:
grep -c 'throttle429Threshold' pkg/config_test.go   # expect >=1

# Test seam present:
grep -n 'func ThrottleGateObserve\|ThrottleMaxPacedRequests' pkg/handler/export_test.go

# Full suite:
go test -mod=mod -count=1 ./pkg/...
</verification>

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"bytes"
	"net/http"
	"strconv"
	"sync"
	stdtime "time"

	libtime "github.com/bborbe/time"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// coldGateEstimateNumerator / coldGateEstimateDenominator encode the
	// fixed cost-estimate divisor 3.5 as integer arithmetic:
	// estimate = contentLength * 2 / 7 (no floating point).
	coldGateEstimateNumerator   = 2
	coldGateEstimateDenominator = 7
	// coldGateDefaultSessionWindow is the session window used when the
	// configured coldSessionWindowSeconds is absent, 0, or negative.
	coldGateDefaultSessionWindow = 600 * stdtime.Second
	// coldGateDeltaMarker is the SSE/JSON marker whose first appearance in
	// the response stream means prefill has finished.
	coldGateDeltaMarker = "content_block_delta"
	// coldGateRateBurst is the fixed burst of newly-seen session ids a
	// provider admits immediately before the refill applies (spec 019
	// DB 3). Fixed internal constant, not a config knob.
	coldGateRateBurst = 2
	// coldGateQueueCapacity is the fixed bounded queue capacity (spec 019
	// DB 4): at most this many cold requests wait; a request arriving when
	// the queue is full is refused immediately. Fixed internal constant,
	// not a config knob.
	coldGateQueueCapacity = 32
	// coldGateMaxWait is the fixed maximum queue wait before refusal
	// (spec 019 DB 4). Fixed internal constant, not a config knob.
	coldGateMaxWait = 30 * stdtime.Second
	// coldGateRetryAfterMinSeconds / coldGateRetryAfterMaxSeconds clamp
	// the integer Retry-After header value into [1, 60] (spec 019 DB 4).
	coldGateRetryAfterMinSeconds = 1
	coldGateRetryAfterMaxSeconds = 60
)

// ColdGateMetrics groups the four additive collectors the cold-start
// admission gate emits, each labeled by provider. A nil field disables
// that observation; the gate never dereferences a nil field.
type ColdGateMetrics struct {
	Delayed        *prometheus.CounterVec
	Refused        *prometheus.CounterVec
	TokensInFlight *prometheus.GaugeVec
	TTFT           *prometheus.HistogramVec
}

// NewColdStartGate returns a handler that admits a request whose session id
// was seen inside sessionWindow straight through — no wait, no reservation,
// no new behaviour for an established session. A request whose session id is
// absent, unknown, or last seen outside the window is "cold": it is charged a
// prefill cost estimate derived from its body size and admitted only while
// the provider's in-flight cold tokens plus its estimate stay within
// budgetTokens and while the provider's per-minute new-session rate allows
// it. A cold request that cannot be admitted waits in a bounded queue (at
// most coldGateQueueCapacity waiters) for up to maxWait; a request that
// cannot be admitted within that wait, or that arrives when the queue is
// full, is answered HTTP 429 with the static Anthropic-shaped rate_limit_error
// body (limiter429Body) and an integer Retry-After header clamped to [1, 60].
// No admission outcome produces a 5xx and no request is dropped without an
// answer. The reservation is released the moment the response stream emits
// its first content delta — the point at which prefill has finished — and
// also whenever the wrapped handler returns, so a stream end or an upstream
// error frees it too; a client that disconnects while waiting never holds a
// queue slot or a reservation.
//
// When budgetTokens and newSessionsPerMinute are both <= 0 the wrapper is a
// no-op and returns next unchanged — the request path is byte-for-byte
// identical to a release without the gate (feature-off default). Either check
// alone enables the gate. sessionWindow <= 0 resolves to
// coldGateDefaultSessionWindow; maxWait <= 0 resolves to coldGateMaxWait. now
// is the router's injected clock and falls back to the real clock when nil.
//
// The gate is per provider: a later prompt constructs one instance per
// upstream, immediately after the upstream pool handler and before the
// throttle gate, so the existing 429 delay gate stays outermost.
func NewColdStartGate(
	next http.Handler,
	provider string,
	budgetTokens int,
	sessionWindow stdtime.Duration,
	newSessionsPerMinute int,
	maxWait stdtime.Duration,
	now func() libtime.DateTime,
	metrics ColdGateMetrics,
) http.Handler {
	if budgetTokens <= 0 && newSessionsPerMinute <= 0 {
		return next
	}
	if sessionWindow <= 0 {
		sessionWindow = coldGateDefaultSessionWindow
	}
	if maxWait <= 0 {
		maxWait = coldGateMaxWait
	}
	// Resolve the nil-clock fallback before calling now(): now() on a nil
	// clock would panic when initializing the rate bucket below.
	if now == nil {
		now = realNow
	}
	return &coldStartGate{
		next:           next,
		provider:       provider,
		budget:         budgetTokens,
		window:         sessionWindow,
		rate:           newSessionsPerMinute,
		maxWait:        maxWait,
		now:            now,
		metrics:        metrics,
		seen:           make(map[string]stdtime.Time),
		notify:         make(chan struct{}),
		queue:          make(chan struct{}, coldGateQueueCapacity),
		rateTokens:     coldGateRateBurst,
		rateLastRefill: now().Time(),
	}
}

// coldStartGate is the per-provider cold-start admission gate. All mutable
// state lives under mu: the seen map, the in-flight token count, and the rate
// token bucket are mutated from concurrent request goroutines. notify is the
// broadcast channel waiters select on; broadcastLocked closes and replaces it
// on every state transition that can free budget or rate, so every waiter
// re-reads state after waking. queue is the bounded waiter semaphore — a
// buffered channel whose capacity bounds how many cold requests wait at once.
type coldStartGate struct {
	next     http.Handler
	provider string
	budget   int
	window   stdtime.Duration
	rate     int
	maxWait  stdtime.Duration
	now      func() libtime.DateTime
	metrics  ColdGateMetrics
	queue    chan struct{}

	mu             sync.Mutex
	seen           map[string]stdtime.Time
	inFlightTokens int
	rateTokens     int
	rateLastRefill stdtime.Time
	notify         chan struct{}
}

// InFlight returns the number of cold-prefill tokens currently reserved —
// the gate's in-flight cold token count. Only valid on a real gate
// (budgetTokens > 0); the disabled path returns next unchanged and has no
// gate (mirrors the concurrency limiter's InFlight accessor). Tests read
// this, not the Prometheus gauge.
func (g *coldStartGate) InFlight() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inFlightTokens
}

// ServeHTTP classifies the request from its context session id, admits it
// (waiting in the bounded queue when it is cold and the provider is saturated
// on budget or rate), and forwards it. A request that cannot be admitted is
// refused with HTTP 429 — a client that disconnects while waiting is dropped
// silently. A warm request — and a cold request whose body size yields a zero
// estimate — forwards directly with no reservation; a cold request with a
// non-zero estimate forwards through the release-observing wrapper.
func (g *coldStartGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sessionID := SessionIDFromContext(r.Context())
	estimate := g.estimate(r.ContentLength)
	admitted, warm, refuseReason := g.admit(r, sessionID, estimate)
	if refuseReason != "" {
		g.refuse(w, r, refuseReason)
		return
	}
	if !admitted {
		// Client disconnected while waiting: return without forwarding and
		// without holding a queue slot or a reservation.
		return
	}
	if warm || estimate == 0 {
		g.next.ServeHTTP(w, r)
		return
	}
	g.forwardCold(w, r, estimate)
}

// admit classifies and reserves under g.mu, waiting in the bounded queue
// until the request is admitted, its client disconnects, or the max wait
// elapses. It returns (true, true) for a warm request (forward with no
// reservation), (true, false) for an admitted cold request (the caller owns
// the reservation), (false, false, "") when the client disconnected while
// waiting, and (false, false, reason) with reason "queue_full" or "timeout"
// when the request must be refused. Every waiter re-reads the clock and the
// gate state after waking.
//
// The first admission attempt runs before any queue slot is acquired, so a
// request that can be admitted immediately is never refused queue_full. The
// queue slot is held only while waiting: it is released the moment the
// request is admitted, refused, or its context is done — never through the
// forward, so the queue bounds waiters, not in-flight work. A cold request
// admitted after waiting increments the delayed counter once; a request the
// warm re-check admits does not.
func (g *coldStartGate) admit(
	r *http.Request,
	sessionID string,
	estimate int,
) (admitted bool, warm bool, refuseReason string) {
	at := g.now().Time()
	g.mu.Lock()
	g.pruneLocked(at)
	admitted, warm, blockedReason := g.tryAdmitLocked(sessionID, estimate, at)
	if admitted {
		g.mu.Unlock()
		return true, warm, ""
	}
	slotHeld := false
	select {
	case g.queue <- struct{}{}:
		slotHeld = true
	default:
		g.mu.Unlock()
		return false, false, "queue_full"
	}
	defer func() {
		if slotHeld {
			<-g.queue
		}
	}()
	deadline := stdtime.NewTimer(g.maxWait)
	defer deadline.Stop()
	for {
		notify := g.notify
		g.mu.Unlock()
		select {
		case <-notify:
		case <-deadline.C:
			return false, false, "timeout"
		case <-r.Context().Done():
			return false, false, ""
		}
		at = g.now().Time()
		g.mu.Lock()
		g.pruneLocked(at)
		var blocked string
		admitted, warm, blocked = g.tryAdmitLocked(sessionID, estimate, at)
		if admitted {
			break
		}
		blockedReason = blocked
	}
	g.mu.Unlock()
	// A request admitted after waiting was delayed by the budget or the rate
	// (blockedReason is "budget" or "rate"); a request the warm re-check
	// admitted was not, and carries no charge. The same reason feeds the
	// delayed log line a later prompt adds.
	if !warm && blockedReason != "" && g.metrics.Delayed != nil {
		g.metrics.Delayed.WithLabelValues(g.provider).Inc()
	}
	<-g.queue
	slotHeld = false
	return true, warm, ""
}

// refuse answers a cold request the gate could not admit with HTTP 429, the
// static Anthropic-shaped rate_limit_error body, and an integer Retry-After
// header clamped to [1, 60]. It never writes a 5xx and never leaks internal
// state: the body is the existing limiter429Body constant and carries no
// queue depth, provider name, upstream URL, or session-identifying value. The
// refused counter is incremented once when configured.
//
//nolint:unparam // reason is consumed by the refusal log line a later prompt adds
func (g *coldStartGate) refuse(w http.ResponseWriter, r *http.Request, reason string) {
	// A client that already disconnected is not answered: writing would fail
	// harmlessly on the dead connection and counting it as refused would
	// overstate the refusal rate. The wait select races the deadline against
	// the request context, so a disconnect can land here.
	if r.Context().Err() != nil {
		return
	}
	if g.metrics.Refused != nil {
		g.metrics.Refused.WithLabelValues(g.provider).Inc()
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", strconv.Itoa(g.retryAfterSeconds()))
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte(limiter429Body))
}

// retryAfterSeconds is the integer Retry-After header value: the max wait in
// whole seconds, clamped into [coldGateRetryAfterMinSeconds,
// coldGateRetryAfterMaxSeconds], so the value is always an integer in 1–60 —
// even a sub-second test max wait yields 1.
func (g *coldStartGate) retryAfterSeconds() int {
	seconds := int(g.maxWait / stdtime.Second)
	if seconds < coldGateRetryAfterMinSeconds {
		seconds = coldGateRetryAfterMinSeconds
	}
	if seconds > coldGateRetryAfterMaxSeconds {
		seconds = coldGateRetryAfterMaxSeconds
	}
	return seconds
}

// tryAdmitLocked classifies and reserves in one step. Must be called with
// g.mu held. The reason is "" on admission, "budget" when the in-flight cold
// tokens plus the estimate exceed the budget, and "rate" when the per-minute
// new-session bucket has no token left.
//
// A warm session is re-checked first: when a concurrent first request for the
// same id was admitted while this one waited, this request is warm too —
// refresh the window and forward with no reservation and no charge.
//
// A cold request is admitted immediately when the provider has nothing cold
// in flight, and otherwise only while the in-flight cold tokens plus its
// estimate stay within the budget and the rate bucket allows it. Recording
// the id as seen happens only on admission, never at arrival, so a request
// that is never admitted does not mark its id as seen — that is what makes
// two concurrent first requests for one id cost one charge.
func (g *coldStartGate) tryAdmitLocked(
	sessionID string,
	estimate int,
	at stdtime.Time,
) (admitted bool, warm bool, reason string) {
	if sessionID != "" {
		if last, ok := g.seen[sessionID]; ok && at.Sub(last) < g.window {
			g.seen[sessionID] = at
			return true, true, ""
		}
	}
	if g.budget > 0 && g.inFlightTokens > 0 && g.inFlightTokens+estimate > g.budget {
		return false, false, "budget"
	}
	g.refillLocked(at)
	if g.rate > 0 && g.rateTokens < 1 {
		return false, false, "rate"
	}
	g.inFlightTokens += estimate
	if g.rate > 0 {
		g.rateTokens--
	}
	if sessionID != "" {
		g.seen[sessionID] = at
	}
	g.broadcastLocked()
	return true, false, ""
}

// refillLocked lazily refills the per-minute new-session token bucket. Must
// be called with g.mu held. It is a no-op when the rate is disabled. While
// the bucket is full it just advances the refill anchor; otherwise it credits
// the tokens earned since the anchor and advances the anchor by exactly the
// time those tokens represent, so repeated calls neither drift nor
// over-credit. A newly-seen session id consumes one token at admission.
func (g *coldStartGate) refillLocked(at stdtime.Time) {
	if g.rate <= 0 {
		return
	}
	if g.rateTokens >= coldGateRateBurst {
		g.rateLastRefill = at
		return
	}
	earned := int(at.Sub(g.rateLastRefill)) * g.rate / int(stdtime.Minute)
	if earned <= 0 {
		return
	}
	g.rateTokens += earned
	if g.rateTokens > coldGateRateBurst {
		g.rateTokens = coldGateRateBurst
	}
	g.rateLastRefill = g.rateLastRefill.Add(
		stdtime.Duration(earned) * stdtime.Minute / stdtime.Duration(g.rate),
	)
}

// pruneLocked drops every seen entry that has aged out of the session window
// so the map cannot grow without bound. Must be called with g.mu held.
func (g *coldStartGate) pruneLocked(at stdtime.Time) {
	for id, last := range g.seen {
		if at.Sub(last) >= g.window {
			delete(g.seen, id)
		}
	}
}

// broadcastLocked wakes every waiter so it re-reads the gate state. Must be
// called with g.mu held.
func (g *coldStartGate) broadcastLocked() {
	close(g.notify)
	g.notify = make(chan struct{})
}

// estimate derives the cold-prefill cost estimate from the request body size.
// A negative ContentLength means the size is unknown (chunked bodies) and
// yields 0; the arithmetic is integer-only, so the fixed 3.5 divisor is
// encoded as contentLength * 2 / 7 with no floating point.
func (g *coldStartGate) estimate(contentLength int64) int {
	if contentLength <= 0 {
		return 0
	}
	return int(contentLength * coldGateEstimateNumerator / coldGateEstimateDenominator)
}

// forwardCold forwards an admitted cold request through the release-observing
// recorder. The reservation is released on the first content delta (prefill
// finished) and, via the deferred release, whenever the handler returns —
// stream end or upstream error. sync.OnceFunc makes the two idempotent, so
// the reservation is freed exactly once. Warm requests never reach this path:
// they carry no reservation, no TTFT observation, and no gauge update.
func (g *coldStartGate) forwardCold(w http.ResponseWriter, r *http.Request, estimate int) {
	dispatchAt := g.now().Time()
	release := sync.OnceFunc(func() {
		g.mu.Lock()
		g.inFlightTokens -= estimate
		if g.inFlightTokens < 0 {
			g.inFlightTokens = 0
		}
		if g.metrics.TokensInFlight != nil {
			g.metrics.TokensInFlight.WithLabelValues(g.provider).Sub(float64(estimate))
		}
		g.broadcastLocked()
		g.mu.Unlock()
	})
	defer release()

	rec := &coldReleaseRecorder{
		ResponseWriter: w,
		onDelta: func() {
			release()
			if g.metrics.TTFT != nil {
				g.metrics.TTFT.
					WithLabelValues(g.provider).
					Observe(g.now().Time().Sub(dispatchAt).Seconds())
			}
		},
	}
	g.next.ServeHTTP(rec, r)
}

// coldReleaseRecorder wraps the response writer and calls onDelta exactly
// once, on the first Write carrying the content_block_delta marker — the
// point at which prefill has finished. Bytes pass through unmodified and
// Unwrap preserves the SSE-safe flush chain. A marker split across two
// Writes is best-effort undetected; the caller's deferred release still frees
// the reservation (matches ExtractUsage's best-effort SSE scan precedent).
type coldReleaseRecorder struct {
	http.ResponseWriter
	onDelta func()
	once    sync.Once
}

// Write writes the bytes through to the client first (the write-through path
// is unchanged, so SSE chunks flush at the same cadence), then scans them for
// the content-delta marker and fires onDelta once. The count and error come
// straight from the underlying writer.
func (c *coldReleaseRecorder) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	if bytes.Contains(b, []byte(coldGateDeltaMarker)) {
		c.once.Do(c.onDelta)
	}
	return n, err
}

// Unwrap returns the embedded ResponseWriter so http.NewResponseController
// (Go 1.20+) can recursively reach Flusher / Hijacker on the original writer.
// Required for SSE flush to pass through the wrapper.
func (c *coldReleaseRecorder) Unwrap() http.ResponseWriter {
	return c.ResponseWriter
}

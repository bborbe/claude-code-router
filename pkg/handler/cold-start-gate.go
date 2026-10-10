// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"bytes"
	"net/http"
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
// budgetTokens. A cold request that would exceed the budget waits until
// budget frees; it is never dropped and never answered with an error (the
// bounded queue and its 429 refusal are a later prompt). The reservation is
// released the moment the response stream emits its first content delta — the
// point at which prefill has finished — and also whenever the wrapped handler
// returns, so a stream end or an upstream error frees it too; a client that
// disconnects while waiting never holds a reservation.
//
// When budgetTokens is <= 0 the wrapper is a no-op and returns next unchanged
// — the request path is byte-for-byte identical to a release without the gate
// (feature-off default). sessionWindow <= 0 resolves to
// coldGateDefaultSessionWindow. now is the router's injected clock and falls
// back to the real clock when nil.
//
// The gate is per provider: a later prompt constructs one instance per
// upstream, immediately after the upstream pool handler and before the
// throttle gate, so the existing 429 delay gate stays outermost.
func NewColdStartGate(
	next http.Handler,
	provider string,
	budgetTokens int,
	sessionWindow stdtime.Duration,
	now func() libtime.DateTime,
	metrics ColdGateMetrics,
) http.Handler {
	if budgetTokens <= 0 {
		return next
	}
	if sessionWindow <= 0 {
		sessionWindow = coldGateDefaultSessionWindow
	}
	if now == nil {
		now = realNow
	}
	return &coldStartGate{
		next:     next,
		provider: provider,
		budget:   budgetTokens,
		window:   sessionWindow,
		now:      now,
		metrics:  metrics,
		seen:     make(map[string]stdtime.Time),
		notify:   make(chan struct{}),
	}
}

// coldStartGate is the per-provider cold-start admission gate. All mutable
// state lives under mu: the seen map and the in-flight token count are
// mutated from concurrent request goroutines. notify is the broadcast channel
// waiters select on; broadcastLocked closes and replaces it on every state
// transition that can free budget, so every waiter re-reads state after
// waking.
type coldStartGate struct {
	next     http.Handler
	provider string
	budget   int
	window   stdtime.Duration
	now      func() libtime.DateTime
	metrics  ColdGateMetrics

	mu             sync.Mutex
	seen           map[string]stdtime.Time
	inFlightTokens int
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
// (waiting for budget when it is cold and the provider is saturated), and
// forwards it. A warm request — and a cold request whose body size yields a
// zero estimate — forwards directly with no reservation; a cold request with
// a non-zero estimate forwards through the release-observing wrapper.
func (g *coldStartGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sessionID := SessionIDFromContext(r.Context())
	estimate := g.estimate(r.ContentLength)
	admitted, warm := g.admit(r, sessionID, estimate)
	if !admitted {
		// Client disconnected while waiting: return without forwarding and
		// without holding a reservation.
		return
	}
	if warm || estimate == 0 {
		g.next.ServeHTTP(w, r)
		return
	}
	g.forwardCold(w, r, estimate)
}

// admit classifies and reserves under g.mu, waiting on the broadcast channel
// until the request is admitted or its client disconnects. It returns
// (true, true) for a warm request (forward with no reservation), (true,
// false) for an admitted cold request (the caller owns the reservation), and
// (false, false) when the client disconnected while waiting. Every waiter
// re-reads the clock and the gate state after waking.
func (g *coldStartGate) admit(
	r *http.Request,
	sessionID string,
	estimate int,
) (admitted bool, warm bool) {
	at := g.now().Time()
	g.mu.Lock()
	g.pruneLocked(at)
	admitted, warm = g.tryAdmitLocked(sessionID, estimate, at)
	for !admitted {
		notify := g.notify
		g.mu.Unlock()
		select {
		case <-notify:
		case <-r.Context().Done():
			return false, false
		}
		at = g.now().Time()
		g.mu.Lock()
		g.pruneLocked(at)
		admitted, warm = g.tryAdmitLocked(sessionID, estimate, at)
	}
	g.mu.Unlock()
	return admitted, warm
}

// tryAdmitLocked classifies and reserves in one step. Must be called with
// g.mu held.
//
// A warm session is re-checked first: when a concurrent first request for the
// same id was admitted while this one waited, this request is warm too —
// refresh the window and forward with no reservation and no charge.
//
// A cold request is admitted immediately when the provider has nothing cold
// in flight, and otherwise only while the in-flight cold tokens plus its
// estimate stay within the budget. Recording the id as seen happens only on
// admission, never at arrival, so a request that is never admitted does not
// mark its id as seen — that is what makes two concurrent first requests for
// one id cost one charge.
func (g *coldStartGate) tryAdmitLocked(
	sessionID string,
	estimate int,
	at stdtime.Time,
) (admitted bool, warm bool) {
	if sessionID != "" {
		if last, ok := g.seen[sessionID]; ok && at.Sub(last) < g.window {
			g.seen[sessionID] = at
			return true, true
		}
	}
	if g.budget > 0 && g.inFlightTokens > 0 && g.inFlightTokens+estimate > g.budget {
		return false, false
	}
	g.inFlightTokens += estimate
	if sessionID != "" {
		g.seen[sessionID] = at
	}
	g.broadcastLocked()
	return true, false
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

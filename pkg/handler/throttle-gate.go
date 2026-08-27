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

// ServeHTTP runs entirely in the request goroutine: it snapshots the
// throttle state at entry, then either forwards immediately (not
// throttled) or paces the request — acquiring a bounded pacing slot and
// waiting the current delay before forwarding. A request that cannot
// acquire a pacing slot within maxDelay is answered HTTP 429 with the
// static Anthropic-shaped rate_limit_error body (limiter429Body), and a
// client that disconnects while waiting — for a slot or its pacing turn —
// returns without acquiring and without forwarding. The pacing slot is
// held through the delay AND the forward, so a saturated queue
// deterministically sheds new arrivals with HTTP 429 instead of
// accumulating goroutines.
func (g *throttleGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	on := g.on
	delay := g.delay
	g.mu.Unlock()
	if !on {
		g.forward(w, r)
		return
	}
	queueTimer := stdtime.NewTimer(g.maxDelay)
	defer queueTimer.Stop()
	select {
	case g.pace <- struct{}{}:
		// Pacing slot acquired; held through the delay AND the forward,
		// released when ServeHTTP returns.
		defer func() { <-g.pace }()
		delayTimer := stdtime.NewTimer(delay)
		defer delayTimer.Stop()
		select {
		case <-delayTimer.C:
			g.throttled.WithLabelValues(g.provider).Inc()
			glog.V(4).Infof("[throttle] provider=%s paced delay=%s", g.provider, delay)
			g.forward(w, r)
		case <-r.Context().Done():
			// Client disconnected while waiting its pacing turn: return
			// without forwarding; the defer releases the pacing slot.
		}
	case <-queueTimer.C:
		// Pacing queue saturated: could not acquire a slot within the max
		// delay. Answer HTTP 429 with the static generic body — never a 5xx,
		// never internal state.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(limiter429Body))
	case <-r.Context().Done():
		// Client disconnected while waiting for a pacing slot: return
		// without acquiring and without forwarding.
	}
}

// forward wraps w in the existing statusRecorder (its Unwrap keeps
// SSE-safe flushing through http.NewResponseController), calls the
// wrapped handler, then feeds the observed status to the per-provider
// detector. The upstream's 429 passes through unchanged — never retried —
// and only the status is observed to adjust future pacing.
func (g *throttleGate) forward(w http.ResponseWriter, r *http.Request) {
	rec := &statusRecorder{ResponseWriter: w}
	g.next.ServeHTTP(rec, r)
	g.observe(rec.status, g.now())
}

// observe is the per-provider detector: it maintains the 60s windowed
// count of upstream 429 responses and applies the AIMD delay transitions.
// All state transitions happen under g.mu. The branches are delegated to
// small helpers so the window/AIMD/recovery transitions stay legible.
func (g *throttleGate) observe(status int, at libtime.DateTime) {
	now := at.Time()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneWindow(now)
	if status == http.StatusTooManyRequests {
		g.observe429(now)
		return
	}
	g.observeClean(now)
}

// pruneWindow drops every 429 observation timestamp that has aged out of
// the 60s observation window (in-place filter over the backing array).
// Must be called with g.mu held.
func (g *throttleGate) pruneWindow(now stdtime.Time) {
	kept := g.window[:0]
	for _, t := range g.window {
		if now.Sub(t) < throttleObservationWindow {
			kept = append(kept, t)
		}
	}
	g.window = kept
}

// observe429 records a 429 observation and applies the AIMD increase: the
// first observation to cross the threshold enters throttle at the initial
// delay (capped at the max); every later 429 while throttled doubles the
// delay. Must be called with g.mu held.
func (g *throttleGate) observe429(now stdtime.Time) {
	g.window = append(g.window, now)
	if !g.on && len(g.window) >= g.threshold {
		g.enterThrottle(now)
		return
	}
	if g.on {
		g.increaseDelay()
	}
}

// enterThrottle starts pacing at the initial delay, capped at the max.
// Must be called with g.mu held.
func (g *throttleGate) enterThrottle(now stdtime.Time) {
	g.on = true
	g.delay = throttleInitialDelay
	if g.delay > g.maxDelay {
		g.delay = g.maxDelay
	}
	g.lastHalf = now
	glog.Infof("[throttle] provider=%s state=on", g.provider)
}

// increaseDelay applies the AIMD increase: each observed 429 while
// throttled doubles the delay, never past the max. The next < delay term
// catches int64 wraparound, so the ×2 is never applied past the cap. Must
// be called with g.mu held.
func (g *throttleGate) increaseDelay() {
	next := g.delay * throttleDelayMultiplier
	if next > g.maxDelay || next < g.delay {
		g.delay = g.maxDelay
	} else {
		g.delay = next
	}
}

// observeClean applies the AIMD decrease on a clean response: after a full
// 60s window with no 429 — and at least one clean window since the last
// halving — halve the delay (exactly one halving per clean window); below
// the recovery floor the provider exits throttle and forwards undelayed.
// Must be called with g.mu held.
func (g *throttleGate) observeClean(now stdtime.Time) {
	if !g.on || len(g.window) != 0 || now.Sub(g.lastHalf) < throttleObservationWindow {
		return
	}
	g.delay /= throttleDelayDivisor
	g.lastHalf = now
	if g.delay < throttleRecoveryFloor {
		g.on = false
		g.delay = 0
		glog.Infof("[throttle] provider=%s state=off", g.provider)
	}
}

// Delay returns the current pacing delay (0 when not throttled). Only
// valid on a real gate (threshold > 0); the no-op path returns next
// unchanged and has no gate (mirrors the concurrency limiter's InFlight
// accessor).
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

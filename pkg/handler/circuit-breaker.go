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
	// breakerOpenPeriod is the fixed duration the breaker stays open before
	// admitting a single probe. Fixed internal constant, not a config knob.
	breakerOpenPeriod = 30 * stdtime.Second
	// breakerOpenPeriodSeconds is the integer Retry-After header value sent
	// while the breaker is open (the open period in whole seconds).
	breakerOpenPeriodSeconds = int(breakerOpenPeriod / stdtime.Second)
)

// breakerState is the circuit breaker's state.
type breakerState int

const (
	// breakerClosed forwards every request and counts consecutive failures.
	breakerClosed breakerState = iota
	// breakerOpen refuses every request for the fixed open period.
	breakerOpen
	// breakerHalfOpen admits exactly one probe and refuses the rest.
	breakerHalfOpen
)

// breakerStateLabels are the bounded state metric label values, in order.
// The metric label uses an underscore (half_open) while the log line uses a
// hyphen (half-open).
var breakerStateLabels = []string{"closed", "open", "half_open"}

// NewCircuitBreaker returns a handler that, when failureThreshold > 0,
// counts consecutive upstream failures for the wrapped provider and opens
// the breaker once the count reaches the threshold (spec 020): while open,
// every request is answered HTTP 429 with the existing static
// Anthropic-shaped rate_limit_error body and an integer Retry-After header
// equal to the fixed open period, and the upstream is never invoked. The
// router never answers a client with a 5xx of its own making. When the open
// period elapses the breaker admits exactly one probe request while
// concurrent requests are answered 429; a probe whose response is not a
// failure closes the breaker and resets the count, and a failing probe
// reopens it for a further open period. A non-5xx response resets the
// consecutive-failure count. When failureThreshold <= 0 the wrapper is a
// no-op and returns next unchanged — the request path is byte-for-byte
// identical to a release without the breaker (feature-off default).
//
// now is the router's injected clock and falls back to the real clock when
// nil. transitions, when non-nil, counts state transitions labeled by
// provider and state, with its three series pre-initialized to 0.
func NewCircuitBreaker(
	next http.Handler,
	provider string,
	failureThreshold int,
	now func() libtime.DateTime,
	transitions *prometheus.CounterVec,
) http.Handler {
	if failureThreshold <= 0 {
		return next
	}
	if now == nil {
		now = realNow
	}
	// Pre-initialize the three bounded series so a `rate(...) > 0` alert
	// evaluates to 0 rather than no-data for a provider that has never
	// transitioned (matches the alias pre-initialization in NewMetrics).
	if transitions != nil {
		for _, s := range breakerStateLabels {
			transitions.WithLabelValues(provider, s).Add(0)
		}
	}
	return &circuitBreaker{
		next:        next,
		provider:    provider,
		threshold:   failureThreshold,
		now:         now,
		transitions: transitions,
	}
}

// circuitBreaker is the per-provider consecutive-failure gate. Every field
// below mu is mutated from concurrent request goroutines — the state, the
// failure count, the open timestamp, and the probe flag — so every
// read-modify-write takes mu. The immutable configuration fields sit above
// it.
type circuitBreaker struct {
	next        http.Handler
	provider    string
	threshold   int
	now         func() libtime.DateTime
	transitions *prometheus.CounterVec

	mu       sync.Mutex
	state    breakerState
	failures int
	openedAt stdtime.Time
	probing  bool
}

// State returns the breaker's current state as a metric-style label value
// ("closed", "open", or "half_open"). Only valid on a real breaker
// (failureThreshold > 0); the no-op path returns next unchanged and has no
// breaker (mirrors the concurrency limiter's InFlight accessor).
func (b *circuitBreaker) State() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return breakerStateLabel(b.state)
}

// ServeHTTP admits or refuses the request, then observes the forwarded
// status. A refused request is answered with the shared 429 — never a 5xx,
// and the upstream is never invoked. An admitted request is forwarded
// through the existing statusRecorder (its Unwrap keeps SSE flushing
// working) so the response status at the provider boundary drives the
// failure count.
func (b *circuitBreaker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	admitted, probe := b.admit(b.now().Time())
	if !admitted {
		writeRateLimited(w, breakerOpenPeriodSeconds)
		return
	}
	sr := &statusRecorder{ResponseWriter: w}
	// observe runs only on the normal path; a panic unwinding out of the
	// wrapped handler would otherwise leave the probe flag set forever and
	// wedge the breaker in half-open, answering 429 until the process
	// restarts. The deferred release is idempotent — observe has already
	// cleared the flag on the normal path — so a panic leaves the breaker
	// state untouched rather than reading a router bug as an upstream verdict.
	if probe {
		defer b.releaseProbe()
	}
	b.next.ServeHTTP(sr, r)
	b.observe(sr.status, probe, b.now().Time())
}

// admit decides whether the request may reach the upstream and whether it is
// the half-open probe. It never runs with b.mu held.
//
// While open and inside the open period the request is refused. Once the
// period has elapsed the breaker transitions to half-open and this request
// becomes the single probe. While half-open, a request arriving with a probe
// already in flight is refused, so exactly one request reaches the upstream
// per open→half-open transition.
func (b *circuitBreaker) admit(at stdtime.Time) (admitted bool, probe bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case breakerOpen:
		if at.Sub(b.openedAt) < breakerOpenPeriod {
			return false, false
		}
		b.setStateLocked(breakerHalfOpen, at)
		b.probing = true
		return true, true
	case breakerHalfOpen:
		if b.probing {
			return false, false
		}
		b.probing = true
		return true, true
	default:
		return true, false
	}
}

// observe folds one forwarded response into the breaker state. It never runs
// with b.mu held. The failure signal is the response status at the provider
// boundary: a 5xx counts, anything below 500 does not, and a client
// disconnect never arrives as a 5xx so it is never counted.
//
// A probe's own verdict is authoritative: it clears the probe flag and either
// closes the breaker (resetting the count) or reopens it for a further open
// period. A non-probe response is ignored once the state has left closed, so
// a slow request that returns after the breaker has already opened cannot
// drive it. A status-0 response — a client disconnect, or an inner gate that
// wrote nothing — is neither a failure nor evidence of health, so it leaves
// the count untouched.
func (b *circuitBreaker) observe(status int, probe bool, at stdtime.Time) {
	failed := status >= 500
	b.mu.Lock()
	defer b.mu.Unlock()
	if probe {
		b.probing = false
		if failed {
			b.failures = b.threshold
			b.setStateLocked(breakerOpen, at)
			return
		}
		b.failures = 0
		b.setStateLocked(breakerClosed, at)
		return
	}
	if b.state != breakerClosed {
		return
	}
	if status == 0 {
		return
	}
	if status < 500 {
		b.failures = 0
		return
	}
	b.failures++
	if b.failures >= b.threshold {
		b.setStateLocked(breakerOpen, at)
	}
}

// releaseProbe clears the half-open probe flag without changing the breaker
// state, so a probe whose forwarded request never returned leaves the breaker
// half-open and the next request becomes the probe.
func (b *circuitBreaker) releaseProbe() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
}

// setStateLocked assigns the new state and emits its observable effects: on
// entering open it records the open timestamp and clears the probe flag, it
// prints exactly one INFO line naming the provider and the state, and it
// increments the transition counter when one is configured. A transition to
// the current state is a no-op. Must be called with b.mu held.
func (b *circuitBreaker) setStateLocked(state breakerState, at stdtime.Time) {
	if b.state == state {
		return
	}
	b.state = state
	if state == breakerOpen {
		b.openedAt = at
		b.probing = false
	}
	glog.Infof("[breaker] provider=%s state=%s", b.provider, breakerStateLogName(state))
	if b.transitions != nil {
		b.transitions.WithLabelValues(b.provider, breakerStateLabel(state)).Inc()
	}
}

// breakerStateLogName maps the state to its hyphenated log token
// ("half-open"), the spelling the log line uses.
func breakerStateLogName(state breakerState) string {
	switch state {
	case breakerOpen:
		return "open"
	case breakerHalfOpen:
		return "half-open"
	default:
		return "closed"
	}
}

// breakerStateLabel maps the state to its underscored metric label value
// ("half_open"), the spelling the transition counter uses.
func breakerStateLabel(state breakerState) string {
	switch state {
	case breakerOpen:
		return "open"
	case breakerHalfOpen:
		return "half_open"
	default:
		return "closed"
	}
}

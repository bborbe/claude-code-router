// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang/glog"
)

// TraceTTLDefault is the production TTL window for trace enable.
// The TTL timer turns tracing off automatically after this duration.
// The 5-minute constant is a frozen invariant; TRACE_TTL is a
// test-only override and is never read by production code.
const TraceTTLDefault = 5 * time.Minute

// TraceState owns the process-global trace-enabled flag and its TTL
// timer. It guarantees exactly one live timer at any time: Enable()
// cancels the prior timer before starting a fresh window, and Disable()
// cancels the in-flight timer so no concurrent expiry can re-enable
// tracing. The flag is atomic for lock-free IsEnabled() reads on the
// request hot path.
type TraceState struct {
	enabled atomic.Bool
	mu      sync.Mutex
	timer   *time.Timer
	ttl     time.Duration
}

// NewTraceState returns a TraceState whose TTL window is the production
// constant TraceTTLDefault (5 minutes). Tests use NewTraceStateWithTTL
// with a shorter window.
func NewTraceState() *TraceState {
	return NewTraceStateWithTTL(TraceTTLDefault)
}

// NewTraceStateWithTTL returns a TraceState whose TTL window is ttl.
// Tests use a short ttl for fast expiry assertions; production uses
// NewTraceState (TraceTTLDefault = 5 minutes).
func NewTraceStateWithTTL(ttl time.Duration) *TraceState {
	ts := &TraceState{
		ttl: ttl,
	}
	ts.enabled.Store(false)
	return ts
}

// traceTTLFromEnv reads TRACE_TTL and returns the parsed duration, or
// returns TraceTTLDefault if unset or unparseable. Intended for tests
// that want to shorten the window without touching the production
// constructor. Production code calls NewTraceState() which always
// uses TraceTTLDefault.
func traceTTLFromEnv() time.Duration {
	val := os.Getenv("TRACE_TTL")
	if val == "" {
		return TraceTTLDefault
	}
	d, err := time.ParseDuration(val)
	if err != nil {
		return TraceTTLDefault
	}
	return d
}

// Enable sets the trace-enabled flag to true, cancels any in-flight TTL
// timer, and starts a fresh TTL window. Repeated calls are idempotent on
// the flag but always reset the window to a fresh full ttl.
func (ts *TraceState) Enable() {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if ts.timer != nil {
		ts.timer.Stop()
	}
	ts.enabled.Store(true)
	ts.timer = time.AfterFunc(ts.ttl, ts.disableForExpiry)
	glog.V(2).Infof("trace enabled via endpoint")
}

// disableForExpiry is called by the TTL timer when it fires. It calls
// Disable() through the internal path so the mutex serializes the
// cancel-and-clear sequence shared with the handler-facing Disable().
func (ts *TraceState) disableForExpiry() {
	ts.Disable()
	glog.V(2).Infof("trace ttl expired")
}

// Disable sets the trace-enabled flag to false and cancels any in-flight
// TTL timer so no later expiry can flip tracing back on.
func (ts *TraceState) Disable() {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if ts.timer != nil {
		ts.timer.Stop()
		ts.timer = nil
	}
	ts.enabled.Store(false)
	glog.V(2).Infof("trace disabled via endpoint")
}

// IsEnabled returns the current trace-enabled flag value. This is the
// hot path consulted per request by the trace middleware; it performs
// an atomic load without locking.
func (ts *TraceState) IsEnabled() bool {
	return ts.enabled.Load()
}

// defaultTraceState is the process-global trace-state instance consulted
// by the /enabletrace + /disabletrace handlers and by NewTraceMiddleware's
// per-request IsEnabled() check. It is initialized once at package load;
// a restart resets it to off (no persistence). Tests use
// NewTraceStateWithTTL to build an isolated instance instead of mutating
// the process-global default.
var defaultTraceState = NewTraceState()

// DefaultTraceState returns the process-global trace-state instance.
func DefaultTraceState() *TraceState {
	return defaultTraceState
}

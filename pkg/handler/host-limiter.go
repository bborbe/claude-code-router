// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"net/http"
	"sync/atomic"
	"time"
)

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
func NewHostLimiter(maxConcurrentRequests int, maxConcurrentWait time.Duration) *HostLimiter {
	h := &HostLimiter{wait: maxConcurrentWait}
	if maxConcurrentRequests > 0 {
		h.sem = make(chan struct{}, maxConcurrentRequests)
	}
	return h
}

// Wrap returns next guarded by this host's shared budget. Every handler
// returned by Wrap on the same HostLimiter shares one budget.
func (h *HostLimiter) Wrap(next http.Handler) http.Handler {
	if h.sem == nil {
		return &hostInFlightCounter{next: next, inFlight: &h.inFlight}
	}
	return &concurrencyLimiter{next: next, sem: h.sem, wait: h.wait}
}

// InFlight returns the host's current in-flight request count: the
// shared semaphore's occupancy when capped, the live in-flight count
// when unlimited.
func (h *HostLimiter) InFlight() int {
	if h.sem != nil {
		return len(h.sem)
	}
	return int(h.inFlight.Load())
}

// hostInFlightCounter is the unlimited-host wrapper: it counts the
// requests passing through without queueing, timing out, or rewriting
// the response. It runs in the request goroutine and does not wrap w, so
// SSE flushing is untouched.
type hostInFlightCounter struct {
	next     http.Handler
	inFlight *atomic.Int32
}

// ServeHTTP increments the host's in-flight count, forwards the request,
// and decrements on return. The defer guarantees the count is released
// even if next panics.
func (c *hostInFlightCounter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.inFlight.Add(1)
	defer c.inFlight.Add(-1)
	c.next.ServeHTTP(w, r)
}

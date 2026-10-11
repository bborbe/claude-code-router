// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

// limiter429Body is the Anthropic-shaped rate_limit_error envelope written
// when a queued request cannot acquire a slot within the wait window. The
// shape is load-bearing: the client parses it to trigger its own backoff
// retry. The message is deliberately static and generic — it must not carry
// queue depth, the upstream URL, or the provider name (no internal state
// leaked).
const limiter429Body = `{"type":"error","error":{"type":"rate_limit_error","message":"too many concurrent requests, please retry"}}`

// writeRateLimited answers a request the gate could not admit: HTTP 429 with
// the static Anthropic-shaped limiter429Body. It never writes a 5xx and never
// leaks internal state — the body is a fixed constant carrying no queue depth,
// provider name, upstream URL, or session-identifying value.
//
// retryAfterSeconds > 0 adds an integer Retry-After header; 0 omits it, which
// is the shape the concurrency limiter and the throttle gate have always used
// and which the cold-start gate overrides with its clamped 1–60. Shared by all
// three so the envelope and the header write cannot drift apart.
func writeRateLimited(w http.ResponseWriter, retryAfterSeconds int) {
	w.Header().Set("Content-Type", "application/json")
	if retryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds))
	}
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte(limiter429Body))
}

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
// InFlight reports the semaphore occupancy — the per-upstream in-flight
// count the upstream pool handler's least-loaded selection reads — and
// Queued reports how many requests are blocked waiting for one of those
// slots. Together they split the dispatch path into what is running and
// what is waiting.
// maxConcurrentWait must be > 0; the factory resolves the 30s default
// (spec DB 5) before constructing.
func NewConcurrencyLimiter(
	next http.Handler,
	maxConcurrentRequests int,
	maxConcurrentWait time.Duration,
) http.Handler {
	if maxConcurrentRequests <= 0 {
		return next
	}
	return &concurrencyLimiter{
		next: next,
		sem:  make(chan struct{}, maxConcurrentRequests),
		wait: maxConcurrentWait,
	}
}

type concurrencyLimiter struct {
	next http.Handler
	sem  chan struct{}
	wait time.Duration
	// waiters counts requests currently blocked waiting for a slot — those
	// that found every slot held and have not yet acquired one. Incremented
	// on entering the queued path and decremented on every exit from it, so
	// a request that takes a free slot immediately is never counted.
	waiters atomic.Int64
}

// InFlight returns the number of slots currently held — the number of
// requests this limiter is currently serving. It is the per-upstream
// in-flight count the pool handler's least-loaded selection reads
// (spec 012 DB 4). Only valid on a real limiter (constructed with
// maxConcurrentRequests > 0); the <= 0 no-op path returns next
// unchanged and has no limiter, and the pool handler treats a missing
// counter as 0.
func (l *concurrencyLimiter) InFlight() int {
	return len(l.sem)
}

// Queued returns the number of requests currently blocked waiting for a
// slot. It is the counterpart to InFlight: a request holding a slot is
// dispatched, a request that found every slot held is queued, and no
// request is both. It reads 0 for a limiter that is not saturated, and
// is only meaningful on a real limiter — the <= 0 no-op path returns
// next unchanged and has no limiter, so callers treat a missing counter
// as 0 exactly as they do for InFlight.
func (l *concurrencyLimiter) Queued() int {
	return int(l.waiters.Load())
}

// ServeHTTP runs entirely in the request goroutine: it acquires a slot
// from the buffered-channel semaphore, then calls next.ServeHTTP
// synchronously in the same goroutine, releasing the slot with a defer
// exactly when the upstream round-trip (including an SSE stream) returns.
// A slot is never acquired for a client that disconnects while queued, so
// no concurrency is lost to dead connections.
//
// A request that finds a free slot takes it on the first, non-blocking
// attempt and is never counted as queued; only a request that finds every
// slot held enters the waiting path, where it is counted by Queued until
// it acquires a slot, times out, or its client disconnects.
func (l *concurrencyLimiter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case l.sem <- struct{}{}:
		defer func() { <-l.sem }()
		l.next.ServeHTTP(w, r)
		return
	default:
	}

	l.waiters.Add(1)
	defer l.waiters.Add(-1)

	timer := time.NewTimer(l.wait)
	defer timer.Stop()
	select {
	case l.sem <- struct{}{}:
		defer func() { <-l.sem }()
		l.next.ServeHTTP(w, r)
	case <-timer.C:
		writeRateLimited(w, 0)
	case <-r.Context().Done():
		// Client disconnected while queued: return without acquiring a
		// slot and without forwarding. No response write is needed — the
		// write would fail harmlessly on the dead connection.
	}
}

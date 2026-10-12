// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	libtime "github.com/bborbe/time"
	"github.com/prometheus/client_golang/prometheus"
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
// four so the envelope and the header write cannot drift apart.
//
// reason is the gate's own cause (one of the rejectionReason* constants) and is
// recorded on the request's shared slot before the write, so the router's
// single post-dispatch observation point can attribute the 429. It is a
// required parameter rather than an optional one on purpose: every caller
// already knows which gate it is, so making the reason positional means a
// future 429 site cannot be added without naming its cause — the alternative,
// each gate incrementing the counter itself, is what makes a nested-gate
// double count possible.
func writeRateLimited(
	w http.ResponseWriter,
	r *http.Request,
	retryAfterSeconds int,
	reason string,
) {
	recordRejection(r, reason)
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
//
// provider labels the queue-wait histogram; queueWait, when non-nil, is
// observed with how long a request waited for a slot — on the acquire path
// and on the timeout path, but never for a request that took a free slot on
// its first attempt, because that request never queued and observing it
// would fold service-free requests into a distribution meant to measure
// contention. A nil queueWait disables the observation without changing the
// request path, so a caller with no metrics wiring (tests) behaves exactly
// as before.
//
// now is the router's injected clock and falls back to realNow when nil, the
// same contract NewThrottleGate and NewCircuitBreaker use. It is injected
// rather than read directly because production code outside main.go must not
// call time.Now() — a queue wait measured from an uninjected clock cannot be
// driven from a test. The wait is still an elapsed-time measurement, so the
// injected getter is read twice and subtracted rather than compared to any
// absolute instant.
//
// waiter times the queue wait: a queued request is refused once
// waiter.Wait(ctx, maxConcurrentWait) returns nil. It is required — the
// factory passes libtime.NewWaiterDuration() — so a test can substitute the
// counterfeiter WaiterDuration mock and decide exactly when a queued request
// times out, instead of sleeping through a real timer.
func NewConcurrencyLimiter(
	next http.Handler,
	provider string,
	maxConcurrentRequests int,
	maxConcurrentWait time.Duration,
	waiter libtime.WaiterDuration,
	now func() libtime.DateTime,
	queueWait *prometheus.HistogramVec,
) http.Handler {
	if maxConcurrentRequests <= 0 {
		return next
	}
	if now == nil {
		now = realNow
	}
	if waiter == nil {
		// A nil waiter would panic on the first queued request inside the
		// waiter goroutine, where net/http's recover does not reach — it
		// would take the whole process down, and only under load.
		waiter = libtime.NewWaiterDuration()
	}
	return &concurrencyLimiter{
		next:         next,
		provider:     provider,
		sem:          make(chan struct{}, maxConcurrentRequests),
		wait:         maxConcurrentWait,
		waiter:       waiter,
		now:          now,
		queueWait:    queueWait,
		rejectReason: rejectionReasonQueueTimeout,
	}
}

type concurrencyLimiter struct {
	next     http.Handler
	provider string
	sem      chan struct{}
	wait     time.Duration
	// waiter times the queue wait (see NewConcurrencyLimiter); never nil.
	waiter libtime.WaiterDuration
	// rejectReason is the ccrouter_rejected_total reason recorded when a
	// queued request times out: rejectionReasonQueueTimeout for a member or
	// provider limiter, rejectionReasonHostQueueTimeout for a host limiter
	// built by HostLimiter.Wrap. The type is shared, so the reason has to
	// travel with the instance for the two to stay distinguishable.
	rejectReason string
	// now is the injected clock the queue-wait measurement reads; never nil
	// (the constructor resolves realNow).
	now func() libtime.DateTime
	// queueWait is the ccrouter_queue_wait_seconds histogram, or nil when
	// the caller wired no metrics.
	queueWait *prometheus.HistogramVec
	// waiters counts requests currently blocked waiting for a slot — those
	// that found every slot held and have not yet acquired one. Incremented
	// on entering the queued path and decremented on every exit from it, so
	// a request that takes a free slot immediately is never counted.
	waiters atomic.Int64
}

// timeoutReason is the reason recorded for a queue timeout. A limiter built
// as a struct literal that forgot rejectReason would otherwise record ""
// — which recordRejection treats as unclaimed, so the router would count the
// router's own 429 as reason="upstream". Falling back to queue_timeout keeps
// every limiter timeout attributed to the router.
func (l *concurrencyLimiter) timeoutReason() string {
	if l.rejectReason == "" {
		return rejectionReasonQueueTimeout
	}
	return l.rejectReason
}

// observeQueueWait records how long a request spent in the waiting path.
// A nil histogram (no metrics wiring) makes this a no-op.
func (l *concurrencyLimiter) observeQueueWait(waited time.Duration) {
	if l.queueWait == nil {
		return
	}
	l.queueWait.WithLabelValues(l.provider).Observe(waited.Seconds())
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
	// queuedAt is read from the injected clock and only ever subtracted from
	// another reading of it, so the measurement is an elapsed duration and
	// never depends on the clock's absolute value.
	queuedAt := l.now().Time()

	// The waiter runs in its own goroutine so the wait can race the
	// semaphore in one select. waitCtx is cancelled as soon as the select
	// returns, so the waiter goroutine ends with the queued phase rather than
	// outliving it for the length of the upstream round-trip. timedOut is
	// closed when the wait ended while the queued phase is still live — a
	// completed wait (nil error), or a waiter failing on its own, which is
	// refused rather than left to wait without a bound. A waiter ended by
	// cancellation or client disconnect (waitCtx done) never reads as a
	// timeout. The deferred cancel is a backstop; every select arm also
	// cancels explicitly so the waiter stops before the upstream round-trip.
	waitCtx, cancelWait := context.WithCancel(r.Context())
	defer cancelWait()
	timedOut := make(chan struct{})
	go func() {
		err := l.waiter.Wait(waitCtx, libtime.Duration(l.wait))
		if err == nil || waitCtx.Err() == nil {
			close(timedOut)
		}
	}()
	select {
	case l.sem <- struct{}{}:
		cancelWait()
		l.observeQueueWait(l.now().Time().Sub(queuedAt))
	case <-timedOut:
		cancelWait()
		l.waiters.Add(-1)
		l.observeQueueWait(l.now().Time().Sub(queuedAt))
		writeRateLimited(w, r, 0, l.timeoutReason())
		return
	case <-r.Context().Done():
		// Client disconnected while queued: return without acquiring a
		// slot and without forwarding. No response write is needed — the
		// write would fail harmlessly on the dead connection. The wait is
		// not observed either: the request was neither admitted nor
		// refused by the limiter, so it belongs in neither the queue-wait
		// distribution nor the rejection counter.
		cancelWait()
		l.waiters.Add(-1)
		return
	}

	// The slot is held now, so the request has stopped waiting and must
	// stop counting as queued BEFORE the handler runs. Decrementing on
	// return instead would keep it counted for its whole service duration,
	// which under saturation is exactly the condition this gauge exists to
	// measure — queued would then over-report by roughly the number of
	// slot-holders admitted through this path, and InFlight and Queued
	// would no longer be disjoint.
	l.waiters.Add(-1)
	defer func() { <-l.sem }()
	l.next.ServeHTTP(w, r)
}

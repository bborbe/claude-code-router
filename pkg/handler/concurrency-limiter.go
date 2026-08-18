// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"net/http"
	"time"
)

// limiter429Body is the Anthropic-shaped rate_limit_error envelope written
// when a queued request cannot acquire a slot within the wait window. The
// shape is load-bearing: the client parses it to trigger its own backoff
// retry. The message is deliberately static and generic — it must not carry
// queue depth, the upstream URL, or the provider name (no internal state
// leaked).
const limiter429Body = `{"type":"error","error":{"type":"rate_limit_error","message":"too many concurrent requests, please retry"}}`

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
}

// ServeHTTP runs entirely in the request goroutine: it acquires a slot
// from the buffered-channel semaphore, then calls next.ServeHTTP
// synchronously in the same goroutine, releasing the slot with a defer
// exactly when the upstream round-trip (including an SSE stream) returns.
// A slot is never acquired for a client that disconnects while queued, so
// no concurrency is lost to dead connections.
func (l *concurrencyLimiter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	timer := time.NewTimer(l.wait)
	defer timer.Stop()
	select {
	case l.sem <- struct{}{}:
		defer func() { <-l.sem }()
		l.next.ServeHTTP(w, r)
	case <-timer.C:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(limiter429Body))
	case <-r.Context().Done():
		// Client disconnected while queued: return without acquiring a
		// slot and without forwarding. No response write is needed — the
		// write would fail harmlessly on the dead connection.
	}
}

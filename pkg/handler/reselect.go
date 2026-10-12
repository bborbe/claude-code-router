// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"sync/atomic"
	"time"
)

// reselectPollInterval is how often a request queued for a pool member's
// slot re-checks whether the pool's eligible set has changed since the
// member was selected. Eligibility only changes at window and weekday
// edges, so the check is cheap and the interval only bounds how late a
// queued request notices an edge. It is deliberately not a whole second,
// so a test's WaiterDuration stub can tell a re-check wait apart from a
// maxConcurrentWaitSeconds queue timeout.
const reselectPollInterval = 500 * time.Millisecond

// reselectSignal lets a member's concurrency limiter hand a still-queued
// request back to the upstream pool handler when the member it waits for
// is no longer the pool's choice. The pool handler sets stale and reads
// Bounced after the member handler returns; the limiter calls stale while
// the request waits and marks the signal bounced when it gives the
// request back without serving or refusing it.
//
// Without it a request selects its member once and then waits on that
// member's own semaphore: requests queued on the weekday-day member at
// 17:59 keep waiting for day slots after 18:00, every freed day slot goes
// to the next of them, and the night member's free slots stay unused until
// the day queue drains.
type reselectSignal struct {
	// stale reports whether the pool's eligible set differs from the one the
	// member was selected from. It is called from the limiter's re-check
	// goroutine.
	stale   func() bool
	bounced atomic.Bool
}

// Bounced reports whether the limiter handed the request back unserved.
func (s *reselectSignal) Bounced() bool {
	return s.bounced.Load()
}

type reselectContextKey struct{}

// withReselect returns ctx carrying signal for the member limiter below.
func withReselect(ctx context.Context, signal *reselectSignal) context.Context {
	return context.WithValue(ctx, reselectContextKey{}, signal)
}

// reselectFromContext returns the signal the pool handler attached, or nil
// for a request that did not come through a time-windowed pool.
func reselectFromContext(ctx context.Context) *reselectSignal {
	signal, _ := ctx.Value(reselectContextKey{}).(*reselectSignal)
	return signal
}

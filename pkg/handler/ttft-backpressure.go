// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"sort"
	"sync"
	stdtime "time"

	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// backpressureWindow is the fixed observation window over which the
	// controller evaluates a provider's cold time-to-first-token and its
	// upstream failures. Fixed internal constant, not a config knob.
	backpressureWindow = 60 * stdtime.Second
	// backpressureCleanMedianSeconds is the fixed median cold-TTFT (in
	// seconds) below which a failure-free window is clean and grows the
	// budget. A window whose median sits between this and the configured
	// threshold holds the budget unchanged, so the loop does not oscillate.
	backpressureCleanMedianSeconds = 8.0
	// backpressureGrowthStep is the fixed token step added to the effective
	// budget on a clean window.
	backpressureGrowthStep = 8192
	// backpressureFloorDivisor is the fixed divisor of the configured
	// ceiling that yields the budget floor (ceiling/16).
	backpressureFloorDivisor = 16
)

// TTFTBackpressure is the per-provider backpressure controller (spec 020).
// It watches the provider's own measured cold time-to-first-token over fixed
// 60-second windows, together with the upstream failures observed in the same
// window, and derives the live effective cold-prefill budget the cold-start
// gate admits against.
//
// At most one adjustment happens per window, evaluated lazily when the first
// observation of a new window arrives:
//
//   - a window in which any upstream failure was observed, or whose median
//     cold TTFT rose above the configured threshold, halves the budget —
//     never below ceiling/backpressureFloorDivisor (at least 1);
//   - a failure-free window whose median cold TTFT sits below the fixed
//     backpressureCleanMedianSeconds grows the budget by
//     backpressureGrowthStep — never above the configured ceiling;
//   - a window whose median sits between the clean line and the threshold,
//     with no failure, holds the budget unchanged, so the loop does not
//     oscillate around the threshold.
//
// The cold-start gate reads Budget() on every admission, so a halving takes
// effect on the next cold request rather than only after a restart. Each
// change of the effective budget emits exactly one INFO line and publishes
// the optional gauge.
//
// A nil *TTFTBackpressure is valid and inert: every method is nil-safe, so a
// disabled provider needs no branch at the call site. All mutable state —
// the budget, the window samples, and the failure flag — lives under mu and
// is mutated from concurrent request goroutines.
type TTFTBackpressure struct {
	provider  string
	ceiling   int
	threshold float64
	floor     int
	now       func() libtime.DateTime
	gauge     *prometheus.GaugeVec

	mu          sync.Mutex
	budget      int
	started     bool
	windowIndex int64
	samples     []float64
	failed      bool
}

// NewTTFTBackpressure returns the per-provider backpressure controller for a
// provider whose cold-prefill budget is ceilingTokens and whose cold-TTFT
// threshold is thresholdSeconds. It returns nil — an inert controller —
// when thresholdSeconds <= 0 or ceilingTokens <= 0, so the loop has an effect
// only while the cold-start gate is enabled and a threshold is configured.
//
// now is the router's injected clock and falls back to the real clock when
// nil. The budget starts at the configured ceiling and is published to
// budgetGauge (when non-nil) immediately, so the gauge reads the configured
// ceiling at construction. The floor is ceilingTokens/16, never below 1 — a
// zero budget would silently disable the gate's admission check.
func NewTTFTBackpressure(
	provider string,
	ceilingTokens int,
	thresholdSeconds int,
	now func() libtime.DateTime,
	budgetGauge *prometheus.GaugeVec,
) *TTFTBackpressure {
	if thresholdSeconds <= 0 || ceilingTokens <= 0 {
		return nil
	}
	if now == nil {
		now = realNow
	}
	floor := ceilingTokens / backpressureFloorDivisor
	if floor < 1 {
		floor = 1
	}
	b := &TTFTBackpressure{
		provider:  provider,
		ceiling:   ceilingTokens,
		threshold: float64(thresholdSeconds),
		floor:     floor,
		now:       now,
		gauge:     budgetGauge,
		budget:    ceilingTokens,
	}
	if budgetGauge != nil {
		budgetGauge.WithLabelValues(provider).Set(float64(ceilingTokens))
	}
	return b
}

// Budget returns the live effective cold-prefill budget this provider admits
// against — the configured ceiling until the first adjustment, then the
// halved or grown value. A nil controller returns 0.
func (b *TTFTBackpressure) Budget() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.budget
}

// ObserveColdTTFT records one cold request's measured time-to-first-token in
// seconds into the current window. A nil controller is inert.
func (b *TTFTBackpressure) ObserveColdTTFT(seconds float64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked(b.now().Time())
	b.samples = append(b.samples, seconds)
}

// ObserveStatus records one upstream response status into the current window.
// A status of 500 or above marks the window failed; anything below 500 does
// not, so a client disconnect — which never arrives as a 5xx — is never
// counted. A nil controller is inert.
func (b *TTFTBackpressure) ObserveStatus(status int) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked(b.now().Time())
	if status >= 500 {
		b.failed = true
	}
}

// rollLocked advances the window when at observes a later 60s index than the
// one the controller is on, evaluating the window it is leaving exactly once.
// Must be called with b.mu held. The observation that crosses the boundary
// belongs to the new window, so callers record after rolling. A clock jump
// across several windows performs one evaluation, not one per skipped window;
// a window whose only traffic was warm still rolls, which is why the gate
// observes warm response statuses too.
func (b *TTFTBackpressure) rollLocked(at stdtime.Time) {
	w := at.Unix() / int64(backpressureWindow/stdtime.Second)
	if b.started && w != b.windowIndex {
		b.evaluateLocked()
		b.samples = b.samples[:0]
		b.failed = false
	}
	b.started = true
	b.windowIndex = w
}

// evaluateLocked applies the window's halve/grow/hold decision. Must be
// called with b.mu held. A failure or a median above the threshold halves the
// budget, floored at b.floor; a failure-free median below the clean line
// grows it by backpressureGrowthStep, capped at the ceiling; anything between
// the two holds. A change assigns the budget, publishes the gauge, and emits
// exactly one INFO line; a hold does none of those.
func (b *TTFTBackpressure) evaluateLocked() {
	median := backpressureMedian(b.samples)
	next := b.budget
	switch {
	case b.failed || median > b.threshold:
		next = b.budget / 2
		if next < b.floor {
			next = b.floor
		}
	case median < backpressureCleanMedianSeconds:
		next = b.budget + backpressureGrowthStep
		if next > b.ceiling {
			next = b.ceiling
		}
	}
	if next == b.budget {
		return
	}
	b.budget = next
	if b.gauge != nil {
		b.gauge.WithLabelValues(b.provider).Set(float64(b.budget))
	}
	glog.Infof("[backpressure] provider=%s budget=%d", b.provider, b.budget)
}

// backpressureMedian returns the median of samples: 0 for an empty slice (an
// empty failure-free window is clean and grows the budget), the middle value
// for an odd count, and the arithmetic mean of the two central values for an
// even count. The input is copied, so the caller's slice is never reordered.
func backpressureMedian(samples []float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := make([]float64, len(samples))
	copy(sorted, samples)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"sync"

	libtime "github.com/bborbe/time"
	"github.com/prometheus/client_golang/prometheus"
)

// InFlightPeakWindowSeconds is the width of the sliding window over which
// ccrouter_inflight_requests_peak reports the highest concurrent request
// count per provider. A 60 s window at the default 15 s scrape interval
// means a burst that starts and ends between two scrapes is still visible
// for up to four scrapes.
const InFlightPeakWindowSeconds = 60

// InFlight is a prometheus.Collector that tracks, per provider, how many
// requests are currently dispatched to that provider and not yet returned.
// It exports two gauges:
//
//   - ccrouter_inflight_requests{provider} — the current count. Incremented
//     by Start, decremented by the end function Start returns.
//   - ccrouter_inflight_requests_peak{provider} — the maximum current count
//     observed within the last InFlightPeakWindowSeconds, computed at collect
//     time from per-second maxima. It is never reset on read (several
//     scrapers or pushers see the same value) and is always at least the
//     current count, so a request still open after the window has passed
//     keeps peak >= current.
//
// A provider appears in the output only after its first Start call — there
// is no pre-initialization; the provider set is bounded by the YAML config.
//
// InFlight is safe for concurrent use: Start and the returned end function
// may be called from many goroutines at once.
type InFlight struct {
	mu          sync.Mutex
	clock       libtime.CurrentDateTimeGetter
	currentDesc *prometheus.Desc
	peakDesc    *prometheus.Desc
	// counts holds the live in-flight count per provider.
	counts map[string]int
	// peakBuckets holds, per provider, the highest count seen in each
	// unix-second bucket. Entries older than the peak window are pruned at
	// Start and Collect time.
	peakBuckets map[string]map[int64]int
}

// NewInFlight constructs an InFlight collector that reads the wall clock
// from currentDateTime. It does NOT register itself; register the returned
// collector on the registry /metrics scrapes (Metrics.Register does this).
func NewInFlight(currentDateTime libtime.CurrentDateTimeGetter) *InFlight {
	return &InFlight{
		clock: currentDateTime,
		currentDesc: prometheus.NewDesc(
			"ccrouter_inflight_requests",
			"Number of /v1/* requests currently dispatched to the provider and not yet returned, labeled by provider.",
			[]string{"provider"},
			nil,
		),
		peakDesc: prometheus.NewDesc(
			"ccrouter_inflight_requests_peak",
			"Maximum number of /v1/* requests dispatched to the provider in parallel over the last 60 seconds, labeled by provider. Never reset on read; always at least the current in-flight count.",
			[]string{"provider"},
			nil,
		),
		counts:      make(map[string]int),
		peakBuckets: make(map[string]map[int64]int),
	}
}

// Start records that a request has been dispatched to provider and returns
// an end function that records its return. The end function is idempotent —
// calling it more than once decrements the count exactly once — and never
// lets the count drop below zero. It must be deferred so the decrement runs
// on every exit from the dispatch, including a panic in the target handler.
func (i *InFlight) Start(provider string) func() {
	i.mu.Lock()
	now := i.clock.Now().Time().Unix()
	i.pruneLocked(provider, now)
	i.counts[provider]++
	current := i.counts[provider]
	if i.peakBuckets[provider] == nil {
		i.peakBuckets[provider] = make(map[int64]int)
	}
	if current > i.peakBuckets[provider][now] {
		i.peakBuckets[provider][now] = current
	}
	i.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			i.mu.Lock()
			defer i.mu.Unlock()
			i.counts[provider]--
			if i.counts[provider] < 0 {
				i.counts[provider] = 0
			}
		})
	}
}

// Describe implements prometheus.Collector.
func (i *InFlight) Describe(ch chan<- *prometheus.Desc) {
	ch <- i.currentDesc
	ch <- i.peakDesc
}

// Collect implements prometheus.Collector. The peak is derived here (not
// stored as a reset-on-read value) so repeated collections return the same
// result until the window advances.
func (i *InFlight) Collect(ch chan<- prometheus.Metric) {
	i.mu.Lock()
	defer i.mu.Unlock()
	now := i.clock.Now().Time().Unix()
	for provider, current := range i.counts {
		i.pruneLocked(provider, now)
		peak := current
		for _, bucket := range i.peakBuckets[provider] {
			if bucket > peak {
				peak = bucket
			}
		}
		ch <- prometheus.MustNewConstMetric(
			i.currentDesc,
			prometheus.GaugeValue,
			float64(current),
			provider,
		)
		ch <- prometheus.MustNewConstMetric(
			i.peakDesc,
			prometheus.GaugeValue,
			float64(peak),
			provider,
		)
	}
}

// pruneLocked drops provider's per-second peak buckets that have fallen out
// of the sliding window. Caller must hold i.mu.
func (i *InFlight) pruneLocked(provider string, now int64) {
	for sec := range i.peakBuckets[provider] {
		if now-sec >= InFlightPeakWindowSeconds {
			delete(i.peakBuckets[provider], sec)
		}
	}
}

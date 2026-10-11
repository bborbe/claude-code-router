// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"sort"

	"github.com/prometheus/client_golang/prometheus"
)

// NewProviderInFlightCollector returns a Prometheus collector exporting
// ccrouter_provider_dispatched{provider} and ccrouter_provider_queued{provider}:
// one pair of gauge series per provider, valued at scrape time by summing
// that provider's pool members' limiter counts. dispatched sums each member
// concurrencyLimiter's InFlight() — the slots it currently holds — and
// queued sums their Queued() — the requests blocked waiting for one. The
// two are disjoint by construction: a request holding a slot is dispatched,
// a request that found every slot held is queued, and no request is both.
//
// A provider's members are summed rather than sampled because a provider
// may have several simultaneously-eligible members (a pool with parallel
// members, or a model pool), so one limiter's occupancy would under-report
// the provider's real load. The provider set is snapshotted at construction
// (sorted, for deterministic output) and never mutated.
//
// The collector reads only channel lengths and atomic loads, so Collect
// takes no lock and cannot block the request path. It is registered once
// per handler tree, on the same registerer as the tree's other ccrouter_*
// series.
func NewProviderInFlightCollector(
	dispatched map[string][]func() int,
	queued map[string][]func() int,
) prometheus.Collector {
	providers := make([]string, 0, len(dispatched))
	for provider := range dispatched {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	dispatchedFns := make([][]func() int, 0, len(providers))
	queuedFns := make([][]func() int, 0, len(providers))
	for _, provider := range providers {
		dispatchedFns = append(dispatchedFns, dispatched[provider])
		queuedFns = append(queuedFns, queued[provider])
	}
	return &providerInFlightCollector{
		dispatchedDesc: prometheus.NewDesc(
			providerDispatchedMetricName,
			providerDispatchedMetricHelp,
			[]string{"provider"},
			nil,
		),
		queuedDesc: prometheus.NewDesc(
			providerQueuedMetricName,
			providerQueuedMetricHelp,
			[]string{"provider"},
			nil,
		),
		providers:  providers,
		dispatched: dispatchedFns,
		queued:     queuedFns,
	}
}

// providerInFlightCollector is a checked prometheus.Collector exporting one
// ccrouter_provider_dispatched and one ccrouter_provider_queued gauge per
// provider. It holds only the sorted provider names and their per-member
// counter functions, so Collect reads each limiter's occupancy without
// taking a lock and without blocking the request path.
type providerInFlightCollector struct {
	dispatchedDesc *prometheus.Desc
	queuedDesc     *prometheus.Desc
	providers      []string
	dispatched     [][]func() int
	queued         [][]func() int
}

// Describe sends the two descriptors this collector emits, so registration
// validates both metric names and the provider label.
func (c *providerInFlightCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.dispatchedDesc
	ch <- c.queuedDesc
}

// Collect emits one dispatched and one queued gauge per provider, each
// valued by summing that provider's member limiters.
func (c *providerInFlightCollector) Collect(ch chan<- prometheus.Metric) {
	for i, provider := range c.providers {
		ch <- prometheus.MustNewConstMetric(
			c.dispatchedDesc,
			prometheus.GaugeValue,
			float64(sumCounters(c.dispatched[i])),
			provider,
		)
		ch <- prometheus.MustNewConstMetric(
			c.queuedDesc,
			prometheus.GaugeValue,
			float64(sumCounters(c.queued[i])),
			provider,
		)
	}
}

// sumCounters sums a provider's per-member counter functions, skipping nil
// entries — a member with no limiter (maxConcurrentRequests <= 0) has none,
// and the factory stores nil for it, exactly as the pool handler's
// sumInFlight does for the same slice.
func sumCounters(fns []func() int) int {
	total := 0
	for _, fn := range fns {
		if fn == nil {
			continue
		}
		total += fn()
	}
	return total
}

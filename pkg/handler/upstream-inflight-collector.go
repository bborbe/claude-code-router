// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"sort"

	"github.com/prometheus/client_golang/prometheus"
)

// NewUpstreamInFlightCollector returns a Prometheus collector exporting
// ccrouter_upstream_inflight{host} (spec 019): one gauge series per entry
// of hostLimiters, valued by that HostLimiter's InFlight() read at scrape
// time — the shared semaphore occupancy for a capped host, the live
// in-flight count for an uncapped one. hostLimiters is the factory's
// one-limiter-per-host map; the collector snapshots its keys at
// construction (sorted, for deterministic output) and never mutates it.
// The collector is registered once per handler
// tree, on the same registerer as the tree's other ccrouter_* series.
func NewUpstreamInFlightCollector(hostLimiters map[string]*HostLimiter) prometheus.Collector {
	hosts := make([]string, 0, len(hostLimiters))
	for host := range hostLimiters {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	limiters := make([]*HostLimiter, 0, len(hosts))
	for _, host := range hosts {
		limiters = append(limiters, hostLimiters[host])
	}
	return &upstreamInFlightCollector{
		desc: prometheus.NewDesc(
			upstreamInFlightMetricName,
			upstreamInFlightMetricHelp,
			[]string{"host"},
			nil,
		),
		hosts:    hosts,
		limiters: limiters,
	}
}

// upstreamInFlightCollector is a checked prometheus.Collector exporting
// one ccrouter_upstream_inflight gauge per upstream host. It holds only
// the sorted host names and their limiters, so Collect reads each
// limiter's InFlight() — a channel length or an atomic load — without
// taking a lock and without blocking the request path.
type upstreamInFlightCollector struct {
	desc     *prometheus.Desc
	hosts    []string
	limiters []*HostLimiter
}

// Describe sends the single descriptor this collector emits, so
// registration validates the metric name and the host label.
func (c *upstreamInFlightCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

// Collect emits one gauge per upstream host, valued by that host
// limiter's current in-flight count.
func (c *upstreamInFlightCollector) Collect(ch chan<- prometheus.Metric) {
	for i, host := range c.hosts {
		ch <- prometheus.MustNewConstMetric(
			c.desc,
			prometheus.GaugeValue,
			float64(c.limiters[i].InFlight()),
			host,
		)
	}
}

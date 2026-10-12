// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"math"
	"sort"

	"github.com/prometheus/client_golang/prometheus"
)

// ProviderLimit returns the concurrency limit in force for one provider at
// the moment it is called: the sum of maxConcurrentRequests over the pool
// members eligible right now (UpstreamMember.Eligible — the same rule the
// pool handler routes by). caps[i] is member i's maxConcurrentRequests. An
// eligible member with no cap (caps[i] <= 0) makes the provider unbounded
// and the result +Inf.
//
// With no eligible member the pool does not refuse: its selection falls back
// to member 0 (leastLoaded / pinSlot return index 0 for an empty eligible
// set), which is how a closed default provider is still served as a last
// resort. The limit follows the same rule and reports member 0's cap, so
// dispatched/limit stays meaningful in that state. Only a provider with no
// members at all reads 0.
//
// Because eligibility is re-evaluated on every call, a time-windowed pool's
// limit changes at its window boundaries without a reload: the weekday pool
// reads 16 by day and 25 from 18:00 (spec 014 / 017).
func ProviderLimit(members []UpstreamMember, caps []int) float64 {
	limit := 0
	eligible := 0
	for i, member := range members {
		if !member.Eligible() {
			continue
		}
		eligible++
		if i >= len(caps) || caps[i] <= 0 {
			return math.Inf(1)
		}
		limit += caps[i]
	}
	if eligible == 0 && len(members) > 0 {
		if len(caps) == 0 || caps[0] <= 0 {
			return math.Inf(1)
		}
		return float64(caps[0])
	}
	return float64(limit)
}

// NewProviderLimitCollector returns a Prometheus collector exporting
// ccrouter_provider_limit{provider}: one gauge per provider, valued at scrape
// time by calling that provider's limit function (ProviderLimit over its
// members). It is the denominator for ccrouter_provider_dispatched — the two
// plot on one graph, so a provider pinned at its limit is visible without
// reconstructing the cap from the config. The provider set is snapshotted at
// construction (sorted, for deterministic output) and never mutated.
func NewProviderLimitCollector(limits map[string]func() float64) prometheus.Collector {
	providers := make([]string, 0, len(limits))
	for provider := range limits {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	fns := make([]func() float64, 0, len(providers))
	for _, provider := range providers {
		fns = append(fns, limits[provider])
	}
	return &providerLimitCollector{
		desc: prometheus.NewDesc(
			providerLimitMetricName,
			providerLimitMetricHelp,
			[]string{"provider"},
			nil,
		),
		providers: providers,
		limits:    fns,
	}
}

// providerLimitCollector is a checked prometheus.Collector exporting one
// ccrouter_provider_limit gauge per provider.
type providerLimitCollector struct {
	desc      *prometheus.Desc
	providers []string
	limits    []func() float64
}

// Describe sends the single descriptor this collector emits.
func (c *providerLimitCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

// Collect emits one gauge per provider, valued by its limit right now.
func (c *providerLimitCollector) Collect(ch chan<- prometheus.Metric) {
	for i, provider := range c.providers {
		ch <- prometheus.MustNewConstMetric(
			c.desc,
			prometheus.GaugeValue,
			c.limits[i](),
			provider,
		)
	}
}

// NewUpstreamLimitCollector returns a Prometheus collector exporting
// ccrouter_upstream_limit{host}: one gauge per entry of hostLimiters, valued
// by HostLimiter.Limit — the host's upstreamHostLimits budget, +Inf when
// uncapped. It is the denominator for ccrouter_upstream_inflight, kept a
// separate collector so each collector exports one metric family.
// hostLimiters is the factory's one-limiter-per-host map, snapshotted at
// construction (sorted) and never mutated.
func NewUpstreamLimitCollector(hostLimiters map[string]*HostLimiter) prometheus.Collector {
	hosts := make([]string, 0, len(hostLimiters))
	for host := range hostLimiters {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	limiters := make([]*HostLimiter, 0, len(hosts))
	for _, host := range hosts {
		limiters = append(limiters, hostLimiters[host])
	}
	return &upstreamLimitCollector{
		desc: prometheus.NewDesc(
			upstreamLimitMetricName,
			upstreamLimitMetricHelp,
			[]string{"host"},
			nil,
		),
		hosts:    hosts,
		limiters: limiters,
	}
}

// upstreamLimitCollector is a checked prometheus.Collector exporting one
// ccrouter_upstream_limit gauge per upstream host.
type upstreamLimitCollector struct {
	desc     *prometheus.Desc
	hosts    []string
	limiters []*HostLimiter
}

// Describe sends the single descriptor this collector emits.
func (c *upstreamLimitCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

// Collect emits one gauge per upstream host, valued by its budget.
func (c *upstreamLimitCollector) Collect(ch chan<- prometheus.Metric) {
	for i, host := range c.hosts {
		ch <- prometheus.MustNewConstMetric(
			c.desc,
			prometheus.GaugeValue,
			c.limiters[i].Limit(),
			host,
		)
	}
}

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// LatencyBucketsSeconds covers expected LLM end-to-end latency
// distribution: sub-second for cached / token-counter calls, multi-
// second for streaming completions, up to ~60s for long contexts.
// Bucket boundaries match what a Grafana p95/p99 panel on
// `histogram_quantile(0.95, ...ccrouter_request_duration_seconds...)`
// can meaningfully resolve.
var LatencyBucketsSeconds = []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

// Metrics groups the Prometheus collectors emitted from the model
// router: one CounterVec for request totals labeled by provider +
// model + status_class (`2xx`/`4xx`/`5xx`), one HistogramVec for
// request latency labeled by provider + model with LLM-shaped buckets,
// and one CounterVec for alias resolutions (operator-side observability
// for `/model qwen`-style short names).
//
// Cardinality budget: 5 providers × ~15 active models × 3 status_classes
// = 225 series for the requests counter; histogram adds 5×15×len(buckets)
// = 750 series; aliases counter bounded by the YAML config (≤10). Total
// ~1k series — fine for a local Prometheus scrape.
type Metrics struct {
	RequestsTotal    *prometheus.CounterVec
	RequestDuration  *prometheus.HistogramVec
	AliasResolutions *prometheus.CounterVec
}

// NewMetrics constructs the three collectors but does NOT register
// them. Call Register on a *prometheus.Registry to expose them; that
// split lets tests verify behavior against a fresh registry per spec
// without colliding on the global default registry.
func NewMetrics() *Metrics {
	return &Metrics{
		RequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "ccrouter_requests_total",
				Help: "Total number of /v1/* requests routed, labeled by provider, model, and status_class (2xx/4xx/5xx).",
			},
			[]string{"provider", "model", "status_class"},
		),
		RequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "ccrouter_request_duration_seconds",
				Help:    "End-to-end /v1/* request latency in seconds (wall time from receive to response complete), labeled by provider and model.",
				Buckets: LatencyBucketsSeconds,
			},
			[]string{"provider", "model"},
		),
		AliasResolutions: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "ccrouter_alias_resolutions_total",
				Help: "Number of times an alias (e.g. /model qwen) resolved to a full model name, labeled by alias and resolved.",
			},
			[]string{"alias", "resolved"},
		),
	}
}

// Register registers all collectors against reg. Pass a fresh registry
// in tests; pass the registry the /metrics endpoint scrapes in
// production. Returns the first registration error (if any) so caller
// can decide whether to abort startup.
func (m *Metrics) Register(reg prometheus.Registerer) error {
	for _, c := range []prometheus.Collector{m.RequestsTotal, m.RequestDuration, m.AliasResolutions} {
		if err := reg.Register(c); err != nil {
			return err
		}
	}
	return nil
}

// ObserveRequest is the call-site shorthand used by NewModelRouter
// after each /v1/* dispatch: increments the request counter (bucketing
// status into 2xx/4xx/5xx to keep cardinality bounded) and observes
// the latency on the histogram.
func (m *Metrics) ObserveRequest(provider, model string, status int, latencySeconds float64) {
	m.RequestsTotal.WithLabelValues(provider, model, statusClass(status)).Inc()
	m.RequestDuration.WithLabelValues(provider, model).Observe(latencySeconds)
}

// ObserveAliasResolution increments the alias counter on each hit;
// labels are bounded by the YAML config's `aliases:` map size.
func (m *Metrics) ObserveAliasResolution(alias, resolved string) {
	m.AliasResolutions.WithLabelValues(alias, resolved).Inc()
}

// statusClass collapses raw HTTP status into one of "2xx", "3xx",
// "4xx", "5xx" — keeps the requests-counter cardinality at 3 per
// (provider, model) pair instead of unbounded (every status code
// from upstream creates a new series otherwise).
func statusClass(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500 && status < 600:
		return "5xx"
	default:
		return strconv.Itoa(status)
	}
}

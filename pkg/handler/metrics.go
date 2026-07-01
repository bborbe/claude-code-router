// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// UnknownModelLabel is the sentinel used in place of an empty model
// string when the request body has no `model` field (probe traffic,
// misshapen bodies) or the router-side early-return paths reject
// before body parse. The leading + trailing underscore avoids
// collision with real Anthropic + OpenRouter model names, which
// never start with `_`. This constant is a MODEL-label sentinel; the
// same string is also used as the provider label value from the three
// router-side early-return paths in NewModelRouter (see spec 007
// Desired Behavior 6) — that reuse is deliberate and scoped, NOT a
// general fallback for other labels.
const UnknownModelLabel = "_unknown_"

// LatencyBucketsSeconds covers expected LLM end-to-end latency
// distribution: sub-second for cached / token-counter calls, multi-
// second for streaming completions, up to ~60s for long contexts.
// Bucket boundaries match what a Grafana p95/p99 panel on
// `histogram_quantile(0.95, ...ccrouter_request_duration_seconds...)`
// can meaningfully resolve.
var LatencyBucketsSeconds = []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

// Metrics groups the Prometheus collectors emitted from the model
// router: one CounterVec for request totals labeled by provider +
// model + status_class (`2xx`/`3xx`/`4xx_auth`/`4xx_rate_limited`/
// `4xx_bad_request`/`5xx_upstream`/`5xx_router`), one HistogramVec for
// request latency labeled by provider + model with LLM-shaped buckets,
// one CounterVec for alias resolutions (operator-side observability
// for `/model qwen`-style short names), and one CounterVec for token
// counts labeled by provider + model + direction (`input`/`output`).
//
// Cardinality budget: 5 providers × ~15 active models × 7 status_classes
// = 525 series ceiling for the requests counter (~450 in practice because
// some tuples never fire); 5 × 15 × 2 = 150 series for the tokens
// counter; histogram adds 5×15×len(buckets) = 750 series; aliases
// counter bounded by the YAML config (≤10). Total ~1.5k series — fine
// for a local Prometheus scrape.
type Metrics struct {
	RequestsTotal    *prometheus.CounterVec
	RequestDuration  *prometheus.HistogramVec
	AliasResolutions *prometheus.CounterVec
	TokensTotal      *prometheus.CounterVec
}

// NewMetrics constructs the four collectors but does NOT register
// them. Call Register on a *prometheus.Registry to expose them; that
// split lets tests verify behavior against a fresh registry per spec
// without colliding on the global default registry.
//
// aliases is used to pre-initialize the alias_resolutions counter so
// that `rate(...) > X` alerts evaluate to 0 (not no-data) for aliases
// that haven't been hit yet. A nil aliases map is safe — ranging a nil
// map yields zero iterations. TokensTotal is NOT pre-initialized: there
// is no closed set of (provider, model, direction) tuples known at
// boot; cardinality is bounded by real traffic.
func NewMetrics(aliases map[string]string) *Metrics {
	m := &Metrics{
		RequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "ccrouter_requests_total",
				Help: "Total number of /v1/* requests routed, labeled by provider, model, and status_class (2xx/3xx/4xx_auth/4xx_rate_limited/4xx_bad_request/5xx_upstream/5xx_router).",
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
		TokensTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "ccrouter_tokens_total",
				Help: "Total number of LLM tokens observed on successful (2xx) /v1/* responses, labeled by provider, model, and direction (input|output). Fed from the ExtractUsage tee on the response tail; non-2xx responses do not increment this counter.",
			},
			[]string{"provider", "model", "direction"},
		),
	}
	for alias, resolved := range aliases {
		m.AliasResolutions.WithLabelValues(alias, resolved).Add(0)
	}
	return m
}

// Register registers all collectors against reg. Pass a fresh registry
// in tests; pass the registry the /metrics endpoint scrapes in
// production. Returns the first registration error (if any) so caller
// can decide whether to abort startup.
func (m *Metrics) Register(reg prometheus.Registerer) error {
	for _, c := range []prometheus.Collector{m.RequestsTotal, m.RequestDuration, m.AliasResolutions, m.TokensTotal} {
		if err := reg.Register(c); err != nil {
			return err
		}
	}
	return nil
}

// ObserveRequest is the call-site shorthand used by NewModelRouter
// after each /v1/* dispatch: increments the request counter with the
// 7-value status_class enum (see statusClass) and observes the latency
// on the histogram. isRouterError distinguishes 5xx router-side
// rejections (body-too-large, alias-rewrite-fail) from 5xx upstream
// faults — see spec 007 Desired Behavior 2 and 6. Callers on the
// happy path pass isRouterError=false.
func (m *Metrics) ObserveRequest(
	provider, model string,
	status int,
	latencySeconds float64,
	isRouterError bool,
) {
	m.RequestsTotal.WithLabelValues(provider, model, statusClass(status, isRouterError)).Inc()
	m.RequestDuration.WithLabelValues(provider, model).Observe(latencySeconds)
}

// ObserveTokens increments the ccrouter_tokens_total counter by count
// for the given (provider, model, direction) tuple. Drop rules — the
// call is a no-op (no series created) when:
//
//   - count <= 0                                (zero-drop rule; zero is not a data point, negative is a schema violation)
//   - direction is not "input" or "output"      (bounded-enum rule; keeps cardinality contract)
//
// These drops are silent: token counting is best-effort observability,
// never on the request-serving critical path. See spec 007 Failure
// Modes for the sentinel/negative/zero rows this method absorbs.
func (m *Metrics) ObserveTokens(provider, model, direction string, count int) {
	if count <= 0 {
		return
	}
	if direction != "input" && direction != "output" {
		return
	}
	m.TokensTotal.WithLabelValues(provider, model, direction).Add(float64(count))
}

// ObserveAliasResolution increments the alias counter on each hit;
// labels are bounded by the YAML config's `aliases:` map size.
func (m *Metrics) ObserveAliasResolution(alias, resolved string) {
	m.AliasResolutions.WithLabelValues(alias, resolved).Inc()
}

// statusClass collapses raw HTTP status into a bounded 7-value enum
// used as the ccrouter_requests_total{status_class} label:
//
//	status 2xx                       -> "2xx"
//	status 3xx                       -> "3xx"
//	status 401 or 403                -> "4xx_auth"
//	status 429                       -> "4xx_rate_limited"
//	any other 4xx                    -> "4xx_bad_request"
//	5xx with isRouterError == false  -> "5xx_upstream"
//	5xx with isRouterError == true   -> "5xx_router"
//	anything else                    -> raw strconv.Itoa(status)
//
// Cardinality contract: exactly 7 distinct label values in normal
// operation (plus the raw-code fallback for out-of-range status). No
// per-status-code label (spec 007 Non-goals).
//
// The isRouterError argument is set true by the three router-side
// early-return paths in NewModelRouter (body-too-large 413,
// body-read-fail 400, alias-rewrite-fail 500) so a router-side 500
// separates from an upstream 500 in Grafana.
func statusClass(status int, isRouterError bool) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status == 401 || status == 403:
		return "4xx_auth"
	case status == 429:
		return "4xx_rate_limited"
	case status >= 400 && status < 500:
		return "4xx_bad_request"
	case status >= 500 && status < 600:
		if isRouterError {
			return "5xx_router"
		}
		return "5xx_upstream"
	default:
		return strconv.Itoa(status)
	}
}

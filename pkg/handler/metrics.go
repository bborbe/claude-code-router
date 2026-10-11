// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"strconv"

	libtime "github.com/bborbe/time"
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

const (
	// upstreamInFlightMetricName is the per-upstream-host in-flight gauge
	// (spec 019), exported by the collector in
	// upstream-inflight-collector.go. It is a distinct series from the
	// per-provider in-flight gauges and is registered separately from
	// Metrics.Register so the two never collide.
	upstreamInFlightMetricName = "ccrouter_upstream_inflight"
	// upstreamInFlightMetricHelp is the HELP text of
	// ccrouter_upstream_inflight.
	upstreamInFlightMetricHelp = "Current number of /v1/* requests in flight to an upstream host, summed across every provider and pool member resolving to that host (spec 019). For a host capped in upstreamHostLimits this is the shared semaphore's occupancy and never exceeds the cap; for an uncapped host it is the live in-flight count."
	// providerDispatchedMetricName is the per-provider dispatched gauge,
	// exported by the collector in provider-inflight-collector.go. It is the
	// dispatched half of ccrouter_inflight_requests: the summed occupancy of
	// the provider's eligible members' concurrency limiters. Registered
	// separately from Metrics.Register, like ccrouter_upstream_inflight, so
	// the two never collide.
	providerDispatchedMetricName = "ccrouter_provider_dispatched"
	// providerDispatchedMetricHelp is the HELP text of
	// ccrouter_provider_dispatched.
	providerDispatchedMetricHelp = "Current number of /v1/* requests holding a concurrency slot at the provider, summed across its eligible pool members, labeled by provider. The dispatched half of ccrouter_inflight_requests, which counts queued + paced + executing: this gauge never exceeds the sum of the eligible members' maxConcurrentRequests, so a value pinned at that sum means the provider is saturated."
	// providerQueuedMetricName is the per-provider queued gauge, exported by
	// the collector in provider-inflight-collector.go. It is the queued half
	// of ccrouter_inflight_requests.
	providerQueuedMetricName = "ccrouter_provider_queued"
	// providerQueuedMetricHelp is the HELP text of
	// ccrouter_provider_queued.
	providerQueuedMetricHelp = "Current number of /v1/* requests blocked waiting for a concurrency slot at the provider, summed across its eligible pool members, labeled by provider. The queued half of ccrouter_inflight_requests: it counts only requests that found every slot held, so it excludes requests held by the 429 pacing gate and by the cold-start admission queue."
)

// Metrics groups the Prometheus collectors emitted from the model
// router: one CounterVec for request totals labeled by provider +
// model + status_class (`2xx`/`3xx`/`4xx_auth`/`4xx_rate_limited`/
// `4xx_bad_request`/`5xx_upstream`/`5xx_router`), one HistogramVec for
// request latency labeled by provider + model with LLM-shaped buckets,
// one CounterVec for alias resolutions (operator-side observability
// for `/model qwen`-style short names), one CounterVec for token
// counts labeled by provider + model + direction (`input`/`output`),
// one CounterVec for requests delayed (paced) by the adaptive 429
// delay gate, labeled by provider (spec 018), one InFlight collector
// exporting the current + 60 s-peak in-flight request count per
// provider, four further additive collectors observing the
// per-provider cold-start admission gate (spec 019): delayed and
// refused cold-request counters, an in-flight cold-token gauge, and a
// cold time-to-first-token histogram, and two more additive collectors
// for the spec-020 behaviours: the live effective cold-prefill budget
// gauge and the circuit-breaker state-transition counter.
//
// Cardinality budget: 5 providers × ~15 active models × 7 status_classes
// = 525 series ceiling for the requests counter (~450 in practice because
// some tuples never fire); 5 × 15 × 2 = 150 series for the tokens
// counter; histogram adds 5×15×len(buckets) = 750 series; aliases
// counter bounded by the YAML config (≤10); the in-flight gauges add
// 2 series per provider (10). Total ~1.5k series — fine for a local
// Prometheus scrape.
type Metrics struct {
	RequestsTotal    *prometheus.CounterVec
	RequestDuration  *prometheus.HistogramVec
	AliasResolutions *prometheus.CounterVec
	TokensTotal      *prometheus.CounterVec
	CacheTokensTotal *prometheus.CounterVec
	ThrottledTotal   *prometheus.CounterVec
	// The four cold-start admission collectors (spec 019) are additive:
	// they observe the per-provider cold-start gate and never change the
	// existing request/latency/token series or the status_class enum.
	ColdAdmissionDelayedTotal *prometheus.CounterVec
	ColdAdmissionRefusedTotal *prometheus.CounterVec
	ColdTokensInFlight        *prometheus.GaugeVec
	ColdTTFTSeconds           *prometheus.HistogramVec
	// The two spec-020 collectors are additive: the gauge carries the live
	// effective cold-prefill budget the backpressure loop adjusts, and the
	// counter carries the circuit breaker's state transitions. Neither
	// changes the existing series or the status_class enum.
	ColdPrefillBudgetTokens      *prometheus.GaugeVec   // ccrouter_cold_prefill_budget_tokens{provider}
	BreakerStateTransitionsTotal *prometheus.CounterVec // ccrouter_breaker_state_transitions_total{provider,state}
	InFlight                     *InFlight
}

// NewMetrics constructs the collectors but does NOT register them. Call
// Register on a *prometheus.Registry to expose them; that split lets tests
// verify behavior against a fresh registry per spec without colliding on
// the global default registry.
//
// aliases is used to pre-initialize the alias_resolutions counter so
// that `rate(...) > X` alerts evaluate to 0 (not no-data) for aliases
// that haven't been hit yet. A nil aliases map is safe — ranging a nil
// map yields zero iterations. TokensTotal is NOT pre-initialized: there
// is no closed set of (provider, model, direction) tuples known at
// boot; cardinality is bounded by real traffic.
//
// currentDateTime is the clock the InFlight collector uses to bucket its
// 60 s peak window; the caller supplies it (never a fresh
// libtime.NewCurrentDateTime() built here) so the peak window and the
// router's request timing share one clock.
func NewMetrics(aliases map[string]string, currentDateTime libtime.CurrentDateTimeGetter) *Metrics {
	m := newCoreCollectors()
	m.addColdCollectors()
	m.addSpec020Collectors()
	m.InFlight = NewInFlight(currentDateTime)
	for alias, resolved := range aliases {
		m.AliasResolutions.WithLabelValues(alias, resolved).Add(0)
	}
	return m
}

// newCoreCollectors builds the request, latency, alias, token, cache-token,
// and throttle collectors — the pre-spec-019 set. Extracted from NewMetrics so
// that constructor stays under the length gate; it carries no behavior beyond
// constructing the collectors.
func newCoreCollectors() *Metrics {
	return &Metrics{
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
		CacheTokensTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "ccrouter_cache_tokens_total",
				Help: "Total number of cached LLM tokens observed on successful (2xx) /v1/* responses, labeled by provider, model, and direction (read|creation). cache_read counts tokens served from a reused prompt cache; cache_creation counts tokens written into the cache. Kept separate from ccrouter_tokens_total so billed (fresh input + output) and true (fresh + cache + output) usage stay distinguishable.",
			},
			[]string{"provider", "model", "direction"},
		),
		ThrottledTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "ccrouter_throttled_total",
				Help: "Number of /v1/* requests delayed (paced) by the adaptive 429 delay gate before forwarding, labeled by provider. Paced = the request waited the pacing delay and was forwarded; overflow 429s and non-paced requests do not increment it.",
			},
			[]string{"provider"},
		),
	}
}

// addColdCollectors fills in the four additive spec-019 cold-start admission
// collectors on m: the delayed and refused cold-request counters, the
// in-flight cold-token gauge, and the cold time-to-first-token histogram.
func (m *Metrics) addColdCollectors() {
	m.ColdAdmissionDelayedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ccrouter_cold_admission_delayed_total",
			Help: "Number of cold /v1/* requests the cold-start admission gate admitted only after waiting in the bounded queue (blocked by the prefill budget or the new-session rate), labeled by provider. A request admitted immediately and a warm request do not increment it.",
		},
		[]string{"provider"},
	)
	m.ColdAdmissionRefusedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ccrouter_cold_admission_refused_total",
			Help: "Number of cold /v1/* requests the cold-start admission gate refused with HTTP 429 (the bounded queue was full, or the max wait elapsed), labeled by provider. A client that disconnects while waiting is never counted.",
		},
		[]string{"provider"},
	)
	m.ColdTokensInFlight = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ccrouter_cold_tokens_in_flight",
			Help: "Cold-prefill tokens currently reserved by the cold-start admission gate, labeled by provider. Rises while a cold request holds its reservation and returns to zero as the reservation is released on the first content delta (or when the handler returns).",
		},
		[]string{"provider"},
	)
	m.ColdTTFTSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "ccrouter_cold_ttft_seconds",
			Help:    "Time to first token (seconds) for cold /v1/* requests, measured from admission to the first content delta in the response stream, labeled by provider. Warm requests are not observed.",
			Buckets: LatencyBucketsSeconds,
		},
		[]string{"provider"},
	)
}

// addSpec020Collectors fills in the two additive spec-020 collectors on m: the
// gauge carrying the live effective cold-prefill budget the backpressure loop
// adjusts, and the counter carrying the circuit breaker's state transitions.
// Neither is pre-initialized here — the controller sets the budget gauge at
// construction and the breaker pre-initializes its own three state series.
func (m *Metrics) addSpec020Collectors() {
	m.ColdPrefillBudgetTokens = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ccrouter_cold_prefill_budget_tokens",
			Help: "Current effective cold-prefill token budget the cold-start admission gate admits against, labeled by provider. Reads the configured ceiling until the TTFT backpressure loop adjusts it, then the halved or grown value (spec 020).",
		},
		[]string{"provider"},
	)
	m.BreakerStateTransitionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ccrouter_breaker_state_transitions_total",
			Help: "Number of circuit-breaker state transitions, labeled by provider and state (closed|open|half_open). The three state series are pre-initialized to 0 for a provider with the breaker enabled (spec 020).",
		},
		[]string{"provider", "state"},
	)
}

// Register registers all collectors against reg. Pass a fresh registry
// in tests; pass the registry the /metrics endpoint scrapes in
// production. Returns the first registration error (if any) so caller
// can decide whether to abort startup.
func (m *Metrics) Register(reg prometheus.Registerer) error {
	for _, c := range []prometheus.Collector{m.RequestsTotal, m.RequestDuration, m.AliasResolutions, m.TokensTotal, m.CacheTokensTotal, m.ThrottledTotal, m.ColdAdmissionDelayedTotal, m.ColdAdmissionRefusedTotal, m.ColdTokensInFlight, m.ColdTTFTSeconds, m.ColdPrefillBudgetTokens, m.BreakerStateTransitionsTotal, m.InFlight} {
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

// ObserveCacheTokens increments the ccrouter_cache_tokens_total counter
// by count for the given (provider, model, direction) tuple. Drop rules
// mirror ObserveTokens:
//
//   - count <= 0                                (zero-drop rule)
//   - direction is not "read" or "creation"     (bounded-enum rule)
//
// Kept as a separate observer (not an extension of ObserveTokens's
// input|output enum) so the existing ccrouter_tokens_total contract is
// unchanged — cache is a distinct metric, never merged into input.
func (m *Metrics) ObserveCacheTokens(provider, model, direction string, count int) {
	if count <= 0 {
		return
	}
	if direction != "read" && direction != "creation" {
		return
	}
	m.CacheTokensTotal.WithLabelValues(provider, model, direction).Add(float64(count))
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

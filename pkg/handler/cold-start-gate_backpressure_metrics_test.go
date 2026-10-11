// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Spec 020 collector specs: the two additive Prometheus series the new
// behaviours publish — the live effective cold-prefill budget gauge and the
// circuit-breaker state-transition counter — are wired through the real
// NewMetrics collector set and register cleanly on a fresh registry. Rows
// drive the injected clock so a window boundary never sleeps wall-clock.

package handler_test

import (
	"net/http"
	"net/http/httptest"
	stdtime "time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

var _ = Describe("Spec 020 observability", func() {
	var clock = newClock()

	// newRegisteredMetrics builds the real collector set (including the two
	// spec-020 collectors) and registers it on a fresh registry, so the rows
	// exercise NewMetrics and Register too.
	newRegisteredMetrics := func() (*handler.Metrics, *prometheus.Registry) {
		reg := prometheus.NewRegistry()
		m := handler.NewMetrics(nil, testDateTime)
		Expect(m.Register(reg)).To(Succeed())
		return m, reg
	}

	BeforeEach(func() {
		clock = newClock()
	})

	It("AC 11 — the budget gauge tracks the controller's live effective budget", func() {
		m, _ := newRegisteredMetrics()
		bp := handler.NewTTFTBackpressure("p", 65536, 20, clock.Now, m.ColdPrefillBudgetTokens)

		Expect(testutil.ToFloat64(m.ColdPrefillBudgetTokens.WithLabelValues("p"))).
			To(Equal(float64(65536)), "the gauge reads the configured ceiling at construction")

		// Drive one over-threshold window: the budget halves on the boundary.
		bp.ObserveColdTTFT(25)
		clock.SetNow(bpAt(61 * stdtime.Second))
		bp.ObserveColdTTFT(1.0)
		Expect(testutil.ToFloat64(m.ColdPrefillBudgetTokens.WithLabelValues("p"))).
			To(Equal(float64(32768)), "the gauge reads the halved value")

		// Drive one clean window: the budget grows by the fixed step.
		clock.SetNow(bpAt(122 * stdtime.Second))
		bp.ObserveColdTTFT(1.0)
		Expect(testutil.ToFloat64(m.ColdPrefillBudgetTokens.WithLabelValues("p"))).
			To(Equal(float64(40960)), "the gauge reads the grown value")
	})

	It(
		"AC 12 — the breaker counter is pre-initialized to 0 and increments on a transition",
		func() {
			m, _ := newRegisteredMetrics()
			stub := newGateStub(http.StatusInternalServerError, "")
			breaker := handler.NewCircuitBreaker(
				stub,
				"p",
				5,
				clock.Now,
				m.BreakerStateTransitionsTotal,
			)

			for _, state := range []string{"closed", "open", "half_open"} {
				Expect(
					testutil.ToFloat64(m.BreakerStateTransitionsTotal.WithLabelValues("p", state)),
				).To(Equal(float64(0)), "the "+state+" series is pre-initialized to 0")
			}

			// Five consecutive 5xx responses reach the threshold and open the breaker.
			for i := 0; i < 5; i++ {
				breaker.ServeHTTP(httptest.NewRecorder(), newMessagesRequest())
			}
			Expect(testutil.ToFloat64(m.BreakerStateTransitionsTotal.WithLabelValues("p", "open"))).
				To(Equal(float64(1)), "exactly one closed->open transition is counted")
			Expect(
				testutil.ToFloat64(m.BreakerStateTransitionsTotal.WithLabelValues("p", "closed")),
			).
				To(Equal(float64(0)), "the initial state is not a transition")
		},
	)

	It("AC 3 — neither new series exists when both knobs are disabled", func() {
		m, _ := newRegisteredMetrics()

		// A disabled controller is nil and a disabled breaker returns next
		// unchanged, so neither touches its collector.
		Expect(handler.NewTTFTBackpressure("p", 65536, 0, clock.Now, m.ColdPrefillBudgetTokens)).
			To(BeNil())
		inner := newGateStub(http.StatusOK, "")
		Expect(
			handler.NewCircuitBreaker(inner, "p", 0, clock.Now, m.BreakerStateTransitionsTotal),
		).To(Equal(http.Handler(inner)))

		Expect(
			testutil.CollectAndCount(
				m.ColdPrefillBudgetTokens,
				"ccrouter_cold_prefill_budget_tokens",
			),
		).To(Equal(0), "no budget series for a provider that opts out")
		Expect(
			testutil.CollectAndCount(
				m.BreakerStateTransitionsTotal,
				"ccrouter_breaker_state_transitions_total",
			),
		).To(Equal(0), "no breaker series for a provider that opts out")
	})
})

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Spec 020 wiring specs: CreateRouterFromConfig builds the TTFT backpressure
// controller alongside the cold-start gate and wraps the provider handler in
// the circuit breaker OUTERMOST, so both run on the real dispatch path and
// publish their additive series. A rebuild on a fresh CreateRouterFromConfig
// (exactly the reloader's SIGHUP path) applies changed knobs with fresh
// in-memory state; a provider with neither knob set is byte-for-byte
// unchanged and publishes neither series.

package factory_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/factory"
)

// bpWiringClock returns a clock pinned to a fixed instant, so the breaker's
// open-period arithmetic never elapses across a row.
func bpWiringClock() libtime.CurrentDateTime {
	clock := libtime.NewCurrentDateTime()
	clock.SetNow(libtime.DateTime(stdtime.Date(2026, 10, 11, 12, 0, 0, 0, stdtime.UTC)))
	return clock
}

// gaugeFamilyValue returns the value of the series labeled provider in the
// named gauge family, or (-1, false) when the family or the series is absent.
func gaugeFamilyValue(reg prometheus.Gatherer, name, provider string) (float64, bool) {
	families, err := reg.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "provider" && lp.GetValue() == provider {
					return m.GetGauge().GetValue(), true
				}
			}
		}
	}
	return -1, false
}

// familyPresent reports whether a family with the given name is in the registry.
func familyPresent(reg prometheus.Gatherer, name string) bool {
	families, err := reg.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, f := range families {
		if f.GetName() == name {
			return true
		}
	}
	return false
}

var _ = Describe("CreateRouterFromConfig spec 020 wiring", func() {
	var (
		srv    *httptest.Server
		calls  int32
		status int32
	)

	// bpConfig builds a single-provider config with the given spec-020 knobs;
	// a zero value leaves that knob off.
	bpConfig := func(ttftThreshold, breakerThreshold int) *pkg.Config {
		return &pkg.Config{
			Router: pkg.Router{DefaultProvider: "t"},
			Providers: map[string]pkg.Provider{
				"t": {
					Upstream:                         srv.URL,
					Models:                           []string{"m*"},
					ColdPrefillBudgetTokens:          65536,
					TtftBackpressureThresholdSeconds: ttftThreshold,
					BreakerFailureThreshold:          breakerThreshold,
				},
			},
		}
	}

	// buildRouter builds the handler tree exactly as the reloader does on
	// SIGHUP: a fresh registry per build so metrics.Register never collides.
	buildRouter := func(ttftThreshold, breakerThreshold int) (http.Handler, *prometheus.Registry) {
		reg := prometheus.NewRegistry()
		h, err := factory.CreateRouterFromConfig(
			context.Background(),
			bpConfig(ttftThreshold, breakerThreshold),
			factory.WithMetricsRegisterer(reg),
			factory.WithCurrentDateTime(bpWiringClock()),
		)
		Expect(err).NotTo(HaveOccurred())
		return h, reg
	}

	BeforeEach(func() {
		calls = 0
		status = http.StatusOK
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(int(atomic.LoadInt32(&status)))
			_, _ = w.Write([]byte(`{"type":"message","content":[]}`))
		}))
	})

	AfterEach(func() {
		srv.Close()
	})

	It("wires the budget gauge and it reads the configured ceiling", func() {
		_, reg := buildRouter(20, 0)

		value, ok := gaugeFamilyValue(reg, "ccrouter_cold_prefill_budget_tokens", "t")
		Expect(ok).To(BeTrue(), "the budget gauge is wired for the provider")
		Expect(value).To(Equal(float64(65536)), "it reads the configured ceiling")
	})

	It("wraps the breaker outermost, so an open breaker short-circuits the provider", func() {
		atomic.StoreInt32(&status, http.StatusInternalServerError)
		handler, _ := buildRouter(0, 2)

		// The first two 500s forward and pass the upstream status through.
		for i := 0; i < 2; i++ {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, newMessagesRequest("m1"))
			Expect(rec.Code).To(Equal(http.StatusInternalServerError))
		}
		Expect(atomic.LoadInt32(&calls)).To(BeNumerically("==", 2))

		// The third request is answered 429 by the breaker, and the upstream is
		// never invoked — proving the breaker sits outside the pool handler.
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, newMessagesRequest("m2"))
		Expect(rec.Code).To(Equal(http.StatusTooManyRequests))
		Expect(rec.Header().Get("Retry-After")).To(Equal("30"))
		Expect(atomic.LoadInt32(&calls)).To(BeNumerically("==", 2), "the upstream is not invoked")
	})

	It("publishes neither series when both knobs are unset", func() {
		_, reg := buildRouter(0, 0)

		Expect(familyPresent(reg, "ccrouter_cold_prefill_budget_tokens")).
			To(BeFalse(), "a provider that opts out gets no budget series")
		Expect(familyPresent(reg, "ccrouter_breaker_state_transitions_total")).
			To(BeFalse(), "a provider that opts out gets no breaker series")
	})

	It("applies changed knobs on rebuild with fresh in-memory state (SIGHUP path)", func() {
		atomic.StoreInt32(&status, http.StatusInternalServerError)

		// handler1 with threshold 2 trips on the third request.
		handler1, _ := buildRouter(0, 2)
		for i := 0; i < 2; i++ {
			rec := httptest.NewRecorder()
			handler1.ServeHTTP(rec, newMessagesRequest("m1"))
			Expect(rec.Code).To(Equal(http.StatusInternalServerError))
		}
		tripped := httptest.NewRecorder()
		handler1.ServeHTTP(tripped, newMessagesRequest("m2"))
		Expect(tripped.Code).To(Equal(http.StatusTooManyRequests), "handler1's breaker is open")

		// Rebuild — exactly the reloader's SIGHUP path — with a threshold the
		// 500-returning upstream cannot reach; a request forwards again.
		handler2, _ := buildRouter(0, 100)
		before := atomic.LoadInt32(&calls)
		rec2 := httptest.NewRecorder()
		handler2.ServeHTTP(rec2, newMessagesRequest("m3"))
		Expect(rec2.Code).To(Equal(http.StatusInternalServerError))
		Expect(atomic.LoadInt32(&calls)).To(BeNumerically(">", before), "the upstream is reached")

		// State does not carry over: a rebuild with the SAME threshold as
		// handler1 starts closed, so its first two requests forward and reach
		// the upstream before the third opens the breaker — a carried-over open
		// state would answer the first request 429.
		handler3, _ := buildRouter(0, 2)
		for i := 0; i < 2; i++ {
			before := atomic.LoadInt32(&calls)
			rec := httptest.NewRecorder()
			handler3.ServeHTTP(rec, newMessagesRequest("m4"))
			Expect(
				rec.Code,
			).To(Equal(http.StatusInternalServerError), "the rebuilt breaker is closed")
			Expect(atomic.LoadInt32(&calls)).To(BeNumerically(">", before))
		}
		third := httptest.NewRecorder()
		handler3.ServeHTTP(third, newMessagesRequest("m5"))
		Expect(
			third.Code,
		).To(Equal(http.StatusTooManyRequests), "the third opens the rebuilt breaker")
	})
})

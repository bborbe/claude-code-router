// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Cold-start admission gate observability specs (spec 019): the four
// additive Prometheus series (delayed, refused, in-flight tokens, cold
// time-to-first-token) are emitted from the gate's decision sites through
// the real collectors, and every delayed or refused decision emits one
// INFO [coldgate] line that never carries the client-controlled session id.

package handler_test

import (
	"flag"
	"net/http"
	"net/http/httptest"
	"strings"
	stdtime "time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

var _ = Describe("ColdStartGate observability", func() {
	var clock = newClock()

	// newMeteredColdGate constructs a real gate wired to the four cold
	// collectors on m, so the rows observe the same handles the factory
	// passes (provider "p", one-minute session window).
	newMeteredColdGate := func(
		next http.Handler,
		budget int,
		rate int,
		maxWait stdtime.Duration,
		m *handler.Metrics,
	) http.Handler {
		return handler.NewColdStartGate(
			next,
			"p",
			budget,
			stdtime.Minute,
			rate,
			maxWait,
			clock.Now,
			handler.ColdGateMetrics{
				Delayed:        m.ColdAdmissionDelayedTotal,
				Refused:        m.ColdAdmissionRefusedTotal,
				TokensInFlight: m.ColdTokensInFlight,
				TTFT:           m.ColdTTFTSeconds,
			},
		)
	}

	// newRegisteredMetrics builds the real collector set and registers it on a
	// fresh registry, so the rows exercise NewMetrics and Register too.
	newRegisteredMetrics := func() (*handler.Metrics, *prometheus.Registry) {
		reg := prometheus.NewRegistry()
		m := handler.NewMetrics(nil)
		Expect(m.Register(reg)).To(Succeed())
		return m, reg
	}

	gaugeValue := func(m *handler.Metrics) float64 {
		return testutil.ToFloat64(m.ColdTokensInFlight.WithLabelValues("p"))
	}

	BeforeEach(func() {
		clock = newClock()
	})

	It("counts a delayed admission once and shows the reservation on the in-flight gauge", func() {
		m, _ := newRegisteredMetrics()
		inner := newColdStub("")
		inner.block = make(chan struct{})
		gate := newMeteredColdGate(inner, 200, 0, 30*stdtime.Second, m)
		body := strings.Repeat("x", coldBody)

		// The first cold request is admitted immediately and holds the whole
		// budget: the gauge rises to its estimate while the upstream blocks.
		doneA := serveAsync(gate, httptest.NewRecorder(), sizedRequest("a", body))
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(1))
		Eventually(func() float64 { return gaugeValue(m) }, "1s", "10ms").
			Should(Equal(float64(200)), "the gauge rises with the held reservation")

		// A second cold request is blocked by the budget and not yet delayed.
		doneB := serveAsync(gate, httptest.NewRecorder(), sizedRequest("b", body))
		Consistently(inner.entryCount, "100ms", "10ms").Should(Equal(1))
		Expect(testutil.ToFloat64(m.ColdAdmissionDelayedTotal.WithLabelValues("p"))).
			To(Equal(float64(0)), "a blocked request is not yet delayed")

		// Releasing A frees the budget and admits B only after its wait.
		close(inner.block)
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(2))
		Eventually(
			func() float64 {
				return testutil.ToFloat64(m.ColdAdmissionDelayedTotal.WithLabelValues("p"))
			},
			"1s",
			"10ms",
		).Should(Equal(float64(1)), "the request admitted after waiting is counted once")
		Eventually(doneA, "1s").Should(BeClosed())
		Eventually(doneB, "1s").Should(BeClosed())
		Eventually(func() float64 { return gaugeValue(m) }, "1s", "10ms").
			Should(Equal(float64(0)), "every reservation is released")
	})

	It("counts a timeout refusal and observes one cold TTFT on the first delta", func() {
		m, _ := newRegisteredMetrics()
		inner := newColdStub("")
		inner.block = make(chan struct{})
		// A short max wait keeps the row fast; the held budget blocks the second.
		gate := newMeteredColdGate(inner, 200, 0, 60*stdtime.Millisecond, m)
		body := strings.Repeat("x", coldBody)

		doneA := serveAsync(gate, httptest.NewRecorder(), sizedRequest("a", body))
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(1))

		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, sizedRequest("b", body))
		Expect(rec.Code).To(Equal(http.StatusTooManyRequests))
		Expect(testutil.ToFloat64(m.ColdAdmissionRefusedTotal.WithLabelValues("p"))).
			To(Equal(float64(1)), "a timed-out cold request is refused once")

		close(inner.block)
		Eventually(doneA, "1s").Should(BeClosed())

		// A streaming upstream that emits a content delta observes exactly one
		// cold time-to-first-token sample.
		streamed := newColdStub(coldDelta)
		streamedGate := newMeteredColdGate(streamed, 1000, 0, 30*stdtime.Second, m)
		streamedGate.ServeHTTP(httptest.NewRecorder(), sizedRequest("s2", body))
		Eventually(func() int {
			return testutil.CollectAndCount(m.ColdTTFTSeconds, "ccrouter_cold_ttft_seconds")
		}, "1s", "10ms").Should(Equal(1), "the first delta observes one TTFT sample")
	})

	It("logs delayed and refused decisions without leaking the session id", func() {
		Expect(flag.Set("logtostderr", "true")).To(Succeed())
		m, _ := newRegisteredMetrics()
		body := strings.Repeat("x", coldBody)

		output := captureStderr(func() {
			// Delayed: hold the budget, let a second request wait, then release
			// the first so the second is admitted only after waiting.
			delayedUpstream := newColdStub("")
			delayedUpstream.block = make(chan struct{})
			delayedGate := newMeteredColdGate(
				delayedUpstream,
				200,
				0,
				5*stdtime.Second,
				m,
			)
			holdDone := serveAsync(
				delayedGate,
				httptest.NewRecorder(),
				sizedRequest("SECRET-SESSION-abc123", body),
			)
			Eventually(delayedUpstream.entryCount, "1s", "10ms").Should(Equal(1))
			waitDone := serveAsync(
				delayedGate,
				httptest.NewRecorder(),
				sizedRequest("SECRET-SESSION-abc123-b", body),
			)
			// Wait until the second request is provably queued (it has not
			// entered the upstream) before releasing the first, so it is
			// admitted only after waiting — the delayed path.
			Consistently(delayedUpstream.entryCount, "100ms", "10ms").Should(Equal(1))
			close(delayedUpstream.block)
			Eventually(waitDone, "1s").Should(BeClosed())
			Eventually(holdDone, "1s").Should(BeClosed())

			// Refused: hold the budget with a long-lived request, then let a
			// second time out against a short max wait.
			refusedUpstream := newColdStub("")
			refusedUpstream.block = make(chan struct{})
			refusedGate := newMeteredColdGate(
				refusedUpstream,
				200,
				0,
				60*stdtime.Millisecond,
				m,
			)
			refusedHold := serveAsync(
				refusedGate,
				httptest.NewRecorder(),
				sizedRequest("SECRET-SESSION-abc123-c", body),
			)
			Eventually(refusedUpstream.entryCount, "1s", "10ms").Should(Equal(1))
			refusedRec := httptest.NewRecorder()
			refusedGate.ServeHTTP(
				refusedRec,
				sizedRequest("SECRET-SESSION-abc123-d", body),
			)
			Expect(refusedRec.Code).To(Equal(http.StatusTooManyRequests))
			close(refusedUpstream.block)
			Eventually(refusedHold, "1s").Should(BeClosed())
		})

		Expect(output).To(
			ContainSubstring("[coldgate] provider=p decision=delayed reason=budget"),
		)
		Expect(
			output,
		).To(MatchRegexp(`\[coldgate\] provider=p decision=refused reason=(timeout|queue_full)`))
		Expect(output).NotTo(
			ContainSubstring("SECRET-SESSION-abc123"),
			"no log line may carry a client-controlled session id",
		)
	})
})

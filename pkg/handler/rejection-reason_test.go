// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
)

// rejectionSeriesValue reads the current value of one
// ccrouter_rejected_total series from a registry, or returns -1 when the
// series is absent — the distinction the pre-initialization specs turn on,
// because "absent" and "zero" are exactly what a missing pre-init confuses.
func rejectionSeriesValue(reg *prometheus.Registry, provider, reason string) float64 {
	families, err := reg.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, family := range families {
		if family.GetName() != "ccrouter_rejected_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			var gotProvider, gotReason string
			for _, label := range metric.GetLabel() {
				switch label.GetName() {
				case "provider":
					gotProvider = label.GetValue()
				case "reason":
					gotReason = label.GetValue()
				}
			}
			if gotProvider == provider && gotReason == reason {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return -1
}

// rejectionHistogramSampleCount returns the total observation count of
// ccrouter_queue_wait_seconds for provider, or -1 when the series is
// absent.
func rejectionHistogramSampleCount(reg *prometheus.Registry, provider string) float64 {
	families, err := reg.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, family := range families {
		if family.GetName() != "ccrouter_queue_wait_seconds" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "provider" && label.GetValue() == provider {
					return float64(metric.GetHistogram().GetSampleCount())
				}
			}
		}
	}
	return -1
}

var _ = Describe("Rejection reason", func() {
	var (
		metrics *Metrics
		slot    *rejectionSlot
		req     *http.Request
	)

	BeforeEach(func() {
		metrics = NewMetrics(nil, libtime.NewCurrentDateTime())
		slot = &rejectionSlot{}
		req = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		req = req.WithContext(ContextWithRejectionReason(req.Context(), slot))
	})

	Context("when the concurrency limiter times a request out", func() {
		It("records reason=queue_timeout", func() {
			started := make(chan struct{}, 1)
			release := make(chan struct{})
			blocking := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				started <- struct{}{}
				<-release
			})
			limiter := NewConcurrencyLimiter(
				blocking,
				"seibert-vllm-default",
				1,
				50*stdtime.Millisecond,
				nil, metrics.QueueWaitSeconds,
			)

			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				holder := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				limiter.ServeHTTP(httptest.NewRecorder(), holder)
			}()
			<-started

			rec := httptest.NewRecorder()
			limiter.ServeHTTP(rec, req)

			close(release)
			wg.Wait()

			Expect(rec.Code).To(Equal(http.StatusTooManyRequests))
			Expect(slot.reason).To(Equal(rejectionReasonQueueTimeout))

			// The timed-out wait is observed too, at its full
			// maxConcurrentWait — a queue-wait distribution that counted
			// only admitted requests would hide the saturated tail it
			// exists to show.
			reg := prometheus.NewRegistry()
			Expect(metrics.Register(reg)).To(Succeed())
			Expect(rejectionHistogramSampleCount(reg, "seibert-vllm-default")).To(Equal(float64(1)))
		})

		It("records reason=host_queue_timeout when the shared host budget times it out", func() {
			// The host limiter reuses the concurrencyLimiter type, so before
			// the split a host-slot timeout recorded queue_timeout and was
			// indistinguishable from a member-cap timeout.
			started := make(chan struct{}, 1)
			release := make(chan struct{})
			blocking := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				started <- struct{}{}
				<-release
			})
			host := NewHostLimiter(1, 50*stdtime.Millisecond)
			limiter := host.Wrap(blocking, "seibert-vllm-default", metrics.QueueWaitSeconds)

			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				holder := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				limiter.ServeHTTP(httptest.NewRecorder(), holder)
			}()
			<-started

			rec := httptest.NewRecorder()
			limiter.ServeHTTP(rec, req)

			close(release)
			wg.Wait()

			Expect(rec.Code).To(Equal(http.StatusTooManyRequests))
			Expect(slot.reason).To(Equal(rejectionReasonHostQueueTimeout))
			Expect(slot.reason).NotTo(Equal(rejectionReasonQueueTimeout))
		})

		It("leaves the reason empty when the limiter admits the request", func() {
			limiter := NewConcurrencyLimiter(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}),
				"seibert-vllm-default",
				4,
				stdtime.Second,
				nil, metrics.QueueWaitSeconds,
			)

			rec := httptest.NewRecorder()
			limiter.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(slot.reason).To(BeEmpty())
		})
	})

	Context("when the throttle gate's pacing queue is saturated", func() {
		It("records reason=throttle", func() {
			var calls atomic.Int64
			hold := make(chan struct{})
			// entered signals that a request has taken its pacing slot and
			// reached the wrapped handler, where it blocks until hold
			// closes — so a signal means that slot is held, not merely
			// contended for.
			entered := make(chan struct{}, throttleMaxPacedRequests)
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// The first request trips the gate into throttle by
				// answering 429; every later one holds its pacing slot so
				// the queue stays provably full for the request under test.
				if calls.Add(1) == 1 {
					w.WriteHeader(http.StatusTooManyRequests)
					return
				}
				entered <- struct{}{}
				<-hold
			})
			gate := NewThrottleGate(
				next,
				"seibert-vllm-default",
				1,
				100*stdtime.Millisecond,
				libtime.NewCurrentDateTime().Now,
				metrics.ThrottledTotal,
			)

			// Trip the gate: one forwarded 429 crosses threshold=1.
			gate.ServeHTTP(
				httptest.NewRecorder(),
				httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
			)

			// Fill every pacing slot and wait until each holder is inside
			// the wrapped handler. Waiting matters: firing the request
			// under test while slots were still free would let it take one
			// and block on hold, which is a hang, not a refusal.
			var wg sync.WaitGroup
			for i := 0; i < throttleMaxPacedRequests; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					gate.ServeHTTP(
						httptest.NewRecorder(),
						httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
					)
				}()
			}
			for i := 0; i < throttleMaxPacedRequests; i++ {
				<-entered
			}

			rec := httptest.NewRecorder()
			gate.ServeHTTP(rec, req)

			close(hold)
			wg.Wait()

			Expect(rec.Code).To(Equal(http.StatusTooManyRequests))
			Expect(slot.reason).To(Equal(rejectionReasonThrottle))
		})
	})

	Context("when the cold-start gate refuses a cold request", func() {
		It("records reason=cold_start", func() {
			// The gate admits a lone oversized request by design (spec 019):
			// a single cold request whose estimate exceeds the budget is let
			// through rather than starved. So the refusal has to be forced
			// with the budget already held — one cold request holds its
			// reservation while a second cannot fit behind it.
			started := make(chan struct{}, 2)
			release := make(chan struct{})
			gate := NewColdStartGate(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					started <- struct{}{}
					<-release
					w.WriteHeader(http.StatusOK)
				}),
				"seibert-vllm-default",
				1,
				stdtime.Minute,
				0,
				10*stdtime.Millisecond,
				libtime.NewCurrentDateTime().Now,
				ColdGateMetrics{
					Delayed:        metrics.ColdAdmissionDelayedTotal,
					Refused:        metrics.ColdAdmissionRefusedTotal,
					TokensInFlight: metrics.ColdTokensInFlight,
					TTFT:           metrics.ColdTTFTSeconds,
				},
			)

			coldRequest := func() *http.Request {
				body := `{"model":"m","messages":[{"role":"user","content":"` +
					strings.Repeat("x", 4000) + `"}]}`
				return httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			}

			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				gate.ServeHTTP(httptest.NewRecorder(), coldRequest())
			}()
			<-started

			cold := coldRequest()
			cold = cold.WithContext(ContextWithRejectionReason(cold.Context(), slot))

			rec := httptest.NewRecorder()
			gate.ServeHTTP(rec, cold)

			close(release)
			wg.Wait()

			Expect(rec.Code).To(Equal(http.StatusTooManyRequests))
			Expect(slot.reason).To(Equal(rejectionReasonColdStart))
		})
	})

	Context("when the circuit breaker is open", func() {
		It("records reason=circuit_open", func() {
			breaker := NewCircuitBreaker(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}),
				"seibert-vllm-default",
				1,
				libtime.NewCurrentDateTime().Now,
				metrics.BreakerStateTransitionsTotal,
			)

			// One failing response crosses failureThreshold=1 and opens it.
			breaker.ServeHTTP(
				httptest.NewRecorder(),
				httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
			)

			rec := httptest.NewRecorder()
			breaker.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusTooManyRequests))
			Expect(slot.reason).To(Equal(rejectionReasonCircuitOpen))
		})
	})

	Context("when no gate recorded a reason", func() {
		It("attributes the 429 to the upstream", func() {
			Expect(rejectionReasonOf(&rejectionSlot{})).To(Equal(rejectionReasonUpstream))
		})

		It("prefers the recorded reason when one is present", func() {
			Expect(rejectionReasonOf(&rejectionSlot{reason: rejectionReasonThrottle})).
				To(Equal(rejectionReasonThrottle))
		})

		It("tolerates a nil slot", func() {
			Expect(rejectionReasonOf(nil)).To(Equal(rejectionReasonUpstream))
		})
	})

	Context("when a gate refuses a request that an inner gate already recorded", func() {
		It("keeps the first reason rather than overwriting it", func() {
			recordRejection(req, rejectionReasonColdStart)
			recordRejection(req, rejectionReasonThrottle)

			Expect(slot.reason).To(Equal(rejectionReasonColdStart))
		})

		It("is a no-op when the request carries no slot", func() {
			plain := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

			Expect(func() { recordRejection(plain, rejectionReasonThrottle) }).NotTo(Panic())
		})
	})

	Context("when the rejection series are pre-initialized", func() {
		It("creates every reason at zero so rate() reads 0 rather than no data", func() {
			reg := prometheus.NewRegistry()
			Expect(metrics.Register(reg)).To(Succeed())
			metrics.PreInitRejection("seibert-vllm-default")

			for _, reason := range rejectionReasons {
				Expect(rejectionSeriesValue(reg, "seibert-vllm-default", reason)).
					To(Equal(float64(0)), "reason %q", reason)
			}
			Expect(rejectionHistogramSampleCount(reg, "seibert-vllm-default")).To(Equal(float64(0)))
		})

		It("leaves an unconfigured provider's series absent", func() {
			reg := prometheus.NewRegistry()
			Expect(metrics.Register(reg)).To(Succeed())
			metrics.PreInitRejection("seibert-vllm-default")

			Expect(rejectionSeriesValue(reg, "other-provider", rejectionReasonUpstream)).
				To(Equal(float64(-1)))
		})
	})

	Context("when a request queues for a concurrency slot", func() {
		It("observes the wait on the acquire path", func() {
			// Buffered, not closed: the wrapped handler runs twice here —
			// once for the slot holder and once for the queued request that
			// takes the slot when it is released — so a close would panic
			// on the second entry.
			started := make(chan struct{}, 2)
			release := make(chan struct{})
			limiter := NewConcurrencyLimiter(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					started <- struct{}{}
					<-release
				}),
				"seibert-vllm-default",
				1,
				stdtime.Second,
				nil, metrics.QueueWaitSeconds,
			)

			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				limiter.ServeHTTP(
					httptest.NewRecorder(),
					httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
				)
			}()
			<-started

			queued := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			go func() {
				stdtime.Sleep(20 * stdtime.Millisecond)
				close(release)
			}()
			limiter.ServeHTTP(httptest.NewRecorder(), queued)
			wg.Wait()

			reg := prometheus.NewRegistry()
			Expect(metrics.Register(reg)).To(Succeed())
			Expect(rejectionHistogramSampleCount(reg, "seibert-vllm-default")).To(Equal(float64(1)))
		})

		It("does not observe a request that takes a free slot immediately", func() {
			limiter := NewConcurrencyLimiter(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}),
				"seibert-vllm-default",
				4,
				stdtime.Second,
				nil, metrics.QueueWaitSeconds,
			)

			limiter.ServeHTTP(
				httptest.NewRecorder(),
				httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
			)

			reg := prometheus.NewRegistry()
			Expect(metrics.Register(reg)).To(Succeed())
			// The provider's histogram is seeded in production by
			// PreInitRejection, which this spec deliberately does not call:
			// an unobserved provider therefore reads -1, which is the
			// assertion — an immediate acquire must not create the series.
			Expect(
				rejectionHistogramSampleCount(reg, "seibert-vllm-default"),
			).To(Equal(float64(-1)))
		})
	})
})

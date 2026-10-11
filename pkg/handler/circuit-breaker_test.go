// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Circuit breaker specs (spec 020): a consecutive-upstream-failure gate that
// opens at the configured threshold, answers 429 with the shared static body
// and a 30s Retry-After while open, admits exactly one half-open probe after
// the fixed open period, and closes on a healthy probe. Rows drive the
// injected clock so an open period never sleeps wall-clock.

package handler_test

import (
	"context"
	"flag"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// breakerStateOf returns the breaker's state label. h must be a real breaker;
// the disabled path returns next unchanged and has no breaker.
func breakerStateOf(h http.Handler) string {
	b, ok := h.(interface{ State() string })
	if !ok {
		panic("breakerStateOf: handler is not a real circuit breaker")
	}
	return b.State()
}

// newBreakerCounter returns a fresh state-transition CounterVec so each row
// asserts on an isolated counter.
func newBreakerCounter() *prometheus.CounterVec {
	return prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "ccrouter_breaker_state_transitions_total", Help: "test"},
		[]string{"provider", "state"},
	)
}

// zeroableStub records each entry and writes the configured status — or
// nothing at all when the status is 0. The shared gateStub always calls
// WriteHeader, which panics on the httptest recorder for status 0, so it
// cannot express the client-disconnect / no-status-written case.
type zeroableStub struct {
	calls  atomic.Int32
	status atomic.Int32
}

func (s *zeroableStub) entryCount() int { return int(s.calls.Load()) }

func (s *zeroableStub) setStatus(status int) { s.status.Store(int32(status)) }

func (s *zeroableStub) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	s.calls.Add(1)
	if s.status.Load() == 0 {
		return
	}
	w.WriteHeader(int(s.status.Load()))
}

// firstBlockStub blocks only the first request on release and serves every
// later request immediately with the configured status. The row uses it to
// hold one non-probe request in flight while other requests open the breaker.
type firstBlockStub struct {
	calls   atomic.Int32
	status  atomic.Int32
	release chan struct{}
}

func newFirstBlockStub(status int) *firstBlockStub {
	s := &firstBlockStub{release: make(chan struct{})}
	s.setStatus(status)
	return s
}

func (s *firstBlockStub) entryCount() int { return int(s.calls.Load()) }

func (s *firstBlockStub) setStatus(status int) { s.status.Store(int32(status)) }

func (s *firstBlockStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.calls.Add(1) == 1 {
		select {
		case <-s.release:
		case <-r.Context().Done():
			return
		}
	}
	w.WriteHeader(int(s.status.Load()))
}

var _ = Describe("CircuitBreaker", func() {
	It("returns next unchanged when the threshold is 0 or negative — byte-for-byte no-op", func() {
		clock := newClock()
		for _, threshold := range []int{0, -1} {
			inner := newGateStub(http.StatusOK, "ok")
			cb := handler.NewCircuitBreaker(inner, "p", threshold, clock.Now, nil)
			Expect(cb).To(BeIdenticalTo(http.Handler(inner)))

			rec := httptest.NewRecorder()
			cb.ServeHTTP(rec, newMessagesRequest())
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(inner.entryCount()).To(Equal(1))
		}

		cv := newBreakerCounter()
		inner := newGateStub(http.StatusOK, "ok")
		handler.NewCircuitBreaker(inner, "p", 0, clock.Now, cv)
		Expect(testutil.CollectAndCount(cv)).To(Equal(0), "a disabled breaker creates no series")
	})

	It(
		"opens at the threshold and answers 429 with Retry-After 30 without invoking the upstream",
		func() {
			clock := newClock()
			stub := newGateStub(http.StatusInternalServerError, "")
			cb := handler.NewCircuitBreaker(stub, "p", 5, clock.Now, nil)

			for i := 0; i < 5; i++ {
				rec := httptest.NewRecorder()
				cb.ServeHTTP(rec, newMessagesRequest())
				Expect(rec.Code).To(Equal(http.StatusInternalServerError))
			}
			Expect(stub.entryCount()).To(Equal(5))
			Expect(breakerStateOf(cb)).To(Equal("open"))

			rec := httptest.NewRecorder()
			cb.ServeHTTP(rec, newMessagesRequest())
			Expect(rec.Code).To(Equal(http.StatusTooManyRequests))
			Expect(rec.Body.String()).To(Equal(expected429Body))
			Expect(rec.Header().Get("Retry-After")).To(Equal("30"))
			Expect(stub.entryCount()).To(Equal(5), "the upstream must not be invoked while open")
		},
	)

	It("never answers with a router-made 5xx while open", func() {
		clock := newClock()
		stub := newGateStub(http.StatusInternalServerError, "")
		cb := handler.NewCircuitBreaker(stub, "p", 2, clock.Now, nil)
		for i := 0; i < 2; i++ {
			cb.ServeHTTP(httptest.NewRecorder(), newMessagesRequest())
		}
		Expect(breakerStateOf(cb)).To(Equal("open"))

		// The upstream recovers, but the breaker is still inside its open
		// period: every response must be the router's 429, never a 5xx.
		stub.setStatus(http.StatusOK)
		for i := 0; i < 5; i++ {
			rec := httptest.NewRecorder()
			cb.ServeHTTP(rec, newMessagesRequest())
			Expect(rec.Code).To(Equal(http.StatusTooManyRequests))
			Expect(rec.Body.String()).To(Equal(expected429Body))
			Expect(
				rec.Code,
			).To(BeNumerically("<", 500), "the router never answers a 5xx of its own")
		}
	})

	It(
		"admits one probe after the open period and closes on a healthy probe, resetting the count",
		func() {
			clock := newClock()
			stub := newGateStub(http.StatusInternalServerError, "")
			cb := handler.NewCircuitBreaker(stub, "p", 3, clock.Now, nil)
			for i := 0; i < 3; i++ {
				cb.ServeHTTP(httptest.NewRecorder(), newMessagesRequest())
			}
			Expect(breakerStateOf(cb)).To(Equal("open"))

			clock.SetNow(libtime.DateTime(t0.Time().Add(30 * stdtime.Second)))
			stub.setStatus(http.StatusOK)
			rec := httptest.NewRecorder()
			cb.ServeHTTP(rec, newMessagesRequest())
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(stub.entryCount()).To(Equal(4), "the probe reaches the upstream")
			Expect(breakerStateOf(cb)).To(Equal("closed"))

			// The count is reset: threshold-1 further failures keep it closed.
			stub.setStatus(http.StatusInternalServerError)
			for i := 0; i < 2; i++ {
				cb.ServeHTTP(httptest.NewRecorder(), newMessagesRequest())
			}
			Expect(breakerStateOf(cb)).To(Equal("closed"))
		},
	)

	It("reopens on a failing probe and admits a second probe after a further open period", func() {
		clock := newClock()
		stub := newGateStub(http.StatusInternalServerError, "")
		cb := handler.NewCircuitBreaker(stub, "p", 2, clock.Now, nil)
		for i := 0; i < 2; i++ {
			cb.ServeHTTP(httptest.NewRecorder(), newMessagesRequest())
		}
		Expect(breakerStateOf(cb)).To(Equal("open"))

		clock.SetNow(libtime.DateTime(t0.Time().Add(30 * stdtime.Second)))
		probeRec := httptest.NewRecorder()
		cb.ServeHTTP(probeRec, newMessagesRequest())
		Expect(
			probeRec.Code,
		).To(Equal(http.StatusInternalServerError), "the failing probe passes through")
		Expect(stub.entryCount()).To(Equal(3))
		Expect(breakerStateOf(cb)).To(Equal("open"), "a failing probe reopens the breaker")

		refused := httptest.NewRecorder()
		cb.ServeHTTP(refused, newMessagesRequest())
		Expect(refused.Code).To(Equal(http.StatusTooManyRequests))
		Expect(
			stub.entryCount(),
		).To(Equal(3), "the upstream is not invoked for a further open period")

		clock.SetNow(libtime.DateTime(t0.Time().Add(60 * stdtime.Second)))
		cb.ServeHTTP(httptest.NewRecorder(), newMessagesRequest())
		Expect(stub.entryCount()).To(Equal(4), "a second open period admits a second probe")
	})

	It(
		"admits exactly one probe — concurrent requests are 429 while the probe is in flight",
		func() {
			clock := newClock()
			stub := newGateStub(http.StatusInternalServerError, "")
			cb := handler.NewCircuitBreaker(stub, "p", 2, clock.Now, nil)
			for i := 0; i < 2; i++ {
				cb.ServeHTTP(httptest.NewRecorder(), newMessagesRequest())
			}
			Expect(breakerStateOf(cb)).To(Equal("open"))

			stub.block = make(chan struct{})
			clock.SetNow(libtime.DateTime(t0.Time().Add(30 * stdtime.Second)))
			probeDone := serveAsync(cb, httptest.NewRecorder(), newMessagesRequest())
			Eventually(
				stub.entryCount,
				"1s",
				"10ms",
			).Should(Equal(3), "the probe reaches the upstream")

			for i := 0; i < 5; i++ {
				rec := httptest.NewRecorder()
				cb.ServeHTTP(rec, newMessagesRequest())
				Expect(rec.Code).To(Equal(http.StatusTooManyRequests))
				Expect(rec.Body.String()).To(Equal(expected429Body))
			}
			Expect(stub.entryCount()).To(Equal(3), "exactly one probe reaches the upstream")

			close(stub.block)
			Eventually(probeDone, "1s").Should(BeClosed())
		},
	)

	It(
		"keeps per-provider breakers independent — including two providers sharing one upstream",
		func() {
			clock := newClock()
			stub := newGateStub(http.StatusInternalServerError, "")
			cbA := handler.NewCircuitBreaker(stub, "a", 2, clock.Now, nil)
			cbB := handler.NewCircuitBreaker(stub, "b", 2, clock.Now, nil)

			for i := 0; i < 2; i++ {
				cbA.ServeHTTP(httptest.NewRecorder(), newMessagesRequest())
			}
			Expect(breakerStateOf(cbA)).To(Equal("open"))

			stub.setStatus(http.StatusOK)
			rec := httptest.NewRecorder()
			cbB.ServeHTTP(rec, newMessagesRequest())
			Expect(rec.Code).To(Equal(http.StatusOK), "B forwards normally while A is open")
			Expect(stub.entryCount()).To(Equal(3))
			Expect(breakerStateOf(cbB)).To(Equal("closed"))
		},
	)

	It("pre-initializes the three state series to 0 and increments on transition", func() {
		clock := newClock()
		cv := newBreakerCounter()
		stub := newGateStub(http.StatusInternalServerError, "")
		cb := handler.NewCircuitBreaker(stub, "p", 2, clock.Now, cv)

		Expect(testutil.ToFloat64(cv.WithLabelValues("p", "closed"))).To(Equal(float64(0)))
		Expect(testutil.ToFloat64(cv.WithLabelValues("p", "open"))).To(Equal(float64(0)))
		Expect(testutil.ToFloat64(cv.WithLabelValues("p", "half_open"))).To(Equal(float64(0)))

		for i := 0; i < 2; i++ {
			cb.ServeHTTP(httptest.NewRecorder(), newMessagesRequest())
		}
		Expect(testutil.ToFloat64(cv.WithLabelValues("p", "open"))).To(Equal(float64(1)))

		// Drive the open → half-open → closed path so all three label
		// spellings are exercised through the production mapper.
		clock.SetNow(libtime.DateTime(t0.Time().Add(30 * stdtime.Second)))
		stub.setStatus(http.StatusOK)
		cb.ServeHTTP(httptest.NewRecorder(), newMessagesRequest())
		Expect(testutil.ToFloat64(cv.WithLabelValues("p", "half_open"))).To(Equal(float64(1)))
		Expect(testutil.ToFloat64(cv.WithLabelValues("p", "closed"))).To(Equal(float64(1)))
	})

	It("logs each state transition with the provider and the hyphenated state name", func() {
		Expect(flag.Set("logtostderr", "true")).To(Succeed())
		clock := newClock()
		stub := newGateStub(http.StatusInternalServerError, "")
		cb := handler.NewCircuitBreaker(stub, "p", 2, clock.Now, nil)

		out := captureStderr(func() {
			for i := 0; i < 2; i++ {
				cb.ServeHTTP(httptest.NewRecorder(), newMessagesRequest())
			}
			clock.SetNow(libtime.DateTime(t0.Time().Add(30 * stdtime.Second)))
			stub.setStatus(http.StatusOK)
			cb.ServeHTTP(httptest.NewRecorder(), newMessagesRequest())
		})
		Expect(out).To(ContainSubstring("[breaker] provider=p state=open"))
		Expect(out).To(ContainSubstring("[breaker] provider=p state=half-open"))
		Expect(out).To(ContainSubstring("[breaker] provider=p state=closed"))
	})

	It("resets the consecutive-failure count on a non-5xx response", func() {
		clock := newClock()
		stub := newGateStub(http.StatusInternalServerError, "")
		cb := handler.NewCircuitBreaker(stub, "p", 3, clock.Now, nil)

		for i := 0; i < 2; i++ {
			cb.ServeHTTP(httptest.NewRecorder(), newMessagesRequest())
		}
		stub.setStatus(http.StatusOK)
		cb.ServeHTTP(httptest.NewRecorder(), newMessagesRequest())
		stub.setStatus(http.StatusInternalServerError)
		for i := 0; i < 2; i++ {
			cb.ServeHTTP(httptest.NewRecorder(), newMessagesRequest())
		}
		Expect(breakerStateOf(cb)).To(Equal("closed"), "a non-5xx response resets the count")
	})

	It("does not reset the failure count on a status-0 response", func() {
		clock := newClock()
		stub := &zeroableStub{}
		stub.setStatus(http.StatusInternalServerError)
		cb := handler.NewCircuitBreaker(stub, "p", 3, clock.Now, nil)

		for i := 0; i < 2; i++ {
			cb.ServeHTTP(httptest.NewRecorder(), newMessagesRequest())
		}
		stub.setStatus(0)
		cb.ServeHTTP(httptest.NewRecorder(), newMessagesRequest())
		stub.setStatus(http.StatusInternalServerError)
		cb.ServeHTTP(httptest.NewRecorder(), newMessagesRequest())

		Expect(breakerStateOf(cb)).To(Equal("open"), "the status-0 request left the count intact")
	})

	It("counts 504 and 502 upstream failures toward the breaker", func() {
		for _, status := range []int{http.StatusGatewayTimeout, http.StatusBadGateway} {
			clock := newClock()
			stub := newGateStub(status, "")
			cb := handler.NewCircuitBreaker(stub, "p", 2, clock.Now, nil)
			for i := 0; i < 2; i++ {
				cb.ServeHTTP(httptest.NewRecorder(), newMessagesRequest())
			}
			Expect(breakerStateOf(cb)).To(Equal("open"), "a %d counts as a failure", status)
		}
	})

	It("ignores a non-probe verdict once the breaker has left closed", func() {
		clock := newClock()
		stub := newFirstBlockStub(http.StatusInternalServerError)
		cb := handler.NewCircuitBreaker(stub, "p", 2, clock.Now, nil)

		// One slow request is admitted while closed and held in flight.
		slowDone := serveAsync(cb, httptest.NewRecorder(), newMessagesRequest())
		Eventually(stub.entryCount, "1s", "10ms").Should(Equal(1))

		// Two later requests open the breaker.
		for i := 0; i < 2; i++ {
			cb.ServeHTTP(httptest.NewRecorder(), newMessagesRequest())
		}
		Expect(breakerStateOf(cb)).To(Equal("open"))

		// The slow request now returns 500 — a non-probe verdict while the
		// breaker is no longer closed, so it must be ignored.
		close(stub.release)
		Eventually(slowDone, "1s").Should(BeClosed())
		Expect(breakerStateOf(cb)).To(Equal("open"))
	})

	It("does not count a client disconnect as a failure", func() {
		clock := newClock()
		stub := &zeroableStub{}
		cb := handler.NewCircuitBreaker(stub, "p", 1, clock.Now, nil)

		cancelledCtx, cancel := context.WithCancel(context.Background())
		cancel()
		req := newMessagesRequest().WithContext(cancelledCtx)
		cb.ServeHTTP(httptest.NewRecorder(), req)

		Expect(stub.entryCount()).To(Equal(1))
		Expect(breakerStateOf(cb)).To(Equal("closed"), "a disconnected client is never a failure")
	})
})

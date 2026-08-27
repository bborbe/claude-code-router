// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Adaptive 429 delay gate specs (spec 018): the gate paces requests to a
// provider once a windowed count of upstream 429 responses reaches the
// threshold, following AIMD — 1s entry delay, ×2 per observed 429 (capped
// at the max), ÷2 per clean 60s window, exit below the 1s floor. The 429'd
// request is never retried; the pacing queue is bounded and sheds excess
// arrivals with the static Anthropic-shaped 429 body. Rows drive the
// detector directly through handler.ThrottleGateObserve on the injected
// clock so a growing delay never sleeps wall-clock; only the AC-mandated
// small explicit max delays (50–100ms) actually sleep.

package handler_test

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
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// t0 is the fixed clock time every injected-clock row observes at (spec
// 018); the recovery row advances it in 60s steps.
var t0 = libtime.DateTime(stdtime.Date(2026, 8, 27, 12, 0, 0, 0, stdtime.UTC))

// newClock returns the fixed injected clock pinned at t0, so windowed 429
// counting never expires observations and the detector's time arithmetic is
// deterministic.
func newClock() libtime.CurrentDateTime {
	clock := libtime.NewCurrentDateTime()
	clock.SetNow(t0)
	return clock
}

// newCounterVec returns a fresh ccrouter_throttled_total CounterVec so each
// row asserts on an isolated counter.
func newCounterVec() *prometheus.CounterVec {
	return prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "ccrouter_throttled_total", Help: "test"},
		[]string{"provider"},
	)
}

// throttled reports whether h is a real throttle gate currently in throttle.
// The disabled path returns next unchanged and has no gate (mirrors the
// concurrency limiter's InFlight accessor). h must be a real gate; every
// row constructs one.
func throttled(h http.Handler) bool {
	g, ok := h.(interface{ Throttled() bool })
	if !ok {
		panic("throttled: handler is not a real throttle gate")
	}
	return g.Throttled()
}

// delay returns the gate's current pacing delay (0 when not throttled). h
// must be a real gate; every row constructs one.
func delay(h http.Handler) stdtime.Duration {
	g, ok := h.(interface{ Delay() stdtime.Duration })
	if !ok {
		panic("delay: handler is not a real throttle gate")
	}
	return g.Delay()
}

// gateStub is the configurable upstream stub: it records each invocation in
// an atomic counter, returns the configured status/body (status read per
// call so rows can flip it between synchronous requests), and when block is
// non-nil waits on it after recording entry — also selecting on the request
// context so teardown is leak-free.
type gateStub struct {
	calls  atomic.Int32
	block  chan struct{}
	status atomic.Int32
	body   string
}

func newGateStub(status int, body string) *gateStub {
	s := &gateStub{body: body}
	s.status.Store(int32(status))
	return s
}

func (s *gateStub) entryCount() int {
	return int(s.calls.Load())
}

func (s *gateStub) setStatus(status int) {
	s.status.Store(int32(status))
}

func (s *gateStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.calls.Add(1)
	if s.block != nil {
		select {
		case <-s.block:
		case <-r.Context().Done():
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(int(s.status.Load()))
	if s.body != "" {
		_, _ = w.Write([]byte(s.body))
	}
}

// fireConcurrent launches n requests through the gate concurrently, each with
// its own recorder and done channel, so the overflow row can saturate the
// pacing queue deterministically (spec 018 DB 3).
func fireConcurrent(gate http.Handler, n int) ([]*httptest.ResponseRecorder, []chan struct{}) {
	recs := make([]*httptest.ResponseRecorder, n)
	dones := make([]chan struct{}, n)
	for i := 0; i < n; i++ {
		recs[i] = httptest.NewRecorder()
		dones[i] = make(chan struct{})
		go func(i int) {
			defer close(dones[i])
			gate.ServeHTTP(recs[i], newMessagesRequest())
		}(i)
	}
	return recs, dones
}

// closedCount returns how many of the done channels have closed.
func closedCount(dones []chan struct{}) int {
	n := 0
	for _, d := range dones {
		select {
		case <-d:
			n++
		default:
		}
	}
	return n
}

// shedIndex returns the index of the single closed done channel — the one
// request the bounded pacing queue shed with a 429.
func shedIndex(dones []chan struct{}) int {
	for i, d := range dones {
		select {
		case <-d:
			return i
		default:
		}
	}
	return -1
}

var _ = Describe("ThrottleGate", func() {
	It("returns next unchanged when the threshold is 0 or negative — byte-for-byte no-op", func() {
		clock := newClock()
		cv := newCounterVec()
		for _, threshold := range []int{0, -1} {
			inner := newGateStub(http.StatusOK, "ok")
			gate := handler.NewThrottleGate(inner, "p", threshold, stdtime.Second, clock.Now, cv)
			Expect(gate).To(BeIdenticalTo(http.Handler(inner)))

			rec := httptest.NewRecorder()
			gate.ServeHTTP(rec, newMessagesRequest())
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(inner.entryCount()).To(Equal(1))
			Expect(testutil.CollectAndCount(cv)).To(Equal(0))
		}
	})

	It("engages at the threshold and paces later requests by the capped delay", func() {
		clock := newClock()
		stub := newGateStub(http.StatusTooManyRequests, "")
		gate := handler.NewThrottleGate(
			stub,
			"p",
			3,
			100*stdtime.Millisecond,
			clock.Now,
			newCounterVec(),
		)

		for i := 0; i < 3; i++ {
			rec := httptest.NewRecorder()
			gate.ServeHTTP(rec, newMessagesRequest())
			Expect(
				rec.Code,
			).To(Equal(http.StatusTooManyRequests), "the 429s pass through before the gate engages")
		}
		Expect(throttled(gate)).To(BeTrue())
		Expect(
			delay(gate),
		).To(Equal(100*stdtime.Millisecond), "entry delay 1s capped at the 100ms max")

		stub.setStatus(http.StatusOK)
		rec4 := httptest.NewRecorder()
		done := serveAsync(gate, rec4, newMessagesRequest())
		Consistently(stub.entryCount, "50ms", "10ms").
			Should(Equal(3), "request 4 must be held for the pacing delay")
		Eventually(stub.entryCount, "500ms", "10ms").Should(Equal(4))
		Eventually(done, "1s").Should(BeClosed())
		Expect(rec4.Code).To(Equal(http.StatusOK))
	})

	It("passes the upstream 429 through unchanged — never retried, never rewritten", func() {
		clock := newClock()
		upstreamBody := `{"type":"error","error":{"type":"rate_limit_error","message":"zai upstream says no"}}`
		stub := newGateStub(http.StatusTooManyRequests, upstreamBody)
		gate := handler.NewThrottleGate(
			stub,
			"p",
			3,
			50*stdtime.Millisecond,
			clock.Now,
			newCounterVec(),
		)

		for i := 0; i < 3; i++ {
			handler.ThrottleGateObserve(gate, http.StatusTooManyRequests, t0)
		}
		Expect(throttled(gate)).To(BeTrue())

		rec := httptest.NewRecorder()
		done := serveAsync(gate, rec, newMessagesRequest())
		Eventually(
			stub.entryCount,
			"1s",
			"10ms",
		).Should(Equal(1), "the request must be forwarded exactly once")
		Eventually(done, "1s").Should(BeClosed())

		Expect(rec.Code).To(Equal(http.StatusTooManyRequests))
		Expect(
			rec.Body.String(),
		).To(Equal(upstreamBody), "the router must not rewrite the upstream's 429 body")
		Expect(
			rec.Body.String(),
		).NotTo(Equal(expected429Body), "it is not the router's static pacing body")
		Consistently(stub.entryCount, "200ms", "20ms").Should(Equal(1), "the 429 is never re-sent")
	})

	It("doubles the delay per 429 and caps it at the max — no wall-clock sleep", func() {
		clock := newClock()
		gate := handler.NewThrottleGate(
			newGateStub(http.StatusOK, ""),
			"p",
			3,
			5*stdtime.Second,
			clock.Now,
			newCounterVec(),
		)

		for i := 0; i < 3; i++ {
			handler.ThrottleGateObserve(gate, http.StatusTooManyRequests, t0)
		}
		Expect(throttled(gate)).To(BeTrue())
		Expect(delay(gate)).To(Equal(stdtime.Second))

		for _, want := range []stdtime.Duration{2 * stdtime.Second, 4 * stdtime.Second} {
			handler.ThrottleGateObserve(gate, http.StatusTooManyRequests, t0)
			Expect(delay(gate)).To(Equal(want))
		}
		for i := 0; i < 3; i++ {
			handler.ThrottleGateObserve(gate, http.StatusTooManyRequests, t0)
		}
		Expect(delay(gate)).To(Equal(5*stdtime.Second), "the delay must never exceed the max")
	})

	It("recovers: a clean 60s window halves the delay, below the floor exits throttle", func() {
		clock := newClock()
		stub := newGateStub(http.StatusOK, "")
		gate := handler.NewThrottleGate(stub, "p", 3, 5*stdtime.Second, clock.Now, newCounterVec())

		for i := 0; i < 4; i++ {
			handler.ThrottleGateObserve(gate, http.StatusTooManyRequests, t0)
		}
		Expect(throttled(gate)).To(BeTrue())
		Expect(delay(gate)).To(Equal(2 * stdtime.Second))

		handler.ThrottleGateObserve(
			gate,
			http.StatusOK,
			libtime.DateTime(t0.Time().Add(60*stdtime.Second)),
		)
		Expect(delay(gate)).To(Equal(stdtime.Second), "exactly one halving per clean window")
		Expect(throttled(gate)).To(BeTrue())

		handler.ThrottleGateObserve(
			gate,
			http.StatusOK,
			libtime.DateTime(t0.Time().Add(120*stdtime.Second)),
		)
		Expect(
			delay(gate),
		).To(Equal(stdtime.Duration(0)), "below the 1s floor the provider exits throttle")
		Expect(throttled(gate)).To(BeFalse())

		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, newMessagesRequest())
		Expect(rec.Code).To(Equal(http.StatusOK), "a recovered provider forwards undelayed")
		Expect(stub.entryCount()).To(Equal(1))
	})

	It("answers 429 with the static body when the bounded pacing queue saturates", func() {
		clock := newClock()
		stub := newGateStub(http.StatusOK, "ok")
		stub.block = make(chan struct{})
		gate := handler.NewThrottleGate(
			stub,
			"p",
			1,
			100*stdtime.Millisecond,
			clock.Now,
			newCounterVec(),
		)
		handler.ThrottleGateObserve(gate, http.StatusTooManyRequests, t0)
		Expect(throttled(gate)).To(BeTrue())

		total := handler.ThrottleMaxPacedRequests + 1
		recs, dones := fireConcurrent(gate, total)

		// Exactly one request is shed: it cannot acquire a pacing slot within
		// the 100ms max delay and is answered 429 with the static body.
		Eventually(func() int { return closedCount(dones) }, "1s", "10ms").Should(Equal(1))
		shed := shedIndex(dones)
		Expect(recs[shed].Code).To(Equal(http.StatusTooManyRequests))
		Expect(recs[shed].Header().Get("Content-Type")).To(ContainSubstring("application/json"))
		Expect(recs[shed].Body.String()).To(Equal(expected429Body))

		// The 32 slot-holders reach the upstream at steady state. Asserted
		// separately from the 429 close: both fire at the same ~100ms
		// deadline, so the entry count at the instant of the 429 is a race.
		Eventually(stub.entryCount, "1s", "10ms").Should(Equal(handler.ThrottleMaxPacedRequests))

		close(stub.block)
		Eventually(func() int { return closedCount(dones) }, "1s", "10ms").Should(Equal(total))
		for i, rec := range recs {
			if i != shed {
				Expect(rec.Code).To(Equal(http.StatusOK))
			}
		}
	})

	It("keeps per-provider gates independent — throttling one never delays another", func() {
		clock := newClock()
		stubA := newGateStub(http.StatusOK, "ok")
		stubA.block = make(chan struct{})
		stubB := newGateStub(http.StatusOK, "ok")
		gateA := handler.NewThrottleGate(
			stubA,
			"a",
			3,
			100*stdtime.Millisecond,
			clock.Now,
			newCounterVec(),
		)
		gateB := handler.NewThrottleGate(
			stubB,
			"b",
			3,
			100*stdtime.Millisecond,
			clock.Now,
			newCounterVec(),
		)

		for i := 0; i < 3; i++ {
			handler.ThrottleGateObserve(gateA, http.StatusTooManyRequests, t0)
		}
		Expect(throttled(gateA)).To(BeTrue())
		Expect(throttled(gateB)).To(BeFalse())

		recA := httptest.NewRecorder()
		doneA := serveAsync(gateA, recA, newMessagesRequest())

		// B is never throttled and never blocked by A's throttle: its request
		// completes immediately while A's is still within its pacing delay.
		recB := httptest.NewRecorder()
		gateB.ServeHTTP(recB, newMessagesRequest())
		Expect(recB.Code).To(Equal(http.StatusOK))
		Expect(stubB.entryCount()).To(Equal(1))
		Expect(stubA.entryCount()).To(Equal(0), "A's request is still within its pacing delay")

		close(stubA.block)
		Eventually(doneA, "1s").Should(BeClosed())
		Expect(recA.Code).To(Equal(http.StatusOK))
		Expect(stubA.entryCount()).To(Equal(1))
	})

	It("increments the throttled counter once per paced request — additive only", func() {
		clock := newClock()
		cv := newCounterVec()
		stub := newGateStub(http.StatusOK, "ok")
		gate := handler.NewThrottleGate(stub, "p", 3, 50*stdtime.Millisecond, clock.Now, cv)

		for i := 0; i < 3; i++ {
			handler.ThrottleGateObserve(gate, http.StatusTooManyRequests, t0)
		}
		Expect(throttled(gate)).To(BeTrue())
		Expect(testutil.CollectAndCount(cv)).To(Equal(0), "no series before any paced request")

		rec1 := httptest.NewRecorder()
		gate.ServeHTTP(rec1, newMessagesRequest())
		Expect(rec1.Code).To(Equal(http.StatusOK))
		Expect(testutil.ToFloat64(cv.WithLabelValues("p"))).To(Equal(float64(1)))

		rec2 := httptest.NewRecorder()
		gate.ServeHTTP(rec2, newMessagesRequest())
		Expect(rec2.Code).To(Equal(http.StatusOK))
		Expect(testutil.ToFloat64(cv.WithLabelValues("p"))).To(Equal(float64(2)))
	})

	It("never forwards or holds a pacing slot for a client that disconnects while waiting", func() {
		clock := newClock()
		stub := newGateStub(http.StatusOK, "ok")
		gate := handler.NewThrottleGate(
			stub,
			"p",
			1,
			50*stdtime.Millisecond,
			clock.Now,
			newCounterVec(),
		)
		handler.ThrottleGateObserve(gate, http.StatusTooManyRequests, t0)
		Expect(throttled(gate)).To(BeTrue())

		cancelledCtx, cancel := context.WithCancel(context.Background())
		cancel()
		req := newMessagesRequest().WithContext(cancelledCtx)
		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, req)
		Expect(stub.entryCount()).To(Equal(0), "a disconnected client must never be forwarded")

		// The gate still functions for live clients afterwards.
		rec2 := httptest.NewRecorder()
		gate.ServeHTTP(rec2, newMessagesRequest())
		Expect(rec2.Code).To(Equal(http.StatusOK))
		Expect(stub.entryCount()).To(Equal(1))
	})
})

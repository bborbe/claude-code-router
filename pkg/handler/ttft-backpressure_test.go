// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// TTFT backpressure controller specs (spec 020): the per-provider controller
// watches cold time-to-first-token over fixed 60s windows and halves, grows,
// or holds the effective cold-prefill budget, which the cold-start gate reads
// live. Every row drives the injected clock so a window boundary never sleeps
// wall-clock; only the gate rows' explicit 100ms max wait actually waits.

package handler_test

import (
	"context"
	"flag"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// bpAt advances the clock to t0 plus d and returns the new time.
func bpAt(d stdtime.Duration) libtime.DateTime {
	return libtime.DateTime(t0.Time().Add(d))
}

// bpNewGauge returns a fresh backpressure-budget gauge so each gauge row
// asserts on an isolated series.
func bpNewGauge() *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "test_backpressure_budget", Help: "test"},
		[]string{"provider"},
	)
}

// bpBigBody is a body whose cold estimate is 50000 tokens
// (175000 * 2 / 7), between the 32768 half and the 65536 ceiling, so it is
// admitted at the ceiling and refused once the budget has halved.
const bpBigBody = 175_000

var _ = Describe("TTFTBackpressure", func() {
	var clock libtime.CurrentDateTime

	BeforeEach(func() {
		clock = newClock()
	})

	It("AC 4 — halves once per window on a median above the threshold", func() {
		bp := handler.NewTTFTBackpressure("p", 65536, 20, clock.Now, nil)

		for i := 0; i < 3; i++ {
			bp.ObserveColdTTFT(25)
		}
		Expect(bp.Budget()).To(Equal(65536), "no boundary crossed yet")

		clock.SetNow(bpAt(61 * stdtime.Second))
		bp.ObserveColdTTFT(25)
		Expect(bp.Budget()).To(Equal(32768), "halved exactly once for the three-sample window")
	})

	It("AC 4 — halves on a failure even with no cold sample in the window", func() {
		bp := handler.NewTTFTBackpressure("p", 65536, 20, clock.Now, nil)

		bp.ObserveStatus(http.StatusInternalServerError)
		Expect(bp.Budget()).To(Equal(65536))

		clock.SetNow(bpAt(61 * stdtime.Second))
		bp.ObserveStatus(http.StatusOK)
		Expect(bp.Budget()).To(Equal(32768))
	})

	It(
		"AC 5 — grows by the fixed step on consecutive clean windows and caps at the ceiling",
		func() {
			bp := handler.NewTTFTBackpressure("p", 65536, 20, clock.Now, nil)

			bp.ObserveColdTTFT(25)
			clock.SetNow(bpAt(61 * stdtime.Second))
			bp.ObserveColdTTFT(1.0)
			Expect(bp.Budget()).To(Equal(32768))

			want := 32768
			for i := 0; i < 5; i++ {
				clock.SetNow(bpAt(stdtime.Duration(122+i*61) * stdtime.Second))
				bp.ObserveColdTTFT(1.0)
				want += 8192
				if want > 65536 {
					want = 65536
				}
				Expect(
					bp.Budget(),
				).To(Equal(want), "the budget never exceeds the configured ceiling")
			}
			Expect(bp.Budget()).To(Equal(65536))
		},
	)

	It("AC 5 — holds the budget when the median sits in the hysteresis band", func() {
		bp := handler.NewTTFTBackpressure("p", 65536, 20, clock.Now, nil)

		bp.ObserveColdTTFT(25)
		clock.SetNow(bpAt(61 * stdtime.Second))
		bp.ObserveColdTTFT(15) // roll: halve on the 25s window; W1 holds a 15s sample
		Expect(bp.Budget()).To(Equal(32768))

		clock.SetNow(bpAt(122 * stdtime.Second))
		bp.ObserveColdTTFT(15) // roll: 15 is between the 8s line and the 20s threshold — hold
		Expect(bp.Budget()).To(Equal(32768), "the hysteresis band must not oscillate")
	})

	It("AC 5 — grows on an empty failure-free window", func() {
		bp := handler.NewTTFTBackpressure("p", 65536, 20, clock.Now, nil)

		bp.ObserveColdTTFT(25)
		clock.SetNow(bpAt(61 * stdtime.Second))
		bp.ObserveStatus(http.StatusOK) // roll: halve on the 25s window; W1 has no sample
		Expect(bp.Budget()).To(Equal(32768))

		clock.SetNow(bpAt(122 * stdtime.Second))
		bp.ObserveStatus(http.StatusOK) // roll: W1 is empty and clean — grow
		Expect(bp.Budget()).To(Equal(40960))
	})

	It("AC 6 — never halves below the ceiling/16 floor", func() {
		bp := handler.NewTTFTBackpressure("p", 65536, 20, clock.Now, nil)

		bp.ObserveColdTTFT(25)
		for i := 1; i <= 6; i++ {
			clock.SetNow(bpAt(stdtime.Duration(i*61) * stdtime.Second))
			bp.ObserveColdTTFT(25)
			Expect(bp.Budget()).To(BeNumerically(">=", 4096))
		}
		Expect(bp.Budget()).To(Equal(4096))
	})

	It("AC 6 — keeps a tiny ceiling's floor at least 1", func() {
		bp := handler.NewTTFTBackpressure("p", 10, 20, clock.Now, nil)

		bp.ObserveColdTTFT(25)
		for i := 1; i <= 4; i++ {
			clock.SetNow(bpAt(stdtime.Duration(i*61) * stdtime.Second))
			bp.ObserveColdTTFT(25)
			Expect(bp.Budget()).To(BeNumerically(">=", 1))
		}
		Expect(bp.Budget()).To(Equal(1))
	})

	It("AC 13 — emits exactly one INFO line per budget change", func() {
		Expect(flag.Set("logtostderr", "true")).To(Succeed())
		bp := handler.NewTTFTBackpressure("p", 65536, 20, clock.Now, nil)

		out := captureStderr(func() {
			bp.ObserveColdTTFT(25)
			clock.SetNow(bpAt(61 * stdtime.Second))
			bp.ObserveColdTTFT(1.0) // roll: halve -> log 32768; W1 holds a clean 1.0s sample

			clock.SetNow(bpAt(122 * stdtime.Second))
			bp.ObserveColdTTFT(15) // roll: grow -> log 40960; W2 holds a 15s sample

			clock.SetNow(bpAt(183 * stdtime.Second))
			bp.ObserveColdTTFT(15) // roll: hold -> no line
		})

		Expect(out).To(ContainSubstring("[backpressure] provider=p budget=32768"))
		Expect(out).To(ContainSubstring("[backpressure] provider=p budget=40960"))
		Expect(strings.Count(out, "[backpressure]")).To(Equal(2), "a hold emits no line")
	})

	It("AC 11 — publishes the effective budget to the gauge", func() {
		gauge := bpNewGauge()
		bp := handler.NewTTFTBackpressure("p", 65536, 20, clock.Now, gauge)

		Expect(testutil.ToFloat64(gauge.WithLabelValues("p"))).
			To(Equal(float64(65536)), "the gauge reads the configured ceiling at construction")

		bp.ObserveColdTTFT(25)
		clock.SetNow(bpAt(61 * stdtime.Second))
		bp.ObserveColdTTFT(1.0)
		Expect(testutil.ToFloat64(gauge.WithLabelValues("p"))).To(Equal(float64(32768)))

		clock.SetNow(bpAt(122 * stdtime.Second))
		bp.ObserveColdTTFT(1.0)
		Expect(testutil.ToFloat64(gauge.WithLabelValues("p"))).To(Equal(float64(40960)))
	})

	It("AC 3 — is inert and nil-safe when disabled", func() {
		Expect(handler.NewTTFTBackpressure("p", 65536, 0, clock.Now, nil)).To(BeNil())
		Expect(handler.NewTTFTBackpressure("p", 65536, -1, clock.Now, nil)).To(BeNil())
		Expect(handler.NewTTFTBackpressure("p", 0, 20, clock.Now, nil)).To(BeNil())

		var bp *handler.TTFTBackpressure
		Expect(func() {
			bp.ObserveColdTTFT(25)
			bp.ObserveStatus(http.StatusInternalServerError)
		}).NotTo(Panic())
		Expect(bp.Budget()).To(Equal(0))
	})

	It("falls back to the real clock and a nil gauge when they are absent", func() {
		bp := handler.NewTTFTBackpressure("p", 65536, 20, nil, nil)
		Expect(bp).NotTo(BeNil())
		Expect(bp.Budget()).To(Equal(65536))
		bp.ObserveColdTTFT(1.0)
		Expect(bp.Budget()).To(Equal(65536))
	})

	It("AC 4 — admits against the live budget, not the value frozen at construction", func() {
		inner := newColdStub("")
		inner.block = make(chan struct{})
		bp := handler.NewTTFTBackpressure("p", 65536, 20, clock.Now, nil)
		gate := handler.NewColdStartGateWithBackpressure(
			inner, "p", 65536, bp, stdtime.Minute, 0, 100*stdtime.Millisecond,
			clock.Now, handler.ColdGateMetrics{},
		)

		// Hold one small cold request (estimate 200) so the gate's lone-request
		// fast path does not admit anything regardless of its estimate.
		heldDone := serveAsync(
			gate, httptest.NewRecorder(), sizedRequest("held", strings.Repeat("x", coldBody)),
		)
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(1))
		Expect(coldInFlight(gate)).To(Equal(200))

		// 50000 fits the 65536 ceiling alongside the held 200.
		bigDone := serveAsync(
			gate, httptest.NewRecorder(), sizedRequest("big", strings.Repeat("x", bpBigBody)),
		)
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(2))
		Expect(coldInFlight(gate)).To(Equal(50200))

		// Halve the live budget: 50200 + 50000 now exceeds 32768.
		bp.ObserveColdTTFT(25)
		clock.SetNow(bpAt(61 * stdtime.Second))
		bp.ObserveColdTTFT(25)
		Expect(bp.Budget()).To(Equal(32768))

		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, sizedRequest("big2", strings.Repeat("x", bpBigBody)))
		Expect(rec.Code).To(Equal(http.StatusTooManyRequests), "the gate reads the live budget")
		Expect(coldInFlight(gate)).To(Equal(50200), "the refused request is never charged")
		Expect(inner.entryCount()).To(Equal(2), "the refused request is never forwarded")

		close(inner.block)
		Eventually(heldDone, "1s").Should(BeClosed())
		Eventually(bigDone, "1s").Should(BeClosed())
	})

	It("AC 10 — keeps two providers' budgets independent", func() {
		stubA := newColdStub("")
		stubA.block = make(chan struct{})
		stubB := newColdStub("")
		stubB.block = make(chan struct{})
		bpA := handler.NewTTFTBackpressure("a", 65536, 20, clock.Now, nil)
		bpB := handler.NewTTFTBackpressure("b", 65536, 20, clock.Now, nil)
		gateA := handler.NewColdStartGateWithBackpressure(
			stubA, "a", 65536, bpA, stdtime.Minute, 0, 100*stdtime.Millisecond,
			clock.Now, handler.ColdGateMetrics{},
		)
		gateB := handler.NewColdStartGateWithBackpressure(
			stubB, "b", 65536, bpB, stdtime.Minute, 0, 100*stdtime.Millisecond,
			clock.Now, handler.ColdGateMetrics{},
		)

		doneA := serveAsync(
			gateA, httptest.NewRecorder(), sizedRequest("a1", strings.Repeat("x", coldBody)),
		)
		doneB := serveAsync(
			gateB, httptest.NewRecorder(), sizedRequest("b1", strings.Repeat("x", coldBody)),
		)
		Eventually(stubA.entryCount, "1s", "10ms").Should(Equal(1))
		Eventually(stubB.entryCount, "1s", "10ms").Should(Equal(1))

		bpA.ObserveColdTTFT(25)
		clock.SetNow(bpAt(61 * stdtime.Second))
		bpA.ObserveColdTTFT(25)
		Expect(bpA.Budget()).To(Equal(32768))
		Expect(bpB.Budget()).To(Equal(65536), "halving A must not touch B")

		// A's gate now refuses the big request; B's still admits it.
		recA := httptest.NewRecorder()
		gateA.ServeHTTP(recA, sizedRequest("a2", strings.Repeat("x", bpBigBody)))
		Expect(recA.Code).To(Equal(http.StatusTooManyRequests))
		Expect(stubA.entryCount()).To(Equal(1))

		doneB2 := serveAsync(
			gateB, httptest.NewRecorder(), sizedRequest("b2", strings.Repeat("x", bpBigBody)),
		)
		Eventually(stubB.entryCount, "1s", "10ms").Should(Equal(2))

		close(stubA.block)
		close(stubB.block)
		Eventually(doneA, "1s").Should(BeClosed())
		Eventually(doneB, "1s").Should(BeClosed())
		Eventually(doneB2, "1s").Should(BeClosed())
	})

	It("is race-free under concurrent observation across window boundaries", func() {
		bp := handler.NewTTFTBackpressure("p", 65536, 20, clock.Now, nil)

		stop := make(chan struct{})
		var pollWG sync.WaitGroup
		pollWG.Add(1)
		go func() {
			defer pollWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = bp.Budget()
				}
			}
		}()

		const workers = 8
		var wg sync.WaitGroup
		wg.Add(workers)
		for w := 0; w < workers; w++ {
			go func(w int) {
				defer wg.Done()
				bp.ObserveColdTTFT(25)
				bp.ObserveStatus(http.StatusInternalServerError)
				clock.SetNow(bpAt(stdtime.Duration(w+1) * 61 * stdtime.Second))
				bp.ObserveColdTTFT(1.0)
				bp.ObserveStatus(http.StatusOK)
				clock.SetNow(bpAt(stdtime.Duration(w+20) * 61 * stdtime.Second))
				bp.ObserveColdTTFT(1.0)
			}(w)
		}
		wg.Wait()
		close(stop)
		pollWG.Wait()

		budget := bp.Budget()
		Expect(budget).To(BeNumerically(">=", 4096))
		Expect(budget).To(BeNumerically("<=", 65536))
	})
})

var _ = Describe("TTFTBackpressure in the cold-start gate", func() {
	var clock libtime.CurrentDateTime

	BeforeEach(func() {
		clock = newClock()
	})

	It("AC 4 — halves the next window after a cold request's upstream answers 500", func() {
		stub := newGateStub(http.StatusInternalServerError, "")
		bp := handler.NewTTFTBackpressure("p", 65536, 20, clock.Now, nil)
		gate := handler.NewColdStartGateWithBackpressure(
			stub, "p", 65536, bp, stdtime.Minute, 0, stdtime.Second,
			clock.Now, handler.ColdGateMetrics{},
		)

		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, sizedRequest("s1", strings.Repeat("x", coldBody)))
		Expect(rec.Code).To(Equal(http.StatusInternalServerError))

		clock.SetNow(bpAt(61 * stdtime.Second))
		bp.ObserveStatus(http.StatusOK)
		Expect(bp.Budget()).To(Equal(32768), "the 500 marked the window failed")
	})

	It("AC 4 — does not count a cancelled client as a failure", func() {
		stub := newColdStub("")
		stub.block = make(chan struct{})
		bp := handler.NewTTFTBackpressure("p", 65536, 20, clock.Now, nil)
		gate := handler.NewColdStartGateWithBackpressure(
			stub, "p", 65536, bp, stdtime.Minute, 0, stdtime.Second,
			clock.Now, handler.ColdGateMetrics{},
		)

		bp.ObserveColdTTFT(25)
		clock.SetNow(bpAt(61 * stdtime.Second))
		bp.ObserveColdTTFT(1.0)
		Expect(bp.Budget()).To(Equal(32768))

		ctx, cancel := context.WithCancel(context.Background())
		req := sizedRequest("s1", strings.Repeat("x", coldBody)).
			WithContext(handler.ContextWithSessionID(ctx, "s1"))
		done := serveAsync(gate, httptest.NewRecorder(), req)
		Eventually(stub.entryCount, "1s", "10ms").Should(Equal(1))
		cancel()
		Eventually(done, "1s").Should(BeClosed())

		clock.SetNow(bpAt(122 * stdtime.Second))
		bp.ObserveStatus(http.StatusOK)
		Expect(bp.Budget()).To(Equal(40960), "a disconnect is not a failure — the window grows")
	})

	It("AC 4 — observes the status of a warm request on the no-reservation path", func() {
		stub := newGateStub(http.StatusOK, "ok")
		bp := handler.NewTTFTBackpressure("p", 65536, 20, clock.Now, nil)
		gate := handler.NewColdStartGateWithBackpressure(
			stub, "p", 65536, bp, stdtime.Minute, 0, stdtime.Second,
			clock.Now, handler.ColdGateMetrics{},
		)

		body := strings.Repeat("x", coldBody)
		rec1 := httptest.NewRecorder()
		gate.ServeHTTP(rec1, sizedRequest("s1", body))
		Expect(rec1.Code).To(Equal(http.StatusOK))

		stub.setStatus(http.StatusInternalServerError)
		rec2 := httptest.NewRecorder()
		gate.ServeHTTP(rec2, sizedRequest("s1", body)) // warm
		Expect(rec2.Code).To(Equal(http.StatusInternalServerError))

		clock.SetNow(bpAt(61 * stdtime.Second))
		bp.ObserveStatus(http.StatusOK)
		Expect(bp.Budget()).To(Equal(32768), "the warm path's 500 counted")
	})

	It("AC 4 — observes the status of a zero-estimate request on the no-reservation path", func() {
		stub := newGateStub(http.StatusInternalServerError, "")
		bp := handler.NewTTFTBackpressure("p", 65536, 20, clock.Now, nil)
		gate := handler.NewColdStartGateWithBackpressure(
			stub, "p", 65536, bp, stdtime.Minute, 0, stdtime.Second,
			clock.Now, handler.ColdGateMetrics{},
		)

		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, bodyRequest(""))
		Expect(rec.Code).To(Equal(http.StatusInternalServerError))

		clock.SetNow(bpAt(61 * stdtime.Second))
		bp.ObserveStatus(http.StatusOK)
		Expect(bp.Budget()).To(Equal(32768))
	})

	It("AC 3 — forwards a warm request unchanged when no controller is attached", func() {
		stub := newGateStub(http.StatusOK, "ok")
		gate := handler.NewColdStartGate(
			stub, "p", 65536, stdtime.Minute, 0, stdtime.Second,
			clock.Now, handler.ColdGateMetrics{},
		)

		body := strings.Repeat("x", coldBody)
		rec1 := httptest.NewRecorder()
		gate.ServeHTTP(rec1, sizedRequest("s1", body))
		rec2 := httptest.NewRecorder()
		gate.ServeHTTP(rec2, sizedRequest("s1", body))

		Expect(rec2.Code).To(Equal(http.StatusOK))
		Expect(rec2.Body.String()).To(Equal("ok"))
		Expect(stub.entryCount()).To(Equal(2))
	})

	It("AC 4 — halves on a 504 and on a 502 with no cold sample", func() {
		for _, status := range []int{http.StatusGatewayTimeout, http.StatusBadGateway} {
			clock = newClock()
			bp := handler.NewTTFTBackpressure("p", 65536, 20, clock.Now, nil)
			bp.ObserveStatus(status)
			clock.SetNow(bpAt(61 * stdtime.Second))
			bp.ObserveStatus(http.StatusOK)
			Expect(bp.Budget()).To(Equal(32768), "status %d is a failure", status)
		}
	})
})

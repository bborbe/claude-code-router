// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Cold-start admission gate specs: a session id seen inside the window
// forwards straight through (warm), everything else is cold and charged a
// prefill estimate derived from the body size, admitted only while the
// provider's in-flight cold tokens stay within budget and released on the
// first content_block_delta. Rows drive the injected clock so window
// arithmetic never sleeps wall-clock; only the blocking stubs actually wait.

package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// coldBody is the request body size used by the admission rows: its estimate
// is coldBody * 2 / 7 = 200 tokens, so a budget of 200 admits exactly one
// cold request of this size.
const coldBody = 700

// coldDelta is a minimal SSE content-delta event — the marker whose first
// appearance means prefill has finished.
const coldDelta = "event: content_block_delta\ndata: {\"type\":\"content_block_delta\"}\n\n"

// coldStartEvent is a minimal SSE message_start event — it must NOT release
// the reservation (prefill has not finished at message_start).
const coldStartEvent = "event: message_start\ndata: {\"type\":\"message_start\"}\n\n"

// coldStub is the cold-gate upstream stub: it records each entry in an atomic
// counter, writes the configured SSE bytes (if any), then — when block is
// non-nil — waits on it while also selecting on the request context so a
// cancelled client is leak-free.
type coldStub struct {
	entries atomic.Int32
	block   chan struct{}
	sse     string
}

func newColdStub(sse string) *coldStub {
	return &coldStub{sse: sse}
}

func (s *coldStub) entryCount() int {
	return int(s.entries.Load())
}

func (s *coldStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.entries.Add(1)
	if s.sse != "" {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(s.sse))
		// Flush through http.NewResponseController so the row proves the
		// wrapper's Unwrap chain keeps SSE flushing working.
		_ = http.NewResponseController(w).Flush()
	}
	if s.block != nil {
		select {
		case <-s.block:
		case <-r.Context().Done():
		}
	}
	if s.sse == "" {
		w.WriteHeader(http.StatusOK)
	}
}

// sizedRequest builds a POST /v1/messages request with the given body (so
// httptest sets ContentLength) carrying sessionID in the request context —
// the session middleware strips the X-Session-Id header, so the gate reads
// the id from the context, never the header.
func sizedRequest(sessionID, body string) *http.Request {
	req := bodyRequest(body)
	return req.WithContext(handler.ContextWithSessionID(req.Context(), sessionID))
}

// bodyRequest builds a keyless POST /v1/messages request with the given body
// (so httptest sets ContentLength and the gate derives a non-zero estimate).
func bodyRequest(body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
}

// coldInFlight reads the gate's live reserved-token count. h must be a real
// gate (budget > 0); the disabled path returns next unchanged and has no
// gate (mirrors the concurrency limiter's InFlight accessor).
func coldInFlight(h http.Handler) int {
	g, ok := h.(interface{ InFlight() int })
	if !ok {
		panic("coldInFlight: handler is not a real cold-start gate")
	}
	return g.InFlight()
}

// zeroInFlight polls the gate's reserved-token count until it reaches zero.
func zeroInFlight(gate http.Handler) func() int {
	return func() int { return coldInFlight(gate) }
}

var _ = Describe("ColdStartGate", func() {
	var clock libtime.CurrentDateTime

	// newColdGate is the single construction seam: a later prompt extends the
	// constructor's parameter list here, in one place.
	newColdGate := func(next http.Handler, budget int, window stdtime.Duration) http.Handler {
		return handler.NewColdStartGate(
			next,
			"p",
			budget,
			window,
			clock.Now,
			handler.ColdGateMetrics{},
		)
	}

	BeforeEach(func() {
		clock = newClock()
	})

	It("returns next unchanged when the budget is 0 or negative — byte-for-byte no-op", func() {
		for _, budget := range []int{0, -1} {
			inner := newColdStub("")
			gate := newColdGate(inner, budget, stdtime.Minute)
			Expect(gate).To(BeIdenticalTo(http.Handler(inner)))

			rec := httptest.NewRecorder()
			gate.ServeHTTP(rec, newMessagesRequest())
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(inner.entryCount()).To(Equal(1))
		}
	})

	It("forwards a warm session immediately while another cold request is held", func() {
		inner := newColdStub("")
		inner.block = make(chan struct{})
		gate := newColdGate(inner, 200, stdtime.Minute)
		body := strings.Repeat("x", coldBody)

		done1 := serveAsync(gate, httptest.NewRecorder(), sizedRequest("s1", body))
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(1), "the first cold request enters")

		done2 := serveAsync(gate, httptest.NewRecorder(), sizedRequest("s2", body))
		Consistently(inner.entryCount, "100ms", "10ms").
			Should(Equal(1), "a different cold session must wait for budget")

		done3 := serveAsync(gate, httptest.NewRecorder(), sizedRequest("s1", body))
		Eventually(inner.entryCount, "1s", "10ms").
			Should(Equal(2), "the warm session forwards while the held request has not")

		close(inner.block)
		Eventually(done1, "1s").Should(BeClosed())
		Eventually(done2, "1s").Should(BeClosed())
		Eventually(done3, "1s").Should(BeClosed())
	})

	It("treats a session last seen outside the window as cold again", func() {
		inner := newColdStub("")
		inner.block = make(chan struct{})
		gate := newColdGate(inner, 200, stdtime.Minute)
		body := strings.Repeat("x", coldBody)

		done1 := serveAsync(gate, httptest.NewRecorder(), sizedRequest("s1", body))
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(1))

		clock.SetNow(libtime.DateTime(t0.Time().Add(61 * stdtime.Second)))
		done2 := serveAsync(gate, httptest.NewRecorder(), sizedRequest("s1", body))
		Consistently(inner.entryCount, "100ms", "10ms").
			Should(Equal(1), "outside the window the session is cold and must wait")

		close(inner.block)
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(2))
		Eventually(done1, "1s").Should(BeClosed())
		Eventually(done2, "1s").Should(BeClosed())
	})

	It("treats a request without a session id as always cold", func() {
		inner := newColdStub("")
		inner.block = make(chan struct{})
		gate := newColdGate(inner, 200, stdtime.Minute)
		body := strings.Repeat("x", coldBody)

		done1 := serveAsync(gate, httptest.NewRecorder(), sizedRequest("s1", body))
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(1))

		done2 := serveAsync(gate, httptest.NewRecorder(), bodyRequest(body))
		Consistently(inner.entryCount, "100ms", "10ms").
			Should(Equal(1), "a keyless request is always cold and must wait")

		close(inner.block)
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(2))
		Eventually(done1, "1s").Should(BeClosed())
		Eventually(done2, "1s").Should(BeClosed())
	})

	It("falls back to the 600s default window when the configured window is absent", func() {
		inner := newColdStub("")
		inner.block = make(chan struct{})
		gate := newColdGate(inner, 200, 0)
		body := strings.Repeat("x", coldBody)

		done1 := serveAsync(gate, httptest.NewRecorder(), sizedRequest("s1", body))
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(1))

		clock.SetNow(libtime.DateTime(t0.Time().Add(599 * stdtime.Second)))
		done2 := serveAsync(gate, httptest.NewRecorder(), sizedRequest("s1", body))
		Eventually(inner.entryCount, "1s", "10ms").
			Should(Equal(2), "599s is inside the 600s fallback window — warm")

		clock.SetNow(libtime.DateTime(t0.Time().Add(1200 * stdtime.Second)))
		done3 := serveAsync(gate, httptest.NewRecorder(), sizedRequest("s1", body))
		Consistently(inner.entryCount, "100ms", "10ms").
			Should(Equal(2), "past 600s the session is cold again and must wait")

		close(inner.block)
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(3))
		Eventually(done1, "1s").Should(BeClosed())
		Eventually(done2, "1s").Should(BeClosed())
		Eventually(done3, "1s").Should(BeClosed())
	})

	It("admits a lone cold request whose estimate exceeds the whole budget", func() {
		inner := newColdStub("")
		inner.block = make(chan struct{})
		gate := newColdGate(inner, 100, stdtime.Minute)

		done := serveAsync(
			gate,
			httptest.NewRecorder(),
			sizedRequest("s1", strings.Repeat("x", coldBody)),
		)
		Eventually(inner.entryCount, "1s", "10ms").
			Should(Equal(1), "an empty provider admits the cold request regardless of its estimate")
		Expect(coldInFlight(gate)).To(Equal(200))

		close(inner.block)
		Eventually(done, "1s").Should(BeClosed())
	})

	It(
		"charges one reservation for two concurrent first requests with the same session id",
		func() {
			inner := newColdStub("")
			inner.block = make(chan struct{})
			gate := newColdGate(inner, 200, stdtime.Minute)
			body := strings.Repeat("x", coldBody)

			// Fired concurrently: whichever commits first is charged, the other
			// observes the id as seen and forwards warm — one charge for the pair.
			doneA := serveAsync(gate, httptest.NewRecorder(), sizedRequest("s1", body))
			doneB := serveAsync(gate, httptest.NewRecorder(), sizedRequest("s1", body))
			Eventually(inner.entryCount, "1s", "10ms").Should(Equal(2))
			Expect(coldInFlight(gate)).To(Equal(200), "the pair costs exactly one reservation")

			close(inner.block)
			Eventually(doneA, "1s").Should(BeClosed())
			Eventually(doneB, "1s").Should(BeClosed())
		},
	)

	It("holds a second cold request until the first releases its reservation", func() {
		inner := newColdStub("")
		inner.block = make(chan struct{})
		gate := newColdGate(inner, 200, stdtime.Minute)
		body := strings.Repeat("x", coldBody)

		doneA := serveAsync(gate, httptest.NewRecorder(), sizedRequest("a", body))
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(1))
		Expect(coldInFlight(gate)).To(Equal(200))

		doneB := serveAsync(gate, httptest.NewRecorder(), sizedRequest("b", body))
		Consistently(inner.entryCount, "100ms", "10ms").
			Should(Equal(1), "B must wait: 200 in flight + 200 estimate exceeds the 200 budget")

		close(inner.block)
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(2))
		Eventually(doneA, "1s").Should(BeClosed())
		Eventually(doneB, "1s").Should(BeClosed())
	})

	It("charges each cold request its body-size estimate (bytes * 2 / 7)", func() {
		for _, size := range []int{7, 70, 700, 701} {
			inner := newColdStub("")
			inner.block = make(chan struct{})
			gate := newColdGate(inner, 1_000_000, stdtime.Minute)

			done := serveAsync(
				gate,
				httptest.NewRecorder(),
				sizedRequest("s1", strings.Repeat("x", size)),
			)
			Eventually(inner.entryCount, "1s", "10ms").Should(Equal(1))
			Expect(coldInFlight(gate)).To(Equal(size * 2 / 7))

			close(inner.block)
			Eventually(done, "1s").Should(BeClosed())
			Eventually(zeroInFlight(gate), "1s", "10ms").Should(Equal(0))
		}
	})

	It("reserves nothing for a request whose body size is unknown", func() {
		inner := newColdStub("")
		inner.block = make(chan struct{})
		gate := newColdGate(inner, 1, stdtime.Minute)

		done := serveAsync(gate, httptest.NewRecorder(), newMessagesRequest())
		Eventually(inner.entryCount, "1s", "10ms").
			Should(Equal(1), "a zero estimate forwards with no reservation")
		Expect(coldInFlight(gate)).To(Equal(0))

		close(inner.block)
		Eventually(done, "1s").Should(BeClosed())
	})

	It("releases the reservation on the first content_block_delta", func() {
		inner := newColdStub(coldDelta)
		inner.block = make(chan struct{})
		gate := newColdGate(inner, 1000, stdtime.Minute)

		done := serveAsync(
			gate,
			httptest.NewRecorder(),
			sizedRequest("s1", strings.Repeat("x", coldBody)),
		)
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(1))
		Eventually(zeroInFlight(gate), "1s", "10ms").
			Should(Equal(0), "the reservation frees on the first delta")
		Expect(inner.entryCount()).To(Equal(1), "the handler is still writing")

		close(inner.block)
		Eventually(done, "1s").Should(BeClosed())
	})

	It("does not release the reservation on message_start", func() {
		inner := newColdStub(coldStartEvent)
		inner.block = make(chan struct{})
		gate := newColdGate(inner, 1000, stdtime.Minute)

		done := serveAsync(
			gate,
			httptest.NewRecorder(),
			sizedRequest("s1", strings.Repeat("x", coldBody)),
		)
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(1))
		Consistently(zeroInFlight(gate), "150ms", "15ms").
			Should(Equal(200), "message_start is not a delta — prefill is not finished")

		close(inner.block)
		Eventually(done, "1s").Should(BeClosed())
		Eventually(zeroInFlight(gate), "1s", "10ms").
			Should(Equal(0), "the deferred release frees it when the handler returns")
	})

	It("releases the reservation when the upstream returns without a delta", func() {
		inner := newColdStub("")
		gate := newColdGate(inner, 1000, stdtime.Minute)

		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, sizedRequest("s1", strings.Repeat("x", coldBody)))

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(inner.entryCount()).To(Equal(1))
		Expect(coldInFlight(gate)).To(Equal(0))
	})

	It("never forwards or charges a client that disconnects while waiting", func() {
		inner := newColdStub("")
		inner.block = make(chan struct{})
		gate := newColdGate(inner, 200, stdtime.Minute)
		body := strings.Repeat("x", coldBody)

		done1 := serveAsync(gate, httptest.NewRecorder(), sizedRequest("a", body))
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(1))
		Expect(coldInFlight(gate)).To(Equal(200))

		cancelledCtx, cancel := context.WithCancel(context.Background())
		cancel()
		req := sizedRequest("b", body).
			WithContext(handler.ContextWithSessionID(cancelledCtx, "b"))
		gate.ServeHTTP(httptest.NewRecorder(), req)

		Expect(inner.entryCount()).To(Equal(1), "a disconnected client is never forwarded")
		Expect(coldInFlight(gate)).To(Equal(200), "it is never charged")

		close(inner.block)
		Eventually(done1, "1s").Should(BeClosed())
		Eventually(zeroInFlight(gate), "1s", "10ms").Should(Equal(0))
	})

	It("passes the response bytes through unmodified and keeps SSE flushing working", func() {
		inner := newColdStub(coldDelta)
		gate := newColdGate(inner, 1000, stdtime.Minute)

		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, sizedRequest("s1", strings.Repeat("x", coldBody)))

		Expect(rec.Body.String()).To(Equal(coldDelta), "the wrapper must not rewrite the stream")
		Expect(rec.Flushed).To(BeTrue(), "Unwrap must keep the flush chain intact")
		Expect(coldInFlight(gate)).To(Equal(0))
	})

	It("falls back to the real clock when the injected clock is nil", func() {
		inner := newColdStub("")
		inner.block = make(chan struct{})
		gate := handler.NewColdStartGate(
			inner,
			"p",
			200,
			stdtime.Minute,
			nil,
			handler.ColdGateMetrics{},
		)

		done := serveAsync(
			gate,
			httptest.NewRecorder(),
			sizedRequest("s1", strings.Repeat("x", coldBody)),
		)
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(1))
		Expect(coldInFlight(gate)).To(Equal(200))

		close(inner.block)
		Eventually(done, "1s").Should(BeClosed())
	})

	It("emits the tokens-in-flight gauge and TTFT histogram when configured", func() {
		gauge := prometheus.NewGaugeVec(
			prometheus.GaugeOpts{Name: "ccrouter_cold_tokens_in_flight", Help: "test"},
			[]string{"provider"},
		)
		hist := prometheus.NewHistogramVec(
			prometheus.HistogramOpts{Name: "ccrouter_cold_ttft_seconds", Help: "test"},
			[]string{"provider"},
		)
		metrics := handler.ColdGateMetrics{TokensInFlight: gauge, TTFT: hist}

		// message_start is not a delta: the reservation (and so the gauge) stays
		// at the estimate and no TTFT is observed.
		held := newColdStub(coldStartEvent)
		held.block = make(chan struct{})
		heldGate := handler.NewColdStartGate(held, "p", 1000, stdtime.Minute, clock.Now, metrics)
		gauge.WithLabelValues("p").Add(float64(200))
		heldDone := serveAsync(
			heldGate,
			httptest.NewRecorder(),
			sizedRequest("s1", strings.Repeat("x", coldBody)),
		)
		Eventually(held.entryCount, "1s", "10ms").Should(Equal(1))
		Consistently(
			func() float64 { return testutil.ToFloat64(gauge.WithLabelValues("p")) },
			"150ms",
			"15ms",
		).Should(Equal(float64(200)))
		Expect(testutil.CollectAndCount(hist)).To(Equal(0), "no delta, no TTFT observation")

		close(held.block)
		Eventually(heldDone, "1s").Should(BeClosed())
		Eventually(
			func() float64 { return testutil.ToFloat64(gauge.WithLabelValues("p")) },
			"1s",
			"10ms",
		).Should(Equal(float64(0)), "the deferred release subtracts the estimate")
		Expect(testutil.CollectAndCount(hist)).To(Equal(0))

		// A delta releases immediately and observes exactly one TTFT sample.
		streamed := newColdStub(coldDelta)
		streamedGate := handler.NewColdStartGate(
			streamed,
			"p",
			1000,
			stdtime.Minute,
			clock.Now,
			metrics,
		)
		streamedGate.ServeHTTP(
			httptest.NewRecorder(),
			sizedRequest("s2", strings.Repeat("x", coldBody)),
		)
		Expect(testutil.CollectAndCount(hist)).To(Equal(1))
	})

	It("frees the reservation when a client disconnects while it is held", func() {
		inner := newColdStub("")
		inner.block = make(chan struct{})
		gate := newColdGate(inner, 1000, stdtime.Minute)

		ctx, cancel := context.WithCancel(context.Background())
		req := sizedRequest("s1", strings.Repeat("x", coldBody)).
			WithContext(handler.ContextWithSessionID(ctx, "s1"))
		done := serveAsync(gate, httptest.NewRecorder(), req)
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(1))
		Expect(coldInFlight(gate)).To(Equal(200))

		cancel()
		Eventually(done, "1s").Should(BeClosed())
		Eventually(zeroInFlight(gate), "1s", "10ms").Should(Equal(0))
	})
})

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// panickyHandler panics on every request, modelling a request goroutine
// that dies mid-request so the tests can prove the host slot is still
// released.
type panickyHandler struct{}

func (panickyHandler) ServeHTTP(http.ResponseWriter, *http.Request) {
	panic("boom")
}

var _ = Describe("HostLimiter", func() {
	It("shares one budget across every wrapped handler", func() {
		hl := handler.NewHostLimiter(1, time.Second)
		innerA := newBlockingHandler()
		innerB := newBlockingHandler()
		wrapA := hl.Wrap(innerA)
		wrapB := hl.Wrap(innerB)

		recA := httptest.NewRecorder()
		doneA := serveAsync(wrapA, recA, newMessagesRequest())
		Eventually(innerA.entryCount, "1s", "10ms").Should(Equal(1), "A must hold the host slot")
		Expect(hl.InFlight()).To(Equal(1))

		recB := httptest.NewRecorder()
		doneB := serveAsync(wrapB, recB, newMessagesRequest())
		Consistently(innerB.entryCount, "200ms", "20ms").
			Should(Equal(0), "B must be held by A's slot even though B wraps a different handler")

		close(innerA.release)
		close(innerB.release)
		Eventually(doneA, "1s").Should(BeClosed())
		Eventually(doneB, "1s").Should(BeClosed())
		Expect(recA.Code).To(Equal(http.StatusOK))
		Expect(recB.Code).To(Equal(http.StatusOK))
		Expect(hl.InFlight()).To(Equal(0))
	})

	It("answers HTTP 429 with the static rate_limit_error body on queue timeout", func() {
		hl := handler.NewHostLimiter(1, 50*time.Millisecond)
		innerA := newBlockingHandler()
		innerB := newBlockingHandler()
		wrapA := hl.Wrap(innerA)
		wrapB := hl.Wrap(innerB)

		recA := httptest.NewRecorder()
		doneA := serveAsync(wrapA, recA, newMessagesRequest())
		Eventually(innerA.entryCount, "1s", "10ms").Should(Equal(1), "A must hold the host slot")

		recB := httptest.NewRecorder()
		wrapB.ServeHTTP(recB, newMessagesRequest())

		Expect(recB.Code).To(Equal(http.StatusTooManyRequests))
		Expect(recB.Code).To(BeNumerically("<", 500))
		Expect(recB.Header().Get("Content-Type")).To(ContainSubstring("application/json"))
		Expect(recB.Body.String()).To(Equal(expected429Body))
		Expect(recB.Body.String()).To(ContainSubstring("rate_limit_error"))
		Expect(innerB.entryCount()).To(Equal(0), "the 429'd request must never enter the handler")

		close(innerA.release)
		close(innerB.release)
		Eventually(doneA, "1s").Should(BeClosed())
		Expect(recA.Code).To(Equal(http.StatusOK))
	})

	DescribeTable("never queues when unlimited",
		func(maxConcurrentRequests int) {
			hl := handler.NewHostLimiter(maxConcurrentRequests, time.Second)
			inner := newBlockingHandler()
			wrap := hl.Wrap(inner)

			dones := make([]chan struct{}, 5)
			recs := make([]*httptest.ResponseRecorder, 5)
			for i := range dones {
				recs[i] = httptest.NewRecorder()
				dones[i] = serveAsync(wrap, recs[i], newMessagesRequest())
			}
			Eventually(inner.entryCount, "1s", "10ms").
				Should(Equal(5), "all five must enter at once, with no queueing")
			Expect(hl.InFlight()).To(Equal(5))

			close(inner.release)
			for i := range dones {
				Eventually(dones[i], "1s").Should(BeClosed())
				Expect(recs[i].Code).To(Equal(http.StatusOK))
			}
			Expect(hl.InFlight()).To(Equal(0))
		},
		Entry("cap 0", 0),
		Entry("negative cap", -1),
	)

	It("reports the semaphore occupancy via InFlight when capped", func() {
		hl := handler.NewHostLimiter(2, time.Second)
		inner := newBlockingHandler()
		wrap := hl.Wrap(inner)

		Expect(hl.InFlight()).To(Equal(0))

		rec := httptest.NewRecorder()
		done := serveAsync(wrap, rec, newMessagesRequest())
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(1), "the request must hold a slot")
		Expect(hl.InFlight()).To(Equal(1))

		close(inner.release)
		Eventually(done, "1s").Should(BeClosed())
		Expect(hl.InFlight()).To(Equal(0))
	})

	It("never acquires a slot for a client that disconnects while queued", func() {
		hl := handler.NewHostLimiter(1, time.Second)
		inner := newBlockingHandler()
		wrap := hl.Wrap(inner)

		rec1 := httptest.NewRecorder()
		done1 := serveAsync(wrap, rec1, newMessagesRequest())
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(1), "request 1 must hold the slot")

		cancelledCtx, cancel := context.WithCancel(context.Background())
		cancel()
		req2 := newMessagesRequest().WithContext(cancelledCtx)
		rec2 := httptest.NewRecorder()
		wrap.ServeHTTP(rec2, req2)

		Expect(inner.entryCount()).To(Equal(1), "disconnected request must not be forwarded")
		Expect(hl.InFlight()).To(Equal(1))

		close(inner.release)
		Eventually(done1, "1s").Should(BeClosed())
		Expect(rec1.Code).To(Equal(http.StatusOK))
	})

	DescribeTable("releases the slot when the inner handler panics",
		func(maxConcurrentRequests int) {
			hl := handler.NewHostLimiter(maxConcurrentRequests, time.Second)
			wrap := hl.Wrap(panickyHandler{})

			func() {
				defer func() { _ = recover() }()
				wrap.ServeHTTP(httptest.NewRecorder(), newMessagesRequest())
			}()

			Expect(hl.InFlight()).To(Equal(0), "the slot must be released even on panic")

			if maxConcurrentRequests > 0 {
				// A following request must still acquire the freed slot.
				inner := newBlockingHandler()
				close(inner.release)
				rec := httptest.NewRecorder()
				hl.Wrap(inner).ServeHTTP(rec, newMessagesRequest())
				Expect(rec.Code).To(Equal(http.StatusOK))
			}
		},
		Entry("capped", 1),
		Entry("unlimited", 0),
	)
})

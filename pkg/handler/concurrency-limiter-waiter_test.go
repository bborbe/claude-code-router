// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"time"

	libtime "github.com/bborbe/time"
	libtimemocks "github.com/bborbe/time/mocks"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// The queue wait is timed by an injected libtime.WaiterDuration. These specs
// drive it with the counterfeiter mock, so every timeout is an explicit event
// in the spec rather than a real timer.
var _ = Describe("ConcurrencyLimiter queue wait (injected WaiterDuration)", func() {
	var (
		inner  *blockingHandler
		waiter *libtimemocks.WaiterDuration
		expire chan struct{}
		h      http.Handler
	)

	BeforeEach(func() {
		inner = newBlockingHandler()
		expire = make(chan struct{})
		waiter = &libtimemocks.WaiterDuration{}
		waiter.WaitStub = func(ctx context.Context, _ libtime.Duration) error {
			select {
			case <-expire:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		h = handler.NewConcurrencyLimiter(inner, "", 1, 30*time.Second, waiter, nil, nil)
	})

	// holdTheSlot admits one request and waits until it is inside the
	// inner handler, so the next request finds every slot held.
	holdTheSlot := func() chan struct{} {
		done := serveAsync(h, httptest.NewRecorder(), newMessagesRequest())
		Eventually(inner.entryCount).Should(Equal(1))
		return done
	}

	It("waits with the configured duration and refuses only when the waiter returns", func() {
		holderDone := holdTheSlot()
		Expect(waiter.WaitCallCount()).To(Equal(0), "a request taking a free slot never waits")

		rec := httptest.NewRecorder()
		queuedDone := serveAsync(h, rec, newMessagesRequest())
		Eventually(waiter.WaitCallCount).Should(Equal(1))
		_, d := waiter.WaitArgsForCall(0)
		Expect(d).To(Equal(libtime.Duration(30 * time.Second)))
		Consistently(
			queuedDone,
			"50ms",
		).ShouldNot(BeClosed(), "no timeout before the waiter returns")

		expire <- struct{}{}
		Eventually(queuedDone, "2s").Should(BeClosed())
		Expect(rec.Code).To(Equal(http.StatusTooManyRequests))
		Expect(inner.entryCount()).To(Equal(1), "the refused request never reached the handler")

		close(inner.release)
		Eventually(holderDone, "2s").Should(BeClosed())
	})

	It("cancels the waiter when a slot frees before the wait ends", func() {
		holderDone := holdTheSlot()

		rec := httptest.NewRecorder()
		queuedDone := serveAsync(h, rec, newMessagesRequest())
		Eventually(waiter.WaitCallCount).Should(Equal(1))
		waitCtx, _ := waiter.WaitArgsForCall(0)

		close(inner.release)
		Eventually(holderDone, "2s").Should(BeClosed())
		Eventually(queuedDone, "2s").Should(BeClosed())
		Expect(rec.Code).To(Equal(http.StatusOK))
		Eventually(
			waitCtx.Done(),
			"2s",
		).Should(BeClosed(), "the waiter does not outlive the queued phase")
	})

	It("does not refuse a client that disconnects while queued", func() {
		holderDone := holdTheSlot()

		ctx, cancel := context.WithCancel(context.Background())
		rec := httptest.NewRecorder()
		queuedDone := serveAsync(h, rec, newMessagesRequest().WithContext(ctx))
		Eventually(waiter.WaitCallCount).Should(Equal(1))

		cancel()
		Eventually(queuedDone, "2s").Should(BeClosed())
		Expect(rec.Code).NotTo(Equal(http.StatusTooManyRequests))
		Expect(rec.Body.Len()).To(BeZero(), "nothing is written to a disconnected client")

		close(inner.release)
		Eventually(holderDone, "2s").Should(BeClosed())
	})
})

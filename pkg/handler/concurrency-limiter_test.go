// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// expected429Body is the Anthropic-shaped rate_limit_error envelope the
// limiter writes on queue timeout. Asserting exact equality against this
// static string is the security check: the body carries no queue depth,
// upstream URL, or provider name.
const expected429Body = `{"type":"error","error":{"type":"rate_limit_error","message":"too many concurrent requests, please retry"}}`

// blockingHandler records each request that entered ServeHTTP in a
// buffered channel, then blocks on release until the test closes it, then
// writes 200 with body "ok". entryCount() reads the channel length, so
// assertions can observe how many requests are currently in flight.
type blockingHandler struct {
	entries chan struct{}
	release chan struct{}
}

func newBlockingHandler() *blockingHandler {
	return &blockingHandler{
		entries: make(chan struct{}, 100),
		release: make(chan struct{}),
	}
}

func (b *blockingHandler) entryCount() int {
	return len(b.entries)
}

func (b *blockingHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	b.entries <- struct{}{}
	<-b.release
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// serveAsync runs limiter.ServeHTTP in a goroutine and returns a channel
// that closes when it returns, so the test can read the recorder without
// racing the goroutine's writes.
func serveAsync(
	limiter http.Handler,
	rec *httptest.ResponseRecorder,
	req *http.Request,
) chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		limiter.ServeHTTP(rec, req)
	}()
	return done
}

func newMessagesRequest() *http.Request {
	return httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
}

var _ = Describe("ConcurrencyLimiter", func() {
	It("returns next unchanged when maxConcurrentRequests is 0 — byte-for-byte no-op", func() {
		inner := newBlockingHandler()
		close(inner.release)
		limiter := handler.NewConcurrencyLimiter(inner, 0, time.Second)

		Expect(limiter).To(BeIdenticalTo(http.Handler(inner)))

		rec := httptest.NewRecorder()
		limiter.ServeHTTP(rec, newMessagesRequest())

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(Equal("ok"))
		Expect(inner.entryCount()).To(Equal(1))
	})

	It(
		"caps in-flight requests at the limit and forwards the queued request after the first releases its slot",
		func() {
			inner := newBlockingHandler()
			limiter := handler.NewConcurrencyLimiter(inner, 1, time.Second)

			rec1 := httptest.NewRecorder()
			done1 := serveAsync(limiter, rec1, newMessagesRequest())
			Eventually(
				inner.entryCount,
				"1s",
				"10ms",
			).Should(Equal(1), "request 1 must enter the handler")

			rec2 := httptest.NewRecorder()
			done2 := serveAsync(limiter, rec2, newMessagesRequest())
			Consistently(inner.entryCount, "200ms", "20ms").
				Should(Equal(1), "request 2 must be held; the slot stays held for the full duration of request 1")

			close(inner.release)
			Eventually(done1, "1s").Should(BeClosed())
			Eventually(done2, "1s").Should(BeClosed())

			Expect(rec1.Code).To(Equal(http.StatusOK))
			Expect(rec1.Body.String()).To(Equal("ok"))
			Expect(rec2.Code).To(Equal(http.StatusOK))
			Expect(rec2.Body.String()).To(Equal("ok"))
		},
	)

	It("reports its in-flight slot occupancy via InFlight", func() {
		inner := newBlockingHandler()
		limiter := handler.NewConcurrencyLimiter(inner, 1, time.Second)
		// The no-op path (cap <= 0) returns next unchanged, which carries no
		// InFlight; a real limiter exposes the semaphore occupancy the pool
		// handler's least-loaded selection reads.
		capped, ok := limiter.(interface{ InFlight() int })
		Expect(ok).To(BeTrue(), "a capped limiter must expose InFlight")
		inFlight := capped.InFlight

		rec1 := httptest.NewRecorder()
		done1 := serveAsync(limiter, rec1, newMessagesRequest())
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(1), "request 1 must hold the slot")
		Expect(inFlight()).To(Equal(1), "the limiter must report the held slot")

		close(inner.release)
		Eventually(done1, "1s").Should(BeClosed())
		Expect(inFlight()).To(Equal(0), "the limiter must report the slot released")
	})

	It("answers HTTP 429 with the Anthropic-shaped rate_limit_error body on queue timeout", func() {
		inner := newBlockingHandler()
		limiter := handler.NewConcurrencyLimiter(inner, 1, 50*time.Millisecond)

		rec1 := httptest.NewRecorder()
		done1 := serveAsync(limiter, rec1, newMessagesRequest())
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(1), "request 1 must hold the slot")

		rec2 := httptest.NewRecorder()
		limiter.ServeHTTP(rec2, newMessagesRequest())

		Expect(rec2.Code).To(Equal(http.StatusTooManyRequests))
		Expect(rec2.Header().Get("Content-Type")).To(ContainSubstring("application/json"))
		body := rec2.Body.String()
		// Exact equality against the static generic message proves the body
		// carries no queue depth, upstream URL, or provider name.
		Expect(body).To(Equal(expected429Body))

		var parsed struct {
			Type  string `json:"type"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		Expect(json.Unmarshal([]byte(body), &parsed)).To(Succeed())
		Expect(parsed.Type).To(Equal("error"))
		Expect(parsed.Error.Type).To(Equal("rate_limit_error"))
		Expect(parsed.Error.Message).NotTo(BeEmpty())

		close(inner.release)
		Eventually(done1, "1s").Should(BeClosed())
		Expect(rec1.Code).To(Equal(http.StatusOK))
	})

	It("keeps per-provider limiters independent — saturating one does not affect another", func() {
		innerA := newBlockingHandler()
		innerB := newBlockingHandler()
		limiterA := handler.NewConcurrencyLimiter(innerA, 1, time.Second)
		limiterB := handler.NewConcurrencyLimiter(innerB, 1, time.Second)

		recA := httptest.NewRecorder()
		doneA := serveAsync(limiterA, recA, newMessagesRequest())
		Eventually(innerA.entryCount, "1s", "10ms").Should(Equal(1), "limiter A must be saturated")

		// B's slot is free, so a request through B completes while A stays blocked.
		close(innerB.release)
		recB := httptest.NewRecorder()
		limiterB.ServeHTTP(recB, newMessagesRequest())

		Expect(recB.Code).To(Equal(http.StatusOK))
		Expect(innerB.entryCount()).To(Equal(1), "B's request must have entered")
		Expect(innerA.entryCount()).To(Equal(1), "A's in-flight count must not change")

		close(innerA.release)
		Eventually(doneA, "1s").Should(BeClosed())
	})

	It("never acquires a slot for a client that disconnects while queued", func() {
		inner := newBlockingHandler()
		limiter := handler.NewConcurrencyLimiter(inner, 1, time.Second)

		rec1 := httptest.NewRecorder()
		done1 := serveAsync(limiter, rec1, newMessagesRequest())
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(1), "request 1 must hold the slot")

		cancelledCtx, cancel := context.WithCancel(context.Background())
		cancel()
		req2 := newMessagesRequest().WithContext(cancelledCtx)
		rec2 := httptest.NewRecorder()
		limiter.ServeHTTP(rec2, req2)

		Expect(
			inner.entryCount(),
		).To(Equal(1), "disconnected request must not acquire a slot or be forwarded")

		close(inner.release)
		Eventually(done1, "1s").Should(BeClosed())
		Expect(rec1.Code).To(Equal(http.StatusOK))
	})
})

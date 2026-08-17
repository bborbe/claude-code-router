// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"flag"
	"net/http"
	"net/http/httptest"
	"regexp"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// countingInner records how many requests reached it, which headers it
// observed, and the request context, so specs can assert both that the
// middleware let a request through and that x-api-key was stripped (or, in
// the feature-off case, left intact) before the wrapped handler saw it.
type countingInner struct {
	calls int
	seen  http.Header
	ctx   context.Context
}

func (c *countingInner) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.calls++
	c.seen = r.Header.Clone()
	c.ctx = r.Context()
	w.WriteHeader(http.StatusOK)
}

var _ = Describe("AuthMiddleware", func() {
	var (
		inner *countingInner
		mw    http.Handler
		rec   *httptest.ResponseRecorder
	)

	BeforeEach(func() {
		inner = &countingInner{}
		rec = httptest.NewRecorder()
	})

	// newRequest builds a /v1/messages request with the given remote address
	// and, when apiKey is non-empty, an x-api-key header. The header is set
	// in non-canonical case on purpose — net/http canonicalises on read, and
	// the middleware must match regardless.
	newRequest := func(remoteAddr, apiKey string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		req.RemoteAddr = remoteAddr
		if apiKey != "" {
			req.Header.Set("x-api-key", apiKey)
		}
		return req
	}

	It(
		"returns next unchanged when the key set is empty and passes requests through untouched",
		func() {
			mw = handler.NewAuthMiddleware(inner, map[string]struct{}{})
			Expect(
				mw,
			).To(BeIdenticalTo(http.Handler(inner)), "empty key set must return next unchanged")

			mw.ServeHTTP(rec, newRequest("10.0.0.1:12345", "secret"))

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(inner.calls).To(Equal(1))
			// Feature-off contract (AC 1): the inbound request is not mutated —
			// the wrapped handler still observes the x-api-key header, and no
			// key is recorded in the request context.
			Expect(inner.seen.Get("X-Api-Key")).To(Equal("secret"))
			Expect(handler.PresentedApiKeyFromContext(inner.ctx)).To(Equal(""))
		},
	)

	It(
		"bypasses the key check for loopback requests but still strips x-api-key and records the key",
		func() {
			mw = handler.NewAuthMiddleware(inner, map[string]struct{}{"secret": {}})
			mw.ServeHTTP(rec, newRequest("127.0.0.1:54321", "secret"))

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(inner.calls).To(Equal(1))
			Expect(inner.seen).NotTo(HaveKey("X-Api-Key"))
			Expect(inner.seen).NotTo(HaveKey("x-api-key"))
			Expect(handler.PresentedApiKeyFromContext(inner.ctx)).To(Equal("secret"))
		},
	)

	It("rejects with 401 when the header is missing", func() {
		mw = handler.NewAuthMiddleware(inner, map[string]struct{}{"secret": {}})
		mw.ServeHTTP(rec, newRequest("10.0.0.1:12345", ""))

		Expect(rec.Code).To(Equal(http.StatusUnauthorized))
		Expect(inner.calls).To(Equal(0))
	})

	It("rejects with 401 when the key is wrong", func() {
		mw = handler.NewAuthMiddleware(inner, map[string]struct{}{"secret": {}})
		mw.ServeHTTP(rec, newRequest("10.0.0.1:12345", "wrong"))

		Expect(rec.Code).To(Equal(http.StatusUnauthorized))
		Expect(inner.calls).To(Equal(0))
	})

	It("forwards a non-loopback request with the matching key and strips the header", func() {
		mw = handler.NewAuthMiddleware(inner, map[string]struct{}{"secret": {}})
		mw.ServeHTTP(rec, newRequest("10.0.0.1:12345", "secret"))

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(inner.calls).To(Equal(1))
		Expect(inner.seen).NotTo(HaveKey("X-Api-Key"))
		Expect(inner.seen).NotTo(HaveKey("x-api-key"))
		Expect(handler.PresentedApiKeyFromContext(inner.ctx)).To(Equal("secret"))
	})

	It("accepts a key from a multi-key registry and rejects one not in it", func() {
		mw = handler.NewAuthMiddleware(inner, map[string]struct{}{"a": {}, "secret": {}, "z": {}})
		mw.ServeHTTP(rec, newRequest("10.0.0.1:12345", "secret"))
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(inner.calls).To(Equal(1))

		rec2 := httptest.NewRecorder()
		mw.ServeHTTP(rec2, newRequest("10.0.0.1:12345", "s3cret"))
		Expect(rec2.Code).To(Equal(http.StatusUnauthorized))
		Expect(inner.calls).To(Equal(1), "rejected request must never reach the wrapped handler")
	})

	Context("whitespace-only key is a literal, not empty", func() {
		It("accepts a request presenting the single-space key verbatim", func() {
			mw = handler.NewAuthMiddleware(inner, map[string]struct{}{" ": {}})
			mw.ServeHTTP(rec, newRequest("10.0.0.1:12345", " "))

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(inner.calls).To(Equal(1))
		})

		It("rejects a request with no header", func() {
			mw = handler.NewAuthMiddleware(inner, map[string]struct{}{" ": {}})
			mw.ServeHTTP(rec, newRequest("10.0.0.1:12345", ""))

			Expect(rec.Code).To(Equal(http.StatusUnauthorized))
			Expect(inner.calls).To(Equal(0))
		})

		It("rejects a request presenting an empty-string header", func() {
			mw = handler.NewAuthMiddleware(inner, map[string]struct{}{" ": {}})
			req := newRequest("10.0.0.1:12345", "")
			req.Header.Set("x-api-key", "")
			mw.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusUnauthorized))
			Expect(inner.calls).To(Equal(0))
		})
	})

	Context("rejection log", func() {
		BeforeEach(func() {
			_ = flag.Set("logtostderr", "true")
		})

		AfterEach(func() {
			_ = flag.Set("v", "0")
		})

		It(
			"emits exactly one 'auth rejected remote=' line that never contains the presented key",
			func() {
				mw = handler.NewAuthMiddleware(inner, map[string]struct{}{"secret": {}})
				req := newRequest("10.0.0.1:12345", "leak-canary-presented-key")
				out := captureStderr(func() {
					mw.ServeHTTP(rec, req)
				})

				lines := regexp.MustCompile(`(?m)^.*auth rejected remote=.*$`).
					FindAllString(out, -1)
				Expect(lines).To(HaveLen(1), "exactly one rejection line expected, got: %s", out)
				Expect(lines[0]).To(ContainSubstring("auth rejected remote=10.0.0.1:12345"))
				Expect(lines[0]).NotTo(ContainSubstring("leak-canary-presented-key"))
				Expect(rec.Code).To(Equal(http.StatusUnauthorized))
				Expect(inner.calls).To(Equal(0))
			},
		)
	})
})

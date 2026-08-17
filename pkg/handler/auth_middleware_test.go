// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"flag"
	"net/http"
	"net/http/httptest"
	"regexp"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// countingInner records how many requests reached it and which headers it
// observed, so specs can assert both that the middleware let a request
// through and that x-router-key was stripped before the wrapped handler
// saw it.
type countingInner struct {
	calls int
	seen  http.Header
}

func (c *countingInner) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.calls++
	c.seen = r.Header.Clone()
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
	// and, when routerKey is non-empty, an x-router-key header. The header is
	// set in non-canonical case on purpose — net/http canonicalises on read,
	// and the middleware must match regardless.
	newRequest := func(remoteAddr, routerKey string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		req.RemoteAddr = remoteAddr
		if routerKey != "" {
			req.Header.Set("x-router-key", routerKey)
		}
		return req
	}

	It(
		"returns next unchanged when auth is disabled (empty key) and passes requests through",
		func() {
			mw = handler.NewAuthMiddleware(inner, "")
			Expect(
				mw,
			).To(BeIdenticalTo(http.Handler(inner)), "empty key must return next unchanged")

			mw.ServeHTTP(rec, newRequest("10.0.0.1:12345", "secret"))

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(inner.calls).To(Equal(1))
		},
	)

	It("bypasses the key check for loopback requests but still strips x-router-key", func() {
		mw = handler.NewAuthMiddleware(inner, "secret")
		mw.ServeHTTP(rec, newRequest("127.0.0.1:54321", "secret"))

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(inner.calls).To(Equal(1))
		Expect(inner.seen).NotTo(HaveKey("X-Router-Key"))
		Expect(inner.seen).NotTo(HaveKey("x-router-key"))
	})

	It("rejects with 401 when the header is missing", func() {
		mw = handler.NewAuthMiddleware(inner, "secret")
		mw.ServeHTTP(rec, newRequest("10.0.0.1:12345", ""))

		Expect(rec.Code).To(Equal(http.StatusUnauthorized))
		Expect(inner.calls).To(Equal(0))
	})

	It("rejects with 401 when the key is wrong", func() {
		mw = handler.NewAuthMiddleware(inner, "secret")
		mw.ServeHTTP(rec, newRequest("10.0.0.1:12345", "wrong"))

		Expect(rec.Code).To(Equal(http.StatusUnauthorized))
		Expect(inner.calls).To(Equal(0))
	})

	It("forwards a non-loopback request with the matching key and strips the header", func() {
		mw = handler.NewAuthMiddleware(inner, "secret")
		mw.ServeHTTP(rec, newRequest("10.0.0.1:12345", "secret"))

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(inner.calls).To(Equal(1))
		Expect(inner.seen).NotTo(HaveKey("X-Router-Key"))
		Expect(inner.seen).NotTo(HaveKey("x-router-key"))
	})

	Context("whitespace-only key is a literal, not empty", func() {
		It("accepts a request presenting the single-space key verbatim", func() {
			mw = handler.NewAuthMiddleware(inner, " ")
			mw.ServeHTTP(rec, newRequest("10.0.0.1:12345", " "))

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(inner.calls).To(Equal(1))
		})

		It("rejects a request with no header", func() {
			mw = handler.NewAuthMiddleware(inner, " ")
			mw.ServeHTTP(rec, newRequest("10.0.0.1:12345", ""))

			Expect(rec.Code).To(Equal(http.StatusUnauthorized))
			Expect(inner.calls).To(Equal(0))
		})

		It("rejects a request presenting an empty-string header", func() {
			mw = handler.NewAuthMiddleware(inner, " ")
			req := newRequest("10.0.0.1:12345", "")
			req.Header.Set("x-router-key", "")
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
				mw = handler.NewAuthMiddleware(inner, "secret")
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

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// lowerCaseKeys returns h with every key lower-cased, so assertions can
// check for x-session-id without caring which canonical form the header
// name arrived in (Header.Del and Header.Get are case-insensitive, but
// the raw map keys preserve the wire casing).
func lowerCaseKeys(h http.Header) map[string][]string {
	out := make(map[string][]string, len(h))
	for k, v := range h {
		out[strings.ToLower(k)] = v
	}
	return out
}

var _ = Describe("SessionMiddleware", func() {
	var (
		next     http.Handler
		received *http.Request
	)

	BeforeEach(func() {
		received = nil
		next = http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			received = r
		})
	})

	It("strips X-Session-Id and carries the value in context", func() {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		req.Header.Set("X-Session-Id", "sess-1")
		handler.NewSessionMiddleware(next).ServeHTTP(httptest.NewRecorder(), req)
		Expect(handler.SessionIDFromContext(received.Context())).To(Equal("sess-1"))
		Expect(received.Header.Get("X-Session-Id")).To(Equal(""))
		Expect(lowerCaseKeys(received.Header)).NotTo(HaveKey("x-session-id"))
	})

	It("strips a lower-case x-session-id header and carries the value in context", func() {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		req.Header.Set("x-session-id", "sess-2")
		handler.NewSessionMiddleware(next).ServeHTTP(httptest.NewRecorder(), req)
		Expect(handler.SessionIDFromContext(received.Context())).To(Equal("sess-2"))
		Expect(received.Header.Get("X-Session-Id")).To(Equal(""))
		Expect(lowerCaseKeys(received.Header)).NotTo(HaveKey("x-session-id"))
	})

	It("leaves the context empty and the header absent when no session id is sent", func() {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		handler.NewSessionMiddleware(next).ServeHTTP(httptest.NewRecorder(), req)
		Expect(handler.SessionIDFromContext(received.Context())).To(Equal(""))
		Expect(lowerCaseKeys(received.Header)).NotTo(HaveKey("x-session-id"))
	})

	It("invokes the wrapped handler exactly once per request", func() {
		invocations := 0
		wrapped := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			invocations++
			w.WriteHeader(http.StatusOK)
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		req.Header.Set("X-Session-Id", "sess-1")
		handler.NewSessionMiddleware(wrapped).ServeHTTP(httptest.NewRecorder(), req)
		Expect(invocations).To(Equal(1))
	})
})

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"flag"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// guardCountingInner records how many requests reached it so specs can
// assert that a loopback request was forwarded to the wrapped handler and
// that a refused request never reached it (the /gc side-effect gate).
type guardCountingInner struct {
	calls int
}

func (c *guardCountingInner) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	c.calls++
	w.WriteHeader(http.StatusOK)
}

var _ = Describe("AdminLoopbackGuard", func() {
	var (
		inner *guardCountingInner
		guard http.Handler
		rec   *httptest.ResponseRecorder
	)

	BeforeEach(func() {
		inner = &guardCountingInner{}
		rec = httptest.NewRecorder()
	})

	newRequest := func(remoteAddr string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/enabletrace", nil)
		req.RemoteAddr = remoteAddr
		return req
	}

	Context("loopback requests", func() {
		It("forwards to next when isLoopback reports true", func() {
			guard = handler.NewAdminLoopbackGuard(inner, func(string) bool { return true })
			guard.ServeHTTP(rec, newRequest("10.0.0.1:12345"))

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(inner.calls).To(Equal(1))
		})

		It("passes the real IsLoopbackRemoteAddr for IPv4 and IPv6 loopback", func() {
			guard = handler.NewAdminLoopbackGuard(inner, handler.IsLoopbackRemoteAddr)
			for i, addr := range []string{"127.0.0.1:12345", "[::1]:12345"} {
				rec = httptest.NewRecorder()
				guard.ServeHTTP(rec, newRequest(addr))
				Expect(rec.Code).To(Equal(http.StatusOK))
				Expect(inner.calls).To(Equal(i + 1))
			}
		})
	})

	Context("non-loopback requests", func() {
		BeforeEach(func() {
			guard = handler.NewAdminLoopbackGuard(inner, handler.IsLoopbackRemoteAddr)
		})

		It("rejects with 403 text/plain and never calls next", func() {
			guard.ServeHTTP(rec, newRequest("10.0.0.1:12345"))

			Expect(rec.Code).To(Equal(http.StatusForbidden))
			Expect(rec.Header().Get("Content-Type")).To(ContainSubstring("text/plain"))
			Expect(rec.Body.String()).To(Equal("admin endpoint loopback-only\n"))
			Expect(inner.calls).To(Equal(0), "inner handler must not run for a refused request")
		})

		It("ignores a spoofed X-Forwarded-For loopback header", func() {
			req := newRequest("10.0.0.1:12345")
			req.Header.Set("X-Forwarded-For", "127.0.0.1")
			guard.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusForbidden))
			Expect(inner.calls).To(Equal(0))
		})

		It("ignores a spoofed X-Real-IP loopback header", func() {
			req := newRequest("10.0.0.1:12345")
			req.Header.Set("X-Real-IP", "::1")
			guard.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusForbidden))
			Expect(inner.calls).To(Equal(0))
		})
	})

	Context("refusal log line", func() {
		BeforeEach(func() {
			_ = flag.Set("logtostderr", "true")
		})

		AfterEach(func() {
			_ = flag.Set("v", "0")
		})

		It("emits exactly one 'admin refused' line with the remote as the last field", func() {
			guard = handler.NewAdminLoopbackGuard(inner, handler.IsLoopbackRemoteAddr)
			out := captureStderr(func() {
				guard.ServeHTTP(rec, newRequest("10.0.0.1:12345"))
			})

			lines := regexp.MustCompile(`(?m)^.*admin refused.*$`).FindAllString(out, -1)
			Expect(lines).To(HaveLen(1), "exactly one refusal line expected, got: %s", out)
			Expect(lines[0]).To(ContainSubstring("admin refused path=POST /enabletrace"))
			fields := strings.Fields(lines[0])
			Expect(fields[len(fields)-1]).To(Equal("remote=10.0.0.1:12345"))
			Expect(rec.Code).To(Equal(http.StatusForbidden))
			Expect(inner.calls).To(Equal(0))
		})
	})
})

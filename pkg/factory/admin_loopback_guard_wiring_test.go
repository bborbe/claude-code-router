// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory_test

import (
	"context"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/factory"
	"github.com/bborbe/claude-code-router/pkg/handler"
)

var _ = Describe("CreateRouterFromConfig admin loopback guard", func() {
	var mux http.Handler

	newConfig := func() *pkg.Config {
		return &pkg.Config{
			Router: pkg.Router{DefaultProvider: "test"},
			Providers: map[string]pkg.Provider{
				"test": {Upstream: "http://localhost:9999", Models: []string{"*"}},
			},
			Trace: false,
		}
	}

	BeforeEach(func() {
		var err error
		mux, err = factory.CreateRouterFromConfig(
			context.Background(),
			newConfig(),
			factory.WithMetricsRegisterer(prometheus.NewRegistry()),
		)
		Expect(err).NotTo(HaveOccurred())
	})

	// adminRequest builds a request for an admin route and stamps the given
	// remote address by hand. A real TCP client on the loopback interface can
	// never produce a non-loopback RemoteAddr, so the remote-403 assertions
	// would silently test the wrong thing if served over httptest.NewServer.
	adminRequest := func(method, target, remoteAddr string) *http.Request {
		req := httptest.NewRequest(method, target, nil)
		req.RemoteAddr = remoteAddr
		return req
	}

	serve := func(req *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	DescribeTable(
		"loopback requests pass the guard and reach the inner handler",
		func(method, target string, wantStatus int, wantBodySubstring string) {
			rec := serve(adminRequest(method, target, "127.0.0.1:12345"))
			Expect(
				rec.Code,
			).To(Equal(wantStatus), "loopback %s %s must not be refused", method, target)
			Expect(rec.Body.String()).To(ContainSubstring(wantBodySubstring))
		},
		Entry(
			"GET /setloglevel/1",
			http.MethodGet,
			"/setloglevel/1",
			http.StatusOK,
			"set loglevel to 1 completed",
		),
		Entry("POST /enabletrace", http.MethodPost, "/enabletrace", http.StatusOK, "trace enabled"),
		Entry(
			"POST /disabletrace",
			http.MethodPost,
			"/disabletrace",
			http.StatusOK,
			"trace disabled",
		),
		Entry("POST /gc", http.MethodPost, "/gc", http.StatusOK, "Memory Stats"),
	)

	It("passes IPv6 loopback through the guard", func() {
		rec := serve(adminRequest(http.MethodPost, "/enabletrace", "[::1]:12345"))
		Expect(rec.Code).To(Equal(http.StatusOK))
	})

	DescribeTable("non-loopback requests are refused with 403",
		func(method, target string) {
			rec := serve(adminRequest(method, target, "10.0.0.1:12345"))
			Expect(rec.Code).To(Equal(http.StatusForbidden))
			Expect(rec.Header().Get("Content-Type")).To(ContainSubstring("text/plain"))
			Expect(rec.Body.String()).To(Equal("admin endpoint loopback-only\n"))
		},
		Entry("GET /setloglevel/2", http.MethodGet, "/setloglevel/2"),
		Entry("POST /enabletrace", http.MethodPost, "/enabletrace"),
		Entry("POST /disabletrace", http.MethodPost, "/disabletrace"),
		Entry("POST /gc", http.MethodPost, "/gc"),
	)

	Context("spoofed forwarded headers are ignored", func() {
		It("X-Forwarded-For: 127.0.0.1 does not bypass the guard", func() {
			req := adminRequest(http.MethodPost, "/enabletrace", "10.0.0.1:12345")
			req.Header.Set("X-Forwarded-For", "127.0.0.1")
			rec := serve(req)

			Expect(rec.Code).To(Equal(http.StatusForbidden))
		})

		It("X-Real-IP: ::1 does not bypass the guard", func() {
			req := adminRequest(http.MethodPost, "/disabletrace", "10.0.0.1:12345")
			req.Header.Set("X-Real-IP", "::1")
			rec := serve(req)

			Expect(rec.Code).To(Equal(http.StatusForbidden))
		})
	})

	Context("state-changing behaviour is gated", func() {
		// HOME points at a temp dir for both specs: if tracing were ever left
		// on, a later /v1 request would write trace files into the real home
		// directory via traceDir() -> os.UserHomeDir().
		BeforeEach(func() {
			oldHome := os.Getenv("HOME")
			Expect(os.Setenv("HOME", GinkgoT().TempDir())).To(Succeed())
			DeferCleanup(func() {
				Expect(os.Setenv("HOME", oldHome)).To(Succeed())
			})
			DeferCleanup(handler.DefaultTraceState().Disable)
			// Reset to a clean start: the earlier loopback-bypass specs flip
			// the process-global trace flag on, so the remote-assertion below
			// would otherwise start from an already-enabled state.
			handler.DefaultTraceState().Disable()
		})

		It("a remote POST /enabletrace leaves tracing off", func() {
			rec := serve(adminRequest(http.MethodPost, "/enabletrace", "10.0.0.1:12345"))

			Expect(rec.Code).To(Equal(http.StatusForbidden))
			Expect(handler.DefaultTraceState().IsEnabled()).To(BeFalse())
		})

		It("a loopback POST /enabletrace flips tracing on", func() {
			rec := serve(adminRequest(http.MethodPost, "/enabletrace", "127.0.0.1:12345"))

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(handler.DefaultTraceState().IsEnabled()).To(BeTrue())
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
			out := captureStderr(func() {
				rec := serve(adminRequest(http.MethodPost, "/enabletrace", "10.0.0.1:12345"))
				Expect(rec.Code).To(Equal(http.StatusForbidden))
			})

			lines := regexp.MustCompile(`(?m)^.*admin refused.*$`).FindAllString(out, -1)
			Expect(lines).To(HaveLen(1), "exactly one refusal line expected, got: %s", out)
			Expect(lines[0]).To(ContainSubstring("admin refused path=POST /enabletrace"))
			fields := strings.Fields(lines[0])
			Expect(fields[len(fields)-1]).To(Equal("remote=10.0.0.1:12345"))
		})
	})

	DescribeTable("read-only endpoints stay open to remote callers",
		func(method, target string, wantStatus int) {
			rec := serve(adminRequest(method, target, "10.0.0.1:12345"))
			Expect(
				rec.Code,
			).To(Equal(wantStatus), "read-only %s %s must not be refused", method, target)
		},
		Entry("GET /healthz", http.MethodGet, "/healthz", http.StatusOK),
		Entry("GET /readiness", http.MethodGet, "/readiness", http.StatusOK),
		Entry("GET /metrics", http.MethodGet, "/metrics", http.StatusOK),
		Entry("HEAD /", http.MethodHead, "/", http.StatusOK),
	)
})

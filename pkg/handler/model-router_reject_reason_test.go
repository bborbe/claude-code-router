// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"flag"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	stdtime "time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// The [req] line of a 429 carries reason=<r>, the same cause
// ccrouter_rejected_total records, so a log line alone tells the member
// limiter, the host limiter and the upstream apart. Kept in its own file
// because model-router_test.go is at the revive file-length-limit; the
// shared harness helpers live there.
var _ = Describe("ModelRouter rejection reason in [req] line", func() {
	BeforeEach(func() {
		_ = flag.Set("logtostderr", "true")
		_ = flag.Set("v", "2")
	})

	post := func() *http.Request {
		return httptest.NewRequest(
			http.MethodPost,
			"/v1/messages",
			strings.NewReader(`{"model":"deepseek-v4-flash"}`),
		)
	}

	routerOver := func(h http.Handler) http.Handler {
		return handler.NewModelRouter(
			[]handler.ModelRoute{{Pattern: "deepseek-*", ProviderName: "p", Handler: h}},
			"default-fallback",
			labelHandler("fallback"),
			nil,
			alwaysSample,
			testMetrics,
			testDateTime,
		)
	}

	// saturatedLogLine holds the limiter's single slot with a request sent
	// to the limiter directly (outside the router, so it is not logged),
	// then sends one request through the router and returns its log output.
	saturatedLogLine := func(wrap func(next http.Handler) http.Handler) (int, string) {
		started := make(chan struct{}, 1)
		release := make(chan struct{})
		limited := wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started <- struct{}{}
			<-release
		}))

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			limited.ServeHTTP(httptest.NewRecorder(), post())
		}()
		<-started

		rec := httptest.NewRecorder()
		out := captureStderr(func() {
			routerOver(limited).ServeHTTP(rec, post())
		})

		close(release)
		wg.Wait()
		return rec.Code, out
	}

	It("logs reason=queue_timeout when the member limiter refuses", func() {
		code, out := saturatedLogLine(func(next http.Handler) http.Handler {
			return handler.NewConcurrencyLimiter(next, "p", 1, 50*stdtime.Millisecond, nil, nil)
		})
		Expect(code).To(Equal(http.StatusTooManyRequests))
		Expect(
			out,
		).To(MatchRegexp(`\[req\] POST /v1/messages model=deepseek-v4-flash provider=p/0 status=429 .* reason=queue_timeout\n`))
	})

	It("logs reason=host_queue_timeout when the shared host budget refuses", func() {
		code, out := saturatedLogLine(func(next http.Handler) http.Handler {
			return handler.NewHostLimiter(1, 50*stdtime.Millisecond).Wrap(next, "p", nil)
		})
		Expect(code).To(Equal(http.StatusTooManyRequests))
		Expect(
			out,
		).To(MatchRegexp(`\[req\] POST /v1/messages model=deepseek-v4-flash provider=p/0 status=429 .* reason=host_queue_timeout\n`))
	})

	It("logs reason=upstream for a 429 no router gate claimed", func() {
		upstream429 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"too many concurrent requests (max 16 per user)"}`))
		})
		rec := httptest.NewRecorder()
		out := captureStderr(func() {
			routerOver(upstream429).ServeHTTP(rec, post())
		})
		Expect(rec.Code).To(Equal(http.StatusTooManyRequests))
		Expect(
			out,
		).To(MatchRegexp(`\[req\] POST /v1/messages model=deepseek-v4-flash provider=p/0 status=429 .* reason=upstream\n`))
	})

	It("adds no reason field to a non-429 line", func() {
		rec := httptest.NewRecorder()
		out := captureStderr(func() {
			routerOver(labelHandler("ok")).ServeHTTP(rec, post())
		})
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(out).To(ContainSubstring("status=200"))
		Expect(out).NotTo(ContainSubstring("reason="))
	})
})

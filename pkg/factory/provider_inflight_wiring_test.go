// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/factory"
)

// The collector specs in pkg/handler build their counter slices by hand, so
// they prove the collector sums and re-reads correctly but not that the
// slices describe the real request path. These specs drive requests through
// CreateRouterFromConfig and scrape the exported series, which is what pins
// the wiring: that the counters come from the member limiters actually in
// the path, that they are captured from the UNWRAPPED member (not the shared
// host limiter wrapped outside it), and that an uncapped member's nil
// counters read as 0. The sibling upstream_inflight_wiring_test.go covers
// the same ground for ccrouter_upstream_inflight.
var _ = Describe("CreateRouterFromConfig provider inflight wiring", func() {
	newMessagesRequest := func(model string) *http.Request {
		body := fmt.Sprintf(
			`{"model":%q,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			model,
		)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		return req
	}

	serveAsync := func(h http.Handler, req *http.Request) chan struct{} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			h.ServeHTTP(httptest.NewRecorder(), req)
		}()
		return done
	}

	scrape := func(h http.Handler) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		Expect(rec.Code).To(Equal(http.StatusOK))
		return rec.Body.String()
	}

	It("splits a capped provider's requests into dispatched and queued", func() {
		srv := newBlockingUpstream()
		defer srv.close()

		cfg := &pkg.Config{
			Router: pkg.Router{DefaultProvider: "p"},
			Providers: map[string]pkg.Provider{
				"p": {
					Upstream:                 srv.srv.URL,
					Models:                   []string{"m*"},
					MaxConcurrentRequests:    1,
					MaxConcurrentWaitSeconds: 5,
				},
			},
		}
		router, err := factory.CreateRouterFromConfig(
			context.Background(),
			cfg,
			factory.WithMetricsRegisterer(prometheus.NewRegistry()),
		)
		Expect(err).NotTo(HaveOccurred())

		// Both series exist at rest, before any request has been served.
		Expect(scrape(router)).To(ContainSubstring(`ccrouter_provider_dispatched{provider="p"} 0`))
		Expect(scrape(router)).To(ContainSubstring(`ccrouter_provider_queued{provider="p"} 0`))

		done1 := serveAsync(router, newMessagesRequest("m1"))
		Eventually(srv.inFlight, "1s", "10ms").Should(Equal(int32(1)))
		Eventually(func() string { return scrape(router) }, "1s", "10ms").Should(
			ContainSubstring(`ccrouter_provider_dispatched{provider="p"} 1`),
		)
		Expect(scrape(router)).To(ContainSubstring(`ccrouter_provider_queued{provider="p"} 0`))

		// Two more find the single slot held, so they queue behind it.
		done2 := serveAsync(router, newMessagesRequest("m2"))
		done3 := serveAsync(router, newMessagesRequest("m3"))
		Eventually(func() string { return scrape(router) }, "1s", "10ms").Should(
			ContainSubstring(`ccrouter_provider_queued{provider="p"} 2`),
		)
		Expect(scrape(router)).To(ContainSubstring(`ccrouter_provider_dispatched{provider="p"} 1`))

		srv.releaseAll()
		Eventually(done1, "2s").Should(BeClosed())
		Eventually(done2, "2s").Should(BeClosed())
		Eventually(done3, "2s").Should(BeClosed())
		Eventually(func() string { return scrape(router) }, "2s", "10ms").Should(
			ContainSubstring(`ccrouter_provider_dispatched{provider="p"} 0`),
		)
		Eventually(func() string { return scrape(router) }, "2s", "10ms").Should(
			ContainSubstring(`ccrouter_provider_queued{provider="p"} 0`),
		)
	})

	It("reads 0 for an uncapped provider, whose member has no limiter", func() {
		srv := newBlockingUpstream()
		defer srv.close()

		cfg := &pkg.Config{
			Router: pkg.Router{DefaultProvider: "free"},
			Providers: map[string]pkg.Provider{
				"free": {Upstream: srv.srv.URL, Models: []string{"m*"}},
			},
		}
		router, err := factory.CreateRouterFromConfig(
			context.Background(),
			cfg,
			factory.WithMetricsRegisterer(prometheus.NewRegistry()),
		)
		Expect(err).NotTo(HaveOccurred())

		done := serveAsync(router, newMessagesRequest("m1"))
		Eventually(srv.inFlight, "1s", "10ms").Should(Equal(int32(1)))

		// The member returns next unchanged for maxConcurrentRequests <= 0, so
		// both counters are nil and the collector must read them as 0 rather
		// than panicking or dropping the series.
		Expect(scrape(router)).To(
			ContainSubstring(`ccrouter_provider_dispatched{provider="free"} 0`),
		)
		Expect(scrape(router)).To(ContainSubstring(`ccrouter_provider_queued{provider="free"} 0`))

		srv.releaseAll()
		Eventually(done, "2s").Should(BeClosed())
	})
})

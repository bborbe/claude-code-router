// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory_test

import (
	"context"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/factory"
)

var _ = Describe("CreateRouterFromConfig metrics wiring", func() {
	newConfig := func() *pkg.Config {
		return &pkg.Config{
			Router: pkg.Router{DefaultProvider: "test"},
			Providers: map[string]pkg.Provider{
				"test": {Upstream: "http://localhost:9999", Models: []string{"*"}},
			},
			Trace: false,
		}
	}

	It("serves the router's own registerer on /metrics, not the global default", func() {
		// Sentinel registered only on the router's isolated registry. If
		// /metrics fell back to the global default, this series would be
		// absent — the regression the reload freeze relied on.
		routerReg := prometheus.NewRegistry()
		routerReg.MustRegister(prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ccr_test_router_sentinel",
		}))

		mux, err := factory.CreateRouterFromConfig(
			context.Background(),
			newConfig(),
			factory.WithMetricsRegisterer(routerReg),
		)
		Expect(err).NotTo(HaveOccurred())

		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(ContainSubstring("ccr_test_router_sentinel"))
	})

	It("seeds the reload registry with go and process collectors", func() {
		reg := factory.NewReloadRegistry()

		rec := httptest.NewRecorder()
		promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(
			rec,
			httptest.NewRequest(http.MethodGet, "/metrics", nil),
		)
		Expect(rec.Code).To(Equal(http.StatusOK))
		body := rec.Body.String()
		Expect(body).To(ContainSubstring("go_gc_duration_seconds"))
		Expect(body).To(ContainSubstring("process_start_time_seconds"))
	})
})

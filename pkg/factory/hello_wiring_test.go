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

	"github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/factory"
)

var _ = Describe("CreateRouterFromConfig /api/hello connectivity probe", func() {
	var mux http.Handler

	BeforeEach(func() {
		var err error
		mux, err = factory.CreateRouterFromConfig(
			context.Background(),
			&pkg.Config{
				Router: pkg.Router{DefaultProvider: "test"},
				Providers: map[string]pkg.Provider{
					"test": {Upstream: "http://localhost:9999", Models: []string{"*"}},
				},
				Trace: false,
			},
			factory.WithMetricsRegisterer(prometheus.NewRegistry()),
		)
		Expect(err).NotTo(HaveOccurred())
	})

	// The handler-only test cannot prove the route is registered with the
	// right pattern and wins over the "/" catch-all — only driving the
	// request through the full mux can.
	It("answers HEAD /api/hello with 200 OK instead of the 404 catch-all", func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodHead, "/api/hello", nil)

		mux.ServeHTTP(rec, req)

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.Len()).To(Equal(0))
	})

	It("still 404s unknown paths through the catch-all", func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/hello/nope", nil)

		mux.ServeHTTP(rec, req)

		Expect(rec.Code).To(Equal(http.StatusNotFound))
	})
})

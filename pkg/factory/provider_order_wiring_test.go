// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/factory"
)

// Regression lock for spec 010: CreateRouterFromConfig must build its route
// list in provider declaration order. With two providers sharing a model glob
// (deepseek-* on separate quotas), Go map iteration order is random — before
// Config.ProviderOrder existed, the keyless path was a per-restart coin flip
// (verified live 2026-08-18: two consecutive router processes, same config,
// routed keyless deepseek to different providers).
var _ = Describe("CreateRouterFromConfig provider declaration order", func() {
	var dir string

	BeforeEach(func() {
		var err error
		dir, err = os.MkdirTemp("", "ccr-provider-order")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		_ = os.RemoveAll(dir)
	})

	isolatedRegistry := func() factory.RouterOptionFunc {
		return factory.WithMetricsRegisterer(prometheus.NewRegistry())
	}

	It(
		"routes a keyless request for a shared glob to the FIRST-declared provider, not a map-order coin flip",
		func() {
			var alphaHits atomic.Int64
			var betaHits atomic.Int64

			alpha := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					alphaHits.Add(1)
					w.WriteHeader(http.StatusOK)
				}),
			)
			defer alpha.Close()
			beta := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					betaHits.Add(1)
					w.WriteHeader(http.StatusOK)
				}),
			)
			defer beta.Close()

			cfgPath := filepath.Join(dir, "config.yaml")
			// alpha is declared FIRST and serves the same glob as beta.
			cfgYAML := "router:\n" +
				"  default_provider: alpha\n" +
				"providers:\n" +
				"  alpha:\n" +
				"    upstream: " + alpha.URL + "\n" +
				"    models: [\"shared-*\"]\n" +
				"  beta:\n" +
				"    upstream: " + beta.URL + "\n" +
				"    models: [\"shared-*\"]\n"
			Expect(os.WriteFile(cfgPath, []byte(cfgYAML), 0o600)).To(Succeed())

			cfg, err := pkg.Load(context.Background(), cfgPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.ProviderOrder).To(Equal([]string{"alpha", "beta"}))

			handler, err := factory.CreateRouterFromConfig(
				context.Background(),
				cfg,
				isolatedRegistry(),
			)
			Expect(err).NotTo(HaveOccurred())

			body := `{"model":"shared-model","max_tokens":8}`
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(alphaHits.Load()).To(Equal(int64(1)))
			Expect(betaHits.Load()).To(Equal(int64(0)))
		},
	)
})

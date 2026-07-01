// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

var _ = Describe("Metrics", func() {
	var m *handler.Metrics

	BeforeEach(func() {
		m = handler.NewMetrics(nil)
	})

	It("NewMetrics returns non-nil collectors", func() {
		Expect(m.RequestsTotal).NotTo(BeNil())
		Expect(m.RequestDuration).NotTo(BeNil())
		Expect(m.AliasResolutions).NotTo(BeNil())
		Expect(m.TokensTotal).NotTo(BeNil())
	})

	It("Register against a fresh registry succeeds", func() {
		reg := prometheus.NewRegistry()
		Expect(m.Register(reg)).To(Succeed())
	})

	It("second Register against the same registry returns an error", func() {
		reg := prometheus.NewRegistry()
		Expect(m.Register(reg)).To(Succeed())
		Expect(m.Register(reg)).To(HaveOccurred())
	})

	Context("ObserveRequest", func() {
		It("increments 2xx status_class for status 200", func() {
			m.ObserveRequest("p", "model", 200, 0.1, false)
			Expect(
				testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "model", "2xx")),
			).To(Equal(float64(1)))
		})

		It("increments 4xx_bad_request status_class for status 404", func() {
			m.ObserveRequest("p", "model", 404, 0.1, false)
			Expect(
				testutil.ToFloat64(
					m.RequestsTotal.WithLabelValues("p", "model", "4xx_bad_request"),
				),
			).To(Equal(float64(1)))
		})

		It("increments 5xx_upstream status_class for status 500", func() {
			m.ObserveRequest("p", "model", 500, 0.1, false)
			Expect(
				testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "model", "5xx_upstream")),
			).To(Equal(float64(1)))
		})

		It("uses raw status string for out-of-range status 999", func() {
			m.ObserveRequest("p", "model", 999, 0.1, false)
			Expect(
				testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "model", "999")),
			).To(Equal(float64(1)))
		})
	})

	Context("statusClass via ObserveRequest label", func() {
		It("statusClass(200) maps to 2xx", func() {
			m.ObserveRequest("p", "m", 200, 0, false)
			Expect(
				testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "2xx")),
			).To(Equal(float64(1)))
		})

		It("statusClass(404) maps to 4xx_bad_request", func() {
			m.ObserveRequest("p", "m", 404, 0, false)
			Expect(
				testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "4xx_bad_request")),
			).To(Equal(float64(1)))
		})

		It("statusClass(503) maps to 5xx_upstream", func() {
			m.ObserveRequest("p", "m", 503, 0, false)
			Expect(
				testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "5xx_upstream")),
			).To(Equal(float64(1)))
		})

		It("statusClass(999) maps to raw 999", func() {
			m.ObserveRequest("p", "m", 999, 0, false)
			Expect(
				testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "999")),
			).To(Equal(float64(1)))
		})

		It("statusClass(401) maps to 4xx_auth", func() {
			m.ObserveRequest("p", "m", 401, 0, false)
			Expect(
				testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "4xx_auth")),
			).To(Equal(float64(1)))
		})

		It("statusClass(403) maps to 4xx_auth", func() {
			m.ObserveRequest("p", "m", 403, 0, false)
			Expect(
				testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "4xx_auth")),
			).To(Equal(float64(1)))
		})

		It("statusClass(429) maps to 4xx_rate_limited", func() {
			m.ObserveRequest("p", "m", 429, 0, false)
			Expect(
				testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "4xx_rate_limited")),
			).To(Equal(float64(1)))
		})

		It("statusClass(500) with isRouterError=false maps to 5xx_upstream", func() {
			m.ObserveRequest("p", "m", 500, 0, false)
			Expect(
				testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "5xx_upstream")),
			).To(Equal(float64(1)))
		})

		It("statusClass(500) with isRouterError=true maps to 5xx_router", func() {
			m.ObserveRequest("p", "m", 500, 0, true)
			Expect(
				testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "5xx_router")),
			).To(Equal(float64(1)))
		})

		It("statusClass(502) with isRouterError=false maps to 5xx_upstream", func() {
			m.ObserveRequest("p", "m", 502, 0, false)
			Expect(
				testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "5xx_upstream")),
			).To(Equal(float64(1)))
		})

		It("statusClass(413) maps to 4xx_bad_request", func() {
			m.ObserveRequest("p", "m", 413, 0, true)
			Expect(
				testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "4xx_bad_request")),
			).To(Equal(float64(1)))
		})

		It("statusClass(400) maps to 4xx_bad_request", func() {
			m.ObserveRequest("p", "m", 400, 0, true)
			Expect(
				testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "4xx_bad_request")),
			).To(Equal(float64(1)))
		})
	})

	It("ObserveAliasResolution increments the alias counter once", func() {
		m.ObserveAliasResolution("m3", "MiniMax-M3-highspeed")
		Expect(
			testutil.ToFloat64(m.AliasResolutions.WithLabelValues("m3", "MiniMax-M3-highspeed")),
		).To(Equal(float64(1)))
	})

	Context("NewMetrics with alias map", func() {
		It("pre-initializes one counter series per declared alias", func() {
			aliasMetrics := handler.NewMetrics(map[string]string{
				"qwen": "qwen-coder",
				"m3":   "MiniMax-M3-highspeed",
			})
			Expect(testutil.CollectAndCount(aliasMetrics.AliasResolutions)).To(Equal(2))
		})

		It("creates no series and does not panic when aliases is nil", func() {
			var m *handler.Metrics
			Expect(func() { m = handler.NewMetrics(nil) }).NotTo(Panic())
			Expect(testutil.CollectAndCount(m.AliasResolutions)).To(Equal(0))
		})
	})

	// Anti-fake: token counts vary across specs (42, 17, 10, 5) so a hardcoded Add(1) implementation fails these assertions.
	Context("ObserveTokens", func() {
		It("increments input-direction series with positive count", func() {
			m.ObserveTokens("anthropic-subscription", "claude-opus-4", "input", 42)
			Expect(
				testutil.ToFloat64(
					m.TokensTotal.WithLabelValues(
						"anthropic-subscription",
						"claude-opus-4",
						"input",
					),
				),
			).To(Equal(float64(42)))
		})

		It("increments output-direction series with positive count", func() {
			m.ObserveTokens("anthropic-subscription", "claude-opus-4", "output", 17)
			Expect(
				testutil.ToFloat64(
					m.TokensTotal.WithLabelValues(
						"anthropic-subscription",
						"claude-opus-4",
						"output",
					),
				),
			).To(Equal(float64(17)))
		})

		It("accumulates repeated Adds on the same tuple", func() {
			m.ObserveTokens("p", "m", "input", 10)
			m.ObserveTokens("p", "m", "input", 5)
			Expect(
				testutil.ToFloat64(m.TokensTotal.WithLabelValues("p", "m", "input")),
			).To(Equal(float64(15)))
		})

		It("drops zero count without creating a series", func() {
			m.ObserveTokens("p", "m", "input", 0)
			Expect(testutil.CollectAndCount(m.TokensTotal)).To(Equal(0))
		})

		It("drops negative count without creating a series", func() {
			m.ObserveTokens("p", "m", "input", -1)
			Expect(testutil.CollectAndCount(m.TokensTotal)).To(Equal(0))
		})

		It("drops unknown direction (sideways) without creating a series", func() {
			m.ObserveTokens("p", "m", "sideways", 5)
			Expect(testutil.CollectAndCount(m.TokensTotal)).To(Equal(0))
		})

		It("drops empty direction without creating a series", func() {
			m.ObserveTokens("p", "m", "", 5)
			Expect(testutil.CollectAndCount(m.TokensTotal)).To(Equal(0))
		})
	})

	Context("UnknownModelLabel constant", func() {
		It("resolves to '_unknown_' (leading + trailing underscore)", func() {
			Expect(handler.UnknownModelLabel).To(Equal("_unknown_"))
		})
	})
})

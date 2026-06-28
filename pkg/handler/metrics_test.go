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
			m.ObserveRequest("p", "model", 200, 0.1)
			Expect(
				testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "model", "2xx")),
			).To(Equal(float64(1)))
		})

		It("increments 4xx status_class for status 404", func() {
			m.ObserveRequest("p", "model", 404, 0.1)
			Expect(
				testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "model", "4xx")),
			).To(Equal(float64(1)))
		})

		It("increments 5xx status_class for status 500", func() {
			m.ObserveRequest("p", "model", 500, 0.1)
			Expect(
				testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "model", "5xx")),
			).To(Equal(float64(1)))
		})

		It("uses raw status string for out-of-range status 999", func() {
			m.ObserveRequest("p", "model", 999, 0.1)
			Expect(
				testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "model", "999")),
			).To(Equal(float64(1)))
		})
	})

	Context("statusClass via ObserveRequest label", func() {
		It("statusClass(200) maps to 2xx", func() {
			m.ObserveRequest("p", "m", 200, 0)
			Expect(
				testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "2xx")),
			).To(Equal(float64(1)))
		})

		It("statusClass(404) maps to 4xx", func() {
			m.ObserveRequest("p", "m", 404, 0)
			Expect(
				testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "4xx")),
			).To(Equal(float64(1)))
		})

		It("statusClass(503) maps to 5xx", func() {
			m.ObserveRequest("p", "m", 503, 0)
			Expect(
				testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "5xx")),
			).To(Equal(float64(1)))
		})

		It("statusClass(999) maps to raw 999", func() {
			m.ObserveRequest("p", "m", 999, 0)
			Expect(
				testutil.ToFloat64(m.RequestsTotal.WithLabelValues("p", "m", "999")),
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
})

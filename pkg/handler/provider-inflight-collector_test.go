// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// gaugeFor returns the value of the metric named metricName carrying
// provider as its provider label, and whether such a series was emitted.
func gaugeFor(
	families []*dto.MetricFamily,
	metricName string,
	provider string,
) (float64, bool) {
	for _, family := range families {
		if family.GetName() != metricName {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "provider" && label.GetValue() == provider {
					return metric.GetGauge().GetValue(), true
				}
			}
		}
	}
	return 0, false
}

var _ = Describe("ProviderInFlightCollector", func() {
	var registry *prometheus.Registry

	BeforeEach(func() {
		registry = prometheus.NewRegistry()
	})

	gather := func() []*dto.MetricFamily {
		families, err := registry.Gather()
		Expect(err).To(BeNil())
		return families
	}

	expectGauge := func(
		families []*dto.MetricFamily,
		metricName string,
		provider string,
		expected float64,
	) {
		value, found := gaugeFor(families, metricName, provider)
		Expect(found).To(BeTrue(), "series %s{provider=%q} must exist", metricName, provider)
		Expect(value).To(Equal(expected), "series %s{provider=%q}", metricName, provider)
	}

	It("sums a provider's members instead of sampling one limiter", func() {
		dispatched := map[string][]func() int{
			"alpha": {func() int { return 2 }, func() int { return 1 }},
			"beta":  {func() int { return 3 }},
		}
		queued := map[string][]func() int{
			"alpha": {func() int { return 5 }, func() int { return 1 }},
			"beta":  {func() int { return 0 }},
		}
		Expect(
			registry.Register(handler.NewProviderInFlightCollector(dispatched, queued)),
		).To(Succeed())

		families := gather()
		expectGauge(families, "ccrouter_provider_dispatched", "alpha", 3)
		expectGauge(families, "ccrouter_provider_queued", "alpha", 6)
		expectGauge(families, "ccrouter_provider_dispatched", "beta", 3)
		expectGauge(families, "ccrouter_provider_queued", "beta", 0)
	})

	It("skips a nil member — an uncapped member has no limiter to read", func() {
		dispatched := map[string][]func() int{"alpha": {nil, func() int { return 2 }}}
		queued := map[string][]func() int{"alpha": {nil, func() int { return 4 }}}
		Expect(
			registry.Register(handler.NewProviderInFlightCollector(dispatched, queued)),
		).To(Succeed())

		families := gather()
		expectGauge(families, "ccrouter_provider_dispatched", "alpha", 2)
		expectGauge(families, "ccrouter_provider_queued", "alpha", 4)
	})

	It("reads the counters live at scrape time, so a later change is visible", func() {
		dispatchedValue := 0
		queuedValue := 0
		dispatched := map[string][]func() int{"alpha": {func() int { return dispatchedValue }}}
		queued := map[string][]func() int{"alpha": {func() int { return queuedValue }}}
		Expect(
			registry.Register(handler.NewProviderInFlightCollector(dispatched, queued)),
		).To(Succeed())

		expectGauge(gather(), "ccrouter_provider_dispatched", "alpha", 0)

		dispatchedValue = 7
		queuedValue = 9
		families := gather()
		expectGauge(families, "ccrouter_provider_dispatched", "alpha", 7)
		expectGauge(families, "ccrouter_provider_queued", "alpha", 9)
	})

	It("emits exactly the two documented metric families", func() {
		dispatched := map[string][]func() int{"alpha": {func() int { return 0 }}}
		queued := map[string][]func() int{"alpha": {func() int { return 0 }}}
		Expect(
			registry.Register(handler.NewProviderInFlightCollector(dispatched, queued)),
		).To(Succeed())

		names := make([]string, 0, 2)
		for _, family := range gather() {
			names = append(names, family.GetName())
		}
		Expect(names).To(ConsistOf(
			"ccrouter_provider_dispatched",
			"ccrouter_provider_queued",
		))
	})
})

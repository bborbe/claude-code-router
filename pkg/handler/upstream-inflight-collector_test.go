// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"net/http"
	"net/http/httptest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

var _ = Describe("UpstreamInFlightCollector", func() {
	It("reports a capped host's occupancy 0 -> 1 -> 0", func() {
		hl := handler.NewHostLimiter(2, time.Second)
		c := handler.NewUpstreamInFlightCollector(map[string]*handler.HostLimiter{
			"vllm.seibert.tools": hl,
		})

		Expect(testutil.ToFloat64(c)).To(Equal(0.0))

		inner := newBlockingHandler()
		done := serveAsync(hl.Wrap(inner), httptest.NewRecorder(), newMessagesRequest())
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(1))
		Eventually(func() float64 { return testutil.ToFloat64(c) }, "1s", "10ms").Should(Equal(1.0))

		close(inner.release)
		Eventually(done, "1s").Should(BeClosed())
		Eventually(func() float64 { return testutil.ToFloat64(c) }, "1s", "10ms").Should(Equal(0.0))
	})

	It("reports an uncapped host's live in-flight count 0 -> 1 -> 0", func() {
		hl := handler.NewHostLimiter(0, time.Second)
		c := handler.NewUpstreamInFlightCollector(map[string]*handler.HostLimiter{
			"vllm.seibert.tools": hl,
		})

		Expect(testutil.ToFloat64(c)).To(Equal(0.0))

		inner := newBlockingHandler()
		done := serveAsync(hl.Wrap(inner), httptest.NewRecorder(), newMessagesRequest())
		Eventually(inner.entryCount, "1s", "10ms").Should(Equal(1))
		Eventually(func() float64 { return testutil.ToFloat64(c) }, "1s", "10ms").Should(Equal(1.0))

		close(inner.release)
		Eventually(done, "1s").Should(BeClosed())
		Eventually(func() float64 { return testutil.ToFloat64(c) }, "1s", "10ms").Should(Equal(0.0))
	})

	It("emits one gauge family with exactly the host label per host", func() {
		c := handler.NewUpstreamInFlightCollector(map[string]*handler.HostLimiter{
			"a.example":      handler.NewHostLimiter(2, time.Second),
			"127.0.0.1:8317": handler.NewHostLimiter(0, time.Second),
		})

		reg := prometheus.NewPedanticRegistry()
		Expect(reg.Register(c)).To(Succeed())

		families, err := reg.Gather()
		Expect(err).NotTo(HaveOccurred())

		var family *dto.MetricFamily
		for _, f := range families {
			if f.GetName() == "ccrouter_upstream_inflight" {
				family = f
			}
		}
		Expect(family).NotTo(BeNil())
		Expect(family.GetType()).To(Equal(dto.MetricType_GAUGE))
		Expect(family.GetMetric()).To(HaveLen(2))

		hosts := make([]string, 0, 2)
		for _, metric := range family.GetMetric() {
			Expect(metric.GetLabel()).To(HaveLen(1))
			Expect(metric.GetLabel()[0].GetName()).To(Equal("host"))
			hosts = append(hosts, metric.GetLabel()[0].GetValue())
		}
		Expect(hosts).To(ConsistOf("a.example", "127.0.0.1:8317"))
	})

	It("never reports above the cap while a request is queued", func() {
		hl := handler.NewHostLimiter(1, 50*time.Millisecond)
		c := handler.NewUpstreamInFlightCollector(map[string]*handler.HostLimiter{
			"capped.example": hl,
		})

		innerA := newBlockingHandler()
		doneA := serveAsync(hl.Wrap(innerA), httptest.NewRecorder(), newMessagesRequest())
		Eventually(innerA.entryCount, "1s", "10ms").Should(Equal(1))
		Eventually(func() float64 { return testutil.ToFloat64(c) }, "1s", "10ms").Should(Equal(1.0))

		innerB := newBlockingHandler()
		recB := httptest.NewRecorder()
		doneB := serveAsync(hl.Wrap(innerB), recB, newMessagesRequest())
		Eventually(doneB, "1s").Should(BeClosed())
		Expect(recB.Code).To(Equal(http.StatusTooManyRequests))
		Expect(innerB.entryCount()).To(Equal(0))

		Consistently(func() float64 { return testutil.ToFloat64(c) }, "300ms", "30ms").
			Should(Equal(1.0), "the queued request must not be counted in flight")

		close(innerA.release)
		Eventually(doneA, "1s").Should(BeClosed())
		Eventually(func() float64 { return testutil.ToFloat64(c) }, "1s", "10ms").Should(Equal(0.0))
	})

	DescribeTable("returns to 0 after a failed request",
		func(maxConcurrent int) {
			hl := handler.NewHostLimiter(maxConcurrent, time.Second)
			c := handler.NewUpstreamInFlightCollector(map[string]*handler.HostLimiter{
				"failing.example": hl,
			})

			func() {
				defer func() { _ = recover() }()
				hl.Wrap(panickyHandler{}).ServeHTTP(
					httptest.NewRecorder(),
					newMessagesRequest(),
				)
			}()

			Expect(testutil.ToFloat64(c)).To(Equal(0.0))
		},
		Entry("capped", 2),
		Entry("uncapped", 0),
	)
})

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// gatherGauge returns the value of the named gauge for the given provider
// from reg, and whether a series for that provider exists at all.
func gatherGauge(reg *prometheus.Registry, name, provider string) (float64, bool) {
	mfs, err := reg.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, metric := range mf.GetMetric() {
			for _, lp := range metric.GetLabel() {
				if lp.GetName() == "provider" && lp.GetValue() == provider {
					return metric.GetGauge().GetValue(), true
				}
			}
		}
	}
	return 0, false
}

// gaugeValue asserts the series exists and returns its value.
func gaugeValue(reg *prometheus.Registry, name, provider string) float64 {
	v, ok := gatherGauge(reg, name, provider)
	Expect(ok).To(BeTrue(), "expected series %s{provider=%q} to exist", name, provider)
	return v
}

const (
	inflightCurrent = "ccrouter_inflight_requests"
	inflightPeak    = "ccrouter_inflight_requests_peak"
)

var _ = Describe("InFlight", func() {
	var (
		clock libtime.CurrentDateTime
		inf   *handler.InFlight
		reg   *prometheus.Registry
	)

	advance := func(d stdtime.Duration) {
		clock.SetNow(libtime.DateTime(clock.Now().Time().Add(d)))
	}

	BeforeEach(func() {
		clock = libtime.NewCurrentDateTime()
		clock.SetNow(libtime.DateTime(stdtime.Date(2026, 10, 10, 12, 0, 0, 0, stdtime.UTC)))
		inf = handler.NewInFlight(clock)
		reg = prometheus.NewRegistry()
		Expect(reg.Register(inf)).To(Succeed())
	})

	It("omits a provider from the output until its first Start", func() {
		_, ok := gatherGauge(reg, inflightCurrent, "p")
		Expect(ok).To(BeFalse())
		_, ok = gatherGauge(reg, inflightPeak, "p")
		Expect(ok).To(BeFalse())
	})

	It("increments current on Start and decrements it on end", func() {
		end := inf.Start("p")
		Expect(gaugeValue(reg, inflightCurrent, "p")).To(Equal(1.0))
		end()
		Expect(gaugeValue(reg, inflightCurrent, "p")).To(Equal(0.0))
	})

	It(
		"reports current 2 and peak 2 for two overlapping requests, peak persists after they end",
		func() {
			end1 := inf.Start("p")
			end2 := inf.Start("p")
			Expect(gaugeValue(reg, inflightCurrent, "p")).To(Equal(2.0))
			Expect(gaugeValue(reg, inflightPeak, "p")).To(Equal(2.0))
			end1()
			end2()
			Expect(gaugeValue(reg, inflightCurrent, "p")).To(Equal(0.0))
			Expect(gaugeValue(reg, inflightPeak, "p")).To(Equal(2.0))
		},
	)

	It("drops the peak back to 0 once the 60 s window has passed", func() {
		end1 := inf.Start("p")
		end2 := inf.Start("p")
		end1()
		end2()
		Expect(gaugeValue(reg, inflightPeak, "p")).To(Equal(2.0))
		advance(61 * stdtime.Second)
		Expect(gaugeValue(reg, inflightPeak, "p")).To(Equal(0.0))
	})

	It("decrements exactly once when the end function is called twice", func() {
		end := inf.Start("p")
		end()
		end()
		Expect(gaugeValue(reg, inflightCurrent, "p")).To(Equal(0.0))
	})

	It("isolates the count per provider", func() {
		endA := inf.Start("a")
		endB := inf.Start("b")
		Expect(gaugeValue(reg, inflightCurrent, "a")).To(Equal(1.0))
		Expect(gaugeValue(reg, inflightCurrent, "b")).To(Equal(1.0))
		endA()
		Expect(gaugeValue(reg, inflightCurrent, "a")).To(Equal(0.0))
		Expect(gaugeValue(reg, inflightCurrent, "b")).To(Equal(1.0))
		endB()
		Expect(gaugeValue(reg, inflightCurrent, "b")).To(Equal(0.0))
	})

	It("keeps peak at least the current count for a request open past the window", func() {
		_ = inf.Start("p")
		advance(90 * stdtime.Second)
		Expect(gaugeValue(reg, inflightCurrent, "p")).To(Equal(1.0))
		Expect(gaugeValue(reg, inflightPeak, "p")).To(Equal(1.0))
	})

	It("returns current to 0 after many concurrent Start/end pairs", func() {
		var wg sync.WaitGroup
		for g := 0; g < 50; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer GinkgoRecover()
				for k := 0; k < 20; k++ {
					end := inf.Start("p")
					end()
				}
			}()
		}
		wg.Wait()
		Expect(gaugeValue(reg, inflightCurrent, "p")).To(Equal(0.0))
	})

	It("never reports a negative current count even if end outruns Start", func() {
		end := inf.Start("p")
		end()
		end()
		end()
		Expect(gaugeValue(reg, inflightCurrent, "p")).To(Equal(0.0))
	})
})

var _ = Describe("InFlight through the model router", func() {
	postJSON := func(body string) *http.Request {
		return httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	}

	It("reports 1 while a dispatched request is blocked and 0 after it returns", func() {
		entered := make(chan struct{})
		release := make(chan struct{})
		blocking := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			close(entered)
			<-release
			w.WriteHeader(http.StatusOK)
		})
		m := handler.NewMetrics(nil, libtime.NewCurrentDateTime())
		reg := prometheus.NewPedanticRegistry()
		Expect(m.Register(reg)).To(Succeed())
		mux := handler.NewModelRouter(
			[]handler.ModelRoute{
				{Pattern: "*", ProviderName: "blocker", Handler: blocking},
			},
			"default-fallback",
			blocking,
			nil,
			alwaysSample,
			m,
			libtime.NewCurrentDateTime(),
		)

		done := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			defer close(done)
			mux.ServeHTTP(httptest.NewRecorder(), postJSON(`{"model":"x"}`))
		}()
		<-entered
		Expect(gaugeValue(reg, inflightCurrent, "blocker")).To(Equal(1.0))
		Expect(gaugeValue(reg, inflightPeak, "blocker")).To(Equal(1.0))
		close(release)
		<-done
		Expect(gaugeValue(reg, inflightCurrent, "blocker")).To(Equal(0.0))
	})

	It("returns the count to 0 when the target panics", func() {
		panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("boom")
		})
		m := handler.NewMetrics(nil, libtime.NewCurrentDateTime())
		reg := prometheus.NewPedanticRegistry()
		Expect(m.Register(reg)).To(Succeed())
		mux := handler.NewModelRouter(
			[]handler.ModelRoute{
				{Pattern: "*", ProviderName: "boom", Handler: panicking},
			},
			"default-fallback",
			panicking,
			nil,
			alwaysSample,
			m,
			libtime.NewCurrentDateTime(),
		)

		Expect(func() {
			mux.ServeHTTP(httptest.NewRecorder(), postJSON(`{"model":"x"}`))
		}).To(Panic())
		Expect(gaugeValue(reg, inflightCurrent, "boom")).To(Equal(0.0))
	})

	It("does not touch the gauge on a router-side early return (body too large)", func() {
		m := handler.NewMetrics(nil, libtime.NewCurrentDateTime())
		reg := prometheus.NewPedanticRegistry()
		Expect(m.Register(reg)).To(Succeed())
		mux := handler.NewModelRouter(
			[]handler.ModelRoute{
				{Pattern: "*", ProviderName: "any", Handler: labelHandler("any")},
			},
			"default-fallback",
			labelHandler("fallback"),
			nil,
			alwaysSample,
			m,
			libtime.NewCurrentDateTime(),
		)
		// Body over the 32 MB cap fails the read before any dispatch.
		oversized := `{"model":"x","pad":"` + strings.Repeat("x", 32<<20) + `"}`
		mux.ServeHTTP(httptest.NewRecorder(), postJSON(oversized))
		_, ok := gatherGauge(reg, inflightCurrent, "any")
		Expect(ok).To(BeFalse())
		_, ok = gatherGauge(reg, inflightCurrent, "_unknown_")
		Expect(ok).To(BeFalse())
	})
})

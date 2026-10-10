// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Cold-start admission gate wiring specs (spec 019): CreateRouterFromConfig
// wraps each provider's pool handler in the cold gate inside the throttle
// gate, so both glob-routed and default-provider traffic pass through it on
// the real dispatch path, a saturation burst is refused with HTTP 429
// through the UNCHANGED 4xx_rate_limited class, and a rebuild on a fresh
// CreateRouterFromConfig (exactly the reloader's SIGHUP path) applies the
// changed knobs with fresh in-memory state.

package factory_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/factory"
)

// coldGateBurst exceeds the gate's fixed queue capacity of 32, so exactly one
// arrival in the burst finds the bounded queue full and is refused.
const coldGateBurst = 33

// coldGateStatusClasses is the existing 7-value status_class enum the refusal
// must reuse; the four additive cold series never introduce a new value.
var coldGateStatusClasses = []string{
	"2xx",
	"3xx",
	"4xx_auth",
	"4xx_rate_limited",
	"4xx_bad_request",
	"5xx_upstream",
	"5xx_router",
}

// newColdRequest builds a /v1/messages request carrying the session id in the
// X-Session-Id header — the session middleware strips it and carries it on the
// request context, which is where the cold gate reads it.
func newColdRequest(id, model string) *http.Request {
	req := newMessagesRequest(model)
	req.Header.Set("X-Session-Id", id)
	return req
}

// coldWiringClock returns a clock pinned to a fixed instant, so the gate's
// session-window arithmetic never drifts across a row.
func coldWiringClock() libtime.CurrentDateTime {
	clock := libtime.NewCurrentDateTime()
	clock.SetNow(libtime.DateTime(stdtime.Date(2026, 8, 27, 12, 0, 0, 0, stdtime.UTC)))
	return clock
}

// coldBurst is a set of concurrent cold requests sharing one cancellable
// context, with their recorders and completion channels.
type coldBurst struct {
	recs   []*httptest.ResponseRecorder
	dones  []chan struct{}
	cancel context.CancelFunc
}

// fireColdBurst fires n concurrent cold requests through h with distinct
// session ids. The caller cancels via cleanup to free the waiters.
func fireColdBurst(h http.Handler, n int) *coldBurst {
	ctx, cancel := context.WithCancel(context.Background())
	b := &coldBurst{
		recs:   make([]*httptest.ResponseRecorder, n),
		dones:  make([]chan struct{}, n),
		cancel: cancel,
	}
	for i := range b.recs {
		b.recs[i] = httptest.NewRecorder()
		b.dones[i] = make(chan struct{})
		req := newColdRequest(fmt.Sprintf("cold-%d", i), "m1").WithContext(ctx)
		go func(i int, req *http.Request) {
			defer close(b.dones[i])
			h.ServeHTTP(b.recs[i], req)
		}(i, req)
	}
	return b
}

// closedCount returns how many of the burst's requests have been answered.
func (b *coldBurst) closedCount() int {
	n := 0
	for _, d := range b.dones {
		select {
		case <-d:
			n++
		default:
		}
	}
	return n
}

// shedRecorder returns the recorder of the single answered request — the one
// the gate refused because the queue was full. Call it while exactly one
// request has been answered.
func (b *coldBurst) shedRecorder() *httptest.ResponseRecorder {
	for i, d := range b.dones {
		select {
		case <-d:
			return b.recs[i]
		default:
		}
	}
	return nil
}

// requestsFamily returns the ccrouter_requests_total family, or nil when it is
// absent from the registry.
func requestsFamily(reg prometheus.Gatherer) *dto.MetricFamily {
	families, err := reg.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, f := range families {
		if f.GetName() == "ccrouter_requests_total" {
			return f
		}
	}
	return nil
}

// statusClassCounts sums the requests counter per status_class label value.
func statusClassCounts(reg prometheus.Gatherer) map[string]float64 {
	out := map[string]float64{}
	fam := requestsFamily(reg)
	if fam == nil {
		return out
	}
	for _, m := range fam.GetMetric() {
		for _, lp := range m.GetLabel() {
			if lp.GetName() == "status_class" {
				out[lp.GetValue()] += m.GetCounter().GetValue()
			}
		}
	}
	return out
}

// statusClassCount returns the summed requests counter for one status_class.
func statusClassCount(reg prometheus.Gatherer, class string) float64 {
	return statusClassCounts(reg)[class]
}

var _ = Describe("CreateRouterFromConfig cold-start gate wiring", func() {
	var (
		srv     *httptest.Server
		calls   int32
		release chan struct{}
	)

	// coldConfig builds a single-provider config whose cold gate is enabled at
	// the given prefill budget; the rate stays disabled so the budget is the
	// only admission constraint.
	coldConfig := func(budget int) *pkg.Config {
		return &pkg.Config{
			Router: pkg.Router{DefaultProvider: "t"},
			Providers: map[string]pkg.Provider{
				"t": {
					Upstream:                srv.URL,
					Models:                  []string{"m*"},
					ColdPrefillBudgetTokens: budget,
				},
			},
		}
	}

	// buildRouter builds the handler tree exactly as the reloader does on
	// SIGHUP: a fresh registry per build so metrics.Register never collides.
	buildRouter := func(budget int) (http.Handler, *prometheus.Registry) {
		reg := prometheus.NewRegistry()
		h, err := factory.CreateRouterFromConfig(
			context.Background(),
			coldConfig(budget),
			factory.WithMetricsRegisterer(reg),
			factory.WithCurrentDateTime(coldWiringClock()),
		)
		Expect(err).NotTo(HaveOccurred())
		return h, reg
	}

	BeforeEach(func() {
		calls = 0
		release = make(chan struct{})
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"type":"message","content":[]}`))
		}))
	})

	AfterEach(func() {
		srv.Close()
	})

	It("admits on the real path and applies the changed knobs on rebuild (SIGHUP path)", func() {
		handler1, _ := buildRouter(1)

		// A lone cold request is admitted even though its estimate exceeds the
		// budget, and holds its reservation while the upstream blocks.
		holdDone := serveAsync(handler1, httptest.NewRecorder(), newColdRequest("hold", "m1"))
		Eventually(func() int32 { return atomic.LoadInt32(&calls) }, "1s", "10ms").
			Should(BeNumerically("==", 1), "the lone cold request is admitted")

		// The queue holds 32; the 33rd arrival is refused immediately with 429.
		burst := fireColdBurst(handler1, coldGateBurst)
		Eventually(burst.closedCount, "2s", "10ms").
			Should(Equal(1), "exactly one arrival finds the bounded queue full")
		Expect(burst.shedRecorder().Code).To(Equal(http.StatusTooManyRequests))
		Consistently(burst.closedCount, "200ms", "20ms").
			Should(Equal(1), "the other 32 cold requests wait")

		burst.cancel()
		Eventually(burst.closedCount, "2s", "10ms").Should(Equal(coldGateBurst))
		close(release)
		Eventually(holdDone, "2s").Should(BeClosed())

		// Rebuild — exactly the reloader's SIGHUP path — with the changed knob;
		// the rebuilt tree admits a cold request the old budget would have held.
		handler2, _ := buildRouter(1000000)
		reloadRec := httptest.NewRecorder()
		reloadDone := serveAsync(handler2, reloadRec, newColdRequest("after-reload", "m2"))
		Eventually(reloadDone, "2s").Should(BeClosed())
		Expect(reloadRec.Code).To(Equal(http.StatusOK))
		Expect(atomic.LoadInt32(&calls)).To(BeNumerically(">=", 2))
	})

	It("records a cold refusal through the unchanged 4xx_rate_limited class", func() {
		handler, reg := buildRouter(1)

		holdDone := serveAsync(handler, httptest.NewRecorder(), newColdRequest("hold", "m1"))
		Eventually(func() int32 { return atomic.LoadInt32(&calls) }, "1s", "10ms").
			Should(BeNumerically("==", 1))

		burst := fireColdBurst(handler, coldGateBurst)
		Eventually(burst.closedCount, "2s", "10ms").Should(Equal(1))
		Expect(burst.shedRecorder().Code).To(Equal(http.StatusTooManyRequests))

		burst.cancel()
		Eventually(burst.closedCount, "2s", "10ms").Should(Equal(coldGateBurst))
		close(release)
		Eventually(holdDone, "2s").Should(BeClosed())

		// The refusal lands in the existing 4xx_rate_limited class with no new
		// status_class value.
		Eventually(
			func() float64 { return statusClassCount(reg, "4xx_rate_limited") },
			"2s",
			"10ms",
		).Should(BeNumerically(">=", 1))
		for class := range statusClassCounts(reg) {
			Expect(coldGateStatusClasses).To(ContainElement(class), "no new status_class value")
		}
	})
})

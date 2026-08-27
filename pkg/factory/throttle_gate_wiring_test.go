// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Adaptive 429 delay gate wiring specs (spec 018): CreateRouterFromConfig
// wraps each provider's pool handler in the gate (so glob-routed and
// default-provider traffic both pass through it), resolves an absent,
// zero, or negative throttleMaxDelaySeconds to the 30s default, and a
// rebuild on a fresh CreateRouterFromConfig (exactly the reloader's SIGHUP
// path) resets the per-provider in-memory throttle state while enforcing
// the new threshold. Rows use an explicit fresh registry and a fixed
// injected clock so windowed counting never expires observations.

package factory_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/factory"
)

var _ = Describe("CreateRouterFromConfig throttle gate wiring", func() {
	var (
		srv   *httptest.Server
		calls int32
	)

	// newMessagesRequest builds a /v1/messages request with the given model.
	// The model is the only variable part; everything else matches the body
	// shape the model router dispatches on.
	newMessagesRequest := func(model string) *http.Request {
		body := fmt.Sprintf(
			`{"model":%q,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			model,
		)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		return req
	}

	// serveAsync runs ServeHTTP in a goroutine and returns a channel that
	// closes when it returns, so the test can read the recorder without
	// racing the goroutine's writes.
	serveAsync := func(h http.Handler, rec *httptest.ResponseRecorder, req *http.Request) chan struct{} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			h.ServeHTTP(rec, req)
		}()
		return done
	}

	// makeConfig builds a single-provider config whose upstream always
	// answers 429, with the gate enabled at the given threshold and max
	// delay (seconds).
	makeConfig := func(threshold, maxDelaySeconds int) *pkg.Config {
		return &pkg.Config{
			Router: pkg.Router{DefaultProvider: "t"},
			Providers: map[string]pkg.Provider{
				"t": {
					Upstream:                srv.URL,
					Models:                  []string{"m*"},
					Throttle429Threshold:    threshold,
					ThrottleMaxDelaySeconds: maxDelaySeconds,
				},
			},
		}
	}

	// fixedClock returns a clock pinned to a fixed instant, so the gate's
	// windowed 429 counting never expires observations across a row.
	fixedClock := func() libtime.CurrentDateTime {
		clock := libtime.NewCurrentDateTime()
		clock.SetNow(libtime.DateTime(stdtime.Date(2026, 8, 27, 12, 0, 0, 0, stdtime.UTC)))
		return clock
	}

	BeforeEach(func() {
		calls = 0
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write(
				[]byte(
					`{"type":"error","error":{"type":"rate_limit_error","message":"upstream says no"}}`,
				),
			)
		}))
	})

	AfterEach(func() {
		srv.Close()
	})

	It(
		"paces through the real dispatch path and re-enforces the new threshold on rebuild (SIGHUP path)",
		func() {
			reg1 := prometheus.NewRegistry()
			cfg1 := makeConfig(1, 1)
			handler1, err := factory.CreateRouterFromConfig(
				context.Background(),
				cfg1,
				factory.WithMetricsRegisterer(reg1),
				factory.WithCurrentDateTime(fixedClock()),
			)
			Expect(err).NotTo(HaveOccurred())

			// Request 1: forwarded before the first 429 engages the
			// threshold-1 gate.
			rec1 := httptest.NewRecorder()
			done1 := serveAsync(handler1, rec1, newMessagesRequest("m1"))
			Eventually(func() int32 { return atomic.LoadInt32(&calls) }, "1s", "10ms").
				Should(BeNumerically("==", 1), "request 1 must be forwarded once")
			Eventually(done1, "1s").Should(BeClosed())
			Expect(rec1.Code).To(Equal(http.StatusTooManyRequests))

			// Request 2: held for the 1s pacing delay before forwarding.
			rec2 := httptest.NewRecorder()
			done2 := serveAsync(handler1, rec2, newMessagesRequest("m2"))
			Consistently(func() int32 { return atomic.LoadInt32(&calls) }, "300ms", "30ms").
				Should(BeNumerically("==", 1), "request 2 must be held for the pacing delay")
			Eventually(func() int32 { return atomic.LoadInt32(&calls) }, "2s", "10ms").
				Should(BeNumerically("==", 2))
			Eventually(done2, "2s").Should(BeClosed())
			Expect(rec2.Code).To(Equal(http.StatusTooManyRequests))

			// Rebuild with the new threshold — exactly the reloader's
			// rebuild; the in-memory throttle state resets to not-throttled.
			reg2 := prometheus.NewRegistry()
			cfg2 := makeConfig(2, 1)
			handler2, err := factory.CreateRouterFromConfig(
				context.Background(),
				cfg2,
				factory.WithMetricsRegisterer(reg2),
				factory.WithCurrentDateTime(fixedClock()),
			)
			Expect(err).NotTo(HaveOccurred())

			// Request 3: first 429 under the NEW threshold of 2 does not
			// engage throttle, so it returns synchronously — no pacing delay.
			rec3 := httptest.NewRecorder()
			handler2.ServeHTTP(rec3, newMessagesRequest("m3"))
			Expect(rec3.Code).To(Equal(http.StatusTooManyRequests))
			Expect(atomic.LoadInt32(&calls)).To(BeNumerically("==", 3))

			// Request 4: count 2 >= 2 engages the rebuilt gate.
			rec4 := httptest.NewRecorder()
			handler2.ServeHTTP(rec4, newMessagesRequest("m4"))
			Expect(rec4.Code).To(Equal(http.StatusTooManyRequests))
			Expect(atomic.LoadInt32(&calls)).To(BeNumerically("==", 4))

			// Request 5: paced by the rebuilt gate's 1s delay.
			rec5 := httptest.NewRecorder()
			done5 := serveAsync(handler2, rec5, newMessagesRequest("m5"))
			Consistently(func() int32 { return atomic.LoadInt32(&calls) }, "300ms", "30ms").
				Should(BeNumerically("==", 4), "request 5 must be held for the pacing delay")
			Eventually(func() int32 { return atomic.LoadInt32(&calls) }, "2s", "10ms").
				Should(BeNumerically("==", 5))
			Eventually(done5, "2s").Should(BeClosed())
			Expect(rec5.Code).To(Equal(http.StatusTooManyRequests))
		},
	)

	It(
		"records a non-paced 429 through the unchanged 4xx_rate_limited path and no throttled series",
		func() {
			reg := prometheus.NewRegistry()
			cfg := makeConfig(1, 1)
			router, err := factory.CreateRouterFromConfig(
				context.Background(),
				cfg,
				factory.WithMetricsRegisterer(reg),
				factory.WithCurrentDateTime(fixedClock()),
			)
			Expect(err).NotTo(HaveOccurred())

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, newMessagesRequest("m1"))
			Expect(rec.Code).To(Equal(http.StatusTooManyRequests))

			// The additive counter gains no series from a non-paced 429.
			Expect(testutil.GatherAndCount(reg, "ccrouter_throttled_total")).To(Equal(0))

			// The unchanged status_class classification still records the 429:
			// no new status-class value, and the series counter is >= 1.
			families, err := reg.Gather()
			Expect(err).NotTo(HaveOccurred())
			var requests *dto.MetricFamily
			for _, f := range families {
				if f.GetName() == "ccrouter_requests_total" {
					requests = f
				}
			}
			Expect(requests).NotTo(BeNil(), "ccrouter_requests_total must be present")
			rateLimited := false
			for _, m := range requests.GetMetric() {
				isRateLimited := false
				for _, lp := range m.GetLabel() {
					if lp.GetName() == "status_class" && lp.GetValue() == "4xx_rate_limited" {
						isRateLimited = true
					}
				}
				if isRateLimited && m.GetCounter().GetValue() >= 1 {
					rateLimited = true
				}
			}
			Expect(
				rateLimited,
			).To(BeTrue(), "a 429 must record through the unchanged 4xx_rate_limited path")
		},
	)
})

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Live-pool wiring specs: the operator's three-member weekday/night/weekend
// pool (seibert-vllm-default as of 2026-10-12 — day 16, night 25, weekend 25,
// host 25), driven end-to-end through CreateRouterFromConfig with the clock
// fixed by SetNow. Two concerns:
//
//   - window boundaries: which member serves at 07:59, 08:00, 17:59, 18:00
//     and on a Saturday, including one session pinned across the switches;
//   - saturation: which limiter refuses the request one past the binding
//     cap, read from ccrouter_rejected_total{reason} — the member cap
//     (queue_timeout) and the shared host budget (host_queue_timeout) must
//     stay distinguishable, because they call for different config changes.

package factory_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/factory"
)

// livePoolProvider builds the three-member pool as the live config declares
// it: member 0 is the weekday day window, member 1 the weekday night window,
// member 2 the weekend. urls are indexed by member; caps by member too.
func livePoolProvider(urls [3]string, caps [3]int) pkg.Provider {
	weekdays := "monday, tuesday, wednesday, thursday, friday"
	return pkg.Provider{
		Models: []string{"deepseek-*"},
		Upstreams: []pkg.Upstream{
			{
				Upstream:                 urls[0],
				Weight:                   1,
				MaxConcurrentRequests:    caps[0],
				MaxConcurrentWaitSeconds: 1,
				Days:                     mustDays(weekdays),
				Window: &pkg.Window{
					From:  mustTOD("08:00 Europe/Berlin"),
					Until: mustTOD("18:00 Europe/Berlin"),
				},
			},
			{
				Upstream:                 urls[1],
				Weight:                   1,
				MaxConcurrentRequests:    caps[1],
				MaxConcurrentWaitSeconds: 1,
				Days:                     mustDays(weekdays),
				Window: &pkg.Window{
					From:  mustTOD("18:00 Europe/Berlin"),
					Until: mustTOD("08:00 Europe/Berlin"),
				},
			},
			{
				Upstream:                 urls[2],
				Weight:                   1,
				MaxConcurrentRequests:    caps[2],
				MaxConcurrentWaitSeconds: 1,
				Days:                     mustDays("saturday, sunday Europe/Berlin"),
			},
		},
	}
}

// rejectedCount reads one ccrouter_rejected_total series from reg, or 0
// when the series is absent.
func rejectedCount(reg *prometheus.Registry, provider, reason string) float64 {
	families, err := reg.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, family := range families {
		if family.GetName() != "ccrouter_rejected_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			var gotProvider, gotReason string
			for _, label := range metric.GetLabel() {
				switch label.GetName() {
				case "provider":
					gotProvider = label.GetValue()
				case "reason":
					gotReason = label.GetValue()
				}
			}
			if gotProvider == provider && gotReason == reason {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// berlinAt returns a fixed clock reading the given Berlin wall time.
func berlinAt(year int, month stdtime.Month, day, hour, minute int) libtime.CurrentDateTimeGetter {
	clock := libtime.NewCurrentDateTime()
	clock.SetNow(libtime.DateTime(stdtime.Date(year, month, day, hour, minute, 0, 0, berlin)))
	return clock
}

var _ = Describe("CreateRouterFromConfig live three-member pool", func() {
	const provider = "seibert-vllm-default"

	Context("window boundaries (each member on its own upstream)", func() {
		var members [3]*poolUpstream

		BeforeEach(func() {
			for i := range members {
				members[i] = newPoolUpstream()
				members[i].unblock() // answer immediately; only routing is under test
			}
		})

		AfterEach(func() {
			for _, m := range members {
				m.closeUpstream()
			}
		})

		buildAt := func(clock libtime.CurrentDateTimeGetter) http.Handler {
			cfg := &pkg.Config{
				Router: pkg.Router{DefaultProvider: provider},
				Providers: map[string]pkg.Provider{
					provider: livePoolProvider(
						[3]string{members[0].url, members[1].url, members[2].url},
						[3]int{16, 25, 25},
					),
				},
			}
			h, err := factory.CreateRouterFromConfig(
				context.Background(),
				cfg,
				isolatedRegistry(),
				factory.WithCurrentDateTime(clock),
			)
			Expect(err).NotTo(HaveOccurred())
			return h
		}

		// servedBy dispatches one request and returns the index of the member
		// whose upstream received it.
		servedBy := func(h http.Handler, req *http.Request) int {
			before := [3]int32{members[0].count(), members[1].count(), members[2].count()}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusOK))
			for i, m := range members {
				if m.count() == before[i]+1 {
					return i
				}
			}
			Fail("no member received the request")
			return -1
		}

		DescribeTable(
			"serves from exactly the member whose window holds the instant",
			func(clock libtime.CurrentDateTimeGetter, want int) {
				h := buildAt(clock)
				Expect(
					servedBy(h, newMessagesRequest("deepseek-v4-flash")),
				).To(Equal(want), "keyless")
				Expect(
					servedBy(h, sessionedRequest("s-1", "deepseek-v4-flash")),
				).To(Equal(want), "pinned")
			},
			Entry("Mon 07:59 → night", berlinAt(2026, stdtime.October, 12, 7, 59), 1),
			Entry("Mon 08:00 → day", berlinAt(2026, stdtime.October, 12, 8, 0), 0),
			Entry("Mon 17:59 → day", berlinAt(2026, stdtime.October, 12, 17, 59), 0),
			Entry("Mon 18:00 → night", berlinAt(2026, stdtime.October, 12, 18, 0), 1),
			Entry("Sat 12:00 → weekend", berlinAt(2026, stdtime.October, 17, 12, 0), 2),
			Entry("Sun 23:59 → weekend", berlinAt(2026, stdtime.October, 18, 23, 59), 2),
			Entry(
				"Mon 00:00 after a weekend → night",
				berlinAt(2026, stdtime.October, 19, 0, 0),
				1,
			),
		)

		It("re-resolves one pinned session across the 08:00 and 18:00 switches", func() {
			clock := libtime.NewCurrentDateTime()
			h := buildAt(clock)
			steps := []struct {
				hour, minute int
				want         int
			}{
				{7, 59, 1},
				{8, 0, 0},
				{17, 59, 0},
				{18, 0, 1},
			}
			for _, s := range steps {
				clock.SetNow(
					libtime.DateTime(
						stdtime.Date(2026, stdtime.October, 12, s.hour, s.minute, 0, 0, berlin),
					),
				)
				Expect(
					servedBy(h, sessionedRequest("pinned-across-boundaries", "deepseek-v4-flash")),
				).
					To(Equal(s.want), "at %02d:%02d", s.hour, s.minute)
			}
		})
	})

	Context("saturation (all members on one shared host)", func() {
		DescribeTable(
			"refuses the request past the binding cap with that limiter's reason",
			func(
				clock libtime.CurrentDateTimeGetter,
				caps [3]int,
				hostCap int,
				admitted int,
				wantReason string,
				otherReason string,
			) {
				srv := newPoolUpstream()
				defer srv.closeUpstream()
				u, err := url.Parse(srv.url)
				Expect(err).NotTo(HaveOccurred())

				cfg := &pkg.Config{
					Router: pkg.Router{DefaultProvider: provider},
					Providers: map[string]pkg.Provider{
						provider: livePoolProvider([3]string{srv.url, srv.url, srv.url}, caps),
					},
					UpstreamHostLimits: map[string]pkg.HostLimit{
						pkg.UpstreamHostKey(u): {
							MaxConcurrentRequests:    hostCap,
							MaxConcurrentWaitSeconds: 1,
						},
					},
				}
				reg := prometheus.NewRegistry()
				h, err := factory.CreateRouterFromConfig(
					context.Background(),
					cfg,
					factory.WithMetricsRegisterer(reg),
					factory.WithCurrentDateTime(clock),
				)
				Expect(err).NotTo(HaveOccurred())

				inFlight := func() int32 { return atomic.LoadInt32(&srv.inFlight) }

				dones := make([]chan struct{}, 0, admitted)
				for i := 0; i < admitted; i++ {
					dones = append(
						dones,
						serveAsync(
							h,
							httptest.NewRecorder(),
							newMessagesRequest("deepseek-v4-flash"),
						),
					)
				}
				Eventually(inFlight, "2s", "10ms").Should(BeNumerically("==", admitted))

				over := httptest.NewRecorder()
				h.ServeHTTP(over, newMessagesRequest("deepseek-v4-flash"))

				Expect(over.Code).To(Equal(http.StatusTooManyRequests))
				Expect(
					inFlight(),
				).To(BeNumerically("==", admitted), "the refused request never reached the upstream")
				Expect(rejectedCount(reg, provider, wantReason)).To(Equal(float64(1)))
				Expect(rejectedCount(reg, provider, otherReason)).To(Equal(float64(0)))
				Expect(rejectedCount(reg, provider, "upstream")).To(Equal(float64(0)))

				srv.unblock()
				for _, done := range dones {
					Eventually(done, "2s").Should(BeClosed())
				}
			},
			Entry("live, weekday daytime: the day member's 16 binds while the host's 25 has room",
				berlinAt(2026, stdtime.October, 12, 10, 0), [3]int{16, 25, 25}, 25, 16,
				"queue_timeout", "host_queue_timeout"),
			Entry("host budget below the member cap: the host binds first",
				berlinAt(2026, stdtime.October, 12, 10, 0), [3]int{16, 25, 25}, 10, 10,
				"host_queue_timeout", "queue_timeout"),
			Entry(
				"live, weekday night: night member and host both 25 — the outer host limiter refuses",
				berlinAt(2026, stdtime.October, 12, 20, 0),
				[3]int{16, 25, 25},
				25,
				25,
				"host_queue_timeout",
				"queue_timeout",
			),
		)
	})
})

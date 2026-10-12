// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Live-pool wiring specs: the operator's three-member weekday/night/weekend
// pool (seibert-vllm-default as of 2026-10-12 — day 16, night 25, weekend 25,
// host 25), driven end-to-end through CreateRouterFromConfig with the clock
// fixed by SetNow. Three concerns:
//
//   - window boundaries: which member serves at 07:59, 08:00, 17:59, 18:00
//     and on a Saturday, including one session pinned across the switches;
//   - saturation: which limiter refuses the request one past the binding
//     cap, read from ccrouter_rejected_total{reason} — the member cap
//     (queue_timeout) and the shared host budget (host_queue_timeout) must
//     stay distinguishable, because they call for different config changes;
//   - the 18:00 switch on one running router: admission rises from the day
//     member's 16 to the host budget's 25 by the clock alone, with no
//     restart or reload — bounded by the host while day requests still hold
//     their slots.

package factory_test

import (
	"context"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	stdtime "time"

	httpmocks "github.com/bborbe/http/mocks"
	libtime "github.com/bborbe/time"
	libtimemocks "github.com/bborbe/time/mocks"
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

	Context("[req] member label for a request no member saw", func() {
		It(
			"logs /- with the gate's reason, never /0, when the breaker refuses on a Sunday",
			func() {
				// Reproduces the 2026-10-11 log: provider-level refusals were
				// logged as member /0 — the weekday-day member — although no member
				// saw them, which read as weekend traffic dispatched to the day
				// member.
				upstream := &httpmocks.HttpHandler{}
				upstream.ServeHTTPStub = func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					w.WriteHeader(http.StatusInternalServerError)
				}
				srv := httptest.NewServer(upstream)
				defer srv.Close()

				prov := livePoolProvider([3]string{srv.URL, srv.URL, srv.URL}, [3]int{16, 25, 25})
				prov.BreakerFailureThreshold = 1
				cfg := &pkg.Config{
					Router:    pkg.Router{DefaultProvider: provider},
					Providers: map[string]pkg.Provider{provider: prov},
				}
				h, err := factory.CreateRouterFromConfig(
					context.Background(),
					cfg,
					isolatedRegistry(),
					factory.WithCurrentDateTime(berlinAt(2026, stdtime.October, 11, 12, 0)),
				)
				Expect(err).NotTo(HaveOccurred())

				oldV := flag.Lookup("v").Value.String()
				oldLogToStderr := flag.Lookup("logtostderr").Value.String()
				defer func() {
					Expect(flag.Set("v", oldV)).To(Succeed())
					Expect(flag.Set("logtostderr", oldLogToStderr)).To(Succeed())
				}()
				Expect(flag.Set("v", "1")).To(Succeed())
				Expect(flag.Set("logtostderr", "true")).To(Succeed())

				var failed, refused *httptest.ResponseRecorder
				out := captureStderr(func() {
					failed = httptest.NewRecorder()
					h.ServeHTTP(failed, newMessagesRequest("deepseek-v4-flash"))
					refused = httptest.NewRecorder()
					h.ServeHTTP(refused, newMessagesRequest("deepseek-v4-flash"))
				})

				Expect(failed.Code).To(Equal(http.StatusInternalServerError))
				Expect(refused.Code).To(Equal(http.StatusTooManyRequests))
				Expect(
					upstream.ServeHTTPCallCount(),
				).To(Equal(1), "the open breaker never reached a member")
				Expect(
					out,
				).To(MatchRegexp(`provider=seibert-vllm-default/2 status=500 `), "the weekend member served the first")
				Expect(
					out,
				).To(MatchRegexp(`provider=seibert-vllm-default/- status=429 .* reason=circuit_open\n`))
				Expect(out).NotTo(ContainSubstring("provider=seibert-vllm-default/0"))
			},
		)
	})

	Context("18:00 switch on one running router (no restart, no reload)", func() {
		It(
			"raises admission from the day cap 16 to the host budget 25 as the clock crosses 18:00",
			func() {
				// One upstream server backed by the Counterfeiter HttpHandler mock.
				// The stub sorts requests into lanes by URL path prefix: each lane
				// counts its own in-flight requests and is released on its own, so
				// day traffic can drain while night traffic stays held.
				lanes := map[string]*upstreamLane{
					"/day":     newUpstreamLane(),
					"/night":   newUpstreamLane(),
					"/weekend": newUpstreamLane(),
				}
				upstream := &httpmocks.HttpHandler{}
				upstream.ServeHTTPStub = func(w http.ResponseWriter, r *http.Request) {
					l := lanes[laneOf(r.URL.Path)]
					if l == nil {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					atomic.AddInt32(&l.inFlight, 1)
					defer atomic.AddInt32(&l.inFlight, -1)
					// Read the body first, as poolUpstream does, so a client
					// disconnect is noticed and cannot leave the lane counter stuck.
					_, _ = io.Copy(io.Discard, r.Body)
					select {
					case <-l.release:
					case <-r.Context().Done():
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"ok":true}`))
				}
				srv := httptest.NewServer(upstream)
				defer func() {
					for _, l := range lanes {
						l.open()
					}
					srv.Close()
				}()
				u, err := url.Parse(srv.URL)
				Expect(err).NotTo(HaveOccurred())

				// The URL path does not enter the host key, so all three members
				// draw from one host budget of 25.
				cfg := &pkg.Config{
					Router: pkg.Router{DefaultProvider: provider},
					Providers: map[string]pkg.Provider{
						provider: livePoolProvider(
							[3]string{srv.URL + "/day", srv.URL + "/night", srv.URL + "/weekend"},
							[3]int{16, 25, 25},
						),
					},
					UpstreamHostLimits: map[string]pkg.HostLimit{
						pkg.UpstreamHostKey(u): {
							MaxConcurrentRequests:    25,
							MaxConcurrentWaitSeconds: 1,
						},
					},
				}
				clock := libtime.NewCurrentDateTime()
				clock.SetNow(
					libtime.DateTime(stdtime.Date(2026, stdtime.October, 12, 17, 59, 0, 0, berlin)),
				)
				// The queue wait is timed by the counterfeiter WaiterDuration mock:
				// a queued request times out only when the spec sends on expire,
				// so each refusal is one deterministic event rather than a real
				// 1s timer.
				expire := make(chan struct{})
				waiter := &libtimemocks.WaiterDuration{}
				waiter.WaitStub = func(ctx context.Context, _ libtime.Duration) error {
					select {
					case <-expire:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				reg := prometheus.NewRegistry()
				h, err := factory.CreateRouterFromConfig(
					context.Background(),
					cfg,
					factory.WithMetricsRegisterer(reg),
					factory.WithCurrentDateTime(clock),
					factory.WithWaiterDuration(waiter),
				)
				Expect(err).NotTo(HaveOccurred())

				admit := func(n int) []chan struct{} {
					dones := make([]chan struct{}, 0, n)
					for i := 0; i < n; i++ {
						dones = append(
							dones,
							serveAsync(
								h,
								httptest.NewRecorder(),
								newMessagesRequest("deepseek-v4-flash"),
							),
						)
					}
					return dones
				}
				// refused sends one request past the binding cap: it queues (one
				// more Wait call), the spec expires exactly that wait, and the
				// request is answered 429. Exactly one request is queued at each
				// call, so the single send on expire reaches that one waiter.
				refused := func() {
					waitsBefore := waiter.WaitCallCount()
					rec := httptest.NewRecorder()
					done := serveAsync(h, rec, newMessagesRequest("deepseek-v4-flash"))
					Eventually(waiter.WaitCallCount).Should(Equal(waitsBefore + 1))
					_, d := waiter.WaitArgsForCall(waitsBefore)
					Expect(
						d,
					).To(Equal(libtime.Duration(stdtime.Second)), "maxConcurrentWaitSeconds: 1")
					expire <- struct{}{}
					Eventually(done, "2s").Should(BeClosed())
					Expect(rec.Code).To(Equal(http.StatusTooManyRequests))
				}

				By("17:59 — the day member's 16 binds while the host has room")
				dayDones := admit(16)
				Eventually(lanes["/day"].count).Should(BeNumerically("==", 16))
				refused()
				Expect(rejectedCount(reg, provider, "queue_timeout")).To(Equal(float64(1)))
				Expect(rejectedCount(reg, provider, "host_queue_timeout")).To(Equal(float64(0)))

				By(
					"18:00 — same router, night member eligible; the 16 day requests still hold host slots",
				)
				clock.SetNow(
					libtime.DateTime(stdtime.Date(2026, stdtime.October, 12, 18, 0, 0, 0, berlin)),
				)
				nightDones := admit(9)
				Eventually(lanes["/night"].count).Should(BeNumerically("==", 9))
				Expect(lanes["/day"].count()).To(BeNumerically("==", 16))
				refused()
				Expect(rejectedCount(reg, provider, "host_queue_timeout")).To(Equal(float64(1)))
				Expect(rejectedCount(reg, provider, "queue_timeout")).To(Equal(float64(1)))

				By("the day requests drain — the night member fills to the full 25")
				lanes["/day"].open()
				for _, done := range dayDones {
					Eventually(done, "2s").Should(BeClosed())
				}
				nightDones = append(nightDones, admit(16)...)
				Eventually(lanes["/night"].count).Should(BeNumerically("==", 25))
				Expect(lanes["/day"].count()).To(BeNumerically("==", 0))
				refused()
				Expect(rejectedCount(reg, provider, "host_queue_timeout")).To(Equal(float64(2)))
				Expect(rejectedCount(reg, provider, "queue_timeout")).To(Equal(float64(1)))
				Expect(rejectedCount(reg, provider, "upstream")).To(Equal(float64(0)))

				lanes["/night"].open()
				for _, done := range nightDones {
					Eventually(done, "2s").Should(BeClosed())
				}

				By(
					"the upstream saw exactly the admitted requests — none of the three refused ones",
				)
				received := map[string]int{}
				for i := 0; i < upstream.ServeHTTPCallCount(); i++ {
					_, r := upstream.ServeHTTPArgsForCall(i)
					received[laneOf(r.URL.Path)]++
				}
				Expect(received).To(Equal(map[string]int{"/day": 16, "/night": 25}))
			},
		)
	})
})

// upstreamLane is the per-member state the HttpHandler mock's stub reads:
// an in-flight counter and a release channel that frees every request held
// in the lane.
type upstreamLane struct {
	inFlight int32
	release  chan struct{}
	once     sync.Once
}

func newUpstreamLane() *upstreamLane {
	return &upstreamLane{release: make(chan struct{})}
}

// count returns the lane's in-flight request count; usable as a poll func.
func (l *upstreamLane) count() int32 {
	return atomic.LoadInt32(&l.inFlight)
}

// open releases every request held in the lane. Idempotent.
func (l *upstreamLane) open() {
	l.once.Do(func() { close(l.release) })
}

// laneOf maps an upstream request path to its member lane: the first path
// segment the member's upstream URL carries ("/day/v1/messages" → "/day").
func laneOf(path string) string {
	for _, p := range []string{"/day", "/night", "/weekend"} {
		if strings.HasPrefix(path, p+"/") || path == p {
			return p
		}
	}
	return ""
}

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Time-window eligibility specs (spec 014): a pool member whose window does
// not contain "now" — from the injected libtime.CurrentDateTimeGetter,
// evaluated in the value's attached IANA location — is excluded from session
// pinning and keyless least-loaded selection, and a provider whose pool has
// no eligible member is skipped by the model router's glob walk and key
// routing in favor of the next eligible provider or default_provider. Every
// row drives a fixed clock (libtime.NewCurrentDateTime + SetNow) through the
// real dispatch path, never the wall clock.

package handler_test

import (
	"context"
	"flag"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"strings"
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	pkg "github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/handler"
)

var berlin = mustLoadLocation("Europe/Berlin")

func mustLoadLocation(name string) *stdtime.Location {
	loc, err := stdtime.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// mustTOD parses a "HH:MM <location>" time-of-day string, failing the test
// on a malformed value.
func mustTOD(s string) libtime.TimeOfDay {
	v, err := libtime.ParseTimeOfDay(context.Background(), s)
	Expect(err).NotTo(HaveOccurred())
	return *v
}

// at returns a fixed-clock DateTime for the given hour/minute in loc on a
// fixed date, so window tests never depend on the wall clock.
func at(h, min int, loc *stdtime.Location) libtime.DateTime {
	return libtime.DateTime(stdtime.Date(2026, 8, 19, h, min, 0, 0, loc))
}

// pinSlotID mirrors the production FNV-1a weighted-ring slot computation
// (upstream-pool-handler.go pinSlot) so tests pick session ids pinned to a
// target member deterministically instead of hard-coding hash outcomes.
func pinSlotID(sessionID string, weights ...int) int {
	total := 0
	cumulative := make([]int, len(weights))
	for i, w := range weights {
		total += w
		cumulative[i] = total
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(sessionID))
	slot := h.Sum64() % uint64(total)
	for i, c := range cumulative {
		if uint64(c) > slot {
			return i
		}
	}
	return len(weights) - 1
}

// pinID returns a session id whose weighted-ring slot over weights equals
// target.
func pinID(target int, weights ...int) string {
	for i := 0; i < 500; i++ {
		id := fmt.Sprintf("win-%d", i)
		if pinSlotID(id, weights...) == target {
			return id
		}
	}
	Fail("no session id pinned to the target member")
	return ""
}

// windowedMember builds an UpstreamMember carrying the given window and the
// fixed clock's Now, mirroring the factory wiring (pkg/factory/factory.go).
func windowedMember(
	upstream, from, until string,
	h http.Handler,
	clock libtime.CurrentDateTimeGetter,
) handler.UpstreamMember {
	return handler.UpstreamMember{
		Upstream: upstream,
		Handler:  h,
		Weight:   1,
		Window:   &pkg.Window{From: mustTOD(from), Until: mustTOD(until)},
		Now:      clock.Now,
	}
}

// sessionedReq builds a /v1/messages request with sessionID injected into
// its context via the session-middleware seam (the middleware itself is
// not run here).
func sessionedReq(sessionID string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	return req.WithContext(handler.ContextWithSessionID(req.Context(), sessionID))
}

// windowStub is a provider handler that reports a fixed eligibility,
// implementing the WindowEligible seam so a model pool can exercise the
// "provider closed" skip without a real time window.
type windowStub struct {
	http.Handler
	eligible bool
}

func (w *windowStub) HasEligibleMember(context.Context) bool { return w.eligible }

// mustDays parses a "comma-separated weekday names, optional location"
// value into a *pkg.Days, failing the test on a malformed one.
func mustDays(s string) *pkg.Days {
	d := &pkg.Days{}
	Expect(d.UnmarshalText([]byte(s))).To(Succeed())
	return d
}

// daysMember builds an UpstreamMember carrying the given days set and
// the fixed clock's Now, mirroring the factory wiring. window may be
// nil (a days-only member).
func daysMember(
	upstream, days string,
	h http.Handler,
	clock libtime.CurrentDateTimeGetter,
	window *pkg.Window,
) handler.UpstreamMember {
	return handler.UpstreamMember{
		Upstream: upstream,
		Handler:  h,
		Weight:   1,
		Window:   window,
		Days:     mustDays(days),
		Now:      clock.Now,
	}
}

// poolEligible reports whether a pool over the given members has at
// least one eligible member right now, mirroring the router's
// windowEligible gate.
func poolEligible(members ...handler.UpstreamMember) bool {
	pool := handler.NewUpstreamPoolHandler(context.Background(), members)
	e, ok := pool.(interface{ HasEligibleMember(context.Context) bool })
	if !ok {
		return true
	}
	return e.HasEligibleMember(context.Background())
}

// atDate returns a fixed-clock DateTime for the given date/time in loc,
// so weekday tests never depend on the wall clock. Fixed dates used
// below: 2026-08-19 Wednesday, 2026-08-21 Friday, 2026-08-22 Saturday,
// 2026-08-23 Sunday, 2026-08-24 Monday.
func atDate(y, mo, d, h, min int, loc *stdtime.Location) libtime.DateTime {
	return libtime.DateTime(stdtime.Date(y, stdtime.Month(mo), d, h, min, 0, 0, loc))
}

var _ = Describe("UpstreamPoolHandler time windows", func() {
	It(
		"AC 3: complementary windows — business-hours requests serve only the day member, off-peak only the night",
		func() {
			clock := libtime.NewCurrentDateTime()
			clock.SetNow(at(10, 0, berlin))
			dayCounter := &countingHandler{}
			nightCounter := &countingHandler{}
			pool := handler.NewUpstreamPoolHandler(context.Background(), []handler.UpstreamMember{
				windowedMember(
					"https://day", "08:00 Europe/Berlin", "18:00 Europe/Berlin",
					handler.NewConcurrencyLimiter(dayCounter, 16, stdtime.Second), clock,
				),
				windowedMember(
					"https://night", "18:00 Europe/Berlin", "08:00 Europe/Berlin",
					handler.NewConcurrencyLimiter(nightCounter, 50, stdtime.Second), clock,
				),
			})

			// Business hours (10:00): every sessioned and keyless request lands on
			// the day member, well below its cap of 16, so no genuine 429 can occur.
			for i := 0; i < 4; i++ {
				rec := httptest.NewRecorder()
				pool.ServeHTTP(rec, sessionedReq(fmt.Sprintf("day-%d", i)))
				Expect(rec.Code).To(Equal(http.StatusOK))
			}
			for i := 0; i < 4; i++ {
				rec := httptest.NewRecorder()
				pool.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
				Expect(rec.Code).To(Equal(http.StatusOK))
			}
			Expect(
				dayCounter.invocations(),
			).To(Equal(8), "business-hours requests must all land on the day member")
			Expect(
				nightCounter.invocations(),
			).To(Equal(0), "the night member must see no business-hours traffic")

			// Off-peak (22:00): the same bounded batch all lands on the night member.
			clock.SetNow(at(22, 0, berlin))
			for i := 0; i < 4; i++ {
				rec := httptest.NewRecorder()
				pool.ServeHTTP(rec, sessionedReq(fmt.Sprintf("night-%d", i)))
				Expect(rec.Code).To(Equal(http.StatusOK))
			}
			for i := 0; i < 4; i++ {
				rec := httptest.NewRecorder()
				pool.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
				Expect(rec.Code).To(Equal(http.StatusOK))
			}
			Expect(
				nightCounter.invocations(),
			).To(Equal(8), "off-peak requests must all land on the night member")
			Expect(
				dayCounter.invocations(),
			).To(Equal(8), "the day member must see no off-peak traffic")
		},
	)

	It(
		"AC 3: a session re-resolves to an eligible member when its window closes; the in-flight request completes",
		func() {
			clock := libtime.NewCurrentDateTime()
			clock.SetNow(at(17, 59, berlin))
			dayBlocker := newBlockingHandler()
			nightCounter := &countingHandler{}
			pool := handler.NewUpstreamPoolHandler(context.Background(), []handler.UpstreamMember{
				windowedMember(
					"https://day",
					"08:00 Europe/Berlin",
					"18:00 Europe/Berlin",
					dayBlocker,
					clock,
				),
				windowedMember(
					"https://night",
					"18:00 Europe/Berlin",
					"08:00 Europe/Berlin",
					nightCounter,
					clock,
				),
			})
			id := pinID(0, 1, 1) // pinned to the day member over the full ring

			// Request 1 (blocked on the day member) starts before the boundary.
			rec1 := httptest.NewRecorder()
			done1 := serveAsync(pool, rec1, sessionedReq(id))
			Eventually(dayBlocker.entryCount, "1s", "10ms").
				Should(Equal(1), "request 1 must be in flight on the day member")

			// The window closes; request 2 (same session) re-resolves to the night
			// member — selection recomputes eligibility per request, stateless.
			clock.SetNow(at(18, 1, berlin))
			rec2 := httptest.NewRecorder()
			pool.ServeHTTP(rec2, sessionedReq(id))
			Expect(rec2.Code).To(Equal(http.StatusOK))
			Expect(
				nightCounter.invocations(),
			).To(Equal(1), "request 2 must be served by the night member")
			Expect(
				dayBlocker.entryCount(),
			).To(Equal(1), "request 1 must still be in flight on the day member")

			// Release request 1 — it completes 200 even though the boundary passed.
			close(dayBlocker.release)
			Eventually(done1, "1s").Should(BeClosed())
			Expect(rec1.Code).To(Equal(http.StatusOK))
		},
	)

	It(
		"AC 4: an overnight window (22:00 -> 06:00) treats 02:00 as inside and 14:00 as outside",
		func() {
			clock := libtime.NewCurrentDateTime()
			night := labelHandler("night")
			open := labelHandler("open")
			pool := handler.NewUpstreamPoolHandler(context.Background(), []handler.UpstreamMember{
				windowedMember(
					"https://night",
					"22:00 Europe/Berlin",
					"06:00 Europe/Berlin",
					night,
					clock,
				),
				{Upstream: "https://open", Handler: open, Weight: 1},
			})
			id := pinID(0, 1, 1) // pinned to the night member over the full ring

			clock.SetNow(at(2, 0, berlin)) // 02:00 Berlin — inside the overnight window
			rec := httptest.NewRecorder()
			pool.ServeHTTP(rec, sessionedReq(id))
			Expect(rec.Body.String()).To(Equal("night"))

			clock.SetNow(at(14, 0, berlin)) // 14:00 Berlin — outside
			rec = httptest.NewRecorder()
			pool.ServeHTTP(rec, sessionedReq(id))
			Expect(rec.Body.String()).To(Equal("open"))
		},
	)

	It(
		"AC 5: the window boundary is the value's attached IANA location, never host local time",
		func() {
			clock := libtime.NewCurrentDateTime()
			berlinWin := labelHandler("berlin-win")
			open := labelHandler("open")
			pool := handler.NewUpstreamPoolHandler(context.Background(), []handler.UpstreamMember{
				windowedMember(
					"https://berlin",
					"17:00 Europe/Berlin",
					"18:00 Europe/Berlin",
					berlinWin,
					clock,
				),
				{Upstream: "https://open", Handler: open, Weight: 1},
			})
			id := pinID(0, 1, 1) // pinned to the berlin member over the full ring

			// 15:30 UTC IS 17:30 Berlin — inside.
			clock.SetNow(libtime.DateTime(stdtime.Date(2026, 8, 19, 15, 30, 0, 0, stdtime.UTC)))
			rec := httptest.NewRecorder()
			pool.ServeHTTP(rec, sessionedReq(id))
			Expect(rec.Body.String()).To(Equal("berlin-win"))

			// 17:30 UTC is 19:30 Berlin — outside.
			clock.SetNow(libtime.DateTime(stdtime.Date(2026, 8, 19, 17, 30, 0, 0, stdtime.UTC)))
			rec = httptest.NewRecorder()
			pool.ServeHTTP(rec, sessionedReq(id))
			Expect(rec.Body.String()).To(Equal("open"))

			// 16:00 UTC is exactly 18:00 Berlin, and [From, Until) excludes Until.
			clock.SetNow(libtime.DateTime(stdtime.Date(2026, 8, 19, 16, 0, 0, 0, stdtime.UTC)))
			rec = httptest.NewRecorder()
			pool.ServeHTTP(rec, sessionedReq(id))
			Expect(rec.Body.String()).To(Equal("open"))
		},
	)

	It(
		"DB 3: a closed member is skipped by pinning and by keyless least-loaded selection",
		func() {
			clock := libtime.NewCurrentDateTime()
			clock.SetNow(at(12, 0, berlin))
			a := &countingHandler{}
			b := &countingHandler{}
			pool := handler.NewUpstreamPoolHandler(context.Background(), []handler.UpstreamMember{
				windowedMember("https://a", "00:00 Europe/Berlin", "06:00 Europe/Berlin", a, clock),
				{Upstream: "https://b", Handler: b, Weight: 1},
			})
			// A session id that WOULD pin to A over the full [A,B] ring is served
			// by B — A's window excludes 12:00.
			id := pinID(0, 1, 1)
			rec := httptest.NewRecorder()
			pool.ServeHTTP(rec, sessionedReq(id))
			Expect(a.invocations()).To(Equal(0), "the closed member must not serve")
			Expect(b.invocations()).To(Equal(1), "the open member must serve the pinned session")

			// Least-loaded sibling: the closed member is excluded even when it
			// reports less load than the open one.
			a2 := &countingHandler{}
			b2 := &countingHandler{}
			pool2 := handler.NewUpstreamPoolHandler(context.Background(), []handler.UpstreamMember{
				{
					Upstream: "https://a",
					Handler:  a2,
					Weight:   1,
					InFlight: func() int { return 0 },
					Window: &pkg.Window{
						From:  mustTOD("00:00 Europe/Berlin"),
						Until: mustTOD("06:00 Europe/Berlin"),
					},
					Now: clock.Now,
				},
				{Upstream: "https://b", Handler: b2, Weight: 1, InFlight: func() int { return 5 }},
			})
			rec2 := httptest.NewRecorder()
			pool2.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
			Expect(
				a2.invocations(),
			).To(Equal(0), "the closed member must not be least-loaded-selected")
			Expect(b2.invocations()).To(Equal(1))
		},
	)

	It(
		"AC 7: a closed window is eligibility, never a 429 and never an error",
		func() {
			clock := libtime.NewCurrentDateTime()
			clock.SetNow(at(10, 0, berlin))
			dayCounter := &countingHandler{}
			nightCounter := &countingHandler{}
			pool := handler.NewUpstreamPoolHandler(context.Background(), []handler.UpstreamMember{
				windowedMember(
					"https://day", "08:00 Europe/Berlin", "18:00 Europe/Berlin",
					handler.NewConcurrencyLimiter(dayCounter, 16, stdtime.Second), clock,
				),
				windowedMember(
					"https://night", "18:00 Europe/Berlin", "08:00 Europe/Berlin",
					handler.NewConcurrencyLimiter(nightCounter, 50, stdtime.Second), clock,
				),
			})
			_ = flag.Set("logtostderr", "true")
			_ = flag.Set("v", "2")
			out := captureStderr(func() {
				for i := 0; i < 4; i++ {
					rec := httptest.NewRecorder()
					pool.ServeHTTP(rec, sessionedReq(fmt.Sprintf("neg-%d", i)))
					Expect(rec.Code).To(Equal(http.StatusOK))
				}
				for i := 0; i < 4; i++ {
					rec := httptest.NewRecorder()
					pool.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
					Expect(rec.Code).To(Equal(http.StatusOK))
				}
			})
			Expect(dayCounter.invocations()).To(Equal(8))
			Expect(nightCounter.invocations()).To(Equal(0))
			Expect(out).NotTo(ContainSubstring("status=429"))
			Expect(out).NotTo(MatchRegexp(`ERROR|error`))
		},
	)

	It("DB 1: a window-less pool behaves byte-for-byte as before", func() {
		// Pinning stability: a session pinned to member 0 stays there.
		a := &countingHandler{}
		b := &countingHandler{}
		pool := handler.NewUpstreamPoolHandler(context.Background(), []handler.UpstreamMember{
			{Upstream: "https://a", Handler: a, Weight: 1},
			{Upstream: "https://b", Handler: b, Weight: 1},
		})
		id := pinID(0, 1, 1)
		for i := 0; i < 3; i++ {
			rec := httptest.NewRecorder()
			pool.ServeHTTP(rec, sessionedReq(id))
		}
		Expect(a.invocations()).To(Equal(3))
		Expect(b.invocations()).To(Equal(0))

		// Least-loaded unchanged: a loaded member loses keyless traffic to the
		// idle one (spec 012 AC 3).
		a2 := &countingHandler{}
		b2 := &countingHandler{}
		pool2 := handler.NewUpstreamPoolHandler(context.Background(), []handler.UpstreamMember{
			{Upstream: "https://a", Handler: a2, Weight: 1, InFlight: func() int { return 1 }},
			{Upstream: "https://b", Handler: b2, Weight: 1, InFlight: func() int { return 0 }},
		})
		rec := httptest.NewRecorder()
		pool2.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		Expect(a2.invocations()).To(Equal(0))
		Expect(b2.invocations()).To(Equal(1))
	})
})

var _ = Describe("ModelRouter time-window fall-through", func() {
	post := func(body string) *http.Request {
		return httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	}

	It("falls through to the next glob-matching provider whose pool is eligible", func() {
		clock := libtime.NewCurrentDateTime()
		clock.SetNow(at(22, 0, berlin)) // day window [08:00,18:00) closed
		dayCounter := &countingHandler{}
		nightCounter := &countingHandler{}
		dayPool := handler.NewUpstreamPoolHandler(context.Background(), []handler.UpstreamMember{
			windowedMember(
				"https://day",
				"08:00 Europe/Berlin",
				"18:00 Europe/Berlin",
				dayCounter,
				clock,
			),
		})
		nightPool := handler.NewUpstreamPoolHandler(context.Background(), []handler.UpstreamMember{
			{Upstream: "https://night", Handler: nightCounter, Weight: 1},
		})
		mux := handler.NewModelRouter(
			[]handler.ModelRoute{
				{Pattern: "deepseek-*", ProviderName: "day-pool", Handler: dayPool},
				{Pattern: "deepseek-*", ProviderName: "night-pool", Handler: nightPool},
			},
			"default",
			labelHandler("fallback"),
			nil,
			alwaysSample,
			testMetrics,
			testDateTime,
		)
		_ = flag.Set("logtostderr", "true")
		_ = flag.Set("v", "2")
		rec := httptest.NewRecorder()
		out := captureStderr(func() {
			mux.ServeHTTP(rec, post(`{"model":"deepseek-x"}`))
		})
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(dayCounter.invocations()).To(Equal(0), "the closed day-pool must not serve")
		Expect(nightCounter.invocations()).To(Equal(1), "the eligible night-pool must serve")
		Expect(out).To(ContainSubstring("[route] provider=day-pool window=closed -> night-pool"))
	})

	It("falls through to default_provider when the only glob-matching provider is closed", func() {
		clock := libtime.NewCurrentDateTime()
		clock.SetNow(at(22, 0, berlin))
		dayCounter := &countingHandler{}
		dayPool := handler.NewUpstreamPoolHandler(context.Background(), []handler.UpstreamMember{
			windowedMember(
				"https://day",
				"08:00 Europe/Berlin",
				"18:00 Europe/Berlin",
				dayCounter,
				clock,
			),
		})
		mux := handler.NewModelRouter(
			[]handler.ModelRoute{
				{Pattern: "deepseek-*", ProviderName: "day-pool", Handler: dayPool},
			},
			"default",
			labelHandler("fallback"),
			nil,
			alwaysSample,
			testMetrics,
			testDateTime,
		)
		_ = flag.Set("logtostderr", "true")
		_ = flag.Set("v", "2")
		rec := httptest.NewRecorder()
		out := captureStderr(func() {
			mux.ServeHTTP(rec, post(`{"model":"deepseek-x"}`))
		})
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(Equal("fallback"))
		Expect(dayCounter.invocations()).To(Equal(0))
		Expect(out).To(ContainSubstring("[route] provider=day-pool window=closed -> default"))
		Expect(out).NotTo(ContainSubstring("status=429"))
	})

	It("model pool skips a member whose provider pool is closed", func() {
		pool := handler.NewModelPool(context.Background(), []handler.ModelPoolMember{
			{Provider: "a", Model: "a", Weight: 1, Handler: &windowStub{eligible: false}},
			{Provider: "b", Model: "b", Weight: 1, Handler: &windowStub{eligible: true}},
		})
		id := pinID(0, 1, 1) // pinned to member 0 over the full [A,B] ring
		Expect(pool.Resolve(context.Background(), id).Provider).To(Equal("b"))
		Expect(pool.Resolve(context.Background(), "").Provider).To(Equal("b"))
	})
})

var _ = Describe("UpstreamPoolHandler weekday eligibility (days)", func() {
	It(
		"AC 3: a days-only weekend member is eligible all day Sat+Sun (Berlin) and ineligible Mon-Fri, through the real dispatch path",
		func() {
			clock := libtime.NewCurrentDateTime()
			id := pinID(0, 1, 1) // pinned to member 0 over the full ring

			// ELIGIBLE Berlin instants: Sat and Sun all day. A fresh weekend/open
			// pair per instant (the countingHandler has no reset) so the counters
			// assert cleanly.
			eligible := []libtime.DateTime{
				atDate(2026, 8, 22, 0, 1, berlin),   // Sat 00:01
				atDate(2026, 8, 22, 10, 0, berlin),  // Sat 10:00
				atDate(2026, 8, 22, 23, 59, berlin), // Sat 23:59
				atDate(2026, 8, 23, 0, 1, berlin),   // Sun 00:01
				atDate(2026, 8, 23, 23, 59, berlin), // Sun 23:59
			}
			for _, now := range eligible {
				weekend := &countingHandler{}
				open := &countingHandler{}
				pool := handler.NewUpstreamPoolHandler(
					context.Background(),
					[]handler.UpstreamMember{
						daysMember(
							"https://weekend",
							"saturday, sunday Europe/Berlin",
							weekend,
							clock,
							nil,
						),
						{Upstream: "https://open", Handler: open, Weight: 1},
					},
				)
				clock.SetNow(now)
				rec := httptest.NewRecorder()
				pool.ServeHTTP(rec, sessionedReq(id))
				Expect(rec.Code).To(Equal(http.StatusOK))
				rec = httptest.NewRecorder()
				pool.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
				Expect(rec.Code).To(Equal(http.StatusOK))
				Expect(
					weekend.invocations(),
				).To(Equal(2), "the weekend member must serve the pinned and keyless requests")
				Expect(
					open.invocations(),
				).To(Equal(0), "the open member must see no weekend traffic")
			}

			// INELIGIBLE Berlin instants: Mon and Fri — the same pinned id plus a
			// keyless request must both land on the open member.
			ineligible := []libtime.DateTime{
				atDate(2026, 8, 24, 0, 1, berlin),   // Mon 00:01
				atDate(2026, 8, 24, 10, 0, berlin),  // Mon 10:00
				atDate(2026, 8, 21, 23, 59, berlin), // Fri 23:59
			}
			for _, now := range ineligible {
				weekend := &countingHandler{}
				open := &countingHandler{}
				pool := handler.NewUpstreamPoolHandler(
					context.Background(),
					[]handler.UpstreamMember{
						daysMember(
							"https://weekend",
							"saturday, sunday Europe/Berlin",
							weekend,
							clock,
							nil,
						),
						{Upstream: "https://open", Handler: open, Weight: 1},
					},
				)
				clock.SetNow(now)
				rec := httptest.NewRecorder()
				pool.ServeHTTP(rec, sessionedReq(id))
				Expect(rec.Code).To(Equal(http.StatusOK))
				rec = httptest.NewRecorder()
				pool.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
				Expect(rec.Code).To(Equal(http.StatusOK))
				Expect(
					open.invocations(),
				).To(Equal(2), "the open member must serve the pinned and keyless requests")
				Expect(
					weekend.invocations(),
				).To(Equal(0), "the weekend member must serve nothing Mon-Fri")
			}
		},
	)

	It(
		"AC 3: the boundary is the attached location's calendar, not host UTC (offset boundary)",
		func() {
			clock := libtime.NewCurrentDateTime()
			weekend := &countingHandler{}
			open := &countingHandler{}
			pool := handler.NewUpstreamPoolHandler(context.Background(), []handler.UpstreamMember{
				daysMember(
					"https://weekend",
					"saturday, sunday Europe/Berlin",
					weekend,
					clock,
					nil,
				),
				{Upstream: "https://open", Handler: open, Weight: 1},
			})
			id := pinID(0, 1, 1) // pinned to the weekend member over the full ring

			// UTC Friday 22:30 IS Berlin Saturday 00:30 — the weekend member serves.
			clock.SetNow(libtime.DateTime(stdtime.Date(2026, 8, 21, 22, 30, 0, 0, stdtime.UTC)))
			rec := httptest.NewRecorder()
			pool.ServeHTTP(rec, sessionedReq(id))
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(weekend.invocations()).To(Equal(1), "UTC Friday evening is Berlin Saturday")
			Expect(open.invocations()).To(Equal(0))

			// UTC Sunday 22:30 IS Berlin Monday 00:30 — the open member serves.
			clock.SetNow(libtime.DateTime(stdtime.Date(2026, 8, 23, 22, 30, 0, 0, stdtime.UTC)))
			rec = httptest.NewRecorder()
			pool.ServeHTTP(rec, sessionedReq(id))
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(open.invocations()).To(Equal(1), "UTC Sunday evening is Berlin Monday")
			Expect(
				weekend.invocations(),
			).To(Equal(1), "the weekend member must stay idle on Berlin Monday")
		},
	)

	It(
		"AC 5: a member whose location is Europe/Berlin flips eligibility on Berlin's weekday while UTC disagrees",
		func() {
			clock := libtime.NewCurrentDateTime()
			berlinSun := daysMember(
				"https://berlin", "sunday Europe/Berlin", labelHandler("berlin-sun"), clock, nil,
			)
			utcSun := daysMember("https://utc", "sunday UTC", labelHandler("utc-sun"), clock, nil)
			pool := handler.NewUpstreamPoolHandler(
				context.Background(),
				[]handler.UpstreamMember{berlinSun, utcSun},
			)
			id := pinID(0, 1, 1) // pinned to member 0 over the full ring

			// UTC Sat 22:30 = Berlin Sun 00:30 — berlin-sun eligible, utc-sun not.
			clock.SetNow(libtime.DateTime(stdtime.Date(2026, 8, 22, 22, 30, 0, 0, stdtime.UTC)))
			rec := httptest.NewRecorder()
			pool.ServeHTTP(rec, sessionedReq(id))
			Expect(rec.Body.String()).To(Equal("berlin-sun"))

			// UTC Sun 22:30 = Berlin Mon 00:30 — utc-sun eligible, berlin-sun not.
			clock.SetNow(libtime.DateTime(stdtime.Date(2026, 8, 23, 22, 30, 0, 0, stdtime.UTC)))
			rec = httptest.NewRecorder()
			pool.ServeHTTP(rec, sessionedReq(id))
			Expect(rec.Body.String()).To(Equal("utc-sun"))
		},
	)

	It(
		"AC 4: complementary three-member pool — exactly one eligible member per (day, time), the cap traveling with each member",
		func() {
			clock := libtime.NewCurrentDateTime()
			weekend := daysMember(
				"https://weekend",
				"saturday, sunday Europe/Berlin",
				handler.NewConcurrencyLimiter(
					labelHandler("weekend"),
					50,
					stdtime.Second,
				),
				clock,
				nil,
			)
			day := handler.UpstreamMember{
				Upstream: "https://day",
				Handler:  handler.NewConcurrencyLimiter(labelHandler("day"), 16, stdtime.Second),
				Weight:   1,
				Window: &pkg.Window{
					From:  mustTOD("08:00 Europe/Berlin"),
					Until: mustTOD("18:00 Europe/Berlin"),
				},
				Days: mustDays("monday, friday"),
				Now:  clock.Now,
			}
			night := handler.UpstreamMember{
				Upstream: "https://night",
				Handler:  handler.NewConcurrencyLimiter(labelHandler("night"), 50, stdtime.Second),
				Weight:   1,
				Window: &pkg.Window{
					From:  mustTOD("18:00 Europe/Berlin"),
					Until: mustTOD("08:00 Europe/Berlin"),
				},
				Days: mustDays("monday, friday"),
				Now:  clock.Now,
			}
			pool := handler.NewUpstreamPoolHandler(
				context.Background(),
				[]handler.UpstreamMember{weekend, day, night},
			)

			points := []struct {
				now      libtime.DateTime
				eligible string
			}{
				{atDate(2026, 8, 24, 10, 0, berlin), "day"},     // Mon 10:00
				{atDate(2026, 8, 24, 22, 0, berlin), "night"},   // Mon 22:00
				{atDate(2026, 8, 24, 0, 30, berlin), "night"},   // Mon 00:30 (weekend NOT eligible)
				{atDate(2026, 8, 22, 0, 30, berlin), "weekend"}, // Sat 00:30 (night NOT eligible)
				{atDate(2026, 8, 22, 10, 0, berlin), "weekend"}, // Sat 10:00
				{atDate(2026, 8, 22, 22, 0, berlin), "weekend"}, // Sat 22:00
				{atDate(2026, 8, 23, 10, 0, berlin), "weekend"}, // Sun 10:00
				{atDate(2026, 8, 21, 23, 30, berlin), "night"},  // Fri 23:30
			}
			for _, pt := range points {
				clock.SetNow(pt.now)
				candidates := []struct {
					name   string
					member handler.UpstreamMember
				}{
					{"weekend", weekend},
					{"day", day},
					{"night", night},
				}
				eligibleCount := 0
				eligibleName := ""
				for _, c := range candidates {
					if poolEligible(c.member) {
						eligibleCount++
						eligibleName = c.name
					}
				}
				Expect(
					eligibleCount,
				).To(Equal(1), "exactly one member must be eligible at %v", pt.now)
				Expect(eligibleName).To(Equal(pt.eligible))

				// The real three-member pool dispatches both the pinned and the
				// keyless request to the single eligible member; the serving label
				// proves the member's distinct limiter cap travels with it.
				rec := httptest.NewRecorder()
				pool.ServeHTTP(rec, sessionedReq(pinID(0, 1, 1, 1)))
				Expect(rec.Body.String()).To(Equal(pt.eligible))
				rec = httptest.NewRecorder()
				pool.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
				Expect(rec.Body.String()).To(Equal(pt.eligible))
			}
		},
	)

	It(
		"AC 2: a member outside its days is skipped by keyless least-loaded selection",
		func() {
			clock := libtime.NewCurrentDateTime()
			clock.SetNow(atDate(2026, 8, 22, 10, 0, berlin)) // Saturday
			a := &countingHandler{}
			b := &countingHandler{}
			pool := handler.NewUpstreamPoolHandler(context.Background(), []handler.UpstreamMember{
				daysMember("https://a", "monday Europe/Berlin", a, clock, nil),
				{Upstream: "https://b", Handler: b, Weight: 1, InFlight: func() int { return 5 }},
			})
			rec := httptest.NewRecorder()
			pool.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
			Expect(
				a.invocations(),
			).To(Equal(0), "the days-closed member must not be least-loaded-selected")
			Expect(b.invocations()).To(Equal(1), "the open member must serve even at higher load")
		},
	)

	It(
		"AC 2: provider fall-through — a provider whose only member is outside its days logs window=closed and falls through, never 429",
		func() {
			clock := libtime.NewCurrentDateTime()
			clock.SetNow(atDate(2026, 8, 22, 10, 0, berlin)) // Saturday
			weekdayCounter := &countingHandler{}
			weekdayPool := handler.NewUpstreamPoolHandler(
				context.Background(),
				[]handler.UpstreamMember{
					daysMember(
						"https://weekday",
						"monday, friday Europe/Berlin",
						weekdayCounter,
						clock,
						nil,
					),
				},
			)
			mux := handler.NewModelRouter(
				[]handler.ModelRoute{
					{Pattern: "deepseek-*", ProviderName: "weekday-pool", Handler: weekdayPool},
				},
				"default",
				labelHandler("fallback"),
				nil,
				alwaysSample,
				testMetrics,
				testDateTime,
			)
			_ = flag.Set("logtostderr", "true")
			_ = flag.Set("v", "2")
			rec := httptest.NewRecorder()
			out := captureStderr(func() {
				mux.ServeHTTP(
					rec,
					httptest.NewRequest(
						http.MethodPost,
						"/v1/messages",
						strings.NewReader(`{"model":"deepseek-x"}`),
					),
				)
			})
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(rec.Body.String()).To(Equal("fallback"))
			Expect(weekdayCounter.invocations()).To(Equal(0), "the days-closed pool must not serve")
			Expect(
				out,
			).To(ContainSubstring("[route] provider=weekday-pool window=closed -> default"))
			Expect(out).NotTo(ContainSubstring("status=429"))
			Expect(out).NotTo(ContainSubstring("ERROR"))
		},
	)
})

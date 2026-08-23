// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Time-window wiring specs (spec 014 AC 7): the factory copies each
// configured upstream window onto the runtime pool member and drives it
// from the injected libtime.CurrentDateTimeGetter, so a SIGHUP-rebuilt
// pool tree enforces an edited window without a restart, and a provider
// whose only member's window is closed falls through to the default
// provider — 200, never a 429.

package factory_test

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/factory"
	"github.com/bborbe/claude-code-router/pkg/handler"
)

// mustTOD parses a "HH:MM <location>" time-of-day string, failing the test
// on a malformed value.
func mustTOD(s string) libtime.TimeOfDay {
	v, err := libtime.ParseTimeOfDay(context.Background(), s)
	Expect(err).NotTo(HaveOccurred())
	return *v
}

// mustDays parses a "comma-separated weekday names, optional location"
// value into a *pkg.Days, failing the test on a malformed one.
func mustDays(s string) *pkg.Days {
	d := &pkg.Days{}
	Expect(d.UnmarshalText([]byte(s))).To(Succeed())
	return d
}

// berlin is the fixed IANA location used by the weekday wiring rows.
var berlin = func() *stdtime.Location {
	l, err := stdtime.LoadLocation("Europe/Berlin")
	if err != nil {
		panic(err)
	}
	return l
}()

// newMessagesRequest builds a /v1/messages request with the given model.
// The model is the only variable part; everything else matches the body
// shape the model router dispatches on.
func newMessagesRequest(model string) *http.Request {
	body := fmt.Sprintf(
		`{"model":%q,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
		model,
	)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// sessionedRequest returns a /v1/messages request with the given model and
// a session id injected directly into its context. The session middleware
// is not run for these (its own behavior is covered in the handler
// package); the upstream pool handler reads the id from context to pin.
func sessionedRequest(id, model string) *http.Request {
	req := newMessagesRequest(model)
	return req.WithContext(handler.ContextWithSessionID(req.Context(), id))
}

// serveAsync runs ServeHTTP in a goroutine and returns a channel that
// closes when it returns, so the test can read the recorder without racing
// the goroutine's writes.
func serveAsync(
	h http.Handler,
	rec *httptest.ResponseRecorder,
	req *http.Request,
) chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()
	return done
}

var _ = Describe("CreateRouterFromConfig time-window wiring", func() {
	var (
		a *poolUpstream
		b *poolUpstream
	)

	buildRouter := func(cfg *pkg.Config, clock libtime.CurrentDateTimeGetter) http.Handler {
		h, err := factory.CreateRouterFromConfig(
			context.Background(),
			cfg,
			isolatedRegistry(),
			factory.WithCurrentDateTime(clock),
		)
		Expect(err).NotTo(HaveOccurred())
		return h
	}

	BeforeEach(func() {
		a = newPoolUpstream()
		b = newPoolUpstream()
	})

	AfterEach(func() {
		a.closeUpstream()
		b.closeUpstream()
	})

	It("rebuilds the pool tree — a rebuilt pool enforces the new window", func() {
		clock := libtime.NewCurrentDateTime()
		clock.SetNow(libtime.DateTime(stdtime.Date(2026, 8, 19, 10, 0, 0, 0, stdtime.UTC)))

		// cfg1: member A eligible at 10:00 (08:00-18:00 Berlin), member B open.
		cfg1 := &pkg.Config{
			Router: pkg.Router{DefaultProvider: "pool"},
			Providers: map[string]pkg.Provider{
				"pool": {
					Models: []string{"m*"},
					Upstreams: []pkg.Upstream{
						{
							Upstream: a.url,
							Weight:   1,
							Window: &pkg.Window{
								From:  mustTOD("08:00 Europe/Berlin"),
								Until: mustTOD("18:00 Europe/Berlin"),
							},
						},
						{Upstream: b.url, Weight: 1},
					},
				},
			},
		}
		router1 := buildRouter(cfg1, clock)
		// A session id pinned to member A over the full [A,B] ring.
		id := sessionPinnedToSlot(0, 1, 1)
		rec1 := httptest.NewRecorder()
		done1 := serveAsync(router1, rec1, sessionedRequest(id, "m1"))
		Eventually(func() int32 { return atomic.LoadInt32(&a.inFlight) }, "1s", "10ms").
			Should(BeNumerically("==", 1), "request 1 must be in flight on member A")
		Expect(
			atomic.LoadInt32(&b.inFlight),
		).To(BeNumerically("==", 0), "member B must stay idle at 10:00")

		// cfg2 — a SECOND CreateRouterFromConfig exactly mirrors the reloader's
		// SIGHUP rebuild — member A's window is now closed at 10:00 (20:00-22:00).
		cfg2 := &pkg.Config{
			Router: pkg.Router{DefaultProvider: "pool"},
			Providers: map[string]pkg.Provider{
				"pool": {
					Models: []string{"m*"},
					Upstreams: []pkg.Upstream{
						{
							Upstream: a.url,
							Weight:   1,
							Window: &pkg.Window{
								From:  mustTOD("20:00 Europe/Berlin"),
								Until: mustTOD("22:00 Europe/Berlin"),
							},
						},
						{Upstream: b.url, Weight: 1},
					},
				},
			},
		}
		router2 := buildRouter(cfg2, clock)

		// The same session id through the rebuilt router: only B is eligible, so
		// the rebuilt pool must serve it from B — A's in-flight stays unchanged.
		rec2 := httptest.NewRecorder()
		done2 := serveAsync(router2, rec2, sessionedRequest(id, "m2"))
		Eventually(func() int32 { return atomic.LoadInt32(&b.inFlight) }, "1s", "10ms").
			Should(BeNumerically("==", 1), "the rebuilt pool must serve the pinned session from member B")
		Expect(
			atomic.LoadInt32(&a.inFlight),
		).To(BeNumerically("==", 1), "the rebuilt pool must not touch member A")

		a.unblock()
		b.unblock()
		Eventually(done1, "1s").Should(BeClosed())
		Eventually(done2, "1s").Should(BeClosed())
		Expect(rec1.Code).To(Equal(http.StatusOK))
		Expect(rec2.Code).To(Equal(http.StatusOK))
	})

	It("rebuilds the pool tree — a rebuilt pool enforces a changed days set on SIGHUP", func() {
		clock := libtime.NewCurrentDateTime()
		clock.SetNow(
			libtime.DateTime(stdtime.Date(2026, 8, 21, 10, 0, 0, 0, berlin)),
		) // Friday 10:00 Berlin

		// cfg1: member A eligible Friday (monday, friday), member B open.
		cfg1 := &pkg.Config{
			Router: pkg.Router{DefaultProvider: "pool"},
			Providers: map[string]pkg.Provider{
				"pool": {
					Models: []string{"m*"},
					Upstreams: []pkg.Upstream{
						{
							Upstream: a.url,
							Weight:   1,
							Days:     mustDays("monday, friday Europe/Berlin"),
						},
						{Upstream: b.url, Weight: 1},
					},
				},
			},
		}
		router1 := buildRouter(cfg1, clock)
		// A session id pinned to member A over the full [A,B] ring.
		id := sessionPinnedToSlot(0, 1, 1)
		rec1 := httptest.NewRecorder()
		done1 := serveAsync(router1, rec1, sessionedRequest(id, "m1"))
		Eventually(func() int32 { return atomic.LoadInt32(&a.inFlight) }, "1s", "10ms").
			Should(BeNumerically("==", 1), "request 1 must be in flight on member A (eligible Friday)")
		Expect(
			atomic.LoadInt32(&b.inFlight),
		).To(BeNumerically("==", 0), "member B must stay idle at Friday 10:00")

		// cfg2 — a SECOND CreateRouterFromConfig exactly mirrors the reloader's
		// SIGHUP rebuild — member A's days changed to (saturday, sunday), so it
		// is closed at the fixed Friday 10:00.
		cfg2 := &pkg.Config{
			Router: pkg.Router{DefaultProvider: "pool"},
			Providers: map[string]pkg.Provider{
				"pool": {
					Models: []string{"m*"},
					Upstreams: []pkg.Upstream{
						{
							Upstream: a.url,
							Weight:   1,
							Days:     mustDays("saturday, sunday Europe/Berlin"),
						},
						{Upstream: b.url, Weight: 1},
					},
				},
			},
		}
		router2 := buildRouter(cfg2, clock)

		// The same session id through the rebuilt router: only B is eligible, so
		// the rebuilt pool must serve it from B — A's in-flight stays unchanged.
		rec2 := httptest.NewRecorder()
		done2 := serveAsync(router2, rec2, sessionedRequest(id, "m2"))
		Eventually(func() int32 { return atomic.LoadInt32(&b.inFlight) }, "1s", "10ms").
			Should(BeNumerically("==", 1), "the rebuilt pool must serve the pinned session from member B")
		Expect(
			atomic.LoadInt32(&a.inFlight),
		).To(BeNumerically("==", 1), "the rebuilt pool must not touch member A")

		a.unblock()
		b.unblock()
		Eventually(done1, "1s").Should(BeClosed())
		Eventually(done2, "1s").Should(BeClosed())
		Expect(rec1.Code).To(Equal(http.StatusOK))
		Expect(rec2.Code).To(Equal(http.StatusOK))
	})

	It(
		"falls through to the default provider when the only glob-matching provider is closed — never 429",
		func() {
			clock := libtime.NewCurrentDateTime()
			clock.SetNow(libtime.DateTime(stdtime.Date(2026, 8, 19, 10, 0, 0, 0, stdtime.UTC)))

			// "pool" globs m* but its only member's window [20:00,22:00) Berlin is
			// closed at the fixed 10:00 now; "fallback" globs * and is always open.
			// ProviderOrder pins declaration order so the closed pool is considered
			// before the fallback (programmatic configs sort keys otherwise).
			cfg := &pkg.Config{
				Router:        pkg.Router{DefaultProvider: "fallback"},
				ProviderOrder: []string{"pool", "fallback"},
				Providers: map[string]pkg.Provider{
					"pool": {
						Models: []string{"m*"},
						Upstreams: []pkg.Upstream{
							{
								Upstream: a.url,
								Weight:   1,
								Window: &pkg.Window{
									From:  mustTOD("20:00 Europe/Berlin"),
									Until: mustTOD("22:00 Europe/Berlin"),
								},
							},
						},
					},
					"fallback": {
						Models:    []string{"*"},
						Upstreams: []pkg.Upstream{{Upstream: b.url, Weight: 1}},
					},
				},
			}

			oldV := flag.Lookup("v").Value.String()
			oldLogToStderr := flag.Lookup("logtostderr").Value.String()
			defer func() {
				Expect(flag.Set("v", oldV)).To(Succeed())
				Expect(flag.Set("logtostderr", oldLogToStderr)).To(Succeed())
			}()
			Expect(flag.Set("v", "2")).To(Succeed())
			Expect(flag.Set("logtostderr", "true")).To(Succeed())

			router := buildRouter(cfg, clock)
			// The fallback upstream blocks until released (the poolUpstream harness),
			// so fire the request asynchronously and hold the stderr capture window
			// open until the fallback server is actually serving — the [route] line
			// is written before dispatch, so a risen B counter proves it landed inside
			// the window.
			rec := httptest.NewRecorder()
			done := serveAsync(router, rec, newMessagesRequest("m1"))
			out := captureStderr(func() {
				Eventually(func() int32 { return atomic.LoadInt32(&b.inFlight) }, "1s", "10ms").
					Should(BeNumerically("==", 1), "the fallback provider must serve")
			})
			b.unblock()
			Eventually(done, "1s").Should(BeClosed())
			Expect(rec.Code).To(Equal(http.StatusOK), "a closed window is eligibility, never a 429")
			Expect(a.count()).To(BeNumerically("==", 0), "the closed pool must not serve")
			Expect(out).To(ContainSubstring("[route] provider=pool window=closed -> fallback"))
			Expect(out).NotTo(ContainSubstring("status=429"))
			Expect(out).NotTo(ContainSubstring("ERROR"))
		},
	)

	It(
		"falls through to the default provider when the only glob-matching provider's days are closed — never 429",
		func() {
			clock := libtime.NewCurrentDateTime()
			// 2026-08-19 is Wednesday; a "monday, friday" member is closed.
			clock.SetNow(libtime.DateTime(stdtime.Date(2026, 8, 19, 10, 0, 0, 0, stdtime.UTC)))

			// "pool" globs m* but its only member's days (monday, friday Berlin) are
			// closed at the fixed Wednesday now; "fallback" globs * and is always
			// open. ProviderOrder pins declaration order so the closed pool is
			// considered before the fallback (programmatic configs sort keys
			// otherwise).
			cfg := &pkg.Config{
				Router:        pkg.Router{DefaultProvider: "fallback"},
				ProviderOrder: []string{"pool", "fallback"},
				Providers: map[string]pkg.Provider{
					"pool": {
						Models: []string{"m*"},
						Upstreams: []pkg.Upstream{
							{
								Upstream: a.url,
								Weight:   1,
								Days:     mustDays("monday, friday Europe/Berlin"),
							},
						},
					},
					"fallback": {
						Models:    []string{"*"},
						Upstreams: []pkg.Upstream{{Upstream: b.url, Weight: 1}},
					},
				},
			}

			oldV := flag.Lookup("v").Value.String()
			oldLogToStderr := flag.Lookup("logtostderr").Value.String()
			defer func() {
				Expect(flag.Set("v", oldV)).To(Succeed())
				Expect(flag.Set("logtostderr", oldLogToStderr)).To(Succeed())
			}()
			Expect(flag.Set("v", "2")).To(Succeed())
			Expect(flag.Set("logtostderr", "true")).To(Succeed())

			router := buildRouter(cfg, clock)
			// The fallback upstream blocks until released (the poolUpstream harness),
			// so fire the request asynchronously and hold the stderr capture window
			// open until the fallback server is actually serving — the [route] line
			// is written before dispatch, so a risen B counter proves it landed inside
			// the window.
			rec := httptest.NewRecorder()
			done := serveAsync(router, rec, newMessagesRequest("m1"))
			out := captureStderr(func() {
				Eventually(func() int32 { return atomic.LoadInt32(&b.inFlight) }, "1s", "10ms").
					Should(BeNumerically("==", 1), "the fallback provider must serve")
			})
			b.unblock()
			Eventually(done, "1s").Should(BeClosed())
			Expect(rec.Code).To(Equal(http.StatusOK), "closed days are eligibility, never a 429")
			Expect(a.count()).To(BeNumerically("==", 0), "the days-closed pool must not serve")
			Expect(out).To(ContainSubstring("[route] provider=pool window=closed -> fallback"))
			Expect(out).NotTo(ContainSubstring("status=429"))
			Expect(out).NotTo(ContainSubstring("ERROR"))
		},
	)
})

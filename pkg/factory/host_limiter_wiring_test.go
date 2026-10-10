// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/factory"
)

var _ = Describe("CreateRouterFromConfig host limiter wiring", func() {
	var (
		srv       *httptest.Server
		release   chan struct{}
		closeOnce sync.Once
		current   int32
		peak      int32
		hostKey   string
	)

	newMessagesRequest := func(model string) *http.Request {
		body := fmt.Sprintf(
			`{"model":%q,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			model,
		)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		return req
	}

	serveAsync := func(h http.Handler, rec *httptest.ResponseRecorder, req *http.Request) chan struct{} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			h.ServeHTTP(rec, req)
		}()
		return done
	}

	inFlight := func() int32 { return atomic.LoadInt32(&current) }
	peakInFlight := func() int32 { return atomic.LoadInt32(&peak) }

	BeforeEach(func() {
		release = make(chan struct{})
		closeOnce = sync.Once{}
		current = 0
		peak = 0
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c := atomic.AddInt32(&current, 1)
			// Raise the peak with a CAS loop so concurrent requests never
			// clobber a higher observed value.
			for {
				p := atomic.LoadInt32(&peak)
				if c <= p || atomic.CompareAndSwapInt32(&peak, p, c) {
					break
				}
			}
			defer atomic.AddInt32(&current, -1)
			select {
			case <-release:
			case <-r.Context().Done():
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
		u, err := url.Parse(srv.URL)
		Expect(err).NotTo(HaveOccurred())
		hostKey = pkg.UpstreamHostKey(u)
	})

	AfterEach(func() {
		closeOnce.Do(func() { close(release) })
		srv.Close()
	})

	It("shares one host budget across providers", func() {
		cfg := &pkg.Config{
			Router: pkg.Router{DefaultProvider: "a"},
			Providers: map[string]pkg.Provider{
				"a": {
					Upstream:                 srv.URL,
					Models:                   []string{"a*"},
					MaxConcurrentRequests:    4,
					MaxConcurrentWaitSeconds: 5,
				},
				"b": {
					Upstream:                 srv.URL,
					Models:                   []string{"b*"},
					MaxConcurrentRequests:    4,
					MaxConcurrentWaitSeconds: 5,
				},
			},
			UpstreamHostLimits: map[string]pkg.HostLimit{
				hostKey: {MaxConcurrentRequests: 2, MaxConcurrentWaitSeconds: 5},
			},
		}
		router, err := factory.CreateRouterFromConfig(context.Background(), cfg, isolatedRegistry())
		Expect(err).NotTo(HaveOccurred())

		recs := make([]*httptest.ResponseRecorder, 4)
		dones := make([]chan struct{}, 4)
		for i, model := range []string{"a1", "a2", "b1", "b2"} {
			recs[i] = httptest.NewRecorder()
			dones[i] = serveAsync(router, recs[i], newMessagesRequest(model))
		}
		Eventually(inFlight, "1s", "10ms").Should(BeNumerically("==", 2))
		Consistently(inFlight, "300ms", "30ms").
			Should(BeNumerically("==", 2), "the shared host budget must hold at 2 across both providers")

		closeOnce.Do(func() { close(release) })
		for i := range dones {
			Eventually(dones[i], "1s").Should(BeClosed())
			Expect(recs[i].Code).To(Equal(http.StatusOK))
		}
		Expect(peakInFlight()).To(Equal(int32(2)), "peak must be the host cap N, not 2N or 4")
	})

	It("shares one host budget across pool members of the same host", func() {
		cfg := &pkg.Config{
			Router: pkg.Router{DefaultProvider: "p"},
			Providers: map[string]pkg.Provider{
				"p": {
					Models: []string{"p*"},
					Upstreams: []pkg.Upstream{
						{
							Upstream:                 srv.URL,
							Weight:                   1,
							MaxConcurrentRequests:    1,
							MaxConcurrentWaitSeconds: 1,
						},
						{
							Upstream:                 srv.URL + "/v1",
							Weight:                   1,
							MaxConcurrentRequests:    1,
							MaxConcurrentWaitSeconds: 1,
						},
					},
				},
			},
			UpstreamHostLimits: map[string]pkg.HostLimit{
				hostKey: {MaxConcurrentRequests: 1, MaxConcurrentWaitSeconds: 1},
			},
		}
		router, err := factory.CreateRouterFromConfig(context.Background(), cfg, isolatedRegistry())
		Expect(err).NotTo(HaveOccurred())

		rec1 := httptest.NewRecorder()
		done1 := serveAsync(router, rec1, newMessagesRequest("p1"))
		Eventually(inFlight, "1s", "10ms").Should(BeNumerically("==", 1))

		// Request 2 is keyless, so least-loaded steers it to the OTHER member
		// (member 1's own InFlight is 1). That member's own cap is free, so
		// only a SHARED host budget can hold request 2. With one host limiter
		// per member, request 2 would reach the upstream and peak would be 2.
		rec2 := httptest.NewRecorder()
		router.ServeHTTP(rec2, newMessagesRequest("p2"))
		Expect(rec2.Code).To(Equal(http.StatusTooManyRequests))
		Expect(rec2.Body.String()).To(ContainSubstring("rate_limit_error"))
		Expect(peakInFlight()).To(Equal(int32(1)))

		closeOnce.Do(func() { close(release) })
		Eventually(done1, "1s").Should(BeClosed())
		Expect(rec1.Code).To(Equal(http.StatusOK))
	})

	It("passes through uncapped when the host is not named", func() {
		cfg := &pkg.Config{
			Router: pkg.Router{DefaultProvider: "p"},
			Providers: map[string]pkg.Provider{
				"p": {Upstream: srv.URL, Models: []string{"m*"}},
			},
			// Names a DIFFERENT host only — inert, covering the failure mode
			// "config names a host no provider resolves to".
			UpstreamHostLimits: map[string]pkg.HostLimit{
				"other.example": {MaxConcurrentRequests: 1},
			},
		}
		router, err := factory.CreateRouterFromConfig(context.Background(), cfg, isolatedRegistry())
		Expect(err).NotTo(HaveOccurred())

		recs := make([]*httptest.ResponseRecorder, 3)
		dones := make([]chan struct{}, 3)
		for i := range recs {
			recs[i] = httptest.NewRecorder()
			dones[i] = serveAsync(router, recs[i], newMessagesRequest("m1"))
		}
		Eventually(inFlight, "1s", "10ms").Should(BeNumerically("==", 3))

		closeOnce.Do(func() { close(release) })
		for i := range dones {
			Eventually(dones[i], "1s").Should(BeClosed())
			Expect(recs[i].Code).To(Equal(http.StatusOK))
		}
	})

	It("still enforces the member cap inside the host cap", func() {
		cfg := &pkg.Config{
			Router: pkg.Router{DefaultProvider: "p"},
			Providers: map[string]pkg.Provider{
				"p": {
					Upstream:                 srv.URL,
					Models:                   []string{"m*"},
					MaxConcurrentRequests:    2,
					MaxConcurrentWaitSeconds: 5,
				},
			},
			UpstreamHostLimits: map[string]pkg.HostLimit{
				hostKey: {MaxConcurrentRequests: 8, MaxConcurrentWaitSeconds: 5},
			},
		}
		router, err := factory.CreateRouterFromConfig(context.Background(), cfg, isolatedRegistry())
		Expect(err).NotTo(HaveOccurred())

		recs := make([]*httptest.ResponseRecorder, 4)
		dones := make([]chan struct{}, 4)
		for i := range recs {
			recs[i] = httptest.NewRecorder()
			dones[i] = serveAsync(router, recs[i], newMessagesRequest("m1"))
		}
		Eventually(inFlight, "1s", "10ms").Should(BeNumerically("==", 2))
		Consistently(inFlight, "300ms", "30ms").
			Should(BeNumerically("==", 2), "the member cap of 2 must hold inside the host cap of 8")

		closeOnce.Do(func() { close(release) })
		for i := range dones {
			Eventually(dones[i], "1s").Should(BeClosed())
			Expect(recs[i].Code).To(Equal(http.StatusOK))
		}
		Expect(peakInFlight()).To(Equal(int32(2)))
	})

	It("treats a negative host maxConcurrentRequests as unlimited", func() {
		cfg := &pkg.Config{
			Router: pkg.Router{DefaultProvider: "p"},
			Providers: map[string]pkg.Provider{
				"p": {Upstream: srv.URL, Models: []string{"m*"}},
			},
			UpstreamHostLimits: map[string]pkg.HostLimit{
				hostKey: {MaxConcurrentRequests: -1},
			},
		}
		router, err := factory.CreateRouterFromConfig(context.Background(), cfg, isolatedRegistry())
		Expect(err).NotTo(HaveOccurred())

		recs := make([]*httptest.ResponseRecorder, 3)
		dones := make([]chan struct{}, 3)
		for i := range recs {
			recs[i] = httptest.NewRecorder()
			dones[i] = serveAsync(router, recs[i], newMessagesRequest("m1"))
		}
		Eventually(inFlight, "1s", "10ms").Should(BeNumerically("==", 3))

		closeOnce.Do(func() { close(release) })
		for i := range dones {
			Eventually(dones[i], "1s").Should(BeClosed())
			Expect(recs[i].Code).To(Equal(http.StatusOK))
		}
	})

	It("resolves a negative host maxConcurrentWaitSeconds to the 30s default", func() {
		cfg := &pkg.Config{
			Router: pkg.Router{DefaultProvider: "p"},
			Providers: map[string]pkg.Provider{
				"p": {Upstream: srv.URL, Models: []string{"m*"}},
			},
			UpstreamHostLimits: map[string]pkg.HostLimit{
				hostKey: {MaxConcurrentRequests: 1, MaxConcurrentWaitSeconds: -1},
			},
		}
		router, err := factory.CreateRouterFromConfig(context.Background(), cfg, isolatedRegistry())
		Expect(err).NotTo(HaveOccurred())

		rec1 := httptest.NewRecorder()
		done1 := serveAsync(router, rec1, newMessagesRequest("m1"))
		Eventually(inFlight, "1s", "10ms").Should(BeNumerically("==", 1))

		rec2 := httptest.NewRecorder()
		rec2Done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			router.ServeHTTP(rec2, newMessagesRequest("m2"))
			rec2Done <- rec2
		}()
		Consistently(func() int {
			select {
			case <-rec2Done:
				return 1
			default:
				return 0
			}
		}, "300ms", "30ms").
			Should(Equal(0), "request 2 must still be queued — 0 would have been an instant timeout")

		closeOnce.Do(func() { close(release) })
		Eventually(rec2Done, "1s").Should(Receive())
		Expect(rec2.Code).To(Equal(http.StatusOK))
		Eventually(done1, "1s").Should(BeClosed())
		Expect(rec1.Code).To(Equal(http.StatusOK))
	})

	It("rebuilds host limiters on a fresh CreateRouterFromConfig (SIGHUP path)", func() {
		provider := pkg.Provider{Upstream: srv.URL, Models: []string{"m*"}}
		cfg1 := &pkg.Config{
			Router:    pkg.Router{DefaultProvider: "p"},
			Providers: map[string]pkg.Provider{"p": provider},
			UpstreamHostLimits: map[string]pkg.HostLimit{
				hostKey: {MaxConcurrentRequests: 1, MaxConcurrentWaitSeconds: 5},
			},
		}
		handler1, err := factory.CreateRouterFromConfig(
			context.Background(),
			cfg1,
			isolatedRegistry(),
		)
		Expect(err).NotTo(HaveOccurred())

		recA := httptest.NewRecorder()
		doneA := serveAsync(handler1, recA, newMessagesRequest("m1"))
		Eventually(inFlight, "1s", "10ms").Should(BeNumerically("==", 1))

		recB := httptest.NewRecorder()
		doneB := serveAsync(handler1, recB, newMessagesRequest("m2"))
		Consistently(inFlight, "300ms", "30ms").
			Should(BeNumerically("==", 1), "handler1 caps the host at 1")

		// Raised cap: a fresh CreateRouterFromConfig mirrors the reloader.
		cfg2 := &pkg.Config{
			Router:    pkg.Router{DefaultProvider: "p"},
			Providers: map[string]pkg.Provider{"p": provider},
			UpstreamHostLimits: map[string]pkg.HostLimit{
				hostKey: {MaxConcurrentRequests: 2, MaxConcurrentWaitSeconds: 5},
			},
		}
		handler2, err := factory.CreateRouterFromConfig(
			context.Background(),
			cfg2,
			isolatedRegistry(),
		)
		Expect(err).NotTo(HaveOccurred())

		recC := httptest.NewRecorder()
		doneC := serveAsync(handler2, recC, newMessagesRequest("m3"))
		recD := httptest.NewRecorder()
		doneD := serveAsync(handler2, recD, newMessagesRequest("m4"))
		Eventually(inFlight, "1s", "10ms").
			Should(BeNumerically("==", 3), "A (handler1) + C + D (handler2) in flight")

		// Host removed: unlimited again on the third tree.
		cfg3 := &pkg.Config{
			Router:    pkg.Router{DefaultProvider: "p"},
			Providers: map[string]pkg.Provider{"p": provider},
		}
		handler3, err := factory.CreateRouterFromConfig(
			context.Background(),
			cfg3,
			isolatedRegistry(),
		)
		Expect(err).NotTo(HaveOccurred())

		recE := httptest.NewRecorder()
		doneE := serveAsync(handler3, recE, newMessagesRequest("m5"))
		recF := httptest.NewRecorder()
		doneF := serveAsync(handler3, recF, newMessagesRequest("m6"))
		recG := httptest.NewRecorder()
		doneG := serveAsync(handler3, recG, newMessagesRequest("m7"))
		Eventually(inFlight, "1s", "10ms").
			Should(BeNumerically("==", 6), "the removed host must be unlimited — E, F, G admitted at once")

		closeOnce.Do(func() { close(release) })
		for _, done := range []chan struct{}{doneA, doneB, doneC, doneD, doneE, doneF, doneG} {
			Eventually(done, "1s").Should(BeClosed())
		}
		for _, rec := range []*httptest.ResponseRecorder{recA, recB, recC, recD, recE, recF, recG} {
			Expect(rec.Code).To(Equal(http.StatusOK))
		}
	})
})

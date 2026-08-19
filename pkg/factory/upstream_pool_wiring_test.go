// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory_test

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/factory"
	"github.com/bborbe/claude-code-router/pkg/handler"
)

// poolUpstream is one blocking test upstream in the pool wiring harness: an
// httptest server that records the in-flight count and the headers of every
// request it receives, then blocks until released or the request context is
// cancelled. A row's config points pool members at these servers and asserts
// on the per-server counters.
type poolUpstream struct {
	srv         *httptest.Server
	url         string
	inFlight    int32
	requests    int32
	release     chan struct{}
	releaseOnce sync.Once
	hdrs        http.Header
	hdrsMu      sync.Mutex
}

func newPoolUpstream() *poolUpstream {
	u := &poolUpstream{release: make(chan struct{})}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&u.requests, 1)
		atomic.AddInt32(&u.inFlight, 1)
		defer atomic.AddInt32(&u.inFlight, -1)
		u.hdrsMu.Lock()
		u.hdrs = r.Header.Clone()
		u.hdrsMu.Unlock()
		// Read the body before blocking: with an unread body the net/http
		// server never notices a client disconnect and a cancelled probe
		// request would leave this handler (and its in-flight counter) stuck.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-u.release:
		case <-r.Context().Done():
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	u.url = u.srv.URL
	return u
}

// count returns how many requests this server has received in total.
func (u *poolUpstream) count() int32 {
	return atomic.LoadInt32(&u.requests)
}

// unblock closes the release channel so every request blocked on this server
// completes. Idempotent — safe to call from a row body and again from
// AfterEach.
func (u *poolUpstream) unblock() {
	u.releaseOnce.Do(func() { close(u.release) })
}

// closeUpstream unblocks then closes the server so AfterEach never hangs on
// a request still blocked in the handler.
func (u *poolUpstream) closeUpstream() {
	u.unblock()
	u.srv.Close()
}

var _ = Describe("CreateRouterFromConfig upstream pool wiring", func() {
	var (
		a *poolUpstream
		b *poolUpstream
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
	serveAsync := func(
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

	// makePoolConfig builds a single-provider config whose provider routes
	// to the given upstreams.
	makePoolConfig := func(upstreams []pkg.Upstream) *pkg.Config {
		return &pkg.Config{
			Router: pkg.Router{DefaultProvider: "pool"},
			Providers: map[string]pkg.Provider{
				"pool": {Models: []string{"m*"}, Upstreams: upstreams},
			},
		}
	}

	buildRouter := func(cfg *pkg.Config) http.Handler {
		h, err := factory.CreateRouterFromConfig(context.Background(), cfg, isolatedRegistry())
		Expect(err).NotTo(HaveOccurred())
		return h
	}

	// sessionedRequest returns a /v1/messages request with the given model
	// and a session id injected directly into its context. The session
	// middleware is not run for these (its own behavior is covered in the
	// handler package); the upstream pool handler reads the id from context
	// to pin the request.
	sessionedRequest := func(id, model string) *http.Request {
		req := newMessagesRequest(model)
		return req.WithContext(handler.ContextWithSessionID(req.Context(), id))
	}

	// probePinnedTo returns a session id that the pool's weighted ring hash
	// pins to target. It fires sessioned probe requests through router and
	// observes which member's in-flight counter rose, cancelling each probe's
	// context so the probe completes without consuming a release channel and
	// leaves no slot or counter held for the row's real assertions.
	probePinnedTo := func(router http.Handler, target, other *poolUpstream, model string) string {
		for i := 0; i < 200; i++ {
			id := fmt.Sprintf("probe-%d", i)
			baseTarget := atomic.LoadInt32(&target.inFlight)
			baseOther := atomic.LoadInt32(&other.inFlight)
			ctx, cancel := context.WithCancel(context.Background())
			req := newMessagesRequest(model).WithContext(
				handler.ContextWithSessionID(ctx, id),
			)
			rec := httptest.NewRecorder()
			done := serveAsync(router, rec, req)
			var landed *poolUpstream
			Eventually(func() string {
				switch {
				case atomic.LoadInt32(&target.inFlight) > baseTarget:
					landed = target
					return "target"
				case atomic.LoadInt32(&other.inFlight) > baseOther:
					landed = other
					return "other"
				default:
					return ""
				}
			}, "1s", "10ms").ShouldNot(BeEmpty(), "probe request must land on a member")
			cancel()
			Eventually(
				done,
				"1s",
			).Should(BeClosed(), "probe request must complete after cancellation")
			base := baseOther
			if landed == target {
				base = baseTarget
			}
			Eventually(func() int32 { return atomic.LoadInt32(&landed.inFlight) }, "1s", "10ms").
				Should(BeNumerically("==", base), "probe request must not leave a slot held")
			if landed == target {
				return id
			}
		}
		Fail("no probe session id pinned to the target member")
		return ""
	}

	BeforeEach(func() {
		a = newPoolUpstream()
		b = newPoolUpstream()
	})

	AfterEach(func() {
		a.closeUpstream()
		b.closeUpstream()
	})

	It(
		"AC 5: a capped member holds at most its cap; the next pinned request is 429'd through the real dispatch path",
		func() {
			cfg := makePoolConfig([]pkg.Upstream{
				{Upstream: a.url, Weight: 1, MaxConcurrentRequests: 1, MaxConcurrentWaitSeconds: 1},
				{Upstream: b.url, Weight: 1, MaxConcurrentRequests: 1, MaxConcurrentWaitSeconds: 1},
			})
			router := buildRouter(cfg)

			// Pick a session id the pool pins to member A.
			idA := probePinnedTo(router, a, b, "m1")
			Expect(idA).NotTo(BeEmpty())

			// Request 1 holds A's single slot.
			rec1 := httptest.NewRecorder()
			done1 := serveAsync(router, rec1, sessionedRequest(idA, "m1"))
			Eventually(func() int32 { return atomic.LoadInt32(&a.inFlight) }, "1s", "10ms").
				Should(BeNumerically("==", 1), "request 1 must be in flight on member A")
			Expect(
				atomic.LoadInt32(&b.inFlight),
			).To(BeNumerically("==", 0), "member B must stay idle")

			// Request 2, same session → pinned to A whose slot is held → 429 after
			// the ~1s wait. B is idle, so the cap is provably A's own, not a shared
			// pool cap.
			rec2 := httptest.NewRecorder()
			router.ServeHTTP(rec2, sessionedRequest(idA, "m2"))
			Expect(rec2.Code).To(Equal(http.StatusTooManyRequests))
			Expect(rec2.Body.String()).To(ContainSubstring("rate_limit_error"))
			Expect(
				atomic.LoadInt32(&a.inFlight),
			).To(BeNumerically("==", 1), "A's in-flight must not change")
			Expect(
				atomic.LoadInt32(&b.inFlight),
			).To(BeNumerically("==", 0), "B never saw request 2")

			a.unblock()
			Eventually(done1, "1s").Should(BeClosed())
			Expect(rec1.Code).To(Equal(http.StatusOK))
		},
	)

	It(
		"AC 3 full-path: keyless dispatch goes to the least-loaded member, never the first-declared",
		func() {
			cfg := makePoolConfig([]pkg.Upstream{
				{Upstream: a.url, Weight: 1, MaxConcurrentRequests: 1, MaxConcurrentWaitSeconds: 1},
				{Upstream: b.url, Weight: 1, MaxConcurrentRequests: 1, MaxConcurrentWaitSeconds: 1},
			})
			router := buildRouter(cfg)

			// Pick a session id pinned to A (the first-declared member), then
			// saturate A with a blocking sessioned request.
			idA := probePinnedTo(router, a, b, "m1")
			Expect(idA).NotTo(BeEmpty())

			recA := httptest.NewRecorder()
			doneA := serveAsync(router, recA, sessionedRequest(idA, "m1"))
			Eventually(func() int32 { return atomic.LoadInt32(&a.inFlight) }, "1s", "10ms").
				Should(BeNumerically("==", 1), "sessioned request must pin to A and saturate it")
			Expect(atomic.LoadInt32(&b.inFlight)).To(BeNumerically("==", 0))

			// Capture the [route] log line at V(2) so the keyless dispatch's
			// chosen upstream is provable. Save and restore the process-global
			// glog flags.
			oldV := flag.Lookup("v").Value.String()
			oldLogToStderr := flag.Lookup("logtostderr").Value.String()
			defer func() {
				Expect(flag.Set("v", oldV)).To(Succeed())
				Expect(flag.Set("logtostderr", oldLogToStderr)).To(Succeed())
			}()
			Expect(flag.Set("v", "2")).To(Succeed())
			Expect(flag.Set("logtostderr", "true")).To(Succeed())

			// Fire the keyless request in the background and hold the stderr
			// capture window open until B's counter rises — the [route] line is
			// written before the chosen member's upstream blocks, so a risen B
			// counter proves the line landed inside the window.
			recK := httptest.NewRecorder()
			var keylessDone chan struct{}
			out := captureStderr(func() {
				keylessDone = serveAsync(router, recK, newMessagesRequest("m1"))
				Eventually(func() int32 { return atomic.LoadInt32(&b.inFlight) }, "1s", "10ms").
					Should(BeNumerically("==", 1), "keyless request must be served by the least-loaded member B")
			})
			Expect(atomic.LoadInt32(&a.inFlight)).To(BeNumerically("==", 1), "A stays saturated")
			Expect(out).To(ContainSubstring("[route] session= upstream=" + b.url))

			a.unblock()
			b.unblock()
			Eventually(doneA, "1s").Should(BeClosed())
			Eventually(keylessDone, "1s").Should(BeClosed())
			Expect(recA.Code).To(Equal(http.StatusOK))
			Expect(recK.Code).To(Equal(http.StatusOK))
		},
	)

	It("AC 2 full-path: a session id stays on one member across requests", func() {
		cfg := makePoolConfig([]pkg.Upstream{
			{Upstream: a.url, Weight: 1, MaxConcurrentRequests: 3, MaxConcurrentWaitSeconds: 5},
			{Upstream: b.url, Weight: 1, MaxConcurrentRequests: 3, MaxConcurrentWaitSeconds: 5},
		})
		router := buildRouter(cfg)

		// Probe both pin ids while both members are idle, then saturate.
		idB := probePinnedTo(router, b, a, "m1")
		Expect(idB).NotTo(BeEmpty())
		idA := probePinnedTo(router, a, b, "m1")
		Expect(idA).NotTo(BeEmpty())
		Expect(idA).NotTo(Equal(idB))

		// Three concurrent requests for session idA all pin to the same member.
		recsS := make([]*httptest.ResponseRecorder, 3)
		donesS := make([]chan struct{}, 3)
		for i := range recsS {
			recsS[i] = httptest.NewRecorder()
			donesS[i] = serveAsync(router, recsS[i], sessionedRequest(idA, "m1"))
		}
		Eventually(func() int32 { return atomic.LoadInt32(&a.inFlight) }, "1s", "10ms").
			Should(BeNumerically("==", 3), "all three sessioned requests must land on member A")
		Expect(atomic.LoadInt32(&b.inFlight)).
			To(BeNumerically("==", 0), "member B must see none of session A")

		// Three concurrent requests for session idB all pin to the other member.
		recsT := make([]*httptest.ResponseRecorder, 3)
		donesT := make([]chan struct{}, 3)
		for i := range recsT {
			recsT[i] = httptest.NewRecorder()
			donesT[i] = serveAsync(router, recsT[i], sessionedRequest(idB, "m1"))
		}
		Eventually(func() int32 { return atomic.LoadInt32(&b.inFlight) }, "1s", "10ms").
			Should(BeNumerically("==", 3), "all three sessioned requests must land on member B")
		Expect(atomic.LoadInt32(&a.inFlight)).
			To(BeNumerically("==", 3), "member A keeps session A's three")

		a.unblock()
		b.unblock()
		for _, done := range append(donesS, donesT...) {
			Eventually(done, "1s").Should(BeClosed())
		}
		for _, rec := range append(recsS, recsT...) {
			Expect(rec.Code).To(Equal(http.StatusOK))
		}
	})

	It("AC 5: the legacy single-upstream form wires as a one-member pool", func() {
		cfg := &pkg.Config{
			Router: pkg.Router{DefaultProvider: "legacy"},
			Providers: map[string]pkg.Provider{
				"legacy": {
					Upstream:              a.url,
					Models:                []string{"m*"},
					MaxConcurrentRequests: 1,
				},
			},
		}
		router := buildRouter(cfg)

		// A sessioned and a keyless request both reach the single member; B is
		// not part of this pool.
		recS := httptest.NewRecorder()
		doneS := serveAsync(router, recS, sessionedRequest("legacy-session", "m1"))
		Eventually(func() int32 { return atomic.LoadInt32(&a.inFlight) }, "1s", "10ms").
			Should(BeNumerically("==", 1), "sessioned request must reach the single member")
		Expect(
			atomic.LoadInt32(&b.inFlight),
		).To(BeNumerically("==", 0), "B is not part of this pool")

		a.unblock()
		Eventually(doneS, "1s").Should(BeClosed())
		Expect(recS.Code).To(Equal(http.StatusOK))

		recK := httptest.NewRecorder()
		router.ServeHTTP(recK, newMessagesRequest("m2"))
		Expect(recK.Code).To(Equal(http.StatusOK))
		Expect(a.count()).To(BeNumerically("==", 2), "both requests must reach serverA")
		Expect(b.count()).To(BeNumerically("==", 0), "B must never be reached")
	})

	It("rebuilds the pool tree — a rebuilt pool enforces the new cap", func() {
		// handler1: one-member pool (server A), cap 1.
		cfg1 := &pkg.Config{
			Router: pkg.Router{DefaultProvider: "pool"},
			Providers: map[string]pkg.Provider{
				"pool": {
					Models: []string{"m*"},
					Upstreams: []pkg.Upstream{
						{
							Upstream:                 a.url,
							Weight:                   1,
							MaxConcurrentRequests:    1,
							MaxConcurrentWaitSeconds: 5,
						},
					},
				},
			},
		}
		handler1 := buildRouter(cfg1)

		// A single-member pool pins every session to member A (the only slot).
		recA := httptest.NewRecorder()
		doneA := serveAsync(handler1, recA, sessionedRequest("one-member", "m1"))
		Eventually(func() int32 { return atomic.LoadInt32(&a.inFlight) }, "1s", "10ms").
			Should(BeNumerically("==", 1), "request A must be in flight on the single member")

		// Request B, same session → same member, cap 1 → stays queued.
		recB := httptest.NewRecorder()
		doneB := serveAsync(handler1, recB, sessionedRequest("one-member", "m2"))
		Consistently(func() int32 { return atomic.LoadInt32(&a.inFlight) }, "300ms", "30ms").
			Should(BeNumerically("==", 1), "handler1 caps at 1: request B must not reach the upstream")

		// handler2: rebuilt two-member pool (A + B), each cap 2 — a fresh
		// CreateRouterFromConfig exactly mirrors the reloader's SIGHUP rebuild.
		cfg2 := &pkg.Config{
			Router: pkg.Router{DefaultProvider: "pool"},
			Providers: map[string]pkg.Provider{
				"pool": {
					Models: []string{"m*"},
					Upstreams: []pkg.Upstream{
						{
							Upstream:                 a.url,
							Weight:                   1,
							MaxConcurrentRequests:    2,
							MaxConcurrentWaitSeconds: 5,
						},
						{
							Upstream:                 b.url,
							Weight:                   1,
							MaxConcurrentRequests:    2,
							MaxConcurrentWaitSeconds: 5,
						},
					},
				},
			},
		}
		handler2 := buildRouter(cfg2)

		// Probe a session id pinned to member A of the rebuilt two-member pool
		// (the ring changed, so the old one-member id need not pin to A here).
		idA2 := probePinnedTo(handler2, a, b, "m1")
		Expect(idA2).NotTo(BeEmpty())

		recC := httptest.NewRecorder()
		doneC := serveAsync(handler2, recC, sessionedRequest(idA2, "m3"))
		recD := httptest.NewRecorder()
		doneD := serveAsync(handler2, recD, sessionedRequest(idA2, "m4"))
		Eventually(func() int32 { return atomic.LoadInt32(&a.inFlight) }, "1s", "10ms").
			Should(
				BeNumerically("==", 3),
				"rebuilt member A holds A (handler1) + C + D — the new cap of 2 is enforced",
			)

		a.unblock()
		b.unblock()
		Eventually(doneA, "1s").Should(BeClosed())
		Eventually(doneB, "1s").Should(BeClosed())
		Eventually(doneC, "1s").Should(BeClosed())
		Eventually(doneD, "1s").Should(BeClosed())
		for _, rec := range []*httptest.ResponseRecorder{recA, recB, recC, recD} {
			Expect(rec.Code).To(Equal(http.StatusOK))
		}
	})

	It(
		"strips x-session-id outbound through the full mux; the pinned member never sees it",
		func() {
			cfg := makePoolConfig([]pkg.Upstream{
				{Upstream: a.url, Weight: 1, MaxConcurrentRequests: 2, MaxConcurrentWaitSeconds: 5},
				{Upstream: b.url, Weight: 1, MaxConcurrentRequests: 2, MaxConcurrentWaitSeconds: 5},
			})
			router := buildRouter(cfg)

			// Probe a session id pinned to A — the real header value is the same
			// id the ring would pin on.
			idA := probePinnedTo(router, a, b, "m1")
			Expect(idA).NotTo(BeEmpty())

			// Through the full mux: the session middleware reads X-Session-Id,
			// strips it, and carries it on the context, so the pool pins to A and
			// the server that receives the request observes no X-Session-Id header.
			beforeA := a.count()
			beforeB := b.count()
			req := newMessagesRequest("m1")
			req.Header.Set("X-Session-Id", idA)
			rec := httptest.NewRecorder()
			done := serveAsync(router, rec, req)
			Eventually(func() int32 { return atomic.LoadInt32(&a.inFlight) }, "1s", "10ms").
				Should(BeNumerically("==", 1), "the header-carrying request must pin to member A")

			a.hdrsMu.Lock()
			hdrs := a.hdrs.Clone()
			a.hdrsMu.Unlock()
			Expect(lowerCaseKeys(hdrs)).NotTo(HaveKey("x-session-id"))

			a.unblock()
			Eventually(done, "1s").Should(BeClosed())
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(
				a.count(),
			).To(BeNumerically("==", beforeA+1), "the pinned member must serve the request")
			Expect(b.count()).To(BeNumerically("==", beforeB), "the other member must not see it")
		},
	)
})

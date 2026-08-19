// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// countingHandler records how many times it is invoked so tests can
// assert which member of a pool served a batch of requests.
type countingHandler struct {
	mu    sync.Mutex
	count int
}

func (c *countingHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	c.mu.Lock()
	c.count++
	c.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (c *countingHandler) invocations() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

var _ = Describe("UpstreamPoolHandler", func() {
	var (
		a, b     *countingHandler
		balanced []handler.UpstreamMember
		pool     http.Handler
	)

	BeforeEach(func() {
		a, b = &countingHandler{}, &countingHandler{}
		balanced = []handler.UpstreamMember{
			{Upstream: "https://a", Handler: a, Weight: 1},
			{Upstream: "https://b", Handler: b, Weight: 1},
		}
		pool = handler.NewUpstreamPoolHandler(balanced)
	})

	// pinnedRequest builds a request with sessionID injected into its
	// context via the session-middleware seam — the middleware itself is
	// not run in these unit tests (same seam as postWithKey in the model
	// router key-routing specs).
	pinnedRequest := func(sessionID string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		return req.WithContext(handler.ContextWithSessionID(req.Context(), sessionID))
	}

	It(
		"AC 2: pins a session id to the same member across requests and across handler instances",
		func() {
			labelA := labelHandler("a")
			labelB := labelHandler("b")
			members := []handler.UpstreamMember{
				{Upstream: "https://a", Handler: labelA, Weight: 1},
				{Upstream: "https://b", Handler: labelB, Weight: 1},
			}
			// A second, independent NewUpstreamPoolHandler over identical
			// members must pick the same member — proving the selection is
			// recomputable from the id, with no in-memory session→member map.
			pool := handler.NewUpstreamPoolHandler(members)
			pool2 := handler.NewUpstreamPoolHandler(members)

			serve := func(p http.Handler) string {
				rec := httptest.NewRecorder()
				p.ServeHTTP(rec, pinnedRequest("sess-1"))
				return rec.Body.String()
			}

			first := serve(pool)
			Expect(first).To(Or(Equal("a"), Equal("b")))
			for i := 0; i < 5; i++ {
				Expect(serve(pool)).To(Equal(first))
				Expect(serve(pool2)).To(Equal(first))
			}
		},
	)

	It(
		"AC 2: two distinct session ids land on different members, each stable across requests",
		func() {
			labelA := labelHandler("a")
			labelB := labelHandler("b")
			members := []handler.UpstreamMember{
				{Upstream: "https://a", Handler: labelA, Weight: 1},
				{Upstream: "https://b", Handler: labelB, Weight: 1},
			}
			pool := handler.NewUpstreamPoolHandler(members)

			serve := func(sessionID string) string {
				rec := httptest.NewRecorder()
				pool.ServeHTTP(rec, pinnedRequest(sessionID))
				return rec.Body.String()
			}

			// Probe candidate ids for two that map to different members rather
			// than hard-coding the FNV outcome.
			var idA, idB, memberA, memberB string
			for i := 0; i < 10; i++ {
				id := fmt.Sprintf("sess-%d", i)
				member := serve(id)
				if idA == "" {
					idA, memberA = id, member
					continue
				}
				if member != memberA {
					idB, memberB = id, member
					break
				}
			}
			Expect(idB).NotTo(BeEmpty(), "expected two candidate ids to map to different members")
			Expect(memberA).NotTo(Equal(memberB))

			for i := 0; i < 5; i++ {
				Expect(serve(idA)).To(Equal(memberA))
				Expect(serve(idB)).To(Equal(memberB))
			}
		},
	)

	It(
		"AC 4: the weighted ring hash spreads pinned sessions proportionally to the weights",
		func() {
			weighted := []handler.UpstreamMember{
				{Upstream: "https://a", Handler: a, Weight: 2},
				{Upstream: "https://b", Handler: b, Weight: 1},
			}
			pool = handler.NewUpstreamPoolHandler(weighted)

			for i := 0; i < 100; i++ {
				rec := httptest.NewRecorder()
				pool.ServeHTTP(rec, pinnedRequest(fmt.Sprintf("session-%d", i)))
			}
			// Verified: FNV-1a 64 over "session-0".."session-99" mod 3 gives
			// 66/34, so the heavier member ("https://a") clears the AC 4
			// threshold comfortably.
			Expect(a.invocations()).To(BeNumerically(">=", 55))
			Expect(a.invocations() + b.invocations()).To(Equal(100))
		},
	)

	It(
		"AC 3: a keyless request goes to the least-loaded member, never the first-declared one",
		func() {
			pool = handler.NewUpstreamPoolHandler([]handler.UpstreamMember{
				{Upstream: "https://a", Handler: a, Weight: 1, InFlight: func() int { return 1 }},
				{Upstream: "https://b", Handler: b, Weight: 1, InFlight: func() int { return 0 }},
			})
			rec := httptest.NewRecorder()
			pool.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
			Expect(a.invocations()).To(Equal(0))
			Expect(b.invocations()).To(Equal(1))
		},
	)

	It("AC 3: a member with nil InFlight is treated as load 0 and never panics", func() {
		pool = handler.NewUpstreamPoolHandler([]handler.UpstreamMember{
			{Upstream: "https://a", Handler: a, Weight: 1, InFlight: nil},
			{Upstream: "https://b", Handler: b, Weight: 1, InFlight: func() int { return 5 }},
		})
		rec := httptest.NewRecorder()
		pool.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		Expect(a.invocations()).To(Equal(1))
		Expect(b.invocations()).To(Equal(0))
	})

	It(
		"AC 3: idle keyless requests spread across members instead of stacking on the first",
		func() {
			pool = handler.NewUpstreamPoolHandler([]handler.UpstreamMember{
				{Upstream: "https://a", Handler: a, Weight: 1, InFlight: func() int { return 0 }},
				{Upstream: "https://b", Handler: b, Weight: 1, InFlight: func() int { return 0 }},
			})
			_ = flag.Set("logtostderr", "true")
			_ = flag.Set("v", "2")
			out := captureStderr(func() {
				for i := 0; i < 8; i++ {
					rec := httptest.NewRecorder()
					pool.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
				}
			})
			// Equally-loaded members share keyless traffic round-robin.
			Expect(a.invocations()).To(Equal(4))
			Expect(b.invocations()).To(Equal(4))
			// The [route] lines name both upstreams — spread, not stacking.
			Expect(strings.Count(out, "upstream=https://a")).To(Equal(4))
			Expect(strings.Count(out, "upstream=https://b")).To(Equal(4))
		},
	)

	It("[route] session= log line names the chosen member for pinned and keyless requests", func() {
		// sess-0 pins to member a under FNV-1a 64 mod 2; member a is loaded,
		// so a keyless request deterministically goes to the idle member b.
		pool = handler.NewUpstreamPoolHandler([]handler.UpstreamMember{
			{Upstream: "https://a", Handler: a, Weight: 1, InFlight: func() int { return 1 }},
			{Upstream: "https://b", Handler: b, Weight: 1, InFlight: func() int { return 0 }},
		})
		_ = flag.Set("logtostderr", "true")
		_ = flag.Set("v", "2")

		rec := httptest.NewRecorder()
		out := captureStderr(func() {
			pool.ServeHTTP(rec, pinnedRequest("sess-0"))
		})
		Expect(out).To(ContainSubstring("[route] session=sess-0 upstream=https://a"))

		rec = httptest.NewRecorder()
		out = captureStderr(func() {
			pool.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		})
		Expect(out).To(ContainSubstring("[route] session= upstream=https://b"))
	})

	It("serves both pinned and keyless requests through a single-member pool", func() {
		only := &countingHandler{}
		pool = handler.NewUpstreamPoolHandler([]handler.UpstreamMember{
			{Upstream: "https://only", Handler: only, Weight: 1},
		})
		rec := httptest.NewRecorder()
		pool.ServeHTTP(rec, pinnedRequest("sess-1"))
		rec = httptest.NewRecorder()
		pool.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		Expect(only.invocations()).To(Equal(2))
	})
})

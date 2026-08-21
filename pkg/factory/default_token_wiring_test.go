// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Effective-token wiring specs (spec 015): the factory resolves each
// upstream member's outbound bearer at WIRING time — the member's own
// token: wins (for legacy single-upstream configs normalizeUpstreams /
// UpstreamList already copied the provider-level token onto the member),
// else the top-level default_token:, else empty, where an empty
// effective token keeps the auth-swap no-op contract and the client's
// Authorization passes through byte-for-byte. The auth-swap transport
// sits OUTSIDE the logging roundtripper so the V(3) [upstream.headers]
// line reflects the SWAPPED outbound Authorization, redacted to
// <redacted len=N> — the operator evidence that never leaks the key.

package factory_test

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/factory"
)

var _ = Describe("CreateRouterFromConfig default_token wiring", func() {
	var (
		a *poolUpstream
		b *poolUpstream
	)

	buildRouter := func(cfg *pkg.Config) http.Handler {
		h, err := factory.CreateRouterFromConfig(context.Background(), cfg, isolatedRegistry())
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

	It("AC 2: a provider without its own token inherits the global default_token", func() {
		cfg := &pkg.Config{
			Router:       pkg.Router{DefaultProvider: "inherit"},
			DefaultToken: "global-key-123",
			Providers: map[string]pkg.Provider{
				"inherit": {
					Models:    []string{"m*"},
					Upstreams: []pkg.Upstream{{Upstream: a.url, Weight: 1}},
				},
			},
		}
		router := buildRouter(cfg)

		rec := httptest.NewRecorder()
		done := serveAsync(router, rec, newMessagesRequest("m1"))
		Eventually(func() int32 { return atomic.LoadInt32(&a.inFlight) }, "1s", "10ms").
			Should(BeNumerically("==", 1), "the inherit request must reach the upstream")

		a.hdrsMu.Lock()
		hdrs := a.hdrs.Clone()
		a.hdrsMu.Unlock()
		Expect(hdrs.Get("Authorization")).To(Equal("Bearer global-key-123"))

		a.unblock()
		Eventually(done, "1s").Should(BeClosed())
		Expect(rec.Code).To(Equal(http.StatusOK))
	})

	It(
		"AC 2 V(3): the [upstream.headers] line shows <redacted len=N> and never the literal key",
		func() {
			const globalKey = "global-key-123"
			cfg := &pkg.Config{
				Router:       pkg.Router{DefaultProvider: "inherit"},
				DefaultToken: globalKey,
				Providers: map[string]pkg.Provider{
					"inherit": {
						Models:    []string{"m*"},
						Upstreams: []pkg.Upstream{{Upstream: a.url, Weight: 1}},
					},
				},
			}
			router := buildRouter(cfg)

			oldV := flag.Lookup("v").Value.String()
			oldLogToStderr := flag.Lookup("logtostderr").Value.String()
			defer func() {
				Expect(flag.Set("v", oldV)).To(Succeed())
				Expect(flag.Set("logtostderr", oldLogToStderr)).To(Succeed())
			}()
			Expect(flag.Set("v", "3")).To(Succeed())
			Expect(flag.Set("logtostderr", "true")).To(Succeed())

			// Dispatch async and hold the stderr capture window open until A's
			// in-flight counter rises — the [upstream.headers] line is emitted
			// before the upstream blocks, so a risen counter proves the line landed
			// inside the capture window.
			rec := httptest.NewRecorder()
			var done chan struct{}
			out := captureStderr(func() {
				done = serveAsync(router, rec, newMessagesRequest("m1"))
				Eventually(func() int32 { return atomic.LoadInt32(&a.inFlight) }, "1s", "10ms").
					Should(BeNumerically("==", 1), "the request must reach the upstream")
				a.unblock()
				Eventually(done, "1s").Should(BeClosed())
			})
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(out).To(ContainSubstring("[upstream.headers]"))
			// RedactHeadersForLog replaces the full joined header value (the
			// "Bearer " prefix included) with <redacted len=N>; encoding/json sorts
			// the map keys so Authorization precedes Content-Type.
			Expect(out).To(ContainSubstring(
				fmt.Sprintf("Authorization\":\"<redacted len=%d>", len("Bearer "+globalKey)),
			))
			Expect(out).NotTo(ContainSubstring(globalKey))
		},
	)

	It("AC 3: a provider's own token overrides the global default_token", func() {
		cfg := &pkg.Config{
			Router:       pkg.Router{DefaultProvider: "override"},
			DefaultToken: "global-key-123",
			Providers: map[string]pkg.Provider{
				"override": {
					Models: []string{"m*"},
					Upstreams: []pkg.Upstream{{
						Upstream: a.url,
						Weight:   1,
						Token:    "override-key-456",
					}},
				},
			},
		}
		router := buildRouter(cfg)

		rec := httptest.NewRecorder()
		done := serveAsync(router, rec, newMessagesRequest("m1"))
		Eventually(func() int32 { return atomic.LoadInt32(&a.inFlight) }, "1s", "10ms").
			Should(BeNumerically("==", 1), "the override request must reach the upstream")

		a.hdrsMu.Lock()
		hdrs := a.hdrs.Clone()
		a.hdrsMu.Unlock()
		Expect(hdrs.Get("Authorization")).To(Equal("Bearer override-key-456"))
		Expect(hdrs.Get("Authorization")).NotTo(Equal("Bearer global-key-123"))

		a.unblock()
		Eventually(done, "1s").Should(BeClosed())
		Expect(rec.Code).To(Equal(http.StatusOK))
	})

	It(
		"AC 4: with neither a member token nor a global default, the client Authorization passes through unchanged",
		func() {
			cfg := &pkg.Config{
				Router: pkg.Router{DefaultProvider: "passthrough"},
				Providers: map[string]pkg.Provider{
					"passthrough": {
						Models:    []string{"m*"},
						Upstreams: []pkg.Upstream{{Upstream: a.url, Weight: 1}},
					},
				},
			}
			router := buildRouter(cfg)

			// No allowedApiKeys configured, so the client Authorization is not a
			// routing key and flows to the proxy; the auth-swap transport takes its
			// no-op branch and the header reaches the upstream byte-for-byte.
			req := newMessagesRequest("m1")
			req.Header.Set("Authorization", "Bearer client-oauth-abc")
			rec := httptest.NewRecorder()
			done := serveAsync(router, rec, req)
			Eventually(func() int32 { return atomic.LoadInt32(&a.inFlight) }, "1s", "10ms").
				Should(BeNumerically("==", 1), "the passthrough request must reach the upstream")

			a.hdrsMu.Lock()
			hdrs := a.hdrs.Clone()
			a.hdrsMu.Unlock()
			Expect(hdrs.Get("Authorization")).To(Equal("Bearer client-oauth-abc"))

			a.unblock()
			Eventually(done, "1s").Should(BeClosed())
			Expect(rec.Code).To(Equal(http.StatusOK))
		},
	)

	It(
		"AC 5: a token-less pool member inherits the global default_token; a member with a token uses its own",
		func() {
			cfg := &pkg.Config{
				Router:       pkg.Router{DefaultProvider: "pool"},
				DefaultToken: "global-key-123",
				Providers: map[string]pkg.Provider{
					"pool": {
						Models: []string{"m*"},
						Upstreams: []pkg.Upstream{
							{Upstream: a.url, Weight: 1},
							{Upstream: b.url, Weight: 1, Token: "member-b-key"},
						},
					},
				},
			}
			router := buildRouter(cfg)

			// The weighted-ring slot helpers mirror production pinning, so these
			// pin deterministically: idA -> slot 0 (member A), idB -> slot 1 (B).
			idA := sessionPinnedToSlot(0, 1, 1)
			idB := sessionPinnedToSlot(1, 1, 1)
			Expect(idA).NotTo(Equal(idB))

			recA := httptest.NewRecorder()
			doneA := serveAsync(router, recA, sessionedRequest(idA, "m1"))
			Eventually(func() int32 { return atomic.LoadInt32(&a.inFlight) }, "1s", "10ms").
				Should(BeNumerically("==", 1), "session A must pin to member A")
			Expect(
				atomic.LoadInt32(&b.inFlight),
			).To(BeNumerically("==", 0), "session A must never reach member B")

			recB := httptest.NewRecorder()
			doneB := serveAsync(router, recB, sessionedRequest(idB, "m2"))
			Eventually(func() int32 { return atomic.LoadInt32(&b.inFlight) }, "1s", "10ms").
				Should(BeNumerically("==", 1), "session B must pin to member B")
			Expect(
				atomic.LoadInt32(&a.inFlight),
			).To(BeNumerically("==", 1), "member A still holds only its own request")

			a.hdrsMu.Lock()
			hdrsA := a.hdrs.Clone()
			a.hdrsMu.Unlock()
			b.hdrsMu.Lock()
			hdrsB := b.hdrs.Clone()
			b.hdrsMu.Unlock()
			Expect(hdrsA.Get("Authorization")).To(Equal("Bearer global-key-123"),
				"a token-less member inherits the global default")
			Expect(hdrsB.Get("Authorization")).To(Equal("Bearer member-b-key"),
				"a member token overrides the global default")

			a.unblock()
			b.unblock()
			Eventually(doneA, "1s").Should(BeClosed())
			Eventually(doneB, "1s").Should(BeClosed())
			Expect(recA.Code).To(Equal(http.StatusOK))
			Expect(recB.Code).To(Equal(http.StatusOK))
			Expect(a.count()).To(BeNumerically("==", 1), "member A saw only its own pinned request")
			Expect(b.count()).To(BeNumerically("==", 1), "member B saw only its own pinned request")
		},
	)

	It(
		"SIGHUP rebuild forwards a changed default_token — a rebuilt router applies the new global key",
		func() {
			// cfg1: global default_token is token-v1.
			cfg1 := &pkg.Config{
				Router:       pkg.Router{DefaultProvider: "pool"},
				DefaultToken: "token-v1",
				Providers: map[string]pkg.Provider{
					"pool": {
						Models:    []string{"m*"},
						Upstreams: []pkg.Upstream{{Upstream: a.url, Weight: 1}},
					},
				},
			}
			router1 := buildRouter(cfg1)

			rec1 := httptest.NewRecorder()
			done1 := serveAsync(router1, rec1, newMessagesRequest("m1"))
			Eventually(func() int32 { return atomic.LoadInt32(&a.inFlight) }, "1s", "10ms").
				Should(BeNumerically("==", 1), "request 1 must reach the upstream")

			a.hdrsMu.Lock()
			hdrs1 := a.hdrs.Clone()
			a.hdrsMu.Unlock()
			Expect(hdrs1.Get("Authorization")).To(Equal("Bearer token-v1"))

			a.unblock()
			Eventually(done1, "1s").Should(BeClosed())
			Expect(rec1.Code).To(Equal(http.StatusOK))

			// cfg2 — a SECOND CreateRouterFromConfig exactly mirrors the reloader's
			// SIGHUP rebuild — the same token-less provider with the global
			// default_token changed.
			cfg2 := &pkg.Config{
				Router:       pkg.Router{DefaultProvider: "pool"},
				DefaultToken: "token-v2",
				Providers: map[string]pkg.Provider{
					"pool": {
						Models:    []string{"m*"},
						Upstreams: []pkg.Upstream{{Upstream: a.url, Weight: 1}},
					},
				},
			}
			router2 := buildRouter(cfg2)

			// The release channel is already open, so request 2 completes
			// immediately — the harness records headers before responding, so a
			// synchronous dispatch is deterministic: after ServeHTTP returns,
			// a.hdrs holds request 2's headers.
			rec2 := httptest.NewRecorder()
			router2.ServeHTTP(rec2, newMessagesRequest("m1"))
			Expect(rec2.Code).To(Equal(http.StatusOK))

			a.hdrsMu.Lock()
			hdrs2 := a.hdrs.Clone()
			a.hdrsMu.Unlock()
			Expect(hdrs2.Get("Authorization")).To(Equal("Bearer token-v2"),
				"the rebuilt router applies the changed default_token")
		},
	)

	It("Security: captured V(3)/V(4) output never contains the literal default_token", func() {
		const globalKey = "global-key-123"
		cfg := &pkg.Config{
			Router:       pkg.Router{DefaultProvider: "inherit"},
			DefaultToken: globalKey,
			Providers: map[string]pkg.Provider{
				"inherit": {
					Models:    []string{"m*"},
					Upstreams: []pkg.Upstream{{Upstream: a.url, Weight: 1}},
				},
			},
		}
		router := buildRouter(cfg)

		oldV := flag.Lookup("v").Value.String()
		oldLogToStderr := flag.Lookup("logtostderr").Value.String()
		defer func() {
			Expect(flag.Set("v", oldV)).To(Succeed())
			Expect(flag.Set("logtostderr", oldLogToStderr)).To(Succeed())
		}()
		// v=5 also exercises the V(4) body-sample path ([upstream.req.body]),
		// where the body's bearer tokens are redacted too.
		Expect(flag.Set("v", "5")).To(Succeed())
		Expect(flag.Set("logtostderr", "true")).To(Succeed())

		rec := httptest.NewRecorder()
		var done chan struct{}
		out := captureStderr(func() {
			done = serveAsync(router, rec, newMessagesRequest("m1"))
			Eventually(func() int32 { return atomic.LoadInt32(&a.inFlight) }, "1s", "10ms").
				Should(BeNumerically("==", 1), "the request must reach the upstream")
			a.unblock()
			Eventually(done, "1s").Should(BeClosed())
		})
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(out).To(ContainSubstring("[upstream.headers]"))
		Expect(out).To(ContainSubstring("[upstream.req.body]"))
		// The literal key must never appear anywhere in the captured output,
		// and every Authorization occurrence is the redacted placeholder.
		Expect(out).NotTo(ContainSubstring(globalKey))
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "Authorization") {
				Expect(line).To(ContainSubstring(`"Authorization":"<redacted`))
			}
		}
	})
})

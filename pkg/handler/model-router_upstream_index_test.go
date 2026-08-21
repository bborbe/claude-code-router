// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// Spec 016: the [req] V1 line carries the serving upstream-pool member's
// zero-based index on the provider value (provider=<name>/<index>), in
// both the alias and non-alias variants, with /0 always emitted. Kept in
// its own file because model-router_test.go is at the revive
// file-length-limit; the shared harness helpers (labelHandler,
// captureStderr, alwaysSample, testMetrics, testDateTime) live in
// model-router_test.go in this package.
var _ = Describe("ModelRouter upstream pool member index in [req] line", func() {
	var (
		fallback = labelHandler("fallback")
		mux      http.Handler
		rec      *httptest.ResponseRecorder
	)

	BeforeEach(func() {
		_ = flag.Set("logtostderr", "true")
		_ = flag.Set("v", "2")
		rec = httptest.NewRecorder()
	})

	post := func(body string) *http.Request {
		return httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	}
	Context("upstream pool member index in [req] line", func() {
		BeforeEach(func() {
			_ = flag.Set("logtostderr", "true")
			_ = flag.Set("v", "2")
		})

		// poolRoutes builds a ModelRoute whose handler is a REAL
		// upstream pool handler over members, matching deepseek-*.
		// Dispatching through a real NewUpstreamPoolHandler is what
		// exercises the pool handler's own index-publish path (AC 3).
		poolRoutes := func(members []handler.UpstreamMember) []handler.ModelRoute {
			return []handler.ModelRoute{
				{
					Pattern:      "deepseek-*",
					ProviderName: "seibert-pool",
					Handler:      handler.NewUpstreamPoolHandler(context.Background(), members),
				},
			}
		}

		// postWithSession injects a session id into the request via the
		// session-middleware seam, so the upstream pool pins by it.
		postWithSession := func(body, sessionID string) *http.Request {
			req := post(body)
			return req.WithContext(handler.ContextWithSessionID(req.Context(), sessionID))
		}

		It(
			"AC 1: multi-member pool logs provider=<name>/<index> for the serving member (keyless)",
			func() {
				// Member 0 loaded, member 1 idle: least-loaded picks member
				// 1 deterministically -> /1.
				a := labelHandler("a")
				b := labelHandler("b")
				members := []handler.UpstreamMember{
					{
						Upstream: "https://pool-a",
						Handler:  a,
						Weight:   1,
						InFlight: func() int { return 1 },
					},
					{
						Upstream: "https://pool-b",
						Handler:  b,
						Weight:   1,
						InFlight: func() int { return 0 },
					},
				}
				mux = handler.NewModelRouter(
					poolRoutes(members),
					"default-fallback",
					fallback,
					nil,
					alwaysSample,
					testMetrics,
					testDateTime,
				)
				out := captureStderr(func() {
					mux.ServeHTTP(rec, post(`{"model":"deepseek-v4-flash"}`))
				})
				Expect(rec.Body.String()).To(Equal("b"))
				// /1 = the serving member, correlated with the [route]
				// upstream= line naming that same member.
				Expect(out).To(MatchRegexp(
					`\[req\] POST /v1/messages model=deepseek-v4-flash provider=seibert-pool/1 status=200`,
				))
				Expect(out).To(ContainSubstring("[route] session= upstream=https://pool-b"))
			},
		)

		It("AC 2: alias variant logs provider=<name>/<index>", func() {
			a := labelHandler("a")
			b := labelHandler("b")
			members := []handler.UpstreamMember{
				{
					Upstream: "https://pool-a",
					Handler:  a,
					Weight:   1,
					InFlight: func() int { return 1 },
				},
				{
					Upstream: "https://pool-b",
					Handler:  b,
					Weight:   1,
					InFlight: func() int { return 0 },
				},
			}
			aliases := map[string]string{"ds": "deepseek-v4-flash"}
			mux = handler.NewModelRouter(
				poolRoutes(members),
				"default-fallback",
				fallback,
				aliases,
				alwaysSample,
				testMetrics,
				testDateTime,
			)
			out := captureStderr(func() {
				mux.ServeHTTP(rec, post(`{"model":"ds"}`))
			})
			Expect(rec.Body.String()).To(Equal("b"))
			Expect(out).To(MatchRegexp(
				`\[req\] POST /v1/messages model=ds alias=deepseek-v4-flash provider=seibert-pool/1 status=200`,
			))
		})

		It("AC 3: a real one-entry pool logs provider=<name>/0", func() {
			only := labelHandler("only")
			members := []handler.UpstreamMember{
				{Upstream: "https://only", Handler: only, Weight: 1},
			}
			mux = handler.NewModelRouter(
				poolRoutes(members),
				"default-fallback",
				fallback,
				nil,
				alwaysSample,
				testMetrics,
				testDateTime,
			)
			out := captureStderr(func() {
				mux.ServeHTTP(rec, post(`{"model":"deepseek-v4-flash"}`))
			})
			Expect(rec.Body.String()).To(Equal("only"))
			Expect(out).To(MatchRegexp(
				`\[req\] POST /v1/messages model=deepseek-v4-flash provider=seibert-pool/0 status=200`,
			))
		})

		It(
			"AC 4: a pinned session reports the member the ring hash selected (non-zero index)",
			func() {
				a := labelHandler("a")
				b := labelHandler("b")
				members := []handler.UpstreamMember{
					{Upstream: "https://pool-a", Handler: a, Weight: 1},
					{Upstream: "https://pool-b", Handler: b, Weight: 1},
				}
				mux = handler.NewModelRouter(
					poolRoutes(members),
					"default-fallback",
					fallback,
					nil,
					alwaysSample,
					testMetrics,
					testDateTime,
				)
				// Probe for a session id the ring hash pins to member 1
				// (index 1), so the assertion defeats a hardcoded /0 fake.
				pinned := ""
				for i := 0; i < 100 && pinned == ""; i++ {
					rec = httptest.NewRecorder()
					id := fmt.Sprintf("pinned-%d", i)
					mux.ServeHTTP(rec, postWithSession(`{"model":"deepseek-v4-flash"}`, id))
					if rec.Body.String() == "b" {
						pinned = id
					}
				}
				Expect(pinned).NotTo(BeEmpty(), "expected a session id pinned to member 1")

				rec = httptest.NewRecorder()
				out := captureStderr(func() {
					mux.ServeHTTP(rec, postWithSession(`{"model":"deepseek-v4-flash"}`, pinned))
				})
				Expect(rec.Body.String()).To(Equal("b"))
				Expect(out).To(MatchRegexp(
					`\[req\] POST /v1/messages model=deepseek-v4-flash provider=seibert-pool/1 status=200`,
				))
				Expect(
					out,
				).To(ContainSubstring("[route] session=" + pinned + " upstream=https://pool-b"))
			},
		)

		It(
			"AC 4 (nested): a model-pool dispatch logs the upstream-pool index, never the model-pool index",
			func() {
				a := labelHandler("a")
				b := labelHandler("b")
				upstream := handler.NewUpstreamPoolHandler(
					context.Background(),
					[]handler.UpstreamMember{
						{
							Upstream: "https://pool-a",
							Handler:  a,
							Weight:   1,
							InFlight: func() int { return 1 },
						},
						{
							Upstream: "https://pool-b",
							Handler:  b,
							Weight:   1,
							InFlight: func() int { return 0 },
						},
					},
				)
				pool := handler.NewModelPool(context.Background(), []handler.ModelPoolMember{
					{
						Provider: "seibert-pool",
						Model:    "deepseek-v4-flash",
						Weight:   1,
						Handler:  upstream,
						InFlight: func() int { return 0 },
					},
				})
				mux = handler.NewModelRouterWithPools(
					[]handler.ModelRoute{
						{Pattern: "deepseek-*", ProviderName: "seibert-pool", Handler: upstream},
					},
					"default-fallback",
					fallback,
					nil,
					map[string]*handler.ModelPool{"coding": pool},
					alwaysSample,
					testMetrics,
					testDateTime,
				)
				out := captureStderr(func() {
					mux.ServeHTTP(rec, post(`{"model":"coding"}`))
				})
				Expect(rec.Body.String()).To(Equal("b"))
				// The model pool resolves "coding" to its single member
				// (model-pool index 0), but the logged index is the
				// upstream-pool member index (1) — never the model-pool index.
				Expect(out).To(MatchRegexp(
					`\[req\] POST /v1/messages model=coding provider=seibert-pool/1 status=200`,
				))
			},
		)

		It("AC 5: [req] line gains only the /N suffix — no upstream= key, no URL", func() {
			only := labelHandler("only")
			members := []handler.UpstreamMember{
				{Upstream: "https://only", Handler: only, Weight: 1},
			}
			mux = handler.NewModelRouter(
				poolRoutes(members),
				"default-fallback",
				fallback,
				nil,
				alwaysSample,
				testMetrics,
				testDateTime,
			)
			out := captureStderr(func() {
				mux.ServeHTTP(rec, post(`{"model":"deepseek-v4-flash"}`))
			})
			reqLine := regexp.MustCompile(`(?m)\[req\][^\n]*`).FindString(out)
			Expect(reqLine).To(MatchRegexp(
				`^\[req\] [^ ]+ [^ ]+ model=[^ ]+ provider=[^ ]+/[0-9]+ status=[0-9]+ latency=[^ ]+ in=[^ ]+ out=[^ ]+$`,
			))
			Expect(reqLine).NotTo(ContainSubstring("upstream="))
			Expect(reqLine).NotTo(ContainSubstring("http://"))
		})
	})
})

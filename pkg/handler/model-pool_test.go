// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// recordingHandler serves like labelHandler but records the request body
// it received, so a row can assert BOTH which member served the request
// AND the exact rewritten body that member's provider would forward.
type recordingHandler struct {
	label string
	mu    sync.Mutex
	body  []byte
}

func newRecordingHandler(label string) *recordingHandler {
	return &recordingHandler{label: label}
}

func (r *recordingHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.body = b
	r.mu.Unlock()
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(r.label))
}

func (r *recordingHandler) lastBody() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.body...)
}

// poolPinSlot returns the member index the weighted ring hash maps
// sessionID to over members, mirroring the production computation (FNV-1a
// 64a mod cumulative weights) so tests pick pinning ids deterministically
// instead of hard-coding hash outcomes.
func poolPinSlot(members []handler.ModelPoolMember, sessionID string) int {
	total := 0
	cumulative := make([]int, len(members))
	for i, m := range members {
		total += m.Weight
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
	return len(members) - 1
}

// sessionPinnedTo returns a candidate session id whose weighted ring slot
// over members equals target.
func sessionPinnedTo(members []handler.ModelPoolMember, target int) string {
	for i := 0; i < 500; i++ {
		id := fmt.Sprintf("pin-%d", i)
		if poolPinSlot(members, id) == target {
			return id
		}
	}
	Fail("no session id pinned to the target member")
	return ""
}

var _ = Describe("ModelPool resolution", func() {
	var (
		fallback = labelHandler("fallback")
		rec      *httptest.ResponseRecorder
	)

	post := func(body string) *http.Request {
		return httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	}

	// postWithSession builds a request like post but injects the session id
	// into the request context via the session-middleware seam — the
	// middleware itself is not run in these unit tests.
	postWithSession := func(sessionID, body string) *http.Request {
		req := post(body)
		return req.WithContext(handler.ContextWithSessionID(req.Context(), sessionID))
	}

	// buildPoolMux constructs the pool-aware router over routes that glob
	// both pool providers, so a non-pool request still routes by glob and a
	// pool-resolved request seeds requiresLeadingSystem from its provider.
	buildPoolMux := func(pools map[string]*handler.ModelPool) http.Handler {
		return handler.NewModelRouterWithPools(
			[]handler.ModelRoute{
				{
					Pattern:      "claude-*",
					ProviderName: "anthropic-subscription",
					Handler:      labelHandler("anthropic"),
				},
				{
					Pattern:      "deepseek-*",
					ProviderName: "deepseek-pool",
					Handler:      labelHandler("deepseek-pool"),
				},
				{
					Pattern:      "MiniMax-*",
					ProviderName: "minimax-pool",
					Handler:      labelHandler("minimax-pool"),
				},
			},
			"default-fallback",
			fallback,
			nil,
			pools,
			alwaysSample,
			testMetrics,
			testDateTime,
		)
	}

	BeforeEach(func() {
		rec = httptest.NewRecorder()
	})

	It("AC 2: resolves a pool name to a member and rewrites the body model field", func() {
		recA := newRecordingHandler("deepseek")
		recB := newRecordingHandler("minimax")
		members := []handler.ModelPoolMember{
			{Provider: "deepseek-pool", Model: "deepseek-v4-flash", Weight: 1, Handler: recA},
			{Provider: "minimax-pool", Model: "MiniMax-2.7", Weight: 1, Handler: recB},
		}
		pools := map[string]*handler.ModelPool{"coding": handler.NewModelPool(members)}
		mux := buildPoolMux(pools)
		_ = flag.Set("logtostderr", "true")
		_ = flag.Set("v", "2")

		// Two distinct session ids the pool pins to DIFFERENT members.
		idA := sessionPinnedTo(members, 0)
		idB := sessionPinnedTo(members, 1)
		Expect(idA).NotTo(Equal(idB))

		// Session pinned to member A: served by A's handler, body model
		// rewritten to A's concrete model.
		out := captureStderr(func() {
			mux.ServeHTTP(rec, postWithSession(idA, `{"model":"coding"}`))
		})
		Expect(rec.Body.String()).To(Equal("deepseek"))
		var seenA map[string]any
		Expect(json.Unmarshal(recA.lastBody(), &seenA)).To(Succeed())
		Expect(seenA["model"]).To(Equal("deepseek-v4-flash"))
		Expect(
			out,
		).To(ContainSubstring("[route] model=coding -> provider=deepseek-pool model=deepseek-v4-flash"))

		// Session pinned to member B: the minimax equivalent.
		rec = httptest.NewRecorder()
		out = captureStderr(func() {
			mux.ServeHTTP(rec, postWithSession(idB, `{"model":"coding"}`))
		})
		Expect(rec.Body.String()).To(Equal("minimax"))
		var seenB map[string]any
		Expect(json.Unmarshal(recB.lastBody(), &seenB)).To(Succeed())
		Expect(seenB["model"]).To(Equal("MiniMax-2.7"))
		Expect(
			out,
		).To(ContainSubstring("[route] model=coding -> provider=minimax-pool model=MiniMax-2.7"))
	})

	It(
		"AC 3: pins a session id to the same member across requests and across router instances",
		func() {
			recA := newRecordingHandler("deepseek")
			recB := newRecordingHandler("minimax")
			members := []handler.ModelPoolMember{
				{Provider: "deepseek-pool", Model: "deepseek-v4-flash", Weight: 1, Handler: recA},
				{Provider: "minimax-pool", Model: "MiniMax-2.7", Weight: 1, Handler: recB},
			}
			// A second, independent router over identical pool members must pick
			// the same member — proving the selection is recomputable from the id,
			// with no in-memory session→member map.
			mux1 := buildPoolMux(
				map[string]*handler.ModelPool{"coding": handler.NewModelPool(members)},
			)
			mux2 := buildPoolMux(
				map[string]*handler.ModelPool{"coding": handler.NewModelPool(members)},
			)

			id := sessionPinnedTo(members, 0)
			serve := func(mux http.Handler) string {
				r := httptest.NewRecorder()
				mux.ServeHTTP(r, postWithSession(id, `{"model":"coding"}`))
				return r.Body.String()
			}
			first := serve(mux1)
			Expect(first).To(Or(Equal("deepseek"), Equal("minimax")))
			for i := 0; i < 5; i++ {
				Expect(serve(mux1)).To(Equal(first))
				Expect(serve(mux2)).To(Equal(first))
			}
		},
	)

	It(
		"AC 5: the weighted ring hash spreads pinned sessions proportionally to the weights",
		func() {
			a := &countingHandler{}
			b := &countingHandler{}
			pools := map[string]*handler.ModelPool{
				"coding": handler.NewModelPool([]handler.ModelPoolMember{
					{Provider: "deepseek-pool", Model: "deepseek-v4-flash", Weight: 2, Handler: a},
					{Provider: "minimax-pool", Model: "MiniMax-2.7", Weight: 1, Handler: b},
				}),
			}
			mux := buildPoolMux(pools)
			for i := 0; i < 100; i++ {
				mux.ServeHTTP(
					rec,
					postWithSession(fmt.Sprintf("session-%d", i), `{"model":"coding"}`),
				)
			}
			// Verified: FNV-1a 64 over "session-0".."session-99" mod 3 gives
			// 66/34, so the heavier member clears the AC 5 threshold comfortably.
			Expect(a.invocations()).To(BeNumerically(">=", 55))
			Expect(a.invocations() + b.invocations()).To(Equal(100))
		},
	)

	It("AC 4: an idless request goes to the least-loaded member, never the first-declared", func() {
		a := &countingHandler{}
		b := &countingHandler{}
		pools := map[string]*handler.ModelPool{
			"coding": handler.NewModelPool([]handler.ModelPoolMember{
				{
					Provider: "deepseek-pool",
					Model:    "deepseek-v4-flash",
					Weight:   1,
					InFlight: func() int { return 1 },
					Handler:  a,
				},
				{
					Provider: "minimax-pool",
					Model:    "MiniMax-2.7",
					Weight:   1,
					InFlight: func() int { return 0 },
					Handler:  b,
				},
			}),
		}
		mux := buildPoolMux(pools)
		mux.ServeHTTP(rec, post(`{"model":"coding"}`))
		Expect(a.invocations()).To(Equal(0))
		Expect(b.invocations()).To(Equal(1))
	})

	It("AC 4: a member with nil InFlight is treated as load 0 and never panics", func() {
		a := &countingHandler{}
		b := &countingHandler{}
		pools := map[string]*handler.ModelPool{
			"coding": handler.NewModelPool([]handler.ModelPoolMember{
				{
					Provider: "deepseek-pool",
					Model:    "deepseek-v4-flash",
					Weight:   1,
					InFlight: nil,
					Handler:  a,
				},
				{
					Provider: "minimax-pool",
					Model:    "MiniMax-2.7",
					Weight:   1,
					InFlight: func() int { return 5 },
					Handler:  b,
				},
			}),
		}
		mux := buildPoolMux(pools)
		mux.ServeHTTP(rec, post(`{"model":"coding"}`))
		Expect(a.invocations()).To(Equal(1))
		Expect(b.invocations()).To(Equal(0))
	})

	It("AC 4: idle idless requests spread across members instead of stacking on the first", func() {
		a := &countingHandler{}
		b := &countingHandler{}
		pools := map[string]*handler.ModelPool{
			"coding": handler.NewModelPool([]handler.ModelPoolMember{
				{
					Provider: "deepseek-pool",
					Model:    "deepseek-v4-flash",
					Weight:   1,
					InFlight: func() int { return 0 },
					Handler:  a,
				},
				{
					Provider: "minimax-pool",
					Model:    "MiniMax-2.7",
					Weight:   1,
					InFlight: func() int { return 0 },
					Handler:  b,
				},
			}),
		}
		mux := buildPoolMux(pools)
		_ = flag.Set("logtostderr", "true")
		_ = flag.Set("v", "2")
		out := captureStderr(func() {
			for i := 0; i < 8; i++ {
				mux.ServeHTTP(rec, post(`{"model":"coding"}`))
			}
		})
		// Equally-loaded members share idless traffic round-robin.
		Expect(a.invocations()).To(Equal(4))
		Expect(b.invocations()).To(Equal(4))
		// The [route] lines name both providers — spread, not stacking.
		re := regexp.MustCompile(`\[route\] model=coding -> provider=([A-Za-z0-9_-]+)`)
		providers := map[string]int{}
		for _, m := range re.FindAllStringSubmatch(out, -1) {
			providers[m[1]]++
		}
		Expect(providers).To(HaveLen(2))
	})

	It(
		"AC 6: a saturated pinned member with overflow: true falls over to the least-loaded sibling",
		func() {
			recA := newRecordingHandler("deepseek")
			recB := newRecordingHandler("minimax")
			members := []handler.ModelPoolMember{
				{
					Provider:  "deepseek-pool",
					Model:     "deepseek-v4-flash",
					Weight:    1,
					Overflow:  true,
					Saturated: func() bool { return true },
					Handler:   recA,
				},
				{Provider: "minimax-pool", Model: "MiniMax-2.7", Weight: 1, Handler: recB},
			}
			pools := map[string]*handler.ModelPool{"coding": handler.NewModelPool(members)}
			mux := buildPoolMux(pools)
			_ = flag.Set("logtostderr", "true")
			_ = flag.Set("v", "2")
			idA := sessionPinnedTo(members, 0)
			out := captureStderr(func() {
				mux.ServeHTTP(rec, postWithSession(idA, `{"model":"coding"}`))
			})
			Expect(rec.Body.String()).To(Equal("minimax"))
			Expect(
				out,
			).To(ContainSubstring("[route] model=coding -> provider=minimax-pool model=MiniMax-2.7"))
		},
	)

	It("DB 4: overflow: false keeps the request on its pinned member despite saturation", func() {
		recA := newRecordingHandler("deepseek")
		recB := newRecordingHandler("minimax")
		members := []handler.ModelPoolMember{
			{
				Provider:  "deepseek-pool",
				Model:     "deepseek-v4-flash",
				Weight:    1,
				Overflow:  false,
				Saturated: func() bool { return true },
				Handler:   recA,
			},
			{Provider: "minimax-pool", Model: "MiniMax-2.7", Weight: 1, Handler: recB},
		}
		pools := map[string]*handler.ModelPool{"coding": handler.NewModelPool(members)}
		mux := buildPoolMux(pools)
		idA := sessionPinnedTo(members, 0)
		mux.ServeHTTP(rec, postWithSession(idA, `{"model":"coding"}`))
		Expect(rec.Body.String()).To(Equal("deepseek"))
	})

	It(
		"single-member pool with overflow: true stays on the only member instead of panicking",
		func() {
			// A one-member pool is valid config; with nowhere to overflow to, the
			// pinned member is served and its own provider semantics apply.
			recA := newRecordingHandler("deepseek")
			members := []handler.ModelPoolMember{
				{
					Provider:  "deepseek-pool",
					Model:     "deepseek-v4-flash",
					Weight:    1,
					Overflow:  true,
					Saturated: func() bool { return true },
					Handler:   recA,
				},
			}
			pools := map[string]*handler.ModelPool{"coding": handler.NewModelPool(members)}
			mux := buildPoolMux(pools)
			idA := sessionPinnedTo(members, 0)
			mux.ServeHTTP(rec, postWithSession(idA, `{"model":"coding"}`))
			Expect(rec.Body.String()).To(Equal("deepseek"))
		},
	)

	It("DB 1: a model that is not a configured pool name falls through to glob routing", func() {
		// The pool table has no "coding" pool, so a request for a model that
		// glob-routes (claude-opus-4-7) is served exactly as today.
		pools := map[string]*handler.ModelPool{
			"other-pool": handler.NewModelPool([]handler.ModelPoolMember{
				{
					Provider: "deepseek-pool",
					Model:    "deepseek-v4-flash",
					Weight:   1,
					Handler:  newRecordingHandler("deepseek"),
				},
			}),
		}
		mux := buildPoolMux(pools)
		_ = flag.Set("logtostderr", "true")
		_ = flag.Set("v", "2")
		out := captureStderr(func() {
			mux.ServeHTTP(rec, post(`{"model":"claude-opus-4-7"}`))
		})
		Expect(rec.Body.String()).To(Equal("anthropic"))
		// The glob [route] line fired; no pool [route] line was emitted.
		Expect(out).To(ContainSubstring(
			`[route] model="claude-opus-4-7" matched "claude-*" -> provider=anthropic-subscription`,
		))
		Expect(out).NotTo(ContainSubstring("[pool]"))
	})
})

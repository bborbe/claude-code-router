// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The model pool YAML contract (the `model_pools:` key, spec 013 prompt 1)
// is parsed into pkg.Config.ModelPools; these specs exercise the factory
// wiring of that table — the runtime members, their load/saturation
// closures, and the rewritten body reaching the real upstream.

package factory_test

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
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

// recordingUpstream is one test upstream in the model-pool wiring harness:
// an httptest server that records the in-flight count and the received
// request body of every request, optionally blocking on a release channel
// until the test unblocks it. A row's config points pool members at these
// servers and asserts on the per-server counters and the rewritten body.
type recordingUpstream struct {
	srv         *httptest.Server
	url         string
	block       bool
	inFlight    int32
	requests    int32
	release     chan struct{}
	releaseOnce sync.Once
	bodyMu      sync.Mutex
	body        []byte
}

func newRecordingUpstream(block bool) *recordingUpstream {
	u := &recordingUpstream{block: block, release: make(chan struct{})}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&u.requests, 1)
		atomic.AddInt32(&u.inFlight, 1)
		defer atomic.AddInt32(&u.inFlight, -1)
		body, _ := io.ReadAll(r.Body)
		u.bodyMu.Lock()
		u.body = body
		u.bodyMu.Unlock()
		if u.block {
			select {
			case <-u.release:
			case <-r.Context().Done():
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	u.url = u.srv.URL
	return u
}

func (u *recordingUpstream) count() int32 {
	return atomic.LoadInt32(&u.requests)
}

func (u *recordingUpstream) recordedModel() string {
	u.bodyMu.Lock()
	body := append([]byte(nil), u.body...)
	u.bodyMu.Unlock()
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	return req.Model
}

// unblock closes the release channel so every request blocked on this
// server completes. Idempotent — safe to call from a row body and again
// from AfterEach.
func (u *recordingUpstream) unblock() {
	u.releaseOnce.Do(func() { close(u.release) })
}

// closeUpstream unblocks then closes the server so AfterEach never hangs
// on a request still blocked in the handler.
func (u *recordingUpstream) closeUpstream() {
	u.unblock()
	u.srv.Close()
}

// poolSlot returns the weighted-ring slot of sessionID over a pool whose
// members have the given weights, mirroring the production computation so
// tests pick pinning ids deterministically instead of probing.
func poolSlot(sessionID string, weights ...int) int {
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

// sessionPinnedToSlot returns a session id whose weighted-ring slot over
// the given weights equals target.
func sessionPinnedToSlot(target int, weights ...int) string {
	for i := 0; i < 500; i++ {
		id := fmt.Sprintf("pin-%d", i)
		if poolSlot(id, weights...) == target {
			return id
		}
	}
	Fail("no session id pinned to the target slot")
	return ""
}

var _ = Describe("CreateRouterFromConfig model pool wiring", func() {
	var (
		a *recordingUpstream
		b *recordingUpstream
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

	// sessionedRequest returns a /v1/messages request with the given model
	// and a session id injected directly into its context. The session
	// middleware is not run for these (its own behavior is covered in the
	// handler package); the model pool reads the id from context to pin.
	sessionedRequest := func(id, model string) *http.Request {
		req := newMessagesRequest(model)
		return req.WithContext(handler.ContextWithSessionID(req.Context(), id))
	}

	BeforeEach(func() {
		a = newRecordingUpstream(false)
		b = newRecordingUpstream(false)
	})

	AfterEach(func() {
		a.closeUpstream()
		b.closeUpstream()
	})

	// twoPoolConfig builds a config whose two providers (deepseek-pool →
	// serverA, minimax-pool → serverB) back the given model pool. Each
	// provider carries a 1-concurrent cap so the saturation closure reads a
	// real limiter occupancy.
	twoPoolConfig := func(poolMembers []pkg.ModelPoolMember) *pkg.Config {
		provider := func(srv *recordingUpstream) pkg.Provider {
			return pkg.Provider{
				Upstreams: []pkg.Upstream{{
					Upstream:                 srv.url,
					Weight:                   1,
					MaxConcurrentRequests:    1,
					MaxConcurrentWaitSeconds: 1,
				}},
				Models: []string{"*"},
			}
		}
		return &pkg.Config{
			Router: pkg.Router{DefaultProvider: "deepseek-pool"},
			Providers: map[string]pkg.Provider{
				"deepseek-pool": provider(a),
				"minimax-pool":  provider(b),
			},
			ModelPools: map[string][]pkg.ModelPoolMember{"coding": poolMembers},
		}
	}

	buildRouter := func(cfg *pkg.Config) http.Handler {
		h, err := factory.CreateRouterFromConfig(context.Background(), cfg, isolatedRegistry())
		Expect(err).NotTo(HaveOccurred())
		return h
	}

	It(
		"AC 7: rebuilds the model pool table on a fresh config — a rebuilt pool resolves the new member",
		func() {
			// router1: pool "coding" resolves only deepseek-pool.
			cfg1 := &pkg.Config{
				Router: pkg.Router{DefaultProvider: "deepseek-pool"},
				Providers: map[string]pkg.Provider{
					"deepseek-pool": {
						Upstreams: []pkg.Upstream{{Upstream: a.url, Weight: 1}},
						Models:    []string{"*"},
					},
					"minimax-pool": {
						Upstreams: []pkg.Upstream{{Upstream: b.url, Weight: 1}},
						Models:    []string{"*"},
					},
				},
				ModelPools: map[string][]pkg.ModelPoolMember{
					"coding": {
						{Provider: "deepseek-pool", Model: "deepseek-v4-flash", Weight: 1},
					},
				},
			}
			router1 := buildRouter(cfg1)
			// The one-member pool serves every session through deepseek-pool.
			rec0 := httptest.NewRecorder()
			router1.ServeHTTP(rec0, newMessagesRequest("coding"))
			Expect(rec0.Code).To(Equal(http.StatusOK))
			Expect(a.recordedModel()).To(Equal("deepseek-v4-flash"))

			// router2: a fresh CreateRouterFromConfig exactly mirrors the
			// reloader's SIGHUP rebuild — the pool table now ALSO resolves the
			// new member.
			cfg2 := &pkg.Config{
				Router: pkg.Router{DefaultProvider: "deepseek-pool"},
				Providers: map[string]pkg.Provider{
					"deepseek-pool": {
						Upstreams: []pkg.Upstream{{Upstream: a.url, Weight: 1}},
						Models:    []string{"*"},
					},
					"minimax-pool": {
						Upstreams: []pkg.Upstream{{Upstream: b.url, Weight: 1}},
						Models:    []string{"*"},
					},
				},
				ModelPools: map[string][]pkg.ModelPoolMember{
					"coding": {
						{Provider: "deepseek-pool", Model: "deepseek-v4-flash", Weight: 1},
						{Provider: "minimax-pool", Model: "MiniMax-2.7", Weight: 1},
					},
				},
			}
			router2 := buildRouter(cfg2)

			// In router2's pool totalWeight = 2; a session pinned to slot 1
			// (the NEW member) must be served by minimax-pool -> serverB with
			// the rewritten body model.
			beforeA := a.count()
			id := sessionPinnedToSlot(1, 1, 1)
			rec := httptest.NewRecorder()
			router2.ServeHTTP(rec, sessionedRequest(id, "coding"))
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(b.recordedModel()).To(Equal("MiniMax-2.7"))
			// The pinned session must not reach the old member: serverA's count
			// is unchanged by the router2 request (the router1 request above
			// already served it once).
			Expect(
				a.count(),
			).To(Equal(beforeA), "the rebuilt pool must not route the pinned session to the old member")
		},
	)

	It(
		"resolves and rewrites a pool name at the real dispatch boundary; non-pool models route unchanged",
		func() {
			cfg := &pkg.Config{
				Router: pkg.Router{DefaultProvider: "deepseek-pool"},
				Providers: map[string]pkg.Provider{
					"deepseek-pool": {
						Upstreams: []pkg.Upstream{{Upstream: a.url, Weight: 1}},
						Models:    []string{"*"},
					},
				},
				ModelPools: map[string][]pkg.ModelPoolMember{
					"coding": {
						{Provider: "deepseek-pool", Model: "deepseek-v4-flash", Weight: 1},
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
			Expect(flag.Set("v", "2")).To(Succeed())
			Expect(flag.Set("logtostderr", "true")).To(Succeed())

			rec := httptest.NewRecorder()
			out := captureStderr(func() {
				router.ServeHTTP(rec, newMessagesRequest("coding"))
			})
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(a.recordedModel()).To(Equal("deepseek-v4-flash"))
			Expect(
				out,
			).To(ContainSubstring("[route] model=coding -> provider=deepseek-pool model=deepseek-v4-flash"))

			// A concrete, non-pool model routes normally through the same provider
			// — the pool pre-step is a no-op for non-pool names.
			rec = httptest.NewRecorder()
			router.ServeHTTP(rec, newMessagesRequest("deepseek-v4-flash"))
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(a.recordedModel()).To(Equal("deepseek-v4-flash"))
		},
	)

	It(
		"AC 6: a saturated pinned provider with overflow overflows to the sibling at the real dispatch path",
		func() {
			// serverA blocks on release so request 1 holds A's only slot; serverB
			// responds immediately so request 2 completes synchronously.
			a = newRecordingUpstream(true)
			b = newRecordingUpstream(false)
			cfg := twoPoolConfig([]pkg.ModelPoolMember{
				{Provider: "deepseek-pool", Model: "deepseek-v4-flash", Weight: 1, Overflow: true},
				{Provider: "minimax-pool", Model: "MiniMax-2.7", Weight: 1},
			})
			router := buildRouter(cfg)

			// A session id the pool pins to member A (deepseek-pool) at the pool
			// level: slot 0 of totalWeight 2.
			id := sessionPinnedToSlot(0, 1, 1)

			oldV := flag.Lookup("v").Value.String()
			oldLogToStderr := flag.Lookup("logtostderr").Value.String()
			defer func() {
				Expect(flag.Set("v", oldV)).To(Succeed())
				Expect(flag.Set("logtostderr", oldLogToStderr)).To(Succeed())
			}()
			Expect(flag.Set("v", "2")).To(Succeed())
			Expect(flag.Set("logtostderr", "true")).To(Succeed())

			// Request 1 holds A's single slot, so A's provider Saturated closure
			// reads real in-flight 1 >= cap 1.
			rec1 := httptest.NewRecorder()
			done1 := serveAsync(router, rec1, sessionedRequest(id, "coding"))
			Eventually(func() int32 { return atomic.LoadInt32(&a.inFlight) }, "1s", "10ms").
				Should(BeNumerically("==", 1), "request 1 must be in flight on provider A")

			// Request 2, same session → pool pins to A, sees A saturated + overflow
			// → falls over to B.
			rec2 := httptest.NewRecorder()
			out := captureStderr(func() {
				router.ServeHTTP(rec2, sessionedRequest(id, "coding"))
			})
			Expect(rec2.Code).To(Equal(http.StatusOK))
			Expect(b.recordedModel()).To(Equal("MiniMax-2.7"))
			Expect(
				out,
			).To(ContainSubstring("[route] model=coding -> provider=minimax-pool model=MiniMax-2.7"))
			Expect(
				atomic.LoadInt32(&a.inFlight),
			).To(BeNumerically("==", 1), "request 2 must never touch provider A")

			a.unblock()
			Eventually(done1, "1s").Should(BeClosed())
			Expect(rec1.Code).To(Equal(http.StatusOK))
		},
	)
})

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/factory"
)

var _ = Describe("CreateRouterFromConfig concurrency limiter wiring", func() {
	var (
		srv       *httptest.Server
		release   chan struct{}
		closeOnce sync.Once
		inFlight  int32
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
	serveAsync := func(h http.Handler, rec *httptest.ResponseRecorder, req *http.Request) chan struct{} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			h.ServeHTTP(rec, req)
		}()
		return done
	}

	// makeConfig builds a single-provider config against the test upstream.
	makeConfig := func(provider pkg.Provider) *pkg.Config {
		return &pkg.Config{
			Router: pkg.Router{DefaultProvider: "capped"},
			Providers: map[string]pkg.Provider{
				"capped": provider,
			},
		}
	}

	BeforeEach(func() {
		release = make(chan struct{})
		closeOnce = sync.Once{}
		inFlight = 0
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&inFlight, 1)
			defer atomic.AddInt32(&inFlight, -1)
			select {
			case <-release:
			case <-r.Context().Done():
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
	})

	AfterEach(func() {
		// Close release before srv.Close so any request still blocked in the
		// upstream can finish and Close does not hang on it. closeOnce makes
		// this safe when the test body already released.
		closeOnce.Do(func() { close(release) })
		srv.Close()
	})

	It("answers 429 through the real dispatch boundary for a capped provider", func() {
		cfg := makeConfig(pkg.Provider{
			Upstream:                 srv.URL,
			Models:                   []string{"m*"},
			MaxConcurrentRequests:    1,
			MaxConcurrentWaitSeconds: 1,
		})
		router, err := factory.CreateRouterFromConfig(context.Background(), cfg, isolatedRegistry())
		Expect(err).NotTo(HaveOccurred())

		rec1 := httptest.NewRecorder()
		done1 := serveAsync(router, rec1, newMessagesRequest("m1"))
		Eventually(func() int32 { return atomic.LoadInt32(&inFlight) }, "1s", "10ms").
			Should(BeNumerically("==", 1), "request 1 must be in flight upstream")

		// Request 2 glob-matches but the slot is held → router answers 429
		// after the ~1s wait; it never reaches the upstream.
		rec2 := httptest.NewRecorder()
		router.ServeHTTP(rec2, newMessagesRequest("m2"))
		Expect(rec2.Code).To(Equal(http.StatusTooManyRequests))
		Expect(rec2.Header().Get("Content-Type")).To(ContainSubstring("application/json"))
		Expect(rec2.Body.String()).To(ContainSubstring("rate_limit_error"))
		Expect(
			atomic.LoadInt32(&inFlight),
		).To(Equal(int32(1)), "request 2 must never reach the upstream")

		// Request 3 matches no glob → routes to default_provider, whose
		// handler is the same wrapped limiter → also 429.
		rec3 := httptest.NewRecorder()
		router.ServeHTTP(rec3, newMessagesRequest("nomatch-xyz"))
		Expect(rec3.Code).To(Equal(http.StatusTooManyRequests))
		Expect(rec3.Body.String()).To(ContainSubstring("rate_limit_error"))
		Expect(
			atomic.LoadInt32(&inFlight),
		).To(Equal(int32(1)), "request 3 must never reach the upstream")

		closeOnce.Do(func() { close(release) })
		Eventually(done1, "1s").Should(BeClosed())
		Expect(rec1.Code).To(Equal(http.StatusOK))
	})

	It(
		"resolves an absent maxConcurrentWaitSeconds to the 30s default, not an instant timeout",
		func() {
			cfg := makeConfig(pkg.Provider{
				Upstream:              srv.URL,
				Models:                []string{"m*"},
				MaxConcurrentRequests: 1,
				// MaxConcurrentWaitSeconds 0 → the factory resolves the 30s default.
			})
			router, err := factory.CreateRouterFromConfig(
				context.Background(),
				cfg,
				isolatedRegistry(),
			)
			Expect(err).NotTo(HaveOccurred())

			rec1 := httptest.NewRecorder()
			done1 := serveAsync(router, rec1, newMessagesRequest("m1"))
			Eventually(func() int32 { return atomic.LoadInt32(&inFlight) }, "1s", "10ms").
				Should(BeNumerically("==", 1), "request 1 must be in flight upstream")

			// Request 2 queued with the default 30s wait. Completion is recorded
			// on a channel so the Consistently below can prove it is still queued.
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
				Should(Equal(0), "request 2 must still be queued, not timed out — 0 would have been an instant timeout")

			closeOnce.Do(func() { close(release) })
			Eventually(rec2Done, "1s").Should(Receive())
			Expect(rec2.Code).To(Equal(http.StatusOK))
			Eventually(done1, "1s").Should(BeClosed())
			Expect(rec1.Code).To(Equal(http.StatusOK))
		},
	)

	It(
		"rebuilds the limiter with the new cap on a fresh CreateRouterFromConfig (SIGHUP path)",
		func() {
			cfg1 := makeConfig(pkg.Provider{
				Upstream:                 srv.URL,
				Models:                   []string{"m*"},
				MaxConcurrentRequests:    1,
				MaxConcurrentWaitSeconds: 5,
			})
			handler1, err := factory.CreateRouterFromConfig(
				context.Background(),
				cfg1,
				isolatedRegistry(),
			)
			Expect(err).NotTo(HaveOccurred())

			recA := httptest.NewRecorder()
			doneA := serveAsync(handler1, recA, newMessagesRequest("m1"))
			Eventually(func() int32 { return atomic.LoadInt32(&inFlight) }, "1s", "10ms").
				Should(BeNumerically("==", 1), "request A must be in flight upstream")

			recB := httptest.NewRecorder()
			doneB := serveAsync(handler1, recB, newMessagesRequest("m2"))
			Consistently(func() int32 { return atomic.LoadInt32(&inFlight) }, "300ms", "30ms").
				Should(BeNumerically("==", 1), "handler1 caps at 1: request B must not reach the upstream")

			// Second config, same provider key, cap raised to 2 — a fresh
			// CreateRouterFromConfig exactly mirrors the reloader's rebuild.
			cfg2 := makeConfig(pkg.Provider{
				Upstream:                 srv.URL,
				Models:                   []string{"m*"},
				MaxConcurrentRequests:    2,
				MaxConcurrentWaitSeconds: 5,
			})
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
			Eventually(func() int32 { return atomic.LoadInt32(&inFlight) }, "1s", "10ms").
				Should(BeNumerically("==", 3), "handler2 caps at 2: A (handler1) + C + D in flight")

			closeOnce.Do(func() { close(release) })
			Eventually(doneA, "1s").Should(BeClosed())
			Eventually(doneB, "1s").Should(BeClosed())
			Eventually(doneC, "1s").Should(BeClosed())
			Eventually(doneD, "1s").Should(BeClosed())
			for _, rec := range []*httptest.ResponseRecorder{recA, recB, recC, recD} {
				Expect(rec.Code).To(Equal(http.StatusOK))
			}
		},
	)
})

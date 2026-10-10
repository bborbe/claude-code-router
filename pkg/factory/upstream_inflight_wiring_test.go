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
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/factory"
)

// blockingUpstream is an httptest upstream whose handler blocks on a
// release channel until the test closes it, so a request can be held in
// flight while the gauge is scraped.
type blockingUpstream struct {
	srv     *httptest.Server
	release chan struct{}
	once    sync.Once
	current int32
}

func newBlockingUpstream() *blockingUpstream {
	u := &blockingUpstream{release: make(chan struct{})}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&u.current, 1)
		defer atomic.AddInt32(&u.current, -1)
		select {
		case <-u.release:
		case <-r.Context().Done():
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	return u
}

func (u *blockingUpstream) releaseAll() { u.once.Do(func() { close(u.release) }) }

func (u *blockingUpstream) close() {
	u.releaseAll()
	u.srv.Close()
}

func (u *blockingUpstream) inFlight() int32 { return atomic.LoadInt32(&u.current) }

func (u *blockingUpstream) hostKey() string {
	parsed, err := url.Parse(u.srv.URL)
	Expect(err).NotTo(HaveOccurred())
	return pkg.UpstreamHostKey(parsed)
}

var _ = Describe("CreateRouterFromConfig upstream inflight wiring", func() {
	newMessagesRequest := func(model string) *http.Request {
		body := fmt.Sprintf(
			`{"model":%q,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			model,
		)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		return req
	}

	serveAsync := func(h http.Handler, req *http.Request) chan struct{} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			h.ServeHTTP(httptest.NewRecorder(), req)
		}()
		return done
	}

	scrape := func(h http.Handler) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		Expect(rec.Code).To(Equal(http.StatusOK))
		return rec.Body.String()
	}

	It("exposes capped and uncapped hosts, returning to 0 after release", func() {
		srvA := newBlockingUpstream()
		defer srvA.close()
		srvB := newBlockingUpstream()
		defer srvB.close()
		keyA := srvA.hostKey()
		keyB := srvB.hostKey()
		Expect(keyA).NotTo(Equal(keyB))

		cfg := &pkg.Config{
			Router: pkg.Router{DefaultProvider: "capped"},
			Providers: map[string]pkg.Provider{
				"capped": {Upstream: srvA.srv.URL, Models: []string{"c*"}},
				"free":   {Upstream: srvB.srv.URL, Models: []string{"f*"}},
			},
			UpstreamHostLimits: map[string]pkg.HostLimit{
				keyA:             {MaxConcurrentRequests: 1, MaxConcurrentWaitSeconds: 5},
				"unused.example": {MaxConcurrentRequests: 1},
			},
		}
		router, err := factory.CreateRouterFromConfig(
			context.Background(),
			cfg,
			factory.WithMetricsRegisterer(prometheus.NewRegistry()),
		)
		Expect(err).NotTo(HaveOccurred())

		Expect(scrape(router)).To(ContainSubstring(
			fmt.Sprintf(`ccrouter_upstream_inflight{host=%q} 0`, keyA),
		))
		Expect(scrape(router)).To(ContainSubstring(
			fmt.Sprintf(`ccrouter_upstream_inflight{host=%q} 0`, keyB),
		))
		// A host named in the config that no provider resolves to has no limiter.
		Expect(scrape(router)).NotTo(ContainSubstring(`host="unused.example"`))

		doneC := serveAsync(router, newMessagesRequest("c1"))
		Eventually(srvA.inFlight, "1s", "10ms").Should(Equal(int32(1)))
		Eventually(func() string { return scrape(router) }, "1s", "10ms").Should(
			ContainSubstring(fmt.Sprintf(`ccrouter_upstream_inflight{host=%q} 1`, keyA)),
		)

		doneF := serveAsync(router, newMessagesRequest("f1"))
		Eventually(srvB.inFlight, "1s", "10ms").Should(Equal(int32(1)))
		Eventually(func() string { return scrape(router) }, "1s", "10ms").Should(
			ContainSubstring(fmt.Sprintf(`ccrouter_upstream_inflight{host=%q} 1`, keyB)),
		)

		srvA.releaseAll()
		srvB.releaseAll()
		Eventually(doneC, "1s").Should(BeClosed())
		Eventually(doneF, "1s").Should(BeClosed())
		Eventually(func() string { return scrape(router) }, "1s", "10ms").Should(
			ContainSubstring(fmt.Sprintf(`ccrouter_upstream_inflight{host=%q} 0`, keyA)),
		)
		Eventually(func() string { return scrape(router) }, "1s", "10ms").Should(
			ContainSubstring(fmt.Sprintf(`ccrouter_upstream_inflight{host=%q} 0`, keyB)),
		)
	})

	It("reports one summed series for a host shared by two providers", func() {
		srv := newBlockingUpstream()
		defer srv.close()
		key := srv.hostKey()

		cfg := &pkg.Config{
			Router: pkg.Router{DefaultProvider: "a"},
			Providers: map[string]pkg.Provider{
				"a": {Upstream: srv.srv.URL, Models: []string{"a*"}},
				"b": {Upstream: srv.srv.URL, Models: []string{"b*"}},
			},
		}
		router, err := factory.CreateRouterFromConfig(
			context.Background(),
			cfg,
			factory.WithMetricsRegisterer(prometheus.NewRegistry()),
		)
		Expect(err).NotTo(HaveOccurred())

		doneA := serveAsync(router, newMessagesRequest("a1"))
		doneB := serveAsync(router, newMessagesRequest("b1"))
		Eventually(srv.inFlight, "1s", "10ms").Should(Equal(int32(2)))
		Eventually(func() string { return scrape(router) }, "1s", "10ms").Should(
			ContainSubstring(fmt.Sprintf(`ccrouter_upstream_inflight{host=%q} 2`, key)),
		)
		Expect(strings.Count(scrape(router), fmt.Sprintf(`host=%q`, key))).To(Equal(1))

		srv.releaseAll()
		Eventually(doneA, "1s").Should(BeClosed())
		Eventually(doneB, "1s").Should(BeClosed())
	})

	It("exposes the rebuilt tree's hosts after a reload", func() {
		srv := newBlockingUpstream()
		defer srv.close()
		key := srv.hostKey()

		provider := pkg.Provider{Upstream: srv.srv.URL, Models: []string{"m*"}}
		handler1, err := factory.CreateRouterFromConfig(
			context.Background(),
			&pkg.Config{
				Router:    pkg.Router{DefaultProvider: "p"},
				Providers: map[string]pkg.Provider{"p": provider},
				UpstreamHostLimits: map[string]pkg.HostLimit{
					key: {MaxConcurrentRequests: 1, MaxConcurrentWaitSeconds: 5},
				},
			},
			factory.WithMetricsRegisterer(prometheus.NewRegistry()),
		)
		Expect(err).NotTo(HaveOccurred())

		// Second build mirrors the reloader's fresh registry; a shared
		// registry would collide on the collector's registration.
		handler2, err := factory.CreateRouterFromConfig(
			context.Background(),
			&pkg.Config{
				Router:    pkg.Router{DefaultProvider: "p"},
				Providers: map[string]pkg.Provider{"p": provider},
			},
			factory.WithMetricsRegisterer(prometheus.NewRegistry()),
		)
		Expect(err).NotTo(HaveOccurred())

		Expect(scrape(handler1)).To(ContainSubstring(
			fmt.Sprintf(`ccrouter_upstream_inflight{host=%q}`, key),
		))
		Expect(scrape(handler2)).To(ContainSubstring(
			fmt.Sprintf(`ccrouter_upstream_inflight{host=%q}`, key),
		))
	})
})

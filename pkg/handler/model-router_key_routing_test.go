// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"flag"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// ModelRouter key routing specs: a presented x-api-key (from the auth
// middleware's context seam) that a provider's `allowedApiKeys` claims
// pins the dispatch to that provider, overriding model-glob selection.
// They share the package-level alwaysSample / testMetrics / testDateTime /
// labelHandler / captureStderr helpers from model-router_test.go and live
// in their own file to keep that suite under the 2000-line limit.
var _ = Describe("ModelRouter key routing", func() {
	var (
		seibertVllm        = labelHandler("seibert-vllm")
		seibertDarkFactory = labelHandler("seibert-dark-factory")
		fallback           = labelHandler("fallback")
		routes             []handler.ModelRoute
		mux                http.Handler
		rec                *httptest.ResponseRecorder
	)

	BeforeEach(func() {
		// Two providers share one glob; only the SECOND claims a key, so the
		// specs can prove the key overrides declaration order (AC 3) and that
		// keyless requests keep glob selection (declaration order is a fixed
		// slice here, unlike the factory's map iteration).
		routes = []handler.ModelRoute{
			{Pattern: "deepseek-*", ProviderName: "seibert-vllm", Handler: seibertVllm},
			{
				Pattern:        "deepseek-*",
				ProviderName:   "seibert-dark-factory",
				Handler:        seibertDarkFactory,
				AllowedApiKeys: []string{"dark-factory-key"},
			},
		}
		mux = handler.NewModelRouter(
			routes,
			"default-fallback",
			fallback,
			nil,
			alwaysSample,
			testMetrics,
			testDateTime,
		)
		rec = httptest.NewRecorder()
	})

	post := func(body string) *http.Request {
		return httptest.NewRequest(
			http.MethodPost,
			"/v1/messages",
			strings.NewReader(body),
		)
	}

	// postWithKey builds a request like post, but injects the presented
	// x-api-key into the request context via the prompt-2 seam — the auth
	// middleware is not run in these unit tests.
	postWithKey := func(body, key string) *http.Request {
		req := post(body)
		if key != "" {
			req = req.WithContext(handler.ContextWithPresentedApiKey(req.Context(), key))
		}
		return req
	}

	It(
		"AC 3: a claimed key wins over the model glob that would have selected the first provider",
		func() {
			// `deepseek-*` matches both seibert-vllm (declared first) and
			// seibert-dark-factory (declared second). The key is claimed only
			// by the second provider, so the key must override declaration
			// order — proving the key branch fires before the glob walk.
			mux.ServeHTTP(rec, postWithKey(`{"model":"deepseek-v4-pro"}`, "dark-factory-key"))
			Expect(rec.Body.String()).To(Equal("seibert-dark-factory"))
		},
	)

	It("AC 3: a claimed key wins even when its provider's glob would NOT match the model", func() {
		// deepseek-v4-pro globs to seibert-vllm; the key's provider is
		// seibert-dark-factory, whose glob (deepseek-*) would match too — but
		// use a model that glob-routes elsewhere entirely (claude-opus-4-7
		// would match an anthropic route, not the key-holder's). The key
		// still wins regardless of the model name.
		routes = append(routes, handler.ModelRoute{
			Pattern:      "claude-*",
			ProviderName: "anthropic-subscription",
			Handler:      labelHandler("anthropic"),
		})
		mux = handler.NewModelRouter(
			routes,
			"default-fallback",
			fallback,
			nil,
			alwaysSample,
			testMetrics,
			testDateTime,
		)
		mux.ServeHTTP(rec, postWithKey(`{"model":"claude-opus-4-7"}`, "dark-factory-key"))
		Expect(rec.Body.String()).To(Equal("seibert-dark-factory"))
	})

	It("AC 4: a valid-but-unclaimed key routes by glob exactly like the keyless case", func() {
		// registry-only-key is present in the auth registry but claimed by
		// no provider, so it must not pin routing — deepseek-v4-pro globs
		// to the first provider, seibert-vllm.
		mux.ServeHTTP(rec, postWithKey(`{"model":"deepseek-v4-pro"}`, "registry-only-key"))
		Expect(rec.Body.String()).To(Equal("seibert-vllm"))
	})

	It("AC 5: a keyless request routes by glob unchanged and logs no key-matched line", func() {
		_ = flag.Set("logtostderr", "true")
		_ = flag.Set("v", "2")
		out := captureStderr(func() {
			mux.ServeHTTP(rec, post(`{"model":"deepseek-v4-pro"}`))
		})
		Expect(rec.Body.String()).To(Equal("seibert-vllm"))
		// No key-routing branch fired.
		Expect(out).NotTo(ContainSubstring("[route] key matched"))
		// The [req] line still names the glob-selected provider.
		Expect(out).To(MatchRegexp(
			`\[req\] POST /v1/messages model=deepseek-v4-pro provider=seibert-vllm status=200 latency=`,
		))
	})

	It("routes by glob when no provider claims any key, even with a key in context", func() {
		noKeyRoutes := []handler.ModelRoute{
			{Pattern: "deepseek-*", ProviderName: "seibert-vllm", Handler: seibertVllm},
		}
		mux = handler.NewModelRouter(
			noKeyRoutes,
			"default-fallback",
			fallback,
			nil,
			alwaysSample,
			testMetrics,
			testDateTime,
		)
		// A presented key is ignored when nothing claims it.
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, postWithKey(`{"model":"deepseek-v4-pro"}`, "dark-factory-key"))
		Expect(rec.Body.String()).To(Equal("seibert-vllm"))
		// The keyless request is byte-for-byte unchanged.
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, post(`{"model":"deepseek-v4-pro"}`))
		Expect(rec.Body.String()).To(Equal("seibert-vllm"))
	})

	It(
		"key match still emits the ccrouter_requests_total observation with the key-selected provider label",
		func() {
			m := handler.NewMetrics(nil)
			mux = handler.NewModelRouter(
				routes,
				"default-fallback",
				fallback,
				nil,
				alwaysSample,
				m,
				testDateTime,
			)
			rec = httptest.NewRecorder()
			mux.ServeHTTP(rec, postWithKey(`{"model":"deepseek-v4-pro"}`, "dark-factory-key"))
			Expect(rec.Body.String()).To(Equal("seibert-dark-factory"))
			// The metric label is the KEY-selected provider, not the
			// glob-selected one — the model label itself is unchanged.
			Expect(
				testutil.ToFloat64(
					m.RequestsTotal.WithLabelValues(
						"seibert-dark-factory",
						"deepseek-v4-pro",
						"2xx",
					),
				),
			).To(Equal(float64(1)))
		},
	)
})

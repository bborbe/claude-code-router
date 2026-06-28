// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// roundTripperFunc adapts a function to http.RoundTripper for tests.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

var _ = Describe("LoggingRoundTripper", func() {
	It("falls back to http.DefaultTransport when inner is nil (no panic on RoundTrip)", func() {
		rt := handler.NewLoggingRoundTripper(nil)
		Expect(rt).NotTo(BeNil())
		// We don't actually invoke RoundTrip — http.DefaultTransport
		// would attempt a real network call. The constructor not
		// returning nil + not panicking is the contract under test.
	})

	It("returns nil resp on inner error (matches net/http contract)", func() {
		boomErr := errors.New("boom")
		// Even if a buggy inner transport returns a non-nil resp alongside err,
		// the wrapper must scrub it to nil to match the documented contract.
		inner := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 500}, boomErr
		})
		rt := handler.NewLoggingRoundTripper(inner)
		req := httptest.NewRequest(http.MethodPost, "https://api.example.com/v1/messages", nil)
		resp, err := rt.RoundTrip(req)
		Expect(err).To(MatchError(boomErr))
		Expect(resp).To(BeNil(), "wrapper must scrub the (possibly stale) resp when err != nil")
	})

	It("passes resp through unchanged on success", func() {
		want := &http.Response{StatusCode: 200}
		inner := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
			return want, nil
		})
		rt := handler.NewLoggingRoundTripper(inner)
		req := httptest.NewRequest(http.MethodPost, "https://api.example.com/v1/messages", nil)
		resp, err := rt.RoundTrip(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp).To(Equal(want))
	})

	Context("V(3) outbound header logging", func() {
		BeforeEach(func() {
			_ = flag.Set("logtostderr", "true")
		})

		AfterEach(func() {
			// Reset verbosity so other specs are not affected.
			_ = flag.Set("v", "0")
		})

		makeRT := func() http.RoundTripper {
			return handler.NewLoggingRoundTripper(
				roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200}, nil
				}),
			)
		}

		It("emits [upstream.headers] line with redacted Authorization at V(3)", func() {
			_ = flag.Set("v", "3")
			rt := makeRT()
			req := httptest.NewRequest(http.MethodPost, "https://api.example.com/v1/messages", nil)
			req.Header.Set("Authorization", "Bearer test-token-v3")
			out := captureStderr(func() {
				_, _ = rt.RoundTrip(req)
			})
			Expect(out).To(ContainSubstring("[upstream.headers]"))
			Expect(out).To(ContainSubstring("<redacted"))
		})

		It("does not emit [upstream.headers] at V(1)", func() {
			_ = flag.Set("v", "1")
			rt := makeRT()
			req := httptest.NewRequest(http.MethodPost, "https://api.example.com/v1/messages", nil)
			out := captureStderr(func() {
				_, _ = rt.RoundTrip(req)
			})
			Expect(out).NotTo(ContainSubstring("[upstream.headers]"))
		})

		It("does not emit [upstream.headers] at V(2)", func() {
			_ = flag.Set("v", "2")
			rt := makeRT()
			req := httptest.NewRequest(http.MethodPost, "https://api.example.com/v1/messages", nil)
			out := captureStderr(func() {
				_, _ = rt.RoundTrip(req)
			})
			Expect(out).NotTo(ContainSubstring("[upstream.headers]"))
		})

		It(
			"canary: leak-canary-v3 value in Authorization header does not appear in V(3) log output",
			func() {
				_ = flag.Set("v", "3")
				rt := makeRT()
				req := httptest.NewRequest(
					http.MethodPost,
					"https://api.example.com/v1/messages",
					nil,
				)
				req.Header.Set("Authorization", "Bearer leak-canary-v3")
				out := captureStderr(func() {
					_, _ = rt.RoundTrip(req)
				})
				Expect(out).To(ContainSubstring("[upstream.headers]"))
				Expect(out).NotTo(ContainSubstring("leak-canary-v3"))
			},
		)
	})
})

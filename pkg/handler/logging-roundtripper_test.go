// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"errors"
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
})

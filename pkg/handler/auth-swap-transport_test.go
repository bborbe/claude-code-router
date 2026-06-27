// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

type captureTransport struct{ captured *http.Request }

func (c *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.captured = req
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

var _ = Describe("AuthSwapTransport", func() {
	It("replaces Authorization with Bearer <token> when token set", func() {
		cap := &captureTransport{}
		swap := handler.NewAuthSwapTransport(cap, "secret-token")

		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		req.Header.Set("Authorization", "Bearer oauth-original")

		_, err := swap.RoundTrip(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(cap.captured.Header.Get("Authorization")).To(Equal("Bearer secret-token"))
	})

	It("does not mutate the caller's request headers (clone)", func() {
		cap := &captureTransport{}
		swap := handler.NewAuthSwapTransport(cap, "secret")

		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		req.Header.Set("Authorization", "Bearer original")

		_, _ = swap.RoundTrip(req)

		Expect(req.Header.Get("Authorization")).To(Equal("Bearer original"))
	})

	It("returns next unchanged when token is empty (no-op)", func() {
		cap := &captureTransport{}
		swap := handler.NewAuthSwapTransport(cap, "")

		Expect(swap).To(BeIdenticalTo(http.RoundTripper(cap)))
	})
})

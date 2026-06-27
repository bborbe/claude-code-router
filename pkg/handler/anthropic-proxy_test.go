// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// stubTransport routes every request to the test handler bypassing the
// network. Lets us assert the request the proxy would send upstream.
type stubTransport struct {
	upstream http.Handler
	captured *http.Request
}

func (s *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.captured = req
	rec := httptest.NewRecorder()
	s.upstream.ServeHTTP(rec, req)
	return rec.Result(), nil
}

type errTransport struct{ err error }

func (e errTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return nil, e.err
}

var _ = Describe("AnthropicProxyHandler", func() {
	var upstreamURL *url.URL

	BeforeEach(func() {
		var err error
		upstreamURL, err = url.Parse("https://api.anthropic.com")
		Expect(err).NotTo(HaveOccurred())
	})

	It("forwards POST /v1/messages to upstream with body intact", func() {
		stub := &stubTransport{
			upstream: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"msg_test","content":[]}`))
			}),
		}

		h := handler.NewAnthropicProxyHandler(upstreamURL, stub)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(
			http.MethodPost,
			"/v1/messages",
			strings.NewReader(`{"model":"claude-opus-4-7"}`),
		)

		h.ServeHTTP(rec, req)

		Expect(rec.Code).To(Equal(http.StatusOK))
		body, err := io.ReadAll(rec.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).To(ContainSubstring(`"id":"msg_test"`))
		Expect(stub.captured).NotTo(BeNil())
		Expect(stub.captured.Method).To(Equal(http.MethodPost))
		Expect(stub.captured.URL.Path).To(Equal("/v1/messages"))
		Expect(stub.captured.Host).To(Equal("api.anthropic.com"))
	})

	It("preserves the Authorization header through the proxy (OAuth bearer pass-through)", func() {
		stub := &stubTransport{
			upstream: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}),
		}

		h := handler.NewAnthropicProxyHandler(upstreamURL, stub)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer sk-oauth-bearer-secret")

		h.ServeHTTP(rec, req)

		Expect(stub.captured.Header.Get("Authorization")).To(Equal("Bearer sk-oauth-bearer-secret"))
	})

	It("returns 502 when the upstream transport fails", func() {
		h := handler.NewAnthropicProxyHandler(
			upstreamURL,
			errTransport{err: errors.New("connection refused")},
		)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))

		h.ServeHTTP(rec, req)

		Expect(rec.Code).To(Equal(http.StatusBadGateway))
		body, _ := io.ReadAll(rec.Body)
		Expect(string(body)).To(ContainSubstring("connection refused"))
	})
})

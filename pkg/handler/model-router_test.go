// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// labelHandler writes its label to the body so tests can assert which
// route was chosen.
func labelHandler(label string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(label))
	})
}

var _ = Describe("ModelRouter", func() {
	var (
		anthropic = labelHandler("anthropic")
		minimax   = labelHandler("minimax")
		ollama    = labelHandler("ollama")
		fallback  = labelHandler("fallback")
		routes    []handler.ModelRoute
		mux       http.Handler
		rec       *httptest.ResponseRecorder
	)

	BeforeEach(func() {
		routes = []handler.ModelRoute{
			{Pattern: "claude-*", Handler: anthropic},
			{Pattern: "opus", Handler: anthropic},
			{Pattern: "sonnet", Handler: anthropic},
			{Pattern: "MiniMax-*", Handler: minimax},
			{Pattern: "qwen*", Handler: ollama},
		}
		mux = handler.NewModelRouter(routes, fallback)
		rec = httptest.NewRecorder()
	})

	post := func(body string) *http.Request {
		return httptest.NewRequest(
			http.MethodPost,
			"/v1/messages",
			strings.NewReader(body),
		)
	}

	It("routes claude-opus-4-7 to anthropic via glob", func() {
		mux.ServeHTTP(rec, post(`{"model":"claude-opus-4-7"}`))
		Expect(rec.Body.String()).To(Equal("anthropic"))
	})

	It("routes bare alias 'opus' to anthropic via exact match", func() {
		mux.ServeHTTP(rec, post(`{"model":"opus"}`))
		Expect(rec.Body.String()).To(Equal("anthropic"))
	})

	It("routes MiniMax-M3-highspeed to minimax", func() {
		mux.ServeHTTP(rec, post(`{"model":"MiniMax-M3-highspeed"}`))
		Expect(rec.Body.String()).To(Equal("minimax"))
	})

	It("routes qwen3.6:35b to ollama", func() {
		mux.ServeHTTP(rec, post(`{"model":"qwen3.6:35b"}`))
		Expect(rec.Body.String()).To(Equal("ollama"))
	})

	It("falls back when model matches no pattern", func() {
		mux.ServeHTTP(rec, post(`{"model":"gemini-3-pro"}`))
		Expect(rec.Body.String()).To(Equal("fallback"))
	})

	It("falls back when body has no model field", func() {
		mux.ServeHTTP(rec, post(`{"other":"thing"}`))
		Expect(rec.Body.String()).To(Equal("fallback"))
	})

	It("falls back when body is not JSON (e.g. GET /v1/models)", func() {
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
		Expect(rec.Body.String()).To(Equal("fallback"))
	})

	It("preserves the body for the downstream handler to re-read", func() {
		seen := ""
		capturing := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			seen = string(b)
		})
		mux = handler.NewModelRouter(
			[]handler.ModelRoute{{Pattern: "claude-*", Handler: capturing}},
			fallback,
		)
		body := `{"model":"claude-opus-4-7","messages":[{"role":"user","content":"hi"}]}`
		mux.ServeHTTP(rec, post(body))
		Expect(seen).To(Equal(body))
	})
})

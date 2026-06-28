// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"encoding/json"
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
		mux = handler.NewModelRouter(routes, fallback, nil)
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
			nil,
		)
		body := `{"model":"claude-opus-4-7","messages":[{"role":"user","content":"hi"}]}`
		mux.ServeHTTP(rec, post(body))
		Expect(seen).To(Equal(body))
	})

	Context("alias resolution", func() {
		It("rewrites the request body's .model field when an alias matches", func() {
			var capturedBody []byte
			var capturedContentLength int64
			capturing := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				capturedBody, _ = io.ReadAll(r.Body)
				capturedContentLength = r.ContentLength
			})
			aliases := map[string]string{"qwen": "qwen3.6:35b-a3b-coding-nvfp4"}
			mux = handler.NewModelRouter(
				[]handler.ModelRoute{{Pattern: "qwen*", Handler: capturing}},
				fallback,
				aliases,
			)
			mux.ServeHTTP(rec, post(`{"model":"qwen"}`))

			var seen map[string]any
			Expect(json.Unmarshal(capturedBody, &seen)).To(Succeed())
			Expect(seen["model"]).To(Equal("qwen3.6:35b-a3b-coding-nvfp4"))
			Expect(capturedContentLength).To(Equal(int64(len(capturedBody))))
		})

		It("routes the rewritten body to the matching glob", func() {
			aliases := map[string]string{"qwen": "qwen3.6:35b-a3b-coding-nvfp4"}
			mux = handler.NewModelRouter(
				[]handler.ModelRoute{{Pattern: "qwen*", Handler: labelHandler("ollama")}},
				fallback,
				aliases,
			)
			mux.ServeHTTP(rec, post(`{"model":"qwen"}`))
			Expect(rec.Body.String()).To(Equal("ollama"))
		})

		It("preserves other top-level body fields across the rewrite", func() {
			var capturedBody []byte
			capturing := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				capturedBody, _ = io.ReadAll(r.Body)
			})
			aliases := map[string]string{"qwen": "qwen3.6:35b-a3b-coding-nvfp4"}
			mux = handler.NewModelRouter(
				[]handler.ModelRoute{{Pattern: "qwen*", Handler: capturing}},
				fallback,
				aliases,
			)
			body := `{"model":"qwen","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
			mux.ServeHTTP(rec, post(body))

			var seen map[string]any
			Expect(json.Unmarshal(capturedBody, &seen)).To(Succeed())
			Expect(seen["model"]).To(Equal("qwen3.6:35b-a3b-coding-nvfp4"))
			Expect(seen["max_tokens"]).To(Equal(float64(100)))
			messages, ok := seen["messages"].([]any)
			Expect(ok).To(BeTrue())
			Expect(len(messages)).To(BeNumerically(">", 0))
			firstMsg, ok := messages[0].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(firstMsg["role"]).To(Equal("user"))
		})

		It("does not rewrite on alias miss", func() {
			var capturedBody []byte
			var capturedContentLength int64
			capturing := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				capturedBody, _ = io.ReadAll(r.Body)
				capturedContentLength = r.ContentLength
			})
			aliases := map[string]string{"qwen": "qwen3.6:35b-a3b-coding-nvfp4"}
			mux = handler.NewModelRouter(
				[]handler.ModelRoute{{Pattern: "claude-opus*", Handler: capturing}},
				fallback,
				aliases,
			)
			originalBody := `{"model":"claude-opus-4-7"}`
			mux.ServeHTTP(rec, post(originalBody))
			Expect(string(capturedBody)).To(Equal(originalBody))
			Expect(capturedContentLength).To(Equal(int64(len(originalBody))))
		})

		It("does not rewrite when aliases map is nil", func() {
			var capturedBody []byte
			var capturedContentLength int64
			capturing := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				capturedBody, _ = io.ReadAll(r.Body)
				capturedContentLength = r.ContentLength
			})
			mux = handler.NewModelRouter(
				[]handler.ModelRoute{{Pattern: "claude-opus*", Handler: capturing}},
				fallback,
				nil,
			)
			originalBody := `{"model":"claude-opus-4-7"}`
			mux.ServeHTTP(rec, post(originalBody))
			Expect(string(capturedBody)).To(Equal(originalBody))
			Expect(capturedContentLength).To(Equal(int64(len(originalBody))))
		})

		It("does not rewrite when body has no model field", func() {
			var capturedBody []byte
			var capturedContentLength int64
			capturing := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				capturedBody, _ = io.ReadAll(r.Body)
				capturedContentLength = r.ContentLength
			})
			mux = handler.NewModelRouter(nil, capturing, map[string]string{"": "should-not-fire"})
			originalBody := `{"other":"thing"}`
			mux.ServeHTTP(rec, post(originalBody))
			Expect(string(capturedBody)).To(Equal(originalBody))
			Expect(capturedContentLength).To(Equal(int64(len(originalBody))))
		})
	})
})

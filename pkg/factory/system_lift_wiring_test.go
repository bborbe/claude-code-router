// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/factory"
)

var _ = Describe("CreateRouterFromConfig requiresLeadingSystem wiring", func() {
	var srv *httptest.Server
	var capturedBody []byte

	BeforeEach(func() {
		capturedBody = nil
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
	})

	AfterEach(func() {
		srv.Close()
	})

	makeConfig := func(provider pkg.Provider) *pkg.Config {
		return &pkg.Config{
			Router: pkg.Router{DefaultProvider: "ollama-local"},
			Providers: map[string]pkg.Provider{
				"ollama-local": provider,
			},
		}
	}

	isolatedRegistry := func() factory.RouterOptionFunc {
		return factory.WithMetricsRegisterer(prometheus.NewRegistry())
	}

	It(
		"lifts non-leading system messages for a model matching the provider's requiresLeadingSystem",
		func() {
			cfg := makeConfig(pkg.Provider{
				Upstream:              srv.URL,
				Models:                []string{"qwen*"},
				RequiresLeadingSystem: []string{"qwen3.8*"},
			})
			handler, err := factory.CreateRouterFromConfig(
				context.Background(),
				cfg,
				isolatedRegistry(),
			)
			Expect(err).NotTo(HaveOccurred())

			body := `{"model":"qwen3.8:27b-mlx","max_tokens":64,"system":[{"type":"text","text":"top"}],"messages":[{"role":"user","content":"hi"},{"role":"system","content":"A"},{"role":"assistant","content":"ok"},{"role":"system","content":"B"}]}`
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(capturedBody).NotTo(BeNil())

			// Parse the body the upstream received
			var result map[string]interface{}
			Expect(json.Unmarshal(capturedBody, &result)).To(Succeed())

			// No system-role entry remains in messages
			messagesVal, ok := result["messages"].([]interface{})
			Expect(ok).To(BeTrue())
			Expect(messagesVal).To(HaveLen(2))
			msg0, ok := messagesVal[0].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(msg0["role"]).To(Equal("user"))
			msg1, ok := messagesVal[1].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(msg1["role"]).To(Equal("assistant"))

			// Top-level system block has three text blocks: top, A, B
			systemRaw, ok := result["system"].([]interface{})
			Expect(ok).To(BeTrue())
			Expect(systemRaw).To(HaveLen(3))
			texts := make([]string, 3)
			for i, blk := range systemRaw {
				blkMap, ok := blk.(map[string]interface{})
				Expect(ok).To(BeTrue())
				texts[i], ok = blkMap["text"].(string)
				Expect(ok).To(BeTrue())
			}
			Expect(texts).To(Equal([]string{"top", "A", "B"}))
		},
	)

	It(
		"forwards byte-identically for a model that matches the provider glob but not requiresLeadingSystem",
		func() {
			cfg := makeConfig(pkg.Provider{
				Upstream:              srv.URL,
				Models:                []string{"qwen*"},
				RequiresLeadingSystem: []string{"qwen3.8*"},
			})
			handler, err := factory.CreateRouterFromConfig(
				context.Background(),
				cfg,
				isolatedRegistry(),
			)
			Expect(err).NotTo(HaveOccurred())

			original := `{"model":"qwen3.6:35b-a3b-coding-nvfp4","max_tokens":64,"system":[{"type":"text","text":"top"}],"messages":[{"role":"user","content":"hi"},{"role":"system","content":"A"},{"role":"assistant","content":"ok"},{"role":"system","content":"B"}]}`
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(original))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(capturedBody).To(Equal([]byte(original)))
		},
	)

	It("forwards byte-identically when the provider omits requiresLeadingSystem", func() {
		cfg := makeConfig(pkg.Provider{
			Upstream: srv.URL,
			Models:   []string{"qwen*"},
		})
		handler, err := factory.CreateRouterFromConfig(
			context.Background(),
			cfg,
			isolatedRegistry(),
		)
		Expect(err).NotTo(HaveOccurred())

		original := `{"model":"qwen3.8:27b-mlx","max_tokens":64,"system":[{"type":"text","text":"top"}],"messages":[{"role":"user","content":"hi"},{"role":"system","content":"A"},{"role":"assistant","content":"ok"},{"role":"system","content":"B"}]}`
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(original))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(capturedBody).To(Equal([]byte(original)))
	})
})

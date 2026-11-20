// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"bytes"
	"flag"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/andybalholm/brotli"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// The `[req.body]` line exists so a provider rejection is diagnosable
// from the log alone. A compressed body defeats that: before brotli was
// decoded, Cloudflare's `Content-Encoding: br` response reached the log
// as raw bytes and any 0x0a inside them split the line in two.
var _ = Describe("ModelRouter [req.body] encoding", func() {
	var (
		fallback = labelHandler("fallback")
		mux      http.Handler
		rec      *httptest.ResponseRecorder
	)

	post := func(body string) *http.Request {
		return httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	}

	// newMux routes MiniMax-* to a handler that answers 400 with body and
	// the given Content-Encoding.
	newMux := func(body []byte, contentEncoding string) {
		failing := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Encoding", contentEncoding)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write(body)
		})
		mux = handler.NewModelRouter(
			[]handler.ModelRoute{
				{
					Pattern:      "MiniMax-*",
					ProviderName: "anthropic-subscription",
					Handler:      failing,
				},
			},
			"default-fallback",
			fallback,
			nil,
			alwaysSample,
			testMetrics,
			testDateTime,
		)
		rec = httptest.NewRecorder()
	}

	BeforeEach(func() {
		_ = flag.Set("logtostderr", "true")
		_ = flag.Set("v", "1")
	})

	It("decodes a brotli-encoded non-2xx body before logging it", func() {
		payload := []byte(
			`{"type":"error","error":{"type":"invalid_request_error","message":"Invalid signature in thinking block"}}`,
		)
		var compressed bytes.Buffer
		bw := brotli.NewWriter(&compressed)
		_, err := bw.Write(payload)
		Expect(err).NotTo(HaveOccurred())
		Expect(bw.Close()).To(Succeed())
		body := compressed.Bytes()
		// Anti-fake: the fixture must actually be compressed, otherwise the
		// assertion below would pass on the identity path.
		Expect(body).ToNot(Equal(payload))

		newMux(body, "br")
		out := captureStderr(func() {
			mux.ServeHTTP(rec, post(`{"model":"MiniMax-M3-highspeed"}`))
		})
		Expect(
			out,
		).To(MatchRegexp(`\[req\.body\] POST /v1/messages provider=anthropic-subscription/- status=400 body_len=\d+ body=.*Invalid signature in thinking block`))
		Expect(out).ToNot(ContainSubstring("<binary"))
	})

	It("never logs raw bytes for an undecodable encoding", func() {
		// The embedded 0x0a is the load-bearing byte: printed raw it ends
		// the log line early.
		opaque := []byte{0x03, 0x57, 0x00, 0x00, 0x0a, 0x80, 0xaa, 0xaa}

		newMux(opaque, "zstd")
		out := captureStderr(func() {
			mux.ServeHTTP(rec, post(`{"model":"MiniMax-M3-highspeed"}`))
		})
		Expect(out).To(ContainSubstring(`body=<binary content-encoding="zstd">`))
		Expect(out).ToNot(ContainSubstring("\x03W"))
		Expect(strings.Count(out, "[req.body]")).To(Equal(1))
		Expect(out).To(ContainSubstring("status=400"))
	})

	It("keeps a large text body readable when the cut lands mid-rune", func() {
		// BodySampleMaxBytes falls on the middle of the multi-byte rune, so a
		// naive slice would leave invalid UTF-8 and misreport text as binary.
		payload := strings.Repeat("a", handler.BodySampleMaxBytes-1) + "€ tail"
		newMux([]byte(payload), "")

		out := captureStderr(func() {
			mux.ServeHTTP(rec, post(`{"model":"MiniMax-M3-highspeed"}`))
		})
		Expect(out).ToNot(ContainSubstring("<binary"))
		Expect(out).To(ContainSubstring("aaaa"))
	})

	It("flattens a decoded body containing newlines onto one line", func() {
		// CR/LF are valid UTF-8, so the binary guard does not catch them —
		// but they still end the log line early.
		payload := []byte("{\"error\":\"line one\nline two\r\nline three\"}")
		newMux(payload, "")

		out := captureStderr(func() {
			mux.ServeHTTP(rec, post(`{"model":"MiniMax-M3-highspeed"}`))
		})
		Expect(strings.Count(out, "[req.body]")).To(Equal(1))
		Expect(out).To(ContainSubstring("line one line two  line three"))
	})

	It("never logs raw bytes when a decoded body is not valid UTF-8", func() {
		// A corrupt stream that brotli tolerates but that yields bytes no
		// text log can carry.
		opaque := []byte{0x1b, 0x00, 0x00, 0x00, 0x0a, 0xff, 0xfe}

		newMux(opaque, "br")
		out := captureStderr(func() {
			mux.ServeHTTP(rec, post(`{"model":"MiniMax-M3-highspeed"}`))
		})
		Expect(out).To(ContainSubstring("body=<binary content-encoding="))
		Expect(strings.Count(out, "[req.body]")).To(Equal(1))
	})
})

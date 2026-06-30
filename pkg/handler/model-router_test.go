// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"

	liblog "github.com/bborbe/log"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// alwaysSample is the test-default sampler — always returns true, so
// every request emits its `[req]` line. Specs that exercise the 10s
// sampling behavior construct their own sampler inline.
var alwaysSample = liblog.NewSamplerTrue()

var testMetrics = handler.NewMetrics(nil)

var testDateTime = libtime.NewCurrentDateTime()

// labelHandler writes its label to the body so tests can assert which
// route was chosen.
func labelHandler(label string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(label))
	})
}

// captureStderr runs fn with os.Stderr piped into a buffer and returns
// what was written. glog logs to stderr by default once -logtostderr is
// set; this lets tests assert on the structured log line shape.
func captureStderr(fn func()) string {
	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		buf, _ := io.ReadAll(r)
		done <- string(buf)
	}()
	fn()
	glog.Flush()
	_ = w.Close()
	os.Stderr = origStderr
	return <-done
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
			{Pattern: "claude-*", ProviderName: "anthropic-subscription", Handler: anthropic},
			{Pattern: "opus", ProviderName: "anthropic-subscription", Handler: anthropic},
			{Pattern: "sonnet", ProviderName: "anthropic-subscription", Handler: anthropic},
			{Pattern: "MiniMax-*", ProviderName: "minimax", Handler: minimax},
			{Pattern: "qwen*", ProviderName: "ollama-local", Handler: ollama},
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
			[]handler.ModelRoute{
				{Pattern: "claude-*", ProviderName: "anthropic-subscription", Handler: capturing},
			},
			"default-fallback",
			fallback,
			nil,
			alwaysSample,
			testMetrics,
			testDateTime,
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
				[]handler.ModelRoute{
					{Pattern: "qwen*", ProviderName: "ollama-local", Handler: capturing},
				},
				"default-fallback",
				fallback,
				aliases,
				alwaysSample,
				testMetrics,
				testDateTime,
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
				[]handler.ModelRoute{
					{
						Pattern:      "qwen*",
						ProviderName: "ollama-local",
						Handler:      labelHandler("ollama"),
					},
				},
				"default-fallback",
				fallback,
				aliases,
				alwaysSample,
				testMetrics,
				testDateTime,
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
				[]handler.ModelRoute{
					{Pattern: "qwen*", ProviderName: "ollama-local", Handler: capturing},
				},
				"default-fallback",
				fallback,
				aliases,
				alwaysSample,
				testMetrics,
				testDateTime,
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
				[]handler.ModelRoute{
					{
						Pattern:      "claude-opus*",
						ProviderName: "anthropic-subscription",
						Handler:      capturing,
					},
				},
				"default-fallback",
				fallback,
				aliases,
				alwaysSample,
				testMetrics,
				testDateTime,
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
				[]handler.ModelRoute{
					{
						Pattern:      "claude-opus*",
						ProviderName: "anthropic-subscription",
						Handler:      capturing,
					},
				},
				"default-fallback",
				fallback,
				nil,
				alwaysSample,
				testMetrics,
				testDateTime,
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
			mux = handler.NewModelRouter(
				nil,
				"default-fallback",
				capturing,
				map[string]string{"": "should-not-fire"},
				alwaysSample,
				testMetrics,
				testDateTime,
			)
			originalBody := `{"other":"thing"}`
			mux.ServeHTTP(rec, post(originalBody))
			Expect(string(capturedBody)).To(Equal(originalBody))
			Expect(capturedContentLength).To(Equal(int64(len(originalBody))))
		})
	})

	Context("structured request log", func() {
		BeforeEach(func() {
			// glog initialized once globally; bump verbosity for these specs.
			_ = flag.Set("logtostderr", "true")
			_ = flag.Set("v", "2")
		})

		It("emits one [req] line with model, provider, status, and latency on a route hit", func() {
			out := captureStderr(func() {
				mux.ServeHTTP(rec, post(`{"model":"MiniMax-M3-highspeed"}`))
			})
			Expect(
				out,
			).To(MatchRegexp(`\[req\] POST /v1/messages model=MiniMax-M3-highspeed provider=minimax status=200 latency=\d+m?s`))
		})

		It("emits [req] with alias= field on alias hit", func() {
			aliases := map[string]string{"m3": "MiniMax-M3-highspeed"}
			mux = handler.NewModelRouter(
				routes,
				"default-fallback",
				fallback,
				aliases,
				alwaysSample,
				testMetrics,
				testDateTime,
			)
			out := captureStderr(func() {
				mux.ServeHTTP(rec, post(`{"model":"m3"}`))
			})
			Expect(
				out,
			).To(MatchRegexp(`\[req\] POST /v1/messages model=m3 alias=MiniMax-M3-highspeed provider=minimax status=200 latency=`))
		})

		It("emits [req] with default provider name on fallback", func() {
			out := captureStderr(func() {
				mux.ServeHTTP(rec, post(`{"model":"gemini-3-pro"}`))
			})
			Expect(
				out,
			).To(MatchRegexp(`\[req\] POST /v1/messages model=gemini-3-pro provider=default-fallback status=200 latency=`))
		})

		It("latency value is non-zero and ends in ms or s", func() {
			out := captureStderr(func() {
				mux.ServeHTTP(rec, post(`{"model":"opus"}`))
			})
			latency := regexp.MustCompile(`latency=(\S+)`).FindStringSubmatch(out)
			Expect(latency).To(HaveLen(2))
			Expect(latency[1]).To(MatchRegexp(`^\d+(\.\d+)?(m?s)$`))
		})

		Context("sampler gating", func() {
			It("suppresses 200 [req] lines when the sampler returns false", func() {
				never := liblog.SamplerFunc(func() bool { return false })
				mux = handler.NewModelRouter(
					routes,
					"default-fallback",
					fallback,
					nil,
					never,
					testMetrics,
					testDateTime,
				)
				out := captureStderr(func() {
					mux.ServeHTTP(rec, post(`{"model":"opus"}`))
				})
				Expect(out).NotTo(ContainSubstring("[req]"))
				// Request still served end-to-end, just not logged.
				Expect(rec.Body.String()).To(Equal("anthropic"))
			})

			It("always logs non-200 even when the sampler returns false", func() {
				never := liblog.SamplerFunc(func() bool { return false })
				erroring := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusBadGateway)
					_, _ = w.Write([]byte("upstream unavailable"))
				})
				erroringRoute := []handler.ModelRoute{
					{
						Pattern:      "claude-*",
						ProviderName: "anthropic-subscription",
						Handler:      erroring,
					},
				}
				mux = handler.NewModelRouter(
					erroringRoute,
					"default-fallback",
					fallback,
					nil,
					never,
					testMetrics,
					testDateTime,
				)
				out := captureStderr(func() {
					mux.ServeHTTP(rec, post(`{"model":"claude-opus-4-7"}`))
				})
				Expect(out).To(ContainSubstring("status=502"))
			})
		})
	})

	// Anti-fake: upstream token numbers are varied across all cases —
	// a hardcoded constant extractor must fail these specs (spec 004 AC 8).
	Context("token usage in [req] line", func() {
		BeforeEach(func() {
			_ = flag.Set("logtostderr", "true")
			_ = flag.Set("v", "2")
		})

		It("appends in=<N> out=<N> for an SSE 200 response matching upstream usage", func() {
			// Distinct numbers: input=42, output=17 (different from JSON case below).
			streaming := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(
					[]byte(
						"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":42,\"output_tokens\":17}}\n\n",
					),
				)
			})
			streamRoutes := []handler.ModelRoute{
				{Pattern: "claude-*", ProviderName: "anthropic-subscription", Handler: streaming},
			}
			mux = handler.NewModelRouter(
				streamRoutes,
				"default-fallback",
				fallback,
				nil,
				alwaysSample,
				testMetrics,
				testDateTime,
			)
			out := captureStderr(func() {
				mux.ServeHTTP(rec, post(`{"model":"claude-opus-4-7"}`))
			})
			// Parse in=/out= from the line so a hardcoded append fails.
			re := regexp.MustCompile(`in=(\d+) out=(\d+)`)
			matches := re.FindStringSubmatch(out)
			Expect(matches).To(HaveLen(3), "expected in=<N> out=<N> in: %s", out)
			Expect(matches[1]).To(Equal("42"))
			Expect(matches[2]).To(Equal("17"))
		})

		It("appends in=<N> out=<N> for a non-streaming JSON 200 response", func() {
			// Distinct numbers: input=100, output=5 (different from SSE case above).
			jsonHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(
					[]byte(`{"id":"msg_01","usage":{"input_tokens":100,"output_tokens":5}}`),
				)
			})
			jsonRoutes := []handler.ModelRoute{
				{Pattern: "claude-*", ProviderName: "anthropic-subscription", Handler: jsonHandler},
			}
			mux = handler.NewModelRouter(
				jsonRoutes,
				"default-fallback",
				fallback,
				nil,
				alwaysSample,
				testMetrics,
				testDateTime,
			)
			out := captureStderr(func() {
				mux.ServeHTTP(rec, post(`{"model":"claude-opus-4-7"}`))
			})
			re := regexp.MustCompile(`in=(\d+) out=(\d+)`)
			matches := re.FindStringSubmatch(out)
			Expect(matches).To(HaveLen(3), "expected in=<N> out=<N> in: %s", out)
			Expect(matches[1]).To(Equal("100"))
			Expect(matches[2]).To(Equal("5"))
		})

		It("appends in=- out=- for a 200 response with no parseable usage", func() {
			jsonHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"ok":true}`))
			})
			jsonRoutes := []handler.ModelRoute{
				{Pattern: "claude-*", ProviderName: "anthropic-subscription", Handler: jsonHandler},
			}
			mux = handler.NewModelRouter(
				jsonRoutes,
				"default-fallback",
				fallback,
				nil,
				alwaysSample,
				testMetrics,
				testDateTime,
			)
			out := captureStderr(func() {
				mux.ServeHTTP(rec, post(`{"model":"claude-opus-4-7"}`))
			})
			Expect(out).To(ContainSubstring("in=- out=-"))
		})

		It("appends in=- out=- for a non-200 (502) response", func() {
			erroring := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte("upstream unavailable"))
			})
			erroringRoute := []handler.ModelRoute{
				{Pattern: "claude-*", ProviderName: "anthropic-subscription", Handler: erroring},
			}
			mux = handler.NewModelRouter(
				erroringRoute,
				"default-fallback",
				fallback,
				nil,
				alwaysSample,
				testMetrics,
				testDateTime,
			)
			out := captureStderr(func() {
				mux.ServeHTTP(rec, post(`{"model":"claude-opus-4-7"}`))
			})
			Expect(out).To(ContainSubstring("status=502"))
			Expect(out).To(ContainSubstring("in=- out=-"))
		})

		It("appends in=/out= for an alias-hit 200 SSE response", func() {
			// Distinct numbers: input=7, output=3.
			streaming := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(
					[]byte(
						"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}\n\n",
					),
				)
			})
			streamRoutes := []handler.ModelRoute{
				{Pattern: "MiniMax-*", ProviderName: "minimax", Handler: streaming},
			}
			aliases := map[string]string{"m3": "MiniMax-M3-highspeed"}
			mux = handler.NewModelRouter(
				streamRoutes,
				"default-fallback",
				fallback,
				aliases,
				alwaysSample,
				testMetrics,
				testDateTime,
			)
			out := captureStderr(func() {
				mux.ServeHTTP(rec, post(`{"model":"m3"}`))
			})
			Expect(
				out,
			).To(MatchRegexp(`\[req\] POST /v1/messages model=m3 alias=MiniMax-M3-highspeed provider=minimax status=200 latency=\d+m?s`))
			re := regexp.MustCompile(`in=(\d+) out=(\d+)`)
			matches := re.FindStringSubmatch(out)
			Expect(matches).To(HaveLen(3), "expected in=<N> out=<N> in: %s", out)
			Expect(matches[1]).To(Equal("7"))
			Expect(matches[2]).To(Equal("3"))
		})

		It("preserves the existing [req] field order with in=/out= appended at the end", func() {
			streaming := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(
					[]byte(
						"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":9,\"output_tokens\":2}}\n\n",
					),
				)
			})
			streamRoutes := []handler.ModelRoute{
				{Pattern: "claude-*", ProviderName: "anthropic-subscription", Handler: streaming},
			}
			mux = handler.NewModelRouter(
				streamRoutes,
				"default-fallback",
				fallback,
				nil,
				alwaysSample,
				testMetrics,
				testDateTime,
			)
			out := captureStderr(func() {
				mux.ServeHTTP(rec, post(`{"model":"claude-opus-4-7"}`))
			})
			// Non-alias variant: model= before provider=, in=/out= after latency=.
			Expect(out).To(MatchRegexp(
				`\[req\] POST /v1/messages model=\S+ provider=\S+ status=200 latency=\d+m?s in=\d+ out=\d+`,
			))
		})

		It("does not extract usage on a suppressed 200 (sampler returns false)", func() {
			never := liblog.SamplerFunc(func() bool { return false })
			streaming := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(
					[]byte(
						"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":999,\"output_tokens\":888}}\n\n",
					),
				)
			})
			streamRoutes := []handler.ModelRoute{
				{Pattern: "claude-*", ProviderName: "anthropic-subscription", Handler: streaming},
			}
			mux = handler.NewModelRouter(
				streamRoutes,
				"default-fallback",
				fallback,
				nil,
				never,
				testMetrics,
				testDateTime,
			)
			out := captureStderr(func() {
				mux.ServeHTTP(rec, post(`{"model":"claude-opus-4-7"}`))
			})
			// No [req] line at all when sampler suppresses.
			Expect(out).NotTo(ContainSubstring("[req]"))
			// Request still served.
			Expect(rec.Code).To(Equal(http.StatusOK))
		})
	})

	Context("metrics", func() {
		var m *handler.Metrics

		BeforeEach(func() {
			m = handler.NewMetrics(nil)
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
		})

		It("increments requests_total counter on a successful dispatch", func() {
			mux.ServeHTTP(rec, post(`{"model":"MiniMax-M3-highspeed"}`))
			Expect(
				testutil.ToFloat64(
					m.RequestsTotal.WithLabelValues("minimax", "MiniMax-M3-highspeed", "2xx"),
				),
			).To(Equal(float64(1)))
		})

		It("records one histogram observation after a dispatch", func() {
			before := testutil.CollectAndCount(m.RequestDuration)
			mux.ServeHTTP(rec, post(`{"model":"MiniMax-M3-highspeed"}`))
			after := testutil.CollectAndCount(m.RequestDuration)
			Expect(after - before).To(Equal(1))
		})

		It("increments alias_resolutions_total on an alias hit", func() {
			aliases := map[string]string{"m3": "MiniMax-M3-highspeed"}
			mux = handler.NewModelRouter(
				routes,
				"default-fallback",
				fallback,
				aliases,
				alwaysSample,
				m,
				testDateTime,
			)
			mux.ServeHTTP(rec, post(`{"model":"m3"}`))
			Expect(
				testutil.ToFloat64(
					m.AliasResolutions.WithLabelValues("m3", "MiniMax-M3-highspeed"),
				),
			).To(Equal(float64(1)))
		})
	})

	Context("MaxRequestBodyBytes", func() {
		// prefix/suffix overhead: len(`{"model":"claude-opus-4-7","pad":"`) + len(`"}`) = 36
		const (
			bodyPrefix   = `{"model":"claude-opus-4-7","pad":"`
			bodySuffix   = `"}`
			bodyOverhead = len(bodyPrefix) + len(bodySuffix) // 36
		)

		It("allows a body just under 32 MB", func() {
			padding := strings.Repeat("x", (32<<20)-bodyOverhead-1) // body = (32<<20)-1 bytes
			mux.ServeHTTP(rec, post(bodyPrefix+padding+bodySuffix))
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(rec.Body.String()).To(Equal("anthropic"))
		})

		It("allows a body exactly at 32 MB (boundary inclusive)", func() {
			padding := strings.Repeat("x", (32<<20)-bodyOverhead) // body = 32<<20 bytes
			mux.ServeHTTP(rec, post(bodyPrefix+padding+bodySuffix))
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(rec.Body.String()).To(Equal("anthropic"))
		})

		It("rejects a body just over 32 MB with 413 without leaking the limit", func() {
			padding := strings.Repeat("x", (32<<20)-bodyOverhead+1) // body = (32<<20)+1 bytes
			mux.ServeHTTP(rec, post(bodyPrefix+padding+bodySuffix))
			Expect(rec.Code).To(Equal(http.StatusRequestEntityTooLarge))
			Expect(rec.Body.String()).To(ContainSubstring("request body too large"))
			// must not echo the numeric limit back to the caller
			Expect(rec.Body.String()).NotTo(ContainSubstring("33554432"))
		})
	})

	Context("SSE flush passthrough (regression)", func() {
		It("forwards http.NewResponseController().Flush() to the underlying writer", func() {
			// Repro for the /compact-stuck-at-95% bug: the inner handler
			// represents Anthropic's SSE upstream (writes a chunk, then
			// calls the response controller's Flush — exactly what
			// httputil.ReverseProxy does between SSE chunks). The model-
			// router wraps the writer in *statusRecorder; without an
			// Unwrap method the Flush call cannot reach the underlying
			// writer and bytes pile up in an intermediate buffer.
			spy := &flushTrackingWriter{ResponseRecorder: httptest.NewRecorder()}

			streaming := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("data: hello\n\n"))
				Expect(http.NewResponseController(w).Flush()).To(Succeed())
			})
			streamRoutes := []handler.ModelRoute{
				{Pattern: "claude-*", ProviderName: "anthropic-subscription", Handler: streaming},
			}
			mux = handler.NewModelRouter(
				streamRoutes,
				"default-fallback",
				fallback,
				nil,
				alwaysSample,
				handler.NewMetrics(nil),
				testDateTime,
			)
			mux.ServeHTTP(spy, post(`{"model":"claude-opus-4-7"}`))

			Expect(spy.flushed).To(
				BeNumerically(">", 0),
				"Flush did not reach the underlying writer — statusRecorder.Unwrap missing?",
			)
		})
	})
})

// flushTrackingWriter is an http.ResponseWriter that counts Flush calls.
// Used by the SSE-flush regression spec to assert that
// statusRecorder.Unwrap routes http.NewResponseController(w).Flush()
// through to the underlying writer.
type flushTrackingWriter struct {
	*httptest.ResponseRecorder
	flushed int
}

func (f *flushTrackingWriter) Flush() {
	f.flushed++
	f.ResponseRecorder.Flush()
}

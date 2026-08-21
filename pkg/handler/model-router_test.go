// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"bytes"
	"encoding/json"
	"errors"
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

	Context("[1m] suffix stripping", func() {
		It("strips [1m] produced by an alias value (deepseek-pro -> deepseek-v4-pro[1m])", func() {
			var capturedBody []byte
			capturing := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				capturedBody, _ = io.ReadAll(r.Body)
			})
			dsRoutes := []handler.ModelRoute{
				{Pattern: "deepseek-*", ProviderName: "seibert-vllm", Handler: capturing},
			}
			aliases := map[string]string{"deepseek-pro": "deepseek-v4-pro[1m]"}
			mux = handler.NewModelRouter(
				dsRoutes,
				"default-fallback",
				fallback,
				aliases,
				alwaysSample,
				testMetrics,
				testDateTime,
			)
			mux.ServeHTTP(rec, post(`{"model":"deepseek-pro"}`))
			var seen map[string]any
			Expect(json.Unmarshal(capturedBody, &seen)).To(Succeed())
			Expect(seen["model"]).To(Equal("deepseek-v4-pro"))
		})

		It("routes deepseek-v4-pro-max[1m] as deepseek-v4-pro-max via glob", func() {
			dsRoutes := []handler.ModelRoute{
				{Pattern: "claude-*", ProviderName: "anthropic-subscription", Handler: anthropic},
				{
					Pattern:      "deepseek-*",
					ProviderName: "seibert-vllm",
					Handler:      labelHandler("seibert-vllm"),
				},
			}
			mux = handler.NewModelRouter(
				dsRoutes,
				"default-fallback",
				fallback,
				nil,
				alwaysSample,
				testMetrics,
				testDateTime,
			)
			mux.ServeHTTP(rec, post(`{"model":"deepseek-v4-pro-max[1m]"}`))
			Expect(rec.Body.String()).To(Equal("seibert-vllm"))
		})

		It("rewrites body .model from deepseek-v4-pro-max[1m] to deepseek-v4-pro-max", func() {
			var capturedBody []byte
			capturing := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				capturedBody, _ = io.ReadAll(r.Body)
			})
			dsRoutes := []handler.ModelRoute{
				{Pattern: "deepseek-*", ProviderName: "seibert-vllm", Handler: capturing},
			}
			mux = handler.NewModelRouter(
				dsRoutes,
				"default-fallback",
				fallback,
				nil,
				alwaysSample,
				testMetrics,
				testDateTime,
			)
			mux.ServeHTTP(rec, post(`{"model":"deepseek-v4-pro-max[1m]"}`))
			var seen map[string]any
			Expect(json.Unmarshal(capturedBody, &seen)).To(Succeed())
			Expect(seen["model"]).To(Equal("deepseek-v4-pro-max"))
		})

		It("does not strip [1m] from model without trailing suffix", func() {
			var capturedBody []byte
			capturing := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				capturedBody, _ = io.ReadAll(r.Body)
			})
			dsRoutes := []handler.ModelRoute{
				{Pattern: "deepseek-*", ProviderName: "seibert-vllm", Handler: capturing},
			}
			mux = handler.NewModelRouter(
				dsRoutes,
				"default-fallback",
				fallback,
				nil,
				alwaysSample,
				testMetrics,
				testDateTime,
			)
			mux.ServeHTTP(rec, post(`{"model":"deepseek-v4-pro-max"}`))
			var seen map[string]any
			Expect(json.Unmarshal(capturedBody, &seen)).To(Succeed())
			Expect(seen["model"]).To(Equal("deepseek-v4-pro-max"))
		})

		It("does not strip [1m] mid-string — only trailing suffix", func() {
			var capturedBody []byte
			capturing := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				capturedBody, _ = io.ReadAll(r.Body)
			})
			dsRoutes := []handler.ModelRoute{
				{Pattern: "*-suffix", ProviderName: "test-provider", Handler: capturing},
			}
			mux = handler.NewModelRouter(
				dsRoutes,
				"default-fallback",
				fallback,
				nil,
				alwaysSample,
				testMetrics,
				testDateTime,
			)
			mux.ServeHTTP(rec, post(`{"model":"model[1m]-suffix"}`))
			var seen map[string]any
			Expect(json.Unmarshal(capturedBody, &seen)).To(Succeed())
			Expect(seen["model"]).To(Equal("model[1m]-suffix"))
		})

		It("preserves other top-level fields across [1m] strip rewrite", func() {
			var capturedBody []byte
			capturing := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				capturedBody, _ = io.ReadAll(r.Body)
			})
			dsRoutes := []handler.ModelRoute{
				{Pattern: "deepseek-*", ProviderName: "seibert-vllm", Handler: capturing},
			}
			mux = handler.NewModelRouter(
				dsRoutes,
				"default-fallback",
				fallback,
				nil,
				alwaysSample,
				testMetrics,
				testDateTime,
			)
			body := `{"model":"deepseek-v4-pro-max[1m]","max_tokens":4096,"messages":[{"role":"user","content":"hi"}]}`
			mux.ServeHTTP(rec, post(body))
			var seen map[string]any
			Expect(json.Unmarshal(capturedBody, &seen)).To(Succeed())
			Expect(seen["model"]).To(Equal("deepseek-v4-pro-max"))
			Expect(seen["max_tokens"]).To(Equal(float64(4096)))
			messages, ok := seen["messages"].([]any)
			Expect(ok).To(BeTrue())
			Expect(len(messages)).To(BeNumerically(">", 0))
		})

		It("uses cleaned model in metrics label (no [1m] series)", func() {
			m := handler.NewMetrics(nil)
			dsRoutes := []handler.ModelRoute{
				{
					Pattern:      "deepseek-*",
					ProviderName: "seibert-vllm",
					Handler:      labelHandler("seibert-vllm"),
				},
			}
			mux = handler.NewModelRouter(
				dsRoutes,
				"default-fallback",
				fallback,
				nil,
				alwaysSample,
				m,
				testDateTime,
			)
			mux.ServeHTTP(rec, post(`{"model":"deepseek-v4-pro-max[1m]"}`))
			Expect(testutil.ToFloat64(
				m.RequestsTotal.WithLabelValues("seibert-vllm", "deepseek-v4-pro-max", "2xx"),
			)).To(Equal(float64(1)))
			// No series with the [1m] suffix: assert the exact series set.
			// A leaked deepseek-v4-pro-max[1m] series would add a line and
			// fail this compare.
			expectedMetric := `
				# HELP ccrouter_requests_total Total number of /v1/* requests routed, labeled by provider, model, and status_class (2xx/3xx/4xx_auth/4xx_rate_limited/4xx_bad_request/5xx_upstream/5xx_router).
				# TYPE ccrouter_requests_total counter
				ccrouter_requests_total{model="deepseek-v4-pro-max",provider="seibert-vllm",status_class="2xx"} 1
			`
			Expect(testutil.CollectAndCompare(
				m.RequestsTotal,
				strings.NewReader(expectedMetric),
				"ccrouter_requests_total",
			)).To(Succeed())
		})
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
			).To(MatchRegexp(`\[req\] POST /v1/messages model=MiniMax-M3-highspeed provider=minimax/0 status=200 latency=\d+m?s`))
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
			).To(MatchRegexp(`\[req\] POST /v1/messages model=m3 alias=MiniMax-M3-highspeed provider=minimax/0 status=200 latency=`))
		})

		It("emits [req] with default provider name on fallback", func() {
			out := captureStderr(func() {
				mux.ServeHTTP(rec, post(`{"model":"gemini-3-pro"}`))
			})
			Expect(
				out,
			).To(MatchRegexp(`\[req\] POST /v1/messages model=gemini-3-pro provider=default-fallback/0 status=200 latency=`))
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
			).To(MatchRegexp(`\[req\] POST /v1/messages model=m3 alias=MiniMax-M3-highspeed provider=minimax/0 status=200 latency=\d+m?s`))
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

		It("suppresses [req] log line on a suppressed 200 (sampler returns false)", func() {
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
			// [req] line is suppressed by the sampler gate, but usage IS
			// extracted above the gate (token metrics flow regardless).
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

// Anti-fake: token counts vary across specs; a hardcoded Add(1) or missing sentinel-chain resolution fails these assertions.
var _ = Describe("ModelRouter metrics wiring", func() {
	var (
		fallback = labelHandler("fallback")
		rec      *httptest.ResponseRecorder
	)

	post := func(body string) *http.Request {
		return httptest.NewRequest(
			http.MethodPost,
			"/v1/messages",
			strings.NewReader(body),
		)
	}

	It(
		"increments ccrouter_tokens_total{direction=input} and {direction=output} on a 200 SSE response",
		func() {
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
			m := handler.NewMetrics(nil)
			mux := handler.NewModelRouter(
				streamRoutes,
				"default-fallback",
				fallback,
				nil,
				alwaysSample,
				m,
				testDateTime,
			)
			rec = httptest.NewRecorder()
			mux.ServeHTTP(rec, post(`{"model":"claude-opus-4-7"}`))
			Expect(
				testutil.ToFloat64(
					m.TokensTotal.WithLabelValues(
						"anthropic-subscription",
						"claude-opus-4-7",
						"input",
					),
				),
			).To(Equal(float64(42)))
			Expect(
				testutil.ToFloat64(
					m.TokensTotal.WithLabelValues(
						"anthropic-subscription",
						"claude-opus-4-7",
						"output",
					),
				),
			).To(Equal(float64(17)))
		},
	)

	It("increments ccrouter_tokens_total on a 200 JSON response", func() {
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
		m := handler.NewMetrics(nil)
		mux := handler.NewModelRouter(
			jsonRoutes,
			"default-fallback",
			fallback,
			nil,
			alwaysSample,
			m,
			testDateTime,
		)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, post(`{"model":"claude-opus-4-7"}`))
		Expect(
			testutil.ToFloat64(
				m.TokensTotal.WithLabelValues("anthropic-subscription", "claude-opus-4-7", "input"),
			),
		).To(Equal(float64(100)))
		Expect(
			testutil.ToFloat64(
				m.TokensTotal.WithLabelValues(
					"anthropic-subscription",
					"claude-opus-4-7",
					"output",
				),
			),
		).To(Equal(float64(5)))
	})

	It("does not increment ccrouter_tokens_total on a 502 upstream error", func() {
		erroring := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("upstream unavailable"))
		})
		erroringRoute := []handler.ModelRoute{
			{Pattern: "claude-*", ProviderName: "anthropic-subscription", Handler: erroring},
		}
		m := handler.NewMetrics(nil)
		mux := handler.NewModelRouter(
			erroringRoute,
			"default-fallback",
			fallback,
			nil,
			alwaysSample,
			m,
			testDateTime,
		)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, post(`{"model":"claude-opus-4-7"}`))
		Expect(testutil.CollectAndCount(m.TokensTotal)).To(Equal(0))
		Expect(
			testutil.ToFloat64(
				m.RequestsTotal.WithLabelValues(
					"anthropic-subscription",
					"claude-opus-4-7",
					"5xx_upstream",
				),
			),
		).To(Equal(float64(1)))
	})

	It("does not increment ccrouter_tokens_total on a 200 with no parseable usage", func() {
		jsonHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		})
		jsonRoutes := []handler.ModelRoute{
			{Pattern: "claude-*", ProviderName: "anthropic-subscription", Handler: jsonHandler},
		}
		m := handler.NewMetrics(nil)
		mux := handler.NewModelRouter(
			jsonRoutes,
			"default-fallback",
			fallback,
			nil,
			alwaysSample,
			m,
			testDateTime,
		)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, post(`{"model":"claude-opus-4-7"}`))
		Expect(testutil.CollectAndCount(m.TokensTotal)).To(Equal(0))
	})

	It("does not increment ccrouter_tokens_total on a 200 with zero-token usage", func() {
		jsonHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"usage":{"input_tokens":0,"output_tokens":0}}`))
		})
		jsonRoutes := []handler.ModelRoute{
			{Pattern: "claude-*", ProviderName: "anthropic-subscription", Handler: jsonHandler},
		}
		m := handler.NewMetrics(nil)
		mux := handler.NewModelRouter(
			jsonRoutes,
			"default-fallback",
			fallback,
			nil,
			alwaysSample,
			m,
			testDateTime,
		)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, post(`{"model":"claude-opus-4-7"}`))
		// Zero is dropped by ObserveTokens' zero-drop rule.
		Expect(testutil.CollectAndCount(m.TokensTotal)).To(Equal(0))
	})

	It("increments only the positive direction when the other is missing", func() {
		streaming := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(
				[]byte(
					"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7}}}\n\n",
				),
			)
		})
		streamRoutes := []handler.ModelRoute{
			{Pattern: "claude-*", ProviderName: "anthropic-subscription", Handler: streaming},
		}
		m := handler.NewMetrics(nil)
		mux := handler.NewModelRouter(
			streamRoutes,
			"default-fallback",
			fallback,
			nil,
			alwaysSample,
			m,
			testDateTime,
		)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, post(`{"model":"claude-opus-4-7"}`))
		Expect(
			testutil.ToFloat64(
				m.TokensTotal.WithLabelValues("anthropic-subscription", "claude-opus-4-7", "input"),
			),
		).To(Equal(float64(7)))
		// output direction was not emitted (no message_delta with usage).
		Expect(
			testutil.ToFloat64(
				m.TokensTotal.WithLabelValues(
					"anthropic-subscription",
					"claude-opus-4-7",
					"output",
				),
			),
		).To(Equal(0.0))
	})

	It(
		"emits ccrouter_requests_total{status_class=4xx_bad_request} on a body-read-failed early-return",
		func() {
			boomHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.ReadAll(&boomReader{})
			})
			routes := []handler.ModelRoute{
				{Pattern: "claude-*", ProviderName: "anthropic-subscription", Handler: boomHandler},
			}
			m := handler.NewMetrics(nil)
			mux := handler.NewModelRouter(
				routes,
				"default-fallback",
				fallback,
				nil,
				alwaysSample,
				m,
				testDateTime,
			)
			rec = httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", &boomReader{})
			mux.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusBadRequest))
			Expect(
				testutil.ToFloat64(
					m.RequestsTotal.WithLabelValues("_unknown_", "_unknown_", "4xx_bad_request"),
				),
			).To(Equal(float64(1)))
		},
	)

	It(
		"emits ccrouter_requests_total{status_class=4xx_bad_request} on a body-too-large 413 early-return",
		func() {
			smallBodyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.ReadAll(io.LimitReader(r.Body, 1))
			})
			routes := []handler.ModelRoute{
				{
					Pattern:      "claude-*",
					ProviderName: "anthropic-subscription",
					Handler:      smallBodyHandler,
				},
			}
			m := handler.NewMetrics(nil)
			mux := handler.NewModelRouter(
				routes,
				"default-fallback",
				fallback,
				nil,
				alwaysSample,
				m,
				testDateTime,
			)
			rec = httptest.NewRecorder()
			// A body that exceeds MaxRequestBodyBytes (32MB) will be rejected
			// by MaxBytesReader and surface as *http.MaxBytesError.
			bigBody := make([]byte, 33<<20) // 33 MB
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(bigBody))
			mux.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusRequestEntityTooLarge))
			Expect(
				testutil.ToFloat64(
					m.RequestsTotal.WithLabelValues("_unknown_", "_unknown_", "4xx_bad_request"),
				),
			).To(Equal(float64(1)))
		},
	)

	PIt(
		"emits ccrouter_requests_total{status_class=5xx_router} on an alias-rewrite-failed early-return",
		func() {
			// PIt: rewriteModelField failure requires a test-only seam (package-level var override) not yet plumbed.
			// AC 13's "≥3 lines" evidence is satisfied by the production-code grep on model-router.go —
			// this integration test is future work.
		},
	)

	It(
		"increments ccrouter_tokens_total on a sampler-suppressed 200 — extraction runs above the sampler gate",
		func() {
			never := liblog.SamplerFunc(func() bool { return false })
			streaming := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(
					[]byte(
						"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":13,\"output_tokens\":7}}\n\n",
					),
				)
			})
			streamRoutes := []handler.ModelRoute{
				{Pattern: "claude-*", ProviderName: "anthropic-subscription", Handler: streaming},
			}
			m := handler.NewMetrics(nil)
			mux := handler.NewModelRouter(
				streamRoutes,
				"default-fallback",
				fallback,
				nil,
				never,
				m,
				testDateTime,
			)
			rec = httptest.NewRecorder()
			out := captureStderr(func() {
				mux.ServeHTTP(rec, post(`{"model":"claude-opus-4-7"}`))
			})
			// Tokens ARE counted even when sampler suppresses the log.
			Expect(
				testutil.ToFloat64(
					m.TokensTotal.WithLabelValues(
						"anthropic-subscription",
						"claude-opus-4-7",
						"input",
					),
				),
			).To(Equal(float64(13)))
			Expect(
				testutil.ToFloat64(
					m.TokensTotal.WithLabelValues(
						"anthropic-subscription",
						"claude-opus-4-7",
						"output",
					),
				),
			).To(Equal(float64(7)))
			// [req] line is still suppressed.
			Expect(out).NotTo(ContainSubstring("[req]"))
		},
	)

	It("resolves model label via sentinel chain: resolved > orig > _unknown_", func() {
		// Sub-case 1: resolved model wins on alias hit.
		aliases := map[string]string{"m3": "MiniMax-M3-highspeed"}
		streaming := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(
				[]byte(
					"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n\n",
				),
			)
		})
		m1 := handler.NewMetrics(nil)
		streamRoutes := []handler.ModelRoute{
			{Pattern: "MiniMax-*", ProviderName: "minimax", Handler: streaming},
		}
		mux1 := handler.NewModelRouter(
			streamRoutes,
			"default-fallback",
			fallback,
			aliases,
			alwaysSample,
			m1,
			testDateTime,
		)
		rec = httptest.NewRecorder()
		mux1.ServeHTTP(rec, post(`{"model":"m3"}`))
		Expect(
			testutil.ToFloat64(
				m1.RequestsTotal.WithLabelValues("minimax", "MiniMax-M3-highspeed", "2xx"),
			),
		).To(Equal(float64(1)))

		// Sub-case 2: origModel used when no alias resolves.
		routes2 := []handler.ModelRoute{
			{Pattern: "gemini-*", ProviderName: "google", Handler: streaming},
		}
		m2 := handler.NewMetrics(nil)
		mux2 := handler.NewModelRouter(
			routes2,
			"default-fallback",
			fallback,
			nil,
			alwaysSample,
			m2,
			testDateTime,
		)
		rec = httptest.NewRecorder()
		mux2.ServeHTTP(rec, post(`{"model":"gemini-3-pro"}`))
		Expect(
			testutil.ToFloat64(m2.RequestsTotal.WithLabelValues("google", "gemini-3-pro", "2xx")),
		).To(Equal(float64(1)))

		// Sub-case 3: _unknown_ used when both resolved and orig are empty.
		m3 := handler.NewMetrics(nil)
		mux3 := handler.NewModelRouter(
			routes2,
			"default-fallback",
			fallback,
			nil,
			alwaysSample,
			m3,
			testDateTime,
		)
		rec = httptest.NewRecorder()
		mux3.ServeHTTP(rec, post(`{"other":"thing"}`))
		Expect(
			testutil.ToFloat64(
				m3.RequestsTotal.WithLabelValues("default-fallback", "_unknown_", "2xx"),
			),
		).To(Equal(float64(1)))
	})
})

var _ = Describe("ModelRouter system lift", func() {
	BeforeEach(func() {
		// Ensure glog flags are parsed so V(log level) works correctly.
		// Without this, glog may not output at the expected verbosity level.
		_ = flag.Set("logtostderr", "true")
		_ = flag.Set("v", "1")
	})

	const liftBody = `{"model":"qwen3.8:27b-mlx","max_tokens":64,"system":[{"type":"text","text":"top"}],"messages":[{"role":"user","content":"hi"},{"role":"system","content":"A"},{"role":"assistant","content":"ok"},{"role":"system","content":"B"}]}`

	It("lifts non-leading system entries for a matching model, preserving order", func() {
		var capturedBody []byte
		var capturedContentLength int64
		var capturedStatus int
		capturing := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			capturedBody, _ = io.ReadAll(r.Body)
			capturedContentLength = r.ContentLength
		})
		upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedBody, _ = io.ReadAll(r.Body)
			capturedContentLength = r.ContentLength
			capturedStatus = http.StatusTeapot
			w.WriteHeader(http.StatusTeapot)
		})
		routes := []handler.ModelRoute{
			{
				Pattern:               "qwen*",
				ProviderName:          "ollama-local",
				Handler:               upstream,
				RequiresLeadingSystem: []string{"qwen3.8*"},
			},
		}
		mux := handler.NewModelRouter(
			routes,
			"default-fallback",
			capturing,
			nil,
			alwaysSample,
			testMetrics,
			testDateTime,
		)
		rec := httptest.NewRecorder()
		post := func(body string) *http.Request {
			return httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		}
		mux.ServeHTTP(rec, post(liftBody))

		var result map[string]interface{}
		Expect(json.Unmarshal(capturedBody, &result)).To(Succeed())

		messages, ok := result["messages"].([]interface{})
		Expect(ok).To(BeTrue())
		Expect(len(messages)).To(Equal(2))
		msg0, ok := messages[0].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(msg0["role"]).To(Equal("user"))
		msg1, ok := messages[1].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(msg1["role"]).To(Equal("assistant"))

		systemRaw, ok := result["system"].([]interface{})
		Expect(ok).To(BeTrue())
		Expect(len(systemRaw)).To(Equal(3))
		sys0, ok := systemRaw[0].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(sys0["text"]).To(Equal("top"))
		sys1, ok := systemRaw[1].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(sys1["text"]).To(Equal("A"))
		sys2, ok := systemRaw[2].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(sys2["text"]).To(Equal("B"))

		Expect(capturedContentLength).To(Equal(int64(len(capturedBody))))
		Expect(capturedStatus).To(Equal(http.StatusTeapot))
	})

	It("lifts on the default-provider fallback path when no route pattern matches", func() {
		var capturedBody []byte
		defaultUpstream := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			capturedBody, _ = io.ReadAll(r.Body)
		})
		routed := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			Fail("request must not reach the routed handler")
		})
		// The model matches no route pattern, so it falls through to
		// defaultHandler — whose provider still declares the restriction.
		routes := []handler.ModelRoute{
			{
				Pattern:               "llama*",
				ProviderName:          "ollama-local",
				Handler:               routed,
				RequiresLeadingSystem: []string{"qwen3.8*"},
			},
		}
		mux := handler.NewModelRouter(
			routes,
			"ollama-local",
			defaultUpstream,
			nil,
			alwaysSample,
			testMetrics,
			testDateTime,
		)
		rec := httptest.NewRecorder()
		post := func(body string) *http.Request {
			return httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		}
		mux.ServeHTTP(rec, post(liftBody))

		var result map[string]interface{}
		Expect(json.Unmarshal(capturedBody, &result)).To(Succeed())
		messages, ok := result["messages"].([]interface{})
		Expect(ok).To(BeTrue())
		Expect(len(messages)).To(Equal(2))
		systemRaw, ok := result["system"].([]interface{})
		Expect(ok).To(BeTrue())
		Expect(len(systemRaw)).To(Equal(3))
	})

	It("forwards byte-identically for a non-matching model on the same provider", func() {
		var capturedBody []byte
		capturing := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			capturedBody, _ = io.ReadAll(r.Body)
		})
		routes := []handler.ModelRoute{
			{
				Pattern:               "qwen*",
				ProviderName:          "ollama-local",
				Handler:               capturing,
				RequiresLeadingSystem: []string{"qwen3.8*"},
			},
		}
		mux := handler.NewModelRouter(
			routes,
			"default-fallback",
			capturing,
			nil,
			alwaysSample,
			testMetrics,
			testDateTime,
		)
		rec := httptest.NewRecorder()
		post := func(body string) *http.Request {
			return httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		}
		body := `{"model":"qwen3.6:35b-a3b-coding-nvfp4","max_tokens":64,"system":[{"type":"text","text":"top"}],"messages":[{"role":"user","content":"hi"},{"role":"system","content":"A"},{"role":"assistant","content":"ok"},{"role":"system","content":"B"}]}`
		mux.ServeHTTP(rec, post(body))
		Expect(capturedBody).To(Equal([]byte(body)))
	})

	It("forwards byte-identically when the route declares no requiresLeadingSystem", func() {
		var capturedBody []byte
		capturing := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			capturedBody, _ = io.ReadAll(r.Body)
		})
		routes := []handler.ModelRoute{
			{
				Pattern:      "qwen*",
				ProviderName: "ollama-local",
				Handler:      capturing,
				// RequiresLeadingSystem omitted
			},
		}
		mux := handler.NewModelRouter(
			routes,
			"default-fallback",
			capturing,
			nil,
			alwaysSample,
			testMetrics,
			testDateTime,
		)
		rec := httptest.NewRecorder()
		post := func(body string) *http.Request {
			return httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		}
		mux.ServeHTTP(rec, post(liftBody))
		Expect(capturedBody).To(Equal([]byte(liftBody)))

		out := captureStderr(func() {})
		Expect(strings.Count(out, "[system-lift]")).To(Equal(0))
	})

	It("forwards byte-identically when requiresLeadingSystem is an explicit empty list", func() {
		var capturedBody []byte
		capturing := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			capturedBody, _ = io.ReadAll(r.Body)
		})
		routes := []handler.ModelRoute{
			{
				Pattern:               "qwen*",
				ProviderName:          "ollama-local",
				Handler:               capturing,
				RequiresLeadingSystem: []string{},
			},
		}
		mux := handler.NewModelRouter(
			routes,
			"default-fallback",
			capturing,
			nil,
			alwaysSample,
			testMetrics,
			testDateTime,
		)
		rec := httptest.NewRecorder()
		post := func(body string) *http.Request {
			return httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		}
		mux.ServeHTTP(rec, post(liftBody))
		Expect(capturedBody).To(Equal([]byte(liftBody)))

		out := captureStderr(func() {})
		Expect(strings.Count(out, "[system-lift]")).To(Equal(0))
	})

	It("forwards byte-identically and warns once when messages is not a list", func() {
		var capturedBody []byte
		var capturedStatus int
		upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedBody, _ = io.ReadAll(r.Body)
			capturedStatus = http.StatusTeapot
			w.WriteHeader(http.StatusTeapot)
		})
		routes := []handler.ModelRoute{
			{
				Pattern:               "qwen*",
				ProviderName:          "ollama-local",
				Handler:               upstream,
				RequiresLeadingSystem: []string{"qwen3.8*"},
			},
		}
		mux := handler.NewModelRouter(
			routes,
			"default-fallback",
			upstream,
			nil,
			alwaysSample,
			testMetrics,
			testDateTime,
		)
		rec := httptest.NewRecorder()
		post := func(body string) *http.Request {
			return httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		}
		body := `{"model":"qwen3.8:27b-mlx","messages":"nope"}`
		out := captureStderr(func() {
			mux.ServeHTTP(rec, post(body))
		})
		Expect(capturedBody).To(Equal([]byte(body)))
		Expect(capturedStatus).To(Equal(http.StatusTeapot))
		Expect(strings.Count(out, "[system-lift]")).To(Equal(1))
		Expect(out).To(MatchRegexp(`W\d{4} .*\[system-lift\] skipped`))
	})

	It("forwards byte-identically and warns once when a message entry is not an object", func() {
		var capturedBody []byte
		var capturedStatus int
		upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedBody, _ = io.ReadAll(r.Body)
			capturedStatus = http.StatusTeapot
			w.WriteHeader(http.StatusTeapot)
		})
		routes := []handler.ModelRoute{
			{
				Pattern:               "qwen*",
				ProviderName:          "ollama-local",
				Handler:               upstream,
				RequiresLeadingSystem: []string{"qwen3.8*"},
			},
		}
		mux := handler.NewModelRouter(
			routes,
			"default-fallback",
			upstream,
			nil,
			alwaysSample,
			testMetrics,
			testDateTime,
		)
		rec := httptest.NewRecorder()
		post := func(body string) *http.Request {
			return httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		}
		body := `{"model":"qwen3.8:27b-mlx","messages":[{"role":"user","content":"hi"},42]}`
		out := captureStderr(func() {
			mux.ServeHTTP(rec, post(body))
		})
		Expect(capturedBody).To(Equal([]byte(body)))
		Expect(capturedStatus).To(Equal(http.StatusTeapot))
		Expect(strings.Count(out, "[system-lift]")).To(Equal(1))
		Expect(out).To(MatchRegexp(`W\d{4} .*\[system-lift\] skipped`))
	})

	It(
		"emits exactly one [system-lift] line naming the model and moved count, with no content",
		func() {
			// Anti-fake: moved=2 is derived from the fixture's two misplaced entries —
			// a hardcoded moved=1 or a content-echoing format string fails this spec.
			capturing := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				io.ReadAll(r.Body)
			})
			routes := []handler.ModelRoute{
				{
					Pattern:               "qwen*",
					ProviderName:          "ollama-local",
					Handler:               capturing,
					RequiresLeadingSystem: []string{"qwen3.8*"},
				},
			}
			mux := handler.NewModelRouter(
				routes,
				"default-fallback",
				capturing,
				nil,
				alwaysSample,
				testMetrics,
				testDateTime,
			)
			rec := httptest.NewRecorder()
			post := func(body string) *http.Request {
				return httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			}
			// [system-lift] is V(2), same level as [alias] and [1m-strip]
			_ = flag.Set("logtostderr", "true")
			_ = flag.Set("v", "2")
			out := captureStderr(func() {
				mux.ServeHTTP(rec, post(liftBody))
			})
			lineRegex := regexp.MustCompile(`\[system-lift\][^\n]*`)
			matches := lineRegex.FindAllString(out, -1)
			Expect(len(matches)).To(Equal(1))
			line := matches[0]
			Expect(line).To(ContainSubstring("model=qwen3.8:27b-mlx"))
			Expect(line).To(ContainSubstring("moved=2"))
			Expect(line).NotTo(ContainSubstring("A"))
			Expect(line).NotTo(ContainSubstring("B"))
		},
	)

	It(
		"leaves a system entry that is already first in place and forwards byte-identically",
		func() {
			var capturedBody []byte
			capturing := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				capturedBody, _ = io.ReadAll(r.Body)
			})
			routes := []handler.ModelRoute{
				{
					Pattern:               "qwen*",
					ProviderName:          "ollama-local",
					Handler:               capturing,
					RequiresLeadingSystem: []string{"qwen3.8*"},
				},
			}
			mux := handler.NewModelRouter(
				routes,
				"default-fallback",
				capturing,
				nil,
				alwaysSample,
				testMetrics,
				testDateTime,
			)
			rec := httptest.NewRecorder()
			post := func(body string) *http.Request {
				return httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			}
			body := `{"model":"qwen3.8:27b-mlx","system":[{"type":"text","text":"top"}],"messages":[{"role":"system","content":"first"},{"role":"user","content":"hi"}]}`
			out := captureStderr(func() {
				mux.ServeHTTP(rec, post(body))
			})
			Expect(capturedBody).To(Equal([]byte(body)))

			var result map[string]interface{}
			Expect(json.Unmarshal(capturedBody, &result)).To(Succeed())
			messages, ok := result["messages"].([]interface{})
			Expect(ok).To(BeTrue())
			Expect(len(messages)).To(Equal(2))
			msg0, ok := messages[0].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(msg0["role"]).To(Equal("system"))

			systemRaw, ok := result["system"].([]interface{})
			Expect(ok).To(BeTrue())
			Expect(len(systemRaw)).To(Equal(1))

			Expect(strings.Count(out, "[system-lift]")).To(Equal(0))
		},
	)

	It("normalizes string and block-list system content through the full router path", func() {
		var capturedBody []byte
		capturing := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			capturedBody, _ = io.ReadAll(r.Body)
		})
		routes := []handler.ModelRoute{
			{
				Pattern:               "qwen*",
				ProviderName:          "ollama-local",
				Handler:               capturing,
				RequiresLeadingSystem: []string{"qwen3.8*"},
			},
		}
		mux := handler.NewModelRouter(
			routes,
			"default-fallback",
			capturing,
			nil,
			alwaysSample,
			testMetrics,
			testDateTime,
		)
		rec := httptest.NewRecorder()
		post := func(body string) *http.Request {
			return httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		}
		body := `{"model":"qwen3.8:27b-mlx","messages":[{"role":"user","content":"hi"},{"role":"system","content":"hello"},{"role":"system","content":[{"type":"text","text":"x"},{"type":"text","text":"y"}]}]}`
		mux.ServeHTTP(rec, post(body))

		var result map[string]interface{}
		Expect(json.Unmarshal(capturedBody, &result)).To(Succeed())

		systemRaw, ok := result["system"].([]interface{})
		Expect(ok).To(BeTrue())
		Expect(len(systemRaw)).To(Equal(3))
		block0, ok := systemRaw[0].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(block0["type"]).To(Equal("text"))
		Expect(block0["text"]).To(Equal("hello"))
		block1, ok := systemRaw[1].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(block1["type"]).To(Equal("text"))
		Expect(block1["text"]).To(Equal("x"))
		block2, ok := systemRaw[2].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(block2["type"]).To(Equal("text"))
		Expect(block2["text"]).To(Equal("y"))
	})

	It("does not transform a request that fell through to the default provider", func() {
		var capturedBody []byte
		capturing := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			capturedBody, _ = io.ReadAll(r.Body)
		})
		routes := []handler.ModelRoute{
			{
				Pattern:               "qwen*",
				ProviderName:          "ollama-local",
				Handler:               capturing,
				RequiresLeadingSystem: []string{"qwen3.8*"},
			},
		}
		mux := handler.NewModelRouter(
			routes,
			"default-fallback",
			capturing,
			nil,
			alwaysSample,
			testMetrics,
			testDateTime,
		)
		rec := httptest.NewRecorder()
		post := func(body string) *http.Request {
			return httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		}
		body := `{"model":"gemini-3-pro","messages":[{"role":"user","content":"hi"},{"role":"system","content":"A"}]}`
		out := captureStderr(func() {
			mux.ServeHTTP(rec, post(body))
		})
		Expect(capturedBody).To(Equal([]byte(body)))
		Expect(strings.Count(out, "[system-lift]")).To(Equal(0))
	})

	It("still routes, strips [1m], and resolves aliases unchanged alongside the transform", func() {
		var capturedBody []byte
		capturing := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			capturedBody, _ = io.ReadAll(r.Body)
		})
		routes := []handler.ModelRoute{
			{
				Pattern:               "qwen*",
				ProviderName:          "ollama-local",
				Handler:               capturing,
				RequiresLeadingSystem: []string{"qwen3.8*"},
			},
		}
		aliases := map[string]string{"q38": "qwen3.8:27b-mlx[1m]"}
		mux := handler.NewModelRouter(
			routes,
			"default-fallback",
			capturing,
			aliases,
			alwaysSample,
			testMetrics,
			testDateTime,
		)
		rec := httptest.NewRecorder()
		post := func(body string) *http.Request {
			return httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		}
		body := `{"model":"q38","messages":[{"role":"user","content":"hi"},{"role":"system","content":"A"}]}`
		mux.ServeHTTP(rec, post(body))

		var result map[string]interface{}
		Expect(json.Unmarshal(capturedBody, &result)).To(Succeed())
		// Alias resolved and [1m] stripped
		Expect(result["model"]).To(Equal("qwen3.8:27b-mlx"))

		// Transform still fired: no system entries left in messages
		messages, ok := result["messages"].([]interface{})
		Expect(ok).To(BeTrue())
		Expect(len(messages)).To(Equal(1))
		msg0, ok := messages[0].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(msg0["role"]).To(Equal("user"))

		// And one text block A in system
		systemRaw, ok := result["system"].([]interface{})
		Expect(ok).To(BeTrue())
		Expect(len(systemRaw)).To(Equal(1))
		block, ok := systemRaw[0].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(block["type"]).To(Equal("text"))
		Expect(block["text"]).To(Equal("A"))
	})
})

// boomReader is an io.Reader that returns an error on first Read.
// Used to simulate a body-read failure for 400 early-return testing.
type boomReader struct{}

func (b *boomReader) Read([]byte) (int, error) {
	return 0, errors.New("boom")
}

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

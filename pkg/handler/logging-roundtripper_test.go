// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"

	liblog "github.com/bborbe/log"
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
		rt := handler.NewLoggingRoundTripper(nil, liblog.NewSamplerTrue())
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
		rt := handler.NewLoggingRoundTripper(inner, liblog.NewSamplerTrue())
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
		rt := handler.NewLoggingRoundTripper(inner, liblog.NewSamplerTrue())
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
				liblog.NewSamplerTrue(),
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

	Context("V(4) body sample logging", func() {
		BeforeEach(func() {
			_ = flag.Set("logtostderr", "true")
		})

		AfterEach(func() {
			_ = flag.Set("v", "0")
		})

		makeRTWithBody := func(respBody string) http.RoundTripper {
			return handler.NewLoggingRoundTripper(
				roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(strings.NewReader(respBody)),
					}, nil
				}),
				liblog.NewSamplerTrue(),
			)
		}

		It("emits [upstream.req.body] with Bearer redacted at V(4)", func() {
			_ = flag.Set("v", "4")
			rt := makeRTWithBody("")
			body := strings.NewReader("Authorization: Bearer leak-canary-v4-req")
			req := httptest.NewRequest(http.MethodPost, "https://api.example.com/v1/messages", body)
			out := captureStderr(func() {
				resp, _ := rt.RoundTrip(req)
				if resp != nil && resp.Body != nil {
					_, _ = io.ReadAll(resp.Body)
					_ = resp.Body.Close()
				}
			})
			Expect(out).To(ContainSubstring("[upstream.req.body]"))
			Expect(out).To(ContainSubstring("Bearer <redacted>"))
			Expect(out).NotTo(ContainSubstring("leak-canary-v4-req"))
		})

		It("emits [upstream.resp.body] with Bearer redacted after ReadAll+Close at V(4)", func() {
			_ = flag.Set("v", "4")
			rt := makeRTWithBody("Bearer leak-canary-v4-resp data here")
			req := httptest.NewRequest(http.MethodPost, "https://api.example.com/v1/messages", nil)
			out := captureStderr(func() {
				resp, _ := rt.RoundTrip(req)
				if resp != nil && resp.Body != nil {
					_, _ = io.ReadAll(resp.Body)
					_ = resp.Body.Close()
				}
			})
			Expect(out).To(ContainSubstring("[upstream.resp.body]"))
			Expect(out).To(ContainSubstring("Bearer <redacted>"))
			Expect(out).NotTo(ContainSubstring("leak-canary-v4-resp"))
		})

		It("does not emit [upstream.req.body] or [upstream.resp.body] at V(1)", func() {
			_ = flag.Set("v", "1")
			rt := makeRTWithBody("Bearer some-token")
			body := strings.NewReader("Bearer some-req-token")
			req := httptest.NewRequest(http.MethodPost, "https://api.example.com/v1/messages", body)
			out := captureStderr(func() {
				resp, _ := rt.RoundTrip(req)
				if resp != nil && resp.Body != nil {
					_, _ = io.ReadAll(resp.Body)
					_ = resp.Body.Close()
				}
			})
			Expect(out).NotTo(ContainSubstring("[upstream.req.body]"))
			Expect(out).NotTo(ContainSubstring("[upstream.resp.body]"))
		})

		It("does not emit [upstream.req.body] or [upstream.resp.body] at V(3)", func() {
			_ = flag.Set("v", "3")
			rt := makeRTWithBody("Bearer some-token")
			body := strings.NewReader("Bearer some-req-token")
			req := httptest.NewRequest(http.MethodPost, "https://api.example.com/v1/messages", body)
			out := captureStderr(func() {
				resp, _ := rt.RoundTrip(req)
				if resp != nil && resp.Body != nil {
					_, _ = io.ReadAll(resp.Body)
					_ = resp.Body.Close()
				}
			})
			Expect(out).NotTo(ContainSubstring("[upstream.req.body]"))
			Expect(out).NotTo(ContainSubstring("[upstream.resp.body]"))
		})

		It("emits [upstream.req.body] for all 5 rapid-fire calls at V(5) with TrueSampler", func() {
			_ = flag.Set("v", "5")
			rt := makeRTWithBody("")
			out := captureStderr(func() {
				for i := 0; i < 5; i++ {
					body := strings.NewReader("payload")
					req := httptest.NewRequest(
						http.MethodPost,
						"https://api.example.com/v1/messages",
						body,
					)
					resp, _ := rt.RoundTrip(req)
					if resp != nil && resp.Body != nil {
						_ = resp.Body.Close()
					}
				}
			})
			// Count occurrences of the req.body line — must be exactly 5.
			Expect(strings.Count(out, "[upstream.req.body]")).To(Equal(5))
		})

		It("emits 0 [upstream.req.body] lines when sampler always returns false", func() {
			_ = flag.Set("v", "4")
			falseSampler := liblog.SamplerFunc(func() bool { return false })
			rt := handler.NewLoggingRoundTripper(
				roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(strings.NewReader("")),
					}, nil
				}),
				falseSampler,
			)
			out := captureStderr(func() {
				for i := 0; i < 5; i++ {
					body := strings.NewReader("payload")
					req := httptest.NewRequest(
						http.MethodPost,
						"https://api.example.com/v1/messages",
						body,
					)
					resp, _ := rt.RoundTrip(req)
					if resp != nil && resp.Body != nil {
						_, _ = io.ReadAll(resp.Body)
						_ = resp.Body.Close()
					}
				}
			})
			Expect(out).NotTo(ContainSubstring("[upstream.req.body]"))
			Expect(out).NotTo(ContainSubstring("[upstream.resp.body]"))
		})

		It("truncates the body sample at BodySampleMaxBytes and reports the full body_len", func() {
			_ = flag.Set("v", "4")
			// Build a request body twice the cap to exercise the truncation
			// path; the snippet must be <= BodySampleMaxBytes but body_len
			// must reflect the full length the upstream actually sees.
			fullLen := handler.BodySampleMaxBytes * 2
			big := bytes.Repeat([]byte("a"), fullLen)
			req := httptest.NewRequest(
				http.MethodPost,
				"https://api.example.com/v1/messages",
				bytes.NewReader(big),
			)
			rt := handler.NewLoggingRoundTripper(
				roundTripperFunc(func(r *http.Request) (*http.Response, error) {
					// Drain the body the proxy would forward — must equal fullLen.
					forwarded, _ := io.ReadAll(r.Body)
					Expect(len(forwarded)).To(Equal(fullLen),
						"snippet capture must not consume bytes from the forwarded body")
					return &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(strings.NewReader("")),
					}, nil
				}),
				liblog.NewSamplerTrue(),
			)
			out := captureStderr(func() {
				resp, err := rt.RoundTrip(req)
				Expect(err).NotTo(HaveOccurred())
				_ = resp.Body.Close()
			})
			// body_len=<fullLen> reported, regardless of truncation
			Expect(out).To(ContainSubstring(fmt.Sprintf("body_len=%d", fullLen)))
			// sample= field must not exceed BodySampleMaxBytes worth of `a`s
			sampleRe := regexp.MustCompile(`sample=(a+)`)
			match := sampleRe.FindStringSubmatch(out)
			Expect(match).To(HaveLen(2), "sample= field missing from log line: %s", out)
			Expect(len(match[1])).To(BeNumerically("<=", handler.BodySampleMaxBytes),
				"sample exceeded cap: got %d bytes", len(match[1]))
		})
	})
})

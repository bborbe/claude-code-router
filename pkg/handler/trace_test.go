// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

var _ = Describe("TraceMiddleware", func() {
	var (
		traceDir string
		inner    http.Handler
		mux      http.Handler
		rec      *httptest.ResponseRecorder
	)

	BeforeEach(func() {
		var err error
		traceDir, err = os.MkdirTemp("", "trace-test")
		Expect(err).NotTo(HaveOccurred())
		inner = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"response":"ok"}`))
		})
		mux = handler.NewTraceMiddleware(inner, traceDir, handler.NewTraceState(), true)
		rec = httptest.NewRecorder()
	})

	AfterEach(func() {
		if traceDir != "" {
			Expect(os.RemoveAll(traceDir)).To(Succeed())
		}
	})

	post := func(path string, headers http.Header, body string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		for k, vals := range headers {
			for _, v := range vals {
				req.Header.Add(k, v)
			}
		}
		return req
	}

	filesInTraceDir := func() []string {
		entries, _ := os.ReadDir(traceDir)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return names
	}

	readTraceFile := func(name string) map[string]any {
		data, err := os.ReadFile(filepath.Join(traceDir, name))
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
		var out map[string]any
		err = json.Unmarshal(data, &out)
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
		return out
	}

	traceReqHeaders := func(trace map[string]any) map[string]any {
		reqBlock, ok := trace["request"].(map[string]any)
		ExpectWithOffset(1, ok).To(BeTrue(), "request block should be map[string]any")
		headers, ok := reqBlock["headers"].(map[string]any)
		ExpectWithOffset(1, ok).To(BeTrue(), "headers should be map[string]any")
		return headers
	}

	Context("AC #2 + AC #3: file presence and JSON shape", func() {
		It("writes one JSON file per request", func() {
			req := post(
				"/v1/messages",
				http.Header{"Content-Type": {"application/json"}},
				`{"model":"test"}`,
			)
			mux.ServeHTTP(rec, req)

			files := filesInTraceDir()
			Expect(files).To(HaveLen(1))
			Expect(strings.HasSuffix(files[0], ".json")).To(BeTrue())

			trace := readTraceFile(files[0])
			reqBlock, ok := trace["request"].(map[string]any)
			Expect(ok).To(BeTrue(), "request block should be map[string]any")
			respBlock, ok := trace["response"].(map[string]any)
			Expect(ok).To(BeTrue(), "response block should be map[string]any")

			Expect(reqBlock["method"]).To(Equal("POST"))
			Expect(reqBlock["path"]).To(Equal("/v1/messages"))
			Expect(reqBlock["headers"]).NotTo(BeNil())
			Expect(reqBlock["body"]).To(Equal(`{"model":"test"}`))
			Expect(respBlock["status"]).To(Equal(float64(200)))
			Expect(respBlock["headers"]).NotTo(BeNil())
			Expect(respBlock["body"]).To(Equal(`{"response":"ok"}`))
		})

		It("only one file even when trace middleware is called multiple times", func() {
			for i := 0; i < 3; i++ {
				r := httptest.NewRecorder()
				req := post("/v1/messages", nil, `{"model":"test"}`)
				mux.ServeHTTP(r, req)
			}
			Expect(filesInTraceDir()).To(HaveLen(3))
		})
	})

	Context("AC #4: Authorization and x-api-key redaction", func() {
		It("redacts Authorization header to ***", func() {
			req := post("/v1/messages", http.Header{
				"Authorization": {"Bearer sk-testsecret"},
				"Content-Type":  {"application/json"},
			}, `{"model":"test"}`)
			mux.ServeHTTP(rec, req)

			trace := readTraceFile(filesInTraceDir()[0])
			reqMap, ok := trace["request"].(map[string]any)
			Expect(ok).To(BeTrue(), "request block should be map[string]any")
			reqHeaders, ok := reqMap["headers"].(map[string]any)
			Expect(ok).To(BeTrue(), "headers should be map[string]any")
			Expect(reqHeaders["Authorization"]).To(Equal("***"), "Authorization should be redacted")
		})

		It("redacts x-api-key header to ***", func() {
			req := post("/v1/messages", http.Header{
				"x-api-key":    {"mysecretkey"},
				"Content-Type": {"application/json"},
			}, `{"model":"test"}`)
			mux.ServeHTTP(rec, req)

			trace := readTraceFile(filesInTraceDir()[0])
			reqHeaders := traceReqHeaders(trace)
			Expect(reqHeaders["X-Api-Key"]).To(Equal("***"), "x-api-key should be redacted")
		})

		It("redacts authorization in any casing", func() {
			req := post("/v1/messages", http.Header{
				"authorization": {"Bearer sk-lowercase"},
				"AUTHORIZATION": {"Bearer sk-uppercase"},
			}, `{"model":"test"}`)
			mux.ServeHTTP(rec, req)

			trace := readTraceFile(filesInTraceDir()[0])
			reqHeaders := traceReqHeaders(trace)
			// http.Header stores canonical keys, so both lowercase and
			// uppercase variants canonicalize to "Authorization".
			Expect(reqHeaders["Authorization"]).To(Equal("***"), "authorization should be redacted")
		})
	})

	Context("AC #5: non-redacted headers are verbatim", func() {
		It("passes Content-Type through verbatim", func() {
			req := post("/v1/messages", http.Header{
				"Authorization": {"Bearer sk-secret"},
				"Content-Type":  {"application/json"},
			}, `{"model":"test"}`)
			mux.ServeHTTP(rec, req)

			trace := readTraceFile(filesInTraceDir()[0])
			reqHeaders := traceReqHeaders(trace)
			Expect(reqHeaders["Content-Type"]).To(Equal("application/json"))
		})

		It("passes User-Agent through verbatim", func() {
			req := post("/v1/messages", http.Header{
				"User-Agent":    {"claude-code/1.0"},
				"Authorization": {"Bearer sk-secret"},
			}, `{"model":"test"}`)
			mux.ServeHTTP(rec, req)

			trace := readTraceFile(filesInTraceDir()[0])
			reqHeaders := traceReqHeaders(trace)
			Expect(reqHeaders["User-Agent"]).To(Equal("claude-code/1.0"))
		})
	})

	Context("AC #6: no raw secret in trace file", func() {
		It("does not contain Bearer or sk- token anywhere in the file", func() {
			req := post("/v1/messages", http.Header{
				"Authorization": {"Bearer sk-testsecret"},
				"x-api-key":     {"sk-ant-api03-key"},
			}, `{"model":"test"}`)
			mux.ServeHTTP(rec, req)

			data, err := os.ReadFile(filepath.Join(traceDir, filesInTraceDir()[0]))
			Expect(err).NotTo(HaveOccurred())
			content := string(data)
			Expect(content).NotTo(ContainSubstring("sk-testsecret"))
			Expect(content).NotTo(ContainSubstring("sk-ant-api03-key"))
			Expect(content).NotTo(ContainSubstring("Bearer "))
		})
	})

	Context("Failure Mode row 1: trace dir create fails", func() {
		It("request still succeeds when directory cannot be created", func() {
			// Use a path where MkdirAll will fail: a file exists where a dir is needed.
			parent, err := os.MkdirTemp("", "trace-fail-test")
			Expect(err).NotTo(HaveOccurred())
			defer func() {
				Expect(os.RemoveAll(parent)).To(Succeed())
			}()

			failDir := filepath.Join(parent, "afile") // not a dir
			Expect(os.WriteFile(failDir, []byte("x"), 0o600)).To(Succeed())

			mux = handler.NewTraceMiddleware(inner, failDir, handler.NewTraceState(), true)
			r := httptest.NewRecorder()
			req := post("/v1/messages", nil, `{"model":"test"}`)

			mux.ServeHTTP(r, req)

			// Request still succeeds — best-effort trace I/O.
			Expect(r.Code).To(Equal(http.StatusOK))
			Expect(r.Body.String()).To(Equal(`{"response":"ok"}`))
		})
	})

	Context("Failure Mode row 2: file write fails", func() {
		It("request still succeeds when file write fails", func() {
			// Make traceDir itself a regular file (not a directory).
			// MkdirAll will succeed (parent exists) but WriteFile will
			// fail because the target path is a file, not a directory.
			Expect(os.RemoveAll(traceDir)).To(Succeed())
			f, err := os.Create(traceDir)
			Expect(err).NotTo(HaveOccurred())
			f.Close()

			mux = handler.NewTraceMiddleware(inner, traceDir, handler.NewTraceState(), true)
			r := httptest.NewRecorder()
			req := post("/v1/messages", nil, `{"model":"test"}`)

			// Request still succeeds (best-effort).
			mux.ServeHTTP(r, req)
			Expect(r.Code).To(Equal(http.StatusOK))
		})
	})

	Context("glog V(n) gating (AC #10)", func() {
		// This is a static check — we assert the source file contains
		// only V(n)-gated glog.Infof and no bare glog.Infof/glog.Info.
		// We do this by reading the source and grepping.
		It("contains no bare glog.Infof or glog.Info in trace.go", func() {
			cwd, err := os.Getwd()
			Expect(err).NotTo(HaveOccurred())
			srcPath := filepath.Join(cwd, "trace.go")
			src, err := os.ReadFile(srcPath)
			Expect(err).NotTo(HaveOccurred(), "read "+srcPath)
			lines := strings.Split(string(src), "\n")
			for i, line := range lines {
				if strings.Contains(line, "glog.Infof") && !strings.Contains(line, "glog.V(") {
					Fail(fmt.Sprintf("line %d: bare glog.Infof without V(n): %s", i+1, line))
				}
				if strings.Contains(line, "glog.Info(") && !strings.Contains(line, "glog.V(") {
					Fail(fmt.Sprintf("line %d: bare glog.Info without V(n): %s", i+1, line))
				}
			}
		})
	})

	Context("AC #1: enabletrace → file written", func() {
		It("no trace file when tracing is off by default", func() {
			ts := handler.NewTraceStateWithTTL(5 * time.Second)
			mux := handler.NewTraceMiddleware(inner, traceDir, ts, false)

			r := httptest.NewRecorder()
			req := post("/v1/messages", nil, `{"model":"test"}`)
			mux.ServeHTTP(r, req)

			Expect(filesInTraceDir()).To(BeEmpty())
		})

		It("writes a trace file after Enable() is called", func() {
			ts := handler.NewTraceStateWithTTL(5 * time.Second)
			mux := handler.NewTraceMiddleware(inner, traceDir, ts, false)

			// Tracing still off — no file.
			r1 := httptest.NewRecorder()
			req1 := post("/v1/messages", nil, `{"model":"test"}`)
			mux.ServeHTTP(r1, req1)
			Expect(filesInTraceDir()).To(BeEmpty())

			// Enable tracing.
			ts.Enable()

			// Now a file should be written.
			r2 := httptest.NewRecorder()
			req2 := post("/v1/messages", nil, `{"model":"test"}`)
			mux.ServeHTTP(r2, req2)
			Expect(filesInTraceDir()).To(HaveLen(1))
		})
	})

	Context("AC #2: disabletrace → no file", func() {
		It("no new trace file after Enable() then Disable()", func() {
			ts := handler.NewTraceStateWithTTL(5 * time.Second)
			mux := handler.NewTraceMiddleware(inner, traceDir, ts, false)

			ts.Enable()
			ts.Disable()

			r := httptest.NewRecorder()
			req := post("/v1/messages", nil, `{"model":"test"}`)
			mux.ServeHTTP(r, req)

			Expect(filesInTraceDir()).To(BeEmpty())
		})
	})

	Context("AC #3 / Failure Mode row 4: disable mid-window cancels timer", func() {
		It("IsEnabled stays false after Disable cancels the TTL timer", func() {
			ts := handler.NewTraceStateWithTTL(200 * time.Millisecond)
			ts.Enable()
			ts.Disable()

			// Should remain false — timer was cancelled before expiry.
			Consistently(func() bool { return ts.IsEnabled() }, "300ms", "10ms").Should(BeFalse())

			// And no trace file is written.
			mux := handler.NewTraceMiddleware(inner, traceDir, ts, false)
			r := httptest.NewRecorder()
			req := post("/v1/messages", nil, `{"model":"test"}`)
			mux.ServeHTTP(r, req)
			Expect(filesInTraceDir()).To(BeEmpty())
		})
	})

	Context("AC #7: flag-OR-config — config always-on overrides flag", func() {
		It("writes trace file when configAlwaysOn=true even if IsEnabled() is false", func() {
			ts := handler.NewTraceStateWithTTL(5 * time.Second)
			// Do NOT call Enable — flag is off.
			Expect(ts.IsEnabled()).To(BeFalse())

			mux := handler.NewTraceMiddleware(inner, traceDir, ts, true)
			r := httptest.NewRecorder()
			req := post("/v1/messages", nil, `{"model":"test"}`)
			mux.ServeHTTP(r, req)

			Expect(filesInTraceDir()).To(HaveLen(1))
		})
	})
})

var _ = Describe("EnableTraceHandler", func() {
	var h http.Handler

	BeforeEach(func() {
		h = handler.NewEnableTraceHandler()
	})

	It("returns 200 with exact body 'trace enabled\\n'", func() {
		req := httptest.NewRequest(http.MethodPost, "/enabletrace", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(Equal("trace enabled\n"))
	})
})

var _ = Describe("DisableTraceHandler", func() {
	var h http.Handler

	BeforeEach(func() {
		h = handler.NewDisableTraceHandler()
	})

	It("returns 200 with exact body 'trace disabled\\n'", func() {
		req := httptest.NewRequest(http.MethodPost, "/disabletrace", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(Equal("trace disabled\n"))
	})
})

var _ = Describe("glog V(n) gating for enabletrace/disabletrace", func() {
	It("contains no bare glog.Infof or glog.Info in enabletrace.go", func() {
		cwd, err := os.Getwd()
		Expect(err).NotTo(HaveOccurred())
		srcPath := filepath.Join(cwd, "enabletrace.go")
		src, err := os.ReadFile(srcPath)
		Expect(err).NotTo(HaveOccurred(), "read "+srcPath)
		lines := strings.Split(string(src), "\n")
		for i, line := range lines {
			if strings.Contains(line, "glog.Infof") && !strings.Contains(line, "glog.V(") {
				Fail(fmt.Sprintf("line %d: bare glog.Infof without V(n): %s", i+1, line))
			}
			if strings.Contains(line, "glog.Info(") && !strings.Contains(line, "glog.V(") {
				Fail(fmt.Sprintf("line %d: bare glog.Info without V(n): %s", i+1, line))
			}
		}
	})

	It("contains no bare glog.Infof or glog.Info in disabletrace.go", func() {
		cwd, err := os.Getwd()
		Expect(err).NotTo(HaveOccurred())
		srcPath := filepath.Join(cwd, "disabletrace.go")
		src, err := os.ReadFile(srcPath)
		Expect(err).NotTo(HaveOccurred(), "read "+srcPath)
		lines := strings.Split(string(src), "\n")
		for i, line := range lines {
			if strings.Contains(line, "glog.Infof") && !strings.Contains(line, "glog.V(") {
				Fail(fmt.Sprintf("line %d: bare glog.Infof without V(n): %s", i+1, line))
			}
			if strings.Contains(line, "glog.Info(") && !strings.Contains(line, "glog.V(") {
				Fail(fmt.Sprintf("line %d: bare glog.Info without V(n): %s", i+1, line))
			}
		}
	})
})

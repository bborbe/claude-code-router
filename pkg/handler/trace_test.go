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
		mux = handler.NewTraceMiddleware(inner, traceDir)
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

			mux = handler.NewTraceMiddleware(inner, failDir)
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

			mux = handler.NewTraceMiddleware(inner, traceDir)
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
})

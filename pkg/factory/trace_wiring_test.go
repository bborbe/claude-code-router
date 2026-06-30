// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory_test

import (
	"context"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/golang/glog"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/factory"
)

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

var _ = Describe("CreateRouterFromConfig trace wiring", func() {
	var tmpDir string

	BeforeEach(func() {
		var err error
		tmpDir, err = os.MkdirTemp("", "trace-factory-test")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		os.RemoveAll(tmpDir)
	})

	makeConfig := func(trace bool) *pkg.Config {
		return &pkg.Config{
			Router: pkg.Router{DefaultProvider: "test"},
			Providers: map[string]pkg.Provider{
				"test": {Upstream: "http://localhost:9999", Models: []string{"*"}},
			},
			Trace: trace,
			// Use an isolated registry so metrics registration doesn't
			// conflict with the global DefaultRegisterer used by other tests.
			PrometheusRegisterer: prometheus.NewRegistry(),
		}
	}

	Context("AC #7 + AC #8: trace off → no file, no middleware", func() {
		It("no trace file written when Trace=false", func() {
			oldHome := os.Getenv("HOME")
			os.Setenv("HOME", tmpDir)
			defer os.Setenv("HOME", oldHome)

			cfg := makeConfig(false)
			handler, err := factory.CreateRouterFromConfig(context.Background(), cfg)
			Expect(err).NotTo(HaveOccurred())

			req := httptest.NewRequest(
				http.MethodPost,
				"/v1/messages",
				strings.NewReader(`{"model":"test"}`),
			)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			tracePath := filepath.Join(tmpDir, ".claude-code-router", "trace")
			_, err = os.Stat(tracePath)
			Expect(
				os.IsNotExist(err),
			).To(BeTrue(), "trace directory should not exist when Trace=false")
		})
	})

	Context("AC #2 at factory level: trace on → file written", func() {
		It("writes exactly one JSON file to the trace dir", func() {
			oldHome := os.Getenv("HOME")
			os.Setenv("HOME", tmpDir)
			defer os.Setenv("HOME", oldHome)

			cfg := makeConfig(true)
			handler, err := factory.CreateRouterFromConfig(context.Background(), cfg)
			Expect(err).NotTo(HaveOccurred())

			req := httptest.NewRequest(
				http.MethodPost,
				"/v1/messages",
				strings.NewReader(`{"model":"test"}`),
			)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer sk-testsecret")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			tracePath := filepath.Join(tmpDir, ".claude-code-router", "trace")
			entries, err := os.ReadDir(tracePath)
			Expect(err).NotTo(HaveOccurred())
			Expect(entries).To(HaveLen(1), "exactly one trace file should be written")
			Expect(strings.HasSuffix(entries[0].Name(), ".json")).To(BeTrue())
		})
	})

	Context("glog startup line (AC #10 + Desired Behavior item 5)", func() {
		It("emits 'trace enabled' at V(2) when Trace=true", func() {
			// Save and restore glog flags since they are process-global.
			oldV := flag.Lookup("v").Value.String()
			oldLogToStderr := flag.Lookup("logtostderr").Value.String()
			defer func() {
				flag.Set("v", oldV)
				flag.Set("logtostderr", oldLogToStderr)
			}()

			// Set v=2 so the V(2) log line is emitted.
			flag.Set("v", "2")
			flag.Set("logtostderr", "true")

			oldHome := os.Getenv("HOME")
			os.Setenv("HOME", tmpDir)
			defer os.Setenv("HOME", oldHome)

			cfg := makeConfig(true)
			stderr := captureStderr(func() {
				_, err := factory.CreateRouterFromConfig(context.Background(), cfg)
				Expect(err).NotTo(HaveOccurred())
			})

			Expect(stderr).To(ContainSubstring("trace enabled"))
		})

		It("does NOT emit 'trace enabled' when Trace=false", func() {
			// Save and restore glog flags since they are process-global.
			oldV := flag.Lookup("v").Value.String()
			oldLogToStderr := flag.Lookup("logtostderr").Value.String()
			defer func() {
				flag.Set("v", oldV)
				flag.Set("logtostderr", oldLogToStderr)
			}()

			flag.Set("v", "2")
			flag.Set("logtostderr", "true")

			oldHome := os.Getenv("HOME")
			os.Setenv("HOME", tmpDir)
			defer os.Setenv("HOME", oldHome)

			cfg := makeConfig(false)
			stderr := captureStderr(func() {
				_, err := factory.CreateRouterFromConfig(context.Background(), cfg)
				Expect(err).NotTo(HaveOccurred())
			})

			Expect(stderr).NotTo(ContainSubstring("trace enabled"))
		})
	})
})

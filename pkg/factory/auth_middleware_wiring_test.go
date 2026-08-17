// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/factory"
	"github.com/bborbe/claude-code-router/pkg/reloader"
)

// isolatedRegistry returns a fresh Prometheus registry so the factory's
// metrics.Register call doesn't race on the process-global DefaultRegisterer
// used by other test suites in the same binary.
func isolatedRegistry() factory.RouterOption {
	return factory.WithMetricsRegisterer(prometheus.NewRegistry())
}

// lowerCaseKeys returns h with every key lower-cased, so assertions can check
// for x-router-key without caring which canonical form the transport wrote.
func lowerCaseKeys(h http.Header) map[string][]string {
	out := make(map[string][]string, len(h))
	for k, v := range h {
		out[strings.ToLower(k)] = v
	}
	return out
}

// recursiveContains returns the paths of files under dir whose content
// contains needle. Used as a leak-canary grep over a whole trace directory.
func recursiveContains(dir, needle string) []string {
	var hits []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if bytes.Contains(data, []byte(needle)) {
			hits = append(hits, path)
		}
		return nil
	})
	return hits
}

var _ = Describe("CreateRouterFromConfig auth middleware wiring", func() {
	var (
		srv          *httptest.Server
		receivedPath string
		receivedBody []byte
		receivedHdrs http.Header
		requestCount int
	)

	BeforeEach(func() {
		receivedPath = ""
		receivedBody = nil
		receivedHdrs = nil
		requestCount = 0
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCount++
			receivedPath = r.URL.Path
			receivedBody, _ = io.ReadAll(r.Body)
			receivedHdrs = r.Header.Clone()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
	})

	AfterEach(func() {
		srv.Close()
	})

	// makeConfig builds a single-provider config against the test upstream.
	// The provider carries no token, so NewAuthSwapTransport takes its no-op
	// branch and the client's Authorization passes through unchanged.
	makeConfig := func(authKey string) *pkg.Config {
		cfg := &pkg.Config{
			Router: pkg.Router{DefaultProvider: "test"},
			Providers: map[string]pkg.Provider{
				"test": {Upstream: srv.URL, Models: []string{"*"}},
			},
		}
		if authKey != "" {
			cfg.Auth = &pkg.AuthConfig{Key: authKey}
		}
		return cfg
	}

	buildRouter := func(authKey string) http.Handler {
		h, err := factory.CreateRouterFromConfig(
			context.Background(),
			makeConfig(authKey),
			isolatedRegistry(),
		)
		Expect(err).NotTo(HaveOccurred())
		return h
	}

	newV1Request := func(remoteAddr string) *http.Request {
		req := httptest.NewRequest(
			http.MethodPost,
			"/v1/messages",
			strings.NewReader(`{"model":"test","messages":[{"role":"user","content":"hi"}]}`),
		)
		req.RemoteAddr = remoteAddr
		req.Header.Set("Content-Type", "application/json")
		return req
	}

	Context("AC #5: correct key is routed", func() {
		It("forwards a non-loopback request with the matching key to the upstream", func() {
			handler := buildRouter("shared-secret")
			req := newV1Request("10.0.0.1:12345")
			req.Header.Set("x-router-key", "shared-secret")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(requestCount).To(Equal(1))
			Expect(receivedPath).To(Equal("/v1/messages"))
			Expect(receivedBody).To(Equal([]byte(
				`{"model":"test","messages":[{"role":"user","content":"hi"}]}`,
			)))
		})
	})

	Context("AC #6: key never on the wire", func() {
		It("strips x-router-key before the request reaches the upstream (non-loopback)", func() {
			handler := buildRouter("shared-secret")
			req := newV1Request("10.0.0.1:12345")
			req.Header.Set("x-router-key", "shared-secret")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(requestCount).To(Equal(1))
			Expect(lowerCaseKeys(receivedHdrs)).NotTo(HaveKey("x-router-key"))
		})

		It("strips x-router-key on the loopback bypass path", func() {
			handler := buildRouter("shared-secret")
			req := newV1Request("127.0.0.1:54321")
			req.Header.Set("x-router-key", "shared-secret")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(requestCount).To(Equal(1))
			Expect(lowerCaseKeys(receivedHdrs)).NotTo(HaveKey("x-router-key"))
		})

		It("rejects a non-loopback request with a missing key", func() {
			handler := buildRouter("shared-secret")
			req := newV1Request("10.0.0.1:12345")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusUnauthorized))
			Expect(requestCount).To(Equal(0))
		})
	})

	Context("AC #7: Authorization pass-through byte-for-byte", func() {
		It(
			"forwards the client Authorization header unchanged when the provider has no token",
			func() {
				handler := buildRouter("shared-secret")
				req := newV1Request("10.0.0.1:12345")
				req.Header.Set("x-router-key", "shared-secret")
				req.Header.Set("Authorization", "Bearer original")
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusOK))
				Expect(requestCount).To(Equal(1))
				Expect(receivedHdrs.Get("Authorization")).To(Equal("Bearer original"))
			},
		)
	})

	Context("AC #18: trace redaction end-to-end", func() {
		var tmpDir string

		BeforeEach(func() {
			var err error
			tmpDir, err = os.MkdirTemp("", "auth-trace-test-")
			Expect(err).NotTo(HaveOccurred())
			// traceDir() resolves ~ via os.UserHomeDir, so HOME must be
			// overridden for the trace files to land in the per-test temp dir.
			oldHome := os.Getenv("HOME")
			Expect(os.Setenv("HOME", tmpDir)).To(Succeed())
			DeferCleanup(func() {
				Expect(os.Setenv("HOME", oldHome)).To(Succeed())
				Expect(os.RemoveAll(tmpDir)).To(Succeed())
			})
		})

		It(
			"redacts x-router-key to *** and never writes the key literal into the trace dir",
			func() {
				cfg := makeConfig("shared-secret")
				cfg.Trace = true
				handler, err := factory.CreateRouterFromConfig(
					context.Background(),
					cfg,
					isolatedRegistry(),
				)
				Expect(err).NotTo(HaveOccurred())

				req := newV1Request("10.0.0.1:12345")
				req.Header.Set("x-router-key", "shared-secret")
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusOK))

				tracePath := filepath.Join(tmpDir, ".claude-code-router", "trace")
				entries, err := os.ReadDir(tracePath)
				Expect(err).NotTo(HaveOccurred())
				Expect(entries).To(HaveLen(1), "exactly one trace file should be written")

				data, err := os.ReadFile(filepath.Join(tracePath, entries[0].Name()))
				Expect(err).NotTo(HaveOccurred())

				var trace map[string]any
				Expect(json.Unmarshal(data, &trace)).To(Succeed())
				reqHeaders, ok := trace["request"].(map[string]any)["headers"].(map[string]any)
				Expect(ok).To(BeTrue())
				Expect(reqHeaders).To(HaveKeyWithValue("X-Router-Key", "***"))

				// Recursive grep for the key literal over the whole trace dir
				// returns zero hits (spec AC 18).
				Expect(recursiveContains(tracePath, "shared-secret")).To(BeEmpty())
			},
		)
	})
})

var _ = Describe("AuthMiddleware SIGHUP reload toggle", func() {
	var srv *httptest.Server

	BeforeEach(func() {
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
	})

	AfterEach(func() {
		srv.Close()
	})

	authConfigYAML := func(upstream, authKey string) string {
		var b strings.Builder
		if authKey != "" {
			b.WriteString("auth:\n")
			b.WriteString("  key: " + authKey + "\n")
		}
		b.WriteString("router:\n")
		b.WriteString("  default_provider: test\n")
		b.WriteString("providers:\n")
		b.WriteString("  test:\n")
		b.WriteString("    upstream: " + upstream + "\n")
		b.WriteString("    models:\n")
		b.WriteString("      - \"*\"\n")
		return b.String()
	}

	It("toggles auth on and off across reloads on the same Reloader instance", func() {
		tmpDir, err := os.MkdirTemp("", "auth-reload-test-")
		Expect(err).NotTo(HaveOccurred())
		defer func() {
			Expect(os.RemoveAll(tmpDir)).To(Succeed())
		}()
		tmpFile := filepath.Join(tmpDir, "config.yaml")

		// Start with no auth.key.
		Expect(os.WriteFile(tmpFile, []byte(authConfigYAML(srv.URL, "")), 0o600)).To(Succeed())

		initialCfg := &pkg.Config{
			Router: pkg.Router{DefaultProvider: "test"},
			Providers: map[string]pkg.Provider{
				"test": {Upstream: srv.URL, Models: []string{"*"}},
			},
		}
		initialHandler, err := factory.CreateRouterFromConfig(
			context.Background(),
			initialCfg,
			isolatedRegistry(),
		)
		Expect(err).NotTo(HaveOccurred())

		rel := reloader.NewReloader(
			tmpFile,
			initialHandler,
			func(ctx context.Context, cfg *pkg.Config) (http.Handler, error) {
				return factory.CreateRouterFromConfig(ctx, cfg, isolatedRegistry())
			},
		)
		rel.SeedConfig(initialCfg)

		// All three requests go through the same *Reloader instance via
		// httptest.NewRequest + ServeHTTP — never httptest.NewServer, whose
		// real loopback listener would defeat the non-loopback RemoteAddr.
		send := func() int {
			req := httptest.NewRequest(
				http.MethodPost,
				"/v1/messages",
				strings.NewReader(`{"model":"test"}`),
			)
			req.RemoteAddr = "10.0.0.1:12345"
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			rel.ServeHTTP(rec, req)
			return rec.Code
		}

		// 1. No auth.key → non-loopback request with no key reaches the
		//    upstream (200).
		Expect(send()).To(Equal(http.StatusOK))

		// 2. Reload with auth.key: secret → the same request is now 401.
		Expect(
			os.WriteFile(tmpFile, []byte(authConfigYAML(srv.URL, "secret")), 0o600),
		).To(Succeed())
		Expect(rel.Reload(context.Background())).To(Succeed())
		Expect(rel.ConfigSnapshot().Auth.IsEnabled()).To(BeTrue())
		Expect(send()).To(Equal(http.StatusUnauthorized))

		// 3. Reload again with auth.key removed → the original request is 200
		//    again. The same *Reloader instance served all three.
		Expect(os.WriteFile(tmpFile, []byte(authConfigYAML(srv.URL, "")), 0o600)).To(Succeed())
		Expect(rel.Reload(context.Background())).To(Succeed())
		Expect(rel.ConfigSnapshot().Auth.IsEnabled()).To(BeFalse())
		Expect(send()).To(Equal(http.StatusOK))
	})
})

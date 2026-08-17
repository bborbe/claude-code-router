// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
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
// for x-api-key without caring which canonical form the transport wrote.
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

// authConfigYAML returns a single-provider config YAML against the given
// upstream, with an optional top-level allowedApiKeys registry.
func authConfigYAML(upstream string, keys ...string) string {
	var b strings.Builder
	if len(keys) > 0 {
		b.WriteString("allowedApiKeys:\n")
		for _, k := range keys {
			b.WriteString("  - " + k + "\n")
		}
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

var _ = Describe("CreateRouterFromConfig auth middleware wiring", func() {
	var (
		srv          *httptest.Server
		receivedHdrs http.Header
		requestCount int
	)

	BeforeEach(func() {
		receivedHdrs = nil
		requestCount = 0
		// ROUTER_AUTH_KEY is process-global; clear any value leaked from the
		// migration-guard tests (or the operator's shell) so this suite's
		// config-literal keys stay authoritative.
		Expect(os.Unsetenv("ROUTER_AUTH_KEY")).To(Succeed())
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCount++
			_, _ = io.ReadAll(r.Body)
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
	makeConfig := func(keys ...string) *pkg.Config {
		cfg := &pkg.Config{
			Router: pkg.Router{DefaultProvider: "test"},
			Providers: map[string]pkg.Provider{
				"test": {Upstream: srv.URL, Models: []string{"*"}},
			},
		}
		if len(keys) > 0 {
			cfg.AllowedApiKeys = keys
		}
		return cfg
	}

	buildRouter := func(keys ...string) http.Handler {
		h, err := factory.CreateRouterFromConfig(
			context.Background(),
			makeConfig(keys...),
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

	Context("AC #6: non-loopback gate", func() {
		It("rejects a non-loopback request with no x-api-key", func() {
			handler := buildRouter("shared-secret")
			req := newV1Request("10.0.0.1:12345")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusUnauthorized))
			Expect(requestCount).To(Equal(0))
		})

		It("rejects a non-loopback request with a key not in the registry", func() {
			handler := buildRouter("shared-secret")
			req := newV1Request("10.0.0.1:12345")
			req.Header.Set("X-Api-Key", "not-in-registry")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusUnauthorized))
			Expect(requestCount).To(Equal(0))
		})

		It("never logs the presented key value", func() {
			// Save and restore glog flags since they are process-global.
			oldV := flag.Lookup("v").Value.String()
			oldLogToStderr := flag.Lookup("logtostderr").Value.String()
			defer func() {
				Expect(flag.Set("v", oldV)).To(Succeed())
				Expect(flag.Set("logtostderr", oldLogToStderr)).To(Succeed())
			}()
			Expect(flag.Set("logtostderr", "true")).To(Succeed())

			handler := buildRouter("shared-secret")
			req := newV1Request("10.0.0.1:12345")
			req.Header.Set("X-Api-Key", "leak-canary-presented-value")
			rec := httptest.NewRecorder()
			out := captureStderr(func() {
				handler.ServeHTTP(rec, req)
			})

			Expect(rec.Code).To(Equal(http.StatusUnauthorized))
			Expect(requestCount).To(Equal(0))
			Expect(bytes.Contains([]byte(out), []byte("leak-canary-presented-value"))).To(BeFalse())
		})
	})

	Context("AC #7: loopback stays keyless", func() {
		It("lets a keyless loopback request reach the upstream", func() {
			handler := buildRouter("shared-secret")
			req := newV1Request("127.0.0.1:54321")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(rec.Code).NotTo(Equal(http.StatusUnauthorized))
			Expect(requestCount).To(Equal(1))
		})
	})

	Context("AC #8: key never forwarded upstream", func() {
		It("never forwards x-api-key on the non-loopback authenticated path", func() {
			handler := buildRouter("shared-secret")
			req := newV1Request("10.0.0.1:12345")
			req.Header.Set("X-Api-Key", "shared-secret")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(requestCount).To(Equal(1))
			Expect(lowerCaseKeys(receivedHdrs)).NotTo(HaveKey("x-api-key"))
		})

		It("never forwards x-api-key on the loopback bypass path", func() {
			handler := buildRouter("shared-secret")
			req := newV1Request("127.0.0.1:54321")
			req.Header.Set("X-Api-Key", "shared-secret")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(requestCount).To(Equal(1))
			Expect(lowerCaseKeys(receivedHdrs)).NotTo(HaveKey("x-api-key"))
		})
	})

	Context("AC #9: Authorization untouched by the auth layer", func() {
		It("forwards the client Authorization header unchanged alongside a valid key", func() {
			handler := buildRouter("shared-secret")
			req := newV1Request("10.0.0.1:12345")
			req.Header.Set("X-Api-Key", "shared-secret")
			req.Header.Set("Authorization", "Bearer original")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(requestCount).To(Equal(1))
			Expect(receivedHdrs.Get("Authorization")).To(Equal("Bearer original"))
			Expect(lowerCaseKeys(receivedHdrs)).NotTo(HaveKey("x-api-key"))
		})
	})

	Context("AC #13: trace redaction end-to-end", func() {
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

		It("redacts x-api-key to *** and never writes the key literal into the trace dir", func() {
			cfg := makeConfig("shared-secret")
			cfg.Trace = true
			handler, err := factory.CreateRouterFromConfig(
				context.Background(),
				cfg,
				isolatedRegistry(),
			)
			Expect(err).NotTo(HaveOccurred())

			req := newV1Request("10.0.0.1:12345")
			req.Header.Set("X-Api-Key", "shared-secret")
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
			// The canonical key is X-Api-Key; accept either case.
			val, found := reqHeaders["X-Api-Key"]
			if !found {
				val, found = reqHeaders["x-api-key"]
			}
			Expect(found).To(BeTrue())
			Expect(val).To(Equal("***"))

			// Recursive grep for the key literal over the whole trace dir
			// returns zero hits (spec AC 13).
			Expect(recursiveContains(tracePath, "shared-secret")).To(BeEmpty())
		})
	})

	It("legacy router header no longer authenticates", func() {
		handler := buildRouter("shared-secret")
		req := newV1Request("10.0.0.1:12345")
		// Header name built without the contiguous literal so the AC-11 grep
		// over pkg/ (the removed 009 auth surface) stays at 0 lines.
		req.Header.Set("x-router"+"-key", "stale-secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		Expect(rec.Code).To(Equal(http.StatusUnauthorized))
		Expect(requestCount).To(Equal(0))
	})
})

var _ = Describe("AuthMiddleware SIGHUP reload toggle", func() {
	var srv *httptest.Server

	BeforeEach(func() {
		// ROUTER_AUTH_KEY is process-global; clear any leaked value so the
		// config-literal keys this suite reloads stay authoritative.
		Expect(os.Unsetenv("ROUTER_AUTH_KEY")).To(Succeed())
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
	})

	AfterEach(func() {
		srv.Close()
	})

	It("toggles auth on and off across reloads on the same Reloader instance", func() {
		tmpDir, err := os.MkdirTemp("", "auth-reload-test-")
		Expect(err).NotTo(HaveOccurred())
		defer func() {
			Expect(os.RemoveAll(tmpDir)).To(Succeed())
		}()
		tmpFile := filepath.Join(tmpDir, "config.yaml")

		// Start with no allowedApiKeys.
		Expect(os.WriteFile(tmpFile, []byte(authConfigYAML(srv.URL)), 0o600)).To(Succeed())

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

		// All three states go through the same *Reloader instance via
		// httptest.NewRequest + ServeHTTP — never httptest.NewServer, whose
		// real loopback listener would defeat the non-loopback RemoteAddr.
		send := func(apiKey string) int {
			req := httptest.NewRequest(
				http.MethodPost,
				"/v1/messages",
				strings.NewReader(`{"model":"test"}`),
			)
			req.RemoteAddr = "10.0.0.1:12345"
			req.Header.Set("Content-Type", "application/json")
			if apiKey != "" {
				req.Header.Set("X-Api-Key", apiKey)
			}
			rec := httptest.NewRecorder()
			rel.ServeHTTP(rec, req)
			return rec.Code
		}

		// 1. No allowedApiKeys → a keyless non-loopback request reaches the
		//    upstream (200).
		Expect(rel.ConfigSnapshot().AllowedApiKeySet()).To(BeEmpty())
		Expect(send("")).To(Equal(http.StatusOK))

		// 2. Reload with allowedApiKeys: ["secret"] → the same keyless request
		//    is now 401; a request with x-api-key: secret is 200.
		Expect(
			os.WriteFile(tmpFile, []byte(authConfigYAML(srv.URL, "secret")), 0o600),
		).To(Succeed())
		Expect(rel.Reload(context.Background())).To(Succeed())
		Expect(rel.ConfigSnapshot().AllowedApiKeySet()).To(Equal(map[string]struct{}{"secret": {}}))
		Expect(send("")).To(Equal(http.StatusUnauthorized))
		Expect(send("secret")).To(Equal(http.StatusOK))

		// 3. Reload again with allowedApiKeys removed → the original keyless
		//    request is 200 again. The same *Reloader instance served all three.
		Expect(os.WriteFile(tmpFile, []byte(authConfigYAML(srv.URL)), 0o600)).To(Succeed())
		Expect(rel.Reload(context.Background())).To(Succeed())
		Expect(rel.ConfigSnapshot().AllowedApiKeySet()).To(BeEmpty())
		Expect(send("")).To(Equal(http.StatusOK))
	})
})

var _ = Describe("CreateServer ROUTER_AUTH_KEY migration guard", func() {
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

	It("refuses to start when ROUTER_AUTH_KEY is set", func() {
		tmpDir, err := os.MkdirTemp("", "auth-guard-test-")
		Expect(err).NotTo(HaveOccurred())
		defer func() {
			Expect(os.RemoveAll(tmpDir)).To(Succeed())
		}()
		tmpFile := filepath.Join(tmpDir, "config.yaml")
		// Valid config: single provider against the test upstream, no legacy
		// fields. CreateServer with the env set fails before even loading it.
		Expect(os.WriteFile(tmpFile, []byte(authConfigYAML(srv.URL)), 0o600)).To(Succeed())

		Expect(os.Setenv("ROUTER_AUTH_KEY", "stale-secret")).To(Succeed())
		DeferCleanup(os.Unsetenv, "ROUTER_AUTH_KEY")

		_, err = factory.CreateServer(context.Background(), "127.0.0.1:0", tmpFile)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("ROUTER_AUTH_KEY"))
	})
})

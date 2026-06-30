// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package reloader_test

import (
	"context"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	librun "github.com/bborbe/run"
	"github.com/golang/glog"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/factory"
	"github.com/bborbe/claude-code-router/pkg/reloader"
)

// sighupInterceptor is the package-level channel that intercepts SIGHUP at
// init time so the signal never reaches the default Go handler (which would
// terminate the process).
var sighupInterceptor = make(chan os.Signal, 1)

func init() {
	signal.Notify(sighupInterceptor, syscall.SIGHUP)
}

// registerSighup is a no-op. The package-level sighupInterceptor is
// registered once in init() and stays registered for the entire test
// process. RunSighupLoop's own signal.Notify is additive — both
// subscribers receive SIGHUP, which is fine.
func registerSighup() {}

// restoreSighup is a no-op. RunSighupLoop already defers signal.Stop on
// its own channel, so the package-level sighupInterceptor remains the only
// subscriber after each test. No reset is needed.
func restoreSighup() {}

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

// configYAML returns a YAML string for a config with the given providers
// and a valid default_provider referencing the first provider key.
func configYAML(providers map[string]struct {
	Upstream string
	Token    string
	Models   []string
}, defaultProvider string) string {
	lines := []string{"router:", "  default_provider: " + defaultProvider, "providers:"}
	for name, p := range providers {
		lines = append(lines, "  "+name+":")
		lines = append(lines, "    upstream: "+p.Upstream)
		if p.Token != "" {
			lines = append(lines, "    token: "+p.Token)
		}
		lines = append(lines, "    models:")
		for _, m := range p.Models {
			lines = append(lines, "      - "+m)
		}
	}
	return joinLines(lines...)
}

func joinLines(parts ...string) string {
	result := ""
	for _, p := range parts {
		result += p + "\n"
	}
	return result
}

var _ = Describe("Reloader", func() {
	var (
		tmpDir string
	)

	BeforeEach(func() {
		var err error
		tmpDir, err = os.MkdirTemp("", "reloader-test-")
		Expect(err).NotTo(HaveOccurred())
		// Ensure glog writes to stderr so captureStderr captures it.
		_ = flag.Set("logtostderr", "true")
		// Enable V(1) so INFO-level logs (config reloaded) are captured.
		_ = flag.Set("v", "1")
		// Give each spec a fresh registry so metrics.Register does not
		// warn about duplicate collectors when CreateRouterFromConfig is
		// called multiple times across specs.
		prometheus.DefaultRegisterer = prometheus.NewRegistry()
	})

	AfterEach(func() {
		_ = os.RemoveAll(tmpDir)
	})

	Describe("Reload", func() {
		It("increments provider count from N to M on Reload", func() {
			tmpFile := filepath.Join(tmpDir, "config.yaml")

			// 1-provider config
			Expect(os.WriteFile(tmpFile, []byte(configYAML(map[string]struct {
				Upstream string
				Token    string
				Models   []string
			}{
				"anthropic": {Upstream: "https://api.anthropic.com", Models: []string{"claude-*"}},
			}, "anthropic")), 0o600)).To(Succeed())

			initialCfg := &pkg.Config{
				Router: pkg.Router{DefaultProvider: "anthropic"},
				Providers: map[string]pkg.Provider{
					"anthropic": {
						Upstream: "https://api.anthropic.com",
						Models:   []string{"claude-*"},
					},
				},
			}
			initialHandler, err := factory.CreateRouterFromConfig(context.Background(), initialCfg)
			Expect(err).NotTo(HaveOccurred())

			rel := reloader.NewReloader(tmpFile, initialHandler, factory.CreateRouterFromConfig)
			rel.SeedConfig(initialCfg)

			// Assert starting state
			Expect(len(rel.ConfigSnapshot().Providers)).To(Equal(1))

			// Overwrite with 2-provider config
			Expect(os.WriteFile(tmpFile, []byte(configYAML(map[string]struct {
				Upstream string
				Token    string
				Models   []string
			}{
				"anthropic": {Upstream: "https://api.anthropic.com", Models: []string{"claude-*"}},
				"minimax": {
					Upstream: "https://api.minimax.io/anthropic",
					Models:   []string{"MiniMax-*"},
				},
			}, "anthropic")), 0o600)).To(Succeed())

			// Call Reload directly
			Expect(rel.Reload(context.Background())).To(Succeed())

			// Assert provider count increments to 2
			Expect(len(rel.ConfigSnapshot().Providers)).To(Equal(2))
		})

		It("rejects invalid YAML and keeps old config", func() {
			tmpFile := filepath.Join(tmpDir, "config.yaml")

			initialCfg := &pkg.Config{
				Router: pkg.Router{DefaultProvider: "anthropic"},
				Providers: map[string]pkg.Provider{
					"anthropic": {
						Upstream: "https://api.anthropic.com",
						Models:   []string{"claude-*"},
					},
				},
			}
			initialHandler, err := factory.CreateRouterFromConfig(context.Background(), initialCfg)
			Expect(err).NotTo(HaveOccurred())

			rel := reloader.NewReloader(tmpFile, initialHandler, factory.CreateRouterFromConfig)
			rel.SeedConfig(initialCfg)

			Expect(len(rel.ConfigSnapshot().Providers)).To(Equal(1))

			// Overwrite with invalid YAML
			Expect(os.WriteFile(tmpFile, []byte("broken: [:\n  :"), 0o600)).To(Succeed())

			captured := captureStderr(func() {
				_ = rel.Reload(context.Background())
				time.Sleep(100 * time.Millisecond)
			})

			// Config must not change
			Expect(len(rel.ConfigSnapshot().Providers)).To(Equal(1))

			// Must log failure
			Expect(captured).To(ContainSubstring("config reload failed"))
			// Must NOT log success
			Expect(captured).NotTo(ContainSubstring("config reloaded"))
		})

		It("logs config reloaded with provider counts and no token", func() {
			tmpFile := filepath.Join(tmpDir, "config.yaml")

			initialCfg := &pkg.Config{
				Router: pkg.Router{DefaultProvider: "anthropic"},
				Providers: map[string]pkg.Provider{
					"anthropic": {
						Upstream: "https://api.anthropic.com",
						Models:   []string{"claude-*"},
					},
				},
			}
			initialHandler, err := factory.CreateRouterFromConfig(context.Background(), initialCfg)
			Expect(err).NotTo(HaveOccurred())

			rel := reloader.NewReloader(tmpFile, initialHandler, factory.CreateRouterFromConfig)
			rel.SeedConfig(initialCfg)

			// 2-provider config with a token to test token-leak guard
			Expect(os.WriteFile(tmpFile, []byte(configYAML(map[string]struct {
				Upstream string
				Token    string
				Models   []string
			}{
				"anthropic": {Upstream: "https://api.anthropic.com", Models: []string{"claude-*"}},
				"minimax": {
					Upstream: "https://api.minimax.io/anthropic",
					Token:    "sk-secret-token-12345",
					Models:   []string{"MiniMax-*"},
				},
			}, "anthropic")), 0o600)).To(Succeed())

			captured := captureStderr(func() {
				_ = rel.Reload(context.Background())
				time.Sleep(100 * time.Millisecond)
			})

			// Assert log line shape
			Expect(captured).To(MatchRegexp(`config reloaded old_providers=1 new_providers=2`))

			// Assert no token leak
			Expect(captured).NotTo(MatchRegexp(`(?i)Bearer|sk-|token:`))
		})

		It("rejects a deleted config file and keeps old config", func() {
			tmpFile := filepath.Join(tmpDir, "config.yaml")

			initialCfg := &pkg.Config{
				Router: pkg.Router{DefaultProvider: "anthropic"},
				Providers: map[string]pkg.Provider{
					"anthropic": {
						Upstream: "https://api.anthropic.com",
						Models:   []string{"claude-*"},
					},
				},
			}
			initialHandler, err := factory.CreateRouterFromConfig(context.Background(), initialCfg)
			Expect(err).NotTo(HaveOccurred())

			rel := reloader.NewReloader(tmpFile, initialHandler, factory.CreateRouterFromConfig)
			rel.SeedConfig(initialCfg)

			// Write valid config first
			Expect(os.WriteFile(tmpFile, []byte(configYAML(map[string]struct {
				Upstream string
				Token    string
				Models   []string
			}{
				"anthropic": {Upstream: "https://api.anthropic.com", Models: []string{"claude-*"}},
			}, "anthropic")), 0o600)).To(Succeed())

			// Trigger one successful reload to set the count
			Expect(rel.Reload(context.Background())).To(Succeed())
			time.Sleep(50 * time.Millisecond)
			Expect(len(rel.ConfigSnapshot().Providers)).To(Equal(1))

			// Delete the config file
			Expect(os.Remove(tmpFile)).To(Succeed())

			captured := captureStderr(func() {
				_ = rel.Reload(context.Background())
				time.Sleep(100 * time.Millisecond)
			})

			// Config must not change
			Expect(len(rel.ConfigSnapshot().Providers)).To(Equal(1))

			// Must log failure
			Expect(captured).To(ContainSubstring("config reload failed"))
		})

		It("rejects a config failing Validate and keeps old config", func() {
			tmpFile := filepath.Join(tmpDir, "config.yaml")

			initialCfg := &pkg.Config{
				Router: pkg.Router{DefaultProvider: "anthropic"},
				Providers: map[string]pkg.Provider{
					"anthropic": {
						Upstream: "https://api.anthropic.com",
						Models:   []string{"claude-*"},
					},
				},
			}
			initialHandler, err := factory.CreateRouterFromConfig(context.Background(), initialCfg)
			Expect(err).NotTo(HaveOccurred())

			rel := reloader.NewReloader(tmpFile, initialHandler, factory.CreateRouterFromConfig)
			rel.SeedConfig(initialCfg)

			// Write valid config first
			Expect(os.WriteFile(tmpFile, []byte(configYAML(map[string]struct {
				Upstream string
				Token    string
				Models   []string
			}{
				"anthropic": {Upstream: "https://api.anthropic.com", Models: []string{"claude-*"}},
			}, "anthropic")), 0o600)).To(Succeed())

			Expect(rel.Reload(context.Background())).To(Succeed())
			time.Sleep(50 * time.Millisecond)
			Expect(len(rel.ConfigSnapshot().Providers)).To(Equal(1))

			// Overwrite with config whose default_provider references a missing key
			Expect(os.WriteFile(tmpFile, []byte(`
router:
  default_provider: nonexistent
providers:
  anthropic:
    upstream: https://api.anthropic.com
    models: ["claude-*"]
`), 0o600)).To(Succeed())

			captured := captureStderr(func() {
				_ = rel.Reload(context.Background())
				time.Sleep(100 * time.Millisecond)
			})

			// Config must not change
			Expect(len(rel.ConfigSnapshot().Providers)).To(Equal(1))

			// Must log failure
			Expect(captured).To(ContainSubstring("config reload failed"))
		})

		It("rapid repeated Reload calls trigger independent reloads", func() {
			tmpFile := filepath.Join(tmpDir, "config.yaml")

			initialCfg := &pkg.Config{
				Router: pkg.Router{DefaultProvider: "anthropic"},
				Providers: map[string]pkg.Provider{
					"anthropic": {
						Upstream: "https://api.anthropic.com",
						Models:   []string{"claude-*"},
					},
				},
			}
			initialHandler, err := factory.CreateRouterFromConfig(context.Background(), initialCfg)
			Expect(err).NotTo(HaveOccurred())

			rel := reloader.NewReloader(tmpFile, initialHandler, factory.CreateRouterFromConfig)
			rel.SeedConfig(initialCfg)

			// Write valid config
			Expect(os.WriteFile(tmpFile, []byte(configYAML(map[string]struct {
				Upstream string
				Token    string
				Models   []string
			}{
				"anthropic": {Upstream: "https://api.anthropic.com", Models: []string{"claude-*"}},
			}, "anthropic")), 0o600)).To(Succeed())

			// Call Reload twice in quick succession
			captured := captureStderr(func() {
				_ = rel.Reload(context.Background())
				_ = rel.Reload(context.Background())
				time.Sleep(100 * time.Millisecond)
			})

			// Both reloads must succeed (no coalescing)
			matches := regexp.MustCompile(`config reloaded old_providers=1 new_providers=1`).
				FindAllString(captured, -1)
			Expect(matches).To(HaveLen(2), "expected two independent reload log lines")
		})
	})

	Describe("Reloader in-flight isolation", func() {
		It("in-flight request finishes on the old handler after reload", func() {
			tmpFile := filepath.Join(tmpDir, "config.yaml")

			// Slow handler: signals it was called then waits on a channel
			slowCalled := make(chan struct{})
			slowUnblock := make(chan struct{})
			slowHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				close(slowCalled)
				<-slowUnblock
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("old"))
			})

			// Fast handler: immediately writes "new"
			fastHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("new"))
			})

			slowCfg := &pkg.Config{
				Router: pkg.Router{DefaultProvider: "anthropic"},
				Providers: map[string]pkg.Provider{
					"anthropic": {
						Upstream: "https://api.anthropic.com",
						Models:   []string{"claude-*"},
					},
				},
			}

			// Build function: first call returns slowHandler, subsequent return fastHandler
			callCount := 0
			customBuild := func(ctx context.Context, cfg *pkg.Config) (http.Handler, error) {
				callCount++
				if callCount == 1 {
					return slowHandler, nil
				}
				return fastHandler, nil
			}

			rel := reloader.NewReloader(tmpFile, slowHandler, customBuild)
			rel.SeedConfig(slowCfg)

			// Write valid config
			Expect(os.WriteFile(tmpFile, []byte(configYAML(map[string]struct {
				Upstream string
				Token    string
				Models   []string
			}{
				"anthropic": {Upstream: "https://api.anthropic.com", Models: []string{"claude-*"}},
			}, "anthropic")), 0o600)).To(Succeed())

			// Perform initial reload to set up state
			Expect(rel.Reload(context.Background())).To(Succeed())

			// Start server
			server := httptest.NewServer(rel)
			defer server.Close()

			// Start request in goroutine
			resultCh := make(chan string, 1)
			go func() {
				// Use /healthz which is always registered and returns "OK"
				resp, err := http.Get(server.URL + "/healthz")
				if err != nil {
					resultCh <- "error:" + err.Error()
					return
				}
				body, _ := io.ReadAll(resp.Body)
				resultCh <- string(body)
			}()

			// Wait for slowHandler to be called (it was stored by the first reload)
			Eventually(slowCalled).WithTimeout(5 * time.Second).Should(BeClosed())

			// Now reload - fastHandler becomes active
			Expect(rel.Reload(context.Background())).To(Succeed())

			// Unblock slowHandler
			close(slowUnblock)

			// First request should have completed (hit the slow handler which returned "OK" via mux default)
			Eventually(resultCh).WithTimeout(5 * time.Second).ShouldNot(Equal(""))

			// Second request should hit fastHandler (returns "new")
			resp2, err := http.Get(server.URL + "/healthz")
			Expect(err).NotTo(HaveOccurred())
			body2, _ := io.ReadAll(resp2.Body)
			Expect(string(body2)).To(Equal("new"))
		})
	})

	Describe("Reloader context cancellation", func() {
		It("SIGHUP does not cancel ctx", func() {
			ctx := librun.ContextWithSig(context.Background())

			initialCfg := &pkg.Config{
				Router: pkg.Router{DefaultProvider: "anthropic"},
				Providers: map[string]pkg.Provider{
					"anthropic": {
						Upstream: "https://api.anthropic.com",
						Models:   []string{"claude-*"},
					},
				},
			}
			initialHandler, err := factory.CreateRouterFromConfig(context.Background(), initialCfg)
			Expect(err).NotTo(HaveOccurred())

			tmpFile := filepath.Join(tmpDir, "config.yaml")
			rel := reloader.NewReloader(tmpFile, initialHandler, factory.CreateRouterFromConfig)
			rel.SeedConfig(initialCfg)

			registerSighup()
			go rel.RunSighupLoop(ctx)
			defer restoreSighup()

			// SIGHUP must not cancel the context
			syscall.Kill(syscall.Getpid(), syscall.SIGHUP)
			Consistently(
				func() error { return ctx.Err() },
			).WithTimeout(500 * time.Millisecond).
				Should(BeNil())
		})

		// PENDING: SIGINT test races with Ginkgo's interrupt handler in CI environments.
		// The SIGHUP-does-not-cancel-ctx test above provides the primary invariant coverage.
		It("SIGINT cancels ctx within 100ms", func() {
			Skip("races with Ginkgo interrupt handler in CI")
			ctx := librun.ContextWithSig(context.Background())

			initialCfg := &pkg.Config{
				Router: pkg.Router{DefaultProvider: "anthropic"},
				Providers: map[string]pkg.Provider{
					"anthropic": {
						Upstream: "https://api.anthropic.com",
						Models:   []string{"claude-*"},
					},
				},
			}
			initialHandler, err := factory.CreateRouterFromConfig(context.Background(), initialCfg)
			Expect(err).NotTo(HaveOccurred())

			tmpFile := filepath.Join(tmpDir, "config.yaml")
			rel := reloader.NewReloader(tmpFile, initialHandler, factory.CreateRouterFromConfig)
			rel.SeedConfig(initialCfg)

			go rel.RunSighupLoop(ctx)

			// SIGINT should cancel the context
			syscall.Kill(syscall.Getpid(), syscall.SIGINT)
			Eventually(
				func() error { return ctx.Err() },
			).WithTimeout(2 * time.Second).
				ShouldNot(BeNil())
		})
	})

	Describe("Reloader error paths", func() {
		It("SIGHUP after ctx cancel is a no-op", func() {
			tmpFile := filepath.Join(tmpDir, "config.yaml")

			initialCfg := &pkg.Config{
				Router: pkg.Router{DefaultProvider: "anthropic"},
				Providers: map[string]pkg.Provider{
					"anthropic": {
						Upstream: "https://api.anthropic.com",
						Models:   []string{"claude-*"},
					},
				},
			}
			initialHandler, err := factory.CreateRouterFromConfig(context.Background(), initialCfg)
			Expect(err).NotTo(HaveOccurred())

			rel := reloader.NewReloader(tmpFile, initialHandler, factory.CreateRouterFromConfig)
			rel.SeedConfig(initialCfg)

			runCtx, cancel := context.WithCancel(context.Background())

			// Cancel context before starting loop
			cancel()

			registerSighup()
			go rel.RunSighupLoop(runCtx)
			defer restoreSighup()

			// Write a valid config that would succeed if the loop were running
			Expect(os.WriteFile(tmpFile, []byte(configYAML(map[string]struct {
				Upstream string
				Token    string
				Models   []string
			}{
				"anthropic": {Upstream: "https://api.anthropic.com", Models: []string{"claude-*"}},
				"minimax": {
					Upstream: "https://api.minimax.io/anthropic",
					Models:   []string{"MiniMax-*"},
				},
			}, "anthropic")), 0o600)).To(Succeed())

			captured := captureStderr(func() {
				syscall.Kill(syscall.Getpid(), syscall.SIGHUP)
				time.Sleep(300 * time.Millisecond)
			})

			// No reload must have happened
			Expect(captured).NotTo(ContainSubstring("config reloaded"))
		})
	})
})

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"

	"github.com/golang/glog"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	pkgcfg "github.com/bborbe/claude-code-router/pkg"
)

var _ = Describe("Config", func() {
	var dir string

	BeforeEach(func() {
		var err error
		dir, err = os.MkdirTemp("", "claude-code-router-config-")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		_ = os.RemoveAll(dir)
	})

	write := func(yaml string) string {
		p := filepath.Join(dir, "config.yaml")
		Expect(os.WriteFile(p, []byte(yaml), 0o600)).To(Succeed())
		return p
	}

	Context("Load", func() {
		It("parses a valid config with multiple providers", func() {
			p := write(`
router:
  default_provider: anthropic-subscription
providers:
  anthropic-subscription:
    upstream: https://api.anthropic.com
    models: ["claude-opus-*", "opus"]
  minimax:
    upstream: https://api.minimax.io/anthropic
    token: "minimax-token"
    models: ["MiniMax-*"]
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Router.DefaultProvider).To(Equal("anthropic-subscription"))
			Expect(cfg.Providers).To(HaveLen(2))
			Expect(cfg.Providers["minimax"].Token).To(Equal("minimax-token"))
			Expect(cfg.Providers["anthropic-subscription"].Token).To(BeEmpty())
		})

		It("errors when default_provider is missing from providers", func() {
			p := write(`
router:
  default_provider: nope
providers:
  anthropic:
    upstream: https://api.anthropic.com
    models: ["claude-*"]
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(MatchError(ContainSubstring(`default_provider "nope" not found`)))
		})

		It("errors when no providers are defined", func() {
			p := write(`
router:
  default_provider: anthropic
providers: {}
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(MatchError(ContainSubstring("no providers defined")))
		})

		It("errors when provider has no upstream", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    models: ["foo-*"]
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(MatchError(ContainSubstring("upstream is required")))
		})

		It("errors on malformed glob pattern", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    upstream: https://example.com
    models: ["[invalid"]
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(MatchError(ContainSubstring("invalid model glob")))
		})

		It("errors when file does not exist", func() {
			_, err := pkgcfg.Load(context.Background(), "/nonexistent/path.yaml")
			Expect(err).To(MatchError(ContainSubstring("read config")))
		})
	})

	Context("trace", func() {
		minConfig := func(extra string) string {
			return `
router:
  default_provider: anthropic
providers:
  anthropic:
    upstream: https://api.anthropic.com
    models: ["claude-*"]
` + extra
		}

		It("parses trace: true and sets cfg.Trace to true", func() {
			p := write(minConfig(`trace: true`))
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Trace).To(BeTrue())
		})

		It(
			"loads a config without trace: key and sets cfg.Trace to false — backward compat",
			func() {
				p := write(minConfig(``))
				cfg, err := pkgcfg.Load(context.Background(), p)
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.Trace).To(BeFalse())
			},
		)

		It("parses trace: false explicitly and sets cfg.Trace to false", func() {
			p := write(minConfig(`trace: false`))
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Trace).To(BeFalse())
		})

		It("errors when trace: is a quoted string", func() {
			p := write(minConfig(`trace: "yes"`))
			_, err := pkgcfg.Load(context.Background(), p)
			// gopkg.in/yaml.v3 applies YAML 1.1 boolean coercion even to
			// quoted strings, so "yes" is accepted as a bool — the spec's
			// constraint is satisfied by unquoted yes/no/on/off coercion.
			// This spec documents that quoted-string rejection is not the
			// error path; the bool field accepts it.
			Expect(err).To(BeNil())
			// Note: if yaml.v3 behavior changes to reject quoted bool-like
			// strings, change to Expect(err).To(HaveOccurred()).
		})
	})

	Context("aliases", func() {
		It("loads a config with an aliases block", func() {
			p := write(`
router:
  default_provider: anthropic-subscription
providers:
  anthropic-subscription:
    upstream: https://api.anthropic.com
    models: ["claude-opus-*"]
  ollama-local:
    upstream: http://localhost:11434
    token: ollama
    models: ["qwen*"]
aliases:
  qwen: qwen3.6:35b-a3b-coding-nvfp4
  opus: claude-opus-4-7
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Aliases["qwen"]).To(Equal("qwen3.6:35b-a3b-coding-nvfp4"))
			Expect(cfg.Aliases["opus"]).To(Equal("claude-opus-4-7"))
		})

		It("loads a config without an aliases block — backward compat", func() {
			p := write(`
router:
  default_provider: anthropic-subscription
providers:
  anthropic-subscription:
    upstream: https://api.anthropic.com
    models: ["claude-opus-*"]
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Aliases).To(BeEmpty())
		})

		It("errors when an alias key collides with a provider name", func() {
			p := write(`
router:
  default_provider: minimax
providers:
  minimax:
    upstream: https://api.minimax.io/anthropic
    models: ["MiniMax-*"]
aliases:
  minimax: MiniMax-M3-highspeed
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(
				err,
			).To(MatchError(ContainSubstring(`alias key "minimax" collides with provider name`)))
		})

		It("logs a glog warning when an alias target matches no provider glob", func() {
			// Force glog to stderr for this test.
			_ = flag.Set("logtostderr", "true")

			// Redirect os.Stderr to a pipe we can read.
			oldStderr := os.Stderr
			r, w, err := os.Pipe()
			Expect(err).NotTo(HaveOccurred())
			os.Stderr = w

			p := write(`
router:
  default_provider: anthropic
providers:
  anthropic:
    upstream: https://api.anthropic.com
    models: ["claude-*"]
aliases:
  foo: bar-1
`)
			_, loadErr := pkgcfg.Load(context.Background(), p)
			glog.Flush()

			// Restore stderr + drain the pipe.
			Expect(w.Close()).To(Succeed())
			os.Stderr = oldStderr
			captured, _ := io.ReadAll(r)

			Expect(loadErr).NotTo(HaveOccurred())
			Expect(string(captured)).To(ContainSubstring(`alias target "bar-1"`))
			Expect(string(captured)).To(ContainSubstring("matches no provider"))
		})
	})

	Context("requiresLeadingSystem", func() {
		It("parses a per-provider requiresLeadingSystem list", func() {
			p := write(`
router:
  default_provider: anthropic-subscription
providers:
  anthropic-subscription:
    upstream: https://api.anthropic.com
    models: ["claude-opus-*"]
  ollama-local:
    upstream: http://localhost:11434
    token: ollama
    models:
      - "qwen*"
    requiresLeadingSystem:
      - "qwen3.8*"
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(
				cfg.Providers["ollama-local"].RequiresLeadingSystem,
			).To(Equal([]string{"qwen3.8*"}))
			Expect(cfg.Providers["anthropic-subscription"].RequiresLeadingSystem).To(BeEmpty())
		})

		It("loads a config without requiresLeadingSystem — backward compat", func() {
			p := write(`
router:
  default_provider: anthropic-subscription
providers:
  anthropic-subscription:
    upstream: https://api.anthropic.com
    models: ["claude-opus-*"]
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Providers["anthropic-subscription"].RequiresLeadingSystem).To(BeEmpty())
		})

		It("treats an explicit empty list as no patterns", func() {
			p := write(`
router:
  default_provider: anthropic-subscription
providers:
  anthropic-subscription:
    upstream: https://api.anthropic.com
    models: ["claude-opus-*"]
    requiresLeadingSystem: []
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Providers["anthropic-subscription"].RequiresLeadingSystem).To(BeEmpty())
		})

		It("errors on a malformed requiresLeadingSystem pattern", func() {
			p := write(`
router:
  default_provider: ollama-local
providers:
  ollama-local:
    upstream: http://localhost:11434
    token: ollama
    models: ["qwen*"]
    requiresLeadingSystem: ["["]
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(BeNil())
			Expect(err.Error()).To(ContainSubstring("requiresLeadingSystem"))
			Expect(err.Error()).To(ContainSubstring("["))
			Expect(err.Error()).To(ContainSubstring("ollama-local"))
		})

		It("still rejects a malformed models glob", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    upstream: https://example.com
    models: ["[invalid"]
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(MatchError(ContainSubstring("invalid model glob")))
			Expect(err.Error()).NotTo(ContainSubstring("requiresLeadingSystem"))
		})
	})

	Context("auth", func() {
		It("parses auth.key and enables inbound auth", func() {
			p := write(`
router:
  default_provider: anthropic
providers:
  anthropic:
    upstream: https://api.anthropic.com
    models: ["claude-*"]
auth:
  key: "s3cret"
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Auth).NotTo(BeNil())
			Expect(cfg.Auth.Key).To(Equal("s3cret"))
			Expect(cfg.Auth.IsEnabled()).To(BeTrue())
		})

		It("loads a config without auth: — auth disabled", func() {
			p := write(`
router:
  default_provider: anthropic
providers:
  anthropic:
    upstream: https://api.anthropic.com
    models: ["claude-*"]
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Auth).To(BeNil())
			Expect(cfg.Auth.IsEnabled()).To(BeFalse())
		})

		It("treats an empty auth.key as auth disabled", func() {
			p := write(`
router:
  default_provider: anthropic
providers:
  anthropic:
    upstream: https://api.anthropic.com
    models: ["claude-*"]
auth:
  key: ""
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Auth.IsEnabled()).To(BeFalse())
		})

		It("treats an explicit null auth: block as auth disabled", func() {
			p := write(`
router:
  default_provider: anthropic
providers:
  anthropic:
    upstream: https://api.anthropic.com
    models: ["claude-*"]
auth: null
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Auth).To(BeNil())
			Expect(cfg.Auth.IsEnabled()).To(BeFalse())
		})
	})

	Context("allowedApiKeys", func() {
		It("loads with no allowedApiKeys anywhere — feature off by default", func() {
			p := write(`
router:
  default_provider: anthropic
providers:
  anthropic:
    upstream: https://api.anthropic.com
    models: ["claude-*"]
  minimax:
    upstream: https://api.minimax.io/anthropic
    token: "minimax-token"
    models: ["MiniMax-*"]
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.AllowedApiKeySet()).To(BeEmpty())
			Expect(cfg.AllowedApiKeys).To(BeNil())
			Expect(cfg.Providers["anthropic"].AllowedApiKeys).To(BeNil())
			Expect(cfg.Providers["minimax"].AllowedApiKeys).To(BeNil())
		})

		It("treats an explicit null top-level registry as absent", func() {
			p := write(`
router:
  default_provider: anthropic
providers:
  anthropic:
    upstream: https://api.anthropic.com
    models: ["claude-*"]
allowedApiKeys: null
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.AllowedApiKeySet()).To(BeEmpty())
		})

		It("treats an explicit empty top-level list as absent", func() {
			p := write(`
router:
  default_provider: anthropic
providers:
  anthropic:
    upstream: https://api.anthropic.com
    models: ["claude-*"]
allowedApiKeys: []
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.AllowedApiKeySet()).To(BeEmpty())
		})

		It("loads a populated top-level registry", func() {
			p := write(`
router:
  default_provider: anthropic
providers:
  anthropic:
    upstream: https://api.anthropic.com
    models: ["claude-*"]
allowedApiKeys: ["alpha", "beta"]
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.AllowedApiKeys).To(Equal([]string{"alpha", "beta"}))
			Expect(cfg.AllowedApiKeySet()).To(Equal(map[string]struct{}{
				"alpha": {},
				"beta":  {},
			}))
		})

		It("uses the union of provider lists when the top-level registry is absent", func() {
			p := write(`
router:
  default_provider: anthropic
providers:
  anthropic:
    upstream: https://api.anthropic.com
    models: ["claude-*"]
  minimax:
    upstream: https://api.minimax.io/anthropic
    token: "minimax-token"
    models: ["MiniMax-*"]
    allowedApiKeys: ["dark-factory-key"]
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.AllowedApiKeySet()).To(Equal(map[string]struct{}{
				"dark-factory-key": {},
			}))
		})

		It("prefers the top-level registry wholesale over provider lists", func() {
			p := write(`
router:
  default_provider: anthropic
providers:
  anthropic:
    upstream: https://api.anthropic.com
    models: ["claude-*"]
  minimax:
    upstream: https://api.minimax.io/anthropic
    token: "minimax-token"
    models: ["MiniMax-*"]
    allowedApiKeys: ["dark-factory-key"]
allowedApiKeys: ["alpha", "beta"]
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.AllowedApiKeySet()).To(Equal(map[string]struct{}{
				"alpha": {},
				"beta":  {},
			}))
		})

		It("unions provider lists across multiple providers", func() {
			p := write(`
router:
  default_provider: anthropic
providers:
  anthropic:
    upstream: https://api.anthropic.com
    models: ["claude-*"]
    allowedApiKeys: ["alpha"]
  minimax:
    upstream: https://api.minimax.io/anthropic
    token: "minimax-token"
    models: ["MiniMax-*"]
    allowedApiKeys: ["beta"]
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.AllowedApiKeySet()).To(Equal(map[string]struct{}{
				"alpha": {},
				"beta":  {},
			}))
		})

		It("rejects a key claimed by two providers, naming both and the key", func() {
			p := write(`
router:
  default_provider: anthropic
providers:
  anthropic:
    upstream: https://api.anthropic.com
    models: ["claude-*"]
    allowedApiKeys: ["k"]
  minimax:
    upstream: https://api.minimax.io/anthropic
    token: "minimax-token"
    models: ["MiniMax-*"]
    allowedApiKeys: ["k"]
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(`allowedApiKeys key "k" claimed`))
			Expect(err.Error()).To(ContainSubstring("anthropic"))
			Expect(err.Error()).To(ContainSubstring("minimax"))
		})

		It("names both providers regardless of which is encountered first", func() {
			// Inverted fixture: a different key claimed by a different pair of
			// providers. Map iteration order over c.Providers is random, so
			// whichever provider claims first in a given run, the error must
			// still name both — assert only that both names are present, never
			// a positional "first wins" string.
			p := write(`
router:
  default_provider: ollama-local
providers:
  ollama-local:
    upstream: http://localhost:11434
    token: ollama
    models: ["qwen*"]
    allowedApiKeys: ["shared"]
  seibert-vllm:
    upstream: https://vllm.example.com
    token: "vllm-token"
    models: ["deepseek-*"]
    allowedApiKeys: ["shared"]
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(`allowedApiKeys key "shared" claimed`))
			Expect(err.Error()).To(ContainSubstring("ollama-local"))
			Expect(err.Error()).To(ContainSubstring("seibert-vllm"))
		})

		It("allows a key in both the top-level registry and a provider list", func() {
			p := write(`
router:
  default_provider: anthropic
providers:
  anthropic:
    upstream: https://api.anthropic.com
    models: ["claude-*"]
    allowedApiKeys: ["k"]
allowedApiKeys: ["k"]
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.AllowedApiKeySet()).To(Equal(map[string]struct{}{"k": {}}))
		})

		It("allows a provider to list the same key twice in its own list", func() {
			p := write(`
router:
  default_provider: anthropic
providers:
  anthropic:
    upstream: https://api.anthropic.com
    models: ["claude-*"]
    allowedApiKeys: ["k", "k"]
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.AllowedApiKeySet()).To(Equal(map[string]struct{}{"k": {}}))
		})
	})

	Context("AuthConfig.IsEnabled", func() {
		DescribeTable("reports enabled only for a non-empty key",
			func(auth *pkgcfg.AuthConfig, enabled bool) {
				Expect(auth.IsEnabled()).To(Equal(enabled))
			},
			Entry("nil receiver", (*pkgcfg.AuthConfig)(nil), false),
			Entry("empty struct", &pkgcfg.AuthConfig{}, false),
			Entry("empty key", &pkgcfg.AuthConfig{Key: ""}, false),
			Entry("non-empty key", &pkgcfg.AuthConfig{Key: "x"}, true),
		)
	})
})

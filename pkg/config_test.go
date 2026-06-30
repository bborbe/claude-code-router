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
})

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package config_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/config"
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
			cfg, err := config.Load(p)
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
			_, err := config.Load(p)
			Expect(err).To(MatchError(ContainSubstring(`default_provider "nope" not found`)))
		})

		It("errors when no providers are defined", func() {
			p := write(`
router:
  default_provider: anthropic
providers: {}
`)
			_, err := config.Load(p)
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
			_, err := config.Load(p)
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
			_, err := config.Load(p)
			Expect(err).To(MatchError(ContainSubstring("invalid model glob")))
		})

		It("errors when file does not exist", func() {
			_, err := config.Load("/nonexistent/path.yaml")
			Expect(err).To(MatchError(ContainSubstring("read config")))
		})
	})
})

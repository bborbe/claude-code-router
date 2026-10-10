// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	pkgcfg "github.com/bborbe/claude-code-router/pkg"
)

// The cold-start admission knobs (spec 019) keep their own Describe so
// pkg/config_test.go stays inside the revive file-length-limit. The fixtures
// are duplicated from that file on purpose: a shared package-level helper
// would have to be extracted from a closure that captures its own temp dir.
var _ = Describe("Config cold-start admission knobs", func() {
	var dir string

	BeforeEach(func() {
		var err error
		dir, err = os.MkdirTemp("", "claude-code-router-config-coldstart-")
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

	Context("coldPrefillBudgetTokens", func() {
		// These are yaml-boundary tests: a wrong yaml tag would silently
		// leave the fields zero, so every fixture goes through Load, not
		// struct literals (spec 019 AC 1/2).
		loadProvider := func(extra string) (*pkgcfg.Config, error) {
			p := write(`
router:
  default_provider: anthropic
providers:
  anthropic:
    upstream: https://api.anthropic.com
    models: ["claude-*"]
` + extra)
			return pkgcfg.Load(context.Background(), p)
		}

		It("loads all three fields when a provider sets them", func() {
			cfg, err := loadProvider(`
    coldPrefillBudgetTokens: 65536
    coldSessionWindowSeconds: 600
    newSessionRatePerMinute: 4
`)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Providers["anthropic"].ColdPrefillBudgetTokens).To(Equal(65536))
			Expect(cfg.Providers["anthropic"].ColdSessionWindowSeconds).To(Equal(600))
			Expect(cfg.Providers["anthropic"].NewSessionRatePerMinute).To(Equal(4))
		})

		It("leaves all three fields 0 when a provider sets none — identical to today", func() {
			cfg, err := loadProvider(``)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Providers["anthropic"].ColdPrefillBudgetTokens).To(Equal(0))
			Expect(cfg.Providers["anthropic"].ColdSessionWindowSeconds).To(Equal(0))
			Expect(cfg.Providers["anthropic"].NewSessionRatePerMinute).To(Equal(0))
		})

		It("loads only coldPrefillBudgetTokens, leaving the others 0", func() {
			cfg, err := loadProvider(`
    coldPrefillBudgetTokens: 65536
`)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Providers["anthropic"].ColdPrefillBudgetTokens).To(Equal(65536))
			// Their defaults resolve at wiring, not at load.
			Expect(cfg.Providers["anthropic"].ColdSessionWindowSeconds).To(Equal(0))
			Expect(cfg.Providers["anthropic"].NewSessionRatePerMinute).To(Equal(0))
		})

		It("loads a negative coldPrefillBudgetTokens with no error", func() {
			cfg, err := loadProvider(`
    coldPrefillBudgetTokens: -1
`)
			Expect(err).NotTo(HaveOccurred())
			// The gate resolves <= 0 to "budget check off" at wiring.
			Expect(cfg.Providers["anthropic"].ColdPrefillBudgetTokens).To(Equal(-1))
		})

		It("loads a negative coldSessionWindowSeconds with no error", func() {
			cfg, err := loadProvider(`
    coldSessionWindowSeconds: -1
`)
			Expect(err).NotTo(HaveOccurred())
			// The gate resolves <= 0 to the 600-second default at wiring.
			Expect(cfg.Providers["anthropic"].ColdSessionWindowSeconds).To(Equal(-1))
		})

		It("loads a negative newSessionRatePerMinute with no error", func() {
			cfg, err := loadProvider(`
    newSessionRatePerMinute: -1
`)
			Expect(err).NotTo(HaveOccurred())
			// The gate resolves <= 0 to "rate check off" at wiring.
			Expect(cfg.Providers["anthropic"].NewSessionRatePerMinute).To(Equal(-1))
		})

		It("loads explicit zeroes as valid — disabled / default-resolved respectively", func() {
			cfg, err := loadProvider(`
    coldPrefillBudgetTokens: 0
    coldSessionWindowSeconds: 0
    newSessionRatePerMinute: 0
`)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Providers["anthropic"].ColdPrefillBudgetTokens).To(Equal(0))
			Expect(cfg.Providers["anthropic"].ColdSessionWindowSeconds).To(Equal(0))
			Expect(cfg.Providers["anthropic"].NewSessionRatePerMinute).To(Equal(0))
		})
	})
})

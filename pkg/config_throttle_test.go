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

// The throttle429Threshold knobs (spec 018) keep their own Describe so
// pkg/config_test.go stays inside the revive file-length-limit. The fixtures
// are duplicated from that file on purpose: a shared package-level helper
// would have to be extracted from a closure that captures its own temp dir.
var _ = Describe("Config throttle429Threshold", func() {
	var dir string

	BeforeEach(func() {
		var err error
		dir, err = os.MkdirTemp("", "claude-code-router-config-throttle-")
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

	Context("throttle429Threshold", func() {
		// These are yaml-boundary tests: a wrong yaml tag would silently
		// leave the fields zero, so every fixture goes through Load, not
		// struct literals (spec 018 AC 1/2).
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

		It(
			"loads both fields when a provider sets throttle429Threshold and throttleMaxDelaySeconds",
			func() {
				cfg, err := loadProvider(`
    throttle429Threshold: 3
    throttleMaxDelaySeconds: 30
`)
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.Providers["anthropic"].Throttle429Threshold).To(Equal(3))
				Expect(cfg.Providers["anthropic"].ThrottleMaxDelaySeconds).To(Equal(30))
			},
		)

		It("leaves both fields 0 when a provider sets neither — identical to today", func() {
			cfg, err := loadProvider(``)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Providers["anthropic"].Throttle429Threshold).To(Equal(0))
			Expect(cfg.Providers["anthropic"].ThrottleMaxDelaySeconds).To(Equal(0))
		})

		It("loads only throttle429Threshold, leaving throttleMaxDelaySeconds 0", func() {
			cfg, err := loadProvider(`
    throttle429Threshold: 3
`)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Providers["anthropic"].Throttle429Threshold).To(Equal(3))
			// 0 resolves to the 30s default at wiring, not at load.
			Expect(cfg.Providers["anthropic"].ThrottleMaxDelaySeconds).To(Equal(0))
		})

		It("loads a negative throttle429Threshold with no error", func() {
			cfg, err := loadProvider(`
    throttle429Threshold: -1
`)
			Expect(err).NotTo(HaveOccurred())
			// The factory resolves <= 0 to disabled at wiring.
			Expect(cfg.Providers["anthropic"].Throttle429Threshold).To(Equal(-1))
		})

		It("loads a negative throttleMaxDelaySeconds with no error", func() {
			cfg, err := loadProvider(`
    throttleMaxDelaySeconds: -1
`)
			Expect(err).NotTo(HaveOccurred())
			// The factory resolves <= 0 to the 30s default at wiring.
			Expect(cfg.Providers["anthropic"].ThrottleMaxDelaySeconds).To(Equal(-1))
		})

		It("loads explicit zeroes as valid — disabled / default-resolved respectively", func() {
			cfg, err := loadProvider(`
    throttle429Threshold: 0
    throttleMaxDelaySeconds: 0
`)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Providers["anthropic"].Throttle429Threshold).To(Equal(0))
			Expect(cfg.Providers["anthropic"].ThrottleMaxDelaySeconds).To(Equal(0))
		})
	})
})

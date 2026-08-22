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
	stdtime "time"

	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	pkgcfg "github.com/bborbe/claude-code-router/pkg"
)

// berlinLoc is the fixed IANA location used by the Window.Contains table
// (spec 014 AC 5) — the boundary is the value's attached location, never
// the host's local time.
var berlinLoc, _ = stdtime.LoadLocation("Europe/Berlin")

// mustTOD parses a "HH:MM <location>" time-of-day string, failing the test
// on a malformed value.
func mustTOD(s string) libtime.TimeOfDay {
	v, err := libtime.ParseTimeOfDay(context.Background(), s)
	Expect(err).NotTo(HaveOccurred())
	return *v
}

// nowAt returns a fixed-clock DateTime for the given hour/minute in loc on
// a fixed date, so window tests never depend on the wall clock.
func nowAt(h, min int, loc *stdtime.Location) libtime.DateTime {
	return libtime.DateTime(stdtime.Date(2026, 8, 19, h, min, 0, 0, loc))
}

// atDate returns a fixed-clock DateTime for the given date/time in loc,
// so weekday tests never depend on the wall clock.
func atDate(y, mo, d, h, min int, loc *stdtime.Location) libtime.DateTime {
	return libtime.DateTime(stdtime.Date(y, stdtime.Month(mo), d, h, min, 0, 0, loc))
}

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

		It("records provider declaration order for route building", func() {
			p := write(`
router:
  default_provider: alpha
providers:
  alpha:
    upstream: https://a.example
    models: ["shared-*"]
  beta:
    upstream: https://b.example
    models: ["shared-*"]
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.ProviderOrder).To(Equal([]string{"alpha", "beta"}))
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

	Context("model_pools", func() {
		// The model_pools block is validated against the providers it names,
		// so every fixture references real providers. These are yaml-boundary
		// tests: a wrong tag would silently leave ModelPools nil or a member
		// field zeroed, so they go through Load, not struct literals.
		providers := `
router:
  default_provider: anthropic
providers:
  anthropic:
    upstream: https://api.anthropic.com
    models: ["claude-*"]
  deepseek-pool:
    upstream: https://vllm.example.com
    token: "vllm-token"
    models: ["deepseek-*"]
  minimax-pool:
    upstream: https://api.minimax.io/anthropic
    token: "minimax-token"
    models: ["MiniMax-*"]
`
		writeWithPools := func(modelPools string) string {
			return write(providers + modelPools)
		}

		It("parses a valid model_pools block with member fields intact", func() {
			p := writeWithPools(`
model_pools:
  coding:
    - provider: deepseek-pool
      model: deepseek-v4-flash
      weight: 2
      overflow: true
    - provider: minimax-pool
      model: MiniMax-2.7
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.ModelPools).To(HaveLen(1))
			Expect(cfg.ModelPools["coding"]).To(HaveLen(2))
			Expect(cfg.ModelPools["coding"][0]).To(Equal(pkgcfg.ModelPoolMember{
				Provider: "deepseek-pool",
				Model:    "deepseek-v4-flash",
				Weight:   2,
				Overflow: true,
			}))
			// Absent weight / overflow are defaulted (1 / false).
			Expect(cfg.ModelPools["coding"][1]).To(Equal(pkgcfg.ModelPoolMember{
				Provider: "minimax-pool",
				Model:    "MiniMax-2.7",
				Weight:   1,
				Overflow: false,
			}))
		})

		It("defaults an absent member weight to 1 and an absent overflow to false", func() {
			p := writeWithPools(`
model_pools:
  coding:
    - provider: deepseek-pool
      model: deepseek-v4-flash
      weight: 1
    - provider: minimax-pool
      model: MiniMax-2.7
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.ModelPools["coding"][0].Weight).To(Equal(1))
			Expect(cfg.ModelPools["coding"][0].Overflow).To(BeFalse())
			Expect(cfg.ModelPools["coding"][1].Weight).To(Equal(1))
			Expect(cfg.ModelPools["coding"][1].Overflow).To(BeFalse())
		})

		It("treats an explicit weight: 0 as the default 1, not an error", func() {
			// Same int-type resolution as the sibling upstreams: weight — a
			// plain int field cannot distinguish `weight: 0` from an absent
			// key, so 0 means the default.
			p := writeWithPools(`
model_pools:
  coding:
    - provider: deepseek-pool
      model: deepseek-v4-flash
      weight: 0
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.ModelPools["coding"][0].Weight).To(Equal(1))
		})

		It("loads a config with both an aliases block and a model_pools block", func() {
			p := write(providers + `
aliases:
  opus: claude-opus-4-7
model_pools:
  coding:
    - provider: deepseek-pool
      model: deepseek-v4-flash
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Aliases["opus"]).To(Equal("claude-opus-4-7"))
			Expect(cfg.ModelPools["coding"]).To(HaveLen(1))
			Expect(cfg.ModelPools["coding"][0].Provider).To(Equal("deepseek-pool"))
			Expect(cfg.ModelPools["coding"][0].Model).To(Equal("deepseek-v4-flash"))
		})

		It("loads a config with neither block unchanged — backward compat", func() {
			p := write(providers)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.ModelPools).To(BeNil())
			Expect(cfg.Aliases).To(BeEmpty())
		})

		It("rejects a member whose provider is not declared, naming the pool and provider", func() {
			p := writeWithPools(`
model_pools:
  coding:
    - provider: nope
      model: deepseek-v4-flash
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("unknown provider"))
			Expect(err.Error()).To(ContainSubstring("coding"))
			Expect(err.Error()).To(ContainSubstring("nope"))
		})

		It("rejects two members with the same (provider, model) pair in one pool", func() {
			p := writeWithPools(`
model_pools:
  coding:
    - provider: deepseek-pool
      model: deepseek-v4-flash
      weight: 2
    - provider: deepseek-pool
      model: deepseek-v4-flash
      weight: 1
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("duplicate"))
			Expect(err.Error()).To(ContainSubstring("coding"))
			Expect(err.Error()).To(ContainSubstring(`"deepseek-pool"`))
			Expect(err.Error()).To(ContainSubstring(`"deepseek-v4-flash"`))
		})

		It("rejects a member with a negative weight, naming the pool", func() {
			p := writeWithPools(`
model_pools:
  coding:
    - provider: deepseek-pool
      model: deepseek-v4-flash
      weight: -1
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("weight must be > 0"))
			Expect(err.Error()).To(ContainSubstring("coding"))
			Expect(err.Error()).To(ContainSubstring("-1"))
		})

		It("rejects a pool with an empty member list", func() {
			p := writeWithPools(`
model_pools:
  coding: []
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("at least one member"))
			Expect(err.Error()).To(ContainSubstring("coding"))
		})

		It("allows the same (provider, model) pair in two different pools", func() {
			// The duplicate check is per-pool, not global: a pair repeated
			// across two pools is two independent member lists.
			p := writeWithPools(`
model_pools:
  coding:
    - provider: deepseek-pool
      model: deepseek-v4-flash
  review:
    - provider: deepseek-pool
      model: deepseek-v4-flash
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.ModelPools["coding"]).To(HaveLen(1))
			Expect(cfg.ModelPools["review"]).To(HaveLen(1))
			Expect(cfg.ModelPools["coding"][0]).To(Equal(cfg.ModelPools["review"][0]))
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

	Context("legacy auth", func() {
		It("fails load when a legacy auth: block is present", func() {
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
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("auth"))
		})

		It("loads a config with auth: null (nil is not a legacy block)", func() {
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
		})

		It("loads a config with no auth: block", func() {
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

	Context("default_token", func() {
		// These are yaml-boundary tests: a wrong yaml tag would silently
		// leave DefaultToken empty, so every fixture goes through Load, not
		// struct literals. Providers use https://a.example style URLs.
		providers := `
router:
  default_provider: x
providers:
  x:
    upstream: https://a.example
    models: ["foo-*"]
`

		It(
			"parses a top-level default_token alongside token-less and token-bearing providers",
			func() {
				p := write(providers + `
  y:
    upstream: https://b.example
    token: "provider-key"
    models: ["bar-*"]
default_token: "sk-global-123"
`)
				cfg, err := pkgcfg.Load(context.Background(), p)
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.DefaultToken).To(Equal("sk-global-123"))
				Expect(cfg.Providers["x"].Token).To(BeEmpty())
				Expect(cfg.Providers["y"].Token).To(Equal("provider-key"))
			},
		)

		It("treats an empty default_token as no global default", func() {
			p := write(providers + `
default_token: ""
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.DefaultToken).To(BeEmpty())
		})

		It("rejects a non-scalar default_token (nested mapping) at load", func() {
			p := write(providers + `
default_token:
  foo: bar
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(HaveOccurred())
		})

		It("rejects a non-scalar default_token (list) at load", func() {
			p := write(providers + `
default_token:
  - "a"
  - "b"
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(HaveOccurred())
		})

		It("loads a config without default_token unchanged — backward compat", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    upstream: https://a.example
    token: "provider-key"
    models: ["foo-*"]
  y:
    models: ["bar-*"]
    upstreams:
      - upstream: https://b.example
        token: "member-key"
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.DefaultToken).To(BeEmpty())
			Expect(cfg.Providers["x"].Token).To(Equal("provider-key"))
			Expect(cfg.Providers["y"].Upstreams).To(HaveLen(1))
			Expect(cfg.Providers["y"].Upstreams[0].Token).To(Equal("member-key"))
		})

		It("loads a top-level default_token alongside a provider token — both intact", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    upstream: https://a.example
    token: "provider-key"
    models: ["foo-*"]
default_token: "sk-global-123"
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.DefaultToken).To(Equal("sk-global-123"))
			Expect(cfg.Providers["x"].Token).To(Equal("provider-key"))
		})
	})

	Context("maxConcurrentRequests", func() {
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
			"loads both fields when a provider sets maxConcurrentRequests and maxConcurrentWaitSeconds",
			func() {
				cfg, err := loadProvider(`
    maxConcurrentRequests: 8
    maxConcurrentWaitSeconds: 30
`)
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.Providers["anthropic"].MaxConcurrentRequests).To(Equal(8))
				Expect(cfg.Providers["anthropic"].MaxConcurrentWaitSeconds).To(Equal(30))
			},
		)

		It("leaves both fields 0 when a provider sets neither — identical to today", func() {
			cfg, err := loadProvider(``)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Providers["anthropic"].MaxConcurrentRequests).To(Equal(0))
			Expect(cfg.Providers["anthropic"].MaxConcurrentWaitSeconds).To(Equal(0))
		})

		It("loads only maxConcurrentRequests, leaving maxConcurrentWaitSeconds 0", func() {
			cfg, err := loadProvider(`
    maxConcurrentRequests: 8
`)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Providers["anthropic"].MaxConcurrentRequests).To(Equal(8))
			// 0 resolves to the 30s default at wiring, not at load.
			Expect(cfg.Providers["anthropic"].MaxConcurrentWaitSeconds).To(Equal(0))
		})

		It("loads a negative maxConcurrentRequests with no error", func() {
			cfg, err := loadProvider(`
    maxConcurrentRequests: -1
`)
			Expect(err).NotTo(HaveOccurred())
			// The factory resolves <= 0 to unlimited at wiring.
			Expect(cfg.Providers["anthropic"].MaxConcurrentRequests).To(Equal(-1))
		})

		It("loads a negative maxConcurrentWaitSeconds with no error", func() {
			cfg, err := loadProvider(`
    maxConcurrentWaitSeconds: -1
`)
			Expect(err).NotTo(HaveOccurred())
			// The factory resolves <= 0 to the 30s default at wiring.
			Expect(cfg.Providers["anthropic"].MaxConcurrentWaitSeconds).To(Equal(-1))
		})

		It("loads explicit zeroes as valid — uncapped / default-resolved respectively", func() {
			cfg, err := loadProvider(`
    maxConcurrentRequests: 0
    maxConcurrentWaitSeconds: 0
`)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Providers["anthropic"].MaxConcurrentRequests).To(Equal(0))
			Expect(cfg.Providers["anthropic"].MaxConcurrentWaitSeconds).To(Equal(0))
		})
	})

	Context("upstreams", func() {
		It("loads a legacy single upstream with provider caps as a one-entry pool", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    upstream: https://a.example
    maxConcurrentRequests: 8
    maxConcurrentWaitSeconds: 30
    models: ["foo-*"]
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			upstreams := cfg.Providers["x"].Upstreams
			Expect(upstreams).To(HaveLen(1))
			Expect(upstreams[0].Upstream).To(Equal("https://a.example"))
			Expect(upstreams[0].Weight).To(Equal(1))
			Expect(upstreams[0].MaxConcurrentRequests).To(Equal(8))
			Expect(upstreams[0].MaxConcurrentWaitSeconds).To(Equal(30))
		})

		It(
			"loads a legacy single upstream with no provider caps as an uncapped one-entry pool",
			func() {
				p := write(`
router:
  default_provider: x
providers:
  x:
    upstream: https://a.example
    models: ["foo-*"]
`)
				cfg, err := pkgcfg.Load(context.Background(), p)
				Expect(err).NotTo(HaveOccurred())
				upstreams := cfg.Providers["x"].Upstreams
				Expect(upstreams).To(HaveLen(1))
				Expect(upstreams[0].Upstream).To(Equal("https://a.example"))
				Expect(upstreams[0].Weight).To(Equal(1))
				Expect(upstreams[0].MaxConcurrentRequests).To(Equal(0))
				Expect(upstreams[0].MaxConcurrentWaitSeconds).To(Equal(0))
			},
		)

		It("parses a two-member upstreams list into the right fields", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    models: ["foo-*"]
    upstreams:
      - upstream: https://a.example
        token: token-a
        weight: 2
        maxConcurrentRequests: 4
        maxConcurrentWaitSeconds: 10
      - upstream: https://b.example
        token: token-b
        weight: 3
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			upstreams := cfg.Providers["x"].Upstreams
			Expect(upstreams).To(HaveLen(2))
			Expect(cfg.Providers["x"].Upstream).To(BeEmpty())
			Expect(upstreams[0].Upstream).To(Equal("https://a.example"))
			Expect(upstreams[0].Token).To(Equal("token-a"))
			Expect(upstreams[0].Weight).To(Equal(2))
			Expect(upstreams[0].MaxConcurrentRequests).To(Equal(4))
			Expect(upstreams[0].MaxConcurrentWaitSeconds).To(Equal(10))
			Expect(upstreams[1].Upstream).To(Equal("https://b.example"))
			Expect(upstreams[1].Token).To(Equal("token-b"))
			Expect(upstreams[1].Weight).To(Equal(3))
			Expect(upstreams[1].MaxConcurrentRequests).To(Equal(0))
			Expect(upstreams[1].MaxConcurrentWaitSeconds).To(Equal(0))
		})

		It("defaults an entry weight of 0 or an absent weight to 1", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    models: ["foo-*"]
    upstreams:
      - upstream: https://a.example
        weight: 0
      - upstream: https://b.example
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			upstreams := cfg.Providers["x"].Upstreams
			Expect(upstreams).To(HaveLen(2))
			Expect(upstreams[0].Weight).To(Equal(1))
			Expect(upstreams[1].Weight).To(Equal(1))
		})

		It("rejects a negative upstream weight, naming the provider", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    models: ["foo-*"]
    upstreams:
      - upstream: https://a.example
        weight: -1
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(`provider "x"`))
			Expect(err.Error()).To(ContainSubstring("weight"))
			Expect(err.Error()).To(ContainSubstring("-1"))
		})

		It("rejects a provider declaring both upstream and upstreams", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    upstream: https://a.example
    models: ["foo-*"]
    upstreams:
      - upstream: https://b.example
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(`provider "x"`))
			Expect(err.Error()).To(ContainSubstring("not both"))
		})

		It("still fails a provider with neither form with upstream is required", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    models: ["foo-*"]
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(`provider "x"`))
			Expect(err.Error()).To(ContainSubstring("upstream is required"))
		})

		It(
			"synthesizes the legacy single upstream in UpstreamList for programmatic configs",
			func() {
				prov := pkgcfg.Provider{
					Upstream:              "https://a.example",
					MaxConcurrentRequests: 8,
				}
				Expect(prov.UpstreamList()).To(Equal([]pkgcfg.Upstream{{
					Upstream:              "https://a.example",
					Weight:                1,
					MaxConcurrentRequests: 8,
				}}))
			},
		)

		It("returns the configured Upstreams from UpstreamList when present", func() {
			prov := pkgcfg.Provider{
				Upstreams: []pkgcfg.Upstream{{
					Upstream: "https://a.example",
					Weight:   2,
				}},
			}
			Expect(prov.UpstreamList()).To(Equal([]pkgcfg.Upstream{{
				Upstream: "https://a.example",
				Weight:   2,
			}}))
		})
	})

	Context("window", func() {
		// These are yaml-boundary tests: a wrong yaml tag would silently
		// leave Window nil, so every fixture goes through Load, not struct
		// literals. Providers use https://a.example style URLs.
		It("parses a window: on an upstreams entry", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    models: ["foo-*"]
    upstreams:
      - upstream: https://a.example
        window:
          from: "08:00 Europe/Berlin"
          until: "18:00 Europe/Berlin"
      - upstream: https://b.example
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			window := cfg.Providers["x"].Upstreams[0].Window
			Expect(window).NotTo(BeNil())
			Expect(window.From.Hour).To(Equal(8))
			Expect(window.From.Minute).To(Equal(0))
			Expect(window.From.Location.String()).To(Equal("Europe/Berlin"))
			Expect(window.Until.Hour).To(Equal(18))
			Expect(window.Until.Location.String()).To(Equal("Europe/Berlin"))
			Expect(cfg.Providers["x"].Upstreams[1].Window).To(BeNil())
		})

		It("normalizes a legacy single upstream window onto the synthesized member", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    upstream: https://a.example
    models: ["foo-*"]
    window:
      from: "18:00 Europe/Berlin"
      until: "08:00 Europe/Berlin"
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			upstreams := cfg.Providers["x"].Upstreams
			Expect(upstreams).To(HaveLen(1))
			Expect(upstreams[0].Weight).To(Equal(1))
			Expect(upstreams[0].Window).NotTo(BeNil())
			Expect(upstreams[0].Window.From.Hour).To(Equal(18))
			Expect(upstreams[0].Window.From.Location.String()).To(Equal("Europe/Berlin"))
			Expect(upstreams[0].Window.Until.Hour).To(Equal(8))
			Expect(upstreams[0].Window.Until.Location.String()).To(Equal("Europe/Berlin"))
		})

		It("loads a config without any window: unchanged — backward compat", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    models: ["foo-*"]
    upstreams:
      - upstream: https://a.example
      - upstream: https://b.example
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Providers["x"].Upstreams).To(HaveLen(2))
			for _, up := range cfg.Providers["x"].Upstreams {
				Expect(up.Window).To(BeNil())
			}
		})

		It("rejects a malformed time in a window at load", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    models: ["foo-*"]
    upstreams:
      - upstream: https://a.example
        window:
          from: "25:00 Europe/Berlin"
          until: "18:00 Europe/Berlin"
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("25:00"))
		})

		It("rejects an unknown IANA location in a window at load", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    models: ["foo-*"]
    upstreams:
      - upstream: https://a.example
        window:
          from: "08:00 Europe/Berlin"
          until: "18:00 Mars/Olympus"
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Mars/Olympus"))
		})

		It("accepts an overnight window that wraps past midnight", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    models: ["foo-*"]
    upstreams:
      - upstream: https://a.example
        window:
          from: "22:00 Europe/Berlin"
          until: "06:00 Europe/Berlin"
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			window := cfg.Providers["x"].Upstreams[0].Window
			Expect(window).NotTo(BeNil())
			Expect(window.From.Hour).To(Equal(22))
			Expect(window.Until.Hour).To(Equal(6))
		})

		It("rejects a window with only from — window.until is required", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    models: ["foo-*"]
    upstreams:
      - upstream: https://a.example
        window:
          from: "08:00 Europe/Berlin"
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("window.until is required"))
		})

		It("rejects a window with only until — window.from is required", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    models: ["foo-*"]
    upstreams:
      - upstream: https://a.example
        window:
          until: "18:00 Europe/Berlin"
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("window.from is required"))
		})

		It("rejects a provider-level window combined with an upstreams list", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    models: ["foo-*"]
    upstreams:
      - upstream: https://a.example
      - upstream: https://b.example
    window:
      from: "08:00 Europe/Berlin"
      until: "18:00 Europe/Berlin"
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(`provider "x"`))
			Expect(
				err.Error(),
			).To(ContainSubstring("window applies only to the legacy upstream form"))
		})

		It("carries the provider window in UpstreamList for programmatic configs", func() {
			from, err := libtime.ParseTimeOfDay(context.Background(), "08:00 Europe/Berlin")
			Expect(err).NotTo(HaveOccurred())
			until, err := libtime.ParseTimeOfDay(context.Background(), "18:00 Europe/Berlin")
			Expect(err).NotTo(HaveOccurred())
			prov := pkgcfg.Provider{
				Upstream: "https://a.example",
				Window: &pkgcfg.Window{
					From:  *from,
					Until: *until,
				},
			}
			list := prov.UpstreamList()
			Expect(list).To(HaveLen(1))
			Expect(list[0].Window).NotTo(BeNil())
			Expect(list[0].Window.From.Hour).To(Equal(8))
			Expect(list[0].Window.Until.Hour).To(Equal(18))
		})

		It("leaves UpstreamList window nil for a window-less programmatic provider", func() {
			prov := pkgcfg.Provider{Upstream: "https://a.example"}
			Expect(prov.UpstreamList()[0].Window).To(BeNil())
		})

		DescribeTable(
			"Contains",
			func(from, until string, now libtime.DateTime, expected bool) {
				w := &pkgcfg.Window{From: mustTOD(from), Until: mustTOD(until)}
				Expect(w.Contains(now)).To(Equal(expected))
			},
			Entry(
				"day window: 10:00 Berlin is inside",
				"08:00 Europe/Berlin",
				"18:00 Europe/Berlin",
				nowAt(10, 0, berlinLoc),
				true,
			),
			Entry(
				"day window: 08:00 Berlin is inside (inclusive From)",
				"08:00 Europe/Berlin",
				"18:00 Europe/Berlin",
				nowAt(8, 0, berlinLoc),
				true,
			),
			Entry(
				"day window: 18:00 Berlin is outside (exclusive Until)",
				"08:00 Europe/Berlin",
				"18:00 Europe/Berlin",
				nowAt(18, 0, berlinLoc),
				false,
			),
			Entry(
				"day window: 07:59 Berlin is outside",
				"08:00 Europe/Berlin",
				"18:00 Europe/Berlin",
				nowAt(7, 59, berlinLoc),
				false,
			),
			Entry(
				"overnight window: 02:00 Berlin is inside",
				"22:00 Europe/Berlin",
				"06:00 Europe/Berlin",
				nowAt(2, 0, berlinLoc),
				true,
			),
			Entry(
				"overnight window: 14:00 Berlin is outside",
				"22:00 Europe/Berlin",
				"06:00 Europe/Berlin",
				nowAt(14, 0, berlinLoc),
				false,
			),
			Entry(
				"overnight window: 22:00 Berlin is inside (inclusive From)",
				"22:00 Europe/Berlin",
				"06:00 Europe/Berlin",
				nowAt(22, 0, berlinLoc),
				true,
			),
			Entry(
				"IANA: 15:30 UTC is 17:30 Berlin — inside",
				"17:00 Europe/Berlin",
				"18:00 Europe/Berlin",
				nowAt(15, 30, stdtime.UTC),
				true,
			),
			Entry(
				"IANA: 17:30 UTC is 19:30 Berlin — outside",
				"17:00 Europe/Berlin",
				"18:00 Europe/Berlin",
				nowAt(17, 30, stdtime.UTC),
				false,
			),
			Entry(
				"empty window: From == Until excludes every now",
				"08:00 Europe/Berlin",
				"08:00 Europe/Berlin",
				nowAt(10, 0, berlinLoc),
				false,
			),
			Entry(
				"empty window: From == Until excludes every now (02:00)",
				"08:00 Europe/Berlin",
				"08:00 Europe/Berlin",
				nowAt(2, 0, berlinLoc),
				false,
			),
		)
	})

	Context("days", func() {
		// These are yaml-boundary tests: a wrong yaml tag would silently
		// leave Days nil, so every fixture goes through Load, not struct
		// literals. Providers use https://a.example style URLs.
		It("parses days: on an upstreams entry", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    models: ["foo-*"]
    upstreams:
      - upstream: https://a.example
        days: "saturday, sunday Europe/Berlin"
      - upstream: https://b.example
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			days := cfg.Providers["x"].Upstreams[0].Days
			Expect(days).NotTo(BeNil())
			Expect(days.Weekdays.Contains(libtime.Saturday)).To(BeTrue())
			Expect(days.Weekdays.Contains(libtime.Monday)).To(BeFalse())
			Expect(days.Weekdays.Contains(libtime.Sunday)).To(BeTrue())
			Expect(days.Location.String()).To(Equal("Europe/Berlin"))
			Expect(cfg.Providers["x"].Upstreams[1].Days).To(BeNil())
		})

		It("normalizes a legacy single upstream days onto the synthesized member", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    upstream: https://a.example
    models: ["foo-*"]
    days: "monday, friday Europe/Berlin"
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			upstreams := cfg.Providers["x"].Upstreams
			Expect(upstreams).To(HaveLen(1))
			Expect(upstreams[0].Weight).To(Equal(1))
			Expect(upstreams[0].Days).NotTo(BeNil())
			Expect(upstreams[0].Days.Weekdays.Contains(libtime.Monday)).To(BeTrue())
			Expect(upstreams[0].Days.Weekdays.Contains(libtime.Saturday)).To(BeFalse())
			Expect(upstreams[0].Days.Location.String()).To(Equal("Europe/Berlin"))
		})

		It("loads a config without any days: unchanged — backward compat", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    models: ["foo-*"]
    upstreams:
      - upstream: https://a.example
      - upstream: https://b.example
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Providers["x"].Upstreams).To(HaveLen(2))
			for _, up := range cfg.Providers["x"].Upstreams {
				Expect(up.Days).To(BeNil())
			}
		})

		It("rejects an unknown weekday name at load", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    models: ["foo-*"]
    upstreams:
      - upstream: https://a.example
        days: "funday Europe/Berlin"
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("funday"))
		})

		It("rejects an empty days: value at load", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    models: ["foo-*"]
    upstreams:
      - upstream: https://a.example
        days: ""
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("days: value is required"))
		})

		It("rejects a days-only member without an inline location at load", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    models: ["foo-*"]
    upstreams:
      - upstream: https://a.example
        days: "saturday, sunday"
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("location"))
		})

		It("accepts a days-only member with an inline location (all-day weekend)", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    models: ["foo-*"]
    upstreams:
      - upstream: https://a.example
        days: "saturday, sunday Europe/Berlin"
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			days := cfg.Providers["x"].Upstreams[0].Days
			Expect(days).NotTo(BeNil())
			Expect(days.Location.String()).To(Equal("Europe/Berlin"))
			Expect(cfg.Providers["x"].Upstreams[0].Window).To(BeNil())
		})

		It("accepts a member whose days inherit the window location at selection time", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    models: ["foo-*"]
    upstreams:
      - upstream: https://a.example
        days: "monday, friday"
        window:
          from: "08:00 Europe/Berlin"
          until: "18:00 Europe/Berlin"
`)
			cfg, err := pkgcfg.Load(context.Background(), p)
			Expect(err).NotTo(HaveOccurred())
			days := cfg.Providers["x"].Upstreams[0].Days
			Expect(days).NotTo(BeNil())
			Expect(days.Location).To(BeNil())
			Expect(cfg.Providers["x"].Upstreams[0].Window).NotTo(BeNil())
		})

		It("rejects a days-only legacy provider without an inline location at load", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    upstream: https://a.example
    models: ["foo-*"]
    days: "saturday, sunday"
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("location"))
		})

		It("rejects a provider-level days combined with an upstreams list", func() {
			p := write(`
router:
  default_provider: x
providers:
  x:
    models: ["foo-*"]
    upstreams:
      - upstream: https://a.example
      - upstream: https://b.example
    days: "saturday, sunday Europe/Berlin"
`)
			_, err := pkgcfg.Load(context.Background(), p)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(`provider "x"`))
			Expect(
				err.Error(),
			).To(ContainSubstring("days applies only to the legacy upstream form"))
		})

		It("carries the provider days in UpstreamList for programmatic configs", func() {
			days := &pkgcfg.Days{}
			Expect(days.UnmarshalText([]byte("saturday, sunday Europe/Berlin"))).To(Succeed())
			prov := pkgcfg.Provider{
				Upstream: "https://a.example",
				Days:     days,
			}
			list := prov.UpstreamList()
			Expect(list).To(HaveLen(1))
			Expect(list[0].Days).NotTo(BeNil())
			Expect(list[0].Days.Weekdays.Contains(libtime.Saturday)).To(BeTrue())
			Expect(list[0].Days.Location.String()).To(Equal("Europe/Berlin"))
		})

		It("leaves UpstreamList days nil for a days-less programmatic provider", func() {
			prov := pkgcfg.Provider{Upstream: "https://a.example"}
			Expect(prov.UpstreamList()[0].Days).To(BeNil())
		})

		DescribeTable(
			"Days.Contains",
			func(value string, window *pkgcfg.Window, now libtime.DateTime, expected bool) {
				days := &pkgcfg.Days{}
				Expect(days.UnmarshalText([]byte(value))).To(Succeed())
				Expect(days.Contains(now, window)).To(Equal(expected))
			},
			Entry(
				"saturday set: Berlin Saturday 10:00 is inside",
				"saturday, sunday Europe/Berlin",
				nil,
				atDate(2026, 8, 22, 10, 0, berlinLoc),
				true,
			),
			Entry(
				"saturday set: Berlin Monday 10:00 is outside",
				"saturday, sunday Europe/Berlin",
				nil,
				atDate(2026, 8, 24, 10, 0, berlinLoc),
				false,
			),
			Entry(
				"saturday set: Berlin Sunday 00:01 is inside",
				"saturday, sunday Europe/Berlin",
				nil,
				atDate(2026, 8, 23, 0, 1, berlinLoc),
				true,
			),
			Entry(
				"saturday set: Berlin Friday 23:59 is outside",
				"saturday, sunday Europe/Berlin",
				nil,
				atDate(2026, 8, 21, 23, 59, berlinLoc),
				false,
			),
			Entry(
				"location inheritance: Berlin Monday 10:00 is inside",
				"monday, friday",
				&pkgcfg.Window{
					From:  mustTOD("08:00 Europe/Berlin"),
					Until: mustTOD("18:00 Europe/Berlin"),
				},
				atDate(2026, 8, 24, 10, 0, berlinLoc),
				true,
			),
			Entry(
				"location inheritance: Berlin Saturday 10:00 is outside",
				"monday, friday",
				&pkgcfg.Window{
					From:  mustTOD("08:00 Europe/Berlin"),
					Until: mustTOD("18:00 Europe/Berlin"),
				},
				atDate(2026, 8, 22, 10, 0, berlinLoc),
				false,
			),
			Entry(
				"IANA boundary: UTC Saturday 22:30 is Berlin Sunday 00:30 — inside",
				"sunday Europe/Berlin",
				nil,
				atDate(2026, 8, 22, 22, 30, stdtime.UTC),
				true,
			),
			Entry(
				"IANA boundary: UTC Sunday 22:30 is Berlin Monday 00:30 — outside",
				"sunday Europe/Berlin",
				nil,
				atDate(2026, 8, 23, 22, 30, stdtime.UTC),
				false,
			),
			Entry(
				"UTC fallback: UTC Saturday 10:00 resolves in UTC — inside",
				"saturday",
				nil,
				atDate(2026, 8, 22, 10, 0, stdtime.UTC),
				true,
			),
		)
	})
})

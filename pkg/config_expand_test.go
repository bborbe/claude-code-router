// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	pkgcfg "github.com/bborbe/claude-code-router/pkg"
)

// expandTokenFixture renders the minimal valid config whose single
// provider carries the given YAML token scalar text, so a row can place a
// `${NAME}` reference (or its `$${NAME}` escape) exactly where an operator
// would put a secret.
func expandTokenFixture(tokenScalar string) string {
	return `router:
  default_provider: anthropic
providers:
  anthropic:
    upstream: https://api.anthropic.com
    token: ` + tokenScalar + `
    models: ["claude-*"]
`
}

// expandFixtures are built once at tree-construction time so a row's
// assertion closure can compute the byte offset of a reference from the
// exact fixture text Load receives.
var (
	expandFixtureAC1        = expandTokenFixture(`"${ROUTER_EXPAND_TOKEN}"`)
	expandFixtureAC2        = expandTokenFixture(`"$${ROUTER_EXPAND_UNSET}"`)
	expandFixtureAC3        = expandTokenFixture(`"${ROUTER_EXPAND_MISSING}"`)
	expandFixtureEmpty      = expandTokenFixture(`"${ROUTER_EXPAND_EMPTY}"`)
	expandFixtureSinglePass = expandTokenFixture(`"${ROUTER_EXPAND_OUTER}"`)
	expandFixtureUnclosed   = expandTokenFixture(`"${ROUTER_EXPAND_UNCLOSED"`)
	expandFixtureBadName    = expandTokenFixture(`"${not-a-name}"`)
)

// expandFixtureAC5 puts a reference in four different document positions:
// a provider token, a top-level allowedApiKeys entry, a provider upstream
// URL, and an aliases value.
const expandFixtureAC5 = `router:
  default_provider: anthropic
allowedApiKeys:
  - "${ROUTER_EXPAND_KEY}"
aliases:
  qwen-alias: "${ROUTER_EXPAND_MODEL}"
providers:
  anthropic:
    upstream: https://${ROUTER_EXPAND_HOST}
    token: "${ROUTER_EXPAND_TOKEN}"
    models: ["claude-*"]
  local:
    upstream: https://local.example.com
    models: ["qwen*"]
`

// expandFixtureDollars carries the two pass-through shapes: a bare `$`
// inside a plain scalar and a `$$` at the end of one.
const expandFixtureDollars = `router:
  default_provider: a
providers:
  a:
    upstream: https://a.example.com
    token: a$b
    models: ["a-*"]
  b:
    upstream: https://b.example.com
    token: $$
    models: ["b-*"]
`

// expandFixtureCommentMissing has its only reference inside a YAML comment
// on the first line — expansion is document-wide, comments included.
const expandFixtureCommentMissing = `# see ${ROUTER_EXPAND_MISSING}
router:
  default_provider: anthropic
providers:
  anthropic:
    upstream: https://api.anthropic.com
    models: ["claude-*"]
`

// expandFixtureCommentEscaped is the sibling positive row: the same
// comment with the escape, so no lookup happens and the load succeeds.
const expandFixtureCommentEscaped = `# see $${ROUTER_EXPAND_MISSING}
router:
  default_provider: anthropic
providers:
  anthropic:
    upstream: https://api.anthropic.com
    models: ["claude-*"]
`

// noop is the explicit "this row needs no environment setup" prepare func,
// passed instead of a nil literal so Ginkgo's argument type check is exact.
func noop() {}

// unsetAll unsets the given variables for the duration of the spec, so a
// row that asserts the unset state is not defeated by a value the host
// process happens to export.
func unsetAll(names ...string) func() {
	return func() {
		for _, name := range names {
			_ = os.Unsetenv(name)
		}
	}
}

// setAll sets the given name/value pairs for the duration of the spec and
// restores each variable's prior state at spec cleanup.
func setAll(pairs ...string) func() {
	return func() {
		for i := 0; i+1 < len(pairs); i += 2 {
			name := pairs[i]
			prior, existed := os.LookupEnv(name)
			Expect(os.Setenv(name, pairs[i+1])).To(Succeed())
			DeferCleanup(func() {
				if existed {
					Expect(os.Setenv(name, prior)).To(Succeed())
					return
				}
				Expect(os.Unsetenv(name)).To(Succeed())
			})
		}
	}
}

// assertNoEnvValueLeak fails if any non-trivial value of the process
// environment appears in text. Values that are substrings of configPath
// are skipped, because a load error legitimately names the config file.
func assertNoEnvValueLeak(text, configPath string) {
	for _, entry := range os.Environ() {
		idx := strings.Index(entry, "=")
		if idx < 0 {
			continue
		}
		value := entry[idx+1:]
		if len(value) < 12 || strings.Contains(configPath, value) {
			continue
		}
		Expect(text).NotTo(ContainSubstring(value), "leaked env value for %q", entry[:idx])
	}
}

var _ = Describe("Config env expansion", func() {
	DescribeTable(
		"expandEnvRefs",
		func(
			fixture string,
			prepare func(),
			assert func(cfg *pkgcfg.Config, err error, configPath string),
		) {
			dir, err := os.MkdirTemp("", "config-expand-")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(os.RemoveAll, dir)

			configPath := filepath.Join(dir, "config.yaml")
			Expect(os.WriteFile(configPath, []byte(fixture), 0o600)).To(Succeed())

			prepare()

			cfg, loadErr := pkgcfg.Load(context.Background(), configPath)
			assert(cfg, loadErr, configPath)
		},
		Entry("AC1: a set variable resolves in the token and the synthesized pool member",
			expandFixtureAC1,
			setAll("ROUTER_EXPAND_TOKEN", "expanded-token-value"),
			func(cfg *pkgcfg.Config, err error, _ string) {
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.Providers["anthropic"].Token).To(Equal("expanded-token-value"))
				Expect(cfg.Providers["anthropic"].Upstreams[0].Token).
					To(Equal("expanded-token-value"))
			},
		),
		Entry("AC2: $${NAME} renders a literal ${NAME} with no lookup",
			expandFixtureAC2,
			unsetAll("ROUTER_EXPAND_UNSET"),
			func(cfg *pkgcfg.Config, err error, _ string) {
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.Providers["anthropic"].Token).To(Equal("${ROUTER_EXPAND_UNSET}"))
			},
		),
		Entry("AC3: an unset variable fails the load naming the variable and leaking nothing",
			expandFixtureAC3,
			func() {
				unsetAll("ROUTER_EXPAND_MISSING")()
				Expect(os.Setenv("ROUTER_EXPAND_CANARY", "leak-canary-8f3a2b")).To(Succeed())
				DeferCleanup(func() { _ = os.Unsetenv("ROUTER_EXPAND_CANARY") })
			},
			func(_ *pkgcfg.Config, err error, configPath string) {
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("ROUTER_EXPAND_MISSING"))
				Expect(err.Error()).NotTo(ContainSubstring("leak-canary-8f3a2b"))
				assertNoEnvValueLeak(err.Error(), configPath)
			},
		),
		Entry("Failure Modes: a set-but-empty variable resolves to the empty string",
			expandFixtureEmpty,
			setAll("ROUTER_EXPAND_EMPTY", ""),
			func(cfg *pkgcfg.Config, err error, _ string) {
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.Providers["anthropic"].Token).To(Equal(""))
			},
		),
		Entry("Assumption: expansion is single-pass, an inserted value is never re-scanned",
			expandFixtureSinglePass,
			setAll(
				"ROUTER_EXPAND_INNER", "should-not-appear",
				"ROUTER_EXPAND_OUTER", "literal ${ROUTER_EXPAND_INNER}",
			),
			func(cfg *pkgcfg.Config, err error, _ string) {
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.Providers["anthropic"].Token).
					To(Equal("literal ${ROUTER_EXPAND_INNER}"))
			},
		),
		Entry("AC5: a reference resolves in every document position",
			expandFixtureAC5,
			setAll(
				"ROUTER_EXPAND_TOKEN", "ac5-token",
				"ROUTER_EXPAND_KEY", "ac5-api-key",
				"ROUTER_EXPAND_HOST", "api.anthropic.com",
				"ROUTER_EXPAND_MODEL", "qwen3.6",
			),
			func(cfg *pkgcfg.Config, err error, _ string) {
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.Providers["anthropic"].Token).To(Equal("ac5-token"))
				Expect(cfg.AllowedApiKeys).To(Equal([]string{"ac5-api-key"}))
				Expect(cfg.Providers["anthropic"].Upstreams[0].Upstream).
					To(Equal("https://api.anthropic.com"))
				Expect(cfg.Aliases["qwen-alias"]).To(Equal("qwen3.6"))
			},
		),
		Entry("AC6a: an unterminated reference fails naming its byte offset",
			expandFixtureUnclosed,
			noop,
			func(_ *pkgcfg.Config, err error, _ string) {
				Expect(err).To(HaveOccurred())
				offset := strconv.Itoa(
					strings.Index(expandFixtureUnclosed, "${ROUTER_EXPAND_UNCLOSED"),
				)
				Expect(err.Error()).To(ContainSubstring(offset))
			},
		),
		Entry("AC6b: a malformed reference name fails naming its byte offset",
			expandFixtureBadName,
			noop,
			func(_ *pkgcfg.Config, err error, _ string) {
				Expect(err).To(HaveOccurred())
				offset := strconv.Itoa(strings.Index(expandFixtureBadName, "${not-a-name"))
				Expect(err.Error()).To(ContainSubstring(offset))
			},
		),
		Entry("AC7: a bare $ and a trailing $$ pass through verbatim",
			expandFixtureDollars,
			noop,
			func(cfg *pkgcfg.Config, err error, _ string) {
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.Providers["a"].Token).To(Equal("a$b"))
				Expect(cfg.Providers["b"].Token).To(Equal("$$"))
			},
		),
		Entry("Failure Modes: a reference inside a comment is expanded and fails when unset",
			expandFixtureCommentMissing,
			unsetAll("ROUTER_EXPAND_MISSING"),
			func(_ *pkgcfg.Config, err error, _ string) {
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("ROUTER_EXPAND_MISSING"))
			},
		),
		Entry("Failure Modes: an escaped reference inside a comment loads with no error",
			expandFixtureCommentEscaped,
			unsetAll("ROUTER_EXPAND_MISSING"),
			func(_ *pkgcfg.Config, err error, _ string) {
				// The fixture carries the escape only in a comment, so there is
				// nothing to assert about the parsed config beyond the load
				// succeeding — a Token assertion here would read the zero value
				// of a field the fixture never declares.
				Expect(err).NotTo(HaveOccurred())
			},
		),
	)
})

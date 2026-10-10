// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"net/url"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	pkgcfg "github.com/bborbe/claude-code-router/pkg"
)

// mustParseURL parses raw, failing the test on a malformed URL.
func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	Expect(err).NotTo(HaveOccurred())
	return u
}

var _ = Describe("UpstreamHostKey", func() {
	DescribeTable(
		"derives the upstreamHostLimits key from an upstream URL",
		func(raw, expected string) {
			Expect(pkgcfg.UpstreamHostKey(mustParseURL(raw))).To(Equal(expected))
		},
		Entry("bare https host", "https://vllm.seibert.tools", "vllm.seibert.tools"),
		Entry("https host with a path", "https://vllm.seibert.tools/v1", "vllm.seibert.tools"),
		Entry("uppercase host is lowercased", "https://VLLM.Seibert.Tools", "vllm.seibert.tools"),
		Entry("explicit non-default port is kept", "http://127.0.0.1:8317", "127.0.0.1:8317"),
		Entry(
			"https default port is dropped",
			"https://vllm.seibert.tools:443",
			"vllm.seibert.tools",
		),
		Entry("http default port is dropped", "http://ollama.local:80", "ollama.local"),
		Entry("443 on http is not the default", "http://ollama.local:443", "ollama.local:443"),
		Entry("non-default port with a path", "https://api.example:8443/v1", "api.example:8443"),
		Entry("IPv6 literal keeps its brackets", "http://[::1]:8080", "[::1]:8080"),
	)
})

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

var _ = Describe("RedactHeadersForLog", func() {
	It("redacts Authorization value and does not expose the credential in serialised form", func() {
		h := http.Header{"Authorization": []string{"Bearer leak-canary"}}
		result := handler.RedactHeadersForLog(h)
		Expect(result["Authorization"]).To(Equal("<redacted len=18>"))
		// Canary: serialise the whole map and assert the secret is absent.
		b, err := json.Marshal(result)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).NotTo(ContainSubstring("leak-canary"))
	})

	It("redacts X-Api-Key (case-insensitive substring match on header name)", func() {
		h := http.Header{"X-Api-Key": []string{"secret123"}}
		result := handler.RedactHeadersForLog(h)
		Expect(result["X-Api-Key"]).To(Equal(fmt.Sprintf("<redacted len=%d>", len("secret123"))))
	})

	It("redacts Cookie value", func() {
		val := "session=abc; theme=dark"
		h := http.Header{"Cookie": []string{val}}
		result := handler.RedactHeadersForLog(h)
		Expect(result["Cookie"]).To(Equal(fmt.Sprintf("<redacted len=%d>", len(val))))
	})

	It("passes Content-Type through unchanged", func() {
		h := http.Header{"Content-Type": []string{"application/json"}}
		result := handler.RedactHeadersForLog(h)
		Expect(result["Content-Type"]).To(Equal("application/json"))
	})

	It("passes User-Agent through unchanged", func() {
		h := http.Header{"User-Agent": []string{"claude-cli/1.x"}}
		result := handler.RedactHeadersForLog(h)
		Expect(result["User-Agent"]).To(Equal("claude-cli/1.x"))
	})

	It("joins multi-value non-credential header values with ', '", func() {
		h := http.Header{"Accept": []string{"text/html", "application/json"}}
		result := handler.RedactHeadersForLog(h)
		Expect(result["Accept"]).To(Equal("text/html, application/json"))
	})

	It("joins multi-value credential header then redacts using the joined length", func() {
		vals := []string{"Bearer token1", "Bearer token2"}
		joined := "Bearer token1, Bearer token2"
		h := http.Header{"Authorization": vals}
		result := handler.RedactHeadersForLog(h)
		Expect(result["Authorization"]).To(Equal(fmt.Sprintf("<redacted len=%d>", len(joined))))
	})

	It("returns an empty map for a nil http.Header (no panic)", func() {
		Expect(handler.RedactHeadersForLog(nil)).To(BeEmpty())
	})

	It("matches case-insensitively on all-caps header names", func() {
		h := http.Header{"AUTHORIZATION": []string{"Bearer t"}, "X-AUTH-TOKEN": []string{"abc"}}
		result := handler.RedactHeadersForLog(h)
		// http.Header normalises canonical form on read, but a raw map can have
		// non-canonical keys. The redactor must redact regardless of casing.
		for _, v := range result {
			Expect(v).To(MatchRegexp(`^<redacted len=\d+>$`), "unexpected non-redacted: %s", v)
		}
	})

	It("redacts an empty-string value with len=0 placeholder", func() {
		h := http.Header{"Authorization": []string{""}}
		Expect(handler.RedactHeadersForLog(h)["Authorization"]).To(Equal("<redacted len=0>"))
	})

	It("does NOT inspect values for credential substrings — only header NAMES are matched", func() {
		// Document the design: a non-credential header carrying a JSON-encoded
		// secret value passes through value-unchanged. Operators who want
		// body-level redaction need the V(4) body-sample task's regex pass.
		h := http.Header{"X-Trace-Context": []string{`{"token":"sk-leak-canary-value"}`}}
		result := handler.RedactHeadersForLog(h)
		b, _ := json.Marshal(result)
		Expect(string(b)).To(ContainSubstring("sk-leak-canary-value"),
			"intentional: header-name-only redaction; document the boundary")
	})
})

var _ = Describe("RedactBearerTokensInBody", func() {
	It("redacts a Bearer token and does not expose the canary in the output", func() {
		input := []byte("Authorization: Bearer sk-leak-canary-body")
		out := handler.RedactBearerTokensInBody(input)
		Expect(string(out)).To(ContainSubstring("Bearer <redacted>"))
		Expect(string(out)).NotTo(ContainSubstring("sk-leak-canary-body"))
	})

	It("returns the input unchanged (same bytes) when no Bearer token is present", func() {
		input := []byte("plain text with no token here")
		out := handler.RedactBearerTokensInBody(input)
		Expect(out).To(Equal(input))
	})

	It("redacts multiple Bearer tokens in the same body", func() {
		input := []byte(`{"auth1":"Bearer token-one","auth2":"Bearer token-two"}`)
		out := handler.RedactBearerTokensInBody(input)
		Expect(string(out)).NotTo(ContainSubstring("token-one"))
		Expect(string(out)).NotTo(ContainSubstring("token-two"))
		// Both occurrences replaced
		Expect(bytes.Count(out, []byte("Bearer <redacted>"))).To(Equal(2))
	})

	It(
		"leaves the JSON closing bracket intact when a token is directly adjacent (no `,;\"'` between)",
		func() {
			// Regression for the bot review on PR #17: pre-fix, the regex stopped
			// at `,;"'` but NOT `}` / `]`, so a JSON value like `{"k":"Bearer x"}`
			// matched `Bearer x"}` and replacement produced malformed JSON
			// (`{"k":"Bearer <redacted>` — missing the closing `"}`).
			input := []byte(`{"auth":"Bearer sk-trailing-bracket-canary"}`)
			out := handler.RedactBearerTokensInBody(input)
			Expect(string(out)).NotTo(ContainSubstring("sk-trailing-bracket-canary"))
			Expect(
				string(out),
			).To(HaveSuffix(`"}`), "closing `\"}` must survive replacement; got: %s", out)
		},
	)

	It("leaves a `]` close-bracket intact for array-of-tokens style", func() {
		input := []byte(`{"tokens":["Bearer sk-array-canary"]}`)
		out := handler.RedactBearerTokensInBody(input)
		Expect(string(out)).NotTo(ContainSubstring("sk-array-canary"))
		Expect(string(out)).To(ContainSubstring(`"]}`),
			"closing `\"]}` must survive; got: %s", out)
	})
})

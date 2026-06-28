// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
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
})

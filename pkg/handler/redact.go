// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// bearerRE matches Bearer tokens in body content for redaction.
// Compiled once at package init — case-insensitive, covers both
// "Bearer sk-..." and "bearer sk-..." as they can appear in SSE/JSON bodies.
// bearerRE stops at whitespace or common JSON/HTTP delimiters (", , ; ')
// so that adjacent Bearer tokens in a JSON body are each redacted individually
// rather than the greedy \S+ swallowing everything up to the next space.
var bearerRE = regexp.MustCompile(`(?i)Bearer\s+[^\s,;"']+`)

// RedactBearerTokensInBody returns a copy of b with every
// "Bearer <token>" substring replaced by "Bearer <redacted>".
// The replacement is case-insensitive on the "Bearer" keyword.
// Returns b unchanged (same slice, no allocation) when no Bearer token is found.
func RedactBearerTokensInBody(b []byte) []byte {
	if !bearerRE.Match(b) {
		return b
	}
	return bearerRE.ReplaceAll(b, []byte("Bearer <redacted>"))
}

// isCredentialHeader reports whether name identifies a header whose value
// must be redacted before logging. Matching is case-insensitive and covers:
//   - Exact names: Authorization, Cookie, Set-Cookie
//   - Any name whose lower-cased form contains one of: api-key, auth-token,
//     secret, password, bearer
func isCredentialHeader(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case "authorization", "cookie", "set-cookie":
		return true
	}
	for _, sub := range []string{"api-key", "auth-token", "secret", "password", "bearer"} {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	return false
}

// RedactHeadersForLog returns a flat map of header name → value suitable for
// JSON marshaling. Multi-value headers are joined with ", ". Credential-shaped
// headers (see isCredentialHeader) have their joined value replaced with
// "<redacted len=N>" where N is the byte-length of the original joined string.
//
// This helper is designed for V(3) upstream-header logging so operators can
// see exactly what went on the wire without leaking tokens into log files.
func RedactHeadersForLog(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for name, vals := range h {
		joined := strings.Join(vals, ", ")
		if isCredentialHeader(name) {
			out[name] = fmt.Sprintf("<redacted len=%d>", len(joined))
		} else {
			out[name] = joined
		}
	}
	return out
}

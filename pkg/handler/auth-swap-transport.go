// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import "net/http"

// NewAuthSwapTransport wraps next so each outbound request has its
// Authorization header replaced with `Bearer <token>`. Used by the
// model router to swap the client's subscription OAuth bearer for a
// per-provider API token (MiniMax, Ollama, vLLM) before forwarding.
//
// If token is empty, the wrapper is a no-op and returns next.
func NewAuthSwapTransport(next http.RoundTripper, token string) http.RoundTripper {
	if token == "" {
		return next
	}
	return &authSwapTransport{next: next, header: "Bearer " + token}
}

type authSwapTransport struct {
	next   http.RoundTripper
	header string
}

func (t *authSwapTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone to avoid mutating the caller's request headers.
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", t.header)
	return t.next.RoundTrip(clone)
}

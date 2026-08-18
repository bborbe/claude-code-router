// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import "net/http"

// NewHelloHandler returns a 200 OK handler for `/api/hello`.
//
// Claude Code's HTTP client HEAD-probes `{ANTHROPIC_BASE_URL}/api/hello`
// as a connectivity check. Without this handler the probe falls through
// to NewNotFoundHandler and logs a `[404] HEAD /api/hello` line roughly
// once per second per running session, burying the real unknown-path
// signals (misconfigured base URL, `/messages` typo without `/v1`) the
// logger exists to surface.
func NewHelloHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import "net/http"

// NewRootLivenessHandler returns a 200 OK handler for `HEAD /`.
//
// Claude Code's HTTP client probes the base URL with `HEAD /` before
// dispatching its first /v1/messages request on a fresh connection.
// Without this handler the probe falls through to NewNotFoundHandler
// and logs a `[404] HEAD /` line ahead of every real request, which
// is noisy and obscures the actual `[req]` traffic.
func NewRootLivenessHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

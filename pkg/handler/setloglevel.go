// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import "net/http"

// NewSetLoglevelHandler returns a no-op handler for /setloglevel/{level}.
//
// v1 skeleton: slog level is static; nothing to switch at runtime. The
// endpoint exists so the canonical-admin-endpoints check passes and so
// future runtime log-level wiring has a known URL. Returns 200 with
// body "noop" so callers can distinguish from "endpoint missing".
func NewSetLoglevelHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("noop"))
	})
}

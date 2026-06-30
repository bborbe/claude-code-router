// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"fmt"
	"net/http"
)

// NewEnableTraceHandler returns a handler that turns per-request trace
// logging on for a bounded 5-minute window (TraceTTLDefault). The window
// auto-disables on expiry; repeated calls reset the window. Mirrors the
// /setloglevel pattern: operator-local, no auth, short plaintext body.
func NewEnableTraceHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		DefaultTraceState().Enable()
		fmt.Fprintf(w, "trace enabled\n")
	})
}

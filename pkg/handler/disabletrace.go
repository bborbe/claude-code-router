// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"fmt"
	"net/http"
)

// NewDisableTraceHandler returns a handler that turns per-request trace
// logging off immediately and cancels any in-flight TTL timer so no late
// reset can flip tracing back on. Mirrors the /setloglevel pattern.
func NewDisableTraceHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		DefaultTraceState().Disable()
		fmt.Fprintf(w, "trace disabled\n")
	})
}

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory

import (
	"net/http"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// CreateRouter wires the HTTP handlers for the router.
// v1 skeleton: only a healthz endpoint; provider routing lands in task 2.
func CreateRouter() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/healthz", handler.NewHealthzHandler())
	return mux
}

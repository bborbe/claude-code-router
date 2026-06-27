// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory

import (
	"net/http"

	libhttp "github.com/bborbe/http"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// CreateRouter wires the HTTP handlers for the router.
//
// v1 skeleton: registers /healthz, /readiness, and /gc.
// /metrics and /setloglevel/{level} are intentionally omitted — the router
// is a personal-laptop tool, not a k8s-deployed service; Prometheus and
// runtime log-level swapping add weight without an operator who consumes
// them. Add when the use case appears.
//
// Provider routing lands in task 2 ([[Allow Claude Code to Pass Through the Proxy]]).
func CreateRouter() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/healthz", handler.NewHealthzHandler())
	mux.Handle("/readiness", libhttp.NewPrintHandler("OK"))
	mux.Handle("/gc", libhttp.NewGarbageCollectorHandler())
	return mux
}

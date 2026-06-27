// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory

import (
	"net/http"

	libhttp "github.com/bborbe/http"
	librun "github.com/bborbe/run"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

// CreateServer wires the HTTP server bound to listen, with the canonical
// router (CreateRouter) as the handler. The cli package consumes this —
// all dep wiring lives here, not in cli. Returns a run.Func; call it
// with a context to start and graceful-shutdown the listener.
func CreateServer(listen string) librun.Func {
	return libhttp.NewServer(listen, CreateRouter())
}

// CreateRouter wires the HTTP handlers for the router.
//
// Registers all five canonical admin endpoints — /healthz, /readiness,
// /metrics, /setloglevel/{level}, /gc — per go-http-service guide.
// /metrics and /setloglevel are stubbed: the router is a personal-laptop
// tool with no Prometheus scraper and a static slog level today; the
// endpoints exist so future ops tooling (or the rule check) finds them.
//
// Provider routing lands in task 2 ([[Allow Claude Code to Pass Through the Proxy]]).
func CreateRouter() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/healthz", handler.NewHealthzHandler())
	mux.Handle("/readiness", libhttp.NewPrintHandler("OK"))
	mux.Handle("/metrics", libhttp.NewPrintHandler("# metrics not enabled in v1 skeleton\n"))
	mux.Handle("/setloglevel/", handler.NewSetLoglevelHandler())
	mux.Handle("/gc", libhttp.NewGarbageCollectorHandler())
	return handler.NewLoggingHandler(mux)
}

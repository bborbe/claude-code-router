// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"net/http"

	"github.com/golang/glog"
)

// NewNotFoundHandler returns a 404 handler that logs the unknown path
// before responding. Registered at `/` in the factory's mux so it
// catches everything not matched by a more specific route (`/v1/`,
// `/healthz`, `/readiness`, `/metrics`, `/setloglevel/`, `/gc`).
//
// Logged at glog V(1) — same level as `[req]` — so unknown-path probes
// surface in the operator's default log alongside real traffic. Useful
// for catching misconfigured clients (wrong base URL, typo in
// `/v1/messages`) and any probing of the listener.
func NewNotFoundHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		glog.V(1).Infof("[404] %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})
}

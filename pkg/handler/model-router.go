// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"path"

	"github.com/golang/glog"
)

// ModelRoute pairs a glob pattern (filepath.Match syntax) with the
// handler to invoke when an incoming request's `model` field matches.
type ModelRoute struct {
	Pattern string
	Handler http.Handler
}

// NewModelRouter returns an HTTP handler that body-parses each request's
// JSON `model` field and dispatches to the first matching ModelRoute.
// Unmatched models (and non-JSON / no-model requests) fall through to
// defaultHandler. The body is fully read and replayed for the downstream
// handler — fine for /v1/messages JSON payloads (typically <100 KB);
// not suitable for unbounded upload bodies.
func NewModelRouter(routes []ModelRoute, defaultHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			glog.Errorf("[model-router] read body failed: %v", err)
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))

		model := extractModel(body)
		for _, route := range routes {
			ok, _ := path.Match(route.Pattern, model)
			if ok {
				glog.V(1).Infof("[route] model=%q matched %q", model, route.Pattern)
				route.Handler.ServeHTTP(w, r)
				return
			}
		}
		glog.V(1).Infof("[route] model=%q no match, using default", model)
		defaultHandler.ServeHTTP(w, r)
	})
}

// extractModel returns the value of the top-level `model` field from a
// JSON body, or empty string if the body isn't JSON / has no model.
// Best-effort: errors are silently treated as "no model present".
func extractModel(body []byte) string {
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	return req.Model
}

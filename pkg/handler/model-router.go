// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
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
// JSON `model` field, resolves it through the aliases map (single-hop,
// case-sensitive exact match), then dispatches to the first matching
// ModelRoute. Unmatched models (and non-JSON / no-model requests) fall
// through to defaultHandler. The body is fully read and replayed for
// the downstream handler — fine for /v1/messages JSON payloads
// (typically <100 KB); not suitable for unbounded upload bodies.
//
// aliases may be nil or empty — both mean "no alias rewriting", same
// as today's behavior. On a hit, the body's top-level .model field is
// re-marshaled to the resolved value before route dispatch, so the
// upstream sees the full model name. A single glog.V(1) line is
// emitted on hit: "[alias] <short> -> <resolved>".
func NewModelRouter(
	routes []ModelRoute,
	defaultHandler http.Handler,
	aliases map[string]string,
) http.Handler {
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

		if resolved, ok := aliases[model]; ok && model != "" {
			rewritten, rerr := rewriteModelField(body, resolved)
			if rerr != nil {
				glog.Errorf("[alias] rewrite failed for %q -> %q: %v", model, resolved, rerr)
				http.Error(w, "alias rewrite failed", http.StatusInternalServerError)
				return
			}
			glog.V(1).Infof("[alias] %s -> %s", model, resolved)
			body = rewritten
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			model = resolved
		}

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

// rewriteModelField parses body as a JSON object, sets the top-level
// "model" field to resolved, and returns the re-marshaled bytes. All
// other top-level fields are preserved (their values are kept as
// json.RawMessage to avoid lossy re-encoding of nested structures and
// numbers). Returns an error if body is not a JSON object.
//
// rewriteModelField is best-effort; a JSON body that extractModel accepted
// will always re-marshal. The error return is defensive for unforeseen
// input shapes.
func rewriteModelField(body []byte, resolved string) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("parse body as JSON object: %w", err)
	}
	resolvedJSON, err := json.Marshal(resolved)
	if err != nil {
		// Should never happen — string marshal is infallible.
		return nil, fmt.Errorf("marshal resolved model: %w", err)
	}
	obj["model"] = resolvedJSON
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("re-marshal body: %w", err)
	}
	return out, nil
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

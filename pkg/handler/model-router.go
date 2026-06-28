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
	"time"

	liblog "github.com/bborbe/log"
	"github.com/golang/glog"
)

// ModelRoute pairs a glob pattern (filepath.Match syntax) with the
// provider name + handler to invoke when an incoming request's `model`
// field matches. ProviderName is what appears in the structured log
// (`provider=minimax`) and is the same key as in the YAML config's
// `providers:` map.
type ModelRoute struct {
	Pattern      string
	ProviderName string
	Handler      http.Handler
}

// NewModelRouter returns an HTTP handler that body-parses each request's
// JSON `model` field, resolves it through the aliases map (single-hop,
// case-sensitive exact match), then dispatches to the first matching
// ModelRoute. Unmatched models (and non-JSON / no-model requests) fall
// through to defaultHandler (logged as provider=defaultProviderName).
// The body is fully read and replayed for the downstream handler —
// fine for /v1/messages JSON payloads (typically <100 KB); not suitable
// for unbounded upload bodies.
//
// aliases may be nil or empty — both mean "no alias rewriting". On a
// hit, the body's top-level .model field is re-marshaled to the resolved
// value before route dispatch, so the upstream sees the full model name.
//
// One structured `[req]` log line per request at V(1):
//
//	[req] POST /v1/messages model=m3 alias=MiniMax-M3-highspeed provider=minimax status=200 latency=842ms
//
// Non-200 responses are ALWAYS logged; 200 responses are gated by the
// sampler. `log.DefaultSamplerFactory` gives the canonical OR-combo:
// at most once per 10s, OR unconditionally when glog `-v` ≥ 4. This
// keeps the steady-state log readable while preserving every error
// event and giving full visibility once the operator bumps verbosity
// via `/setloglevel/4`.
//
// At V(2), alias resolution and route match get their own `[alias]` /
// `[route]` detail lines (independent of the sampler — V(2) detail is
// already operator-opt-in, additional gating buys nothing).
func NewModelRouter(
	routes []ModelRoute,
	defaultProviderName string,
	defaultHandler http.Handler,
	aliases map[string]string,
	sampler liblog.Sampler,
	metrics *Metrics,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		glog.V(4).Infof("[inbound.start] %s %s", r.Method, r.URL.Path)
		rec := &statusRecorder{ResponseWriter: w}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			glog.Errorf("[model-router] read body failed: %v", err)
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))

		origModel := extractModel(body)
		model := origModel
		var aliasResolved string

		if resolved, ok := aliases[model]; ok && model != "" {
			rewritten, rerr := rewriteModelField(body, resolved)
			if rerr != nil {
				glog.Errorf("[alias] rewrite failed for %q -> %q: %v", model, resolved, rerr)
				http.Error(w, "alias rewrite failed", http.StatusInternalServerError)
				return
			}
			glog.V(2).Infof("[alias] %s -> %s", model, resolved)
			metrics.ObserveAliasResolution(origModel, resolved)
			body = rewritten
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			aliasResolved = resolved
			model = resolved
		}

		providerName := defaultProviderName
		target := defaultHandler
		for _, route := range routes {
			ok, _ := path.Match(route.Pattern, model)
			if ok {
				providerName = route.ProviderName
				target = route.Handler
				glog.V(2).
					Infof("[route] model=%q matched %q -> provider=%s", model, route.Pattern, providerName)
				break
			}
		}

		target.ServeHTTP(rec, r)

		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		// e2e wall-time: includes body read + JSON parse + alias rewrite
		// + upstream round-trip. That's the operator-relevant number
		// ("how long did this `/model X` turn take?"), not the upstream-
		// only segment.
		latency := time.Since(start).Round(time.Millisecond)

		metrics.ObserveRequest(providerName, origModel, status, latency.Seconds())
		logReq(r, status, latency, origModel, aliasResolved, providerName, sampler)
	})
}

// logReq emits the structured `[req]` line. Always logs non-200 (errors
// are signal); samples 200s to keep the steady-state log readable.
// sampler.IsSample() is non-pure (time-based sampler advances its window)
// — only consulted on the success path so the 10s window is paced by 200s.
func logReq(
	r *http.Request,
	status int,
	latency time.Duration,
	origModel, aliasResolved, providerName string,
	sampler liblog.Sampler,
) {
	if status == http.StatusOK && !sampler.IsSample() {
		return
	}
	if aliasResolved != "" {
		glog.V(1).Infof(
			"[req] %s %s model=%s alias=%s provider=%s status=%d latency=%s",
			r.Method, r.URL.Path, origModel, aliasResolved, providerName, status, latency,
		)
		return
	}
	glog.V(1).Infof(
		"[req] %s %s model=%s provider=%s status=%d latency=%s",
		r.Method, r.URL.Path, origModel, providerName, status, latency,
	)
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

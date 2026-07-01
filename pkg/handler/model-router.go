// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"time"

	bberrors "github.com/bborbe/errors"
	liblog "github.com/bborbe/log"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// MaxRequestBodyBytes caps inbound /v1/* request bodies at 32 MB to
// match the Anthropic API ceiling. Long Claude Code sessions (full
// conversation history + tool definitions + sub-agent results) routinely
// exceed 1 MB, so a tighter cap surfaces as confusing 413s that read as
// upstream errors. 32 MB still bounds memory exhaustion from an
// accidental multi-GB upload via io.ReadAll. On overflow, the wrapped
// body returns *http.MaxBytesError; the router responds with HTTP 413
// Request Entity Too Large + a generic body (no internal state leaked).
const MaxRequestBodyBytes = 32 << 20 // 32 MB

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
//
// match route → dispatch → observe metrics → emit logs. Each step's
// branching is local and reads sequentially; extracting any of it into a
// helper buys nothing (the prior `logReq` extraction was a naive line-count
// fix per architecture audit 2026-06-28 — inlined back). If a second
// log-event shape ever needs the same data, introduce a `requestLogger`
// struct holding `sampler` + `metrics` then.
//
//nolint:gocognit,funlen // single-pass request flow: read body → resolve alias →
func NewModelRouter(
	routes []ModelRoute,
	defaultProviderName string,
	defaultHandler http.Handler,
	aliases map[string]string,
	sampler liblog.Sampler,
	metrics *Metrics,
	currentDateTime libtime.CurrentDateTimeGetter,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := currentDateTime.Now().Time()
		glog.V(4).Infof("[inbound.start] %s %s", r.Method, r.URL.Path)
		rec := &statusRecorder{ResponseWriter: w}
		ur := newUsageRecorder(rec)

		r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBodyBytes)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				glog.Warningf(
					"[model-router] request body too large: limit=%d bytes",
					maxBytesErr.Limit,
				)
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				latency := currentDateTime.Now().Time().Sub(start).Round(time.Millisecond)
				metrics.ObserveRequest(
					UnknownModelLabel,
					UnknownModelLabel,
					http.StatusRequestEntityTooLarge,
					latency.Seconds(),
					true,
				)
				return
			}
			glog.Errorf("[model-router] read body failed: %v", err)
			http.Error(w, "read body failed", http.StatusBadRequest)
			latency := currentDateTime.Now().Time().Sub(start).Round(time.Millisecond)
			metrics.ObserveRequest(
				UnknownModelLabel,
				UnknownModelLabel,
				http.StatusBadRequest,
				latency.Seconds(),
				true,
			)
			return
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))

		origModel := extractModel(body)
		model := origModel
		var aliasResolved string

		if resolved, ok := aliases[model]; ok && model != "" {
			rewritten, rerr := rewriteModelField(r.Context(), body, resolved)
			if rerr != nil {
				glog.Errorf("[alias] rewrite failed for %q -> %q: %v", model, resolved, rerr)
				http.Error(w, "alias rewrite failed", http.StatusInternalServerError)
				latency := currentDateTime.Now().Time().Sub(start).Round(time.Millisecond)
				// origModel is populated here (extractModel already ran), so pass it through
				// the sentinel chain — operators can see WHICH model triggered the rewrite
				// failure. The body-too-large / body-read-failed paths above run BEFORE
				// extractModel and use UnknownModelLabel for both provider and model because
				// neither is known yet — the asymmetry is deliberate, not a style slip.
				modelLabel := resolveModelLabel("", origModel)
				metrics.ObserveRequest(
					UnknownModelLabel,
					modelLabel,
					http.StatusInternalServerError,
					latency.Seconds(),
					true,
				)
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

		target.ServeHTTP(ur, r)

		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		// e2e wall-time: includes body read + JSON parse + alias rewrite
		// + upstream round-trip. That's the operator-relevant number
		// ("how long did this `/model X` turn take?"), not the upstream-
		// only segment.
		latency := currentDateTime.Now().Time().Sub(start).Round(time.Millisecond)

		modelLabel := resolveModelLabel(model, origModel)
		metrics.ObserveRequest(providerName, modelLabel, status, latency.Seconds(), false)
		glog.V(4).
			Infof("[inbound.end] %s %s status=%d latency=%s", r.Method, r.URL.Path, status, latency)

		// Extract usage and record tokens on every 2xx BEFORE the sampler
		// gate so token metrics are counted even for ~90% suppressed 200s.
		// The [req] log line remains sampler-gated (see below).
		usage := noUsage
		if status == http.StatusOK {
			usage = ExtractUsage(
				ur.Tail(),
				rec.Header().Get("Content-Type"),
				rec.Header().Get("Content-Encoding"),
			)
			recordTokensFromUsage(metrics, providerName, modelLabel, usage)
		}

		// Always log non-200 (errors are signal); sample 200s to keep the
		// steady-state log readable. sampler.IsSample() is non-pure (time-
		// based sampler advances its window) — only consult it on the 200
		// path so the 10s window is paced by real success density.
		if status == http.StatusOK && !sampler.IsSample() {
			return
		}
		in, out := usage.logLineValue()
		if aliasResolved != "" {
			glog.V(1).Infof(
				"[req] %s %s model=%s alias=%s provider=%s status=%d latency=%s in=%s out=%s",
				r.Method, r.URL.Path, origModel, aliasResolved, providerName, status, latency, in, out,
			)
			return
		}
		glog.V(1).Infof(
			"[req] %s %s model=%s provider=%s status=%d latency=%s in=%s out=%s",
			r.Method, r.URL.Path, origModel, providerName, status, latency, in, out,
		)
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
func rewriteModelField(ctx context.Context, body []byte, resolved string) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, bberrors.Wrapf(ctx, err, "parse body as JSON object")
	}
	resolvedJSON, err := json.Marshal(resolved)
	if err != nil {
		return nil, bberrors.Wrapf(ctx, err, "marshal resolved model")
	}
	obj["model"] = resolvedJSON
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, bberrors.Wrapf(ctx, err, "re-marshal body")
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

// resolveModelLabel picks the label value to emit into the
// ccrouter_requests_total and ccrouter_tokens_total counters for the
// model dimension. Resolution order (spec 007 Desired Behavior 5):
//
//  1. resolvedModel (post-alias resolved model, or the pre-alias model
//     when no alias hit fired) — the string the upstream actually saw.
//  2. origModel (pre-alias, from extractModel) — used when the alias
//     branch nulled the resolved value or the resolved is otherwise
//     empty.
//  3. UnknownModelLabel ("_unknown_") — the sentinel returned when
//     both are empty (probe traffic, misshapen body, router-side
//     early-return before body parse).
//
// Never returns the empty string — the goal is that no ccrouter_*
// series ever carries model="" (spec 007 Goal).
func resolveModelLabel(resolvedModel, origModel string) string {
	if resolvedModel != "" {
		return resolvedModel
	}
	if origModel != "" {
		return origModel
	}
	return UnknownModelLabel
}

// recordTokensFromUsage parses the string-shaped input/output token
// counts produced by ExtractUsage and increments the ccrouter_tokens_total
// counter twice — once for direction=input, once for direction=output.
//
// Drop rules (spec 007 Failure Modes):
//   - Empty string or "-" sentinel   -> that direction is not counted;
//     the other direction (if valid)
//     is counted independently.
//   - Non-numeric string (schema drift) -> parse fails, that direction
//     is dropped, glog.V(2) diagnostic.
//   - Zero or negative count         -> absorbed by ObserveTokens'
//     zero-drop rule (no series
//     created).
//
// Token counting is best-effort observability: a parse failure never
// affects the request-serving path.
func recordTokensFromUsage(metrics *Metrics, provider, model string, usage TokenUsage) {
	recordTokenDirection(metrics, provider, model, "input", usage.Input)
	recordTokenDirection(metrics, provider, model, "output", usage.Output)
}

func recordTokenDirection(metrics *Metrics, provider, model, direction, raw string) {
	if raw == "" || raw == "-" {
		return
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		glog.V(2).Infof("[tokens] parse %s=%q failed: %v", direction, raw, err)
		return
	}
	metrics.ObserveTokens(provider, model, direction, n)
}

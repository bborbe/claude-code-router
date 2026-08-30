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
	"reflect"
	"strconv"
	"strings"
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
	// RequiresLeadingSystem carries this route's provider's
	// `requiresLeadingSystem` config globs. When the resolved model
	// name matches one of them, the router lifts non-leading
	// system-role messages into the top-level system block before
	// dispatching. Nil or empty (the default for every route built
	// from a config that omits the field) means: never transform.
	RequiresLeadingSystem []string
	// AllowedApiKeys carries this route's provider's `allowedApiKeys`
	// config list. When the request's presented x-api-key (from the auth
	// middleware's context) is in one of these lists, the router dispatches
	// to that provider directly — the key is an explicit override that wins
	// over model-glob matching. Because Config.Validate rejects a key
	// claimed by more than one provider, at most one route ever matches a
	// presented key. Nil or empty (the default for every route built from a
	// config that omits the field) means: this provider claims no keys, so
	// it is only reachable via glob routing.
	AllowedApiKeys []string `display:"length"`
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
//	[req] POST /v1/messages model=m3 alias=MiniMax-M3-highspeed provider=minimax/0 status=200 latency=842ms
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
func NewModelRouter(
	routes []ModelRoute,
	defaultProviderName string,
	defaultHandler http.Handler,
	aliases map[string]string,
	sampler liblog.Sampler,
	metrics *Metrics,
	currentDateTime libtime.CurrentDateTimeGetter,
) http.Handler {
	return newModelRouter(
		routes,
		defaultProviderName,
		defaultHandler,
		aliases,
		nil,
		sampler,
		metrics,
		currentDateTime,
	)
}

// NewModelRouterWithPools is NewModelRouter with an additional model-pool
// pre-step: a request whose `model` field names a configured pool
// resolves to one member before alias/key/glob routing, rewrites the
// body's model field to that member's concrete model, and dispatches
// through the member's handler — the glob walk is never reached for a
// pool name. modelPools may be nil or empty — both mean "no pool
// resolution", identical to NewModelRouter.
func NewModelRouterWithPools(
	routes []ModelRoute,
	defaultProviderName string,
	defaultHandler http.Handler,
	aliases map[string]string,
	modelPools map[string]*ModelPool,
	sampler liblog.Sampler,
	metrics *Metrics,
	currentDateTime libtime.CurrentDateTimeGetter,
) http.Handler {
	return newModelRouter(
		routes,
		defaultProviderName,
		defaultHandler,
		aliases,
		modelPools,
		sampler,
		metrics,
		currentDateTime,
	)
}

//nolint:gocognit,funlen,maintidx,gocyclo // single-pass request flow: read body → resolve alias →
func newModelRouter(
	routes []ModelRoute,
	defaultProviderName string,
	defaultHandler http.Handler,
	aliases map[string]string,
	modelPools map[string]*ModelPool,
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

		providerName := defaultProviderName
		target := defaultHandler
		// Seed from the default provider's own globs: a request matching no
		// route still reaches defaultHandler, and that provider's chat-template
		// restriction applies to it just the same. Every route built for a
		// provider carries that provider's list, so the first one wins.
		var requiresLeadingSystem []string
		for _, route := range routes {
			if route.ProviderName == defaultProviderName {
				requiresLeadingSystem = route.RequiresLeadingSystem
				break
			}
		}

		// Model-pool pre-step: a request whose model names a configured pool
		// resolves to one member BEFORE alias/key/glob routing — pool names do
		// not interact with aliases, keys, or the glob walk (spec 013 Desired
		// Behavior 5). The body's model field is rewritten to the member's
		// concrete model, which is what the upstream sees; the pool name itself
		// is matched verbatim, so a pool name must not carry the [1m] suffix
		// (the strip below runs after pool lookup).
		matchedByPool := false
		//nolint:nestif // single pool-resolve branch with its own rewrite-failure path
		if modelPools != nil {
			if pool, ok := modelPools[model]; ok {
				member := pool.Resolve(r.Context(), SessionIDFromContext(r.Context()))
				rewritten, rerr := rewriteModelField(r.Context(), body, member.Model)
				if rerr != nil {
					glog.Errorf("[pool] rewrite failed for %q -> %q: %v", model, member.Model, rerr)
					http.Error(w, "pool rewrite failed", http.StatusInternalServerError)
					latency := currentDateTime.Now().Time().Sub(start).Round(time.Millisecond)
					metrics.ObserveRequest(
						UnknownModelLabel,
						UnknownModelLabel,
						http.StatusInternalServerError,
						latency.Seconds(),
						true,
					)
					return
				}
				body = rewritten
				r.Body = io.NopCloser(bytes.NewReader(body))
				r.ContentLength = int64(len(body))
				model = member.Model
				providerName = member.Provider
				target = member.Handler
				for _, route := range routes {
					if route.ProviderName == member.Provider {
						requiresLeadingSystem = route.RequiresLeadingSystem
						break
					}
				}
				glog.V(2).
					Infof("[route] model=%s -> provider=%s model=%s", origModel, member.Provider, member.Model)
				matchedByPool = true
			}
		}

		if !matchedByPool {
			if resolved, ok := aliases[model]; ok && model != "" {
				rewritten, rerr := rewriteModelField(r.Context(), body, resolved)
				if rerr != nil {
					glog.Errorf("[alias] rewrite failed for %q -> %q: %v", model, resolved, rerr)
					http.Error(w, "alias rewrite failed", http.StatusInternalServerError)
					latency := currentDateTime.Now().Time().Sub(start).Round(time.Millisecond)
					metrics.ObserveRequest(
						UnknownModelLabel,
						UnknownModelLabel,
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
		}

		// Claude Code annotates model names with [1m] to mark 1M-token
		// context windows (e.g. deepseek-v4-pro-max[1m]). The upstream LLM
		// does not recognize the suffixed name, causing 4xx errors. Strip a
		// trailing [1m] AFTER alias resolution so both sources are covered:
		// a client-sent suffix (deepseek-v4-pro-max[1m]) and an alias whose
		// config value itself carries the suffix (deepseek-pro ->
		// deepseek-v4-pro[1m]). Rewrite the body so the upstream and the
		// metrics label both see the canonical name.
		if suffix := "[1m]"; strings.HasSuffix(model, suffix) {
			cleaned := strings.TrimSuffix(model, suffix)
			rewritten, rerr := rewriteModelField(r.Context(), body, cleaned)
			if rerr != nil {
				glog.Errorf("[1m-strip] rewrite failed for %q: %v", model, rerr)
				http.Error(w, "model name normalization failed", http.StatusInternalServerError)
				latency := currentDateTime.Now().Time().Sub(start).Round(time.Millisecond)
				metrics.ObserveRequest(
					UnknownModelLabel,
					UnknownModelLabel,
					http.StatusInternalServerError,
					latency.Seconds(),
					true,
				)
				return
			}
			glog.V(2).Infof("[1m-strip] %s -> %s", model, cleaned)
			body = rewritten
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			model = cleaned
		}

		// Key routing runs BEFORE the glob walk: a presented x-api-key that
		// some provider claims pins the dispatch to that provider, and the
		// glob walk is skipped wholesale. Plain string membership is correct
		// here — the auth middleware already validated the key in constant
		// time, so no timing boundary remains (AC 10 is scoped to
		// auth-middleware.go). The key itself is never logged or written to
		// the request body; the `[route]` line names the provider only. Both
		// branches are skipped wholesale for a pool-resolved request — the
		// model field explicitly named a pool, so the pool pre-step wins.
		// A provider whose pool has no eligible member (its members' time
		// windows all exclude "now", spec 014) is skipped by key routing and
		// the glob walk alike: eligibility is never an error or a 429, the
		// request falls through to the next matching provider or
		// default_provider, logged with `window=closed`.
		//nolint:nestif // key-routing scan + glob walk, both skipped for pool-resolved requests
		if !matchedByPool {
			var closedProvider string
			matchedByKey := false
			if presentedKey := PresentedApiKeyFromContext(r.Context()); presentedKey != "" {
				for _, route := range routes {
					if containsString(route.AllowedApiKeys, presentedKey) {
						if !windowEligible(r.Context(), route.Handler) {
							// The key-pinned provider has no eligible member: fall
							// through to the glob walk / default (spec 014 DB 4).
							closedProvider = route.ProviderName
							break
						}
						providerName = route.ProviderName
						target = route.Handler
						requiresLeadingSystem = route.RequiresLeadingSystem
						glog.V(2).Infof("[route] key matched provider=%s", providerName)
						matchedByKey = true
						break
					}
				}
			}
			for _, route := range routes {
				if matchedByKey {
					break
				}
				ok, _ := path.Match(route.Pattern, model)
				if !ok {
					continue
				}
				if !windowEligible(r.Context(), route.Handler) {
					if closedProvider == "" {
						closedProvider = route.ProviderName
					}
					continue
				}
				providerName = route.ProviderName
				target = route.Handler
				requiresLeadingSystem = route.RequiresLeadingSystem
				glog.V(2).
					Infof("[route] model=%q matched %q -> provider=%s", model, route.Pattern, providerName)
				if closedProvider != "" && closedProvider != providerName {
					glog.V(2).
						Infof("[route] provider=%s window=closed -> %s", closedProvider, providerName)
				}
				break
			}
			if sameHandler(target, defaultHandler) && closedProvider != "" {
				if closedProvider != defaultProviderName {
					glog.V(2).
						Infof("[route] provider=%s window=closed -> %s", closedProvider, defaultProviderName)
				} else {
					// Last-resort: the default provider itself is closed — serve it
					// anyway, never an error/429 (spec: closed window is eligibility,
					// never a failure). The line is the operator's signal.
					glog.V(2).Infof("[route] provider=%s window=closed", closedProvider)
				}
			}
		}

		// Models whose chat template rejects a system message that is not
		// first (spec 008): lift the misplaced entries into the top-level
		// system block. Runs AFTER alias resolution and [1m] stripping so
		// the pattern is matched against the model name the upstream sees.
		// Any body shape the transform cannot interpret is forwarded
		// unchanged with one warning — never a router-generated 5xx.
		if matchesAnyPattern(requiresLeadingSystem, model) {
			lifted, moved, lerr := liftSystemMessages(r.Context(), body)
			switch {
			case lerr != nil:
				glog.Warningf("[system-lift] skipped model=%s: %v", model, lerr)
			case moved > 0:
				glog.V(2).Infof("[system-lift] model=%s moved=%d", model, moved)
				body = lifted
				r.Body = io.NopCloser(bytes.NewReader(body))
				r.ContentLength = int64(len(body))
			}
		}

		// Per-request upstream-member-index slot (spec 016): the router
		// injects a shared slot into the request context before dispatch;
		// an upstream pool handler in the dispatch path writes the
		// zero-based index of the member it selected, and the router reads
		// it back below for the [req] line. No pool handler in the path
		// (default-fallback, test stubs) leaves the slot at its zero value
		// -> a uniform `/0` suffix, never conditionally omitted.
		slot := &upstreamIndexSlot{}
		r = r.WithContext(ContextWithUpstreamIndex(r.Context(), slot))
		target.ServeHTTP(ur, r)
		memberIndex := slot.index

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
				"[req] %s %s model=%s alias=%s provider=%s/%d status=%d latency=%s in=%s out=%s",
				r.Method, r.URL.Path, origModel, aliasResolved, providerName, memberIndex, status, latency, in, out,
			)
			return
		}
		glog.V(1).Infof(
			"[req] %s %s model=%s provider=%s/%d status=%d latency=%s in=%s out=%s",
			r.Method, r.URL.Path, origModel, providerName, memberIndex, status, latency, in, out,
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
	recordCacheTokenDirection(metrics, provider, model, "read", usage.CacheRead)
	recordCacheTokenDirection(metrics, provider, model, "creation", usage.CacheCreation)
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

// recordCacheTokenDirection mirrors recordTokenDirection but feeds the
// separate ccrouter_cache_tokens_total counter (directions read|creation),
// keeping cache tokens out of ccrouter_tokens_total's input|output enum.
func recordCacheTokenDirection(metrics *Metrics, provider, model, direction, raw string) {
	if raw == "" || raw == "-" {
		return
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		glog.V(2).Infof("[tokens] parse %s=%q failed: %v", direction, raw, err)
		return
	}
	metrics.ObserveCacheTokens(provider, model, direction, n)
}

// containsString reports whether s is in list. It is a plain linear scan
// over the tiny per-provider key list; plain string comparison is correct
// because the auth gate already validated the presented key in constant
// time, so no timing boundary remains.
func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// sameHandler reports whether two http.Handler values refer to the same
// handler. Handlers are commonly funcs (http.HandlerFunc, an uncomparable
// type), so a plain == would panic; compare the underlying value's
// pointer instead. Pointers and funcs compare by pointer identity, any
// other kind (comparable by construction) by interface equality — the
// semantics of == without the panic.
func sameHandler(a, b http.Handler) bool {
	if a == nil || b == nil {
		return a == b
	}
	av := reflect.ValueOf(a)
	bv := reflect.ValueOf(b)
	if av.Type() != bv.Type() {
		return false
	}
	switch av.Kind() {
	case reflect.Func,
		reflect.Chan,
		reflect.Map,
		reflect.Slice,
		reflect.Pointer,
		reflect.UnsafePointer:
		return av.Pointer() == bv.Pointer()
	default:
		return av.Interface() == bv.Interface()
	}
}

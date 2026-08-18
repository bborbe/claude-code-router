// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
	liblog "github.com/bborbe/log"
	librun "github.com/bborbe/run"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/handler"
	"github.com/bborbe/claude-code-router/pkg/reloader"
)

// RouterOptionFunc configures CreateRouterFromConfig beyond the parsed Config.
// Options are test seams (e.g. an isolated Prometheus registry) that do
// not belong on the YAML-deserialized Config struct.
type RouterOptionFunc func(*routerOptions)

type routerOptions struct {
	metricsRegisterer prometheus.Registerer
}

// WithMetricsRegisterer overrides the Prometheus registerer used for
// ccrouter_* metrics. Defaults to prometheus.DefaultRegisterer. Tests pass
// an isolated registry to avoid racing on the process-global default.
func WithMetricsRegisterer(reg prometheus.Registerer) RouterOptionFunc {
	return func(o *routerOptions) {
		o.metricsRegisterer = reg
	}
}

// CreateServer loads the config at configPath, wires the model router
// + per-provider proxies, and returns a run.Func that starts the HTTP
// listener with graceful shutdown on ctx cancel.
func CreateServer(ctx context.Context, listen, configPath string) (librun.Func, error) {
	if os.Getenv("ROUTER_AUTH_KEY") != "" {
		return nil, errors.New(
			ctx,
			"ROUTER_AUTH_KEY is no longer supported: remove it from the environment and configure allowedApiKeys in the config instead",
		)
	}
	cfg, err := pkg.Load(ctx, configPath)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "load config")
	}
	router, err := CreateRouterFromConfig(ctx, cfg)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "build router")
	}
	reloader := reloader.NewReloader(
		configPath,
		router,
		func(ctx context.Context, cfg *pkg.Config) (http.Handler, error) {
			// On SIGHUP reload, CreateRouterFromConfig re-runs metrics.Register. At
			// startup (the direct call above) collectors registered on DefaultRegisterer;
			// a second Register on the same registerer returns AlreadyRegisteredError,
			// which master made fatal. Re-registering on a throwaway registry avoids
			// the duplicate error so the handler tree rebuilds against the new config.
			// Trade-off: the reload's fresh counters are not scraped by /metrics (which
			// is wired to DefaultRegisterer), so ccrouter_* metrics go stale after a
			// reload until a full process restart. Acceptable for a local one-operator
			// proxy where reloads are rare config edits; routing itself is unaffected.
			return CreateRouterFromConfig(ctx, cfg, WithMetricsRegisterer(prometheus.NewRegistry()))
		},
	)
	reloader.SeedConfig(cfg)
	go reloader.RunSighupLoop(ctx)
	return libhttp.NewServer(listen, reloader, streamingServerTimeouts), nil
}

// traceDir returns the fixed trace directory path.
// Expand ~ via os.UserHomeDir to handle the tilde in ~/.claude-code-router/trace/.
func traceDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		// Fallback: trace writes go to /tmp instead of ~. Warn so the
		// operator knows where files actually landed (their
		// `rm ~/.claude-code-router/trace/*.json` cleanup would miss them).
		fallback := filepath.Join(os.TempDir(), ".claude-code-router", "trace")
		glog.Warningf("home dir lookup failed, trace files will land in %s: %v", fallback, err)
		return fallback
	}
	return filepath.Join(home, ".claude-code-router", "trace")
}

// streamingServerTimeouts raises libhttp.NewServer's default 30s
// ReadTimeout + 30s WriteTimeout to values that fit LLM-proxy streaming
// while still bounding stuck connections — full chain:
//
//	claude → router (POST body)  — ReadTimeout 5min  (large /compact context, localhost transfer in <5s normally)
//	router → api → router        — transport ResponseHeaderTimeout 5min (TTFB)
//	router → claude (SSE stream) — WriteTimeout 10min (worst observed body stream ~1min; 10min is generous 10x headroom)
//
// Defaults killed `/compact` two ways: ReadTimeout=30s cut off a large
// session-context upload mid-flight; WriteTimeout=30s killed any SSE
// response that streamed >30s (most /compact bodies). Setting these
// to 0 (unlimited) would risk a wedged Anthropic outage piling up
// goroutines forever as claude-code's SDK retries — so we cap at
// generous-but-finite values that surface real wedges as clean
// timeouts the operator can investigate.
//
// ReadHeaderTimeout (10s) and IdleTimeout (60s) stay at defaults —
// those cap pre-body header reads and idle-keepalive recycling, both
// of which are safe to bound at single-digit seconds even for streaming.
func streamingServerTimeouts(o *libhttp.ServerOptions) {
	o.ReadTimeout = 5 * time.Minute
	o.WriteTimeout = 10 * time.Minute
}

// providerKeys returns the provider names in YAML declaration order when the
// config was loaded from YAML (Config.ProviderOrder populated by
// UnmarshalYAML), else sorted for determinism (programmatically built configs
// and tests). The router's "first glob match wins" routing depends on this
// order when two providers share a model glob — the first-declared provider
// owns the keyless traffic, a later one is reached only by an allowedApiKeys
// pin (spec 010).
func providerKeys(cfg *pkg.Config) []string {
	if len(cfg.ProviderOrder) > 0 {
		return cfg.ProviderOrder
	}
	keys := make([]string, 0, len(cfg.Providers))
	for k := range cfg.Providers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// defaultMaxConcurrentWaitSeconds is the queue timeout applied when a
// capped provider's maxConcurrentWaitSeconds is absent, 0, or negative
// (spec DB 5).
const defaultMaxConcurrentWaitSeconds = 30

// CreateRouterFromConfig builds the HTTP handler tree from a parsed
// config: per-provider reverse-proxies with token-swap transports, a
// model-name dispatcher on /v1/, and the canonical admin endpoints
// (/healthz, /readiness, /metrics, /setloglevel/, /gc). The model
// router emits its own structured one-line log per request at V(1)
// (`[req] METHOD path model=... provider=... status=... latency=...`),
// so no outer logging wrapper is needed — admin endpoints stay quiet.
//
//nolint:funlen // per-provider wiring with per-loop ctx cancellation checks
func CreateRouterFromConfig(
	ctx context.Context,
	cfg *pkg.Config,
	opts ...RouterOptionFunc,
) (http.Handler, error) {
	o := &routerOptions{metricsRegisterer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		opt(o)
	}
	providerHandlers := make(map[string]http.Handler, len(cfg.Providers))
	var routes []handler.ModelRoute

	for _, name := range providerKeys(cfg) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		prov := cfg.Providers[name]
		upstream, err := url.Parse(prov.Upstream)
		if err != nil {
			return nil, errors.Wrapf(
				ctx,
				err,
				"provider %q: parse upstream %q",
				name,
				prov.Upstream,
			)
		}
		transport := handler.NewLoggingRoundTripper(
			handler.NewAuthSwapTransport(handler.DefaultProxyTransport(), prov.Token),
			liblog.SamplerList{liblog.NewSampleTime(time.Second), liblog.NewSamplerGlogLevel(5)},
			libtime.NewCurrentDateTime(),
		)
		proxy := handler.NewAnthropicProxyHandler(upstream, transport)
		waitSeconds := prov.MaxConcurrentWaitSeconds
		if waitSeconds <= 0 {
			waitSeconds = defaultMaxConcurrentWaitSeconds
		}
		providerHandler := handler.NewConcurrencyLimiter(
			proxy,
			prov.MaxConcurrentRequests,
			time.Duration(waitSeconds)*time.Second,
		)
		providerHandlers[name] = providerHandler
		for _, pattern := range prov.Models {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			routes = append(routes, handler.ModelRoute{
				Pattern:               pattern,
				ProviderName:          name,
				Handler:               providerHandler,
				RequiresLeadingSystem: prov.RequiresLeadingSystem,
				AllowedApiKeys:        prov.AllowedApiKeys,
			})
		}
	}

	defaultHandler, ok := providerHandlers[cfg.Router.DefaultProvider]
	if !ok {
		// Defensive: Config.Validate already caught this, but keep the
		// safety net so future callers of CreateRouterFromConfig can't
		// bypass it.
		return nil, errors.New(
			ctx,
			fmt.Sprintf("default_provider %q not in providers", cfg.Router.DefaultProvider),
		)
	}

	metrics := handler.NewMetrics(cfg.Aliases)
	if err := metrics.Register(o.metricsRegisterer); err != nil {
		return nil, errors.Wrapf(ctx, err, "register metrics")
	}
	modelRouter := handler.NewModelRouter(
		routes,
		cfg.Router.DefaultProvider,
		defaultHandler,
		cfg.Aliases,
		liblog.DefaultSamplerFactory.Sampler(),
		metrics,
		libtime.NewCurrentDateTime(),
	)

	// The inbound key set is the single auth registry resolved from the
	// config: the top-level allowedApiKeys when non-empty, else the union of
	// every provider's allowedApiKeys. The auth middleware (empty set ⇒ no-op)
	// and the model router (key routing, prompt 3) both consume this set.
	authKeys := cfg.AllowedApiKeySet()
	mux := buildMux(modelRouter, cfg.Trace, authKeys)
	return mux, nil
}

// buildMux wires the operator-local admin handlers and the model router
// into a ServeMux. Admin endpoints are: /healthz, /readiness, /metrics,
// /setloglevel/, /enabletrace, /disabletrace, /gc, HEAD /{$}, /api/hello,
// and the catch-all 404 logger. The model router is wrapped in the auth middleware
// when allowedKeys is non-empty, and in the trace middleware when trace is
// true. Auth sits INSIDE trace so the trace middleware still observes
// x-api-key (redacted) and still captures requests auth rejects, while
// the header is gone before the request reaches the model router.
func buildMux(
	modelRouter http.Handler,
	trace bool,
	allowedKeys map[string]struct{},
) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/healthz", handler.NewHealthzHandler())
	mux.Handle("/readiness", libhttp.NewPrintHandler("OK"))
	// /metrics uses the global default registry (matches go-skeleton
	// convention) so process-level series (go_gc_*, go_memstats_*,
	// process_*) get included alongside the ccrouter_* application
	// series — useful for spotting GC pressure / memory growth on a
	// long-running router daemon.
	mux.Handle("/metrics", promhttp.Handler())
	// The four state-changing admin routes are loopback-only: a non-loopback
	// caller gets HTTP 403 before any handler logic runs, so a remote attacker
	// can never toggle tracing, force GC, or change log levels even when they
	// are the only other caller. Read-only endpoints (/healthz, /readiness,
	// /metrics, HEAD /{$}) stay open to remote callers so health probes keep
	// working.
	mux.Handle(
		"/setloglevel/",
		handler.NewAdminLoopbackGuard(
			handler.NewSetLoglevelHandler(),
			handler.IsLoopbackRemoteAddr,
		),
	)
	mux.Handle(
		"/enabletrace",
		handler.NewAdminLoopbackGuard(
			handler.NewEnableTraceHandler(),
			handler.IsLoopbackRemoteAddr,
		),
	)
	mux.Handle(
		"/disabletrace",
		handler.NewAdminLoopbackGuard(
			handler.NewDisableTraceHandler(),
			handler.IsLoopbackRemoteAddr,
		),
	)
	mux.Handle(
		"/gc",
		handler.NewAdminLoopbackGuard(
			libhttp.NewGarbageCollectorHandler(),
			handler.IsLoopbackRemoteAddr,
		),
	)
	v1Handler := http.Handler(modelRouter)
	v1Handler = handler.NewAuthMiddleware(v1Handler, allowedKeys)
	if trace {
		glog.V(2).Infof("trace enabled via config")
	}
	v1Handler = handler.NewTraceMiddleware(
		v1Handler,
		traceDir(),
		handler.DefaultTraceState(),
		trace,
	)
	mux.Handle("/v1/", v1Handler)
	// HEAD / -> 200: Claude Code probes the base URL for liveness before
	// dispatching its first /v1/messages on a fresh connection. Without
	// this the probe hits the catch-all and logs `[404] HEAD /` ahead of
	// every real request. The method-qualified pattern wins over "/" in
	// the Go 1.22+ ServeMux for HEAD requests to the root.
	mux.Handle("HEAD /{$}", handler.NewRootLivenessHandler())
	// /api/hello -> 200: Claude Code HEAD-probes this path as a
	// connectivity check. Without this handler every probe (roughly one
	// per second per running session) hits the catch-all and logs
	// `[404] HEAD /api/hello`, burying the unknown-path signals the
	// logger exists to surface. Path-only pattern (any method) wins over
	// the "/" catch-all in the Go 1.22+ ServeMux.
	mux.Handle("/api/hello", handler.NewHelloHandler())
	// Catch-all 404 logger — registered at "/" matches any path not
	// covered by a more specific pattern above. Logs at V(1) so unknown-
	// path probes (`/foo/bar`, typos like `/messages` without /v1) show
	// up alongside real traffic.
	mux.Handle("/", handler.NewNotFoundHandler())
	return mux
}

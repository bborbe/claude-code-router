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
	currentDateTime   libtime.CurrentDateTimeGetter
}

// WithMetricsRegisterer overrides the Prometheus registerer used for
// ccrouter_* metrics. Defaults to prometheus.DefaultRegisterer. Tests pass
// an isolated registry to avoid racing on the process-global default.
func WithMetricsRegisterer(reg prometheus.Registerer) RouterOptionFunc {
	return func(o *routerOptions) {
		o.metricsRegisterer = reg
	}
}

// WithCurrentDateTime overrides the clock used for time-window
// eligibility and the router's timestamps. Defaults to
// libtime.NewCurrentDateTime(). Tests pass a fixed clock
// (libtime.NewCurrentDateTime() + SetNow) for deterministic window
// checks (spec 014).
func WithCurrentDateTime(clock libtime.CurrentDateTimeGetter) RouterOptionFunc {
	return func(o *routerOptions) {
		o.currentDateTime = clock
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
func providerKeys(ctx context.Context, cfg *pkg.Config) []string {
	if len(cfg.ProviderOrder) > 0 {
		return cfg.ProviderOrder
	}
	keys := make([]string, 0, len(cfg.Providers))
	for k := range cfg.Providers {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
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
// config: per-provider upstream pools (each member its own reverse proxy,
// token-swap transport, and concurrency limiter), a model-name dispatcher
// on /v1/, and the canonical admin endpoints (/healthz, /readiness,
// /metrics, /setloglevel/, /gc). The model router emits its own structured
// one-line log per request at V(1)
// (`[req] METHOD path model=... provider=... status=... latency=...`),
// so no outer logging wrapper is needed — admin endpoints stay quiet.
//
//nolint:funlen,gocognit // per-provider/per-upstream wiring with per-loop ctx cancellation checks
func CreateRouterFromConfig(
	ctx context.Context,
	cfg *pkg.Config,
	opts ...RouterOptionFunc,
) (http.Handler, error) {
	o := &routerOptions{
		metricsRegisterer: prometheus.DefaultRegisterer,
		currentDateTime:   libtime.NewCurrentDateTime(),
	}
	for _, opt := range opts {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		opt(o)
	}
	providerHandlers := make(map[string]http.Handler, len(cfg.Providers))
	// Per-provider concurrency state consumed by the model-pool closures
	// (spec 013): upCaps records each upstream's MaxConcurrentRequests and
	// upInFlight each upstream's live in-flight counter (nil for uncapped
	// members), both keyed by provider name and indexed identically to the
	// provider's upstream list.
	upCaps := make(map[string][]int, len(cfg.Providers))
	upInFlight := make(map[string][]func() int, len(cfg.Providers))
	var routes []handler.ModelRoute

	for _, name := range providerKeys(ctx, cfg) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		prov := cfg.Providers[name]
		upstreams := prov.UpstreamList()
		members := make([]handler.UpstreamMember, 0, len(upstreams))
		caps := make([]int, 0, len(upstreams))
		inflights := make([]func() int, 0, len(upstreams))
		for _, up := range upstreams {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			upstream, err := url.Parse(up.Upstream)
			if err != nil {
				return nil, errors.Wrapf(
					ctx,
					err,
					"provider %q: parse upstream %q",
					name,
					up.Upstream,
				)
			}
			// Effective outbound token (spec 015): the member's own token wins (for
			// legacy single-upstream configs normalizeUpstreams/UpstreamList already
			// copied the provider-level token onto this member), else the top-level
			// default_token, else empty — an empty effective token keeps the
			// auth-swap no-op contract (client Authorization passes through).
			token := up.Token
			if token == "" {
				token = cfg.DefaultToken
			}
			// Auth-swap OUTER, logging INNER: the V(3) [upstream.headers] line (inside
			// the logging roundtripper) must reflect the SWAPPED outbound
			// Authorization — the operator evidence of which effective token went out
			// (spec 015 AC 2, <redacted len=N> distinguishes the global key from an
			// override key). With logging outer the line would show the client's
			// pre-swap header instead. An empty token returns the logging transport
			// unchanged (passthrough), identical to today's no-op wiring.
			transport := handler.NewAuthSwapTransport(
				handler.NewLoggingRoundTripper(
					handler.DefaultProxyTransport(),
					liblog.SamplerList{
						liblog.NewSampleTime(time.Second),
						liblog.NewSamplerGlogLevel(5),
					},
					libtime.NewCurrentDateTime(),
				),
				token,
			)
			proxy := handler.NewAnthropicProxyHandler(upstream, transport)
			waitSeconds := up.MaxConcurrentWaitSeconds
			if waitSeconds <= 0 {
				waitSeconds = defaultMaxConcurrentWaitSeconds
			}
			memberHandler := handler.NewConcurrencyLimiter(
				proxy,
				up.MaxConcurrentRequests,
				time.Duration(waitSeconds)*time.Second,
			)
			var inFlight func() int
			if limiter, ok := memberHandler.(interface{ InFlight() int }); ok {
				inFlight = limiter.InFlight
			}
			caps = append(caps, up.MaxConcurrentRequests)
			inflights = append(inflights, inFlight)
			members = append(members, handler.UpstreamMember{
				Upstream: up.Upstream,
				Handler:  memberHandler,
				Weight:   up.Weight,
				InFlight: inFlight,
				Window:   up.Window,
				Now:      o.currentDateTime.Now,
			})
		}
		upCaps[name] = caps
		upInFlight[name] = inflights
		providerHandler := handler.NewUpstreamPoolHandler(ctx, members)
		if providerHandler == nil {
			return nil, ctx.Err()
		}
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

	// Build the model-pool table (spec 013): each config member becomes a
	// runtime member carrying the provider's handler plus live load and
	// saturation closures. InFlight is the provider's total in-flight (sum
	// over its upstream counters, nil entries as 0) — the load the pool's
	// least-loaded and overflow selection reads. Saturated is the provider
	// being at capacity: every capped upstream is at its cap, and a single
	// uncapped upstream makes it never saturated. Config.Validate
	// guarantees the provider exists; the lookup failure is a defensive
	// safety net for direct CreateRouterFromConfig callers that bypass
	// Load.
	modelPools, err := buildModelPools(ctx, cfg.ModelPools, providerHandlers, upCaps, upInFlight)
	if err != nil {
		return nil, err
	}

	metrics := handler.NewMetrics(cfg.Aliases)
	if err := metrics.Register(o.metricsRegisterer); err != nil {
		return nil, errors.Wrapf(ctx, err, "register metrics")
	}
	modelRouter := handler.NewModelRouterWithPools(
		routes,
		cfg.Router.DefaultProvider,
		defaultHandler,
		cfg.Aliases,
		modelPools,
		liblog.DefaultSamplerFactory.Sampler(),
		metrics,
		o.currentDateTime,
	)

	// The inbound key set is the single auth registry resolved from the
	// config: the top-level allowedApiKeys when non-empty, else the union of
	// every provider's allowedApiKeys. The auth middleware (empty set ⇒ no-op)
	// and the model router (key routing, prompt 3) both consume this set.
	authKeys := cfg.AllowedApiKeySet()
	mux := buildMux(modelRouter, cfg.Trace, authKeys)
	return mux, nil
}

// buildModelPools builds the runtime model-pool table from config: for each
// pool name it resolves every member's provider handler (defensive —
// Config.Validate guarantees existence) and wires the InFlight/Saturated
// closures from the provider's per-upstream caps and in-flight counters, so
// overflow and least-loaded selection read real production load. Extracted
// from CreateRouterFromConfig to keep that function's complexity under the
// maintidx gate.
func buildModelPools(
	ctx context.Context,
	cfgModelPools map[string][]pkg.ModelPoolMember,
	providerHandlers map[string]http.Handler,
	upCaps map[string][]int,
	upInFlight map[string][]func() int,
) (map[string]*handler.ModelPool, error) {
	modelPools := make(map[string]*handler.ModelPool, len(cfgModelPools))
	for name, cfgMembers := range cfgModelPools {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		members := make([]handler.ModelPoolMember, 0, len(cfgMembers))
		for _, m := range cfgMembers {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			member, err := buildPoolMember(ctx, name, m, providerHandlers, upCaps, upInFlight)
			if err != nil {
				return nil, err
			}
			members = append(members, member)
		}
		pool := handler.NewModelPool(ctx, members)
		if pool == nil {
			return nil, ctx.Err()
		}
		modelPools[name] = pool
	}
	return modelPools, nil
}

// buildPoolMember resolves one configured pool member to its runtime form:
// the provider's request handler plus the InFlight/Saturated closures that
// read the provider's per-upstream caps and in-flight counters, so overflow
// and least-loaded selection see real production load. The provider lookup
// is defensive — Config.Validate guarantees it exists.
func buildPoolMember(
	ctx context.Context,
	poolName string,
	m pkg.ModelPoolMember,
	providerHandlers map[string]http.Handler,
	upCaps map[string][]int,
	upInFlight map[string][]func() int,
) (handler.ModelPoolMember, error) {
	providerHandler, ok := providerHandlers[m.Provider]
	if !ok {
		return handler.ModelPoolMember{}, errors.New(ctx, fmt.Sprintf(
			"model pool %q: provider %q has no handler",
			poolName, m.Provider,
		))
	}
	caps := upCaps[m.Provider]
	inflights := upInFlight[m.Provider]
	return handler.ModelPoolMember{
		Provider: m.Provider,
		Model:    m.Model,
		Weight:   m.Weight,
		Overflow: m.Overflow,
		Handler:  providerHandler,
		InFlight: func() int {
			return sumInFlight(inflights)
		},
		Saturated: func() bool {
			return providerSaturated(caps, inflights)
		},
	}, nil
}

// sumInFlight totals a provider's per-upstream in-flight counters, treating
// nil (an uncapped upstream) as 0.
func sumInFlight(inflights []func() int) int {
	total := 0
	for _, fn := range inflights {
		if fn != nil {
			total += fn()
		}
	}
	return total
}

// providerSaturated reports whether a provider is at capacity: every capped
// upstream is at its cap, and a single uncapped upstream makes it never
// saturated.
func providerSaturated(caps []int, inflights []func() int) bool {
	if len(caps) == 0 {
		return false
	}
	for i, cap := range caps {
		if cap <= 0 || inflights[i] == nil || inflights[i]() < cap {
			return false
		}
	}
	return true
}

// buildMux wires the operator-local admin handlers and the model router
// into a ServeMux. Admin endpoints are: /healthz, /readiness, /metrics,
// /setloglevel/, /enabletrace, /disabletrace, /gc, HEAD /{$}, /api/hello,
// and the catch-all 404 logger. The model router is wrapped, deepest first,
// in the session middleware (strips x-session-id outbound and carries it on
// the context for the upstream pool handler to pin on), then the auth
// middleware when allowedKeys is non-empty, then the trace middleware when
// trace is true — so the chain is trace → auth → session → modelRouter. Auth
// sits INSIDE trace so the trace middleware still observes x-api-key
// (redacted) and still captures requests auth rejects, while the header is
// gone before the request reaches the model router; session sits inside auth
// so x-session-id is read and stripped before dispatch and never reaches an
// upstream.
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
	v1Handler := http.Handler(handler.NewSessionMiddleware(modelRouter))
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

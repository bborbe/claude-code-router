// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
	liblog "github.com/bborbe/log"
	librun "github.com/bborbe/run"
	libtime "github.com/bborbe/time"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/handler"
)

// CreateServer loads the config at configPath, wires the model router
// + per-provider proxies, and returns a run.Func that starts the HTTP
// listener with graceful shutdown on ctx cancel.
func CreateServer(ctx context.Context, listen, configPath string) (librun.Func, error) {
	cfg, err := pkg.Load(ctx, configPath)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "load config")
	}
	router, err := CreateRouterFromConfig(ctx, cfg)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "build router")
	}
	return libhttp.NewServer(listen, router, streamingServerTimeouts), nil
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

// CreateRouterFromConfig builds the HTTP handler tree from a parsed
// config: per-provider reverse-proxies with token-swap transports, a
// model-name dispatcher on /v1/, and the canonical admin endpoints
// (/healthz, /readiness, /metrics, /setloglevel/, /gc). The model
// router emits its own structured one-line log per request at V(1)
// (`[req] METHOD path model=... provider=... status=... latency=...`),
// so no outer logging wrapper is needed — admin endpoints stay quiet.
func CreateRouterFromConfig(ctx context.Context, cfg *pkg.Config) (http.Handler, error) {
	providerHandlers := make(map[string]http.Handler, len(cfg.Providers))
	var routes []handler.ModelRoute

	for name, prov := range cfg.Providers {
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
		providerHandlers[name] = proxy
		for _, pattern := range prov.Models {
			routes = append(routes, handler.ModelRoute{
				Pattern:      pattern,
				ProviderName: name,
				Handler:      proxy,
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
	if err := metrics.Register(prometheus.DefaultRegisterer); err != nil {
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

	mux := http.NewServeMux()
	mux.Handle("/healthz", handler.NewHealthzHandler())
	mux.Handle("/readiness", libhttp.NewPrintHandler("OK"))
	// /metrics uses the global default registry (matches go-skeleton
	// convention) so process-level series (go_gc_*, go_memstats_*,
	// process_*) get included alongside the ccrouter_* application
	// series — useful for spotting GC pressure / memory growth on a
	// long-running router daemon.
	mux.Handle("/metrics", promhttp.Handler())
	mux.Handle("/setloglevel/", handler.NewSetLoglevelHandler())
	mux.Handle("/gc", libhttp.NewGarbageCollectorHandler())
	mux.Handle("/v1/", modelRouter)
	// Catch-all 404 logger — registered at "/" matches any path not
	// covered by a more specific pattern above. Logs at V(1) so unknown-
	// path probes (`/foo/bar`, typos like `/messages` without /v1) show
	// up alongside real traffic.
	mux.Handle("/", handler.NewNotFoundHandler())
	return mux, nil
}

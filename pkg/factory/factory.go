// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory

import (
	"fmt"
	"net/http"
	"net/url"

	libhttp "github.com/bborbe/http"
	liblog "github.com/bborbe/log"
	librun "github.com/bborbe/run"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	pkgcfg "github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/handler"
)

// CreateServer loads the config at configPath, wires the model router
// + per-provider proxies, and returns a run.Func that starts the HTTP
// listener with graceful shutdown on ctx cancel.
func CreateServer(listen, configPath string) (librun.Func, error) {
	cfg, err := pkgcfg.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	router, err := CreateRouterFromConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build router: %w", err)
	}
	return libhttp.NewServer(listen, router), nil
}

// CreateRouterFromConfig builds the HTTP handler tree from a parsed
// config: per-provider reverse-proxies with token-swap transports, a
// model-name dispatcher on /v1/, and the canonical admin endpoints
// (/healthz, /readiness, /metrics, /setloglevel/, /gc). The model
// router emits its own structured one-line log per request at V(1)
// (`[req] METHOD path model=... provider=... status=... latency=...`),
// so no outer logging wrapper is needed — admin endpoints stay quiet.
func CreateRouterFromConfig(cfg *pkgcfg.Config) (http.Handler, error) {
	providerHandlers := make(map[string]http.Handler, len(cfg.Providers))
	var routes []handler.ModelRoute

	for name, prov := range cfg.Providers {
		upstream, err := url.Parse(prov.Upstream)
		if err != nil {
			return nil, fmt.Errorf("provider %q: parse upstream %q: %w", name, prov.Upstream, err)
		}
		transport := handler.NewAuthSwapTransport(handler.DefaultProxyTransport(), prov.Token)
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
		return nil, fmt.Errorf("default_provider %q not in providers", cfg.Router.DefaultProvider)
	}

	metrics := handler.NewMetrics()
	if err := metrics.Register(prometheus.DefaultRegisterer); err != nil {
		return nil, fmt.Errorf("register metrics: %w", err)
	}
	modelRouter := handler.NewModelRouter(
		routes,
		cfg.Router.DefaultProvider,
		defaultHandler,
		cfg.Aliases,
		liblog.DefaultSamplerFactory.Sampler(),
		metrics,
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

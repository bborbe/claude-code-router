// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory

import (
	"fmt"
	"net/http"
	"net/url"

	libhttp "github.com/bborbe/http"
	librun "github.com/bborbe/run"

	"github.com/bborbe/claude-code-router/pkg/config"
	"github.com/bborbe/claude-code-router/pkg/handler"
)

// CreateServer loads the config at configPath, wires the model router
// + per-provider proxies, and returns a run.Func that starts the HTTP
// listener with graceful shutdown on ctx cancel.
func CreateServer(listen, configPath string) (librun.Func, error) {
	cfg, err := config.Load(configPath)
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
// model-name dispatcher on /v1/, the canonical admin endpoints, and
// the logging wrapper around the whole mux.
func CreateRouterFromConfig(cfg *config.Config) (http.Handler, error) {
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
			routes = append(routes, handler.ModelRoute{Pattern: pattern, Handler: proxy})
		}
	}

	defaultHandler, ok := providerHandlers[cfg.Router.DefaultProvider]
	if !ok {
		// Defensive: Config.Validate already caught this, but keep the
		// safety net so future callers of CreateRouterFromConfig can't
		// bypass it.
		return nil, fmt.Errorf("default_provider %q not in providers", cfg.Router.DefaultProvider)
	}

	modelRouter := handler.NewModelRouter(routes, defaultHandler, cfg.Aliases)

	mux := http.NewServeMux()
	mux.Handle("/healthz", handler.NewHealthzHandler())
	mux.Handle("/readiness", libhttp.NewPrintHandler("OK"))
	mux.Handle("/metrics", libhttp.NewPrintHandler("# metrics not enabled in v1 skeleton\n"))
	mux.Handle("/setloglevel/", handler.NewSetLoglevelHandler())
	mux.Handle("/gc", libhttp.NewGarbageCollectorHandler())
	mux.Handle("/v1/", modelRouter)
	return handler.NewLoggingHandler(mux), nil
}

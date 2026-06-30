// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package reloader

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"github.com/bborbe/errors"
	"github.com/golang/glog"

	"github.com/bborbe/claude-code-router/pkg"
)

// Reloader holds the atomic request-dispatch handler that the HTTP
// server serves through, plus the SIGHUP-driven reload loop. On each
// successful SIGHUP the entire mux is rebuilt via CreateRouterFromConfig
// and atomically swapped; in-flight requests already inside the old
// handler tree finish against it. A failed reload leaves the old
// handler pointer intact.
type Reloader struct {
	handler atomic.Value // stores http.Handler
	cfgPath string
	current atomic.Value // stores *pkg.Config
	build   func(ctx context.Context, cfg *pkg.Config) (http.Handler, error)
}

// NewReloader constructs the Reloader and stores initial via handler.Store.
func NewReloader(
	cfgPath string,
	initial http.Handler,
	build func(ctx context.Context, cfg *pkg.Config) (http.Handler, error),
) *Reloader {
	r := &Reloader{
		cfgPath: cfgPath,
		build:   build,
	}
	r.handler.Store(initial)
	return r
}

// SeedConfig seeds the initial config for ConfigSnapshot before the first
// successful reload.
func (r *Reloader) SeedConfig(cfg *pkg.Config) {
	r.current.Store(cfg)
}

// ServeHTTP loads the current handler and calls its ServeHTTP.
// This is the http.Handler that libhttp.NewServer dispatches through.
func (r *Reloader) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	h, ok := r.handler.Load().(http.Handler)
	if !ok {
		return
	}
	h.ServeHTTP(w, req)
}

// ConfigSnapshot returns the currently-active config. Used by prompt 2's
// tests to inspect the active config without racing the reload loop.
func (r *Reloader) ConfigSnapshot() *pkg.Config {
	cfg, ok := r.current.Load().(*pkg.Config)
	if !ok {
		return nil
	}
	return cfg
}

// Reload performs ONE reload attempt: loads the config, builds a new handler
// via r.build, and atomically swaps it. Returns the error from Load or build;
// on error the old handler stays active (no Store call).
func (r *Reloader) Reload(ctx context.Context) error {
	cfg, err := pkg.Load(ctx, r.cfgPath)
	if err != nil {
		return err
	}
	newHandler, err := r.build(ctx, cfg)
	if err != nil {
		return err
	}
	oldCfgVal := r.current.Load()
	oldCfg, ok := oldCfgVal.(*pkg.Config)
	if !ok {
		return errors.New(ctx, "current config is not a *pkg.Config")
	}
	oldCount := len(oldCfg.Providers)
	newCount := len(cfg.Providers)
	r.current.Store(cfg)
	r.handler.Store(newHandler)
	glog.V(1).Infof("config reloaded old_providers=%d new_providers=%d", oldCount, newCount)
	return nil
}

// reloadWithRecover wraps Reload in a recover sheet so panics during mux
// rebuild never crash the process. Logs recovered panics at ERROR.
func (r *Reloader) reloadWithRecover(ctx context.Context) {
	defer func() {
		if rec := recover(); rec != nil {
			glog.Errorf("config reload panic: %v", rec)
		}
	}()
	if err := r.Reload(ctx); err != nil {
		glog.Warningf("config reload failed: %v", err)
	}
}

// RunSighupLoop blocks until ctx is cancelled, handling SIGHUP. On each
// SIGHUP it calls r.Reload(ctx). A reload error is logged at WARNING
// with the message `config reload failed: <err>` and the old config is
// retained. A panic during reload is recovered (no goroutine leak, no
// process crash) and logged at ERROR. SIGHUP NEVER cancels ctx; only
// SIGINT/SIGTERM (handled by run.ContextWithSig upstream) cancel it.
func (r *Reloader) RunSighupLoop(ctx context.Context) {
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)
	defer signal.Stop(sighup)

	for {
		select {
		case <-sighup:
			r.reloadWithRecover(ctx)
		case <-ctx.Done():
			return
		}
	}
}

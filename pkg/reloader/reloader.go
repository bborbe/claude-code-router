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

	"github.com/golang/glog"

	"github.com/bborbe/claude-code-router/pkg"
)

// snapshot pairs the active handler and config so they swap together in a
// single atomic Store. This prevents a partial-swap inconsistency where a
// panic between two separate Stores would leave ConfigSnapshot() reporting
// the new config while ServeHTTP dispatches through the old handler.
type snapshot struct {
	handler http.Handler
	cfg     *pkg.Config
}

// Reloader holds the atomic request-dispatch handler that the HTTP
// server serves through, plus the SIGHUP-driven reload loop. On each
// successful SIGHUP the entire mux is rebuilt via CreateRouterFromConfig
// and atomically swapped; in-flight requests already inside the old
// handler tree finish against it. A failed reload leaves the old
// handler pointer intact.
type Reloader struct {
	snap    atomic.Value // stores snapshot
	cfgPath string
	build   func(ctx context.Context, cfg *pkg.Config) (http.Handler, error)
}

// NewReloader constructs the Reloader and stores initial via a snapshot.
func NewReloader(
	cfgPath string,
	initial http.Handler,
	build func(ctx context.Context, cfg *pkg.Config) (http.Handler, error),
) *Reloader {
	r := &Reloader{
		cfgPath: cfgPath,
		build:   build,
	}
	r.snap.Store(snapshot{handler: initial})
	return r
}

// SeedConfig seeds the initial config for ConfigSnapshot before the first
// successful reload. Overwrites the handler's snapshot with one carrying cfg.
func (r *Reloader) SeedConfig(cfg *pkg.Config) {
	prev, _ := r.snap.Load().(snapshot)
	r.snap.Store(snapshot{handler: prev.handler, cfg: cfg})
}

// ServeHTTP loads the current handler and calls its ServeHTTP.
// This is the http.Handler that libhttp.NewServer dispatches through.
func (r *Reloader) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	s, _ := r.snap.Load().(snapshot)
	if s.handler == nil {
		return
	}
	s.handler.ServeHTTP(w, req)
}

// ConfigSnapshot returns the currently-active config. Used by prompt 2's
// tests to inspect the active config without racing the reload loop.
func (r *Reloader) ConfigSnapshot() *pkg.Config {
	s, _ := r.snap.Load().(snapshot)
	return s.cfg
}

// Reload performs ONE reload attempt: loads the config, builds a new handler
// via r.build, and atomically swaps them together. Returns the error from
// Load or build; on error the old snapshot stays active (no Store call).
func (r *Reloader) Reload(ctx context.Context) error {
	cfg, err := pkg.Load(ctx, r.cfgPath)
	if err != nil {
		glog.Warningf("config reload failed: %v", err)
		return err
	}
	newHandler, err := r.build(ctx, cfg)
	if err != nil {
		glog.Warningf("config reload failed: %v", err)
		return err
	}
	prev, _ := r.snap.Load().(snapshot)
	oldCount := 0
	if prev.cfg != nil {
		oldCount = len(prev.cfg.Providers)
	}
	newCount := len(cfg.Providers)
	// Single atomic Store — handler and cfg swap together, so a panic before
	// this line leaves the old snapshot fully intact (no partial desync).
	r.snap.Store(snapshot{handler: newHandler, cfg: cfg})
	glog.V(1).Infof("config reloaded old_providers=%d new_providers=%d", oldCount, newCount)
	return nil
}

// reloadWithRecover wraps Reload in a recover sheet so panics during mux
// rebuild never crash the process. Logs recovered panics at ERROR. Reload
// owns its own WARNING log on failure (so direct callers see the line too);
// this wrapper does NOT re-log the error to avoid double-logging the same
// failure on the SIGHUP-driven path.
func (r *Reloader) reloadWithRecover(ctx context.Context) {
	defer func() {
		if rec := recover(); rec != nil {
			glog.Errorf("config reload panic: %v", rec)
		}
	}()
	_ = r.Reload(ctx)
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

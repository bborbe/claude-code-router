// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	liblog "github.com/bborbe/log"
	"github.com/golang/glog"
)

// SetLoglevelDefault is the verbosity we revert to after autoRevertAfter.
// V(1) keeps the structured per-request line; the operator bumps to 2/3
// for alias/route detail or to higher tiers for deep debug.
const (
	SetLoglevelDefault    = 1
	SetLoglevelAutoRevert = 5 * time.Minute
)

// NewSetLoglevelHandler returns a handler that flips glog's -v verbosity
// at runtime. Convenience wrapper for the production default
// (SetLoglevelAutoRevert = 5 min); tests use NewSetLoglevelHandlerWithRevert
// with a short window.
func NewSetLoglevelHandler() http.Handler {
	return NewSetLoglevelHandlerWithRevert(SetLoglevelAutoRevert)
}

// NewSetLoglevelHandlerWithRevert returns a handler that flips glog's -v
// verbosity at runtime. URL shape is `/setloglevel/<level>`; the integer
// suffix is parsed and passed to `log.LogLevelSetter.Set`, which auto-
// reverts to SetLoglevelDefault after autoRevert so a forgotten bump
// can't leave the router in verbose mode indefinitely.
//
// The LogLevelSetter is created once at handler construction (single
// instance, shared across requests); `Set()` itself spawns the auto-
// revert goroutine per call — that's upstream bborbe/log behavior, and
// `resetLogLevel` is idempotent so overlapping timers are harmless.
//
// Level validation: negative values are rejected with 400; glog itself
// accepts any int32, but a negative verbosity is meaningless and almost
// always a typo (`-1` instead of `1`).
//
// Example:
//
//	$ curl http://127.0.0.1:8788/setloglevel/3
//	set loglevel to 3 completed
//
// Stdlib-mux compatible: parses the level from URL.Path directly rather
// than relying on gorilla/mux path vars.
func NewSetLoglevelHandlerWithRevert(autoRevert time.Duration) http.Handler {
	setter := liblog.NewLogLevelSetter(SetLoglevelDefault, autoRevert)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		levelStr := strings.TrimPrefix(r.URL.Path, "/setloglevel/")
		level, err := strconv.ParseInt(levelStr, 10, 32)
		if err != nil {
			http.Error(w, fmt.Sprintf("parse loglevel failed: %v", err), http.StatusBadRequest)
			return
		}
		if level < 0 {
			http.Error(
				w,
				fmt.Sprintf("loglevel must be >= 0, got %d", level),
				http.StatusBadRequest,
			)
			return
		}
		if err := setter.Set(r.Context(), glog.Level(int32(level))); err != nil {
			http.Error(
				w,
				fmt.Sprintf("set loglevel failed: %v", err),
				http.StatusInternalServerError,
			)
			return
		}
		fmt.Fprintf(w, "set loglevel to %d completed\n", level)
	})
}

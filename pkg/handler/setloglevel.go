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
// at runtime. URL shape is `/setloglevel/<level>`; the integer suffix is
// parsed and passed to `log.LogLevelSetter.Set`, which auto-reverts to
// SetLoglevelDefault after SetLoglevelAutoRevert so a forgotten bump
// can't leave the router in verbose mode indefinitely.
//
// Example:
//
//	$ curl http://127.0.0.1:8788/setloglevel/3
//	set loglevel to 3 completed
//
// Stdlib-mux compatible: parses the level from URL.Path directly rather
// than relying on gorilla/mux path vars.
func NewSetLoglevelHandler() http.Handler {
	setter := liblog.NewLogLevelSetter(SetLoglevelDefault, SetLoglevelAutoRevert)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		levelStr := strings.TrimPrefix(r.URL.Path, "/setloglevel/")
		level, err := strconv.ParseInt(levelStr, 10, 32)
		if err != nil {
			http.Error(w, fmt.Sprintf("parse loglevel failed: %v", err), http.StatusBadRequest)
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

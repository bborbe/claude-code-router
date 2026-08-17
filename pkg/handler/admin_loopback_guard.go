// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"net/http"

	"github.com/golang/glog"
)

// NewAdminLoopbackGuard wraps next so that only requests arriving from a
// loopback remote address reach the wrapped handler. Any other request is
// refused with HTTP 403 and never reaches next, so a refused request can
// never change router state. The guard is unconditional — there is no
// config knob to disable it — and stateless, so SIGHUP reloads need no
// plumbing.
//
// isLoopback is injected (production passes IsLoopbackRemoteAddr) so tests
// can drive the guard with a fake predicate and a counting inner handler.
// The remote address comes from r.RemoteAddr (the connection) only —
// never from X-Forwarded-For or any other client-supplied header, since no
// trusted proxy sits in front of this router.
func NewAdminLoopbackGuard(next http.Handler, isLoopback func(string) bool) http.Handler {
	return &adminLoopbackGuard{next: next, isLoopback: isLoopback}
}

type adminLoopbackGuard struct {
	next       http.Handler
	isLoopback func(string) bool
}

func (g *adminLoopbackGuard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if g.isLoopback(r.RemoteAddr) {
		g.next.ServeHTTP(w, r)
		return
	}
	glog.Infof("admin refused path=%s %s remote=%s", r.Method, r.URL.Path, r.RemoteAddr)
	http.Error(w, "admin endpoint loopback-only", http.StatusForbidden)
}

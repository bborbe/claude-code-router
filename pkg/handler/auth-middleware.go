// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"crypto/subtle"
	"net/http"

	"github.com/golang/glog"
)

// NewAuthMiddleware wraps next so every non-loopback request must present
// the shared key in the x-router-key header before reaching next. A
// missing or non-matching key is rejected with 401 and never reaches the
// wrapped handler. Loopback requests bypass the key comparison entirely
// but still have the header stripped before forwarding, so an upstream
// never observes the operator's router key. On both the authenticated and
// the bypassed path the header is removed from a cloned request before
// next sees it; the inbound request is never mutated.
//
// If key is empty, the wrapper is a no-op and returns next unchanged, so a
// keyless config has zero effect on the request hot path.
func NewAuthMiddleware(next http.Handler, key string) http.Handler {
	if key == "" {
		return next
	}
	return &authMiddleware{next: next, key: key}
}

type authMiddleware struct {
	next http.Handler
	key  string
}

func (a *authMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !IsLoopbackRemoteAddr(r.RemoteAddr) {
		presented := r.Header.Get("X-Router-Key")
		if presented == "" || subtle.ConstantTimeCompare([]byte(presented), []byte(a.key)) != 1 {
			glog.Infof("auth rejected remote=%s", r.RemoteAddr)
			http.Error(w, "auth required", http.StatusUnauthorized)
			return
		}
	}
	clone := r.Clone(r.Context())
	clone.Header.Del("X-Router-Key")
	a.next.ServeHTTP(w, clone)
}

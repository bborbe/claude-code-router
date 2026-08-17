// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"crypto/subtle"
	"net/http"

	"github.com/golang/glog"
)

// NewAuthMiddleware wraps next so every non-loopback /v1/* request must
// present an x-api-key header whose value is in allowedKeys. A missing
// or non-matching key is rejected with 401 and never reaches next.
// Loopback requests bypass the key check entirely but still have the
// header stripped and still record the presented key in context, so an
// upstream never observes the caller's API key and the router can route
// by it. If allowedKeys is empty, the wrapper is a no-op and returns
// next unchanged — the request path is byte-for-byte identical to a
// release without key routing (feature-off default, AC 1).
func NewAuthMiddleware(next http.Handler, allowedKeys map[string]struct{}) http.Handler {
	if len(allowedKeys) == 0 {
		return next
	}
	return &authMiddleware{next: next, allowedKeys: allowedKeys}
}

type authMiddleware struct {
	next        http.Handler
	allowedKeys map[string]struct{}
}

func (a *authMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	presented := r.Header.Get("X-Api-Key")
	if !IsLoopbackRemoteAddr(r.RemoteAddr) {
		if presented == "" || !a.accepts(presented) {
			glog.Infof("auth rejected remote=%s", r.RemoteAddr)
			http.Error(w, "auth required", http.StatusUnauthorized)
			return
		}
	}
	clone := r.Clone(r.Context())
	clone.Header.Del("X-Api-Key")
	if presented != "" {
		clone = clone.WithContext(ContextWithPresentedApiKey(clone.Context(), presented))
	}
	a.next.ServeHTTP(w, clone)
}

// accepts reports whether presented matches any key in the registry. Every
// allowed key is compared with a constant-time compare and there is no
// early exit, so the comparison timing does not leak which key (if any)
// matched — the only observable signal is the final accept/reject.
func (a *authMiddleware) accepts(presented string) bool {
	matched := 0
	for k := range a.allowedKeys {
		matched |= subtle.ConstantTimeCompare([]byte(presented), []byte(k))
	}
	return matched == 1
}

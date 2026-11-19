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
// by it.
//
// The header is stripped on EVERY path, including the feature-off default
// where allowedKeys is empty. An empty registry turns off key ROUTING —
// no rejection, and no key recorded in the context — but never the strip.
// A caller that sends x-api-key alongside its Authorization (pi does;
// Claude Code does not) would otherwise have the extra Anthropic-style
// header forwarded to the provider, where it takes precedence over the
// bearer and is rejected as an invalid API key.
func NewAuthMiddleware(next http.Handler, allowedKeys map[string]struct{}) http.Handler {
	return &authMiddleware{next: next, allowedKeys: allowedKeys}
}

type authMiddleware struct {
	next        http.Handler
	allowedKeys map[string]struct{}
}

func (a *authMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	presented := r.Header.Get("X-Api-Key")
	// An empty registry means key routing is off: no rejection and no key
	// recorded in the context. The strip below still runs, on this path as
	// on every other — see NewAuthMiddleware's contract.
	if len(a.allowedKeys) > 0 && !IsLoopbackRemoteAddr(r.RemoteAddr) {
		if presented == "" || !a.accepts(presented) {
			glog.Infof("auth rejected remote=%s", r.RemoteAddr)
			http.Error(w, "auth required", http.StatusUnauthorized)
			return
		}
	}
	clone := r.Clone(r.Context())
	clone.Header.Del("X-Api-Key")
	if presented != "" && len(a.allowedKeys) > 0 {
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

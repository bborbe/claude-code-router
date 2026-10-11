// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import "net/http"

// NewSessionMiddleware wraps next so every /v1/* request's x-session-id
// header is stripped before it reaches next and — when present — carried
// on the request context for the upstream pool handler to pin on. The
// header is read ONLY as hash input for pinning: never for auth, never
// logged. An absent or empty header leaves the context value unset
// (SessionIDFromContext returns ""), which dispatches the request
// keyless.
//
// When x-session-id is absent, the id Claude Code sends on every request
// (x-claude-code-session-id) is used instead, so Claude Code sessions are
// recognised as warm by the cold-start gate. That header is left on the
// request: it is Claude Code's own, not router-private.
func NewSessionMiddleware(next http.Handler) http.Handler {
	return &sessionMiddleware{next: next}
}

type sessionMiddleware struct {
	next http.Handler
}

func (s *sessionMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get("X-Session-Id")
	if sessionID == "" {
		sessionID = r.Header.Get("X-Claude-Code-Session-Id")
	}
	clone := r.Clone(r.Context())
	// Header.Del is case-insensitive, so every letter case is stripped —
	// the upstream never observes the caller's session id.
	clone.Header.Del("X-Session-Id")
	if sessionID != "" {
		clone = clone.WithContext(ContextWithSessionID(clone.Context(), sessionID))
	}
	s.next.ServeHTTP(w, clone)
}

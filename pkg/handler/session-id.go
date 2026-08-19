// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import "context"

// sessionIDContextKey is an unexported type to avoid collisions with
// other context values.
type sessionIDContextKey struct{}

// ContextWithSessionID returns a copy of ctx carrying the x-session-id
// value the session middleware stripped from the request. It is also a
// test seam: upstream-pool specs inject a session id directly without
// running the middleware.
func ContextWithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDContextKey{}, sessionID)
}

// SessionIDFromContext returns the x-session-id value stored by
// ContextWithSessionID, or "" when absent (the empty session id state
// that dispatches a request keyless).
func SessionIDFromContext(ctx context.Context) string {
	sessionID, _ := ctx.Value(sessionIDContextKey{}).(string)
	return sessionID
}

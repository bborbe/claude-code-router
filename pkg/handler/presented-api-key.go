// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import "context"

// presentedApiKeyContextKey is an unexported type to avoid collisions
// with other context values.
type presentedApiKeyContextKey struct{}

// ContextWithPresentedApiKey returns a copy of ctx carrying the
// x-api-key value the auth middleware accepted. It is also a test seam:
// routing specs inject a key directly without running the middleware.
func ContextWithPresentedApiKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, presentedApiKeyContextKey{}, key)
}

// PresentedApiKeyFromContext returns the x-api-key value stored by
// ContextWithPresentedApiKey, or "" when absent.
func PresentedApiKeyFromContext(ctx context.Context) string {
	key, _ := ctx.Value(presentedApiKeyContextKey{}).(string)
	return key
}

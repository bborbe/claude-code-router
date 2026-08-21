// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import "context"

// upstreamIndexContextKey is an unexported type to avoid collisions with
// other context values.
type upstreamIndexContextKey struct{}

// upstreamIndexSlot is a per-request value shared between the model
// router (creator) and an upstream pool handler (writer) through the
// request context. The router injects one slot into the request context
// immediately before dispatch and reads the selected member's zero-based
// index back after the dispatch returns; the pool handler writes the
// index of the member it selected during dispatch (spec 016).
//
// The slot is a pointer so the pool handler can publish without replacing
// the router's request object: both sides reach the same allocation via
// the request context, and because each request gets its own slot the
// value is per-request and race-free under concurrent requests. A request
// whose dispatch path contains no upstream pool handler leaves the slot at
// its zero value, so the router logs a uniform `/0` — never a conditional
// omission.
type upstreamIndexSlot struct {
	index int
}

// ContextWithUpstreamIndex returns a copy of ctx carrying the shared
// upstream-member-index slot. The model router injects one slot per
// request before dispatch.
func ContextWithUpstreamIndex(ctx context.Context, slot *upstreamIndexSlot) context.Context {
	return context.WithValue(ctx, upstreamIndexContextKey{}, slot)
}

// UpstreamIndexSlotFromContext returns the shared slot carried by ctx, or
// nil when absent (a dispatch path without a wired pool handler).
func UpstreamIndexSlotFromContext(ctx context.Context) *upstreamIndexSlot {
	slot, _ := ctx.Value(upstreamIndexContextKey{}).(*upstreamIndexSlot)
	return slot
}

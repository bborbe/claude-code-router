// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"net/http"
)

// rejectionReasonContextKey is an unexported type to avoid collisions with
// other context values.
type rejectionReasonContextKey struct{}

// rejectionSlot is a per-request value shared between the model router
// (creator) and whichever 429-producing gate answers the request (writer)
// through the request context. The router injects one slot into the request
// context immediately before dispatch, each gate records the reason it
// refused with, and the router reads the reason back after dispatch returns
// to increment ccrouter_rejected_total once.
//
// The slot is a pointer so a gate can publish without replacing the
// router's request object, and because each request gets its own slot the
// value is per-request and race-free under concurrent requests — the gates
// run in the request's own goroutine, synchronously inside the router's
// dispatch. This is the same shape as upstreamIndexSlot (spec 016) and is
// deliberately not a counter increment inside each gate: the gates are
// nested (a host limiter outside a member limiter, a throttle gate outside
// a cold-start gate), so a per-gate increment would double-count a request
// refused by an outer gate after an inner one had already recorded, and no
// gate can see whether an outer one will refuse it later.
//
// An empty reason is the zero value and means "no router-side gate refused
// this request". The router turns that into reason=upstream when the
// response is a 429, because a 429 nobody claimed is the upstream's own.
type rejectionSlot struct {
	reason string
}

// ContextWithRejectionReason returns a copy of ctx carrying the shared
// rejection-reason slot. The model router injects one slot per request
// before dispatch.
func ContextWithRejectionReason(ctx context.Context, slot *rejectionSlot) context.Context {
	return context.WithValue(ctx, rejectionReasonContextKey{}, slot)
}

// RejectionSlotFromContext returns the shared slot carried by ctx, or nil
// when absent (a dispatch path outside the model router, such as a gate
// exercised directly in a test).
func RejectionSlotFromContext(ctx context.Context) *rejectionSlot {
	slot, _ := ctx.Value(rejectionReasonContextKey{}).(*rejectionSlot)
	return slot
}

// recordRejection marks the request's shared slot with the reason a gate
// refused it. It is a no-op when no slot is present, so a gate driven
// directly (unit tests, a future non-router wiring) still answers 429
// correctly — it simply is not counted, because there is no router-side
// observation point to count it at.
//
// First writer wins: an inner gate that already recorded its reason is not
// overwritten by an outer one. In practice the outermost gate is the one
// that answers (an inner gate's 429 is written to the same ResponseWriter
// and the outer gate forwards it), so this guards the nested case rather
// than changing it.
func recordRejection(r *http.Request, reason string) {
	slot := RejectionSlotFromContext(r.Context())
	if slot == nil || slot.reason != "" {
		return
	}
	slot.reason = reason
}

// rejectionReasonOf resolves which reason a 429 the router answered is
// attributed to: the reason a router-side gate recorded, or
// rejectionReasonUpstream when no gate claimed it. An unclaimed 429 is the
// upstream's own by elimination — every router-side 429 site writes through
// writeRateLimited, which records a reason, so a 429 carrying none did not
// come from the router.
//
// A nil slot (dispatch outside the model router) resolves to upstream for
// the same reason, but the router is the only caller, so in practice the
// slot is always present.
func rejectionReasonOf(slot *rejectionSlot) string {
	if slot != nil && slot.reason != "" {
		return slot.reason
	}
	return rejectionReasonUpstream
}

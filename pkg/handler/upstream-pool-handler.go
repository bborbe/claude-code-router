// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"hash/fnv"
	"net/http"
	"sync/atomic"
	stdtime "time"

	libtime "github.com/bborbe/time"
	"github.com/golang/glog"

	"github.com/bborbe/claude-code-router/pkg"
)

// WindowEligible is implemented by provider handlers that can be
// ineligible for a dispatch because none of their pool members are
// eligible — their time windows and/or weekday sets exclude "now" (spec
// 014 / 017). A handler that does not implement the interface is always
// eligible.
//
//counterfeiter:generate -o ../../mocks/window-eligible.go --fake-name WindowEligible . WindowEligible
type WindowEligible interface {
	HasEligibleMember(ctx context.Context) bool
}

// windowEligible reports whether a provider handler has at least one
// eligible pool member. Handlers without time windows or weekday sets
// are always eligible.
func windowEligible(ctx context.Context, h http.Handler) bool {
	if e, ok := h.(WindowEligible); ok {
		return e.HasEligibleMember(ctx)
	}
	return true
}

// UpstreamMember is one server in a provider's upstream pool: the
// handler that serves it (per-upstream proxy wrapped in its concurrency
// limiter), the upstream URL used only for the [route] log line, and the
// selection inputs — Weight for the weighted ring hash of a pinned
// session, InFlight for least-loaded selection of keyless requests.
// InFlight may be nil, meaning "always 0" (an uncapped member). Window,
// when non-nil, restricts eligibility to the times its Contains holds;
// Days, when non-nil, restricts eligibility to the weekdays its set
// contains. A member whose weekday is not in its days set is excluded
// from session pinning and least-loaded selection alike (spec 017).
type UpstreamMember struct {
	Upstream string
	Handler  http.Handler
	Weight   int
	InFlight func() int
	// Window, when non-nil, is the member's time-of-day eligibility
	// window: the member is eligible only while Window.Contains(now)
	// holds (spec 014). Nil = always eligible.
	Window *pkg.Window
	// Days, when non-nil, is the member's weekday eligibility set: the
	// member is eligible only while Days.Contains(now, Window) holds
	// (spec 017). Nil = every day.
	Days *pkg.Days
	// Now returns the router's current time (the injected
	// libtime.CurrentDateTimeGetter). Consulted only when Window or Days
	// is non-nil; nil Now falls back to the real clock.
	Now func() libtime.DateTime
}

// realNow is the defensive fallback clock for members whose Now is nil
// (direct-construction callers and tests). The factory always sets Now
// via the WithCurrentDateTime option default, so this is only reached by
// callers that build UpstreamMember literals without a clock.
var realNow = func() libtime.DateTime { return libtime.DateTime(stdtime.Now()) }

// NewUpstreamPoolHandler returns a handler that dispatches each request
// to exactly one of members: a request carrying a non-empty session id
// (from the session middleware's context) is pinned to the same member
// every time via a weighted ring hash of the session id — deterministic
// and stateless, recomputable from the id, no session→member map — so
// the session's upstream prompt cache stays warm on that server. A
// request with an empty session id is sent to the least-loaded member
// (fewest in-flight requests by the per-upstream semaphore), with
// round-robin tie-breaking among equally-loaded members so keyless
// floods spread instead of stacking on the first-declared member. A
// member whose time window does not contain "now", or whose weekday is
// not in its days set, is excluded from both selection paths — the ring
// and the least-loaded scan are computed over the eligible subset per
// request, so a member whose window closes mid-session stops receiving
// that session's requests immediately (spec 014 / 017). Each chosen
// dispatch emits a [route] session=<id> upstream=<url> glog V(2) detail
// line.
func NewUpstreamPoolHandler(ctx context.Context, members []UpstreamMember) http.Handler {
	return &upstreamPoolHandler{members: members}
}

type upstreamPoolHandler struct {
	members []UpstreamMember
	// rr is the keyless round-robin tie-break counter. It is the only
	// mutable state in the handler; pinning itself is stateless.
	rr uint64
}

func (p *upstreamPoolHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sessionID := SessionIDFromContext(r.Context())
	memberIndex := p.selectMember(r.Context(), sessionID)
	// Publish the selected member's zero-based index into the per-request
	// slot the model router injected (spec 016), so the router's [req]
	// line can log provider=<name>/<index>. The slot is nil when no pool
	// handler is in the dispatch path (test stubs, non-pool handlers) —
	// then the router keeps its default `/0`.
	if slot := UpstreamIndexSlotFromContext(r.Context()); slot != nil {
		slot.index = memberIndex
	}
	member := p.members[memberIndex]
	glog.V(2).Infof("[route] session=%s upstream=%s", sessionID, member.Upstream)
	member.Handler.ServeHTTP(w, r)
}

// selectMember returns the index of the member that serves a request: the
// weighted ring-hash slot for a non-empty session id, or the least-loaded
// member (round-robin among ties) for an empty one. Both selection paths
// operate on the eligible subset only (spec 014).
func (p *upstreamPoolHandler) selectMember(ctx context.Context, sessionID string) int {
	if sessionID != "" {
		return p.pinSlot(ctx, sessionID)
	}
	return p.leastLoaded(ctx)
}

// memberEligible reports whether member i is eligible for a dispatch:
// (window absent OR window.Contains(now)) AND (days absent OR
// days.Contains(now)). The clock is read only when a member carries a
// window or a days set; a member with neither is always eligible,
// byte-for-byte today (spec 014 / 017).
func (p *upstreamPoolHandler) memberEligible(i int) bool {
	m := p.members[i]
	if m.Window == nil && m.Days == nil {
		return true
	}
	now := m.Now
	if now == nil {
		now = realNow
	}
	if m.Window != nil && !m.Window.Contains(now()) {
		return false
	}
	if m.Days != nil && !m.Days.Contains(now(), m.Window) {
		return false
	}
	return true
}

// eligibleIndices returns the indices of members eligible right now.
func (p *upstreamPoolHandler) eligibleIndices(ctx context.Context) []int {
	idx := make([]int, 0, len(p.members))
	for i := range p.members {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		if p.memberEligible(i) {
			idx = append(idx, i)
		}
	}
	return idx
}

// HasEligibleMember reports whether at least one pool member is
// eligible right now (spec 014 DB 4). A provider whose pool returns
// false is ineligible for the dispatch and the model router falls
// through to the next provider / default_provider.
func (p *upstreamPoolHandler) HasEligibleMember(ctx context.Context) bool {
	return len(p.eligibleIndices(ctx)) > 0
}

// pinSlot returns the member index that the weighted ring hash of
// sessionID maps to over the currently-eligible members: slot = FNV-1a 64
// over the id, mod the eligible total weight, then the first eligible
// member whose cumulative weight exceeds the slot. The same session id
// always yields the same index while the same members are eligible —
// recomputable from the id, no session→member map — so the session's
// upstream prompt cache stays warm on that server across restarts. A
// member whose window excludes "now" is not part of the ring, so a
// session pinned to it re-resolves to an eligible member on the next
// request (spec 014).
func (p *upstreamPoolHandler) pinSlot(ctx context.Context, sessionID string) int {
	idx := p.eligibleIndices(ctx)
	if len(idx) == 0 {
		return 0
	}
	total := 0
	cumulative := make([]int, len(idx))
	for i, mi := range idx {
		total += p.members[mi].Weight
		cumulative[i] = total
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(sessionID))
	// #nosec G115 -- weights are config-validated positive ints; the
	// int->uint64 conversion is overflow-safe.
	slot := h.Sum64() % uint64(total)
	for i, c := range cumulative {
		select {
		case <-ctx.Done():
			return 0
		default:
		}
		// #nosec G115 -- see above.
		if uint64(c) > slot {
			return idx[i]
		}
	}
	return idx[len(idx)-1]
}

// leastLoaded returns the index of the currently-eligible member with the
// fewest in-flight requests, breaking ties round-robin (the atomic rr
// counter, cycled by the tie count) so equally-loaded members share
// keyless traffic instead of stacking on the first-declared one. Members
// whose window excludes "now" are not considered (spec 014).
func (p *upstreamPoolHandler) leastLoaded(ctx context.Context) int {
	idx := p.eligibleIndices(ctx)
	if len(idx) == 0 {
		return 0
	}
	min := p.inFlight(idx[0])
	for i := 1; i < len(idx); i++ {
		select {
		case <-ctx.Done():
			return 0
		default:
		}
		if load := p.inFlight(idx[i]); load < min {
			min = load
		}
	}
	ties := make([]int, 0, len(idx))
	for _, mi := range idx {
		select {
		case <-ctx.Done():
			return 0
		default:
		}
		if p.inFlight(mi) == min {
			ties = append(ties, mi)
		}
	}
	rr := atomic.AddUint64(&p.rr, 1)
	return ties[(rr-1)%uint64(len(ties))]
}

// inFlight returns member i's current in-flight count, treating a nil
// InFlight (an uncapped member — the production default until the
// concurrency limiter is wired into the member) as 0.
func (p *upstreamPoolHandler) inFlight(i int) int {
	if f := p.members[i].InFlight; f != nil {
		return f()
	}
	return 0
}

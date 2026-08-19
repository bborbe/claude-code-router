// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"hash/fnv"
	"net/http"
	"sync/atomic"

	"github.com/golang/glog"
)

// UpstreamMember is one server in a provider's upstream pool: the
// handler that serves it (per-upstream proxy wrapped in its concurrency
// limiter), the upstream URL used only for the [route] log line, and the
// selection inputs — Weight for the weighted ring hash of a pinned
// session, InFlight for least-loaded selection of keyless requests.
// InFlight may be nil, meaning "always 0" (an uncapped member).
type UpstreamMember struct {
	Upstream string
	Handler  http.Handler
	Weight   int
	InFlight func() int
}

// NewUpstreamPoolHandler returns a handler that dispatches each request
// to exactly one of members: a request carrying a non-empty session id
// (from the session middleware's context) is pinned to the same member
// every time via a weighted ring hash of the session id — deterministic
// and stateless, recomputable from the id, no session→member map — so
// the session's upstream prompt cache stays warm on that server. A
// request with an empty session id is sent to the least-loaded member
// (fewest in-flight requests by the per-upstream semaphore), with
// round-robin tie-breaking among equally-loaded members so keyless
// floods spread instead of stacking on the first-declared member. Each
// chosen dispatch emits a [route] session=<id> upstream=<url> glog V(2)
// detail line.
func NewUpstreamPoolHandler(ctx context.Context, members []UpstreamMember) http.Handler {
	cumulative := make([]int, len(members))
	total := 0
	for i, m := range members {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		total += m.Weight
		cumulative[i] = total
	}
	return &upstreamPoolHandler{
		members:     members,
		cumulative:  cumulative,
		totalWeight: total,
	}
}

type upstreamPoolHandler struct {
	members     []UpstreamMember
	cumulative  []int
	totalWeight int
	// rr is the keyless round-robin tie-break counter. It is the only
	// mutable state in the handler; pinning itself is stateless.
	rr uint64
}

func (p *upstreamPoolHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sessionID := SessionIDFromContext(r.Context())
	member := p.members[p.selectMember(r.Context(), sessionID)]
	glog.V(2).Infof("[route] session=%s upstream=%s", sessionID, member.Upstream)
	member.Handler.ServeHTTP(w, r)
}

// selectMember returns the index of the member that serves a request: the
// weighted ring-hash slot for a non-empty session id, or the least-loaded
// member (round-robin among ties) for an empty one.
func (p *upstreamPoolHandler) selectMember(ctx context.Context, sessionID string) int {
	if sessionID != "" {
		return p.pinSlot(ctx, sessionID)
	}
	return p.leastLoaded(ctx)
}

// pinSlot returns the member index that the weighted ring hash of
// sessionID maps to: slot = FNV-1a 64 over the id, mod totalWeight, then
// the first member whose cumulative weight exceeds the slot. The same
// session id always yields the same index — recomputable from the id, no
// session→member map — so the session's upstream prompt cache stays warm
// on that server across restarts.
func (p *upstreamPoolHandler) pinSlot(ctx context.Context, sessionID string) int {
	h := fnv.New64a()
	_, _ = h.Write([]byte(sessionID))
	// #nosec G115 -- totalWeight and cumulative are sums of config-validated
	// positive weights, far below uint64 max; the int→uint64 conversion is
	// overflow-safe.
	slot := h.Sum64() % uint64(p.totalWeight)
	for i, c := range p.cumulative {
		select {
		case <-ctx.Done():
			return 0
		default:
		}
		// #nosec G115 -- see above: cumulative values are positive weight
		// sums, overflow-safe when widened.
		if uint64(c) > slot {
			return i
		}
	}
	return len(p.members) - 1
}

// leastLoaded returns the index of the member with the fewest in-flight
// requests, breaking ties round-robin (the atomic rr counter, cycled by
// the tie count) so equally-loaded members share keyless traffic instead
// of stacking on the first-declared one.
func (p *upstreamPoolHandler) leastLoaded(ctx context.Context) int {
	min := p.inFlight(0)
	for i := 1; i < len(p.members); i++ {
		select {
		case <-ctx.Done():
			return 0
		default:
		}
		if load := p.inFlight(i); load < min {
			min = load
		}
	}
	ties := make([]int, 0, len(p.members))
	for i := range p.members {
		select {
		case <-ctx.Done():
			return 0
		default:
		}
		if p.inFlight(i) == min {
			ties = append(ties, i)
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

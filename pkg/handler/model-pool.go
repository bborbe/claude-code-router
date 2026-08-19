// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"hash/fnv"
	"net/http"
	"sync/atomic"
)

// ModelPoolMember is one candidate of a model pool at runtime: the
// provider to route through, the fixed concrete model string that
// provider sees, the weight for session-pinned member selection, and
// whether the member may overflow to a sibling when its provider is
// saturated. Handler is the member provider's request handler (its
// upstream pool handler). InFlight reports the provider's current
// in-flight load; may be nil, meaning "always 0". Saturated reports
// whether the provider is at capacity; may be nil, meaning "never
// saturated". The factory fills all of these from config (spec 013).
type ModelPoolMember struct {
	Provider  string
	Model     string
	Weight    int
	Overflow  bool
	Handler   http.Handler
	InFlight  func() int
	Saturated func() bool
}

// ModelPool is a runtime model pool: an ordered list of members and the
// precomputed cumulative-weight ring used to pin a session id to one
// member. The only mutable state is the round-robin tie-break counter
// for idless dispatch; session pinning itself is stateless.
type ModelPool struct {
	members     []ModelPoolMember
	cumulative  []int
	totalWeight int
	// rr is the idless round-robin tie-break counter. It is the only
	// mutable state in the pool; pinning is stateless and recomputable
	// from the session id.
	rr atomic.Uint64
}

// NewModelPool returns a pool over members. Config validation (spec 013
// prompt 1) guarantees members is non-empty and every weight is
// positive; like the upstream pool handler (spec 012), no defensive
// validation is repeated here.
func NewModelPool(members []ModelPoolMember) *ModelPool {
	cumulative := make([]int, len(members))
	total := 0
	for i, m := range members {
		total += m.Weight
		cumulative[i] = total
	}
	return &ModelPool{
		members:     members,
		cumulative:  cumulative,
		totalWeight: total,
	}
}

// Resolve selects the member that serves one request for this pool.
// A request carrying a non-empty session id (from the session
// middleware's context) is pinned to the same member every time via a
// weighted ring hash of the id — deterministic and stateless,
// recomputable from the id, no session->member map — so the session's
// prompt cache stays warm on that member. A request with an empty
// session id goes to the least-loaded member, with round-robin
// tie-breaking among equally-loaded members so an idless burst
// spreads instead of stacking on the first member. When the pinned
// member's provider is saturated (Saturated returns true) and the
// member declares Overflow, the request falls over to the
// least-loaded sibling member (declaration-order tie-break) so
// availability wins over cache warmth; otherwise the pinned member is
// served and its provider's own concurrency semantics apply (spec 013
// DB 4/5).
func (p *ModelPool) Resolve(sessionID string) ModelPoolMember {
	if sessionID == "" {
		return p.members[p.leastLoaded()]
	}
	pinned := p.pinSlot(sessionID)
	member := p.members[pinned]
	if member.Overflow && member.Saturated != nil && member.Saturated() {
		return p.members[p.overflowTarget(pinned)]
	}
	return member
}

// pinSlot returns the member index that the weighted ring hash of
// sessionID maps to: slot = FNV-1a 64 over the id, mod totalWeight, then
// the first member whose cumulative weight exceeds the slot. The same
// mechanism the upstream pool handler uses (spec 012), so a fixed
// session id over a fixed pool is stable across requests and restarts.
func (p *ModelPool) pinSlot(sessionID string) int {
	h := fnv.New64a()
	_, _ = h.Write([]byte(sessionID))
	// #nosec G115 -- totalWeight and cumulative are sums of config-validated
	// positive weights, far below uint64 max; the int→uint64 conversion is
	// overflow-safe.
	slot := h.Sum64() % uint64(p.totalWeight)
	for i, c := range p.cumulative {
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
// the tie count) so equally-loaded members share idless traffic instead
// of stacking on the first-declared one.
func (p *ModelPool) leastLoaded() int {
	min := p.load(0)
	for i := 1; i < len(p.members); i++ {
		if load := p.load(i); load < min {
			min = load
		}
	}
	ties := make([]int, 0, len(p.members))
	for i := range p.members {
		if p.load(i) == min {
			ties = append(ties, i)
		}
	}
	rr := p.rr.Add(1)
	return ties[(rr-1)%uint64(len(ties))]
}

// overflowTarget returns the index of the least-loaded member among all
// members EXCEPT excluded, breaking ties by declaration order (stable
// and deterministic — the first-declared lowest-load member wins). With
// no sibling (a single-member pool declaring overflow) there is nowhere
// to overflow to, so the excluded member itself is returned and the
// pinned member's own provider semantics apply.
func (p *ModelPool) overflowTarget(excluded int) int {
	minIdx := excluded
	minLoad := 0
	for i := range p.members {
		if i == excluded {
			continue
		}
		load := p.load(i)
		if minIdx == excluded || load < minLoad {
			minIdx = i
			minLoad = load
		}
	}
	return minIdx
}

// load returns member i's current in-flight count, treating a nil
// InFlight (an uncapped provider — the production default) as 0.
func (p *ModelPool) load(i int) int {
	if f := p.members[i].InFlight; f != nil {
		return f()
	}
	return 0
}

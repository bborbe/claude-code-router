// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("upstream member index slot", func() {
	It(
		"is per-request: concurrent dispatches each publish their own member index (spec 016 AC 4)",
		func() {
			// Each member records the session ids it served, so the test can
			// assert that every request's slot index matches the member that
			// actually served that request — no cross-request bleed. Run under
			// `go test -race` this is the concurrency guard for the slot: a
			// process-global (shared) index would corrupt the results or trip
			// the race detector here.
			var mu sync.Mutex
			servedBy := map[string]int{} // session -> member index
			record := func(idx int) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					servedBy[SessionIDFromContext(r.Context())] = idx
					mu.Unlock()
					w.WriteHeader(http.StatusOK)
				})
			}
			members := []UpstreamMember{
				{Upstream: "https://a", Handler: record(0), Weight: 1},
				{Upstream: "https://b", Handler: record(1), Weight: 1},
			}
			pool := NewUpstreamPoolHandler(context.Background(), members)

			const n = 50
			results := make([]int, n)
			var wg sync.WaitGroup
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					session := fmt.Sprintf("sess-%d", i)
					slot := &upstreamIndexSlot{}
					req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
					req = req.WithContext(ContextWithUpstreamIndex(
						ContextWithSessionID(req.Context(), session), slot))
					pool.ServeHTTP(httptest.NewRecorder(), req)
					results[i] = slot.index
				}(i)
			}
			wg.Wait()

			mu.Lock()
			defer mu.Unlock()
			for i := 0; i < n; i++ {
				session := fmt.Sprintf("sess-%d", i)
				Expect(results[i]).To(Equal(servedBy[session]),
					"request %s published index %d but member %d served it",
					session, results[i], servedBy[session])
			}
		},
	)
})

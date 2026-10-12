// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	stdtime "time"

	libtime "github.com/bborbe/time"
	libtimemocks "github.com/bborbe/time/mocks"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	pkg "github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/handler"
)

// Hand-over at a window switch: a request queued for a member's slot when
// the pool's eligible set changes is handed back to the pool and selected
// again, instead of waiting for a slot on a member whose window closed.
var _ = Describe("UpstreamPoolHandler hand-over at a window switch", func() {
	const queueWait = 30 * stdtime.Second

	var (
		clock      libtime.CurrentDateTime
		waiter     *libtimemocks.WaiterDuration
		recheck    chan struct{}
		rechecks   *atomic.Int32
		dayRelease chan struct{}
		served     chan string
	)

	// lane returns an upstream stand-in that reports its name on served;
	// release, when non-nil, holds the request until it is closed.
	lane := func(name string, release chan struct{}) http.Handler {
		out := served // captured per spec: a request left over from a spec never sees the next one's channel
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if release != nil {
				select {
				case <-release:
				case <-r.Context().Done():
				}
			}
			out <- name
			w.WriteHeader(http.StatusOK)
		})
	}
	// member wraps next in a cap-1 member limiter timed by the waiter mock.
	member := func(name string, next http.Handler, window *pkg.Window) handler.UpstreamMember {
		limiter := handler.NewConcurrencyLimiter(next, "p", 1, queueWait, waiter, clock.Now, nil)
		counter, ok := limiter.(interface{ InFlight() int })
		Expect(ok).To(BeTrue(), "a capped member limiter reports InFlight")
		return handler.UpstreamMember{
			Upstream: name,
			Handler:  limiter,
			Weight:   1,
			InFlight: counter.InFlight,
			Window:   window,
			Now:      clock.Now,
		}
	}
	dayWindow := func() *pkg.Window {
		return &pkg.Window{
			From:  mustTOD("08:00 Europe/Berlin"),
			Until: mustTOD("18:00 Europe/Berlin"),
		}
	}
	nightWindow := func() *pkg.Window {
		return &pkg.Window{
			From:  mustTOD("18:00 Europe/Berlin"),
			Until: mustTOD("08:00 Europe/Berlin"),
		}
	}
	setBerlin := func(hour, minute int) {
		clock.SetNow(
			libtime.DateTime(stdtime.Date(2026, stdtime.October, 12, hour, minute, 0, 0, berlin)),
		)
	}
	serve := func(h http.Handler) chan struct{} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			h.ServeHTTP(
				httptest.NewRecorder(),
				httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
			)
		}()
		return done
	}

	BeforeEach(func() {
		clock = libtime.NewCurrentDateTime()
		setBerlin(17, 59)
		recheck = make(chan struct{})
		rechecks = &atomic.Int32{}
		dayRelease = make(chan struct{})
		served = make(chan string, 4)
		// The queue timeout (queueWait) never fires in these specs; the
		// eligible-set re-check (any other duration) ends only when the spec
		// sends on recheck.
		tick, count := recheck, rechecks
		waiter = &libtimemocks.WaiterDuration{}
		waiter.WaitStub = func(ctx context.Context, d libtime.Duration) error {
			if d == libtime.Duration(queueWait) {
				<-ctx.Done()
				return ctx.Err()
			}
			count.Add(1)
			select {
			case <-tick:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})
	AfterEach(func() {
		select {
		case <-dayRelease:
		default:
			close(dayRelease)
		}
	})

	It("moves a request queued on the day member to the night member at 18:00", func() {
		pool := handler.NewUpstreamPoolHandler(context.Background(), []handler.UpstreamMember{
			member("day", lane("day", dayRelease), dayWindow()),
			member("night", lane("night", nil), nightWindow()),
		})
		first := serve(pool)
		second := serve(pool) // the day member's single slot is taken: one of the two queues
		Eventually(rechecks.Load).Should(Equal(int32(1)))

		setBerlin(18, 0)
		recheck <- struct{}{}

		Eventually(served).Should(Receive(Equal("night")))
		// Exactly one finished — the queued one, on the night member — while
		// the other still holds its day slot.
		Eventually(
			func() int { return closedCount([]chan struct{}{first, second}) },
		).Should(Equal(1))
		Consistently(
			func() int { return closedCount([]chan struct{}{first, second}) },
			"50ms",
		).Should(Equal(1))
	})

	It("keeps a request queued while the eligible set is unchanged", func() {
		pool := handler.NewUpstreamPoolHandler(context.Background(), []handler.UpstreamMember{
			member("day", lane("day", dayRelease), dayWindow()),
			member("night", lane("night", nil), nightWindow()),
		})
		serve(pool)
		queued := serve(pool)
		Eventually(rechecks.Load).Should(Equal(int32(1)))

		recheck <- struct{}{}

		Eventually(rechecks.Load).Should(Equal(int32(2)))
		Consistently(queued, "50ms").ShouldNot(BeClosed())
		close(dayRelease)
		Eventually(served).Should(Receive(Equal("day")))
		Eventually(served).Should(Receive(Equal("day")))
		Eventually(queued).Should(BeClosed())
	})

	It("never re-checks in a pool without windows or days", func() {
		pool := handler.NewUpstreamPoolHandler(context.Background(), []handler.UpstreamMember{
			member("only", lane("only", dayRelease), nil),
		})
		serve(pool)
		queued := serve(pool)
		Eventually(waiter.WaitCallCount).Should(Equal(1)) // the queue timeout only
		Consistently(rechecks.Load, "50ms").Should(Equal(int32(0)))
		close(dayRelease)
		Eventually(queued).Should(BeClosed())
	})

	It("never hands back a request waiting for a host slot", func() {
		host := handler.NewHostLimiter(1, queueWait, waiter)
		day := member("day", lane("day", dayRelease), dayWindow())
		day.Handler = host.Wrap(day.Handler, "p", nil)
		night := member("night", lane("night", nil), nightWindow())
		night.Handler = host.Wrap(night.Handler, "p", nil)
		pool := handler.NewUpstreamPoolHandler(
			context.Background(),
			[]handler.UpstreamMember{day, night},
		)
		serve(pool)
		queued := serve(
			pool,
		) // the night member is closed, so it waits for the day member's host slot
		Eventually(waiter.WaitCallCount).Should(Equal(1))
		Consistently(rechecks.Load, "50ms").Should(Equal(int32(0)))
		close(dayRelease)
		Eventually(queued).Should(BeClosed())
	})
})

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"math"
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"

	pkg "github.com/bborbe/claude-code-router/pkg"
	"github.com/bborbe/claude-code-router/pkg/handler"
)

var _ = Describe("ProviderLimit", func() {
	var clock libtime.CurrentDateTime

	// weekdayPool is the live three-member shape: weekday day 08-18, weekday
	// night 18-08, weekend all day, each member reading the shared clock.
	weekdayPool := func() []handler.UpstreamMember {
		weekdays := "monday, tuesday, wednesday, thursday, friday"
		return []handler.UpstreamMember{
			{
				Upstream: "day",
				Days:     mustDays(weekdays),
				Window: &pkg.Window{
					From:  mustTOD("08:00 Europe/Berlin"),
					Until: mustTOD("18:00 Europe/Berlin"),
				},
				Now: clock.Now,
			},
			{
				Upstream: "night",
				Days:     mustDays(weekdays),
				Window: &pkg.Window{
					From:  mustTOD("18:00 Europe/Berlin"),
					Until: mustTOD("08:00 Europe/Berlin"),
				},
				Now: clock.Now,
			},
			{Upstream: "weekend", Days: mustDays("saturday, sunday Europe/Berlin"), Now: clock.Now},
		}
	}
	setBerlin := func(day, hour, minute int) {
		clock.SetNow(
			libtime.DateTime(stdtime.Date(2026, stdtime.October, day, hour, minute, 0, 0, berlin)),
		)
	}

	BeforeEach(func() {
		clock = libtime.NewCurrentDateTime()
	})

	DescribeTable("sums the caps of the members eligible right now",
		func(day, hour, minute int, want float64) {
			setBerlin(day, hour, minute)
			Expect(handler.ProviderLimit(weekdayPool(), []int{16, 25, 25})).To(Equal(want))
		},
		Entry("Mon 17:59 → day member's 16", 12, 17, 59, 16.0),
		Entry("Mon 18:00 → night member's 25", 12, 18, 0, 25.0),
		Entry("Sat 12:00 → weekend member's 25", 17, 12, 0, 25.0),
	)

	It("adds up several simultaneously eligible members", func() {
		members := []handler.UpstreamMember{{Upstream: "a"}, {Upstream: "b"}}
		Expect(handler.ProviderLimit(members, []int{8, 4})).To(Equal(12.0))
	})

	It("is +Inf when an eligible member is uncapped", func() {
		members := []handler.UpstreamMember{{Upstream: "a"}, {Upstream: "b"}}
		Expect(math.IsInf(handler.ProviderLimit(members, []int{8, 0}), 1)).To(BeTrue())
	})

	It("ignores an uncapped member while it is not eligible", func() {
		setBerlin(12, 10, 0) // Mon 10:00 — only the day member is eligible
		Expect(handler.ProviderLimit(weekdayPool(), []int{16, 0, 0})).To(Equal(16.0))
	})

	It("is 0 when no member is eligible", func() {
		setBerlin(12, 10, 0) // Mon 10:00 — the weekend member alone is closed
		weekendOnly := weekdayPool()[2:]
		Expect(handler.ProviderLimit(weekendOnly, []int{25})).To(Equal(0.0))
	})
})

var _ = Describe("ProviderLimitCollector", func() {
	It("re-evaluates the limit on every scrape", func() {
		limit := 16.0
		c := handler.NewProviderLimitCollector(map[string]func() float64{
			"seibert-vllm-default": func() float64 { return limit },
		})
		Expect(testutil.ToFloat64(c)).To(Equal(16.0))
		limit = 25
		Expect(testutil.ToFloat64(c)).To(Equal(25.0))
	})
})

var _ = Describe("UpstreamLimitCollector", func() {
	It("reports a capped host's budget", func() {
		c := handler.NewUpstreamLimitCollector(map[string]*handler.HostLimiter{
			"vllm.seibert.tools": handler.NewHostLimiter(25, stdtime.Second, nil),
		})
		Expect(testutil.ToFloat64(c)).To(Equal(25.0))
	})

	It("reports +Inf for an uncapped host", func() {
		c := handler.NewUpstreamLimitCollector(map[string]*handler.HostLimiter{
			"127.0.0.1:8317": handler.NewHostLimiter(0, stdtime.Second, nil),
		})
		Expect(math.IsInf(testutil.ToFloat64(c), 1)).To(BeTrue())
	})
})

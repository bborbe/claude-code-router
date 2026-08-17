// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

var _ = Describe("IsLoopbackRemoteAddr", func() {
	DescribeTable("classifies remote addresses",
		func(addr string, loopback bool) {
			Expect(handler.IsLoopbackRemoteAddr(addr)).To(Equal(loopback))
		},
		Entry("IPv4 loopback", "127.0.0.1:1234", true),
		Entry("IPv4 loopback /8", "127.1.2.3:1234", true),
		Entry("IPv6 loopback", "[::1]:1234", true),
		Entry("private IPv4", "10.0.0.1:1234", false),
		Entry("IPv6 unique local", "[2001:db8::1]:1234", false),
		Entry("empty string", "", false),
		Entry("not an address", "not-an-address", false),
	)
})

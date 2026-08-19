// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

var _ = Describe("SessionID context", func() {
	It("round-trips a session id through the context", func() {
		ctx := handler.ContextWithSessionID(context.Background(), "sess-1")
		Expect(handler.SessionIDFromContext(ctx)).To(Equal("sess-1"))
	})

	It("returns an empty string when no session id is stored", func() {
		Expect(handler.SessionIDFromContext(context.Background())).To(Equal(""))
	})

	It("is not polluted by a different context value", func() {
		ctx := handler.ContextWithPresentedApiKey(context.Background(), "key-1")
		Expect(handler.SessionIDFromContext(ctx)).To(Equal(""))
	})
})

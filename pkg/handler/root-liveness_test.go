// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

var _ = Describe("RootLivenessHandler", func() {
	It("returns 200 OK with empty body", func() {
		h := handler.NewRootLivenessHandler()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodHead, "/", nil)

		h.ServeHTTP(rec, req)

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.Len()).To(Equal(0))
	})
})

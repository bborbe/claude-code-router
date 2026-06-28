// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"flag"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

var _ = Describe("NotFoundHandler", func() {
	It("returns 404 with the stdlib not-found body", func() {
		h := handler.NewNotFoundHandler()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/foo/bar", nil)

		h.ServeHTTP(rec, req)

		Expect(rec.Code).To(Equal(http.StatusNotFound))
		Expect(rec.Body.String()).To(ContainSubstring("404 page not found"))
	})

	It("logs the unknown path at V(1)", func() {
		_ = flag.Set("logtostderr", "true")
		_ = flag.Set("v", "1")
		h := handler.NewNotFoundHandler()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/some/unknown", nil)

		out := captureStderr(func() {
			h.ServeHTTP(rec, req)
		})

		Expect(out).To(ContainSubstring("[404] POST /some/unknown"))
	})
})

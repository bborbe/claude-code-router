// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"io"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

var _ = Describe("HealthzHandler", func() {
	It("returns 200 OK", func() {
		h := handler.NewHealthzHandler()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

		h.ServeHTTP(rec, req)

		Expect(rec.Code).To(Equal(http.StatusOK))
		body, err := io.ReadAll(rec.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).To(Equal("OK"))
	})
})

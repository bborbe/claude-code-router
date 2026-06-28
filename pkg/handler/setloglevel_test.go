// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"flag"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

var _ = Describe("SetLoglevelHandler", func() {
	var h http.Handler

	BeforeEach(func() {
		h = handler.NewSetLoglevelHandler()
	})

	It("returns 200 and confirms the level on a valid integer", func() {
		req := httptest.NewRequest(http.MethodGet, "/setloglevel/3", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(ContainSubstring("set loglevel to 3 completed"))
	})

	It("actually flips glog's -v flag", func() {
		req := httptest.NewRequest(http.MethodGet, "/setloglevel/4", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		Expect(flag.Lookup("v").Value.String()).To(Equal("4"))
	})

	It("returns 400 on a non-integer level", func() {
		req := httptest.NewRequest(http.MethodGet, "/setloglevel/banana", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
		Expect(rec.Body.String()).To(ContainSubstring("parse loglevel failed"))
	})

	It("returns 400 on an empty level (trailing slash only)", func() {
		req := httptest.NewRequest(http.MethodGet, "/setloglevel/", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
	})

	It("handles longer paths if the suffix is numeric", func() {
		// stdlib /setloglevel/ pattern strips nothing — full URL.Path
		// reaches the handler. TrimPrefix removes "/setloglevel/" so
		// "/setloglevel/2" yields "2".
		req := httptest.NewRequest(http.MethodGet, "/setloglevel/2", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(strings.TrimSpace(rec.Body.String())).To(Equal("set loglevel to 2 completed"))
	})
})

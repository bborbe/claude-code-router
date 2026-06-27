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

var _ = Describe("LoggingHandler", func() {
	var (
		rec *httptest.ResponseRecorder
		req *http.Request
	)

	BeforeEach(func() {
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, "/anything", nil)
	})

	It("captures explicit WriteHeader status (404)", func() {
		next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})

		handler.NewLoggingHandler(next).ServeHTTP(rec, req)

		Expect(rec.Code).To(Equal(http.StatusNotFound))
	})

	It("captures implicit 200 when handler calls Write without WriteHeader", func() {
		next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("OK"))
		})

		handler.NewLoggingHandler(next).ServeHTTP(rec, req)

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(Equal("OK"))
	})

	It("captures 500 from explicit WriteHeader", func() {
		next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
		})

		handler.NewLoggingHandler(next).ServeHTTP(rec, req)

		Expect(rec.Code).To(Equal(http.StatusInternalServerError))
		Expect(rec.Body.String()).To(Equal("boom"))
	})

	It("ignores duplicate WriteHeader calls (keeps the first)", func() {
		next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			w.WriteHeader(http.StatusTeapot) // duplicate; net/http would warn, we ignore
		})

		handler.NewLoggingHandler(next).ServeHTTP(rec, req)

		Expect(rec.Code).To(Equal(http.StatusAccepted))
	})

	It("works when handler writes nothing (default 200)", func() {
		next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})

		handler.NewLoggingHandler(next).ServeHTTP(rec, req)

		Expect(rec.Code).To(Equal(http.StatusOK))
	})
})

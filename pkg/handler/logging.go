// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"net/http"

	"github.com/golang/glog"
)

// NewLoggingHandler wraps next and emits one glog line per request:
// `[req] METHOD path -> STATUS` at V(1). Used to make router activity
// visible during local testing without depending on the upstream
// provider's own logging.
func NewLoggingHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		status := rec.status
		if status == 0 {
			// Handler returned without writing — match net/http's default.
			status = http.StatusOK
		}
		glog.V(1).Infof("[req] %s %s -> %d", r.Method, r.URL.Path, status)
	})
}

// statusRecorder captures the response status code so the wrapping
// logger can include it in the log line. Both WriteHeader and Write
// are overridden — Write triggers an implicit WriteHeader(200) per
// the http.ResponseWriter contract, so without overriding Write the
// status would be missed for handlers that call Write directly
// (e.g. libhttp.NewPrintHandler).
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
		s.ResponseWriter.WriteHeader(code)
	}
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.WriteHeader(http.StatusOK)
	}
	return s.ResponseWriter.Write(b)
}

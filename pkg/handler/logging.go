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
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		glog.V(1).Infof("[req] %s %s -> %d", r.Method, r.URL.Path, rec.status)
	})
}

// statusRecorder captures the response status code so the wrapping
// logger can include it in the log line.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

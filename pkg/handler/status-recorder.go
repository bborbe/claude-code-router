// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import "net/http"

// statusRecorder captures the response status code so the wrapping
// logger can include it in its log line. Both WriteHeader and Write
// are overridden — Write triggers an implicit WriteHeader(200) per
// the http.ResponseWriter contract, so without overriding Write the
// status would be missed for handlers that call Write directly.
//
// Unwrap exposes the underlying ResponseWriter to
// `http.NewResponseController` so SSE-streaming proxies
// (httputil.ReverseProxy via libhttp.NewProxy) can reach Flush /
// Hijack / SetReadDeadline / SetWriteDeadline through the wrapper.
// Without Unwrap, Anthropic's SSE chunks pile up in an intermediate
// buffer instead of flushing to the client per chunk — symptom is
// Claude Code spinners "stuck" mid-stream and `/compact` appearing
// to hang at 95%; bytes arrive eventually, just all at once when
// the response closes.
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

// Unwrap returns the underlying ResponseWriter so
// `http.NewResponseController` (Go 1.20+) can recursively reach
// Flusher / Hijacker / deadline-setter implementations on the original
// writer. Required for SSE flush to pass through the wrapper.
func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

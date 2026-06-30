// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/golang/glog"
)

// traceResponse captures response data while delegating all writes
// to the underlying http.ResponseWriter. It implements Unwrap() so
// http.NewResponseController can reach Flusher/Hijacker through the
// wrapper — required for SSE streaming through the model router's
// proxy to flush per chunk instead of buffering.
type traceResponse struct {
	http.ResponseWriter
	status         int
	wroteHeader    bool
	headerCaptured bool
	headers        http.Header
	body           *bytes.Buffer
}

func (t *traceResponse) WriteHeader(code int) {
	if !t.wroteHeader {
		t.status = code
		t.wroteHeader = true
		t.headerCaptured = true
		t.headers = t.ResponseWriter.Header().Clone()
		t.ResponseWriter.WriteHeader(code)
	}
}

func (t *traceResponse) Write(b []byte) (int, error) {
	if !t.wroteHeader {
		t.WriteHeader(http.StatusOK)
	}
	if t.body != nil {
		t.body.Write(b)
	}
	return t.ResponseWriter.Write(b)
}

// Unwrap returns the underlying ResponseWriter so
// http.NewResponseController (Go 1.20+) can recursively reach
// Flusher/Hijacker/deadline-setter on the original writer.
// Required for SSE flush to pass through the trace wrapper.
func (t *traceResponse) Unwrap() http.ResponseWriter {
	return t.ResponseWriter
}

// requestIDCounter generates unique request IDs using atomic increment.
// A random hex suffix is appended to handle the unlikely collision
// when two requests land in the same nanosecond on the same counter value.
var requestIDCounter uint64

func nextRequestID() string {
	// Atomic increment for uniqueness under concurrency.
	id := atomic.AddUint64(&requestIDCounter, 1)
	// 8 bytes of random data for the unlikely case of a counter
	// overflow or same-nanosecond collision.
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	return fmt.Sprintf("%d-%s", id, hex.EncodeToString(rnd[:]))
}

// NewTraceMiddleware wraps next in a handler that, for each request,
// captures the full request (method, path, headers, body) and full
// response (status, headers, body) and writes one JSON file to
// traceDir. Authorization and x-api-key request headers are redacted
// to "***" (case-insensitive header lookup); all other headers and
// the entire request/response bodies are logged verbatim. Trace file
// writes are best-effort: a write failure is logged at glog.Warningf
// and the request still succeeds. The trace directory is created on
// demand on the first write (MkdirAll, 0o700).
func NewTraceMiddleware(next http.Handler, traceDir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture request headers (with redaction) and body.
		// Flat map[string]string: http.Header iteration returns canonical keys
		// (Content-Type not content-type) — store verbatim so jq literal-key
		// lookups in AC #5 work. Multi-value headers joined with ", ".
		// Redaction is case-insensitive: Authorization/X-Api-Key checked via
		// lower-cased name regardless of stored key case.
		reqHeaders := make(map[string]string, len(r.Header))
		for name, vals := range r.Header {
			if strings.ToLower(name) == "authorization" || strings.ToLower(name) == "x-api-key" {
				reqHeaders[name] = "***"
			} else {
				reqHeaders[name] = strings.Join(vals, ", ")
			}
		}

		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))

		// Wrap response writer to capture status and body.
		rec := &traceResponse{
			ResponseWriter: w,
			headers:        nil,
			body:           bytes.NewBuffer(nil),
		}

		// Dispatch to the wrapped handler.
		next.ServeHTTP(rec, r)

		// Collect response data after handler returns.
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		respHeaders := http.Header{}
		if rec.headerCaptured && rec.headers != nil {
			respHeaders = rec.headers
		}
		respBody := ""
		if rec.body != nil {
			respBody = rec.body.String()
		}

		trace := map[string]any{
			"request": map[string]any{
				"method":  r.Method,
				"path":    r.URL.Path,
				"headers": reqHeaders,
				"body":    string(body),
			},
			"response": map[string]any{
				"status":  status,
				"headers": respHeaders,
				"body":    respBody,
			},
		}

		filename := fmt.Sprintf("%s-%s.json", formatTimestampNano(), nextRequestID())
		writeTraceFile(traceDir, filename, trace)
	})
}

// traceTimestampFormat is the time.Format layout for trace filenames.
// Uses dash-separated components and zero-padded fields so filenames
// sort chronologically. No colons (RFC3339 uses colons) to stay
// filesystem-legal on all platforms.
const traceTimestampFormat = "2006-01-02-150405.000000000"

// formatTimestampNano returns a sortable timestamp with nanosecond
// resolution using filesystem-legal characters (no colons).
func formatTimestampNano() string {
	return time.Now().Format(traceTimestampFormat)
}

// writeTraceFile creates the trace directory if needed and writes the
// JSON trace file. Errors are best-effort: a failure logs a warning and
// leaves the request to succeed normally.
func writeTraceFile(traceDir, filename string, trace map[string]any) {
	if err := os.MkdirAll(traceDir, 0o700); err != nil {
		glog.Warningf("trace dir create failed: %v", err)
		return
	}
	data, err := json.MarshalIndent(trace, "", "  ")
	if err != nil {
		glog.Warningf("trace json marshal failed: %v", err)
		return
	}
	if err := os.WriteFile(filepath.Join(traceDir, filename), data, 0o600); err != nil {
		glog.Warningf("trace file write failed: %v", err)
	}
}

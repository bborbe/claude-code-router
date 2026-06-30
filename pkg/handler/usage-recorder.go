// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import "net/http"

// TailBufferBytes caps the number of response-body bytes retained for
// post-request usage extraction. The terminal Anthropic SSE
// `message_delta` event (which carries usage) is always the last chunk
// of a stream, and non-streaming JSON usage bodies fit comfortably
// within this bound; a full 5 MB streaming response therefore occupies
// at most TailBufferBytes of additional memory. This is a frozen
// constant, NOT a config field — see spec 004 Non-goals.
const TailBufferBytes = 64 << 10 // 64 KB

// usageRecorder wraps a *statusRecorder and tees every byte written to
// the response into a bounded tail buffer (≤ TailBufferBytes) that
// retains the LAST bytes written. The buffer is read by
// extractUsage (see prompt 2) after the upstream handler returns, to
// pull input/output token counts out of the terminal SSE
// `message_delta` event or the non-streaming JSON `usage` object.
//
// The write-through path is unchanged: every Write call writes to the
// underlying statusRecorder first and copies to the buffer as a side
// effect, so SSE chunks flush to the client at the same cadence as
// before (no added buffering latency). The buffer never holds more
// than TailBufferBytes; older bytes are evicted as new bytes arrive.
//
// Unwrap returns the wrapped *statusRecorder so
// http.NewResponseController reaches the underlying Flusher / Hijacker
// through the existing statusRecorder.Unwrap() chain. Breaking this
// chain regresses SSE flush (Claude Code spinners "stuck" mid-stream)
// — see status-recorder.go doc comment and the spec's "Unwrap() chain
// must stay functional" constraint.
type usageRecorder struct {
	rec  *statusRecorder
	tail tailBuffer
}

// Tail returns a copy of the bytes currently retained in the tail
// buffer (the last ≤ TailBufferBytes bytes written), in write order.
// Called by extractUsage (prompt 2) after the upstream handler returns.
// The returned slice does not alias internal storage, so it is safe to
// read after the handler returns even though extraction happens before
// any further Write.
func (u *usageRecorder) Tail() []byte {
	return u.tail.Tail()
}

// newUsageRecorder wraps w in a *statusRecorder (so the existing
// status-capture + Unwrap chain is preserved) and returns a
// *usageRecorder that tees writes into a fresh empty tail buffer.
// The returned value satisfies http.ResponseWriter.
func newUsageRecorder(w http.ResponseWriter) *usageRecorder {
	return &usageRecorder{
		rec:  &statusRecorder{ResponseWriter: w},
		tail: tailBuffer{},
	}
}

// WriteHeader delegates to the wrapped *statusRecorder. The tee does
// not capture the status itself — statusRecorder already does.
func (u *usageRecorder) WriteHeader(code int) {
	u.rec.WriteHeader(code)
}

// Header delegates to the wrapped *statusRecorder. The tee adds no
// header mutation; it only needs to satisfy http.ResponseWriter so
// http.NewResponseController accepts a *usageRecorder directly. The
// map returned by the underlying writer is shared (read access during
// streaming is unchanged — the tee never writes to it).
func (u *usageRecorder) Header() http.Header {
	return u.rec.Header()
}

// Write writes b through to the wrapped *statusRecorder first (the
// client-facing write is the primary path), then copies the bytes
// actually written into the tail buffer as a best-effort side effect.
// On error from the underlying writer, the buffer is not extended —
// extraction is best-effort and must never interfere with the
// write-through to the client. Partial writes (n < len(b)) copy only
// the first n bytes that reached the client. The returned count and
// error come straight from rec.Write (this is a passthrough, not a
// new error condition, so the error is returned unwrapped).
func (u *usageRecorder) Write(b []byte) (int, error) {
	n, err := u.rec.Write(b)
	if n > 0 {
		u.tail.write(b[:n])
	}
	return n, err
}

// Unwrap returns the wrapped *statusRecorder, preserving the
// http.NewResponseController chain:
//
//	usageRecorder.Unwrap() → *statusRecorder → statusRecorder.Unwrap() → underlying ResponseWriter
//
// Do NOT skip the *statusRecorder — its Write/WriteHeader overrides
// (status capture + implicit WriteHeader(200)) must stay in the path.
func (u *usageRecorder) Unwrap() http.ResponseWriter {
	return u.rec
}

// tailBuffer is a fixed-capacity sliding window that retains the last
// ≤ TailBufferBytes bytes written to it. It never grows beyond
// TailBufferBytes regardless of total bytes written: once full, the
// oldest bytes are evicted (front trim) as new bytes arrive, so the
// stored content is always the most recent ≤ TailBufferBytes bytes in
// write order. This is the sliding-window variant noted in the spec's
// open question — chosen for being simple to reason about and test.
type tailBuffer struct {
	buf []byte
}

// write appends b, evicting the oldest bytes first if the result
// would exceed TailBufferBytes. A zero-length write is a no-op.
func (t *tailBuffer) write(b []byte) {
	if len(b) == 0 {
		return
	}
	// Fast path: a single write larger than the bound replaces the
	// whole window with its tail — no point retaining anything older.
	if len(b) >= TailBufferBytes {
		t.buf = append(t.buf[:0], b[len(b)-TailBufferBytes:]...)
		return
	}
	overflow := len(t.buf) + len(b) - TailBufferBytes
	if overflow > 0 {
		t.buf = t.buf[overflow:]
	}
	t.buf = append(t.buf, b...)
}

// Tail returns a copy of the currently retained bytes (the last
// ≤ TailBufferBytes bytes written), in write order. The returned slice
// is safe to read after the handler returns — it does not alias the
// internal storage, so a subsequent Write (there is none during
// extraction, which runs after target.ServeHTTP returns) cannot mutate
// it. Returns nil when nothing has been written.
func (t *tailBuffer) Tail() []byte {
	if len(t.buf) == 0 {
		return nil
	}
	out := make([]byte, len(t.buf))
	copy(out, t.buf)
	return out
}

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

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

// TokenUsage holds the input/output token counts extracted from an
// upstream response body. When extraction fails or no usage is
// present, Input and Output are the empty string and the caller logs
// the sentinel "-" for each (see logLineValue below). The empty
// string is the "no data" signal — a real zero-token count from the
// upstream is reported as "0" (the extractor reports what it parsed).
type TokenUsage struct {
	Input  string
	Output string
}

// noUsage is the sentinel returned when no parseable usage was found.
// Its fields render as "-" in the [req] log line (in=/out=).
//
//nolint:gochecknoglobals // frozen sentinel value, not a config knob
var noUsage = TokenUsage{Input: "-", Output: "-"}

// logLineValue renders a token count for the [req] line: the parsed
// value, or "-" when extraction yielded nothing. Defined next to the
// type it renders for prompt 3's call site (model-router.go).
func (u TokenUsage) logLineValue() (in, out string) {
	if u.Input == "" {
		in = "-"
	} else {
		in = u.Input
	}
	if u.Output == "" {
		out = "-"
	} else {
		out = u.Output
	}
	return in, out
}

// extractUsage pulls input/output token counts out of a response-body
// tail buffer. SSE responses (Content-Type: text/event-stream) are
// scanned for the terminal `message_delta` event whose `usage` field
// carries input_tokens/output_tokens; the terminal event is always the
// last chunk of an Anthropic stream, so it lives in the tail. JSON
// responses are parsed for a top-level `usage` object.
//
// Detection is by Content-Type: strings.Contains(contentType, "text/event-stream")
// is the primary signal because (a) the upstream sets that Content-Type on
// every Anthropic SSE response reliably, and (b) content-scanning for `event:`
// requires partial-line state and is fragile when the tail buffer is truncated
// mid-event. JSON parsing is the fallback for any other content type.
//
// Extraction is best-effort: truncated tails, malformed JSON/SSE, missing usage,
// or zero-token usage all yield the noUsage sentinel ("-" / "-") and never an
// error — the caller's [req] log line must never be aborted by a parse failure.
//
// Presence detection: the extractor first unmarshals the `usage` JSON object
// into json.RawMessage; if that RawMessage is nil or "null", the field is
// treated as absent and noUsage is returned. Otherwise the inner
// input_tokens/output_tokens integers are parsed. This means a present
// `{"input_tokens":0,"output_tokens":0}` is correctly reported as "0"/"0",
// and a missing usage block returns the sentinel.
// ExtractUsage is the exported alias for the package-internal extractUsage.
// It exists so handler_test can call it directly as a pure function.
//
//nolint:revive // intentionally exported for test package access
func ExtractUsage(tail []byte, contentType string) (usage TokenUsage) {
	defer func() {
		if r := recover(); r != nil {
			usage = noUsage
		}
	}()

	// Empty tail: nothing to parse.
	if len(tail) == 0 {
		return noUsage
	}

	if strings.Contains(contentType, "text/event-stream") {
		return extractUsageSSE(tail)
	}
	return extractUsageJSON(tail)
}

// extractUsageSSE scans tail for the LAST occurrence of "event: message_delta"
// then parses the following data line's JSON for the usage block.
func extractUsageSSE(tail []byte) TokenUsage {
	// Find the LAST "event: message_delta" in the buffer.
	eventMarker := []byte("event: message_delta")
	idx := bytes.LastIndex(tail, eventMarker)
	if idx < 0 {
		return noUsage
	}

	// From that position, scan forward for the next "data: " line.
	rest := tail[idx:]
	dataIdx := bytes.Index(rest, []byte("\ndata: "))
	if dataIdx < 0 {
		// Also try \r\n variant used by some SSE emitters.
		dataIdx = bytes.Index(rest, []byte("\r\ndata: "))
		if dataIdx < 0 {
			return noUsage
		}
		// Adjust to point past the \r\n prefix so the offset is correct.
		dataIdx += 2 // skip \r\n
	}
	// dataIdx is relative to rest; advance past the "\ndata: " prefix (2 bytes for \n + 6 for "data: ").
	dataStart := idx + dataIdx + len("\ndata: ")
	if dataStart >= len(tail) {
		return noUsage
	}

	// Find the end of the data line: double newline (event block terminator).
	lineEnd := bytes.Index(tail[dataStart:], []byte("\n\n"))
	if lineEnd < 0 {
		// No double newline yet; the event may be truncated.
		return noUsage
	}
	dataBytes := tail[dataStart : dataStart+lineEnd]

	// Parse the SSE data payload: first check if "usage" is present and non-null.
	var usageCheck struct {
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(dataBytes, &usageCheck); err != nil {
		return noUsage
	}
	if usageCheck.Usage == nil || bytes.Equal(usageCheck.Usage, []byte("null")) {
		return noUsage
	}

	// Parse the usage integers.
	var usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	}
	if err := json.Unmarshal(usageCheck.Usage, &usage); err != nil {
		return noUsage
	}

	return TokenUsage{
		Input:  strconv.Itoa(usage.InputTokens),
		Output: strconv.Itoa(usage.OutputTokens),
	}
}

// extractUsageJSON parses tail as a JSON object with a top-level `usage` field.
func extractUsageJSON(tail []byte) TokenUsage {
	// First pass: check for a top-level "usage" key and that it's non-null.
	var usageCheck struct {
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(tail, &usageCheck); err != nil {
		return noUsage
	}
	if usageCheck.Usage == nil || bytes.Equal(usageCheck.Usage, []byte("null")) {
		return noUsage
	}

	// Second pass: parse the inner input_tokens/output_tokens.
	var usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	}
	if err := json.Unmarshal(usageCheck.Usage, &usage); err != nil {
		return noUsage
	}

	return TokenUsage{
		Input:  strconv.Itoa(usage.InputTokens),
		Output: strconv.Itoa(usage.OutputTokens),
	}
}

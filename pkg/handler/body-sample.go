// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"bytes"
	"io"
	"net/http"
)

// BodySampleMaxBytes is the maximum number of bytes captured from a request
// or response body for V(4) body-sample logging.  4 KB is large enough to
// see the full JSON envelope of any ordinary Anthropic /messages request
// while staying small enough to avoid log-line bloat for large streaming
// bodies.
const BodySampleMaxBytes = 4 << 10 // 4 KB

// snippet holds a prefix read from a request body together with the
// number of bytes captured (≤ BodySampleMaxBytes).
type snippet struct {
	head     []byte
	totalLen int
}

// readSnippet reads up to max bytes from req.Body and restores req.Body so
// the inner RoundTripper still sees the complete payload.  It does this by
// MultiReader-ing the captured bytes back in front of the original
// ReadCloser.  Returns a zero snippet when req.Body is nil.
func readSnippet(req *http.Request, max int) snippet {
	if req.Body == nil {
		return snippet{}
	}
	limited := io.LimitReader(req.Body, int64(max))
	head, _ := io.ReadAll(limited)
	// Restore: replay the captured prefix then continue with the original body.
	req.Body = io.NopCloser(io.MultiReader(bytes.NewReader(head), req.Body))
	return snippet{head: head, totalLen: len(head)}
}

// teeBody wraps an io.ReadCloser and captures up to max bytes while passing
// all data through to callers unchanged.  When Close is called, onClose is
// invoked with the captured prefix and the total number of bytes that passed
// through all Read calls (which equals the full body length once the caller
// has drained the body).
type teeBody struct {
	rc      io.ReadCloser
	max     int
	buf     bytes.Buffer
	total   int
	onClose func([]byte, int)
}

// newTeeBody wraps rc.  onClose is called exactly once on the first Close,
// with (captured_prefix, total_bytes_read_through_all_Read_calls).
func newTeeBody(rc io.ReadCloser, max int, onClose func([]byte, int)) io.ReadCloser {
	return &teeBody{rc: rc, max: max, onClose: onClose}
}

func (t *teeBody) Read(p []byte) (int, error) {
	n, err := t.rc.Read(p)
	t.total += n
	if remaining := t.max - t.buf.Len(); remaining > 0 && n > 0 {
		take := n
		if take > remaining {
			take = remaining
		}
		t.buf.Write(p[:take])
	}
	return n, err
}

// Close closes the inner ReadCloser and then invokes onClose with the
// captured prefix + cumulative byte count. The order matters — inner
// close runs first so the callback's log line is the LAST event for
// the response; the inner close error is returned to the caller
// unchanged (NOT swallowed), so the proxy chain still surfaces any I/O
// failure on close.
func (t *teeBody) Close() error {
	err := t.rc.Close()
	if t.onClose != nil {
		t.onClose(t.buf.Bytes(), t.total)
		t.onClose = nil // guard against double-close
	}
	return err
}

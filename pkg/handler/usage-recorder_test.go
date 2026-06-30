// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

var _ = Describe("usageRecorder tail buffer", func() {
	var (
		rr *httptest.ResponseRecorder
		ur *handler.UsageRecorder
	)

	BeforeEach(func() {
		rr = httptest.NewRecorder()
		ur = handler.NewUsageRecorder(rr)
	})

	It("retains the last bytes written when total is under the bound", func() {
		n, err := handler.UsageRecorderWrite(ur, []byte("hello world"))
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(11))
		Expect(handler.UsageRecorderTail(ur)).To(Equal([]byte("hello world")))
		// write-through: the underlying recorder received the same bytes.
		Expect(rr.Body.String()).To(Equal("hello world"))
	})

	It("evicts oldest bytes and retains the tail when writes exceed the bound", func() {
		// Anti-fake requirement: the overflow content MUST NOT be a single
		// repeated byte or constant fill — a buggy zero-fill truncation
		// would pass by accident. Each byte position i carries byte(i % 256)
		// across the full write stream, so a wrong slice boundary or a
		// hardcoded make([]byte, TailBufferBytes) produces a detectable
		// mismatch against the expected tail.
		const total = handler.TailBufferBytes + 100
		data := make([]byte, total)
		for i := range data {
			data[i] = byte(i % 256)
		}

		n, err := handler.UsageRecorderWrite(ur, data)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(total))

		tail := handler.UsageRecorderTail(ur)
		Expect(len(tail)).To(Equal(handler.TailBufferBytes))
		Expect(tail).To(Equal(data[total-handler.TailBufferBytes:]))

		// The bound limits the buffer, not the client: the underlying
		// recorder received ALL bytes.
		Expect(rr.Body.Len()).To(Equal(total))
		Expect(rr.Body.Bytes()).To(Equal(data))
	})

	It("retains the terminal chunk after a sequence of overflow writes", func() {
		// Model the SSE scenario: a large filler stream, then a small
		// terminal `message_delta` chunk arrives last. The terminal chunk
		// must survive at the tail of the buffer for extraction (prompt 2).
		filler := make([]byte, handler.TailBufferBytes-50)
		for i := range filler {
			filler[i] = byte('A' + (i % 26))
		}
		_, err := handler.UsageRecorderWrite(ur, filler)
		Expect(err).NotTo(HaveOccurred())

		terminal := []byte(
			"event: message_delta\ndata: {\"usage\":{\"input_tokens\":42,\"output_tokens\":7}}\n\n",
		)
		Expect(len(terminal)).To(BeNumerically("<=", 80))
		_, err = handler.UsageRecorderWrite(ur, terminal)
		Expect(err).NotTo(HaveOccurred())

		tail := handler.UsageRecorderTail(ur)
		Expect(tail).To(ContainSubstring(string(terminal)))
		Expect(string(tail[len(tail)-len(terminal):])).To(Equal(string(terminal)))
	})

	It("never grows beyond TailBufferBytes regardless of total written", func() {
		// 5 MB of filler, written in TailBufferBytes/4-sized chunks.
		const total = 5 << 20
		chunkSize := handler.TailBufferBytes / 4
		chunk := make([]byte, chunkSize)
		for i := range chunk {
			chunk[i] = byte(i % 256)
		}

		written := 0
		for written < total {
			take := chunkSize
			if written+take > total {
				take = total - written
			}
			_, err := handler.UsageRecorderWrite(ur, chunk[:take])
			Expect(err).NotTo(HaveOccurred())
			written += take
			Expect(
				len(handler.UsageRecorderTail(ur)),
			).To(BeNumerically("<=", handler.TailBufferBytes))
		}
		Expect(len(handler.UsageRecorderTail(ur))).To(Equal(handler.TailBufferBytes))
		// All bytes reached the client.
		Expect(rr.Body.Len()).To(Equal(total))
	})

	It("Write returns the count and error from the underlying writer", func() {
		sentinel := errors.New("boom")
		ew := &errorAfterKWriter{ResponseRecorder: httptest.NewRecorder(), k: 3, err: sentinel}
		ur2 := handler.NewUsageRecorder(ew)

		payload := []byte("hello world") // 11 bytes; error fires after 3
		n, err := handler.UsageRecorderWrite(ur2, payload)
		Expect(err).To(MatchError(sentinel))
		Expect(n).To(Equal(3))
		// Only the successfully-written prefix (3 bytes) was copied.
		tail := handler.UsageRecorderTail(ur2)
		Expect(tail).To(Equal(payload[:3]))
	})

	It("WriteHeader and Write delegate to the underlying statusRecorder", func() {
		handler.UsageRecorderWriteHeader(ur, http.StatusTeapot)
		n, err := handler.UsageRecorderWrite(ur, []byte("body"))
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(4))
		// statusRecorder captured the status; assert via the recorded result
		// (preferred over touching statusRecorder internals — see prompt).
		Expect(rr.Result().StatusCode).To(Equal(http.StatusTeapot))
		Expect(handler.UsageRecorderStatus(ur)).To(Equal(http.StatusTeapot))
	})

	Describe("Unwrap chain", func() {
		It(
			"http.NewResponseController(usageRecorder).Flush() reaches the underlying Flusher",
			func() {
				// Mirrors the SSE-flush regression spec in model-router_test.go,
				// but adds the extra usageRecorder wrapper layer. If
				// usageRecorder.Unwrap is missing or returns the wrong target,
				// the flush fails to reach the spy.
				spy := &flushTrackingWriter{ResponseRecorder: httptest.NewRecorder()}
				ur2 := handler.NewUsageRecorder(spy)

				Expect(http.NewResponseController(ur2).Flush()).To(Succeed())
				Expect(spy.flushed).To(BeNumerically(">", 0))
			},
		)

		It("http.NewResponseController(usageRecorder).Hijack() resolves through the chain", func() {
			hw := newHijackTrackingWriter()
			ur2 := handler.NewUsageRecorder(hw)

			conn, rw, err := http.NewResponseController(ur2).Hijack()
			Expect(err).NotTo(HaveOccurred())
			Expect(conn).NotTo(BeNil())
			Expect(rw).NotTo(BeNil())
			Expect(hw.hijacked).To(BeTrue())
		})
	})
})

// errorAfterKWriter writes at most k bytes successfully, then returns
// (n, sentinel) without storing the rest — models a writer that fails
// partway through a Write. The wrapping statusRecorder will see the
// partial n and the error.
type errorAfterKWriter struct {
	*httptest.ResponseRecorder
	k   int
	err error
}

func (e *errorAfterKWriter) Write(b []byte) (int, error) {
	if e.k <= 0 {
		return 0, e.err
	}
	take := len(b)
	if take > e.k {
		take = e.k
	}
	n, _ := e.ResponseRecorder.Write(b[:take])
	e.k -= n
	if e.k == 0 {
		return n, e.err
	}
	return n, nil
}

// hijackTrackingWriter embeds *httptest.ResponseRecorder (which is a
// Flusher) and additionally implements http.Hijacker, so the Unwrap
// chain has a real Hijacker to reach. If usageRecorder.Unwrap is broken,
// http.NewResponseController cannot reach the Hijacker and Hijack errors.
type hijackTrackingWriter struct {
	*httptest.ResponseRecorder
	hijacked bool
}

func newHijackTrackingWriter() *hijackTrackingWriter {
	return &hijackTrackingWriter{ResponseRecorder: httptest.NewRecorder()}
}

func (h *hijackTrackingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	c1, c2 := net.Pipe()
	rw := bufio.NewReadWriter(bufio.NewReader(c2), bufio.NewWriter(c2))
	return c1, rw, nil
}

var _ io.Writer = (*errorAfterKWriter)(nil)

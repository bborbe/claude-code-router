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
	"net/http/httputil"
	"net/url"
	"strings"

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

// Anti-fake: upstream token numbers are varied across all cases —
// a hardcoded constant extractor must fail these specs (spec 004 AC 2/3).
var _ = Describe("extractUsage", func() {
	Describe("SSE responses (text/event-stream)", func() {
		It("extracts usage from a single-event message_delta", func() {
			tail := []byte(
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":42,\"output_tokens\":17}}\n\n",
			)
			usage := handler.ExtractUsage(tail, "text/event-stream")
			Expect(usage.Input).To(Equal("42"))
			Expect(usage.Output).To(Equal("17"))
		})

		It("extracts usage from the terminal message_delta among multiple events", func() {
			// Use distinct numbers: input=300, output=99 (different from single-event case).
			tail := []byte(
				"event: message_start\ndata: {\"type\":\"message_start\"}\n\n" +
					"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"hi\"}}\n\n" +
					"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":300,\"output_tokens\":99}}\n\n",
			)
			usage := handler.ExtractUsage(tail, "text/event-stream")
			Expect(usage.Input).To(Equal("300"))
			Expect(usage.Output).To(Equal("99"))
		})

		It("extracts usage when terminal event fits in tail buffer with filler", func() {
			// Build a tail that is exactly TailBufferBytes: filler + terminal event.
			// Use distinct numbers: input=7, output=3.
			terminalEvent := []byte(
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}\n\n",
			)
			fillerLen := handler.TailBufferBytes - len(terminalEvent)
			filler := make([]byte, 0, fillerLen+len(terminalEvent))
			for i := 0; i < fillerLen; i++ {
				filler = append(filler, byte('A'+(i%26)))
			}
			tail := append(filler, terminalEvent...)

			usage := handler.ExtractUsage(tail, "text/event-stream")
			Expect(usage.Input).To(Equal("7"))
			Expect(usage.Output).To(Equal("3"))
		})

		It("returns noUsage when terminal event was evicted (all filler)", func() {
			// Tail is exactly TailBufferBytes of filler — no message_delta anywhere.
			filler := make([]byte, handler.TailBufferBytes)
			for i := range filler {
				filler[i] = byte('B' + (i % 26))
			}
			tail := filler

			usage := handler.ExtractUsage(tail, "text/event-stream")
			Expect(usage.Input).To(Equal("-"))
			Expect(usage.Output).To(Equal("-"))
		})

		It("returns noUsage for empty tail", func() {
			usage := handler.ExtractUsage(nil, "text/event-stream")
			Expect(usage.Input).To(Equal("-"))
			Expect(usage.Output).To(Equal("-"))
		})

		It("returns noUsage when data line is truncated mid-JSON", func() {
			// Truncated: "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":42" — incomplete JSON.
			tail := []byte(
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":42",
			)
			usage := handler.ExtractUsage(tail, "text/event-stream")
			Expect(usage.Input).To(Equal("-"))
			Expect(usage.Output).To(Equal("-"))
		})
	})

	Describe("non-streaming JSON responses", func() {
		It("extracts usage from JSON with usage block", func() {
			// Use distinct numbers: input=100, output=5.
			tail := []byte(`{"id":"msg_01","usage":{"input_tokens":100,"output_tokens":5}}`)
			usage := handler.ExtractUsage(tail, "application/json")
			Expect(usage.Input).To(Equal("100"))
			Expect(usage.Output).To(Equal("5"))
		})

		It("returns noUsage when usage block is absent", func() {
			tail := []byte(`{"ok":true}`)
			usage := handler.ExtractUsage(tail, "application/json")
			Expect(usage.Input).To(Equal("-"))
			Expect(usage.Output).To(Equal("-"))
		})

		It("reports zero tokens as 0 when usage is present with zeros", func() {
			// Upstream literally sent zeros — extractor reports what it parsed.
			tail := []byte(`{"usage":{"input_tokens":0,"output_tokens":0}}`)
			usage := handler.ExtractUsage(tail, "application/json")
			Expect(usage.Input).To(Equal("0"))
			Expect(usage.Output).To(Equal("0"))
		})

		It("returns noUsage for malformed JSON", func() {
			tail := []byte(`{not json`)
			usage := handler.ExtractUsage(tail, "application/json")
			Expect(usage.Input).To(Equal("-"))
			Expect(usage.Output).To(Equal("-"))
		})
	})

	Describe("content-type routing", func() {
		It(
			"detects SSE via content scan when Content-Type is wrong (e.g. application/json)",
			func() {
				// spec 005 root cause (a): reverse-proxied SSE responses may present
				// an empty or wrong Content-Type on the sniffed rec.Header(). The
				// fix falls back to a content scan for the "event: message_" marker.
				// Anti-fake: different numbers (42/17) from all other cases.
				tail := []byte(
					"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":42,\"output_tokens\":17}}\n\n",
				)
				usage := handler.ExtractUsage(tail, "application/json")
				Expect(usage.Input).To(Equal("42"))
				Expect(usage.Output).To(Equal("17"))
			},
		)

		It("detects SSE via content scan when Content-Type is empty", func() {
			// Different numbers to defeat hardcoded fakes.
			tail := []byte(
				"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1000,\"output_tokens\":1}}}\n\n" +
					"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":250}}\n\n",
			)
			usage := handler.ExtractUsage(tail, "")
			Expect(usage.Input).To(Equal("1000"))
			Expect(usage.Output).To(Equal("250"))
		})

		It("detects SSE via content scan when Content-Type is application/octet-stream", func() {
			// Different numbers again.
			tail := []byte(
				"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":55,\"output_tokens\":1}}}\n\n" +
					"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":66}}\n\n",
			)
			usage := handler.ExtractUsage(tail, "application/octet-stream")
			Expect(usage.Input).To(Equal("55"))
			Expect(usage.Output).To(Equal("66"))
		})
	})

	Describe("panic safety", func() {
		It("returns noUsage without panicking on pathological input", func() {
			// A deeply nested JSON that could exhaust the parser stack.
			deepJSON := `{"usage":{"input_tokens":` + strings.Repeat(
				"[",
				1000,
			) + `1` + strings.Repeat(
				"]",
				1000,
			) + `,"output_tokens":1}}`

			var panicked bool
			fn := func() (in, out string) {
				defer func() {
					if r := recover(); r != nil {
						panicked = true
					}
				}()
				u := handler.ExtractUsage([]byte(deepJSON), "application/json")
				return u.Input, u.Output
			}
			in, out := fn()
			Expect(panicked).To(BeFalse(), "extractUsage panicked on pathological input")
			Expect(in).To(Equal("-"))
			Expect(out).To(Equal("-"))
		})
	})

	// Anti-fake: upstream token numbers are varied across all cases —
	// a hardcoded constant extractor must fail these specs (spec 005 AC).
	Describe("Anthropic split-event SSE (input in message_start, output in message_delta)", func() {
		It(
			"combines input_tokens from message_start with output_tokens from message_delta",
			func() {
				// Anti-fake: distinct numbers 42/128 (different from single-event case).
				tail := []byte(
					"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_01\",\"usage\":{\"input_tokens\":42,\"output_tokens\":1}}}\n\n" +
						"event: content_block_start\ndata: {\"type\":\"content_block_start\"}\n\n" +
						"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"hi\"}}\n\n" +
						"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":128}}\n\n" +
						"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
				)
				usage := handler.ExtractUsage(tail, "text/event-stream")
				Expect(usage.Input).To(Equal("42"))
				Expect(usage.Output).To(Equal("128"))
			},
		)

		It(
			"logs 'in=<N> out=-' when only message_start survives in the tail (message_delta evicted / truncated)",
			func() {
				// Only the message_start block is present; the terminal message_delta was truncated by buffer overflow.
				// Different numbers: input=999.
				tail := []byte(
					"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":999,\"output_tokens\":1}}}\n\n",
				)
				usage := handler.ExtractUsage(tail, "text/event-stream")
				Expect(usage.Input).To(Equal("999"))
				Expect(usage.Output).To(Equal(""))
				in, out := handler.UsageLogLineValue(usage)
				Expect(in).To(Equal("999"))
				Expect(out).To(Equal("-"))
			},
		)

		It(
			"logs 'in=- out=<M>' when only message_delta survives in the tail (message_start evicted)",
			func() {
				// Only the terminal message_delta block is present.
				// Different numbers: output=77.
				tail := []byte(
					"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":77}}\n\n",
				)
				usage := handler.ExtractUsage(tail, "text/event-stream")
				Expect(usage.Input).To(Equal(""))
				Expect(usage.Output).To(Equal("77"))
				in, out := handler.UsageLogLineValue(usage)
				Expect(in).To(Equal("-"))
				Expect(out).To(Equal("77"))
			},
		)

		It("logs 'in=- out=-' when neither event survives in the tail", func() {
			// No message_start or message_delta — only a content_block_delta.
			tail := []byte(
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"filler\"}}\n\n",
			)
			usage := handler.ExtractUsage(tail, "text/event-stream")
			Expect(usage.Input).To(Equal("-"))
			Expect(usage.Output).To(Equal("-"))
		})
	})

	Describe("reverse-proxy tee reception (spec 005 root cause b)", func() {
		It("receives SSE bytes through a real httputil.ReverseProxy and extracts tokens", func() {
			// Anti-fake: distinct numbers 11/22.
			body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":11,\"output_tokens\":1}}}\n\n" +
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":22}}\n\n"

			upstream := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte(body))
				}),
			)
			defer upstream.Close()

			upstreamURL, err := url.Parse(upstream.URL)
			Expect(err).NotTo(HaveOccurred())

			proxy := httputil.NewSingleHostReverseProxy(upstreamURL)

			rr := httptest.NewRecorder()
			ur := handler.NewUsageRecorder(rr)
			req := httptest.NewRequest(
				http.MethodPost,
				"http://router.local/v1/messages",
				strings.NewReader(""),
			)
			proxy.ServeHTTP(ur, req)

			tail := handler.UsageRecorderTail(ur)
			Expect(string(tail)).To(ContainSubstring("event: message_start"))
			Expect(string(tail)).To(ContainSubstring("event: message_delta"))

			// Content-Type at the outer recorder should be forwarded by the proxy,
			// but the fix must also work if it was NOT — use whatever the recorder saw.
			usage := handler.ExtractUsage(tail, rr.Header().Get("Content-Type"))
			Expect(usage.Input).To(Equal("11"))
			Expect(usage.Output).To(Equal("22"))
		})
	})
})

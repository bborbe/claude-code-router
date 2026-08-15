// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

var _ = Describe("system-lift", func() {
	Describe("LiftSystemMessages", func() {
		It("lifts non-leading system entries into the top-level system block in order", func() {
			body := `{"model":"qwen3.8:27b-mlx","max_tokens":64,"system":[{"type":"text","text":"top"}],"messages":[{"role":"user","content":"hi"},{"role":"system","content":"A"},{"role":"assistant","content":"ok"},{"role":"system","content":"B"}]}`
			out, moved, err := handler.LiftSystemMessages(context.Background(), []byte(body))
			Expect(err).To(BeNil())
			Expect(moved).To(Equal(2))

			var result map[string]interface{}
			Expect(json.Unmarshal(out, &result)).To(Succeed())

			messages, ok := result["messages"].([]interface{})
			Expect(ok).To(BeTrue())
			Expect(len(messages)).To(Equal(2))
			msg0, ok := messages[0].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(msg0["role"]).To(Equal("user"))
			msg1, ok := messages[1].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(msg1["role"]).To(Equal("assistant"))

			systemRaw, ok := result["system"].([]interface{})
			Expect(ok).To(BeTrue())
			Expect(len(systemRaw)).To(Equal(3))
			sys0, ok := systemRaw[0].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(sys0["text"]).To(Equal("top"))
			sys1, ok := systemRaw[1].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(sys1["text"]).To(Equal("A"))
			sys2, ok := systemRaw[2].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(sys2["text"]).To(Equal("B"))
		})

		It("renders a string-form system content as a text block with correct fields", func() {
			body := `{"model":"qwen3.8:27b-mlx","messages":[{"role":"user","content":"hi"},{"role":"system","content":"hello"}]}`
			out, moved, err := handler.LiftSystemMessages(context.Background(), []byte(body))
			Expect(err).To(BeNil())
			Expect(moved).To(Equal(1))

			var result map[string]interface{}
			Expect(json.Unmarshal(out, &result)).To(Succeed())

			systemRaw, ok := result["system"].([]interface{})
			Expect(ok).To(BeTrue())
			Expect(len(systemRaw)).To(Equal(1))
			block, ok := systemRaw[0].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(block["type"]).To(Equal("text"))
			Expect(block["text"]).To(Equal("hello"))
		})

		It("appends an already-block-list system content block for block", func() {
			body := `{"model":"qwen3.8:27b-mlx","messages":[{"role":"user","content":"hi"},{"role":"system","content":[{"type":"text","text":"x"},{"type":"text","text":"y"}]}]}`
			out, moved, err := handler.LiftSystemMessages(context.Background(), []byte(body))
			Expect(err).To(BeNil())
			Expect(moved).To(Equal(1))

			var result map[string]interface{}
			Expect(json.Unmarshal(out, &result)).To(Succeed())

			systemRaw, ok := result["system"].([]interface{})
			Expect(ok).To(BeTrue())
			Expect(len(systemRaw)).To(Equal(2))
			block0, ok := systemRaw[0].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(block0["type"]).To(Equal("text"))
			Expect(block0["text"]).To(Equal("x"))
			block1, ok := systemRaw[1].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(block1["type"]).To(Equal("text"))
			Expect(block1["text"]).To(Equal("y"))
		})

		It("normalizes a string-form top-level system field into a text block", func() {
			body := `{"model":"qwen3.8:27b-mlx","system":"top","messages":[{"role":"user","content":"hi"},{"role":"system","content":"A"}]}`
			out, moved, err := handler.LiftSystemMessages(context.Background(), []byte(body))
			Expect(err).To(BeNil())
			Expect(moved).To(Equal(1))

			var result map[string]interface{}
			Expect(json.Unmarshal(out, &result)).To(Succeed())

			systemRaw, ok := result["system"].([]interface{})
			Expect(ok).To(BeTrue())
			Expect(len(systemRaw)).To(Equal(2))
			block0, ok := systemRaw[0].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(block0["type"]).To(Equal("text"))
			Expect(block0["text"]).To(Equal("top"))
			block1, ok := systemRaw[1].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(block1["type"]).To(Equal("text"))
			Expect(block1["text"]).To(Equal("A"))
		})

		It("returns moved=0 and no error when a system entry is already first", func() {
			body := `{"model":"qwen3.8:27b-mlx","messages":[{"role":"system","content":"first"},{"role":"user","content":"hi"}]}`
			out, moved, err := handler.LiftSystemMessages(context.Background(), []byte(body))
			Expect(err).To(BeNil())
			Expect(moved).To(Equal(0))
			Expect(out).To(BeNil())
		})

		It("returns moved=0 when there is no system entry at all", func() {
			body := `{"model":"qwen3.8:27b-mlx","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"ok"}]}`
			out, moved, err := handler.LiftSystemMessages(context.Background(), []byte(body))
			Expect(err).To(BeNil())
			Expect(moved).To(Equal(0))
			Expect(out).To(BeNil())
		})

		It("returns moved=0 when the body has no messages key", func() {
			body := `{"model":"qwen3.8:27b-mlx"}`
			out, moved, err := handler.LiftSystemMessages(context.Background(), []byte(body))
			Expect(err).To(BeNil())
			Expect(moved).To(Equal(0))
			Expect(out).To(BeNil())
		})

		DescribeTable(
			"degrades on shapes it cannot interpret",
			func(body string) {
				out, moved, err := handler.LiftSystemMessages(context.Background(), []byte(body))
				Expect(err).NotTo(BeNil())
				Expect(moved).To(Equal(0))
				Expect(out).To(BeNil())
			},
			Entry("messages is a JSON string", `{"model":"m","messages":"nope"}`),
			Entry(
				"messages list contains a non-object entry",
				`{"model":"m","messages":[{"role":"user","content":"hi"},42]}`,
			),
			Entry("body is not a JSON object", `[1,2,3]`),
			Entry(
				"a role that is not a string",
				`{"model":"m","messages":[{"role":"u"},{"role":7}]}`,
			),
			Entry(
				"a misplaced system entry whose content is a number",
				`{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"system","content":5}]}`,
			),
			Entry(
				"a misplaced system entry whose content is an object",
				`{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"system","content":{"secretmarker":"x"}}]}`,
			),
		)

		It("treats a null message entry as a non-system entry and leaves it in place", func() {
			body := `{"model":"m","messages":[{"role":"user","content":"hi"},null]}`
			out, moved, err := handler.LiftSystemMessages(context.Background(), []byte(body))
			Expect(err).To(BeNil())
			Expect(moved).To(Equal(0))
			Expect(out).To(BeNil())

			// Verify the entry is still there (null Unmarshal into map gives empty map, not error)
			var result map[string]interface{}
			Expect(json.Unmarshal([]byte(body), &result)).To(Succeed())
		})
	})

	Describe("matchesAnyPattern", func() {
		DescribeTable(
			"",
			func(patterns []string, model string, expected bool) {
				Expect(handler.MatchesAnyPattern(patterns, model)).To(Equal(expected))
			},
			Entry("nil patterns never match", nil, "qwen3.8:27b-mlx", false),
			Entry("empty patterns never match", []string{}, "qwen3.8:27b-mlx", false),
			Entry("matching glob", []string{"qwen3.8*"}, "qwen3.8:27b-mlx", true),
			Entry("non-matching glob", []string{"qwen3.8*"}, "qwen3.6:35b-a3b-coding-nvfp4", false),
			Entry(
				"first pattern doesn't match, second does",
				[]string{"nope*", "qwen3.8*"},
				"qwen3.8:27b-mlx",
				true,
			),
		)
	})
})

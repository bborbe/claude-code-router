// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"path"

	bberrors "github.com/bborbe/errors"
)

// systemTextBlock is the normalized shape a plain-string system
// content value is converted into when lifted into the top-level
// system block. Field order is load-bearing: json.Marshal emits
// struct fields in declaration order, so a lifted string "hello"
// renders as exactly {"type":"text","text":"hello"}.
type systemTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// matchesAnyPattern reports whether model matches at least one of the
// glob patterns (path.Match syntax, same as the route globs). A nil
// or empty pattern list never matches — that is the "operator did not
// opt in" path and it must stay allocation- and transform-free.
// Malformed patterns are treated as non-matching here; config.Validate
// already rejects them at load time.
func matchesAnyPattern(patterns []string, model string) bool {
	for _, pattern := range patterns {
		if ok, _ := path.Match(pattern, model); ok {
			return true
		}
	}
	return false
}

// liftSystemMessages moves every system-role entry that is NOT at
// index 0 of the body's `messages` list into the body's top-level
// `system` block, preserving the order the entries appeared in and
// the relative order of the surviving messages.
//
// Returns (transformed body, number of entries moved, nil) when at
// least one entry moved. Returns (nil, 0, nil) when there is nothing
// to do — no `messages` key, or no system entry outside index 0 — and
// the caller MUST then forward the original bytes untouched (spec 008
// Constraint: byte fidelity on the no-transform path; a re-marshal
// would reorder map keys).
//
// Returns (nil, 0, err) for any shape it cannot interpret: body not a
// JSON object, `messages` not a list, a message entry that is not a
// JSON object, a `role` that is not a string, or a system `content` /
// top-level `system` value that is neither a string nor a list. The
// caller degrades by forwarding the original body plus one warning —
// it never fails the request (spec 008 Desired Behavior 6).
//
// The returned error never carries message content: system prompts
// routinely hold private repository context (spec 008 Security §
// log hygiene).
//
// A system entry already at index 0 is left in place and is NOT
// copied into the top-level block, even when a top-level system block
// also exists — deduplicating the two is explicitly out of scope.
func liftSystemMessages(ctx context.Context, body []byte) ([]byte, int, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, 0, bberrors.Wrapf(ctx, err, "parse body as JSON object")
	}
	rawMessages, ok := obj["messages"]
	if !ok {
		return nil, 0, nil
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(rawMessages, &messages); err != nil {
		return nil, 0, bberrors.Wrapf(ctx, err, "parse messages as a list")
	}
	kept := make([]json.RawMessage, 0, len(messages))
	lifted := make([]json.RawMessage, 0, len(messages))
	moved := 0
	for i, rawMessage := range messages {
		var message map[string]json.RawMessage
		if err := json.Unmarshal(rawMessage, &message); err != nil {
			return nil, 0, bberrors.Wrapf(ctx, err, "parse message %d as a JSON object", i)
		}
		rawRole, hasRole := message["role"]
		if !hasRole || i == 0 {
			kept = append(kept, rawMessage)
			continue
		}
		var role string
		if err := json.Unmarshal(rawRole, &role); err != nil {
			return nil, 0, bberrors.Wrapf(ctx, err, "parse role of message %d as a string", i)
		}
		if role != "system" {
			kept = append(kept, rawMessage)
			continue
		}
		blocks, err := normalizeToBlocks(ctx, message["content"])
		if err != nil {
			return nil, 0, bberrors.Wrapf(ctx, err, "normalize content of message %d", i)
		}
		lifted = append(lifted, blocks...)
		moved++
	}
	if moved == 0 {
		return nil, 0, nil
	}
	system, err := normalizeToBlocks(ctx, obj["system"])
	if err != nil {
		return nil, 0, bberrors.Wrapf(ctx, err, "normalize top-level system block")
	}
	system = append(system, lifted...)
	systemJSON, err := json.Marshal(system)
	if err != nil {
		return nil, 0, bberrors.Wrapf(ctx, err, "marshal system block")
	}
	messagesJSON, err := json.Marshal(kept)
	if err != nil {
		return nil, 0, bberrors.Wrapf(ctx, err, "marshal message list")
	}
	obj["system"] = systemJSON
	obj["messages"] = messagesJSON
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, 0, bberrors.Wrapf(ctx, err, "re-marshal body")
	}
	return out, moved, nil
}

// normalizeToBlocks converts a system content value into a list of
// content blocks:
//
//   - absent (nil raw) or JSON null -> zero blocks
//   - JSON string                   -> exactly one systemTextBlock
//   - JSON list                     -> its elements, unchanged, block
//     for block
//   - anything else                 -> error (caller degrades)
//
// The error message deliberately carries no content, only the fact
// that the shape was unsupported.
func normalizeToBlocks(ctx context.Context, raw json.RawMessage) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return []json.RawMessage{}, nil
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil {
		block, merr := json.Marshal(systemTextBlock{Type: "text", Text: text})
		if merr != nil {
			return nil, bberrors.Wrapf(ctx, merr, "marshal text block")
		}
		return []json.RawMessage{block}, nil
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(trimmed, &blocks); err == nil {
		return blocks, nil
	}
	return nil, bberrors.New(ctx, "unsupported system content shape")
}

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	liblog "github.com/bborbe/log"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// NewLoggingRoundTripper wraps inner with upstream-call logging at three
// verbosity tiers:
//
//   - V(3) [upstream.headers]: emitted before the inner RoundTrip call;
//     dumps the outbound request headers (after the auth-swap transport has
//     applied its Authorization rewrite) as a JSON object with
//     credential-shaped values redacted via RedactHeadersForLog. Useful for
//     confirming exactly what token / headers reached the provider. Enable via
//     `curl http://127.0.0.1:8788/setloglevel/3`.
//
//   - V(4) [upstream.start] / [upstream.end]: method+path on start; on end,
//     adds TTFB (time-to-first-byte from when inner.RoundTrip was invoked
//     until it returned with response headers) + status code (or error).
//     Useful for debugging slow upstream behavior — distinguishes "Anthropic
//     took 90s to send first byte" (high TTFB) from "body streaming was slow"
//     (low TTFB, high total latency in the surrounding [req] line). Enable via
//     `curl http://127.0.0.1:8788/setloglevel/4`.
//
//   - V(4) [upstream.req.body] / [upstream.resp.body]: body-sample lines
//     gated by bodySampler (typically
//     SamplerList{NewSampleTime(1s), NewSamplerGlogLevel(5)}).  Captures up to
//     BodySampleMaxBytes (4 KB) of the request body before forwarding it, and
//     wraps the response body so the same size prefix is logged on Close.
//     Bearer tokens are redacted via RedactBearerTokensInBody before the line
//     is emitted.
//
// If inner is nil, http.DefaultTransport is used (matches the nil-default
// pattern in NewAnthropicProxyHandler).
//
// Silent at default V(1)-V(2).
func NewLoggingRoundTripper(
	inner http.RoundTripper,
	bodySampler liblog.Sampler,
	currentDateTime libtime.CurrentDateTimeGetter,
) http.RoundTripper {
	if inner == nil {
		inner = http.DefaultTransport
	}
	return &loggingRoundTripper{
		inner:           inner,
		bodySampler:     bodySampler,
		currentDateTime: currentDateTime,
	}
}

type loggingRoundTripper struct {
	inner           http.RoundTripper
	bodySampler     liblog.Sampler
	currentDateTime libtime.CurrentDateTimeGetter
}

func (l *loggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Net/http guarantees req.URL is non-nil for any request reaching a
	// RoundTripper (transports panic without it), but be defensive — a
	// future caller invoking us directly with a hand-crafted *http.Request
	// shouldn't crash the proxy.
	host, path := "", ""
	if req.URL != nil {
		host, path = req.URL.Host, req.URL.Path
	}
	glog.V(4).Infof("[upstream.start] %s %s%s", req.Method, host, path)
	if glog.V(3) {
		redacted := RedactHeadersForLog(req.Header)
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		// SetEscapeHTML(false) is deliberate — without it, the
		// `<redacted len=N>` placeholder serializes as `<redacted len=N>`
		// and the operator can't grep for it. Threat model: the upstream URLs
		// (api.anthropic.com, minimax.io, localhost ollama) are operator-trusted
		// providers, NOT attacker-controlled. If a future deployment adds an
		// upstream where header values could be attacker-supplied, switch back
		// to default escaping or sanitize values via html.EscapeString first —
		// for the personal-router use case the trade-off favors readability.
		enc.SetEscapeHTML(false)
		_ = enc.Encode(redacted)
		glog.V(3).
			Infof("[upstream.headers] %s %s%s headers=%s", req.Method, host, path, strings.TrimSpace(buf.String()))
	}
	if glog.V(4) {
		if l.bodySampler.IsSample() && req.Body != nil {
			s := readSnippet(req, BodySampleMaxBytes)
			glog.V(4).Infof("[upstream.req.body] %s%s body_len=%d sample=%s",
				host, path, s.totalLen,
				RedactBearerTokensInBody(s.head))
		}
	}
	start := l.currentDateTime.Now().Time()
	resp, err := l.inner.RoundTrip(req)
	ttfb := l.currentDateTime.Now().Time().Sub(start).Round(time.Millisecond)
	if err != nil {
		glog.V(4).
			Infof("[upstream.end] %s %s%s ttfb=%s err=%v", req.Method, host, path, ttfb, err)
		// Return nil resp on error per net/http contract — callers
		// must not inspect resp when err != nil; some inner transports
		// return a non-nil resp alongside err which is a footgun.
		return nil, err
	}
	glog.V(4).
		Infof("[upstream.end] %s %s%s ttfb=%s status=%d", req.Method, host, path, ttfb, resp.StatusCode)
	if glog.V(4) {
		if l.bodySampler.IsSample() && resp.Body != nil {
			resp.Body = newTeeBody(resp.Body, BodySampleMaxBytes, func(data []byte, total int) {
				glog.V(4).Infof("[upstream.resp.body] %s%s body_len=%d sample=%s",
					host, path, total,
					RedactBearerTokensInBody(data))
			})
		}
	}
	return resp, nil
}

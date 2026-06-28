// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"net/http"
	"time"

	"github.com/golang/glog"
)

// NewLoggingRoundTripper wraps inner with a V(4) `[upstream.start]` /
// `[upstream.end]` log-line pair per call: method + path on start,
// method + path + TTFB (time-to-first-byte from when inner.RoundTrip
// was invoked until it returned with the response headers) + status
// code (or error) on end. Useful for debugging slow upstream behavior
// — distinguishes "Anthropic took 90s to start sending headers" (high
// TTFB) from "Anthropic sent headers fast but body streaming was slow"
// (low TTFB, high total latency in the surrounding `[req]` line).
//
// If inner is nil, http.DefaultTransport is used (defensive — matches
// the nil-default pattern in NewAnthropicProxyHandler).
//
// Silent at default V(1)-V(3); enable via `curl http://127.0.0.1:8788/setloglevel/4`.
func NewLoggingRoundTripper(inner http.RoundTripper) http.RoundTripper {
	if inner == nil {
		inner = http.DefaultTransport
	}
	return &loggingRoundTripper{inner: inner}
}

type loggingRoundTripper struct {
	inner http.RoundTripper
}

func (l *loggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	glog.V(4).
		Infof("[upstream.start] %s %s%s", req.Method, req.URL.Host, req.URL.Path)
	start := time.Now()
	resp, err := l.inner.RoundTrip(req)
	ttfb := time.Since(start).Round(time.Millisecond)
	if err != nil {
		glog.V(4).
			Infof("[upstream.end] %s %s%s ttfb=%s err=%v", req.Method, req.URL.Host, req.URL.Path, ttfb, err)
		// Return nil resp on error per net/http contract — callers
		// must not inspect resp when err != nil; some inner transports
		// return a non-nil resp alongside err which is a footgun.
		return nil, err
	}
	glog.V(4).
		Infof("[upstream.end] %s %s%s ttfb=%s status=%d", req.Method, req.URL.Host, req.URL.Path, ttfb, resp.StatusCode)
	return resp, nil
}

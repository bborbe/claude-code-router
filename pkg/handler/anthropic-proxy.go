// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"net"
	"net/http"
	"net/url"
	"time"

	libhttp "github.com/bborbe/http"
	"github.com/golang/glog"
)

// DefaultProxyTransport returns an http.Transport with explicit timeouts
// suitable for upstream LLM API calls. Long ResponseHeaderTimeout because
// LLM completions can take 30s+ for the first byte (SSE); short Dial
// because connections are local-network or quick HTTPS to api.anthropic.com.
func DefaultProxyTransport() *http.Transport {
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}
}

// NewAnthropicProxyHandler returns an HTTP handler that reverse-proxies
// every incoming request to upstream (typically https://api.anthropic.com).
//
// The Authorization header passes through unchanged — this is what lets
// Claude Code's subscription OAuth bearer travel through the router to
// Anthropic without the router ever holding it. No body parsing, no
// model-based routing: that's task 3. v1 of task 2 = single upstream,
// verbatim forward.
//
// Upstream errors (connection refused, 5xx before body, etc.) are logged
// server-side with the full error string for debugging, but the client
// sees only a generic "502 Bad Gateway / upstream unavailable" — the
// internal error details (IPs, TLS handshake failures, connection
// strings) are not leaked.
//
// If transport is nil, DefaultProxyTransport is used.
func NewAnthropicProxyHandler(upstream *url.URL, transport http.RoundTripper) http.Handler {
	if transport == nil {
		transport = DefaultProxyTransport()
	}
	errorHandler := libhttp.ProxyErrorHandlerFunc(
		func(resp http.ResponseWriter, req *http.Request, err error) {
			glog.Errorf("[proxy] %s %s -> upstream error: %v", req.Method, req.URL.Path, err)
			resp.WriteHeader(http.StatusBadGateway)
			_, _ = resp.Write([]byte("upstream unavailable"))
		},
	)
	return libhttp.NewProxy(transport, upstream, errorHandler)
}

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"net/http"
	"net/url"

	libhttp "github.com/bborbe/http"
	"github.com/golang/glog"
)

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
// and surfaced to the client as 502 Bad Gateway so Claude Code can
// distinguish them from real Anthropic errors that flow through the
// reverse proxy untouched.
func NewAnthropicProxyHandler(upstream *url.URL, transport http.RoundTripper) http.Handler {
	if transport == nil {
		transport = http.DefaultTransport
	}
	errorHandler := libhttp.ProxyErrorHandlerFunc(
		func(resp http.ResponseWriter, req *http.Request, err error) {
			glog.Errorf("[proxy] %s %s -> upstream error: %v", req.Method, req.URL.Path, err)
			resp.WriteHeader(http.StatusBadGateway)
			_, _ = resp.Write([]byte("upstream error: " + err.Error()))
		},
	)
	return libhttp.NewProxy(transport, upstream, errorHandler)
}

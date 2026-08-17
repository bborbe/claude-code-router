// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import "net"

// IsLoopbackRemoteAddr reports whether the given remote address is a
// loopback address. It accepts the address in the format Go's http server
// produces in http.Request.RemoteAddr: "127.0.0.1:54321" for IPv4 and
// "[::1]:54321" for IPv6. Both IPv4 loopback (127.0.0.0/8) and IPv6
// loopback (::1) count as local; anything that is not host:port is not
// loopback.
//
// The remote address MUST come from the connection only — never from
// X-Forwarded-For or any other client-supplied header. There is no trusted
// proxy in front of this router; honouring a forwarded header would let any
// remote caller claim to be loopback and bypass both the auth check and the
// admin guard.
func IsLoopbackRemoteAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

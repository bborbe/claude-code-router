// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// export_test.go re-exports unexported symbols for testing.
package handler

import "net/http"

// TraceTTLFromEnv exposes traceTTLFromEnv for handler_test.
var TraceTTLFromEnv = traceTTLFromEnv

// UsageRecorder exposes the unexported usageRecorder type so the
// handler_test package can hold and pass around *usageRecorder values.
// All interaction happens through the accessor functions below.
type UsageRecorder = usageRecorder

// NewUsageRecorder exposes newUsageRecorder for handler_test.
func NewUsageRecorder(w http.ResponseWriter) *usageRecorder {
	return newUsageRecorder(w)
}

// UsageRecorderTail exposes (*usageRecorder).Tail for handler_test.
func UsageRecorderTail(u *usageRecorder) []byte {
	return u.Tail()
}

// UsageRecorderWrite exposes (*usageRecorder).Write for handler_test.
func UsageRecorderWrite(u *usageRecorder, b []byte) (int, error) {
	return u.Write(b)
}

// UsageRecorderWriteHeader exposes (*usageRecorder).WriteHeader for handler_test.
func UsageRecorderWriteHeader(u *usageRecorder, code int) {
	u.WriteHeader(code)
}

// UsageRecorderStatus exposes the status captured by the wrapped
// *statusRecorder so the delegate spec can assert on it directly.
func UsageRecorderStatus(u *usageRecorder) int {
	if !u.rec.wroteHeader {
		return http.StatusOK
	}
	return u.rec.status
}

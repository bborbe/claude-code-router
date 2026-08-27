// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// export_test.go re-exports unexported symbols for testing.
package handler

import (
	"context"
	"net/http"

	libtime "github.com/bborbe/time"
)

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

// UsageLogLineValue exposes (TokenUsage).logLineValue for handler_test.
func UsageLogLineValue(u TokenUsage) (in, out string) {
	return u.logLineValue()
}

// LiftSystemMessages exposes liftSystemMessages for handler_test.
func LiftSystemMessages(ctx context.Context, body []byte) ([]byte, int, error) {
	return liftSystemMessages(ctx, body)
}

// MatchesAnyPattern exposes matchesAnyPattern for handler_test.
func MatchesAnyPattern(patterns []string, model string) bool {
	return matchesAnyPattern(patterns, model)
}

// ThrottleGateObserve drives the detector of a real throttle gate with a
// single observed response status at the given clock time (spec 018): the
// AIMD and recovery rows drive the detector directly so a growing pacing
// delay never sleeps wall-clock. h must be a real gate (threshold > 0);
// the disabled path returns next unchanged and has no gate.
func ThrottleGateObserve(h http.Handler, status int, at libtime.DateTime) {
	g, ok := h.(interface {
		observe(status int, at libtime.DateTime)
	})
	if !ok {
		panic("ThrottleGateObserve: handler is not a real throttle gate")
	}
	g.observe(status, at)
}

// ThrottleMaxPacedRequests exposes the bounded pacing-queue capacity so
// the overflow row can saturate the queue deterministically (spec 018
// DB 3).
var ThrottleMaxPacedRequests = throttleMaxPacedRequests

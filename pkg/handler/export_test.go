// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// export_test.go re-exports unexported symbols for testing.
package handler

// TraceTTLFromEnv exposes traceTTLFromEnv for handler_test.
var TraceTTLFromEnv = traceTTLFromEnv

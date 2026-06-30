// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package reloader_test

import (
	"os/signal"
	"syscall"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
)

func TestReloader(t *testing.T) {
	time.Local = time.UTC
	format.TruncatedDiff = false
	RegisterFailHandler(Fail)
	suiteConfig, reporterConfig := GinkgoConfiguration()
	suiteConfig.Timeout = 60 * time.Second
	RunSpecs(t, "Reloader Suite", suiteConfig, reporterConfig)
}

// AfterSuite stops the package-level SIGHUP interceptor and drains any
// pending SIGHUP from the buffered channel before the test process exits.
// Without this, a SIGHUP queued during a test can fire during Go's exit
// sequence and cause a "signal: hangup" termination.
var _ = AfterSuite(func() {
	// Unregister the package-level interceptor so no more SIGHUPs are delivered to it.
	signal.Stop(sighupInterceptor)
	// Restore default OS disposition (SIGHUP → terminate). The process is
	// exiting anyway; this prevents a queued SIGHUP from firing at exit.
	signal.Reset(syscall.SIGHUP)
	// Non-blocking drain of the buffered channel (cap 1) so a signal
	// delivered just before Stop doesn't fire during Go's atexit handlers.
	for {
		select {
		case <-sighupInterceptor:
			continue
		default:
			return
		}
	}
})

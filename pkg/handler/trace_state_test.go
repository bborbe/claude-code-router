// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-code-router/pkg/handler"
)

var _ = Describe("TraceState", func() {

	Describe("AC #8: default state at boot", func() {
		It("IsEnabled is false immediately after construction", func() {
			ts := handler.NewTraceStateWithTTL(100 * time.Millisecond)
			Expect(ts.IsEnabled()).To(BeFalse())
		})

		It("IsEnabled is false after NewTraceState (production constructor)", func() {
			ts := handler.NewTraceState()
			Expect(ts.IsEnabled()).To(BeFalse())
		})
	})

	Describe("AC #4: TTL auto-disable", func() {
		It("IsEnabled becomes false after the TTL window expires", func() {
			ts := handler.NewTraceStateWithTTL(50 * time.Millisecond)
			ts.Enable()
			Expect(ts.IsEnabled()).To(BeTrue())

			// Wait past the TTL; Eventually polls with 2s window, 10ms interval.
			Eventually(func() bool {
				return ts.IsEnabled()
			}, 2*time.Second, 10*time.Millisecond).Should(BeFalse())
		})
	})

	Describe("AC #5: disable mid-window cancels the timer", func() {
		It("Disable called mid-window prevents the expired timer from re-enabling", func() {
			ts := handler.NewTraceStateWithTTL(100 * time.Millisecond)
			ts.Enable()
			Expect(ts.IsEnabled()).To(BeTrue())

			// Immediately disable before the timer fires.
			ts.Disable()
			Expect(ts.IsEnabled()).To(BeFalse())

			// Wait past the original window; flag must still be false.
			time.Sleep(200 * time.Millisecond)
			Expect(ts.IsEnabled()).To(BeFalse())
		})
	})

	Describe("AC #6: repeated Enable resets the window", func() {
		It("N consecutive Enable calls result in exactly one expiry fire", func() {
			ts := handler.NewTraceStateWithTTL(100 * time.Millisecond)

			// Fire 5 Enable calls in rapid succession.
			for i := 0; i < 5; i++ {
				ts.Enable()
			}
			Expect(ts.IsEnabled()).To(BeTrue())

			// Wait past the last enable's window (100ms); exactly one expiry fires.
			Eventually(func() bool {
				return ts.IsEnabled()
			}, 2*time.Second, 10*time.Millisecond).Should(BeFalse())

			// Wait a further 300ms to confirm no additional expiry fires.
			time.Sleep(300 * time.Millisecond)
			Expect(ts.IsEnabled()).To(BeFalse())
		})
	})

	Describe("Failure Mode row 3: concurrent Enable calls", func() {
		It("does not panic and results in exactly one expiry", func() {
			ts := handler.NewTraceStateWithTTL(100 * time.Millisecond)

			var wg sync.WaitGroup
			for i := 0; i < 20; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					ts.Enable()
				}()
			}
			wg.Wait()

			Expect(ts.IsEnabled()).To(BeTrue())

			Eventually(func() bool {
				return ts.IsEnabled()
			}, 2*time.Second, 10*time.Millisecond).Should(BeFalse())

			// Confirm no stacked expiries — flag stays false after expiry.
			time.Sleep(200 * time.Millisecond)
			Expect(ts.IsEnabled()).To(BeFalse())
		})
	})

	Describe("glog V(n) gating", func() {
		It("trace_state.go contains no bare glog.Infof or glog.Info", func() {
			cwd, err := os.Getwd()
			Expect(err).NotTo(HaveOccurred())
			srcPath := filepath.Join(cwd, "trace_state.go")
			src, err := os.ReadFile(srcPath)
			Expect(err).NotTo(HaveOccurred(), "read "+srcPath)
			lines := strings.Split(string(src), "\n")
			for i, line := range lines {
				if strings.Contains(line, "glog.Infof") && !strings.Contains(line, "glog.V(") {
					Fail(fmt.Sprintf("line %d: bare glog.Infof without V(n): %s", i+1, line))
				}
				if strings.Contains(line, "glog.Info(") && !strings.Contains(line, "glog.V(") {
					Fail(fmt.Sprintf("line %d: bare glog.Info without V(n): %s", i+1, line))
				}
			}
		})
	})
})

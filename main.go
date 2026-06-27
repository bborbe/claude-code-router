// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// No main_test.go: the gexec.Build "Compiles" smoke test commonly OOMs the
// CI runner under -race. Skip per the Setup New Go Service runbook; the
// build is verified by golangci-lint + go test in make precommit.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/bborbe/claude-code-router/pkg/cli"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := cli.NewCommand().ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}

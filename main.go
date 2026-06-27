// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"os"

	"github.com/bborbe/claude-code-router/pkg/cli"
)

func main() {
	if err := cli.NewCommand().Execute(); err != nil {
		os.Exit(1)
	}
}

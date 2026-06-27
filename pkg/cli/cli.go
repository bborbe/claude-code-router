// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package cli builds the root cobra command for claude-code-router.
package cli

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	libhttp "github.com/bborbe/http"
	"github.com/spf13/cobra"

	"github.com/bborbe/claude-code-router/pkg/factory"
)

// version is injected at build time via -ldflags by the Makefile.
var version = "dev"

// NewCommand returns the root cobra command.
func NewCommand() *cobra.Command {
	var listen string

	cmd := &cobra.Command{
		Use:     "claude-code-router",
		Short:   "Multi-provider Claude Code router",
		Long:    "Local HTTP router for Claude Code. Forwards /v1/messages requests to one of several LLM providers based on the request's model field.",
		Version: version,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
			slog.Info("starting claude-code-router", "listen", listen, "version", version)

			return libhttp.NewServer(listen, factory.CreateRouter()).Run(ctx)
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:8788", "address to listen on")
	return cmd
}

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package cli builds the root cobra command for claude-code-router.
package cli

import (
	"log/slog"
	"os"

	libhttp "github.com/bborbe/http"
	"github.com/spf13/cobra"

	"github.com/bborbe/claude-code-router/pkg/factory"
)

// version is injected at build time via -ldflags by the Makefile.
var version = "dev"

// NewCommand returns the root cobra command.
//
// Returns *cobra.Command (concrete) rather than an interface — cobra's
// builder API requires the concrete type, and wrapping it adds no
// testability gain since cobra commands are themselves the test surface.
func NewCommand() *cobra.Command {
	var listen string

	cmd := &cobra.Command{
		Use:     "claude-code-router",
		Short:   "Multi-provider Claude Code router",
		Long:    "Local HTTP router for Claude Code. Forwards /v1/messages requests to one of several LLM providers based on the request's model field.",
		Version: version,
		RunE: func(cmd *cobra.Command, _ []string) error {
			slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
			slog.Info("starting claude-code-router", "listen", listen, "version", version)

			return libhttp.NewServer(listen, factory.CreateRouter()).Run(cmd.Context())
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:8788", "address to listen on")
	return cmd
}

// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package cli holds the application struct that service.MainCmd parses
// CLI args into, plus the Run entry-point that delegates to the injected
// server factory. The factory itself lives in pkg/factory; this package
// is import-free of factory to keep the dependency direction (main ->
// factory -> ...) intact.
package cli

import (
	"context"

	librun "github.com/bborbe/run"
	"github.com/golang/glog"
)

// version is injected at build time via -ldflags by the Makefile
// (-X github.com/bborbe/claude-code-router/pkg/cli.version=...).
var version = "dev"

// ServerFactory is the dep cli requires to start the HTTP listener.
// Satisfied by factory.CreateServer. Returns the run.Func + any
// startup error (config load, validation, etc.).
type ServerFactory func(listen, configPath string) (librun.Func, error)

// App is the application wired by main and parsed by service.MainCmd's
// argument tagger. Exported fields with tags are CLI args; unexported
// fields are dependencies injected by main.
type App struct {
	Listen     string `arg:"listen"      default:"127.0.0.1:8788"                    env:"LISTEN"      required:"true" usage:"address to listen to"`
	ConfigPath string `arg:"config-path" default:"~/.claude-code-router/config.yaml" env:"CONFIG_PATH" required:"true" usage:"path to claude-code-router YAML config"`

	serverFactory ServerFactory
}

// NewApp constructs the App with the server factory injected.
func NewApp(serverFactory ServerFactory) *App {
	return &App{serverFactory: serverFactory}
}

// Run is invoked by service.MainCmd after argument parsing.
func (a *App) Run(ctx context.Context) error {
	glog.V(1).Infof(
		"starting claude-code-router version=%s listen=%s config=%s",
		version, a.Listen, a.ConfigPath,
	)
	runner, err := a.serverFactory(a.Listen, a.ConfigPath)
	if err != nil {
		return err
	}
	return runner(ctx)
}

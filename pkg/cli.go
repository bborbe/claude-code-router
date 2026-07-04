// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package cli holds the application struct that service.MainCmd parses
// CLI args into, plus the Run entry-point that delegates to the injected
// server factory. The factory itself lives in pkg/factory; this package
// is import-free of factory to keep the dependency direction (main ->
// factory -> ...) intact.
package pkg

import (
	"context"
	"path/filepath"

	librun "github.com/bborbe/run"
	"github.com/golang/glog"
)

// version is injected at build time via -ldflags by the Makefile
// (-X github.com/bborbe/claude-code-router/pkg/cli.version=...).
var version = "dev"

// ServerFactory is the dep cli requires to start the HTTP listener.
// Satisfied by factory.CreateServer. Returns the run.Func + any
// startup error (config load, validation, etc.).
type ServerFactory func(ctx context.Context, listen, configPath string) (librun.Func, error)

// App is the application wired by main and parsed by service.MainCmd's
// argument tagger. Exported fields with tags are CLI args; unexported
// fields are dependencies injected by main.
type App struct {
	Listen     string `arg:"listen"      default:"127.0.0.1:8788" env:"LISTEN"      required:"true"  usage:"address to listen to"`
	ConfigPath string `arg:"config-path" default:""               env:"CONFIG_PATH" required:"false" usage:"path to claude-code-router YAML config (default: XDG ~/.config/claude-code-router/config.yaml, falls back to legacy ~/.claude-code-router/config.yaml if that's the only one present)"`

	serverFactory ServerFactory
}

// NewApp constructs the App with the server factory injected.
func NewApp(serverFactory ServerFactory) *App {
	return &App{serverFactory: serverFactory}
}

// resolveConfigPath returns explicit unchanged if non-empty (an explicit
// --config-path flag or CONFIG_PATH env value always wins). If explicit is
// empty, it resolves the default via FindConfigDir + "config.yaml".
func resolveConfigPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	return filepath.Join(FindConfigDir("claude-code-router"), "config.yaml")
}

// Run is invoked by service.MainCmd after argument parsing.
func (a *App) Run(ctx context.Context) error {
	configPath := resolveConfigPath(a.ConfigPath)
	glog.V(1).Infof(
		"starting claude-code-router version=%s listen=%s config=%s",
		version, a.Listen, configPath,
	)
	runner, err := a.serverFactory(ctx, a.Listen, configPath)
	if err != nil {
		return err
	}
	return runner(ctx)
}

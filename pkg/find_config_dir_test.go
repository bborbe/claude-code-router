// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bborbe/argument/v2" //nolint:depguard // test-only: argument is used legitimately here
	librun "github.com/bborbe/run"
)

func TestFindConfigDirFromHome(t *testing.T) {
	t.Run("XDG dir exists, returns XDG path", func(t *testing.T) {
		homeDir := t.TempDir()
		xdgPath := filepath.Join(homeDir, ".config", "claude-code-router")
		if err := os.MkdirAll(xdgPath, 0700); err != nil {
			t.Fatal(err)
		}

		got := findConfigDirFromHome(homeDir, "claude-code-router")
		if got != xdgPath {
			t.Errorf("expected %q, got %q", xdgPath, got)
		}
	})

	t.Run("only legacy dir exists, returns legacy path", func(t *testing.T) {
		homeDir := t.TempDir()
		legacyPath := filepath.Join(homeDir, ".claude-code-router")
		if err := os.MkdirAll(legacyPath, 0700); err != nil {
			t.Fatal(err)
		}

		got := findConfigDirFromHome(homeDir, "claude-code-router")
		if got != legacyPath {
			t.Errorf("expected %q, got %q", legacyPath, got)
		}
	})

	t.Run("both exist, XDG takes priority", func(t *testing.T) {
		homeDir := t.TempDir()
		xdgPath := filepath.Join(homeDir, ".config", "claude-code-router")
		legacyPath := filepath.Join(homeDir, ".claude-code-router")
		if err := os.MkdirAll(xdgPath, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(legacyPath, 0700); err != nil {
			t.Fatal(err)
		}

		got := findConfigDirFromHome(homeDir, "claude-code-router")
		if got != xdgPath {
			t.Errorf(
				"expected XDG path %q, got %q (legacy should lose when both exist)",
				xdgPath,
				got,
			)
		}
	})

	t.Run("neither exists, defaults to XDG path", func(t *testing.T) {
		homeDir := t.TempDir()
		xdgPath := filepath.Join(homeDir, ".config", "claude-code-router")

		got := findConfigDirFromHome(homeDir, "claude-code-router")
		if got != xdgPath {
			t.Errorf("expected %q, got %q", xdgPath, got)
		}
	})
}

func TestResolveConfigPath(t *testing.T) {
	t.Run("explicit value wins regardless of what FindConfigDir would return", func(t *testing.T) {
		got := resolveConfigPath("/custom/path/config.yaml")
		if got != "/custom/path/config.yaml" {
			t.Errorf("expected explicit value unchanged, got %q", got)
		}
	})

	t.Run("empty value resolves through FindConfigDir + config.yaml", func(t *testing.T) {
		want := filepath.Join(FindConfigDir("claude-code-router"), "config.yaml")
		got := resolveConfigPath("")
		if got != want {
			t.Errorf("expected %q, got %q", want, got)
		}
	})
}

func TestAppParsesWithConfigPathUnset(t *testing.T) {
	app := NewApp(func(ctx context.Context, listen, configPath string) (librun.Func, error) {
		return func(ctx context.Context) error {
			return nil
		}, nil
	})
	if err := argument.ParseArgs(context.Background(), app, []string{}); err != nil {
		t.Fatalf("expected App to parse with an unset --config-path, got error: %v", err)
	}
	if app.ConfigPath != "" {
		t.Errorf(
			"expected ConfigPath to stay empty after parse (resolution happens in Run), got %q",
			app.ConfigPath,
		)
	}
}

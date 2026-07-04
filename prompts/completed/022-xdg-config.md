---
status: completed
summary: Migrate default config path from legacy ~/.claude-code-router/config.yaml to XDG ~/.config/claude-code-router/config.yaml with fallback; all tests and precommit pass
execution_id: claude-code-router-xdg-config-exec-022-xdg-config
dark-factory-version: v0.191.0
created: "2026-07-04T08:46:06Z"
queued: "2026-07-04T08:46:06Z"
started: "2026-07-04T08:46:07Z"
completed: "2026-07-04T08:52:25Z"
---

<summary>
- New `FindConfigDir("claude-code-router")` in `pkg/config.go` picks XDG `~/.config/claude-code-router/` over legacy `~/.claude-code-router/` when both exist, prefers legacy when only it exists, defaults to XDG when neither exists.
- `pkg/cli.go`'s `App.ConfigPath` struct tag loses its static default and `required:"true"`; `App.Run` now resolves an empty `ConfigPath` through `FindConfigDir` + `config.yaml` at run time. An explicit `--config-path` flag or `CONFIG_PATH` env value still wins unconditionally.
- `--help` usage text, `README.md`, and `docs/config.md` present the XDG path as primary, legacy as fallback.
- Trace directory (`~/.claude-code-router/trace/`, `pkg/factory/factory.go`) is explicitly untouched — it is runtime output, not config.
- New tests: `pkg/find_config_dir_test.go` (stdlib `testing.T`, table-driven, covers all 4 `FindConfigDir` priority cases) and a `resolveConfigPath` unit test in `pkg/cli_test.go` covering explicit-override-wins.
- `CHANGELOG.md` gets a new `## Unreleased` section (does not exist yet) with one feat bullet.
</summary>

<objective>
Migrate the default `claude-code-router` config path from the legacy dotfile `~/.claude-code-router/config.yaml` to the XDG-compliant `~/.config/claude-code-router/config.yaml`, while preserving the legacy path as a fallback so existing installations keep working with no forced migration. This is the 5th and final tool in a series (task-watcher, vault-ui already shipped the same pattern) — mirror their `FindConfigDir` shape exactly for consistency across the bborbe tool fleet.
</objective>

<context>
Read first (in this order):
- `/workspace/CLAUDE.md` (if present) for project conventions.
- `/workspace/docs/dod.md` — Definition of Done checklist this change is graded against.
- `/workspace/pkg/cli.go` — `App` struct (line ~29-34), `App.Run` (line ~44-52). `ConfigPath` currently: `arg:"config-path" default:"~/.claude-code-router/config.yaml" env:"CONFIG_PATH" required:"true"`.
- `/workspace/pkg/config.go` — `package pkg`. Has `expandTilde` (bottom of file) but no `FindConfigDir`. `Load(ctx, rawPath)` already expands a leading `~/` itself — `FindConfigDir`'s output does NOT need tilde-handling since it's built directly from `os.UserHomeDir()`, never a `~`-prefixed string.
- `/workspace/pkg/config_test.go` — existing Ginkgo/Gomega style, `package pkg_test` (external test package).
- `/workspace/pkg/factory/factory.go` (lines ~84-93) — trace dir at `~/.claude-code-router/trace`. This is runtime state/output, NOT config. Do NOT touch this file or this path in this prompt.
- `/workspace/README.md` (install section, ~line 18-27) and `/workspace/docs/config.md` (~line 1-8) — both show the legacy path as the documented default.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo/Gomega + stdlib-testing conventions used in this repo.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `bborbe/errors` conventions (not needed for this change since `FindConfigDir` never returns an error, but confirms the pattern for anything that touches `Load`).
</context>

<requirements>

### 1. Add `FindConfigDir` to `pkg/config.go`

```go
// FindConfigDir returns the config directory for toolName using XDG
// conventions with legacy dotfile fallback. Priority:
//  1. ~/.config/<toolName>/ if it exists
//  2. ~/.<toolName>/ if it exists
//  3. ~/.config/<toolName>/ (XDG default when neither exists — new installs
//     land in the XDG location from the start)
//
// Deliberately does NOT use os.UserConfigDir() — on macOS that resolves to
// ~/Library/Application Support, which is not this project's XDG convention
// (~/.config/<tool>/ on every platform, matching task-watcher and vault-ui).
func FindConfigDir(toolName string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", toolName)
	}
	return findConfigDirFromHome(homeDir, toolName)
}

func findConfigDirFromHome(homeDir, toolName string) string {
	xdgPath := filepath.Join(homeDir, ".config", toolName)
	legacyPath := filepath.Join(homeDir, "."+toolName)

	if info, err := os.Stat(xdgPath); err == nil && info.IsDir() {
		return xdgPath
	}
	if info, err := os.Stat(legacyPath); err == nil && info.IsDir() {
		return legacyPath
	}
	return xdgPath
}
```

Add both functions near the bottom of `pkg/config.go`, after `expandTilde`. `os` and `path/filepath` are already imported in this file — no new import needed for these two functions. `findConfigDirFromHome` is unexported; it exists purely for testability (tests inject a temp home dir instead of the real one).

On `os.UserHomeDir()` failure, `FindConfigDir` falls back to a relative `.config/<toolName>` path — the subsequent `os.ReadFile` in `Load` will surface a clear "file not found" error the operator can resolve with `--config-path`.

### 2. Change `App.ConfigPath`'s struct tag in `pkg/cli.go`

Current (line ~33):
```go
ConfigPath string `arg:"config-path" default:"~/.claude-code-router/config.yaml" env:"CONFIG_PATH" required:"true" usage:"path to claude-code-router YAML config"`
```

New:
```go
ConfigPath string `arg:"config-path" default:"" env:"CONFIG_PATH" required:"false" usage:"path to claude-code-router YAML config (default: XDG ~/.config/claude-code-router/config.yaml, falls back to legacy ~/.claude-code-router/config.yaml if that's the only one present)"`
```

`required:"false"` is necessary — `github.com/bborbe/argument/v2`'s `ValidateRequired` would reject an empty-after-parse field tagged `required:"true"`, and the static default is being removed specifically so an unset flag/env resolves dynamically instead of to a fixed string.

### 3. Add `resolveConfigPath` helper + wire it into `App.Run` in `pkg/cli.go`

```go
// resolveConfigPath returns explicit unchanged if non-empty (an explicit
// --config-path flag or CONFIG_PATH env value always wins). If explicit is
// empty, it resolves the default via FindConfigDir + "config.yaml".
func resolveConfigPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	return filepath.Join(FindConfigDir("claude-code-router"), "config.yaml")
}
```

Add this as a package-level function in `pkg/cli.go` (not a method — it takes no receiver state). Add the `"path/filepath"` import to `pkg/cli.go`.

Modify `App.Run` to call it before using `a.ConfigPath`:

```go
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
```

Note the log line and `serverFactory` call now use the local `configPath` variable (the resolved value), not `a.ConfigPath` (which may still be empty at this point — `App` itself is not mutated).

### 4. Update `README.md`

In the install section (~line 20-27), change:

```
2. Create the config at `~/.claude-code-router/config.yaml`:

   ```bash
   mkdir -p ~/.claude-code-router
   cp docs/config.example.yaml ~/.claude-code-router/config.yaml
   chmod 600 ~/.claude-code-router/config.yaml
```

To present the XDG path as primary:

```
2. Create the config at `~/.config/claude-code-router/config.yaml` (XDG; falls back to the legacy `~/.claude-code-router/config.yaml` if that's the only one present):

   ```bash
   mkdir -p ~/.config/claude-code-router
   cp docs/config.example.yaml ~/.config/claude-code-router/config.yaml
   chmod 600 ~/.config/claude-code-router/config.yaml
```

Keep everything else in that section (the pasted example YAML that follows) unchanged.

### 5a. Update `docs/config.example.yaml`

Line 2 currently reads:

```
# Copy to ~/.claude-code-router/config.yaml and chmod 600
```

Change to:

```
# Copy to ~/.config/claude-code-router/config.yaml and chmod 600 (XDG; falls back to legacy ~/.claude-code-router/config.yaml if that's the only one present)
```

Leave line 8-9 (the trace-dir comment: `# When true, writes one JSON file per request to ~/.claude-code-router/trace/`) unchanged — trace dir is out of scope per requirement 6.

### 5b. Update `docs/config.md`

Change (~line 1-8):

```
# claude-code-router config

The router loads its provider list from a YAML file. Default path:

```
~/.claude-code-router/config.yaml
```

Override with `--config-path` or `CONFIG_PATH` env var.
```

To:

```
# claude-code-router config

The router loads its provider list from a YAML file. Default path (XDG):

```
~/.config/claude-code-router/config.yaml
```

Falls back to the legacy `~/.claude-code-router/config.yaml` if the XDG directory doesn't exist yet but the legacy one does.

Override with `--config-path` or `CONFIG_PATH` env var — an explicit value always wins over both defaults.
```

### 6. Do NOT touch these (explicitly out of scope)

- `pkg/factory/factory.go` and any `~/.claude-code-router/trace/` reference — trace output is runtime state, not config.
- `docs/debug.md` (trace-dir + hot-reload command references) — genuinely trace-dir or hot-reload illustrative commands, unaffected by the default-path change.
- `docs/dark-factory-integration.md` — its `~/.claude-code-router/config.yaml` references ARE genuine default-config-path mentions (not just trace-dir), but nothing there breaks: the legacy path stays a valid fallback location, so the doc's guidance remains true as written. Leave it as-is; do not treat "leave as-is" as "these are all trace refs" — some are real config-path refs that happen to still be correct post-change.
- `docs/systemd-user-service.md`, `docs/launchd-service.md` — both show an explicit `-config-path <absolute-path>` in their example unit files, which is a user-supplied explicit path (not the default-resolution path), so it's unaffected by this change. Leave them as-is.

### 7. Add `pkg/find_config_dir_test.go`

`findConfigDirFromHome` is unexported (`package pkg`), so it's only reachable from an internal test file. The existing `pkg/config_test.go` is `package pkg_test` (external) — do not add these cases there. Create a new file:

```go
// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"os"
	"path/filepath"
	"testing"
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
			t.Errorf("expected XDG path %q, got %q (legacy should lose when both exist)", xdgPath, got)
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
```

This uses `testing.T` (stdlib), matching the internal-test-file pattern already used for other bborbe tools' `FindConfigDir` (task-watcher, vault-ui). The Ginkgo/Gomega external tests in `config_test.go` remain untouched.

### 8. Add a parse-boundary regression test for `required:"false"` + `default:""`

This is the load-bearing change: `required:"true"` + an empty-after-parse field would fail `github.com/bborbe/argument/v2`'s `ValidateRequired` at startup, which is exactly why requirement 2 changes the tag to `required:"false"`. Guard this boundary directly so a future accidental revert (someone re-adding `required:"true"` without noticing the static default is gone) fails a test instead of only failing at runtime.

Add this case to the same `pkg/find_config_dir_test.go` internal test file (do not create a separate `pkg/cli_test.go` — keep all internal-package helper tests in one file):

```go
func TestAppParsesWithConfigPathUnset(t *testing.T) {
	app := &App{}
	if err := argument.ParseArgs(context.Background(), app, []string{}); err != nil {
		t.Fatalf("expected App to parse with an unset --config-path, got error: %v", err)
	}
	if app.ConfigPath != "" {
		t.Errorf("expected ConfigPath to stay empty after parse (resolution happens in Run), got %q", app.ConfigPath)
	}
}
```

Add `"context"` and `"github.com/bborbe/argument/v2"` to this test file's imports (both already used elsewhere in the module — no new dependency). If `App`'s zero-value construction needs a non-nil `serverFactory` field to satisfy `argument.ParseArgs` (check — `ParseArgs` only touches tagged fields, so it should not), adjust to `NewApp(func(ctx context.Context, listen, configPath string) (librun.Func, error) { return nil, nil })` instead of `&App{}` if the bare struct literal doesn't compile due to the unexported `serverFactory` field being required by a constructor elsewhere in the package — use whichever compiles cleanly in this package.

### 9. Update `CHANGELOG.md`

`CHANGELOG.md` currently has no `## Unreleased` section — it goes straight from the file header to `## v0.18.1`. Insert a new section above `## v0.18.1`:

```markdown
## Unreleased

- feat: migrate default config path to XDG `~/.config/claude-code-router/config.yaml`, falling back to the legacy `~/.claude-code-router/config.yaml` when only that exists. New installs land in the XDG location; existing installs keep working unchanged. `--config-path` / `CONFIG_PATH` still override unconditionally.
```

### 10. Run `make precommit`

Run in the repo root. Fix any lint/format/addlicense issues that surface.

</requirements>

<constraints>
- **Backward compatibility.** Any existing `~/.claude-code-router/config.yaml` install must keep working with zero manual migration — `FindConfigDir` returns the legacy path whenever the XDG dir doesn't exist yet but the legacy one does.
- **Explicit override always wins.** An explicit `--config-path` flag value or `CONFIG_PATH` env value must never be silently replaced by the resolved default. `resolveConfigPath` only kicks in when `a.ConfigPath` is the empty string after argument parsing.
- **No `os.UserConfigDir()`.** That resolves to `~/Library/Application Support` on macOS — wrong for this project's `~/.config/<tool>/` XDG convention. Use `os.UserHomeDir()` + `filepath.Join(home, ".config", toolName)` explicitly.
- **Trace dir is out of scope.** Do not touch `pkg/factory/factory.go` or any `~/.claude-code-router/trace/` reference — it's runtime output, not config, and is explicitly excluded from this migration (mirrors dark-factory's own convention of leaving `container.lock` untouched).
- **No new dependencies.** `os` and `path/filepath` are already imported in `pkg/config.go`; `pkg/cli.go` needs only the stdlib `path/filepath` import added.
- **`Load`'s signature and tilde-expansion behavior are unchanged.** `FindConfigDir`'s output is always an absolute path built from `os.UserHomeDir()` — it never needs `expandTilde`, and `Load` itself is not modified by this prompt.
- **Do NOT commit.** dark-factory handles git.
- **Existing tests must still pass** — the existing `Describe("Config")` specs in `config_test.go` and any other existing test file must continue to pass unchanged after these additions.
</constraints>

<verification>

```bash
cd /workspace
make precommit
```

Must pass. Additionally:

```bash
cd /workspace
go test ./pkg/... -v -count=1 2>&1 | tail -80
```

Expect: all existing specs pass + the new `TestFindConfigDirFromHome` subtests (XDG-exists, legacy-only, both-exist-XDG-wins, neither-exists) + the `resolveConfigPath` cases (explicit-wins, empty-resolves-to-FindConfigDir) all pass.

```bash
grep -n 'FindConfigDir\|resolveConfigPath' /workspace/pkg/config.go /workspace/pkg/cli.go
```

Expect: `FindConfigDir` and `findConfigDirFromHome` defined in `config.go`; `resolveConfigPath` defined and called from `App.Run` in `cli.go`.

```bash
grep -n 'default:""' /workspace/pkg/cli.go
grep -n 'default:"~/.claude-code-router/config.yaml"' /workspace/pkg/cli.go
```

Expect: the first grep matches the `ConfigPath` tag; the second grep matches nothing (the static default is fully removed).

```bash
grep -n '\.config/claude-code-router' /workspace/README.md /workspace/docs/config.md
```

Expect: at least one match in each file (XDG path now documented as primary).

```bash
grep -n '## Unreleased' /workspace/CHANGELOG.md
```

Expect: one match, directly above `## v0.18.1`.

</verification>

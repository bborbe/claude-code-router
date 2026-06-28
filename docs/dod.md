# Definition of Done

After completing the implementation, review your own changes against each criterion below. These are quality checks you perform by inspecting your work — not commands to run (`make precommit` already ran via `validationCommand`). Report any unmet criterion as a blocker.

## Code Quality

- Exported types, functions, and interfaces have GoDoc comments
- Error handling follows `bborbe/errors` conventions where applicable (no silently ignored errors; no bare `return err`)
- No debug `fmt.Printf` / `println` — use `glog.V(n).Infof(...)` for structured logging

## Testing

- New code has Ginkgo/Gomega test coverage (target ≥ 80% on new pkg/<package>)
- Changes to existing code have tests covering at least the changed behavior
- Tests live in `*_test.go` next to the code; suite test files (`*_suite_test.go`) follow the existing pattern

## Architecture

- Package layout follows [Go Package Layout Guide](https://github.com/bborbe/coding-guidelines/blob/master/go-package-layout-guide.md): flat `pkg/<repo-name>` with required `pkg/factory/` + `pkg/handler/` splits
- All `Create*` wiring lives in `pkg/factory/`
- Single-source-of-truth for config: `pkg/config/Config` struct + `Validate()`; no inline parse logic elsewhere

## Dependencies

- If `make precommit` fails due to a dependency vulnerability with a known fix version, update the dependency (`go get <pkg>@<fixed-version> && go mod tidy`) as part of your change
- `go install github.com/bborbe/claude-code-router@latest` works
- No `exclude` or `replace` directives in `go.mod`

## Documentation

- `README.md` updated if the change affects usage, configuration, or install steps
- `docs/config.md` updated if the change adds/removes/renames a YAML field
- `docs/config.example.yaml` updated if the change adds a new YAML field operators would copy
- `CHANGELOG.md` has an entry under `## Unreleased`
- When changing CLI args, config fields, env vars, or flags (add, rename, remove, change default), grep the entire repo and update all references in `docs/`, `README.md`, examples, and comments

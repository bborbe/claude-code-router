---
status: completed
spec: [021-config-env-expansion]
summary: Added expandEnvRefs to pkg/config.go so ${NAME} references in a config file resolve from the process environment at every Load and SIGHUP reload, with Ginkgo coverage for all ACs and no secret in any error path.
execution_id: claude-code-router-env-expand-exec-066-spec-021-env-expansion
dark-factory-version: v0.196.0
created: "2026-10-12T08:46:00Z"
queued: "2026-10-12T09:04:29Z"
started: "2026-10-12T09:04:31Z"
completed: "2026-10-12T09:10:53Z"
branch: dark-factory/config-env-expansion
---

# Config: resolve `${NAME}` references from the environment at load

<summary>
- The router's config file can now *name* a secret instead of containing it.
- A `${NAME}` occurring anywhere in the config file text is replaced at load time by the value of environment variable `NAME`.
- Writing `$${` renders a literal `${` and performs no lookup, so a value or a comment can carry a literal reference.
- A `$` that starts neither a reference nor an escape is passed through untouched, so a bare `$` and a bare `$$` survive verbatim.
- A reference to a variable that is not set fails the load, naming the variable and never its value.
- A malformed reference — an unterminated `${`, or a name that is not a valid environment identifier — fails the load and names the byte offset.
- Expansion is applied to the document as a whole, so a field or a comment anywhere in the file is covered, and a field added later is covered with no change here.
- Expansion is a single pass: a value that itself contains `${OTHER}` is not expanded again.
- A reload (SIGHUP) re-runs expansion against the environment as it stands then, so a rotated environment is picked up without a config edit.
- A config file that contains no `${` loads to exactly the same struct as before.
</summary>

<objective>
Give the router the ability to read a secret from its own process environment at config-load time, so an operator can remove the provider tokens and inbound API keys from the plaintext config file. The end state is: `${NAME}` anywhere in the config file text resolves to environment variable `NAME` at every load and every reload, an unset variable fails the load by name, and a config with no reference behaves byte-for-byte as it does today.
</objective>

<context>
- Repo root is the current working directory. Use repo-relative paths only.
- Single-module Go repo (`go.mod` at root, module `github.com/bborbe/claude-code-router`). This repo's `Makefile` includes only `tools.env` + `Makefile.docker`, so plain `make test` / `make precommit` is correct — there is no `ROOTDIR` override to pass. `.git` may be masked in the container: never put a bare `git` command in `<verification>`.
- **Read `pkg/config.go` fully.** The package is `pkg`. `Load` is the single entry point and today reads:
  ```go
  func Load(ctx context.Context, rawPath string) (*Config, error) {
  	expanded, err := expandTilde(rawPath)
  	if err != nil {
  		return nil, errors.Wrapf(ctx, err, "expand path %q", rawPath)
  	}
  	data, err := os.ReadFile(expanded) //nolint:gosec // operator-provided path
  	if err != nil {
  		return nil, errors.Wrapf(ctx, err, "read config %q", expanded)
  	}
  	c := &Config{}
  	if err := yaml.Unmarshal(data, c); err != nil {
  		return nil, errors.Wrapf(ctx, err, "parse config %q", expanded)
  	}
  	if err := c.Validate(ctx); err != nil {
  		return nil, errors.Wrapf(ctx, err, "validate config %q", expanded)
  	}
  	return c, nil
  }
  ```
  `pkg/config.go` already imports `context`, `fmt`, `net`, `net/url`, `os`, `path`, `path/filepath`, `strings`, `stdtime "time"`, `github.com/bborbe/errors`, `libtime "github.com/bborbe/time"`, `github.com/golang/glog`, and `yaml "gopkg.in/yaml.v3"`. This change additionally needs `regexp` (for the package-level `envRefNamePattern` var in requirement 1c), and `strconv` only if you build the offset text with it. Both are standard library, so the "No new dependencies" constraint still holds — add them to the import block.
- **Both call sites already go through `Load`** — verified in this tree, no production change needed in either:
  - `pkg/factory/factory.go`, `CreateServer`: `cfg, err := pkg.Load(ctx, configPath)`.
  - `pkg/reloader/reloader.go`, `(*Reloader).Reload`: `cfg, err := pkg.Load(ctx, r.cfgPath)`.
  There is no `cmd/` directory and no second `application.Run` — `main.go` builds `pkg.NewApp(factory.CreateServer)` and that is the only entry point.
- **Error-construction convention** (this repo uses `github.com/bborbe/errors`, never `fmt.Errorf`): `errors.New(ctx, "message")`, `errors.Errorf(ctx, "format %q", arg)`, `errors.Wrapf(ctx, err, "context %q", arg)`. All three already appear in `pkg/config.go` — mirror them.
- **Precedent for a ctx-taking function that uses ctx only as error metadata, not as a cancellation point:** `(*Days).UnmarshalText` in `pkg/config.go`. Its GoDoc explains that `context.Background()` feeds `bborbe/errors` purely as tracing metadata. Mirror that reasoning in the new function's GoDoc.
- **Test conventions — read before writing tests:**
  - `pkg/pkg_suite_test.go` is the Ginkgo suite entry point: `package pkg_test`, `func TestSuite(t *testing.T)`.
  - `pkg/config_test.go` is a Ginkgo `Describe("Config", ...)` in `package pkg_test`, and it is **1944 lines** against `.golangci.yml` → `revive` → `file-length-limit` → `max: 2000` (`skipBlankLines` / `skipComments` are false, so `wc -l` is the exact count). **Do NOT add rows to `pkg/config_test.go` — it would cross the limit.**
  - `package pkg_test` has a Ginkgo `TestSuite` entry point (`func TestSuite(t *testing.T)` in `pkg/pkg_suite_test.go`), so the MUST rules `go-testing/no-testing-t-direct` and `go-testing/no-stdlib-table-tests` apply to every file in it: write the expansion rows as a Ginkgo `DescribeTable`/`Entry`, never as a stdlib `t.Run` table or a bare `Test*` function. (`pkg/find_config_dir_test.go` does define plain `Test*` functions, but it is the internal `package pkg` test file — they predate the rule and are not the pattern to copy.)
  - `pkg/reloader/reloader_test.go` is a Ginkgo `Describe("Reloader", ...)` in `package reloader_test` with a `tmpDir` `BeforeEach`/`AfterEach` pair, a package-level `configYAML(...)` fixture helper, and an existing `Describe("Reload", ...)` whose `It("increments provider count from N to M on Reload")` is the exact pattern to mirror: write the fixture file, build an initial handler with `factory.CreateRouterFromConfig(context.Background(), initialCfg)`, construct `reloader.NewReloader(tmpFile, initialHandler, buildFn)` where `buildFn` calls `factory.CreateRouterFromConfig(ctx, cfg, factory.WithMetricsRegisterer(prometheus.NewRegistry()))`, call `rel.SeedConfig(initialCfg)`, then call `rel.Reload(context.Background())` and read `rel.ConfigSnapshot()`.
- Coding plugin docs (in-container paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `bborbe/errors` API, never `fmt.Errorf`, never `context.Background()` inside `pkg/`.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — suite files, external `_test` package, and the MUST rules `go-testing/no-testing-t-direct` and `go-testing/no-stdlib-table-tests`: in a package that has a `TestSuite` entry point, use Ginkgo `DescribeTable`/`Entry` and never stdlib `t.Run` tables or direct `*testing.T` use.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — GoDoc comments on every exported symbol and on unexported helpers that carry non-obvious behaviour.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-precommit.md` — linter limits (`funlen 80`, `nestif 4`, `golines --max-len=100`).
  - `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md` — coverage and verification rules.
</context>

<requirements>
1. **Add one unexported helper to `pkg/config.go`**, named exactly `expandEnvRefs`, with exactly this signature:

   ```go
   func expandEnvRefs(ctx context.Context, data []byte) ([]byte, error)
   ```

   It returns the expanded bytes, or a non-nil error. It MUST use `os.LookupEnv` (not `os.Getenv` and never `os.ExpandEnv`) so that a variable that is *unset* is distinguishable from one that is *set to the empty string*. Give it a GoDoc comment stating: the syntax it implements; that it is a single left-to-right pass over the input and never re-scans an inserted value; that the offsets it reports are 0-based byte offsets into `data`; and that `ctx` feeds `bborbe/errors` as tracing metadata only, mirroring the `(*Days).UnmarshalText` GoDoc precedent.

   **Algorithm — a single left-to-right scan over `data`.** Maintain an output accumulator (`strings.Builder` or a `[]byte`) and an index `i`. At each position:

   a. If `data[i]` is not `'$'`: append `data[i]`, advance `i` by 1.

   b. If `data[i]` is `'$'` and the three bytes at `i` are `'$'`, `'$'`, `'{'`: append the two bytes `"${"`, advance `i` by 3. No lookup happens. (This is the only escape; it is why a comment in a real config must write `$${NAME}`.)

   c. Otherwise, if `data[i]` is `'$'` and `data[i+1]` is `'{'`: this begins a reference.
      - Find the next `'}'` at index `j > i+1`. If there is no `'}'` in the remainder of `data`, return an error that names the 0-based byte offset `i` (the offset of the `'$'` that opened the reference). Do not append anything.
      - Let `name = data[i+2 : j]`. Validate `name` against a package-level `regexp.MustCompile` var declared next to the helper (e.g. `var envRefNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)`); `pkg/config.go` already carries a package-level var of this shape (`weekdayNames`). If `name` does not match, return an error that names the byte offset `i`.
      - Call `os.LookupEnv(string(name))`. If the variable is **unset**, return an error that names the variable and the byte offset `i`, and that contains **no environment value at all**. If it is set — including set to the empty string — append the value verbatim, advance `i` to `j+1`, and continue. The appended value is **never** re-scanned.
   d. Otherwise (`'$'` not followed by `'{'` and not followed by `'$'` + `'{'`): append `'$'`, advance `i` by 1.

   Consequences that are acceptance criteria, so make sure the code has them:
   - `$$` (not followed by `{`) is passed through as the two literal bytes `$$`; a lone `$` is passed through as `$`; `$` at end of input is passed through as `$`.
   - `$${NAME}` yields the four-plus bytes `${NAME}` and performs no lookup.
   - A value that itself contains `${OTHER}` is emitted verbatim and is not expanded (requirement is single-pass).
   - A variable set to `""` resolves to the empty string and the load succeeds.

   **Error-message content contract** (the tests assert on substrings, so the wording is yours — but the content is fixed):
   - The unset-variable error MUST contain the variable name and the decimal byte offset, and MUST NOT contain any environment value.
   - The invalid-name error MUST contain the decimal byte offset. You MAY also name the offending token.
   - The unterminated-reference error MUST contain the decimal byte offset.
   - Never include the referenced value in any error, log line, or trace file (spec Constraints + Security).

2. **Wire the helper into `Load` in `pkg/config.go`**, between the successful `os.ReadFile` and `yaml.Unmarshal`. The call replaces `data` with the expanded bytes:

   ```go
   	data, err := os.ReadFile(expanded) //nolint:gosec // operator-provided path
   	if err != nil {
   		return nil, errors.Wrapf(ctx, err, "read config %q", expanded)
   	}
   	data, err = expandEnvRefs(ctx, data)
   	if err != nil {
   		return nil, errors.Wrapf(ctx, err, "expand env refs in config %q", expanded)
   	}
   ```

   This is the only production change. Do NOT change `expandTilde`, `Validate`, `normalizeUpstreams`, `UnmarshalYAML`, or any other function in `pkg/config.go`. Do NOT touch `pkg/factory/` or `pkg/reloader/` production code — both already call `pkg.Load`, so both paths (initial load and SIGHUP reload) get expansion for free. Do NOT add a config field, a struct field, a yaml tag, or a validation branch: this change adds no schema surface at all.

3. **Add `pkg/config_expand_test.go`** (a NEW file — do not add to `pkg/config_test.go`, which is 1944 lines against a 2000-line `file-length-limit`). Start it with the same two-line BSD copyright header every other `.go` file in this repo carries (`Copyright (c) 2026 Benjamin Borbe All rights reserved.` / `Use of this source code is governed by a BSD-style license that can be found in the LICENSE file.`). Declare `package pkg_test`. Imports: `context`, `os`, `path/filepath`, `strconv`, `strings`, `. "github.com/onsi/ginkgo/v2"`, `. "github.com/onsi/gomega"`, and `pkgcfg "github.com/bborbe/claude-code-router/pkg"` (that alias is what `pkg/config_test.go` uses). Do NOT import `testing` and do NOT declare a `Test*` function in this file — the existing `TestSuite` entry point in `pkg/pkg_suite_test.go` runs this file's specs.

   Write **one Ginkgo `DescribeTable`** — `DescribeTable("expandEnvRefs", func(...) { ... }, Entry(...), Entry(...))` — run by the existing `TestSuite` entry point, so that the spec's AC1 evidence command `go test ./pkg/... -ginkgo.focus=expandEnvRefs` executes these rows. Do NOT write a stdlib `t.Run` table test: `package pkg_test` has a Ginkgo `TestSuite`, and the MUST rules `go-testing/no-testing-t-direct` and `go-testing/no-stdlib-table-tests` forbid it (see the testing-guide pointer in `<context>`). Each `Entry` writes its fixture to a file under a directory made with `os.MkdirTemp`, cleaned up via `DeferCleanup(os.RemoveAll, dir)`, and calls `pkgcfg.Load(context.Background(), path)`; every assertion goes through `Load`, never through a `Config` struct literal, because the whole point is the load path. Set a variable with `os.Setenv(name, value)` plus `DeferCleanup(os.Unsetenv, name)`. For a variable the row needs *unset*, call `_ = os.Unsetenv(name)` at the top of the `Entry` body and set nothing — an empty value is a *set* variable, so `os.Setenv(name, "")` does NOT produce the unset state such a row asserts.

   Base fixture: a minimal valid config, e.g.
   ```yaml
   router:
     default_provider: anthropic
   providers:
     anthropic:
       upstream: https://api.anthropic.com
       token: "${ROUTER_EXPAND_TOKEN}"
       models: ["claude-*"]
   ```
   Rows required (grouped for readability; each must be its own `Entry` so a failure names the case):
   - **AC1** — `ROUTER_EXPAND_TOKEN` set to `expanded-token-value`; assert `err == nil` and `cfg.Providers["anthropic"].Token == "expanded-token-value"` (and, since `Validate` copies the token onto the synthesized single pool member, also assert `cfg.Providers["anthropic"].Upstreams[0].Token == "expanded-token-value"`).
   - **AC2** — `ROUTER_EXPAND_UNSET` deliberately unset; fixture token `"$${ROUTER_EXPAND_UNSET}"`; assert `err == nil` and `Token == "${ROUTER_EXPAND_UNSET}"` (the literal text, no lookup).
   - **AC3** — `ROUTER_EXPAND_MISSING` unset; fixture token `"${ROUTER_EXPAND_MISSING}"`; assert `err != nil`; assert `strings.Contains(err.Error(), "ROUTER_EXPAND_MISSING")`; **negative**: set a canary variable to a distinctive value (e.g. `ROUTER_EXPAND_CANARY=leak-canary-8f3a2b`) and assert the error text does NOT contain `leak-canary-8f3a2b`; additionally iterate `os.Environ()`, split each entry on the first `=`, and for every non-empty value with `len(value) >= 12` assert `!strings.Contains(err.Error(), value)` — skip values that are substrings of the fixture's own file path (`strings.Contains(configPath, value)`), because the error legitimately names the config path.
   - **Failure Modes: set-but-empty** — `ROUTER_EXPAND_EMPTY` set to `""` via `os.Setenv`; fixture token `"${ROUTER_EXPAND_EMPTY}"`; assert `err == nil` and `Token == ""` (a set-but-empty variable resolves; only an *unset* one fails).
   - **Assumption: single pass** — `ROUTER_EXPAND_INNER=should-not-appear`, `ROUTER_EXPAND_OUTER=literal ${ROUTER_EXPAND_INNER}`; fixture token `"${ROUTER_EXPAND_OUTER}"`; assert `err == nil` and `Token == "literal ${ROUTER_EXPAND_INNER}"`.
   - **AC5** — one fixture with four references: a provider `token: "${ROUTER_EXPAND_TOKEN}"`, a top-level `allowedApiKeys:` entry `"${ROUTER_EXPAND_KEY}"`, `upstream: https://${ROUTER_EXPAND_HOST}` and an `aliases:` value `"${ROUTER_EXPAND_MODEL}"` (give the provider a `models:` glob that the expanded alias target matches, e.g. `models: ["qwen*"]` with `ROUTER_EXPAND_MODEL=qwen3.6`, so `validateAliases` logs no unmatched-alias warning). Set all four variables and assert all four expanded values appear in the loaded config (`Token`, `cfg.AllowedApiKeys[0]`, `Upstreams[0].Upstream`, `cfg.Aliases[...]`).
   - **AC6a** — fixture token `"${ROUTER_EXPAND_UNCLOSED"` (no closing brace); assert `err != nil` and that `err.Error()` contains `strconv.Itoa(strings.Index(fixture, "${ROUTER_EXPAND_UNCLOSED"))` — compute the expected offset from the fixture string, do not hard-code a number.
   - **AC6b** — fixture token `"${not-a-name}"`; assert `err != nil` and that `err.Error()` contains `strconv.Itoa(strings.Index(fixture, "${not-a-name"))`.
   - **AC7** — a fixture with two providers, one carrying `token: "a$b"` and one carrying `token: "$$"`; assert `err == nil` and the two tokens are `a$b` and `$$` verbatim.
   - **Failure Modes: a reference inside a comment** — fixture whose first line is `# see ${ROUTER_EXPAND_MISSING}` with that variable unset; assert `err != nil` and the error names `ROUTER_EXPAND_MISSING` (expansion is document-wide, comments included). Add the sibling positive row: the same fixture with `# see $${ROUTER_EXPAND_MISSING}` loads with no error.

4. **Add the reload-path test to `pkg/reloader/reloader_test.go`** (AC4). Add one `It` inside the existing `Describe("Reload", func() { ... })`, following the shape of `It("increments provider count from N to M on Reload")` exactly. Steps:
   - Set `ROUTER_RELOAD_TOKEN=first-value` for the spec with `os.Setenv` and register `DeferCleanup(func() { _ = os.Unsetenv("ROUTER_RELOAD_TOKEN") })` (Ginkgo, so there is no `*testing.T` to hang `t.Setenv` on).
   - Write the fixture with the package's existing `configYAML` helper, passing `Token: "${ROUTER_RELOAD_TOKEN}"` on the single provider — the helper emits it as an unquoted `token:` scalar, and the expanded value (`first-value`) is YAML-safe, so no quoting change is needed.
   - Build the initial handler with `factory.CreateRouterFromConfig(context.Background(), initialCfg)`, construct `reloader.NewReloader(...)` with the same `buildFn` shape the neighbouring `It` uses, and call `rel.SeedConfig(initialCfg)`.
   - Call `rel.Reload(context.Background())`; assert it succeeds and `rel.ConfigSnapshot().Providers["anthropic"].Token == "first-value"`.
   - Then `os.Setenv("ROUTER_RELOAD_TOKEN", "second-value")`, call `rel.Reload(context.Background())` again, and assert `rel.ConfigSnapshot().Providers["anthropic"].Token == "second-value"` — this is the row that proves the reload path re-runs expansion against the environment as it stands at reload time.
   - One more assertion in the same `It` (or a sibling `It`) covering the Failure Modes row "a reload leaves the previous handler in place": after the two successful reloads, `os.Unsetenv("ROUTER_RELOAD_TOKEN")` and call `rel.Reload(context.Background())`; assert it returns an error and that `rel.ConfigSnapshot().Providers["anthropic"].Token` is still `"second-value"` (the previous snapshot survived the failed reload). The existing `It("rejects invalid YAML and keeps old config")` is the pattern for asserting "old config kept".

5. **Do not leak a secret.** No requirement above may be implemented in a way that writes an environment value into an error message, a `glog` line, or a trace file. The unset-variable error names the variable and the offset only. Do not add any logging to `expandEnvRefs`.

6. **Before finishing, re-run `<verification>` and confirm every command passes.** Walk requirements 1–5 against the change, and confirm `wc -l pkg/config_test.go` is still ≤ 2000.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- `pkg.Load` stays the single entry point for reading a config, and expansion happens inside it. Do NOT add a second read path, a second entry point, or a separate expansion step in `pkg/factory/` or `pkg/reloader/`.
- No new required config field and no change to the accepted config schema: a config with no `${` must load to the same struct as before. Do NOT add a struct field, a yaml tag, or a validation branch.
- No secret value may appear in a log line, an error message, or a trace file.
- The existing test suite passes unchanged. Do NOT modify existing test rows in `pkg/config_test.go`, `pkg/config_coldstart_test.go`, `pkg/config_throttle_test.go`, `pkg/reloader/reloader_test.go`, or any `*_suite_test.go` file; only add.
- Expansion reads only the process environment. Do NOT add file, network, or TeamVault access to the router. Do NOT use `os.ExpandEnv` — it cannot distinguish unset from empty, which AC3 requires; use `os.LookupEnv`.
- No new dependencies — the Go standard library plus the existing `bborbe/*` libraries suffice. Do NOT edit `go.mod` / `go.sum`.
- Do NOT touch `docs/` or `CHANGELOG.md` — `prompts/2-spec-021-env-expansion-docs.md` (sibling prompt 2 for this spec) owns the documentation section and the changelog entry.
- `pkg/config_test.go` must stay ≤ 2000 lines (`revive` `file-length-limit`). `pkg/config.go` is 961 lines; the addition must stay well inside the same limit.
- No AI attribution in code or comments.
- `make precommit` must remain green — run it before declaring done. Follow `docs/dod.md`.
</constraints>

<verification>
make precommit

# AC1 evidence — the expansion rows actually run and pass. A zero-match -ginkgo.focus still exits 0,
# so the spec count and the SUCCESS! line are the evidence, not the exit code alone:
go test -mod=mod -count=1 -v ./pkg/... -ginkgo.focus=expandEnvRefs > /tmp/ac1.out 2>&1
grep -qE 'Ran [1-9][0-9]* of [0-9]+ Specs' /tmp/ac1.out
grep -q 'SUCCESS!' /tmp/ac1.out

# AC3 mechanism — unset is distinguished from empty (os.LookupEnv, never os.ExpandEnv):
grep -n 'os.LookupEnv' pkg/config.go
! grep -q 'os.ExpandEnv' pkg/config.go

# The helper exists and Load calls it:
grep -n 'func expandEnvRefs' pkg/config.go
grep -n 'expandEnvRefs(ctx, data)' pkg/config.go

# The new test file exists and carries the rows; config_test.go stayed inside the limit:
test -f pkg/config_expand_test.go
grep -c 'ROUTER_EXPAND' pkg/config_expand_test.go
test "$(wc -l < pkg/config_test.go)" -le 2000

# AC4 — the reload path re-expands:
go test -mod=mod -count=1 ./pkg/reloader/...
grep -n 'ROUTER_RELOAD_TOKEN' pkg/reloader/reloader_test.go

# The existing suites still pass (./pkg/... already covers pkg/factory and pkg/reloader):
go test -mod=mod -count=1 ./pkg/...
</verification>

<!--
RESOLVED AT AUDIT (was OPEN QUESTION 1).
The first draft wrote the expansion rows as a plain top-level `TestExpand` table test, because
`go test -run` filters Go test function names and not Ginkgo spec text — so a Ginkgo-only suite
would have made the spec's literal AC1 command `go test ./pkg/... -run Expand` a silent no-op that
exits 0 without running a row. The prompt-auditor rejected that draft: `package pkg_test` has a
Ginkgo `TestSuite` entry point, so a stdlib `t.Run` table there violates the MUST rules
`go-testing/no-testing-t-direct` and `go-testing/no-stdlib-table-tests`. The rows are now a Ginkgo
`DescribeTable` run by the existing `TestSuite`, and AC1's evidence command is amended in the spec
to `go test ./pkg/... -ginkgo.focus=expandEnvRefs` with the spec count as the evidence — which is
discriminating in the way `-run Expand` was not.

OPEN QUESTION 2 (spec Verification command kept as written).
The spec's container-executable verification includes `grep -n 'os.LookupEnv' pkg/config.go`. That
places the expansion implementation in `pkg/config.go` rather than a new `pkg/config_expand.go`;
requirement 1 follows that placement. If the reviewer would rather see the helper in its own file,
the spec's grep target must change too.
-->

<!--
SCOPE NOTE (not an open question): the spec's Suggested Decomposition assigns AC4 to prompt 1, so
the reload-path test lives here rather than in a third prompt. No production change is needed in
pkg/reloader or pkg/factory for it — both already call pkg.Load, verified in this tree at
pkg/factory/factory.go (`CreateServer`) and pkg/reloader/reloader.go (`(*Reloader).Reload`).
-->

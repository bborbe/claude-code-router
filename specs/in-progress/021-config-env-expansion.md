---
status: prompted
approved: "2026-10-12T08:41:09Z"
generating: "2026-10-12T08:41:09Z"
prompted: "2026-10-12T08:56:20Z"
branch: dark-factory/config-env-expansion
---

## Summary

- The router's config file must be able to *name* a secret instead of containing it.
- `${NAME}` in the config text is replaced at load with the value of environment variable `NAME`.
- `$${` escapes a literal `${`.
- A reference to an unset variable fails the load loudly, naming the variable and never its value.
- Reload (SIGHUP) re-runs expansion, so a rotated environment is picked up without a config edit.

## Problem

The router's config carries provider tokens and inbound API keys as literals, which makes the file a plaintext secret store on disk. The two cluster deployments already avoid this — their config is rendered from TeamVault at `make apply` time into a Kubernetes Secret, so no literal reaches a repository. The local macOS launchd deployment has no apply step, so its config holds the literals and there is no mechanism by which the router could read them from anywhere else. The operator cannot remove the literals without the router gaining the ability to resolve a reference.

## Goal

A router whose config file contains no secret literal. Every secret-bearing value is a `${NAME}` reference resolved from the process environment at load and at every reload, with a reference to an unset variable failing the load by name.

## Non-goals

- The host-side launcher wrapper and the TeamVault migration of the existing config — both live outside this repository and are tracked on the parent task.
- Secret rotation.
- Any change to the cluster deployments' apply-time TeamVault rendering; that mechanism already keeps literals out of both cluster repos and stays as it is.
- Encrypting or permission-narrowing the config file.
- Adding a new required config field.

## Assumptions

- The secrets are present in the router process's environment at start and at reload. This repository's change alone removes nothing from disk; it makes removal *possible*, and the operator's launcher is what supplies the values.
- Expansion is a single pass over the config text: a value that itself contains `${OTHER}` is not re-expanded, so an environment value can never trigger a second lookup.
- The config file stays readable by its owner only. This change removes literals from it and does not alter its permissions.
- The operator authors the config, so the config is trusted input; the environment is likewise operator-controlled.

## Acceptance Criteria

- [ ] **AC1** — a `${NAME}` reference in a config value is replaced by that environment variable's value. Evidence: `go test ./pkg/... -ginkgo.focus=expandEnvRefs` exits 0, its output reports a non-zero spec count (`Ran N of M Specs`, N ≥ 1 — a zero-match focus also exits 0, so the count is the evidence), and the rows assert the loaded token equals the exported value.
- [ ] **AC2** — `$${NAME}` yields the literal text `${NAME}` and performs no lookup. Evidence: with the variable deliberately unset, the same test binary exits 0 and the row asserting the loaded token equals the literal `${NAME}` passes.
- [ ] **AC3** — a reference to an unset variable fails the load. Evidence: `Load` returns a non-nil error; `grep` of the error text finds the variable name; negative — the error text contains no value of any exported variable in the test environment.
- [ ] **AC4** — reload re-runs expansion against the current environment. Evidence: a test that loads, mutates the variable, reloads through the reloader path, and asserts the second config carries the new value.
- [ ] **AC5** — expansion is document-wide, not a whitelist of fields. Evidence: one fixture placing a reference in a provider `token`, in `allowedApiKeys`, in an `upstream`, and in an `aliases` value loads with all four expanded.
- [ ] **AC6** — a malformed reference fails the load. Evidence: `token: ${UNCLOSED` and `token: ${not-a-name}` each return an error, and the error names the byte offset.
- [ ] **AC7** — a config containing no `${` behaves exactly as before. Evidence: negative — a fixture with a bare `$` and with `$$` loads with those characters preserved verbatim.
- [ ] **AC8** — `docs/config.md` documents the syntax. Evidence: a new section exists whose body contains the `${NAME}` form, the `$${` escape, and the unset-variable failure; `grep -c '\${NAME}' docs/config.md` returns ≥1 and `grep -c '\$\${' docs/config.md` returns ≥1.
- [ ] **AC9** — `make precommit` exits 0.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — lint / vet / license / unit tests clean
- `go test ./pkg/...` — the package suite passes, including the new expansion rows
- `go test ./pkg/... -ginkgo.focus=expandEnvRefs` — the expansion rows pass (a non-zero `Ran N of M Specs` is the evidence; a zero-match focus also exits 0)
- `grep -n 'os.LookupEnv' pkg/config.go` — expansion distinguishes an unset variable from an empty one, which AC3 requires and `os.ExpandEnv` cannot provide

### Operator-executable (runs on the host after PR merge)

- `make install` — the released binary lands on the operator's PATH
- `launchctl kickstart -k gui/$UID/de.bborbe.claude-code-router` — the launchd service is replaced
- `curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:8788/healthz` — returns `200` after the restart

## Desired Behavior

1. At load, every `${NAME}` occurring in the config text is replaced by the value of environment variable `NAME`.
2. `$${` renders a literal `${` and consumes no variable lookup, so a value or a comment can carry a literal reference. The concrete consumer is the config's own comments: because expansion covers the whole document, a comment documenting the syntax must write `$${NAME}`.
3. A `$` that begins neither a `${` reference nor a `$${` escape is passed through unchanged, so a bare `$$` stays `$$`.
4. An unterminated `${` fails the load with an error naming its byte offset.
5. A reference whose name is not a valid environment identifier (`[A-Za-z_][A-Za-z0-9_]*`) fails the load.
6. A reference to an unset variable fails the load, naming the variable and never a value.
7. A reload re-runs expansion against the file and the environment as they stand at reload time.
8. Every string value in the document is covered without enumerating schema fields — expansion is applied to the document as a whole, so a field added later is covered without a change here.

## Constraints

- `pkg.Load` stays the single entry point for reading a config, and expansion happens inside it — the initial load (`pkg/factory`) and the SIGHUP reload (`pkg/reloader`) both call `pkg.Load`, so neither path needs a separate change.
- No new required config field and no change to the accepted config schema: a config with no `${` must load to the same struct as before.
- No secret value may appear in a log line, an error message, or a trace file.
- The existing test suite passes unchanged.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| A referenced variable is not set in the process environment | Load returns an error naming the variable; the router does not start and a reload leaves the previous handler in place | Export the variable and restart, or send SIGHUP to re-run the reload |
| A variable is set to the empty string | Load succeeds; the empty value is used, because the reference resolved | Operator runs `launchctl print gui/$UID/de.bborbe.claude-code-router \| grep <NAME>` and confirms the export |
| The config text contains an unterminated `${` | Load returns an error naming the byte offset | Fix the config; the escape `$${` writes a literal |
| A value legitimately needs a literal `${` | Author it as `$${`; the rendered value is `${` | — |
| A comment in the config contains `${UNSET}` | Load fails, because expansion is document-wide | Escape it as `$${UNSET}` or reword the comment |
| An expanded value contains YAML-significant characters (`#`, `: `, a leading `-`/`*`/`&`/`%`, a newline) | The document is parsed after expansion, so an unquoted reference can truncate the value at `#` or fail the parse | Author the reference as a quoted scalar — `token: "${NAME}"` — which keeps the expanded value inside a quoted scalar and preserves it verbatim. Documented in `docs/config.md` |
| The environment changes while the router is running | Nothing, until a reload | Send SIGHUP; the reload re-expands |

## Security / Abuse

- Expansion reads only the process environment, which the operator controls. No file, network, or TeamVault access is added to the router.
- Error messages name the variable, never its value, so a failed start is diagnosable without the failure itself disclosing a secret.
- The config file remains readable by its owner only; this change removes the literals from it but does not alter its permissions.
- Expansion is document-wide, including comments, so a config carrying a literal `${` anywhere must escape it. This is deliberate: a partial expansion (values only) would silently skip a field added later.
- A value that fails to expand leaves the router unstarted rather than running with a blank or truncated credential, so a missing export cannot silently degrade authentication.

## Do-Nothing Option

The local router keeps its provider tokens and inbound API keys as literals on disk, alongside twelve backup copies of the same file. Any process or user able to read the operator's home directory obtains every upstream credential the router holds, and each credential must be rotated by hand in the config rather than in the store that already exists for it. The cluster deployments stay correct, so the cost is confined to the local machine — but it is the machine that holds every credential the router uses.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Expansion helper + wire into `Load` + unit tests | 1-8 | 1-7, 9 | — |
| 2 | `docs/config.md` section | — | 8 | prompt 1 (documents the shipped syntax) |

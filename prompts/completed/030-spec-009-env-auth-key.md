---
status: completed
spec: [009-inbound-api-key-auth]
summary: 'Resolved the inbound auth key env-first from ROUTER_AUTH_KEY in CreateRouterFromConfig (falling back to auth.key when the env var is empty), with four new Ginkgo wiring tests, docs/config.md + config.example.yaml updates, and a CHANGELOG ## Unreleased entry'
execution_id: claude-code-router-exec-030-spec-009-env-auth-key
dark-factory-version: dev
created: "2026-08-17"
queued: "2026-08-17T17:54:34Z"
started: "2026-08-17T17:54:36Z"
completed: "2026-08-17T18:01:36Z"
---

# Resolve the inbound auth key from ROUTER_AUTH_KEY env var (TeamVault via launchd wrapper)

<summary>
- The inbound auth key can come from the `ROUTER_AUTH_KEY` environment variable instead of a literal in `config.yaml`.
- When the env var is set it wins; when it is empty the config's `auth.key` value is used as today.
- The config therefore never has to contain the raw secret — the launchd wrapper resolves it from TeamVault into the env var at startup.
- A config with no auth block but a set `ROUTER_AUTH_KEY` still enables auth.
- Existing configs that hold a literal key keep working unchanged.
</summary>

<objective>
Let the operator keep the router key out of the config file entirely: a launchd wrapper fetches it from TeamVault and injects it as `ROUTER_AUTH_KEY`; the router reads that env var first and only falls back to the config's `auth.key` literal when the env var is empty.
</objective>

<context>
- Repo root is the current working directory.
- Read `pkg/factory/factory.go` around lines 190-197 — the `authKey` resolution block in `CreateRouterFromConfig` currently reads `cfg.Auth.IsEnabled()` → `cfg.Auth.Key`. This is the single place the change lands.
- Read `pkg/factory/factory_test.go` (or the test file the previous auth prompt created) for the existing config → authKey test shape; extend it rather than writing a parallel one.
- Read `docs/config.md` § Inbound auth — add a paragraph documenting the env-var precedence.
- The env var name is fixed: `ROUTER_AUTH_KEY`.
- Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `bborbe/errors`, never `fmt.Errorf`.
</context>

<requirements>
1. In `pkg/factory/factory.go`, change the `authKey` resolution so the environment wins:
   - If `os.Getenv("ROUTER_AUTH_KEY")` is non-empty, `authKey` is that value, regardless of the config's auth block.
   - Else if `cfg.Auth.IsEnabled()`, `authKey` is `cfg.Auth.Key` (unchanged fallback).
   - Else empty (auth disabled).
   `os` is already imported; verify before adding. Do not call `os.LookupEnv` with a different name — the name is fixed.

2. Keep `buildMux(modelRouter, cfg.Trace, authKey)` and the auth middleware unchanged. This change is purely the source of `authKey`.

3. Tests with Ginkgo v2 + Gomega:
   - `ROUTER_AUTH_KEY` set + config auth.key set → `CreateRouterFromConfig`'s resulting mux requires the env value (present `x-router-key: <env-value>` passes, `x-router-key: <config-value>` gets 401). Use `t.Setenv` / `os.Setenv` with `DeferCleanup(os.Unsetenv, "ROUTER_AUTH_KEY")`.
   - `ROUTER_AUTH_KEY` empty + config auth.key set → config value is required (present config value passes).
   - `ROUTER_AUTH_KEY` set + config has NO auth block → auth still enabled with the env value.
   - `ROUTER_AUTH_KEY` empty + no config auth → disabled (no auth middleware effect; a keyless non-loopback request passes through).
   Follow the existing wiring-test exemplar `pkg/factory/trace_wiring_test.go` (config → `CreateRouterFromConfig` → request via `httptest.NewRequest` + `mux.ServeHTTP`).

4. Update `docs/config.md` § Inbound auth: document that `auth.key` in the config may be either a literal key or simply a non-empty marker, and that the runtime value is taken from `ROUTER_AUTH_KEY` when set (the launchd wrapper injects it from TeamVault). State that the raw secret should live in TeamVault, never in the config file.

5. Do NOT modify the auth middleware, the admin guard, or any other route. This is a one-line resolution change plus tests/docs.
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Do NOT log or echo the `ROUTER_AUTH_KEY` value at any level.
- Do NOT write the resolved value back into `config.yaml` or any file.
- Do NOT change the `auth.key` config schema (still a string field). Existing literal-key configs must keep working.
- Do NOT add a new external dependency.
- Use `github.com/bborbe/errors` if any wrapping is needed (likely none — this is a plain env read).
- `make precommit` must remain green.
</constraints>

<verification>
make precommit

# Confirm env-first resolution is wired:
grep -n 'ROUTER_AUTH_KEY' pkg/factory/factory.go

# Confirm the env value is never logged (no echo of it anywhere):
grep -rn 'ROUTER_AUTH_KEY' pkg/ | grep -iE 'log|printf|fmt\.Print' || true   # expected: no output

go test ./pkg/... -count=1
</verification>
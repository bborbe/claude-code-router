---
status: completed
summary: Added NewHelloHandler serving Claude Code's /api/hello connectivity probe with a bare 200, registered it in buildMux above the / catch-all, added handler + factory wiring tests, and a CHANGELOG Unreleased bullet.
execution_id: claude-code-router-hello-exec-035-api-hello-handler
dark-factory-version: dev
created: "2026-08-18T11:35:00Z"
queued: "2026-08-18T09:57:11Z"
started: "2026-08-18T09:57:42Z"
completed: "2026-08-18T10:00:15Z"
---

# Serve Claude Code /api/hello connectivity probe

<summary>
- Claude Code sessions periodically probe the proxy's /api/hello path as a connectivity check
- The router has no route for that path, so every probe falls through to the unknown-path handler
- The log fills with roughly one 404 line per second while multiple sessions run
- The 404 noise buries the real unknown-path signals the logger exists to surface
- A dedicated handler now answers the probe with 200 OK
- The unknown-path logger keeps working for every other unmatched route
- Existing routes and request routing are unchanged
</summary>

<objective>
Claude Code HEAD-probes `{ANTHROPIC_BASE_URL}/api/hello` as a connectivity check; without a matching route each probe lands in the unknown-path 404 logger and floods the log. Serve the probe with a bare 200 so the unknown-path log carries signal again.
</objective>

<context>
Read CLAUDE.md for project conventions.
Read `pkg/handler/root-liveness.go` — the exact precedent: Claude Code's `HEAD /` base-URL probe got the same bare-200 treatment.
Read `pkg/handler/root-liveness_test.go` and `pkg/handler/handler_suite_test.go` for the test pattern.
Read `pkg/factory/factory.go` around `buildMux()` — route registration order (`HEAD /{$}` root-liveness above the `/` catch-all).
</context>

<requirements>
1. Create `pkg/handler/hello.go`: `NewHelloHandler() http.Handler` mirroring the `NewRootLivenessHandler` shape — returns 200 OK, no body, no logging. GoDoc comment explains why: Claude Code HEAD-probes `/api/hello` as a connectivity check; without this handler the probe falls through to `NewNotFoundHandler`.
2. Register the route in `buildMux()` in `pkg/factory/factory.go`, next to the `HEAD /{$}` root-liveness registration and above the `mux.Handle("/", ...)` catch-all. Cover at least `HEAD /api/hello`; a path-only pattern (`/api/hello`, any method) is acceptable.
3. Create `pkg/handler/hello_test.go` following `root-liveness_test.go`: Ginkgo `Describe`/`It`, assert the handler returns `http.StatusOK` with an empty body (add a GET case if the pattern is method-agnostic). Add a factory-level routing assertion in a new or existing `pkg/factory/*_wiring_test.go` that drives `HEAD /api/hello` through `factory.CreateRouterFromConfig` and asserts 200 OK (pattern: `pkg/factory/admin_loopback_guard_wiring_test.go`). This proves the route is registered, has the right pattern, and wins over the `/` catch-all — which the handler-only test cannot.
4. Add a CHANGELOG.md bullet under `## Unreleased` describing the fix. If no `## Unreleased` section exists yet, create it at the top of the file (above the latest released version) following the project's existing convention.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git
- Existing tests must still pass
- No new dependencies
- Do not touch routing/fallback logic, the unknown-path logger, or any other route
- Follow the existing BSD license header convention on new files
</constraints>

<verification>
Run `make precommit` -- must pass.
</verification>

---
status: completed
approved: "2026-06-30T11:56:43Z"
generating: "2026-06-30T12:02:30Z"
prompted: "2026-06-30T12:02:30Z"
verifying: "2026-06-30T12:22:27Z"
completed: "2026-06-30T13:47:00Z"
branch: dark-factory/enabletrace-endpoint
---

## Summary

- Add two operator-local HTTP endpoints, `POST /enabletrace` and `POST /disabletrace`, that toggle per-request trace logging on and off without a router restart.
- `enabletrace` turns tracing on for a bounded 5-minute window that auto-disables on expiry; `disabletrace` turns it off immediately mid-window and cancels the pending timer so no late reset flips tracing back on.
- Repeated `enabletrace` calls reset the window deterministically (cancel prior timer, start fresh).
- The existing `trace:` config flag stays as a deprecated always-on opt-in; the new toggle is a process-internal atomic boolean independent of config reload.
- Mirrors the existing `/setloglevel/{level}` handler pattern: no auth, registered in the operator-local mux, listener on `127.0.0.1:8788` only.

## Problem

Today trace logging is gated by a top-level `trace:` boolean in the config YAML, read once at load. Turning tracing on or off requires editing the config and restarting the launchd/systemd service — which drops in-flight Claude Code requests and breaks the active session. When an operator wants to capture one problematic `/v1/messages` exchange for diagnosis, they either leave tracing on permanently (filling disk, capturing every request's body verbatim) or restart the router mid-session (losing the very request they wanted to inspect because the session reconnects after the restart). There is no bounded "trace for 5 minutes, then stop" path. This matters because trace files contain full request/response bodies, so leaving tracing on indefinitely is a disk and privacy hazard, but restarting to toggle it destroys the context the operator is trying to capture.

## Goal

An operator can turn tracing on for a bounded window, capture the problematic request without restarting the router, and have tracing turn itself off automatically so no trace files accumulate if the operator forgets to disable it. The trace toggle is process-internal state that does not depend on config reload, SIGHUP, or the `atomic.Pointer[http.Handler]` mux swap used by other config changes.

## Non-goals

- Do NOT remove the `trace:` config flag — deprecate only; removal is a separate task after a deprecation cycle. Still parsed, still works as an always-on opt-in.
- Do NOT persist trace-enable state across restarts — the atomic boolean resets to off on every launchd/systemd restart by design.
- Do NOT make the 5-minute TTL configurable via config file, query param, or request body — 5 minutes is the hardcoded v1 constant. (Test-only override via `TRACE_TTL` env var is internal, not a production knob.)
- Do NOT add auth to `/enabletrace` or `/disabletrace` — operator-local trust model, same as `/setloglevel`.
- Do NOT trace non-`/v1/*` paths (healthz, readiness, metrics, admin endpoints) — unchanged from v0.14.0 scope.
- Do NOT add retention, rotation, or cleanup of the trace directory — still operator-managed via `rm`, per v0.14.0.
- Do NOT move other config changes onto the atomic-boolean toggle pattern — only the trace toggle moves off the conditional-mount model; other config still flows through `atomic.Pointer[http.Handler]` swap or restart.
- Do NOT add a TTL-duration config knob or query-param tunable — invariant; if a future consumer demands a different window, that is a separate spec.

## Acceptance Criteria

- [ ] `make precommit` exits 0 in the repo root — evidence: exit code
- [ ] `curl -X POST 127.0.0.1:8788/enabletrace` returns a 2xx HTTP status, and a subsequent `/v1/*` request writes one JSON trace file under `~/.claude-code-router/trace/` — evidence: HTTP status + `ls ~/.claude-code-router/trace/*.json` returns ≥1 file not present before the call
- [ ] `curl -X POST 127.0.0.1:8788/disabletrace` returns a 2xx HTTP status, and a subsequent `/v1/*` request writes NO new trace file — evidence: HTTP status + `ls ~/.claude-code-router/trace/*.json` count unchanged before vs after
- [ ] After the 5-minute TTL elapses with no manual `/disabletrace`, a `/v1/*` request writes NO trace file — evidence: unit test uses `TRACE_TTL` env var override to shorten the window and asserts `ls` count unchanged post-expiry; live launchd smoke uses the real 5-minute clock and asserts the same
- [ ] `curl -X POST .../disabletrace` sent mid-window cancels the TTL timer: a `/v1/*` request sent after the original 5-minute window would have elapsed writes NO trace file (no late reset flips tracing back on) — evidence: unit test with `TRACE_TTL` override asserts `ls` count unchanged at the would-be expiry time
- [ ] Repeated `enabletrace` calls reset the window: each call cancels the prior timer and starts a fresh 5-minute window — evidence: unit test asserts the prior timer's `Stop()` returned `true` (was still active) exactly once per reset, and at most one live timer exists after N consecutive `enabletrace` calls
- [ ] With legacy `trace: true` in the config and NO `/enabletrace` call, every `/v1/*` request writes a trace file regardless of the atomic boolean — evidence: `ls ~/.claude-code-router/trace/*.json` returns ≥1 file per `/v1/*` request without any toggle call
- [ ] Default state at boot with `trace:` absent/false: no trace files written and no TTL goroutine running — evidence: `ls ~/.claude-code-router/trace/*.json` returns 0 files (or no dir) after a `/v1/*` request; unit test asserts zero TTL goroutines
- [ ] Token-leak invariant holds: `grep -REin 'Bearer |sk-' ~/.claude-code-router/trace/` returns 0 lines containing a raw secret (only `***` redactions) — evidence: negative grep returning zero matches
- [ ] `docs/config.md` documents the `/enabletrace` and `/disabletrace` endpoints, the 5-minute TTL, and the deprecation of the `trace:` flag — evidence: `grep -n 'enabletrace' docs/config.md` returns ≥1 line AND `grep -n 'disabletrace' docs/config.md` returns ≥1 line AND `grep -ni 'deprecat' docs/config.md` returns ≥1 line
- [ ] `CHANGELOG.md` gains an entry for this change under a heading at the top of the file (either `## Unreleased` if that pattern is adopted, or a new version section above `## v0.14.0`) — evidence: `grep -ni 'enabletrace\|trace.*ttl\|disabletrace' CHANGELOG.md` returns ≥1 line at or above the line number of the v0.14.0 section

## Verification

```
make precommit
```

Live smoke (operator machine, router running under launchd on `127.0.0.1:8788`):

```
# default-off check
rm -f ~/.claude-code-router/trace/*.json 2>/dev/null
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8788/v1/messages   # expect 200/4xx, NO trace file
ls ~/.claude-code-router/trace/*.json 2>/dev/null | wc -l                     # expect 0

# enable
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://127.0.0.1:8788/enabletrace   # expect 2xx
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8788/v1/messages            # expect trace file
ls ~/.claude-code-router/trace/*.json | wc -l                                         # expect ≥1
grep -REin 'Bearer |sk-' ~/.claude-code-router/trace/ | wc -l                         # expect 0 (only ***)

# disable mid-window
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://127.0.0.1:8788/disabletrace   # expect 2xx
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8788/v1/messages            # expect NO new trace file
```

Unit tests use `TRACE_TTL` env var override to shorten the 5-minute window; the live smoke above uses the real 5-minute clock for the TTL auto-disable check (wait 5 min after `enabletrace`, then send a `/v1/*` request and confirm no new trace file).

## Desired Behavior

1. A `POST /enabletrace` endpoint exists, registered in the same mux as `/setloglevel/` in `CreateRouterFromConfig`, returns a 2xx on success, and sets a process-global trace-enabled flag to true with a freshly started 5-minute TTL timer.
2. A `POST /disabletrace` endpoint exists, registered alongside `/enabletrace`, returns a 2xx on success, sets the trace-enabled flag to false, and cancels any in-flight TTL timer so no later timer expiry can flip tracing back on.
3. The trace middleware is mounted unconditionally on `/v1/` in `CreateRouterFromConfig` (no longer conditional on `cfg.Trace`); per request it consults the trace-enabled flag before writing a trace file. When the legacy `cfg.Trace` config flag is true, the middleware emits unconditionally (flag-OR-config), preserving the v0.14.0 always-on behavior.
4. The TTL timer, on expiry, calls `Disable()` (sets the flag false) — so tracing turns off automatically after 5 minutes if no `/disabletrace` arrives.
5. Repeated `enabletrace` calls are idempotent on the flag (true either way) but reset the window: each call cancels the prior in-flight timer and starts a fresh 5-minute timer. Deterministic — no overlapping timers, no "which timer wins" ambiguity.
6. At boot, with no `enabletrace` call and `trace:` absent/false, the trace-enabled flag is false, no trace files are written, and no TTL goroutine is running.
7. The trace middleware continues to redact `Authorization` and `x-api-key` request headers to `***` (case-insensitive) in every trace file — no regression from v0.14.0.
8. Log messages emitted by the toggle are lowercase (e.g. `trace enabled via endpoint`) and any `Info`-level message is gated behind `glog.V(n)` per go-glog convention.

## Constraints

- Frozen file/seam: `CreateRouterFromConfig` in `pkg/factory/factory.go` is the mux-construction site where `/setloglevel/` is registered today; `/enabletrace` and `/disabletrace` register here. This is the single wiring seam.
- Frozen pattern: the new endpoints mirror `/setloglevel/{level}` (`pkg/handler/setloglevel.go`) — operator-local, no auth, returns a short plaintext body, registered on the stdlib mux.
- Frozen invariant: `Authorization` and `x-api-key` redaction in `NewTraceMiddleware` (`pkg/handler/trace.go`) — must not regress. No raw `Bearer ` or `sk-` secret in any trace file.
- Frozen config field: `Config.Trace` (`pkg/config.go`) stays parsed and functional; deprecated in docs, not removed. `trace: true` continues to emit on every `/v1/*` request regardless of the atomic boolean.
- Frozen listener: `127.0.0.1:8788` operator-local trust model — no auth added, no remote bind.
- Frozen constant: 5-minute production TTL. The `TRACE_TTL` env var is a test-only override (parsed only in test builds or guarded so production cannot shrink the window via env); the live launchd smoke uses the real 5-minute clock.
- Frozen independence: the trace toggle uses a process-internal atomic flag with enable/disable/is-enabled semantics (concrete type and method names — agent decides at impl time; the decision is local and reversible). It does NOT use the `atomic.Pointer[http.Handler]` mux swap. No SIGHUP dependency. Independent of spec 002-sighup-hot-reload (separate worktree/PR, not yet merged).
- Frozen baseline: builds on v0.14.0 (shipped, tagged, merged to master) — `NewTraceMiddleware`, `CreateRouterFromConfig` wiring, `Config.Trace` field.
- glog conventions: `V(n)`-gated Info lines; lowercase log messages (`trace enabled via endpoint`, not `Trace Enabled`).
- Go, Ginkgo v2 + Gomega, `make precommit` gates all merges.
- `.dark-factory.yaml`: workflow `direct`, `autoGeneratePrompts: false` — manual prompt generation path after approve.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---------|-------------------|----------|
| TTL goroutine fires after process crash mid-window | Process is gone; flag is in-memory only, resets to false on next boot by design | Automatic — no persistent state to corrupt; next boot starts with tracing off and no goroutine |
| Operator calls `enabletrace` then process restarts before TTL | In-memory flag and timer vanish on restart; new boot starts with tracing off | Automatic — by-design non-persistence; operator re-calls `enabletrace` if still needed |
| Repeated concurrent `enabletrace` calls | Each cancels the prior timer and starts a fresh 5-min window; exactly one timer remains active | Self-correcting — timer management is guarded by a mutex/context-cancel so no goroutine leak |
| `disabletrace` arrives exactly as TTL fires | Disable wins: flag is false, and the cancelled timer cannot flip it back on | Automatic — `Disable()` cancels the timer; no late reset |
| Trace file write fails (disk full, permissions) | Best-effort per v0.14.0: log `glog.Warningf`, request still succeeds | Operator frees disk / fixes perms on `~/.claude-code-router/trace/`; subsequent writes succeed |
| `TRACE_TTL` env var set in production | Production path ignores it (or test-guarded) — 5-min constant is the production value | Operator unsets env var; no production impact |
| Clock skew / system suspend mid-window | TTL timer is a `time.Timer`/`context` based on monotonic clock; on resume the timer fires relative to wall time — trace may turn off earlier or later than wall-clock 5 min | Operator calls `enabletrace` again to restart the window if still needed |

## Security / Abuse Cases

- Attacker-controlled input: none — endpoints take no body and no query params; `enabletrace`/`disabletrace` are fixed no-arg POSTs. The only input is the HTTP method/path.
- Trust boundary: the endpoints are bound to `127.0.0.1:8788` only (operator-local trust model, identical to `/setloglevel`). Any caller who can reach the port is already the operator. No auth is added — adding auth here would diverge from the established `/setloglevel` pattern and is a non-goal.
- What can hang: the TTL goroutine must not leak — repeated `enabletrace` calls must cancel prior timers, not stack them. The mutex/context-cancel guard ensures deterministic single-timer state.
- Data crossing trust boundaries: trace files contain full request/response bodies (per v0.14.0) written to `~/.claude-code-router/trace/` with `0o600` perms and `0o700` dir. The redaction invariant (Authorization / x-api-key → `***`) must hold for every trace file written via the new toggle path, identical to the v0.14.0 always-on path.
- Resource exhaustion: trace files accumulate on disk while tracing is on; the 5-minute bounded window limits accumulation, and `disabletrace` stops it immediately. No retention is provided (operator runs `rm`) — unchanged from v0.14.0 and a non-goal to change.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Trace-state primitive: process-global `atomic.Bool` with `Enable()`/`Disable()`/`IsEnabled()` + TTL timer (cancel prior, start fresh 5-min, expiry calls Disable); `TRACE_TTL` test-only override | 1, 4, 5, 6 | 4, 5, 6, 8 | — |
| 2 | `/enabletrace` + `/disabletrace` handlers mirroring `/setloglevel`; register in `CreateRouterFromConfig` mux; update `NewTraceMiddleware` to consult `IsEnabled()` per request with flag-OR-config; always-mount on `/v1/` | 1, 2, 3, 7, 8 | 1, 2, 3, 7, 8, 9 | prompt 1 |
| 3 | Docs + changelog: `docs/config.md` endpoints + TTL + deprecation note; `CHANGELOG.md` entry | — | 10, 11 | prompts 1, 2 |

Rationale: prompt 1 establishes the state primitive and timer semantics in isolation (unit-testable without HTTP); prompt 2 wires the HTTP surface and middleware switch on top of the primitive; prompt 3 is doc/changelog only and depends on the final behavior being settled. Splitting state from wiring avoids the prompt-creator holding the timer-cancellation + mux-wiring + redaction-invariant graph in memory at once.

## Do-Nothing Option

If we do nothing, tracing stays config-only: editing `trace:` and restarting the router is the only way to toggle it. Operators either leave it off and cannot capture a problematic exchange without a restart that drops the session, or leave it on and accumulate trace files (full bodies) on disk indefinitely. The bounded-window capture use case remains unsupported. The current approach is workable but forces a restart-vs-disk-hazard tradeoff every time an operator wants to diagnose a single request — which is the common case for a personal-tool router.

## Verification Result

**Verified:** 2026-06-30T13:46:16Z (HEAD 4dee04e)
**Binary:** installed `dark-factory` (dev) for spec lifecycle; live router pid 46673 at `/Users/bborbe/Documents/workspaces/go/bin/claude-code-router` (binary mtime 15:31 local, fresh vs v0.16.0 tag 13:29Z)
**Scenario:** live runtime replay against launchd-managed router on 127.0.0.1:8788 (config has no `trace:` line → default-off); enabletrace/disabletrace HTTP toggles; trace file inspection; fresh `make precommit` + Ginkgo `pkg/handler` suite on origin/master (v0.16.0).
**Evidence:**
- `make precommit` → EXIT_CODE=0 (golangci-lint 0 issues, go vet clean, trivy 0, osv 0, addlicense ok)
- default-off: `rm -f ~/.claude-code-router/trace/*.json` then `POST /v1/messages` → 0 trace files
- `curl -X POST 127.0.0.1:8788/enabletrace` → 200 `trace enabled`; subsequent `POST /v1/messages` → 1 trace file written
- trace JSON has all 7 keys: request.{method,path,headers,body} + response.{status,headers,body}; method=POST, path=/v1/messages, status=200
- `request.headers.Authorization` = `***`, `request.headers["X-Api-Key"]` = `***` (case-insensitive redaction); `Content-Type` = `application/json` (verbatim)
- `grep -REin 'Bearer |sk-' ~/.claude-code-router/trace/` → 0 matches (clean-body run; raw secret nowhere in file)
- `curl -X POST 127.0.0.1:8788/disabletrace` → 200 `trace disabled`; subsequent `POST /v1/messages` (unique body marker) → 0 new files, marker absent from dir
- TTL auto-disable (AC #4), disable-cancels-timer (AC #5), repeated-enable-resets-window (AC #6): `pkg/handler/trace_state_test.go` asserts via `NewTraceStateWithTTL` + `Eventually`/`Consistently`; full Ginkgo suite PASS (exit 0)
- default-at-boot (AC #8): unit test asserts `IsEnabled()==false` after `NewTraceState()`/`NewTraceStateWithTTL`; source `trace_state.go` sets `timer=nil` until `Enable()` (no TTL goroutine at boot)
- flag-OR-config (AC #7): `trace.go` line 103 `if !traceState.IsEnabled() && !configAlwaysOn` short-circuits; unit test `configAlwaysOn=true overrides flag` PASS
- docs: `grep -n enabletrace docs/config.md` → 5 hits; `grep -n disabletrace docs/config.md` → 4 hits; `grep -ni deprecat docs/config.md` → 2 hits
- changelog: `grep -ni 'enabletrace\|disabletrace' CHANGELOG.md` line 9 (under `## v0.16.0` at line 7, above `## v0.14.0` at line 16)
- source on master: `pkg/handler/{enabletrace,disabletrace,trace_state,trace}.go`, `pkg/factory/factory.go` lines 211-212 (mux registration) + 218-223 (unconditional middleware mount with `DefaultTraceState()` + `trace` configAlwaysOn)
**Verdict:** PASS

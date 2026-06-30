---
status: verifying
approved: "2026-06-30T09:10:32Z"
generating: "2026-06-30T09:24:25Z"
prompted: "2026-06-30T09:24:25Z"
verifying: "2026-06-30T11:02:43Z"
branch: dark-factory/sighup-hot-reload
---

## Summary

- Operators edit `~/.claude-code-router/config.yaml` and send `kill -HUP` to the running router process; the router re-reads, validates, and atomically swaps the active routing config without restarting the process or dropping connections.
- In-flight requests finish against the routing table they started under; new requests dispatch against the freshly loaded config.
- A reload event is logged at INFO with old/new provider counts (never token values); a malformed config is rejected and the previous config stays active.
- Documentation (config reference, launchd service guide, the config-update runbook, CHANGELOG) is updated from "restart the service" to "send SIGHUP."
- No file-watcher, no listener/TLS swap, no connection draining, no HTTP reload endpoint.

## Problem

Editing a provider list, alias, or token today requires a full process restart: `launchctl kickstart -k` (or `systemctl --user restart`) kills the running router and relaunches it. Every in-flight Claude Code request is severed mid-stream — a `/compact` on a large session, a long code-generation, or a multi-step tool-use turn all die with a broken connection, and claude-code's SDK retries from scratch. The operator pays the latency and the lost work for a one-line YAML edit. The router is a long-running local proxy; restarting it to pick up config edits is disproportionate.

## Goal

After this work, an operator who edits the config file and sends SIGHUP to the router process observes: the router logs a reload line, subsequent requests route via the new config, in-flight requests complete undisturbed, and the process keeps its PID and listening socket throughout. An invalid edit leaves the old config active and the error in the log. No process restart, no connection drop, no manual service-management command.

## Non-goals

- File-watcher reload (FSEvents/inotify) — SIGHUP only.
- Draining or migrating existing in-flight connections to the new config; they simply finish against the old routing table.
- Hot-swapping the listener address (`--listen`) or TLS material at runtime; those still require a restart.
- Config-diff UI, HTTP `/reload` endpoint, or any non-SIGHUP reload trigger.
- Per-request `atomic.Pointer[Config]` hot-path refactor (the deeper alternative to the mux-swap strategy); out of scope.
- Do NOT add a per-feature opt-out flag that disables SIGHUP handling — the SIGHUP handler is the feature; an escape hatch on it is itself a regression. If a future consumer demands no-reload behavior, that is a separate spec.
- Do NOT add a tunable debounce/coalesce window for SIGHUP — invariant; one SIGHUP equals one reload attempt. No named consumer demands coalescing.
- The Obsidian runbook (`65 Runbooks/Update Claude Code Router Config.md`) is NOT a dark-factory-gated AC — it lives on the host outside the `/workspace` mount, so a container prompt cannot write to it. It is a manual post-implementation operator step tracked in the vault task file, not in this spec's verification.

## Acceptance Criteria

- [ ] `make precommit` exits 0 in the repo root — evidence: exit code.
- [ ] Sending SIGHUP to a running router whose config file was edited from N providers to M providers causes new requests to be served by the M-provider config — evidence: Ginkgo unit test where the test sends `syscall.Kill(syscall.Getpid(), syscall.SIGHUP)` and `Eventually(func() int { return <router>.ConfigSnapshot().ProviderCount() }).Should(Equal(M))` passes within the suite timeout.
- [ ] Sending SIGHUP when the config file contains invalid YAML leaves the active config unchanged at the pre-SIGHUP provider count, logs an error line, and does NOT log the reload-success line — evidence: negative (Ginkgo `Consistently(func() int { return <router>.ConfigSnapshot().ProviderCount() }).Should(Equal(N))` over a 1s window holds, AND `grep -c 'config reload failed' <log>` returns ≥1, AND `grep -c 'config reloaded' <log>` returns 0 during the window).
- [ ] A successful reload logs exactly one line matching `config reloaded old_providers=N new_providers=M` at glog V(1) — evidence: log line shape (`grep -E 'config reloaded old_providers=[0-9]+ new_providers=[0-9]+' /tmp/claude-code-router.log` returns ≥1 line; the line contains no token value — `grep -c 'Bearer\|sk-\|token:' /tmp/claude-code-router.log` over the reload line returns 0).
- [ ] An in-flight HTTP request that started before the SIGHUP completes with its original provider's response code and is NOT dispatched against the new config — evidence: Ginkgo integration test where a slow upstream handler is mid-response when SIGHUP fires, and the response body/status reflects the pre-reload provider (assert body marker injected by the test's first handler, not the second).
- [ ] `docs/launchd-service.md` documents the `kill -HUP $(pgrep claude-code-router)` reload procedure — evidence: `grep -n 'kill -HUP' docs/launchd-service.md` returns line ≥1.
- [ ] `docs/config.md` `## Reload` section documents the SIGHUP procedure and no longer claims "no hot-reload in v1" — evidence: `grep -c 'no hot-reload in v1' docs/config.md` returns 0 AND `grep -n 'kill -HUP' docs/config.md` returns line ≥1.
- [ ] `CHANGELOG.md` has an entry under a `## Unreleased` heading describing SIGHUP hot config reload — evidence: `grep -n '## Unreleased' CHANGELOG.md` returns line ≥1 AND `grep -ni 'sighup\|hot.*reload\|hup' CHANGELOG.md` returns ≥1 line at or after the `## Unreleased` line.
- [ ] The router installs its own `signal.Notify(ch, syscall.SIGHUP)` on a dedicated channel before the HTTP server starts and NEVER cancels the run context on SIGHUP (only on SIGINT/SIGTERM) — evidence: negative (Ginkgo test sends SIGHUP and asserts `ctx.Err()` remains `nil` via `Consistently(func() error { return ctx.Err() }).Should(BeNil())` over a 500ms window; SIGINT in the same suite cancels within 100ms as the control case).

## Verification

```
make precommit
```

Live sanity run (manual, not gated by an AC — confirms the end-to-end operator path):

```
# 1. start router with a 2-provider config
make run        # or launchctl bootstrap with the plist
# 2. edit config to 3 providers
$EDITOR ~/.claude-code-router/config.yaml
# 3. reload
kill -HUP $(pgrep claude-code-router)
# 4. expect:
tail -5 /tmp/claude-code-router.log      # contains: config reloaded old_providers=2 new_providers=3
# 5. invalid-YAML guard:
echo "broken: : :" > ~/.claude-code-router/config.yaml
kill -HUP $(pgrep claude-code-router)
tail -5 /tmp/claude-code-router.log      # contains: config reload failed ... ; NO "config reloaded" line; old config still active
```

## Desired Behavior

1. On process startup, before the HTTP server begins listening, the router registers a dedicated SIGHUP notification channel via `signal.Notify` (distinct from the `run.ContextWithSig` channel that handles SIGINT/SIGTERM). Receiving SIGHUP never cancels the run context.
2. On SIGHUP receipt, the router re-invokes the existing config loader (`pkg.Load`) against the same `--config-path` it used at startup — reusing the existing read + tilde-expand + YAML-parse + `Validate` path, no second implementation of validation.
3. If the reload load succeeds, the router rebuilds the full request-dispatch handler tree (per-provider proxies + model router + admin endpoints) from the new config and atomically swaps the active request-dispatch handler that the server dispatches through. In-flight requests already inside the old handler tree finish against it; the next accepted connection observes the new tree.
4. If the reload load fails (read error, YAML parse error, validation error), the router logs the failure at WARNING with a lowercase message and the underlying error, leaves the active handler pointer pointing at the previous config, and continues serving. No swap occurs.
5. On a successful swap the router logs exactly one line at glog V(1): `config reloaded old_providers=<N> new_providers=<M>` where N and M are the provider-map lengths before and after. On a failed swap it logs `config reload failed: <error>` at WARNING. Token values are never logged — the reload log line carries counts and the error string only, never `cfg` formatted wholesale.
6. The router exposes a test-accessible accessor returning the active provider count (and enough of the active config for tests to assert routing against the post-reload state) without tests inspecting private fields.
7. SIGHUP is idempotent: sending it repeatedly (including with no file change between sends) reloads each time, logging the same counts when the config is unchanged. There is no debouncing and no "already reloaded" short-circuit.
8. SIGHUP and graceful shutdown are independent: a SIGHUP in flight when SIGINT/SIGTERM arrives does not block shutdown; a SIGHUP received after shutdown begins is a no-op (the reload goroutine has exited with the context).

## Constraints

- Language, logging, test, and gate stack are frozen: Go, `github.com/golang/glog` (V(n)-gated INFO), Ginkgo v2 + Gomega, `make precommit` as the gate. See the repo's existing conventions in `pkg/config.go`, `pkg/factory/factory.go`, `pkg/handler/model-router.go`.
- `pkg.Load(ctx, path) (*Config, error)` already performs read + tilde expansion + YAML parse + `Validate`. Reuse it verbatim on the reload path; do NOT extract a second validation or a second loader. Touching `Load`'s signature is out of scope.
- `run.ContextWithSig(ctx)` (from `github.com/bborbe/run`) registers `signal.Notify` for `os.Interrupt, syscall.SIGINT, syscall.SIGTERM` and cancels the context on receipt — it does NOT register SIGHUP. The router MUST install its own `signal.Notify(ch, syscall.SIGHUP)` on a separate channel. Under no condition does the SIGHUP path cancel the run context; that is exclusively SIGINT/SIGTERM's job.
- The mux-swap strategy is the frozen swap mechanism: rebuild the entire `http.Handler` (the whole mux from `CreateRouterFromConfig`) on each successful SIGHUP, then swap the `atomic.Pointer[http.Handler]` the server dispatches through. The per-request `atomic.Pointer[Config]` alternative is explicitly out of scope (Non-goal).
- `NewModelRouter` captures `routes`, `defaultHandler`, and `aliases` at construction time (not per-request). The mux rebuild therefore constructs a fresh `NewModelRouter` per reload; the old model router instance continues to serve its in-flight requests against its captured closures.
- Token-leak invariant (carried from `docs/config.md`): `Config` holds `token:` fields. Reload logging emits provider COUNTS and the reload timestamp only. Never `glog.Infof("config: %+v", cfg)`, never a field-by-field dump. Any new log line added by this work must pass the no-`Bearer`/`sk-`/`token:`-substring grep in the reload-success AC.
- glog discipline: every new `Infof` added by this work is gated behind `glog.V(n)` (n≥1). No bare `glog.Infof`. Lowercase log messages — `config reloaded`, not `Config reloaded`.
- The config file path is fixed at startup from `--config-path`/`CONFIG_PATH` and is reused unchanged on every reload. The reload path does not re-read `--config-path`.
- Existing tests in `pkg/config_test.go`, `pkg/factory/factory_suite_test.go`, `pkg/handler/*_test.go` continue to pass unchanged.
- `make precommit` remains the single gate; no new linter, formatter, or CI step is introduced.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---------|-------------------|----------|
| Config file deleted or unreadable between startup and SIGHUP | `Load` returns a wrapped read error; router logs `config reload failed: read config ...` at WARNING; active config unchanged; server keeps serving on old handler. | Operator restores the file (or points `--config-path` elsewhere on next restart) and sends SIGHUP again; reload succeeds, success line logged. |
| Config file contains invalid YAML on SIGHUP | `Load` returns a parse error; router logs `config reload failed: parse config ...` at WARNING; old config retained; no `config reloaded` line. | Operator fixes the YAML and re-sends SIGHUP. |
| Config file parses but fails `Validate` (e.g. `default_provider` references a missing provider key) | `Load` returns a validation error; router logs `config reload failed: validate config ...` at WARNING; old config retained. | Operator corrects the config and re-sends SIGHUP. |
| SIGHUP arrives during an in-flight request | The in-flight request is already inside the old handler tree and completes against it; the atomic pointer swap only affects connections accepted after the swap. No request is rerouted mid-flight. | None needed — by design. Operator confirms via the `[req]` log line that the in-flight request used the pre-reload provider. |
| Rapid repeated SIGHUP (operator double-sends, or a `kill -HUP` loop) | Each SIGHUP triggers one independent reload attempt; identical configs produce identical count logs each time. No coalescing, no debounce. The last successful swap wins. | None needed — by design. A failed reload among the bursts leaves the most recent successful config active. |
| SIGHUP arrives after SIGINT/SIGTERM has begun shutdown | The reload goroutine has exited with the context; SIGHUP is a no-op. Shutdown proceeds normally. | None needed — by design. |
| SIGHUP handler panics during mux rebuild | The panic is recovered inside the reload goroutine (no goroutine leak, no process crash); router logs the panic at ERROR; old config retained. | Operator inspects `/tmp/claude-code-router.log`, fixes the trigger, re-sends SIGHUP. |

## Security / Abuse Cases

- **Attacker-controlled input:** the config file contents. Mitigation: the existing `Load` + `Validate` path already enforces non-empty providers, valid `default_provider` reference, valid glob syntax, and alias/provider-name collision rejection. The reload path reuses the same validation — a hostile edit that fails validation is rejected and the old config stays active.
- **Trust boundary:** the config file is operator-owned (`~/.claude-code-router/config.yaml`, `chmod 600` per `docs/config.md`). SIGHUP can only be sent by a process with the same UID (or root). No new trust boundary is introduced — the reload path trusts the same file and the same signal sender as startup.
- **Token leakage:** the `Config` struct holds `token:` fields. The only new log surface is the reload line, which emits provider counts and the error string only. Whole-config formatting (`%+v`) and field dumps are explicitly forbidden (see Constraints). The reload-success AC includes a negative grep for `Bearer`/`sk-`/`token:` to enforce this.
- **Hang / retry storm:** a failed reload does NOT auto-retry. One SIGHUP = one reload attempt. An operator scripting `while true; do kill -HUP ...; done` produces one reload per signal, not a retry loop inside the router. There is no infinite-loop risk from the reload path itself.
- **Race:** the atomic pointer swap is the only shared-state mutation. Reads (request dispatch) and writes (pointer swap) are serialized by the atomic primitive; no mutex is held across the mux rebuild, so a slow rebuild does not block in-flight requests. A crash mid-rebuild (panic) is recovered (see Failure Modes) and leaves the old pointer intact.

## Suggested Decomposition

This spec touches 3 code layers (CLI signal wiring, factory mux-swap, handler accessor for tests) plus docs. The natural seam is signal-handling/swap-mechanism vs. test-observable accessor vs. documentation.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | SIGHUP signal handler in CLI entry + atomic-mux-swap in factory (the `atomic.Pointer[http.Handler]` the server dispatches through, rebuild via `CreateRouterFromConfig` + `pkg.Load`, reload/failed-reload log lines, no-ctx-cancel invariant) | 1, 2, 3, 4, 5, 7, 8 | 1, 2, 4, 5, 10 | — |
| 2 | `ConfigSnapshot()` accessor + Ginkgo unit/integration tests (reload-increments-provider-count, invalid-YAML-keeps-old-count, in-flight-request-finishes-on-old-handler, SIGHUP-does-not-cancel-ctx) | 6 | 2, 3, 5, 10 | prompt 1 (uses the swap + accessor) |
| 3 | Docs: `docs/config.md` Reload section, `docs/launchd-service.md` reload note, Obsidian runbook TL;DR/Procedure/Checklist, `CHANGELOG.md` Unreleased entry | — | 6, 7, 8, 9 | prompt 1 (behavior must exist before docs describe it) |

Rationale: prompt 1 is the load-bearing mechanism — the SIGHUP handler, the atomic swap, the log discipline, and the no-ctx-cancel invariant all live together because they are one behavioral unit (a working reload). Prompt 2 builds the test observable on top of prompt 1's swap and validates every behavior the ACs assert. Prompt 3 is doc-only and depends on prompt 1 having shipped the real behavior so the docs describe what exists, not what is planned. Cycles are not a risk: 1 → 2 → 3 is strictly layered.

## Do-Nothing Option

Do nothing: operators continue to run `launchctl kickstart -k` (or `systemctl --user restart`) after every config edit. Every in-flight Claude Code request is severed; claude-code's SDK retries from scratch, losing streaming progress on long `/compact` and code-generation turns. The cost is proportional operator frustration and lost work per edit, not a correctness bug — the router still works, just expensively to reconfigure. Acceptable only if config edits are truly rare; the existing runbook already documents this path. The SIGHUP work is justified because the router is a long-running local proxy whose entire purpose is uninterrupted routing, and a full process restart for a one-line YAML edit violates that purpose.

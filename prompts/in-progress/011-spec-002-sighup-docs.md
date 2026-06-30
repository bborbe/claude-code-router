---
status: approved
spec: ["002"]
created: "2026-06-30T10:15:00Z"
queued: "2026-06-30T09:24:34Z"
---

<summary>
- The config-reference reload section tells operators to send SIGHUP instead of restarting the service.
- The config reference no longer claims there is no hot-reload.
- The macOS service guide gains a reload-without-restart subsection alongside its existing restart commands.
- The changelog records the SIGHUP hot-reload feature under a new unreleased heading.
- Every doc edit describes the behavior that shipped in the implementation prompt, not a planned behavior.
- The reload log line is documented as provider counts only, with no token values anywhere in the docs.
- The Obsidian runbook is explicitly out of scope for this prompt (it lives outside the repo mount).
</summary>

<objective>
Update the operator-facing docs so they describe the SIGHUP reload path that now exists, replacing the "restart the service" instructions for config edits. The Obsidian runbook is out of scope (it lives outside the repo mount; see spec Non-goals).
</objective>

<context>
Read CLAUDE.md at the repo root and every ancestor up to `~/Documents/workspaces/` for project conventions (if present).

Prompt 1 (`1-spec-002-sighup-handler.md`) MUST be implemented first — the docs describe the real behavior, not a plan.

Read these files before editing:
- `docs/config.md` — the `## Reload` section (around lines 130-143) currently says "Config changes require a router restart (no hot-reload in v1)" with `launchctl kickstart -k` / `systemctl --user restart` / Ctrl-C instructions. This is the section to replace.
- `docs/launchd-service.md` — the upgrade flow (lines 125-132) and local hotfix flow (lines 134-155) use `launchctl kickstart -k`. Add a reload note near the upgrade/hotfix sections.
- `CHANGELOG.md` — the top entry is `## v0.13.0` (line 7). There is currently NO `## Unreleased` heading. Add one ABOVE `## v0.13.0`.

Reference coding plugin docs (in-container path):
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `## Unreleased` heading convention.
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — doc structure.
</context>

<requirements>
1. Edit `docs/config.md` `## Reload` section. Replace the body that currently reads (around lines 130-143):

   ```markdown
   ## Reload

   Config changes require a router restart (no hot-reload in v1):

   ```bash
   # macOS launchd
   launchctl kickstart -k gui/$(id -u)/de.bborbe.claude-code-router

   # Linux systemd-user
   systemctl --user restart claude-code-router.service

   # Local foreground (development)
   # Ctrl-C, then `make run` again
   ```
   ```

   with a new body that documents SIGHUP as the reload trigger:

   ```markdown
   ## Reload

   Edit the config file and send SIGHUP to the running router to pick up the change without restarting the process or dropping in-flight requests:

   ```bash
   kill -HUP $(pgrep claude-code-router)
   ```

   On success the router logs one line at `config reloaded old_providers=N new_providers=M` and serves new requests from the updated config. Requests already in flight finish against the config they started under. An invalid config (missing file, invalid YAML, validation failure) is rejected: the old config stays active and the router logs `config reload failed: ...` at WARNING.

   A full process restart is still needed to change the `--listen` address or TLS material — those are not hot-reloadable.

   `launchctl kickstart -k` / `systemctl --user restart` still work for a hard restart (binary upgrade, listener-address change), but are no longer required for config edits.
   ```

   Acceptance checks:
   - `grep -c 'no hot-reload in v1' docs/config.md` returns 0.
   - `grep -n 'kill -HUP' docs/config.md` returns a line >= 1.

2. Edit `docs/launchd-service.md`. Add a short reload note. Insert it as a new subsection (e.g. `### Reload config without restart`) immediately AFTER the upgrade flow section (around line 132) and BEFORE the local hotfix flow section (line 134). Content:

   ```markdown
   ### Reload config without restart

   To pick up a config edit without restarting the process (in-flight requests are preserved), send SIGHUP:

   ```bash
   kill -HUP $(pgrep claude-code-router)
   ```

   The router logs `config reloaded old_providers=N new_providers=M` on success. A malformed config is rejected and the previous config stays active. Use `launchctl kickstart -k` (above) only for binary upgrades or `--listen` address changes — not for config edits.
   ```

   Acceptance check:
   - `grep -n 'kill -HUP' docs/launchd-service.md` returns a line >= 1.

3. Edit `CHANGELOG.md`. Add a `## Unreleased` heading ABOVE the existing `## v0.13.0` line (line 7). Under it, add a bullet describing the SIGHUP reload:

   ```markdown
   ## Unreleased

   - feat: SIGHUP triggers a hot config reload — `kill -HUP $(pgrep claude-code-router)` re-reads, validates, and atomically swaps the active routing config without restarting the process or dropping in-flight requests. A malformed config is rejected and the old config stays active. Reload logging emits provider counts only (no token values).
   ```

   Acceptance checks:
   - `grep -n '## Unreleased' CHANGELOG.md` returns a line >= 1.
   - `grep -ni 'sighup\|hot.*reload\|hup' CHANGELOG.md` returns >= 1 line at or after the `## Unreleased` line.

4. Do NOT edit the Obsidian runbook (`65 Runbooks/Update Claude Code Router Config.md`) — it lives outside the repo mount and is a manual post-implementation operator step tracked in the vault task file (spec Non-goal).

5. Do NOT edit `README.md`'s `clauder` shell function — it is unrelated to the reload mechanism. (If a reviewer asks, the shell function is about pointing a single Claude Code invocation at the router, not about reloading config.)
</requirements>

<constraints>
- Language, logging, test, and gate stack are frozen: Go, `github.com/golang/glog` (V(n)-gated INFO), Ginkgo v2 + Gomega, `make precommit` as the gate.
- Token-leak invariant: reload logging emits provider COUNTS only. The docs must describe the log line as `config reloaded old_providers=N new_providers=M` and NOT include any example that dumps the full config.
- glog discipline: every new `Infof` is gated behind `glog.V(n)` (n>=1). Lowercase log messages. The docs reference the lowercase form `config reloaded` / `config reload failed`.
- The config file path is fixed at startup from `--config-path`/`CONFIG_PATH` and is reused unchanged on every reload. Docs should not suggest the path can be changed at runtime.
- Do NOT add a per-feature opt-out flag or a tunable debounce/coalesce window.
- Do NOT commit — dark-factory handles git.
- `make precommit` remains the single gate.
- The Obsidian runbook is NOT a dark-factory-gated AC and is out of scope for this prompt.
</constraints>

<verification>
Run `make precommit` in the repo root — must exit 0.

Confirm the doc edits:
```
grep -c 'no hot-reload in v1' docs/config.md          # must return 0
grep -n 'kill -HUP' docs/config.md                    # must return line >= 1
grep -n 'kill -HUP' docs/launchd-service.md           # must return line >= 1
grep -n '## Unreleased' CHANGELOG.md                   # must return line >= 1
grep -ni 'sighup\|hot.*reload\|hup' CHANGELOG.md       # must return >= 1 line at or after ## Unreleased
```
</verification>

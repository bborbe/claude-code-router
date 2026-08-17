---
status: cancelled
spec: [009-inbound-api-key-auth]
execution_id: claude-code-router-exec-029-spec-009-docs-changelog
dark-factory-version: dev
created: "2026-08-17"
queued: "2026-08-17T16:13:22Z"
started: "2026-08-17T17:06:15Z"
cancelled: "2026-08-17T17:06:44Z"
---

# Docs + changelog for inbound router auth

<summary>
- A new `## Inbound auth` section in `docs/config.md` documents the optional `auth.key` field, the `x-router-key` header, the loopback exemption and the SIGHUP toggle.
- The stale "no authentication" claim about the admin endpoints is corrected to describe the loopback guard.
- `docs/config.example.yaml` carries an example `auth:` block with a placeholder key and a comment explaining that omitting it disables the check.
- `README.md`'s "example config in full" block includes the same `auth:` shape.
- `CHANGELOG.md` gains an entry under a freshly-created `## Unreleased` heading describing inbound auth and the admin guard.
- The wrong claim in `docs/launchd-service.md` that `launchctl kickstart -k` handles `--listen` address changes is corrected to direct operators to `bootout`/`bootstrap`.
</summary>

<objective>
Land every documentation change required by the inbound-auth feature so that an operator with no context can discover the field, configure it correctly, and apply the bind change without a trip back to the source code.
</objective>

<context>
- Repo root is the current working directory.
- Read `docs/config.md` end-to-end. Note the existing `## Auth` section at line 114 (outbound provider-token semantics — DO NOT modify it). Note the admin-endpoint section at line 142 that currently says the admin endpoints are bound with "no authentication". Note `## Example — all four providers` at line 159.
- Read `docs/config.example.yaml` — the canonical shape operators copy into `~/.config/claude-code-router/config.yaml`. Add the `auth:` block at the top level.
- Read `README.md` — the "example config in full" block lives around lines 30-72.
- Read `CHANGELOG.md` — the file currently jumps straight from the header to a `## v0.X.Y` section. A `## Unreleased` heading must be created at the top of the version list, above the existing newest version.
- Read `docs/launchd-service.md` — the line that currently claims `launchctl kickstart -k` handles `--listen` address changes is at approximately line 142. Verify the exact line before editing.
- Read `docs/dark-factory-integration.md:21-44` for the authoritative plist bind change procedure (`bootout`/`bootstrap`, not `kickstart -k`).
- Read `pkg/config.go` and the schema in `docs/config.md` to confirm the `auth:` block field shape produced by prompts 1-3.
</context>

<requirements>
1. **`docs/config.md` — new `## Inbound auth` section.** Insert it BEFORE the existing `## Auth` section (which is about outbound provider tokens; do not modify that section's content — its subject is different). The new section must cover:
   - The optional `auth:` block at the top of the YAML.
   - The single `key:` string field.
   - The behavior: absent or empty ⇒ disabled; set ⇒ enforced.
   - The header name `x-router-key`.
   - The loopback exemption and why it exists (operator's own Claude Code on the host keeps working keyless).
   - SIGHUP hot-reload applies changes with no restart.
   - The error code returned for a missing or wrong key (401).
   - A short code example showing the YAML block, matching the `docs/config.example.yaml` placeholder. Use `<shared-key-from-teamvault>` or similar — no real secret.

2. **`docs/config.md` — correct the stale claim.** In the section that documents `/setloglevel`, `/enabletrace` and `/disabletrace` (around line 142), find the sentence that currently says they are bound "with no authentication — the same trust model as `/setloglevel`" and rewrite it to describe the loopback-only guard. State explicitly that the guard is unconditional and is the protection once the listener binds beyond `127.0.0.1`. Also document `/gc` as loopback-only in the same section (or in an adjacent admin-endpoint paragraph).

3. **`docs/config.example.yaml` — add the `auth:` block.** Add a top-level commented `auth:` block at the top level of the YAML. Place it where it fits the top-level structure — after the `trace:` line is a sensible spot; do NOT append it after `aliases:` (that line is the file's last, and a trailing block reads as part of the alias section). Shape (do not use any real secret value):
   ```yaml
   # auth:                        # omit (or set key: "") to disable inbound auth on /v1/*; required when the listener binds beyond 127.0.0.1
   #   key: "<shared-key-from-teamvault>"
   ```

4. **`README.md` — add the same block to the "example config in full" block** (around line 30). The block must mirror the `docs/config.example.yaml` content, re-indented to match the surrounding fence (the README fence sits inside numbered list item 2, so it is indented 3 spaces — preserve that indentation).

5. **`CHANGELOG.md` — create `## Unreleased` and add an entry.** The file currently jumps from the intro prose straight to `## v0.20.0`. Insert a `## Unreleased` heading immediately ABOVE the newest version heading (below the "Please choose versions by ..." prose — do not move or reflow that prose). Under it add one bullet (or a tight cluster of two-three bullets) describing:
   - Optional inbound auth on `/v1/*` via the `x-router-key` header, configurable as `auth.key` (absent ⇒ disabled, SIGHUP applies the change).
   - Unconditional loopback-only guard on `/setloglevel`, `/enabletrace`, `/disabletrace` and `/gc`, since the listener is now bound beyond `127.0.0.1`.
   The wording must be factual; no marketing language.

6. **`docs/launchd-service.md` — correct the kickstart claim.** Find the sentence (around line 142) that tells operators `launchctl kickstart -k` handles `--listen` address changes. Replace it with the correct procedure: edit the plist, then `launchctl bootout gui/$(id -u)/de.bborbe.claude-code-router` followed by `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/de.bborbe.claude-code-router.plist`. State plainly that `kickstart -k` keeps cached args and will not reload the plist file. Cross-reference the bind-step procedure from `docs/dark-factory-integration.md` step 1.
   - **The same stale claim lives in `README.md:81`** — "A full restart (`launchctl kickstart -k` / `systemctl --user restart`) is only needed for binary upgrades or `--listen` address changes." Correct the launchd half the same way: a full restart is only needed for binary upgrades or `--listen` address changes; on macOS edit the plist and use `launchctl bootout gui/$(id -u)/de.bborbe.claude-code-router` + `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/de.bborbe.claude-code-router.plist` (`kickstart -k` keeps cached args, won't reload the plist); `systemctl --user restart` is fine on Linux. Do NOT touch the systemd half of the sentence — `systemctl --user restart` genuinely re-reads the unit file, so that claim is correct for systemd.

7. **No code changes.** This prompt is docs-only. Do not touch any file under `pkg/`, `cmd/`, or any test file. If you discover an inconsistency between the docs and the code (e.g. a feature the docs claim that prompt 2 or 3 forgot to ship), STOP, make no code change, and report the inconsistency as an explicit blocker in your final summary — do NOT silently fix the code.

8. **Add `auth:` to the `## Schema` block in `docs/config.md`.** The schema at the top of `docs/config.md` is the canonical top-level field list. Append `auth:` / `  key: <string>   # optional; absent or empty ⇒ inbound auth disabled (see ## Inbound auth)` following the comment style of the existing `trace: <bool>` entry.

9. **`docs/debug.md` — correct the trust-model paragraph.** The paragraph that currently says the router "listens on `127.0.0.1:8788` only" and that the debug endpoints "have no auth" (around line 80) is stale after this change. Rewrite it to state that `/setloglevel/`, `/enabletrace`, `/disabletrace` and `/gc` are guarded by an unconditional loopback-only check (403 for non-loopback), that the listener may legitimately bind `0.0.0.0`, and that `/v1/*` is protected by the optional `auth.key` / `x-router-key` check. Cross-reference `docs/config.md` § Inbound auth.

10. **Document `x-router-key` redaction.** Two existing sentences list exactly two redacted header names and become stale once the trace middleware redacts a third. Apply the matching fix to both:
   - `docs/config.md` § Trace, the sentence beginning "The `Authorization` and `x-api-key` request headers are redacted to `***`" — must now list three headers including `x-router-key`.
   - `docs/debug.md` (§ trace tier), the matching sentence — same three-header list.

11. **Caller-side config in the `## Inbound auth` section.** State how a remote caller presents the key: `ANTHROPIC_CUSTOM_HEADERS` carrying `x-router-key: <value>` (per spec Constraints line 138). Without this an operator can enable auth but cannot configure a caller.
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Do NOT introduce a real secret value in any file. The placeholder is `<shared-key-from-teamvault>` or a similar opaque string. No `sk-` or `Bearer ` literals anywhere.
- Do NOT modify the existing `## Auth` section in `docs/config.md` (outbound provider tokens) or its example providers — those are out of scope.
- Do NOT modify the section's heading numbering or the rest of the surrounding prose beyond what is necessary to make the new section and the correction land coherently.
- Repo-relative paths only for references to files *in this repository* (`docs/...`, `pkg/...`). Home-relative operator paths that describe the operator's own machine are expected and must be preserved — e.g. `~/.config/claude-code-router/config.yaml` and `~/Library/LaunchAgents/de.bborbe.claude-code-router.plist` in requirement 6. Never introduce a `/Users/...` host-absolute path.
- The CHANGELOG entry is evaluated at PR time, before the auto-release cuts a version section. Place the entry under `## Unreleased`; do not place it directly under a version section.
- `make precommit` must remain green — running the full check is cheap insurance that you have not introduced a malformed code fence or broken a build-time include.
- `CHANGELOG.md` updates are not covered by `make precommit`; verify by reading the file after editing.
</constraints>

<verification>
make precommit

# Inbound-auth section is present and named:
grep -n 'x-router-key' docs/config.md   # expected: ≥1
grep -n 'Inbound auth' docs/config.md   # expected: ≥1

# The stale claim is gone from the admin-endpoint section:
sed -n '/^## Trace/,/^## Example/p' docs/config.md | grep -c 'no authentication'   # expected: 0

# Redaction docs list the third header:
grep -n 'x-router-key' docs/config.md docs/debug.md   # expected: ≥1 line each

# docs/debug.md stale "no auth" paragraph is corrected:
grep -c 'no auth' docs/debug.md   # expected: 0 in the admin-endpoint paragraph; tolerate other matches

# Example config carries the auth block, no real secret:
grep -n 'auth:\|x-router-key' docs/config.example.yaml   # expected: ≥1
grep -Ec 'sk-|Bearer ' docs/config.example.yaml   # expected: 0

# README matches:
grep -n 'auth:' README.md   # expected: ≥1

# CHANGELOG entry landed under Unreleased (evaluated at PR time, before release cut):
sed -n '/^## Unreleased/,/^## v/p' CHANGELOG.md | grep -c 'x-router-key\|inbound auth'   # expected: ≥1

# launchd-service doc correction landed — the reload section must no longer tie kickstart -k to --listen:
sed -n '/^### Reload config without restart/,/^## /p' docs/launchd-service.md | grep -cE 'kickstart[^\n]*listen|listen[^\n]*kickstart'   # expected: 0 (no sentence may promise listen changes via kickstart -k)
sed -n '/^### Reload config without restart/,/^## /p' docs/launchd-service.md | grep -n 'bootout\|bootstrap'   # expected: ≥1 line
</verification>
---
status: approved
spec: ["010"]
created: "2026-08-17T19:12:47Z"
queued: "2026-08-17T19:50:37Z"
---

# Docs + changelog: allowedApiKeys surface, supersession of auth, launchd wrapper note

<summary>
- The configuration reference documents the new per-provider API-key registry and the routing-by-key rule, and removes all legacy shared-key auth language.
- `docs/dark-factory-integration.md` instructs remote callers to set `ANTHROPIC_API_KEY` (carried as `x-api-key`) instead of the retired custom-header mechanism.
- `docs/config.example.yaml` and the README's inline example show the new surface without any legacy `auth:` / `x-router-key` block.
- `docs/debug.md` loses its `x-router-key` / `auth.key` references.
- `CHANGELOG.md` gains a `## Unreleased` section describing the routing-by-key feature and the 009 auth supersession.
- A documentation note tells the operator to remove the retired `ROUTER_AUTH_KEY` injection from the launchd wrapper (`~/.local/bin/claude-code-router.sh`).
</summary>

<objective>
Update every operator-facing document and the changelog so the new `allowedApiKeys` surface and the 009 supersession are accurately described, and no stale `x-router-key`/`ROUTER_AUTH_KEY`/`auth:` reference remains in the docs.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- Read `docs/config.md` — the `auth:` schema line (line 19), the `## Inbound auth` section (lines 126-141), the trace-redaction line naming `x-router-key` (line 149), and the `auth:` block in the example (lines 185-186). The `## Schema`/`## Routing`/`## Trace`/`## Reload` sections are the anchors for the new content.
- Read `docs/config.example.yaml` — the `x-router-key` mention (line 10) and the `# auth:` block (lines 14-15) are removed; `allowedApiKeys` examples are added.
- Read `README.md` — the inline "example config in full" block's `# auth:` comment (line 35) is replaced with the `allowedApiKeys` surface.
- Read `docs/dark-factory-integration.md` — step 4's `env:` block (lines 77-85) sets `ANTHROPIC_BASE_URL`; the commented `ANTHROPIC_AUTH_TOKEN` line is replaced with `ANTHROPIC_API_KEY`.
- Read `docs/debug.md` — the trace-redaction sentence naming `x-router-key` (line 71) and the trust-model paragraph naming `auth.key`/`x-router-key` (line 80) are scrubbed.
- Read `docs/launchd-service.md` — the service doc does NOT currently mention `ROUTER_AUTH_KEY`; the wrapper script `~/.local/bin/claude-code-router.sh` is a HOST file outside the repo and is NOT edited here — only documented.
- Read `CHANGELOG.md` — the file currently jumps straight from `# Changelog` to `## v0.26.0` (no `## Unreleased` heading exists; one must be created).
- Read `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — CHANGELOG entry placement and phrasing.
- Read `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — docs conventions.
- Read `docs/dod.md` — README/config.md/config.example.yaml/CHANGELOG update rules; when a config field or env var is removed, grep the repo and update all references.
</context>

<requirements>
1. **`docs/config.md`:**
   - In the `## Schema` YAML block, replace the `auth:` entry (line 19) with a top-level line:
     ```yaml
     allowedApiKeys:                  # optional; list of API keys authenticating non-loopback /v1/* requests (see ## Routing by API key). Absent or empty disables key enforcement and key routing.
     ```
   - The `## Schema` block already declares `providers:` (line 24) — do NOT re-declare it. Under the existing `<provider-key>:` line, add a commented per-provider line: `# allowedApiKeys: # optional; list of keys that route to THIS provider, overriding model-glob selection (see ## Routing by API key)`.
   - Delete the entire `## Inbound auth` section (lines 126-141) and replace it with a `## Routing by API key` section that documents, with the same level of care as the removed section:
     - The top-level `allowedApiKeys` registry and the per-provider `allowedApiKeys` override; absent/null/empty all mean disabled (feature-off default — no key enforcement and no key routing, byte-for-byte as before).
     - The valid inbound key set: the top-level registry when non-empty, else the union of all providers' lists (DB 2).
     - The routing rule: a request whose `x-api-key` value is in a provider's list is dispatched to that provider (its outbound token) — the key wins over model globs; a valid-but-unclaimed key routes exactly like a keyless request (glob → `default_provider`); a keyless request is unchanged.
     - The non-loopback auth gate: with a non-empty registry, non-loopback `/v1/*` requests must present a registry key in `x-api-key` or receive 401 (constant-time comparison; the presented value is never logged); loopback is exempt but its `x-api-key` is still stripped outbound.
     - Outbound hygiene: `x-api-key` is stripped before forwarding (every letter case); `Authorization` behavior is unchanged (token: → Bearer swap; token-less → pass-through).
     - SIGHUP applies registry changes without a restart.
     - Caller side: a remote caller sets `ANTHROPIC_API_KEY` to its registry key (Claude Code sends it as `x-api-key`). Note the spec Failure Modes caveat that on a machine which also runs a Claude subscription, `ANTHROPIC_API_KEY` overrides the subscription OAuth in `-p` mode — the operator's own host stays keyless loopback.
     - Supersession: this replaces the spec-009 `x-router-key` / `auth.key` / `ROUTER_AUTH_KEY` mechanism, which is removed; a config still carrying `auth:` fails load and the binary refuses to start with `ROUTER_AUTH_KEY` set.
     - Sensitive: keys are literals in the operator's `chmod 600` config, like provider `token:` fields; never commit a literal key.
   - In `## Trace`, change the redaction sentence (line 149) to name only `Authorization` and `x-api-key` (drop `x-router-key`).
   - In the `## Example — all four providers` block, delete the `auth:` block (lines 185-186) and add a commented top-level `allowedApiKeys:` placeholder line (e.g. `# allowedApiKeys:            # optional; uncomment + list keys to enable non-loopback auth and key routing`).
   - The `## Related` section needs no change.

2. **`docs/config.example.yaml`:**
   - Line 10: change the trace comment to `# (Authorization + x-api-key headers redacted to ***; bodies verbatim).`
   - Replace the `# auth:` block (lines 14-15) with a commented top-level registry placeholder:
     ```yaml
     # allowedApiKeys:            # optional; uncomment + list caller keys to require x-api-key auth on /v1/*
     #   - "<CALLER_API_KEY>"     # and to route those callers to a provider (see docs/config.md ## Routing by API key)
     ```
   - Under `providers.minimax`, show the per-provider override shape with a comment, e.g. after the `token:` line:
     ```yaml
     # allowedApiKeys:            # optional; keys that route to THIS provider, overriding model globs
     #   - "<ROUTING_KEY>"
     ```
   - The file must contain no value that looks like a real secret (no `sk-`, no `<YOUR_ROUTER_KEY>` residue) — the AC 14 grep `grep -Ec 'x-router-key|ROUTER_AUTH_KEY|auth:' docs/config.example.yaml README.md` must return 0.

3. **`README.md`:** in the inline "example config in full" block, replace the `# auth:` comment (line 35) with the commented `allowedApiKeys:` registry placeholder (same wording as requirement 2). The block must contain no `auth:` / `x-router-key` / `ROUTER_AUTH_KEY` text.

4. **`docs/dark-factory-integration.md`:** in step 4's `env:` block (lines 77-85), replace the commented `ANTHROPIC_AUTH_TOKEN` line with an active, documented `ANTHROPIC_API_KEY` line and a comment explaining that Claude Code sends it as `x-api-key` and the router routes by it:
   ```yaml
   env:
     ANTHROPIC_BASE_URL: http://host.docker.internal:8788
     # ANTHROPIC_API_KEY: <YOUR_ROUTING_KEY>   # the router's registry key; sent as x-api-key, routes the container's traffic to the key's provider and authenticates it
   ```
   Keep the `ANTHROPIC_BASE_URL` fallback comment structure. If the doc anywhere suggests carrying router credentials via `ANTHROPIC_CUSTOM_HEADERS`, replace that instruction with `ANTHROPIC_API_KEY` (the AC 14 evidence `grep -n 'ANTHROPIC_API_KEY' docs/dark-factory-integration.md` must return ≥1 line).

5. **`docs/debug.md`:** line 71 — drop `x-router-key` from the redaction sentence. Line 80 — reword the trust-model paragraph to describe the `x-api-key` gate against the `allowedApiKeys` registry (reference `docs/config.md § Routing by API key`), removing `auth.key` and `x-router-key`.

6. **`CHANGELOG.md`:** create the `## Unreleased` heading immediately above `## v0.26.0` (the file currently has none) and add one `feat:` entry describing:
   - routing by presented `x-api-key` (top-level `allowedApiKeys` registry + per-provider override; key wins over model globs; valid-but-unclaimed keys route by globs; keyless requests unchanged),
   - the non-loopback auth gate now validating `x-api-key` against the registry (constant-time, loopback exempt),
   - `x-api-key` stripped outbound and redacted in traces (alongside `Authorization`),
   - the spec-009 `x-router-key` / `auth.key` / `ROUTER_AUTH_KEY` auth path being removed, with fail-closed migration guards (legacy `auth:` fails load; `ROUTER_AUTH_KEY` set ⇒ startup refused),
   - SIGHUP applying registry changes.
   Follow the changelog-guide phrasing. The AC 14 evidence `sed -n '/^## Unreleased/,/^## v/p' CHANGELOG.md | grep -c 'api.key\|API key\|allowedApiKeys'` must return ≥1 line.

7. **Launchd wrapper note (docs only — the wrapper is a host file, not in the repo):** in `docs/config.md`'s `## Routing by API key` section (or `docs/launchd-service.md`, your choice, pick one and link from the other), add a short "Migrating from 009" note: the retired `ROUTER_AUTH_KEY` env var must be removed from the launchd wrapper `~/.local/bin/claude-code-router.sh` (which currently injects it from TeamVault); the registry values live in the `chmod 600` config file like the provider tokens, and no env var replaces `ROUTER_AUTH_KEY`. State that an operator who forgets this gets the fail-closed startup error, and that removing the injection + configuring `allowedApiKeys` + SIGHUP (or restart) completes the migration. This is the documentation half of DB 7/AC 14 — the behavioral guard already landed in prompt 2.

8. **Repo-wide hygiene:** run `grep -n '^## Inbound auth' docs/config.md` — it must return 0 lines (the 009 section removed). Run `grep -rn 'x-router-key\|ROUTER_AUTH_KEY\|ANTHROPIC_CUSTOM_HEADERS' docs/ | grep -vc 'supersed\|retired\|removed\|Migrating from 009'` — it must return 0 lines: the supersession bullet in requirement 1 and the migration note in requirement 7 name the retired mechanism, and EVERY line that names it must carry a marker word (`superseded`, `retired`, `removed`, or `Migrating from 009`) — no live-instruction mention may remain. Run `grep -rn 'x-router-key\|ROUTER_AUTH_KEY\|auth:' docs/config.example.yaml README.md` — 0 lines. If any `.md`/`.yaml` file outside `docs/` still references the 009 surface (e.g. `helm/` values or templates), update or remove those references so no operator-facing artifact mentions a retired mechanism — do NOT touch `CHANGELOG.md`'s historical v0.22.0-v0.25.0 entries (released sections are frozen) or `prompts/`/`specs/` (history).
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Do NOT touch any Go source in this prompt — prompts 1-3 implemented the behavior; this is documentation and changelog only.
- Do NOT invent config knobs, headers, env vars, or endpoints beyond what the spec defines — `x-api-key` via `ANTHROPIC_API_KEY` is the only surface (spec Non-goals). No new `ROUTER_AUTH_KEY`-style env var, no new header, no per-model keys.
- Do NOT edit released `CHANGELOG.md` sections (`## v0.22.0` … `## v0.26.0`) — the 009 entries stay as history.
- Do NOT place a literal secret anywhere — all examples use placeholders like `<CALLER_API_KEY>`; the AC 14 leak-evidence greps must pass.
- Do NOT create new doc files unless one is genuinely required; prefer extending `docs/config.md` or `docs/launchd-service.md` (spec AC 14 only names the existing files).
- The docs must describe behavior that prompts 1-3 actually shipped — no forward-referencing unbuilt features.
</constraints>

<verification>
make precommit

# AC 14 — new surface documented:
grep -n 'allowedApiKeys' docs/config.md
grep -n 'ANTHROPIC_API_KEY' docs/dark-factory-integration.md

# AC 14 — 009 inbound-auth section removed; legacy names only as supersession statements:
grep -n '^## Inbound auth' docs/config.md                        # expect 0 lines
grep -rn 'x-router-key\|ROUTER_AUTH_KEY\|ANTHROPIC_CUSTOM_HEADERS' docs/ | grep -vc 'supersed\|retired\|removed\|Migrating from 009'   # expect 0
grep -Ec 'x-router-key|ROUTER_AUTH_KEY|auth:' docs/config.example.yaml README.md   # expect 0

# AC 14 — changelog entry under ## Unreleased:
sed -n '/^## Unreleased/,/^## v/p' CHANGELOG.md | grep -c 'api.key\|API key\|allowedApiKeys'   # expect ≥1

# No literal-secret shapes introduced:
grep -Ec 'sk-[A-Za-z0-9]|<YOUR_ROUTER_KEY>' docs/config.example.yaml README.md docs/config.md   # expect 0
</verification>

---
status: approved
spec: [015-global-default-provider-token]
created: "2026-08-20T14:52:00Z"
queued: "2026-08-20T15:11:09Z"
---

# Docs + changelog: top-level `default_token:` (global default outbound key)

<summary>
- The configuration reference documents the new optional top-level `default_token:` field — one shared outbound bearer key inherited by every provider and every `Upstream` pool member that declares no `token:` of its own.
- The `## Auth` token table is rewritten around the three-way resolution order: a provider/member `token:` wins, else the global `default_token:`, else the client's `Authorization` passes through unchanged (the subscription-OAuth case, byte-for-byte today's behavior).
- The schema block gains a `default_token:` line so operators can find the field at a glance, and the section notes there is no per-provider opt-out that forces passthrough while a global default is set.
- The example config gains a commented `# default_token:` line (commented so copying the example never changes behavior) plus a comment on an overriding provider's `token:`.
- The changelog gains a feature entry under a newly created `## Unreleased` section describing the field, the frozen resolution order, the member-level fallback, SIGHUP reload, and the redaction guarantee.
- No Go source is touched — this prompt documents what prompts 1–2 shipped.
</summary>

<objective>
Document the top-level `default_token:` field and its three-way outbound-auth resolution order in the operator-facing config reference, the example config, and the changelog, so an operator can define one shared outbound key once instead of copy-pasting it across providers and pool members.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- Read `docs/config.md` — section order `## Schema` (the top-level YAML block: `router:` then the `allowedApiKeys:` lines at ~19–20 then `trace:` then `model_pools:` then `providers:`) → `## Routing` → `## Aliases` → `## Model pools` → `## Requires leading system` → `## Concurrency limit` → `## Upstream pools` → `## Time-of-day windows` → `## Auth` (line ~290; a two-row `token:` table plus the "The router never stores or logs token values; trace files inherit the same invariant" sentence) → `## Routing by API key` → `## Trace` → `## Example — all four providers` → `## Switching mid-session` → `## Reload` → `## Related`. The primary doc change is the `## Auth` table gaining the three-way resolution order; the `## Schema` block gains the new field line.
- Read `docs/config.example.yaml` — the `# allowedApiKeys:` comment block near the top (after `trace: false`) is where the commented `# default_token:` example goes; the `minimax` provider's `token: "<YOUR_MINIMAX_API_KEY>"` line is where the "overrides default_token" comment goes.
- Read `CHANGELOG.md` — the file currently starts with `## v0.41.1` (NO `## Unreleased` heading exists). Create the `## Unreleased` heading above `## v0.41.1` per the changelog guide's file structure, then add the feature bullet under it. Released sections (`## v0.41.1` and below) are frozen — never edit them.
- Read the prompt 1–2 results (the behavior being documented): `pkg/config.go` (`Config.DefaultToken string yaml:"default_token,omitempty"`), `pkg/factory/factory.go` (the effective-token resolution in the per-upstream loop — member `up.Token` wins, else `cfg.DefaultToken`, else empty — and the auth-swap-outer / logging-inner nesting so the V(3) `[upstream.headers]` line reflects the swapped outbound header, redacted to `<redacted len=N>`), `pkg/handler/auth-swap-transport.go` (empty token = no-op, client `Authorization` passes through byte-identical). Document only what those actually ship — no forward-referencing.
- Read `docs/dod.md` — `docs/config.md` / `docs/config.example.yaml` / `CHANGELOG.md` update rules.
- Coding plugin docs (in-container paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — CHANGELOG entry placement and phrasing (the `## Unreleased` heading goes above the newest released section, after the SemVer preamble; conventional `feat:` prefix).
  - `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — docs conventions.
</context>

<requirements>
1. **`docs/config.md` — schema block.** In the `## Schema` top-level YAML block, add a `default_token:` line right after the `allowedApiKeys:` lines (both are optional top-level keys; match the existing `# optional; ...` comment style), e.g.:
   ```yaml
   default_token: <string>             # optional; one shared outbound key inherited by every provider / pool member that declares no token: of its own (see ## Auth). A provider's own token: overrides it; with neither set, the client's Authorization header passes through unchanged.
   ```

2. **`docs/config.md` — `## Auth` section: three-way resolution order.** Rewrite the `## Auth` section (currently a two-row `| token: field | Behavior |` table plus the "never stores or logs token values" sentence) to document, at the same care level as the neighboring sections (all of DB 1–5 and the Security redaction invariant must be documented):
   - **The resolution order, frozen.** A provider (and every `Upstream` pool member, spec 012) resolves its outbound `Authorization` in this fixed order: (1) its own `token:` — a pool member's per-entry `token:`, or a legacy provider-level `token:` that lands on the implicit single member — WINS when set; (2) else the top-level `default_token:`; (3) else the client's `Authorization` header passes through unchanged (Claude Code's subscription OAuth bearer — the router never holds it). There is NO per-provider opt-out that forces passthrough while a global default is set — a provider needing a different key (separate vLLM quota, the off-peak window keys from `## Time-of-day windows`) declares its own `token:` and overrides.
   - **The table**, e.g.:
     ```markdown
     | `token:` field | Behavior |
     |---|---|
     | set | Replace the outbound `Authorization` with `Bearer <token>` — overrides `default_token:`; used for fixed-token providers (MiniMax, Ollama, vLLM) |
     | absent/empty + `default_token:` set | Replace the outbound `Authorization` with `Bearer <default_token>` — one shared key defined once, no per-provider copies |
     | absent/empty + no `default_token:` | Forward the client's `Authorization` header verbatim — used for Anthropic subscription (Claude Code's OAuth bearer passes through untouched) |
     ```
   - **Top-level placement.** `default_token:` is top-level config at the same level as `providers:` / `router:`; absent or empty means no global default (today's behavior). The resolution applies uniformly at the member level — a pool member's per-entry `token:` (see `## Upstream pools`) and the legacy provider-level `token:` (copied onto the implicit single member) both override the global default.
   - **SIGHUP.** A change to `default_token:` applies on SIGHUP without a restart — the reloader rebuilds the router tree from the edited config (see `## Reload`), and the next request's `[upstream.headers]` `len=N` reflects the new key.
   - **Security / redaction (spec DB 5, Security).** The global default is operator config read only at wiring — never from client input; a client cannot influence which token the router sends. Like every token, it flows only in the outbound `Authorization` header, is never echoed to a client or exposed via `/metrics` or admin endpoints, and never reaches logs or trace files: the V(3) `[upstream.headers]` line shows it as `<redacted len=N>` (the `len` distinguishes the inheriting key from an overriding key without printing either — the operator's live smoke evidence) and trace files redact `Authorization` (see `## Trace`). Keep the existing "The router never stores or logs token values; trace files inherit the same invariant" sentence and extend it to name `default_token:`.
   - A short worked note tying back to the spec's operator rung: with `default_token:` set, every no-token provider inherits it — the passthrough case exists on configs WITHOUT a global default (backward-compat), which is the subscription-OAuth flow.

3. **`docs/config.example.yaml`.** Add a commented `# default_token:` example (MUST be commented — an operator copying this example must not get an unexpected global default), near the top after the `# allowedApiKeys:` comment block, e.g.:
   ```yaml
   # default_token: "<SHARED_OUTBOUND_KEY>"   # optional; one shared outbound key inherited by every provider / pool member with no token: of its own (see docs/config.md ## Auth). A provider's own token: overrides it. MUST stay commented — copying this example must not change behavior.
   ```
   And on the `minimax` provider's `token:` line, add a short comment noting it overrides a global `default_token:` (keep the existing `token:` value line unchanged), e.g.:
   ```yaml
       token: "<YOUR_MINIMAX_API_KEY>"   # overrides default_token: — a provider needing its own key declares token: here
   ```

4. **`CHANGELOG.md`.** Create the `## Unreleased` heading immediately ABOVE `## v0.41.1` (after the SemVer preamble; per `changelog-guide.md`), then add ONE `feat:` bullet under it. The bullet must contain the literal `default_token` (the spec Verification is `sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c 'default_token'` → ≥ 1) and cover, at the detail level of the neighboring entries (e.g. the `## v0.40.0` / `## v0.41.0` entries): the optional top-level `default_token:` field (`Config.DefaultToken`) — one shared outbound bearer key inherited by every provider and every `Upstream` pool member that declares no `token:` of its own; the frozen three-way outbound-auth resolution order applied at wiring time (a provider/member `token:` wins, else the global `default_token:`, else the client's `Authorization` passes through unchanged — byte-for-byte today's subscription-OAuth behavior), with the factory resolving the effective token per upstream member and no per-provider opt-out to force passthrough while a global default is set; the V(3) `[upstream.headers]` log line now reflects the swapped outbound `Authorization` as `<redacted len=N>` (the auth-swap now wraps the logging roundtripper), so an operator can distinguish the inheriting key from an overriding key's `len` without either key reaching the log; a config edit to `default_token:` applies on SIGHUP without a restart; the key is operator config read only at wiring (never from client input) and is redacted like every other token (`display:"length"`, never in logs or trace files).
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Do NOT touch any Go source in this prompt — prompts 1–2 implemented the behavior; this is documentation and changelog only.
- Do NOT invent config knobs, headers, endpoints, or behavior beyond what the spec and prompts 1–2 define — `default_token:` (top-level, `omitempty`, optional string) plus the frozen resolution order are the entire surface (spec Non-goals: no `token:` on the router block, no per-provider opt-out, no rotation tooling, no secrets-management/TeamVault integration, no inbound-auth changes).
- The docs must describe behavior prompts 1–2 actually shipped — no forward-referencing unbuilt features.
- This prompt depends on prompts 1–2: do not approve or execute until prompt 2 has completed and shipped the factory resolution + wiring tests — the `<context>` reads them.
- Do NOT edit released `CHANGELOG.md` sections (`## v0.41.1` and below) — they are frozen history. The `## Unreleased` heading is created ABOVE `## v0.41.1`, after the SemVer preamble, exactly per `changelog-guide.md`.
- `make precommit` must remain green.
</constraints>

<verification>
make precommit

# AC docs — default_token documented in both files:
grep -c 'default_token' docs/config.md           # expect >=1
grep -c 'default_token' docs/config.example.yaml # expect >=1

# The Auth section documents the three-way resolution order:
grep -c '## Auth' docs/config.md                          # expect >=1 (section exists)
grep -c 'default_token' docs/config.md                    # expect >=1 (covered above; resolution-order language)

# Example stays behavior-neutral (default_token commented only) — fail if any UNCOMMENTED occurrence exists:
! grep -nE '^[[:space:]]*default_token:' docs/config.example.yaml   # comment lines start with '#' and do not match

# Changelog bullet under ## Unreleased:
sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c 'default_token'   # expect >=1
</verification>

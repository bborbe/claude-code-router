---
status: approved
spec: [012-session-pinning-pools]
created: "2026-08-19T17:13:00Z"
queued: "2026-08-19T15:50:36Z"
---

# Docs + changelog: `upstreams:` pools, session pinning, per-upstream caps

<summary>
- The configuration reference documents the new `upstreams:` list, the five per-entry fields (`upstream`, `token`, `weight`, `maxConcurrentRequests`, `maxConcurrentWaitSeconds`), and the legacy single-`upstream:` form that loads unchanged as a one-entry pool.
- A new "Upstream pools" section explains session pinning (`x-session-id` header → weighted ring hash, same server per session), keyless least-loaded dispatch with round-robin tie-breaking, weighted session distribution, and per-member concurrency caps with the Anthropic-shaped 429.
- The example config shows an `upstreams:` list as commented optional lines, so copying it does not change behavior.
- The changelog gains a `## Unreleased` section (none exists today) with a feature entry describing the pool schema, session pinning, keyless least-loaded, and per-upstream caps.
- No Go source is touched — this prompt documents what prompts 1–3 shipped.
</summary>

<objective>
Document the `upstreams:` pool schema, session-pinning and keyless least-loaded selection, and per-upstream concurrency caps in the operator-facing docs and changelog, so an operator can give a provider a pool of servers (e.g. five DeepSeek vLLM instances) and know exactly how sessions and anonymous traffic are spread.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- Read `docs/config.md` — the `## Schema` YAML block's `providers:` sub-block (per-provider keys `upstream`, `token`, `models`, `requiresLeadingSystem`, `allowedApiKeys`, and the two commented `maxConcurrentRequests`/`maxConcurrentWaitSeconds` lines) and the section order: `## Schema` → `## Routing` → `## Aliases` → `## Requires leading system` → `## Concurrency limit` → `## Auth` → `## Routing by API key` → `## Trace` → ... The new `## Upstream pools` section slots in after `## Concurrency limit` and before `## Auth`.
- Read `docs/config.example.yaml` — the `providers.ollama-local` block (its current tail is the commented `# maxConcurrentRequests:` / `# maxConcurrentWaitSeconds:` lines), where the new `upstreams:` list is shown as commented lines following the file's existing commented-optional convention.
- Read `CHANGELOG.md` — the file currently jumps straight from `# Changelog` to `## v0.32.0`; there is NO `## Unreleased` heading yet (verified). The new heading is created immediately above `## v0.32.0`. Released sections (`## v0.32.0` and below) are frozen — never edit them.
- Read the prompt 1–3 results (the behavior being documented): `pkg/config.go` (`Upstream` type + `Provider.Upstreams` + `UpstreamList()` + the normalize/validate semantics), `pkg/handler/session-id.go` + `session-middleware.go` + `upstream-pool-handler.go` (the `[route] session=<id> upstream=<url>` line), and `pkg/factory/factory.go` (per-member wiring, `defaultMaxConcurrentWaitSeconds = 30`). Document only what those actually ship — no forward-referencing.
- Read `docs/dod.md` — `docs/config.md` / `docs/config.example.yaml` / `CHANGELOG.md` update rules.
- Coding plugin docs (in-container paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — CHANGELOG entry placement and phrasing.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — docs conventions.
</context>

<requirements>
1. **`docs/config.md` — schema block.** In the `## Schema` YAML block's `providers:` sub-block, add the `upstreams:` list as a commented optional alternative to `upstream:` (alongside the existing commented lines), with the per-entry fields, e.g.:
   ```yaml
       # upstreams:                  # optional; alternative to `upstream:` — a pool of servers. Each entry carries upstream/token/weight/maxConcurrentRequests/maxConcurrentWaitSeconds (see ## Upstream pools). Mutually exclusive with `upstream:`.
       #   - upstream: <URL>
       #     token: <string>         # optional; per-member token (defaults to the provider token semantics: absent = pass client's Authorization through)
       #     weight: 1               # optional; default 1. Relative share of pinned sessions this member receives.
       #     maxConcurrentRequests: 8   # optional; per-member cap. Absent or 0 or negative = unlimited.
       #     maxConcurrentWaitSeconds: 30 # optional; per-member queue wait before HTTP 429 (default 30)
   ```

2. **`docs/config.md` — new `## Upstream pools` section.** Insert after the `## Concurrency limit` section and before `## Auth`. Document, at the same care level as the neighboring sections (all of DB 1–6 must be documented — AC 2/3/4/5):
   - **Schema recap:** `upstreams:` replaces `upstream:` for a provider with more than one server; the two are mutually exclusive (a provider setting both fails to load). The legacy single-`upstream:` / `token:` / provider-level `maxConcurrentRequests` / `maxConcurrentWaitSeconds` form loads unchanged as a one-entry pool with weight 1 — the provider-level caps become the single entry's caps, so nothing an operator has needs editing.
   - **Per-entry fields:** `upstream` (required per entry), `token` (per-member; absent = pass the client's Authorization through, same as the provider-level token), `weight` (default 1; a negative weight is rejected at config load; the relative share of PINNED sessions this member receives — 2:1 weight over two members sends ~2/3 of pinned sessions to the heavier member), `maxConcurrentRequests` / `maxConcurrentWaitSeconds` (per-member cap, spec-011 semantics, see ## Concurrency limit).
   - **Session pinning:** a client that sets an `x-session-id` header (e.g. `ANTHROPIC_CUSTOM_HEADERS='{"x-session-id":"<id>"}'` in Claude Code) is pinned to the same pool member on every request via a weighted ring hash of the id — deterministic and stateless, so the session's upstream prompt cache stays warm on that one server. The header is stripped before forwarding, so upstreams never see it, and it is used only for pinning, never for auth. Absent header = keyless request.
   - **Keyless least-loaded:** a request without `x-session-id` is sent to the least-loaded member (fewest in-flight requests by that member's concurrency semaphore), with round-robin tie-breaking among equally-loaded members — keyless floods (e.g. dark-factory containers) spread across the pool instead of stacking on the first-declared server.
   - **Per-member caps:** each member independently enforces its own `maxConcurrentRequests` — two members each allowing 8 do not share one global cap of 8; a request that queues past `maxConcurrentWaitSeconds` is answered HTTP 429 with the same Anthropic-shaped `rate_limit_error` body as the provider-level cap (see ## Concurrency limit).
   - **Member down:** a member that fails answers with the existing sanitized 502 (unchanged behavior) — there is no probe-and-rotate; the operator removes/repairs the member and SIGHUPs, and the next `[route]` log line reflects the pool without it.
   - **Client disconnect while queued:** no slot is held (a slot is acquired only at dispatch); the 429 write to the dead connection fails harmlessly — spec-011 semantics.
   - **Observability:** each dispatch logs `[route] session=<id> upstream=<url>` at glog `V(2)` (same verbosity as the `[alias]` / `[1m-strip]` detail lines) — the operator evidence that a session is pinned and that keyless load is spreading. The `[req]` line is unchanged.
   - **Pre-existing verbosity fix:** `docs/config.md` currently documents the `[alias]` detail line as `V(1)` (around line 73); the source logs it at `V(2)` (`model-router.go`). Fix that line to `V(2)` as part of this docs pass so the documented detail-line verbosity is internally consistent with the new `[route] session=` line.
   - **SIGHUP:** a change to `upstreams:` (add/remove a member, change a weight or cap) applies on SIGHUP without a restart — the reloader rebuilds the pool tree.
   - A short worked example: a `seibert-vllm-default` provider with two or five DeepSeek vLLM members, each `maxConcurrentRequests: 8` to stay under vllm's per-user ceiling, and the two-session `ANTHROPIC_CUSTOM_HEADERS` verification from the spec's operator rung (each session lands on its own member).

3. **`docs/config.example.yaml`.** Under `providers.ollama-local` (after the commented `# maxConcurrentWaitSeconds:` lines), add a commented `upstreams:` example showing a two-member pool, e.g.:
   ```yaml
       # upstreams:                   # optional; alternative to `upstream:` — a pool of servers (see docs/config.md ## Upstream pools).
       #   - upstream: http://localhost:11434
       #     token: "ollama"
       #     weight: 1
       #     maxConcurrentRequests: 8
       #     maxConcurrentWaitSeconds: 30
       #   - upstream: http://localhost:11435
       #     weight: 1
   ```
   They MUST be commented (not active) — an operator copying this example must not get an unexpected pool on ollama-local. The file's `upstreams` text may appear only in comments or a disabled placeholder.

4. **`CHANGELOG.md`.** Create the `## Unreleased` heading immediately above `## v0.32.0` and add one `feat:` entry mentioning `upstreams`, following `changelog-guide.md` phrasing. Cover: the `Provider.upstreams:` pool schema and `Upstream` type (per-entry `upstream` / `token` / `weight` / `maxConcurrentRequests` / `maxConcurrentWaitSeconds`), the legacy single-`upstream:` form loading unchanged as a one-entry pool, session pinning via the `x-session-id` header (weighted ring hash, stateless, header stripped outbound), keyless least-loaded dispatch with round-robin tie-breaking, per-upstream (per-member) concurrency caps with the Anthropic-shaped 429, and that SIGHUP reload rebuilds the pool tree. Follow the repo's existing entry style (e.g. the `## v0.32.0` / `## v0.31.0` feat entries' detail level).
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Do NOT touch any Go source in this prompt — prompts 1–3 implemented the behavior; this is documentation and changelog only.
- Do NOT invent config knobs, headers, endpoints, or behavior beyond what the spec and prompts 1–3 define — `upstreams:` + the five per-entry fields + `x-session-id` are the entire surface (spec Non-goals: no virtual models, no pool-level failover, no health checks, no dynamic weights, no per-model caps).
- The docs must describe behavior prompts 1–3 actually shipped — no forward-referencing unbuilt features (e.g. do not document pool-level overflow failover or relaying `x-session-id` across router instances).
- This prompt depends on prompts 1–3: do not approve or execute until prompt 3 has completed and shipped the factory wiring, the pool handler, and the session middleware — the `<context>` reads them.
- Do NOT edit released `CHANGELOG.md` sections (`## v0.32.0` and below) — they are frozen history.
- `make precommit` must remain green.
</constraints>

<verification>
make precommit

# AC docs — upstreams documented in both files:
grep -c 'upstreams' docs/config.md           # expect >=1
grep -c 'upstreams' docs/config.example.yaml # expect >=1

# Example stays behavior-neutral (upstreams commented only) — fail if any UNCOMMENTED occurrence exists:
! grep -nE '^[[:space:]]*upstreams:' docs/config.example.yaml   # comment lines start with '#' and do not match

# Changelog bullet under ## Unreleased:
sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c upstreams   # expect >=1
</verification>

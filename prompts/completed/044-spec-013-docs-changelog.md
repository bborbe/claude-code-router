---
status: completed
spec: [013-model-pools]
summary: 'Documented the model_pools: config block in docs/config.md (schema reference + new ## Model pools section covering member fields, session pinning, idless least-loaded dispatch, overflow failover, fall-through, validation, observability, SIGHUP, security) and docs/config.example.yaml (commented two-member example), and added a feat: CHANGELOG bullet under ## Unreleased'
execution_id: claude-code-router-session-pinning-exec-044-spec-013-docs-changelog
dark-factory-version: dev
created: "2026-08-19T18:10:00Z"
queued: "2026-08-19T15:50:36Z"
started: "2026-08-19T16:39:14Z"
completed: "2026-08-19T16:41:39Z"
---

# Docs + changelog: `model_pools:` resolution

<summary>
- The configuration reference documents the new top-level `model_pools:` block: an invented name maps to an ordered list of members, each naming a provider, a fixed concrete model, an optional weight, and an optional overflow flag.
- A new "Model pools" section explains that a client sending `model: <poolname>` is resolved before any provider-glob matching: the body's model is rewritten to one member's concrete model and routed through that member's provider, with the same session id kept on the same member (weighted ring hash, stateless) so its cache stays warm.
- The section covers idless least-loaded dispatch with round-robin spread, weighted session distribution, the `overflow: true` failover semantics, the fall-through when a pool name is not configured, and SIGHUP reload rebuilding the pool table.
- The example config shows a `model_pools:` block as commented optional lines, so copying it does not change behavior.
- The changelog gains a feature entry under `## Unreleased` describing the `model_pools:` schema, session-pinned resolution, body rewrite, and overflow failover.
- No Go source is touched — this prompt documents what prompts 1–2 shipped.
</summary>

<objective>
Document the `model_pools:` config block and its resolution semantics in the operator-facing docs and changelog, so an operator can hand clients one stable invented model name (e.g. `coding`) backed by a configured choice of providers and know exactly how sessions are pinned, spread, and overflowed.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- Read `docs/config.md` — the `## Schema` YAML block (top-level keys `router`, `allowedApiKeys`, `trace`, `providers`, `aliases`), the section order `## Schema` → `## Routing` → `## Aliases` → `## Requires leading system` → `## Concurrency limit` → `## Upstream pools` (added by spec-012) → `## Auth` → `## Routing by API key` → `## Trace` → ... The new `## Model pools` section slots in right after `## Aliases` and before `## Requires leading system` (both are top-level name-resolution blocks; `## Upstream pools` documents the provider-level pools that `model_pools:` members route through).
- Read `docs/config.example.yaml` — the `aliases:` block at the file tail, after which the new commented `model_pools:` example is added.
- Read `CHANGELOG.md` — the `## Unreleased` heading (created by spec-012 prompt 4) immediately above `## v0.32.0`, with one existing 012 feature bullet. The new `model_pools` bullet is added ABOVE that existing bullet under the same `## Unreleased`. Released sections (`## v0.32.0` and below) are frozen — never edit them.
- Read the prompt 1–2 results (the behavior being documented): `pkg/config.go` (`ModelPoolMember` + `Config.ModelPools` + `validateModelPools` semantics), `pkg/handler/model-pool.go` + `pkg/handler/model-router.go` (the `[route] model=<pool> -> provider=<p> model=<concrete>` line, the pool pre-step, `NewModelRouterWithPools`), and `pkg/factory/factory.go` (the pool table wiring + reload). Document only what those actually ship — no forward-referencing.
- Read `docs/dod.md` — `docs/config.md` / `docs/config.example.yaml` / `CHANGELOG.md` update rules.
- Coding plugin docs (in-container paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — CHANGELOG entry placement and phrasing.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — docs conventions.
</context>

<requirements>
1. **`docs/config.md` — schema block.** In the `## Schema` YAML block, add the top-level `model_pools:` key as a commented optional block in the same `#`-comment style as the existing per-provider `# allowedApiKeys:` / `# maxConcurrentRequests:` lines, e.g.:
   ```yaml
   # model_pools:               # optional; invented model names that resolve to a choice of providers (see ## Model pools). Each entry carries provider/model/weight/overflow.
   #   <poolname>:
   #     - provider: <provider-key>   # required; must exist under providers:
   #       model: <concrete-model>    # required; the fixed model string that provider sees
   #       weight: 1                  # optional; default 1. Relative share of pinned sessions this member receives.
   #       overflow: false            # optional; default false. If true, a saturated pinned member may fail over to a sibling member.
   ```

2. **`docs/config.md` — new `## Model pools` section.** Insert after the `## Aliases` section and before `## Requires leading system`. Document, at the same care level as the neighboring sections (all of DB 1–6 and AC 2–6 must be documented):
   - **What it is.** A top-level `model_pools:` block maps an invented model name (e.g. `coding`) to an ordered list of members, each `{provider, model, weight, overflow}`. A client sends `model: <poolname>`; the router picks one member, rewrites the request body's `model` field to that member's fixed concrete model, and routes through that member's provider. The provider then applies its own upstream pool + session pinning + caps (see `## Upstream pools`). The client never sees which member it got.
   - **Per-member fields.** `provider` (required; must name a provider in `providers:` — an unknown provider fails config load), `model` (required; the concrete model string that provider sees — it may itself match that provider's `models:` globs, which is the normal case), `weight` (optional; default 1; a negative weight is rejected at config load; relative share of PINNED sessions this member receives — 2:1 weight over two members sends ~2/3 of pinned sessions to the heavier member), `overflow` (optional; default false; see failover below).
   - **Session pinning.** A client that sets an `x-session-id` header (e.g. `ANTHROPIC_CUSTOM_HEADERS='{"x-session-id":"<id>"}'` in Claude Code) is pinned to the same pool member on every request via a weighted ring hash of the id — deterministic and stateless, so the member's prompt cache stays warm and the session consistently sees one provider's model. The same id over a two-member pool resolves to the same member across requests and restarts. The header is used only as the selection key, never for auth, and is stripped before forwarding (see `## Upstream pools`).
   - **Idless least-loaded.** A request without `x-session-id` is sent to the least-loaded member (fewest in-flight requests at the member's provider), with round-robin tie-breaking among equally-loaded members — an idless burst (e.g. dark-factory containers) spreads across the members instead of stacking on the first-declared one.
   - **Overflow failover.** When the pinned member's provider is saturated (every capped upstream at its concurrency cap) and the member declares `overflow: true`, the request fails over to the least-loaded sibling member — availability over cache warmth, which costs nothing here because members are different providers/caches anyway. The `[route]` line names the member that actually served the request. With `overflow: false` (the default), the request stays on its pinned member and the provider's own concurrency semantics apply (it waits and answers HTTP 429 with the Anthropic-shaped `rate_limit_error` body — see `## Concurrency limit` / `## Upstream pools`).
   - **Fall-through.** A model name that is not a configured pool name is untouched: it flows through the existing alias + provider-glob routing exactly as before. `model_pools:` names do not interact with `aliases:` — the two blocks are independent ("one name → one model" vs "one name → a choice of models").
   - **Validation.** Unknown provider, negative weight, a duplicate `(provider, model)` pair within a pool, and an empty member list all fail config load with errors naming the pool. `weight: 0` and an absent weight both mean the default 1.
   - **Observability.** Each pool resolution logs `[route] model=<poolname> -> provider=<provider> model=<concrete>` at glog `V(2)` (same verbosity as the `[route] model=... matched ...` detail lines) — the operator evidence of which member served a session. The `[req]` line and `ccrouter_requests_total` / `ccrouter_tokens_total` metrics are unchanged (the metrics' model label is the concrete member model the upstream saw).
   - **SIGHUP.** A change to `model_pools:` (add/remove a member, change a weight or overflow flag) applies on SIGHUP without a restart — the reloader rebuilds the pool table (see `## Reload`).
   - **Security.** A pool name is ordinary client input like any model string — it never widens access; resolution only selects among configured members and their providers' existing auth. The rewritten body carries only the member's configured concrete model — a client cannot inject an arbitrary model string via a pool name.
   - A short worked example matching the spec's operator rung: `model_pools: { coding: [deepseek-pool/deepseek-v4-flash, minimax-pool/MiniMax-2.7] }`, two Claude Code sessions with distinct `x-session-id` values both sending `model: coding` — each lands on its own member consistently, and each upstream log sees only its own model name in the body.

3. **`docs/config.example.yaml`.** After the `aliases:` block (the file tail), add a commented `model_pools:` example showing a two-member pool, e.g.:
   ```yaml
   # model_pools:                 # optional; invented model names that resolve to a choice of providers (see docs/config.md ## Model pools).
   #   coding:
   #     - provider: deepseek-pool      # required; must exist under providers:
   #       model: deepseek-v4-flash     # required; the fixed concrete model this member serves
   #       weight: 2                    # optional; default 1. Share of pinned sessions this member receives.
   #       overflow: true               # optional; default false. Allow failover to a sibling when this member's provider is saturated.
   #     - provider: minimax-pool
   #       model: MiniMax-2.7
   ```
   They MUST be commented (not active) — an operator copying this example must not get an unexpected pool. The file's `model_pools` text may appear only in comments or a disabled placeholder.

4. **`CHANGELOG.md`.** Under the existing `## Unreleased` heading, add one `feat:` bullet mentioning `model_pools`, following `changelog-guide.md` phrasing, ABOVE the existing 012 bullet. Cover: the top-level `model_pools:` schema and `ModelPoolMember` (per-member `provider` / `model` / `weight` / `overflow`), the pool pre-step resolving a client-sent pool name before provider-glob matching with the body's `model` field rewritten to the member's concrete model (`rewriteModelField`), session-pinned member selection via weighted ring hash on the `x-session-id` header (stateless, same session stays on the same member for cache warmth), idless least-loaded dispatch with round-robin tie-breaking, weighted session distribution, `overflow: true` failover to a sibling when the pinned member's provider is saturated (default `overflow: false` keeps the request on its pinned member), fall-through for non-pool names (aliases untouched), validation (unknown provider / negative weight / duplicate pair / empty pool rejected), and SIGHUP reload rebuilding the pool table. Follow the repo's existing entry style (e.g. the `## v0.31.0` / `## v0.29.0` feat entries' detail level).
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Do NOT touch any Go source in this prompt — prompts 1–2 implemented the behavior; this is documentation and changelog only.
- Do NOT invent config knobs, headers, endpoints, or behavior beyond what the spec and prompts 1–2 define — `model_pools:` + the four per-member fields (`provider`, `model`, `weight`, `overflow`) + the `x-session-id` selection key are the entire surface (spec Non-goals: no replacing `aliases:`, no per-request non-pinned switching, no cross-member cache sharing, no complexity-based routing, no health checks / circuit breakers).
- The docs must describe behavior prompts 1–2 actually shipped — no forward-referencing unbuilt features.
- This prompt depends on prompts 1–2: do not approve or execute until prompt 2 has completed and shipped the resolution + factory wiring — the `<context>` reads them.
- Do NOT edit released `CHANGELOG.md` sections (`## v0.32.0` and below) — they are frozen history. Add only under the existing `## Unreleased` (created by spec-012 prompt 4).
- `make precommit` must remain green.
</constraints>

<verification>
make precommit

# AC docs — model_pools documented in both files:
grep -c '^## Model pools' docs/config.md        # expect >=1 (the section itself, not just the schema comment)
grep -c 'model_pools' docs/config.md            # expect >=1
grep -c 'model_pools' docs/config.example.yaml  # expect >=1

# Example stays behavior-neutral (model_pools commented only) — fail if any UNCOMMENTED occurrence exists:
! grep -nE '^[[:space:]]*model_pools:' docs/config.example.yaml   # comment lines start with '#' and do not match

# Changelog bullet under ## Unreleased:
sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c model_pools   # expect >=1
</verification>

---
status: completed
approved: "2026-08-19T15:01:30Z"
generating: "2026-08-19T15:39:21Z"
prompted: "2026-08-19T15:39:21Z"
verifying: "2026-08-19T16:41:42Z"
completed: "2026-08-20T13:46:51Z"
branch: dark-factory/model-pools
---

## Summary

- Today a client picks a model by name; the router maps that model name to a provider via globs. To use a different provider's model, the client must send that provider's concrete model name.
- When two providers are roughly equally capable (e.g. a DeepSeek vLLM and MiniMax), an operator may want to fan a fleet of sessions across both without each client knowing which provider it got.
- This spec adds a top-level `model_pools:` block: an invented name (e.g. `coding`) maps to an ordered list of `{provider, model, weight}` members. A client sends `model: coding`; the router picks one member, **rewrites the request body's `model` field to that member's fixed concrete model**, and routes through that member's provider.
- Selection is session-pinned by default: the same session id keeps the same member across requests, so that member's prompt cache stays warm. When the pinned member's provider is saturated, the request may overflow to another member.
- The existing `aliases:` block is untouched — its invariant "one name → one model" is preserved; `model_pools:` is a separate concern (one name → a choice of models).

## Problem

The router (bborbe/claude-code-router) routes on the client-sent `model` field: a session picks a concrete model name and is locked to that provider for as long as it keeps sending it. To spread sessions across two capable-but-different providers, every client must know both concrete model names and choose between them — which couples the client to the routing topology and makes "use whichever is free" impossible. The operator wants to hand clients one stable name and let the router decide which concrete model serves it, while keeping each session pinned to its chosen member so caches stay warm (the same pinning machinery this project adds in the session-pinning-pools spec).

The `overflow: true` knob exists because saturation is real: when a pinned member's provider is at its per-server concurrency cap (the DeepSeek fleet's cap-8), the session should still get served by a sibling member rather than a 429 — availability over cache warmth, which costs nothing here because members are different providers/caches anyway.

## Goal

An operator can declare `model_pools:` entries, give clients an invented model name, and have the router deterministically resolve that name to a concrete model on a specific provider per session — pinning the session to one member for cache warmth, spreading load across members by weight, and rewriting the body so each upstream sees its own model name.

## Non-goals

- Replacing the `aliases:` block — aliases stay "one name → one model"; `model_pools:` is the new "one name → a choice" block.
- Per-request (non-pinned) member switching within a session — session-pinned by default; a pinned session stays on its member.
- Cross-member cache sharing — members are different providers/caches by design; overflow costs cache warmth and is allowed because members share no cache anyway.
- Complexity-based routing (cost/latency-aware per-turn selection) — separate backlog goal.
- Health-checking / circuit breakers — a dead member's requests fail; no probe-and-rotate.

## Acceptance Criteria

- [ ] **Config parse + validation:** a top-level `model_pools:` block parses; each entry is an ordered list of `{provider, model, weight}`; unknown providers fail validation; negative `weight` rejected (absent/0 defaults to 1 — yaml `int` cannot distinguish them); duplicate `(provider, model)` pairs rejected within a pool (the same pair in two different pools is not a duplicate); an empty member list is rejected; the existing `aliases:` block parses unchanged. Evidence: `go test -count=1 ./pkg/` passes (new Ginkgo rows assert each rule); `grep -c 'model_pools' pkg/config_test.go` ≥1.
- [ ] **Resolution + rewrite:** `model: coding` resolves for one pinned session to `deepseek-v4-flash` via deepseek-pool and for another to `MiniMax-2.7` via minimax-pool — the request body's `model` field is rewritten to the member's fixed model before forwarding. Evidence: the handler-test log shows `[route] model=coding -> provider=<p> model=<concrete>` for each session; `go test -count=1 ./pkg/handler/` passes (new Ginkgo row asserts the rewritten body). (This is a handler-test log, not the deployed router — the deployed check lives in the operator-executable rung.)
- [ ] **Session-pinned member:** the same `x-session-id` resolves `model: coding` to the same member across requests (weighted choice), so the member's cache stays warm. Evidence: `go test -count=1 ./pkg/handler/` passes (new Ginkgo row asserts stable member for a fixed session id over N requests).
- [ ] **Idless least-loaded:** a `model: coding` request WITHOUT `x-session-id` is resolved to the least-loaded member's provider, never the first-declared one. Evidence: `go test -count=1 ./pkg/handler/` passes (new Ginkgo row asserts an idless burst spreads off the first member, mirroring the sibling spec's least-loaded rule).
- [ ] **Weighted distribution:** over a sample of pinned sessions, a heavier-weighted member receives proportionally more sessions. Evidence: `go test -count=1 ./pkg/handler/` passes (new Ginkgo row: with a 2:1 weight ratio over N=100 distinct session ids, the heavier member serves ≥55 of the 100).
- [ ] **Member overflow:** when the pinned member's provider pool is saturated and the member declares `overflow: true`, the request is served by another member and logged with the actual serving provider/model. Evidence: `go test -count=1 ./pkg/handler/` passes (new Ginkgo row asserts sibling-member serve + log line naming the actual provider/model).
- [ ] **SIGHUP reload:** a second `CreateRouterFromConfig` with a changed `model_pools:` (add/remove member, re-weight) rebuilds the resolution table and the rebuilt table resolves the new member. Evidence: `go test -count=1 ./pkg/factory/` passes (new wiring row asserts the rebuilt table resolves the new member); `grep -c 'model_pools' pkg/factory/*_test.go` ≥1.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format / lint / vet / vulncheck clean
- `make test` — full Go suite passes, including the new Ginkgo rows named in the ACs
- `grep -c 'model_pools' docs/config.md docs/config.example.yaml` → ≥1 per file
- `sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c 'model_pools'` → ≥1

### Operator-executable (runs on the host after PR merge + release + install)

- `make install`; restart the launchd service (`de.bborbe.claude-code-router`).
- Add a `model_pools:` entry (e.g. `coding:` → deepseek-pool/deepseek-v4-flash + minimax-pool/MiniMax-2.7) to `~/.config/claude-code-router/config.yaml`; `kill -HUP $(pgrep claude-code-router)`.
- Two Claude Code sessions with distinct `x-session-id` both send `model: coding`; `/tmp/claude-code-router.log` shows each session resolving to one concrete model consistently (one → deepseek-v4-flash, the other → MiniMax-2.7), and each upstream log sees only its own model name in the body.

## Desired Behavior

1. A top-level `model_pools:` block maps an invented model name to an ordered list of members `{provider, model, weight}`; `weight` defaults to 1 when absent or 0 (yaml `int` cannot distinguish them); negative `weight` rejected at validation; duplicate member (provider, model) pairs are rejected at validation.
2. When the client sends `model: <poolname>`, the router resolves the pool before provider-glob routing: it selects one member, rewrites the body's `model` field to that member's fixed concrete `model`, and routes through that member's provider (which then applies its own upstream pool + pinning + caps per the session-pinning-pools spec).
3. Member selection is **session-pinned**: the same `x-session-id` selects the same member on every request (weighted choice keyed on the session id), so the session's prompt cache stays warm on that member. A session with no id selects a member by least-loaded provider.
4. When the selected member's provider is saturated and the member declares `overflow: true`, the request fails over to another member of the same pool — served and logged with the actual provider/model. Default `overflow: false` (the request waits/429s per the provider's concurrency semantics).
5. `aliases:` behavior is unchanged — aliases still resolve one name to one model; `model_pools:` names do not interact with aliases.
6. SIGHUP hot-reload applies `model_pools:` changes (add/remove member, re-weight) without a process restart.

## Constraints

- Config schema: top-level `ModelPools map[string][]ModelPoolMember` (yaml `model_pools`, `omitempty`); `ModelPoolMember` = `{Provider string, Model string, Weight int, Overflow bool}` (yaml `overflow`, `omitempty`).
- `model_pools:` is resolved as a pre-step before the glob walk — a `model` that matches a pool name never reaches the glob matcher. Rewrite reuses the existing `rewriteModelField` machinery.
- A pool member's `provider` must name an existing provider (validated); a member's `model` is the concrete string sent to that provider (may itself match the provider's globs — that is the normal case).
- Pinning is stateless (weighted choice keyed on session id — same deterministic mechanism as the session-pinning-pools spec); no in-memory session→member map.
- `x-session-id` is used only as a selection key, never for auth.
- 429/failover semantics come from the underlying provider's concurrency config (session-pinning-pools spec) — this spec adds the resolution layer only.
- Tests follow the repo's Ginkgo convention; no new dependencies; no AI attribution; no real 30s waits.
- Docs: `docs/config.md` + `docs/config.example.yaml` document `model_pools:`; CHANGELOG `## Unreleased` bullet. Reference `docs/config.md` for the existing glob/alias mechanism the resolution pre-step sits before.

## Assumptions

- Inherited from the session-pinning-pools spec: single router instance (no cross-instance pool state), static weights via SIGHUP, statistical distribution accepted (deterministic per session, approximate per spread), `x-session-id` present only when the client opts in.
- Pool-member providers already exist with their own auth config; this spec adds no auth surface.
- The sibling spec ships first — `model_pools:` resolution builds on its pool + least-loaded + per-upstream-cap machinery.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Pool name not configured | Falls through to provider-glob routing as today (no match → default provider) | Operator configures the pool and SIGHUPs; the next `[route]` log line for that model reflects the pool |
| Unknown provider in a member | Config rejected at validation | Correct the provider name; SIGHUP re-applies |
| Duplicate (provider, model) pair | Config rejected at validation | Remove the duplicate; SIGHUP re-applies |
| Pinned member's provider saturated (overflow off) | Request waits / 429s per the provider's concurrency config | Queue drains; no operator action; 429 lines stop once load drops below the cap |
| Pinned member's provider saturated (overflow on) | Request served by another pool member; logged with actual provider/model | None — failover is the recovery; the `[route]` line names the serving member |

## Security / Abuse

- A `model_pools:` name is ordinary client input like any model string — it never widens access; resolution only selects among configured members and their providers' existing auth.
- The rewritten body carries only the member's configured concrete model — a client cannot inject an arbitrary model string via a pool name.
- No internal state (pool internals, other members) is exposed in responses or logs beyond the intended `[route]` line.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Config: `model_pools:` schema + `ModelPoolMember` + validation (unknown provider, weight, duplicate pairs) + backward-compat aliases | 1, 5 | 1 | — |
| 2 | Resolution: pool pre-step — session-pinned weighted member choice, body rewrite via `rewriteModelField`, idless least-loaded fallback, `overflow: true` failover; factory wiring test for the SIGHUP-rebuilt table | 2, 3, 4, 6 | 2, 3, 4, 5, 6, 7 | prompt 1; session-pinning-pools spec (pool/least-loaded machinery) |
| 3 | Docs + CHANGELOG: `docs/config.md`, `docs/config.example.yaml`, `## Unreleased` bullet | 1 (documented) | docs greps only | prompts 1–2 |

Rationale: prompt 1 lands the config contract and its validation; prompt 2 wires resolution on top of the session-pinning-pools selection machinery and owns the factory reload test (AC 7); prompt 3 documents what 1–2 shipped and owns no acceptance criterion itself.

## Do-Nothing Option

Clients must send concrete model names and choose providers themselves; spreading sessions across two capable providers means editing every client's model pick. The router already has the rewrite machinery (aliases) and this spec reuses the pool + pinning machinery from session-pinning-pools — deferring only keeps the operator from handing clients one stable name and moving the provider choice into config.

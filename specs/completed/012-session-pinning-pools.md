---
status: completed
approved: "2026-08-19T15:01:30Z"
generating: "2026-08-19T15:22:10Z"
prompted: "2026-08-19T15:22:10Z"
verifying: "2026-08-19T16:15:42Z"
completed: "2026-08-20T14:39:00Z"
branch: dark-factory/session-pinning-pools
---

## Summary

- The router today is stateless per request: a provider has exactly one `upstream`, and every request matching its `models:` glob goes there — no session identity, no load spread, no per-server concurrency accounting.
- When a provider has more than one server (e.g. five DeepSeek vLLM instances), the same Claude Code session's turns can land on different servers, cold-starting the upstream prompt cache on every turn — token burn and latency on long sessions.
- Keyless callers (dark-factory containers) have no routing key at all, so a pool with a "first-wins" fallback would stack all their load on one server and trip its concurrency cap.
- This change gives a provider a **pool of upstreams** (`upstreams:`), pins each session to one member via an `x-session-id` header so its prompt cache stays warm, spreads keyless traffic by least-loaded, and moves the `maxConcurrentRequests` cap down from provider to **per upstream** (each DeepSeek server allows 8 — that is a per-server limit, not a per-provider one).
- Legacy single-`upstream:` configs load unchanged as a one-entry pool; SIGHUP hot-reload applies every config change.

## Problem

`bborbe/claude-code-router` forwards each `/v1/*` request to exactly one upstream per provider. `Provider` config holds a single `upstream` URL (pkg/config.go:122-172), and the model router (pkg/handler/model-router.go) never sees a session or conversation id — routing is per-request, stateless. Once a provider runs more than one server, two failures compound:

1. **Cache warmth.** The same session's turns flip between servers, so each server's prompt-cache prefix is cold on every other turn. For long coding sessions this is measurable token + latency waste, and it gets worse the more replicas exist.
2. **Concurrency accounting.** `maxConcurrentRequests` (spec 011) is modeled per provider — one semaphore per provider name. But `vllm.seibert.tools`' cap of 8 is a **per-server** limit. Two upstreams each allowing 8 must not share one global cap of 8; and a keyless flood stacking on the first-declared server blows that server's cap while siblings sit idle.

The router is the right place to fix both: it already reads the body, swaps auth per provider, and counts concurrency — it just lacks session identity and a pool concept.

## Goal

A provider can declare a pool of upstreams. Each request is dispatched to exactly one member: a request carrying an `x-session-id` is pinned to the same member every time (weighted, deterministic, stateless — so the session's prompt cache stays warm on that server), a request without one is sent to the least-loaded member, and every member independently enforces its own `maxConcurrentRequests` cap. All of it config-driven and hot-reloadable; existing single-upstream configs behave byte-for-byte as today.

## Non-goals

- Virtual models (`model_pools:` — one invented name resolving to different concrete models per provider) — separate spec; virtual models build on this one's pool machinery.
- Pool-level overflow failover (jumping a pinned session to a sibling member when its pinned member is saturated) — no consumer names the availability-over-cache tradeoff yet; member overflow for virtual models is handled in the model-pools spec; defer the homogeneous-pool variant until a consumer exists.
- Complexity-based routing (cost / latency-aware per-turn selection) — separate backlog goal.
- Health-checking / circuit breakers — a dead upstream fails the request; there is no probe-and-rotate.
- Relaying `x-session-id` across a chain of router instances (cluster router → burn relay) — follow-up.
- Dynamic weight rebalancing — weights are static YAML; SIGHUP is the only change channel.
- Per-model concurrency limits — the cap is per upstream, not per model.
- Client-side plumbing for automatic `x-session-id` injection across launcher scripts — only the verification injection via `ANTHROPIC_CUSTOM_HEADERS` is exercised here; generalizing is a follow-up.

## Acceptance Criteria

- [ ] **Backward-compat load:** an existing `~/.config/claude-code-router/config.yaml` (single `upstream:`, provider-level `maxConcurrentRequests`/`maxConcurrentWaitSeconds`) passes `config.Load` + validation with zero edits and behaves as a one-entry pool. Evidence: `go test -count=1 ./pkg/` passes (new Ginkgo row asserts the legacy shape loads and its caps land on the single entry); `grep -c 'Upstreams' pkg/config_test.go` returns ≥1.
- [ ] **Session pinning:** two concurrent Claude Code sessions with distinct `x-session-id` values land on the same upstream every request, each on its own member. Evidence: the test-run log shows `[route] session=<id> upstream=<url>` stable across turns per id — `grep -c 'session=' <test log>` ≥2 distinct ids, each mapping to exactly one upstream. (This is a handler-test log, not the deployed router — the deployed check lives in the operator-executable rung.)
- [ ] **Keyless least-loaded:** a request WITHOUT `x-session-id` is dispatched to the least-loaded member, never the first-declared one. Evidence: during a keyless burst the test-run log's `[route] session=<empty> upstream=<url>` lines show ≥2 distinct `upstream=` values, and no single member stacks to its cap; `go test -count=1 ./pkg/handler/` passes (new Ginkgo row asserts the spread).
- [ ] **Weighted distribution:** over a sample of pinned sessions, a heavier-weighted member receives proportionally more sessions. Evidence: `go test -count=1 ./pkg/handler/` passes (new Ginkgo row: with a 2:1 weight ratio over N=100 distinct session ids, the heavier member logs ≥55 of the 100).
- [ ] **Per-upstream cap:** a pool member with `maxConcurrentRequests: 8` holds at most 8 in flight; the 9th concurrent waits up to `maxConcurrentWaitSeconds` then receives HTTP 429 with an Anthropic-shaped `rate_limit_error` body. Evidence: `go test -count=1 ./pkg/handler/` passes (new Ginkgo row asserts status 429 + body contains `rate_limit_error`); `grep -c 'maxConcurrentRequests' pkg/config_test.go` ≥1.
- [ ] **SIGHUP reload:** a second `CreateRouterFromConfig` with a changed `upstreams:` list / weight / cap rebuilds the pool and the rebuilt tree enforces the new pool. Evidence: `go test -count=1 ./pkg/factory/` passes (new wiring row asserts the rebuilt tree enforces the new cap/weight); `grep -c 'rebuilt' pkg/factory/*_test.go` ≥1.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format / lint / vet / vulncheck clean
- `make test` — full Go suite passes, including the new Ginkgo rows named in the ACs
- `grep -c 'upstreams' docs/config.md docs/config.example.yaml` → ≥1 per file
- `sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c 'upstreams'` → ≥1

### Operator-executable (runs on the host after PR merge + release + install)

- `make install`; restart the launchd service (`de.bborbe.claude-code-router`).
- In `~/.config/claude-code-router/config.yaml`, give `seibert-vllm-default` an `upstreams:` list of 2+ members each with `maxConcurrentRequests: 8`; `kill -HUP $(pgrep claude-code-router)`.
- Launch two Claude Code sessions with distinct `ANTHROPIC_CUSTOM_HEADERS='{"x-session-id":"<id>"}'`; over several turns each, `/tmp/claude-code-router.log` shows `[route] session=<id> upstream=<url>` stable per id, and the two ids land on different members.
- Fire 9 concurrent `curl /v1/messages` at one capped member → the 9th returns 429 with `rate_limit_error` after the wait, and no vllm `max 8 per user` 429 reaches a client.
- A config edit (add a member / change a weight) applies on SIGHUP without restart; the next request's `[route]` log line reflects the new pool.

## Desired Behavior

1. A provider config accepts an `upstreams:` list; each entry carries `{upstream, token, weight, maxConcurrentRequests, maxConcurrentWaitSeconds}`. The legacy single `upstream:` / `token:` / provider-level caps parse as a one-entry pool with weight 1. `weight` defaults to 1 when absent or 0 (yaml cannot distinguish them with `Weight int`); negative weights are rejected at validation.
2. The router reads an inbound `x-session-id` header into a context value (mirroring the `presented-api-key` pattern); the header is stripped before forwarding upstream. Absent header → empty session id.
3. A request with a non-empty session id is dispatched to the pool member selected by a **weighted ring hash of the session id** — deterministic and stateless, so the same session hits the same member on every request, across restarts, with no in-memory map.
4. A request with an empty session id is dispatched to the **least-loaded** member (fewest in-flight requests by the per-upstream semaphore), never the first-declared one — keyless floods spread across the pool instead of stacking on one server.
5. Each pool member enforces its own `maxConcurrentRequests` (absent / 0 / negative = unlimited, spec 011 semantics) and `maxConcurrentWaitSeconds` (default 30). A request that acquires a slot within the wait is forwarded normally; one that times out gets HTTP 429 with an Anthropic-shaped `rate_limit_error` body.
6. SIGHUP hot-reload rebuilds the pool tree from config: added/removed members, changed weights and caps apply without a process restart (existing reloader).

## Constraints

- Config schema: `Provider` gains `Upstreams []Upstream` (yaml `upstreams`, `omitempty`); `Upstream` = `{Upstream string, Token string, Weight int, MaxConcurrentRequests int, MaxConcurrentWaitSeconds int}`. Legacy single-`upstream:` form is sugar for a one-entry pool — its provider-level `maxConcurrentRequests` / `maxConcurrentWaitSeconds` become the single entry's caps. When `upstreams:` is present it wins; validation rejects both `upstreams:` and `upstream:` set together.
- Session pinning is **stateless**: weighted ring hash only, no session→member map, no TTL, no memory growth. Recomputable from the session id at any time.
- The router reads `x-session-id` only as hash input — never for auth. Auth stays exactly as today (`allowedApiKeys`, loopback bypass).
- The per-upstream semaphore is a generalized `pkg/handler/concurrency-limiter.go` (spec 011): one instance per pool member instead of one per provider. Existing provider-level cap behavior on single-upstream providers is unchanged.
- Weighted selection: ring of cumulative weights, session id hashed onto it. Distribution is statistical — a specific session is deterministic, the *spread* across sessions follows the weights.
- 429 body stays Anthropic-shaped and generic (`rate_limit_error`, no internal state / queue depth / upstream URL / provider name).
- Tests follow the repo's Ginkgo convention and must not depend on real 30s waits (small explicit waits in tests). No new dependencies; no AI attribution.
- Docs: `docs/config.md` + `docs/config.example.yaml` document `upstreams:`, per-entry fields, and the legacy form; CHANGELOG `## Unreleased` bullet.

## Assumptions

- Single router instance — no cross-instance pool state; pinning and least-loaded are per-process.
- Weights are static YAML; SIGHUP is the only change channel.
- Statistical distribution is accepted: pinning is deterministic per session, but the session-to-member spread only approximates the weights.
- `x-session-id` is present only when the client opts in via `ANTHROPIC_CUSTOM_HEADERS`; absence is the normal state for container traffic.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Pinned member saturated | Request waits ≤ `maxConcurrentWaitSeconds`, then 429 (client retries with backoff) | Queue drains; no operator action; subsequent `[req] ... status=429` lines stop appearing once load falls below the cap |
| Upstream member down | Requests to it fail (502 sanitized, existing behavior); no probe-and-rotate (out of scope) | Operator removes/repairs the member and SIGHUPs; the next request's `[route]` log line reflects the pool without the member |
| Keyless flood (dark-factory burst) | Least-loaded spread + per-member caps bound each server | 429s shed load; client backoff spaces retries; no member's cap is exceeded (per-cap 429 lines name the member) |
| Weight misconfiguration (negative) | Config rejected at validation | Correct the weight; SIGHUP re-applies |
| Client disconnects while queued | No slot held (slot acquired at dispatch); 429 write to dead connection fails harmlessly | None — connection teardown is the recovery |
| Config reload mid-request | Old handler tree finishes in-flight requests; new tree serves subsequent ones (existing reloader semantics) | None; the next request's `[route]` line reflects the new pool |

## Security / Abuse

- `x-session-id` is untrusted client input: used solely as a hash key for pinning, never for authentication or authorization decisions.
- An attacker flooding a pool cannot make any single upstream exceed its cap (semaphore is per member), and memory stays bounded by request-rate × wait.
- 429 response bodies reveal no internal state (queue depth, upstream URLs, provider names, session ids).

## Suggested Decomposition

Prompts generated in this order — each row is one prompt.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Config: `Provider.upstreams:` schema + `Upstream` type + weight validation + legacy one-entry sugar + backward-compat load rows | 1 | 1 | — |
| 2 | Session pinning + selection: `x-session-id` middleware → context → weighted ring hash; keyless → least-loaded (via semaphore in-flight counts); header strip | 2, 3, 4, 5 | 2, 3, 4 | prompt 1 |
| 3 | Per-upstream concurrency + reload wiring: generalize `concurrency-limiter.go` provider→member; wait→429; factory wiring test for the rebuilt pool tree | 6 | 5, 6 | prompts 1 (upstreams), 2 (semaphore in-flight counts for least-loaded) |
| 4 | Docs + CHANGELOG: `docs/config.md`, `docs/config.example.yaml`, `## Unreleased` bullet | 1 (documented) | docs greps only | prompts 1–3 |

Rationale: prompt 1 establishes the config contract and factory wiring; prompt 2 builds session/keyless selection on top; prompt 3 generalizes the limiter and locks the SIGHUP-rebuilt pool (AC 6); prompt 4 documents what 1–3 shipped and owns no acceptance criterion itself.

## Do-Nothing Option

The router keeps one-upstream-per-provider routing. Sessions keep flipping servers on multi-replica providers, paying cold-cache token + latency cost on every turn; keyless dark-factory bursts stack on the first server and trip its cap while siblings idle; and two providers pointing at the same vLLM share one global cap of 8 instead of the per-server 8 each deserves. The fix is config + selection + limiter generalization — deferring it keeps paying the burst and cache tax on every provider that gains a second server.

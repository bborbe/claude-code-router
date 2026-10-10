---
status: draft
---

## Summary

- The router caps concurrent `/v1/*` requests per provider and per upstream-pool member, but those caps are counted independently: nothing bounds how many requests reach one server when several providers resolve to it.
- `seibert-vllm-default`'s three pool members (16 / 32 / 32, time-windowed) and `seibert-dark-factory`'s member (16) all resolve to `vllm.seibert.tools`, so the caps that are individually enforced sum to ~32 concurrently in the daytime window and ~48 at night or on weekends.
- On 2026-10-10 a fleet burst pushed `vllm.seibert.tools` over its capacity and it served nothing for ~17 minutes, for every user of that backend.
- This change adds an optional per-upstream-**host** cap: every provider and pool member whose upstream resolves to the same host shares one semaphore, so the total reaching that host is bounded regardless of how the providers are configured.
- It also exports `ccrouter_upstream_inflight{host=...}`, so the per-host in-flight count — and therefore whether the shared cap is holding — is observable rather than inferred.

## Problem

Concurrency caps are configured per provider and per pool member, and each enforces its own. The router has no notion of the *server* those providers share. An operator who sets `maxConcurrentRequests: 16` on two providers pointing at one host has configured a ceiling of 32, not 16 — and there is no config that expresses "this host takes at most 8, whoever sends the traffic". The 2026-10-10 outage was exactly this: the per-provider caps were correct and individually respected, the host still received ~32 concurrent cold prefills, and it stopped serving. The router is the only component that knows every provider's upstream, so it is the only place the host-level bound can be enforced.

## Goal

The router config can cap the concurrent `/v1/*` requests reaching one upstream **host**, and every provider and pool member resolving to that host draws from that one shared budget. A host at its cap holds the excess in a bounded queue and answers a request that waits too long with a clean, retryable HTTP 429 — so no combination of provider-level caps can push a single host past its configured ceiling, and the in-flight count per host is readable from `/metrics`.

## Non-goals

- No change to the per-provider and per-member `maxConcurrentRequests` / `maxConcurrentWaitSeconds` semantics. They keep their own independent budgets; the host cap is an additional, coarser bound layered over them.
- This **deliberately reverses** a non-goal of spec 011 — *"No shared/global semaphore across providers — caps are per-provider and independent, even when two providers share one upstream"* (`specs/completed/011-max-concurrent-requests.md`). That non-goal was right for a per-provider throttle and is precisely what this spec supersedes: the host cap is the shared bound it excluded. Spec 011's per-provider and per-member caps stay in force and are not relaxed or removed, so a reader should not read the two specs as contradictory — 011 bounds one provider, this bounds one server.
- No automatic discovery or derivation of host caps from provider config. A host is capped only when the operator names it; every other host stays unlimited.
- No change to auth, model routing, alias resolution, key routing, system-lift, or body handling.
- No start-rate limiting, no latency-based backoff, no circuit breaker, no 5xx throttle.
- No change to the `llm-proxy` upstream (`api.gpt.seibert.ai`) or to any provider's own config block.
- No new queue-depth, wait-time, or rejection metrics beyond the one in-flight gauge this spec names.

## Acceptance Criteria

- [ ] Config parsing: a config with a top-level `upstreamHostLimits` map naming one host loads and validates; a config with no `upstreamHostLimits` key loads identically to today. Evidence: `go test -count=1 ./pkg/` passes and `grep -c 'upstreamHostLimits' pkg/config_test.go` returns ≥1 (new Ginkgo rows assert both load paths).
- [ ] Lenient validation: a negative host `maxConcurrentRequests` is treated as unlimited and a negative host `maxConcurrentWaitSeconds` as the 30s default — the config always loads. Evidence: `go test -count=1 ./pkg/` passes (new Ginkgo row asserts a negative value loads and behaves as its fallback).
- [ ] Shared budget across providers: with a host cap of N and two providers resolving to that host, the total in flight to that host never exceeds N, even when each provider's own cap is above N. Evidence: `go test -count=1 ./pkg/factory/` passes (new wiring row drives both providers concurrently and asserts the host limiter's observed peak is N, not 2N).
- [ ] Shared budget across pool members: with a host cap of N and a provider whose pool holds two members on that host, the two members share the one N-budget rather than holding N each. Evidence: `go test -count=1 ./pkg/factory/` passes (new wiring row asserts one host limiter instance is constructed for the host, not one per member).
- [ ] Queue timeout: a request that waits longer than the host `maxConcurrentWaitSeconds` receives HTTP 429 whose JSON body contains `"type":"rate_limit_error"` and never 5xx. Evidence: `go test -count=1 ./pkg/handler/` passes (new row asserts status 429 + body contains `rate_limit_error`, and asserts the status is not ≥500).
- [ ] Uncapped host passthrough: a host named nowhere in `upstreamHostLimits` forwards without queueing and without router-issued 429s, byte-for-byte today's behavior. Evidence: `go test -count=1 ./pkg/handler/` passes (new row asserts immediate pass-through on an unlimited host limiter) and `go test -count=1 ./pkg/factory/` passes (row asserts a provider on an uncapped host is not wrapped in a queueing limiter).
- [ ] Per-member caps still enforced inside the host cap: a member cap below the host cap still queues at the member level. Evidence: `go test -count=1 ./pkg/factory/` passes (new row asserts a member cap of 2 under a host cap of 8 admits 2).
- [ ] In-flight gauge: `/metrics` exposes a gauge `ccrouter_upstream_inflight` labelled by `host`, reporting that host's current in-flight request count — the shared semaphore's occupancy where a cap is configured, the in-flight count where none is. Evidence: `go test -count=1 ./pkg/handler/` passes (new row asserts the collector reports occupancy 0 at rest, 1 while a request is held in flight, and 0 after it returns) and `grep -c 'ccrouter_upstream_inflight' pkg/handler/metrics.go` returns ≥1.
- [ ] Reload: a second `CreateRouterFromConfig` call with a changed host cap builds a host limiter enforcing the new value, and a host removed from `upstreamHostLimits` becomes unlimited. Evidence: `go test -count=1 ./pkg/factory/` passes (new row asserts both directions across two builds, mirroring the SIGHUP reloader path).
- [ ] Docs: `docs/config.md` documents the `upstreamHostLimits` schema keys, their absent/zero/negative semantics and the gauge; `docs/config.example.yaml` shows a host-cap block; and `docs/metrics.md` documents the new series in its Series table with a cardinality note, as specs 007 and 018 did for the series they added. Evidence: `grep -c 'upstreamHostLimits' docs/config.md docs/config.example.yaml` returns ≥1 for each file, and `grep -c 'ccrouter_upstream_inflight' docs/config.md docs/metrics.md` returns ≥1 for each file.
- [ ] Docs no longer assert the reversed design: the statements that caps are independent even when two providers share one upstream are rewritten to describe the host cap. Evidence: `grep -n 'independent' docs/config.md` returns no line asserting that two providers sharing one upstream cannot be jointly capped.
- [ ] CHANGELOG: `CHANGELOG.md` has a bullet under `## Unreleased` mentioning `upstreamHostLimits`. Evidence: `sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c upstreamHostLimits` returns ≥1.
- [ ] **Post-Deploy (Rung-2):** the live router enforces the host cap — a burst of 16 concurrent requests to `seibert-vllm-default` peaks `ccrouter_upstream_inflight{host="vllm.seibert.tools"}` at the configured cap and never exceeds it, and a control burst with the host cap absent peaks at 16. Evidence: `curl -fsS http://127.0.0.1:8788/metrics | grep 'ccrouter_upstream_inflight{host="vllm.seibert.tools"}'` sampled every 200 ms during the burst peaks at the configured value, not at 16.
  - `deploy_check:` `grep -o 'version=[^ ]*' /tmp/claude-code-router.log | tail -1 | sed 's/version=//'`
  - `deploy_target:` `$(git tag --sort=-v:refname | head -1)`

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format / lint / vet / vulncheck clean
- `make test` — full Go suite passes, including the new Ginkgo rows named in the ACs
- `grep -c 'upstreamHostLimits' docs/config.md docs/config.example.yaml` → ≥1 for each file
- `grep -c 'ccrouter_upstream_inflight' pkg/handler/metrics.go docs/config.md` → ≥1 for each file
- `sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c upstreamHostLimits` → ≥1

### Operator-executable (runs on the host after PR merge, release and local install)

- `make install`, then `launchctl kickstart -k gui/$(id -u)/de.bborbe.claude-code-router`; confirm `grep -o 'version=[^ ]*' /tmp/claude-code-router.log | tail -1` reports the released tag.
- Stage first on a test port per `docs/launchd-service.md`: copy the live config, add an `upstreamHostLimits` block with `vllm.seibert.tools: maxConcurrentRequests: 8`, start the new binary on `127.0.0.1:8799`, and confirm `curl -fsS http://127.0.0.1:8799/metrics | grep ccrouter_upstream_inflight` reports the `host` label before touching `:8788`.
- With the host cap at 8, fire 16 concurrent `curl /v1/messages` to `seibert-vllm-default` and sample the gauge every 200 ms; the peak must be 8, and requests not admitted within the wait window must return 429 with `rate_limit_error`.
- Remove the host cap and repeat: the peak must be 16, with no router-issued 429.

## Desired Behavior

1. A config may declare `upstreamHostLimits`, a map keyed by upstream host, each value carrying `maxConcurrentRequests` and `maxConcurrentWaitSeconds` with the same absent/zero/negative semantics as the provider-level fields. A host not named in the map is unlimited.
2. Every provider and every pool member whose upstream URL resolves to a named host draws its concurrency from that host's single shared budget. The host's cap is the count of requests in flight to that host, summed across all providers and members resolving to it.
3. A request bound for a capped host acquires a host slot before it is forwarded. When the host is at its cap the request waits in a bounded queue rather than being forwarded.
4. A request that acquires a host slot within the host's `maxConcurrentWaitSeconds` is forwarded normally and the client sees the upstream's response unchanged. The slot is held for the full request duration, including streaming SSE responses.
5. A request still waiting after the host's `maxConcurrentWaitSeconds` is answered HTTP 429 with the Anthropic-shaped body `{"type":"error","error":{"type":"rate_limit_error","message": ...}}`, carrying no host name, queue depth, or provider name. A host-cap rejection is never a 5xx.
6. The per-provider and per-member `maxConcurrentRequests` caps continue to apply inside the host cap: a request must hold both a host slot and its provider/member slot to be forwarded, so the effective limit at any instant is the lower of the two.
7. `/metrics` exposes `ccrouter_upstream_inflight{host="<host>"}`, a gauge of the current in-flight request count for that host, present for every host the router serves — including uncapped ones, where it reports the in-flight count with no limit applied.
8. A SIGHUP config reload applies changed, added, and removed `upstreamHostLimits` entries without a process restart: a host cap raised takes effect immediately, and a host removed from the map becomes unlimited.

## Constraints

- Config schema: a new top-level `upstreamHostLimits` key, `map[string]HostLimit` (yaml `upstreamHostLimits`, `omitempty`), where `HostLimit` carries `maxConcurrentRequests int` (yaml `maxConcurrentRequests,omitempty`) and `maxConcurrentWaitSeconds int` (yaml `maxConcurrentWaitSeconds,omitempty`). The zero value of the whole key must remain today's behavior.
- Host key derivation: the upstream URL's host, lowercased, with an explicit non-default port appended as `host:port`. `https://vllm.seibert.tools` and `https://vllm.seibert.tools/v1` both key `vllm.seibert.tools`; `http://127.0.0.1:8317` keys `127.0.0.1:8317`. Matching against the map is exact string equality on that value.
- Validation is lenient, matching the provider-level fields: a negative host `maxConcurrentRequests` is unlimited, a negative host `maxConcurrentWaitSeconds` is the 30s default, and no value fails `config.Load`.
- The host limiter is composed **outside** the existing per-provider / per-member limiter, so a request acquires its host slot first. The existing `NewConcurrencyLimiter` in `pkg/handler/concurrency-limiter.go` is reused for the host budget rather than a second queueing mechanism being written; its 429 body and wait semantics are unchanged.
- The 429 body stays byte-identical to the provider-level limiter's `limiter429Body`; the host limiter adds no field naming the host.
- The existing `ccrouter_*` counter and histogram series are unchanged; `ccrouter_upstream_inflight` is additive.
- The reloader (`pkg/reloader`) rebuilds host limiters through the same factory path as provider limiters; no reloader-specific host-cap code.
- Go style, error wrapping (`github.com/bborbe/errors`), GoDoc on exported items, and the Ginkgo/Gomega test conventions of the repo apply unchanged.

## Assumptions

- One host limiter can be built per distinct host inside the existing factory loop and shared by every provider and pool member resolving to that host, without restructuring that loop's per-upstream iteration.
- The existing `NewConcurrencyLimiter` can serve as the host budget unchanged — its queue, wait and 429 semantics are what the host cap needs, so no second queueing mechanism is written.
- The host key derived from an upstream URL is stable for the life of a config, so a limiter built at reload time keys the same host that a request resolves at dispatch time.
- Provider and pool-member upstreams are already parsed at config load (`normalizeUpstreams`), so host derivation adds no new parsing path.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Reversibility |
|---|---|---|---|---|
| Host cap reached, request waits past `maxConcurrentWaitSeconds` | HTTP 429 with the Anthropic-shaped `rate_limit_error` body; no slot leaked | Client (Claude Code SDK) backs off and retries, as it already does for the provider-level 429 | `ccrouter_requests_total` for the host's providers shows 4xx; `/tmp/claude-code-router.log` shows `status=429` | Reversible — the request was never forwarded |
| Host cap set too low for normal traffic | Requests queue and then 429 under ordinary load | Operator raises `maxConcurrentRequests` for that host and SIGHUPs; no restart needed | Sustained 429s at low load, `ccrouter_upstream_inflight` pinned at the cap | Reversible via config + SIGHUP |
| Host cap set higher than the server tolerates | No protection; the server degrades as before this spec | Operator lowers the cap and SIGHUPs | Upstream 5xx or latency rise with the gauge below the cap | Reversible via config + SIGHUP |
| A provider's member cap is below the host cap and saturates, while host slots are held by its queued requests | Requests to another provider on the same host can wait on host slots held by the first provider's queue | Operator raises the saturated member cap, or lowers it so fewer host slots are held while queued | `ccrouter_upstream_inflight` pinned at the host cap while one provider shows member-cap saturation | Reversible via config + SIGHUP |
| Two upstreams differ only by port | They key separately (`host:port`) and are capped independently | Operator names each key explicitly | The two hosts report separate `ccrouter_upstream_inflight` series | n/a — configuration choice |
| Config names a host no provider resolves to | The entry loads and is inert; no limiter is built for it | None required; the operator may remove the dead entry | The host has no `ccrouter_upstream_inflight` series | Reversible via config |
| A host is removed from `upstreamHostLimits` on reload | The host becomes unlimited on the rebuilt handler tree; in-flight requests finish on the old tree | None required | The gauge still reports the host, now below any cap | Reversible via config + SIGHUP |
| Sustained arrival above the host cap | The queue is bounded by arrival rate × wait window, matching the provider-level limiter; memory grows with the queue, not without limit | Operator lowers the cap or the wait window, or raises the host's capacity | `ccrouter_upstream_inflight` pinned at the cap with 429s rising | Reversible via config + SIGHUP |
| A request goroutine dies mid-request while holding a host slot | The slot is released by the limiter's `defer`, so the budget is not permanently reduced | None required | `ccrouter_upstream_inflight` returns to 0 after the failure and stays there | Reversible — a leaked slot is the failure; no state to undo |

## Security / Abuse

The gauge label is the upstream host, which is operator-configured infrastructure and already visible in `docs/config.md` and the request log's `provider=` field — it discloses no credential. The 429 body is static and generic, matching the existing limiter: it carries no host name, queue depth, provider name, or client identity, so a rejected client learns only that it should retry. The host limiter introduces no new input surface — it reads the same upstream URL the provider already parses, and no request header or body field influences the host key. A client cannot choose its own host key, and therefore cannot escape a host's budget by changing a header, model, or API key: the budget is a property of the resolved upstream, not of the request.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | `upstreamHostLimits` schema, host-key derivation, lenient validation, and the factory wiring that builds one shared host limiter per host outside the per-member limiter | 1, 2, 3, 4, 5, 6, 8 | 1, 2, 3, 4, 5, 6, 7, 9 | — |
| 2 | `ccrouter_upstream_inflight{host=...}` gauge, wired to the host limiter's occupancy and present for uncapped hosts | 7 | 8 | prompt 1 (needs the limiter) |
| 3 | Docs (`docs/config.md`, `docs/config.example.yaml`, `docs/metrics.md`) rewriting the independent-caps statements and documenting the series, plus the `## Unreleased` CHANGELOG bullet | — | 10, 11, 12 | prompts 1, 2 |

AC 13 is the operator-run Post-Deploy burst — no prompt covers it; it runs on the host after release.

Rationale: prompt 1 establishes the host limiter and its config contract, which is the load-bearing change; prompt 2 adds the observable on top of it; prompt 3 is documentation that must describe the shipped shape, so it follows both.

## Do-Nothing Option

Every provider keeps its own independent cap. The router continues to have no way to express a ceiling on a *server*, so the ~32-to-48 aggregate reaching `vllm.seibert.tools` remains the operator's only protection, and it holds only for as long as nobody adds a provider, a pool member, or a time window pointing at that host. The next fleet burst that lands inside the daytime window reproduces the 2026-10-10 outage — 17 minutes of a backend serving nothing for the whole fleet and every other user of it — and the mitigating config change is the same one this spec ships.

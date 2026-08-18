---
status: verifying
approved: "2026-08-18T19:56:32Z"
generating: "2026-08-18T20:18:51Z"
prompted: "2026-08-18T20:18:51Z"
verifying: "2026-08-18T20:29:36Z"
branch: dark-factory/max-concurrent-requests
---

## Summary

- The router currently forwards every `/v1/*` request straight to the matched provider's upstream, with no concurrency bound.
- `vllm.seibert.tools` caps concurrent requests per user at 8 and answers `429 too many concurrent requests (max 8 per user)` when exceeded.
- Multiple Claude Code sessions, parallel sub-agents, and dark-factory runs all funnel through the router, so bursts regularly exceed 8 concurrent requests to the seibert vllm and the client eats 429 retry waste.
- This change adds an optional per-provider `maxConcurrentRequests` cap (absent / zero / negative = unlimited, byte-for-byte current behavior): the router throttles inside itself before the upstream throttles it, queueing excess requests.
- Queued requests wait up to a configurable `maxConcurrentWaitSeconds` (default 30) for a free slot; on timeout the router answers HTTP 429 with an Anthropic-shaped `rate_limit_error` body so Claude Code's own backoff retries cleanly.

## Problem

`vllm.seibert.tools` enforces a per-user concurrency ceiling of 8. The router (`bborbe/claude-code-router`) is the single ingress for all Claude Code traffic to that vllm — two provider entries (`seibert-vllm-default`, `seibert-dark-factory`) both point at it. When more than 8 requests are in flight concurrently, vllm rejects the overflow with `429 too many concurrent requests (max 8 per user) — retry shortly`. The Claude Code SDK then retries with backoff, which wastes wall-clock time and tokens on bursts that a tiny in-router queue would absorb. The router is the natural place to throttle: it knows the provider, it already counts request latency and status, and it can hold excess requests in a bounded queue.

## Goal

A router config can cap how many `/v1/*` requests any single provider forwards upstream at the same time. When the cap is reached, the router queues the excess; a request that frees a slot within the queue wait is forwarded normally, and a request that waits too long gets a clean, retryable HTTP 429 — so the router never lets a provider exceed its configured concurrency ceiling, and vllm's own 429 is never hit.

## Non-goals

- No shared/global semaphore across providers — caps are per-provider and independent, even when two providers share one upstream.
- No change to the auth middleware, model routing, alias resolution, or body handling.
- No new Prometheus metrics (the existing `statusClass(429)` → `4xx_rate_limited` mapping already classifies router-issued 429s).
- No retry logic inside the router — a 429 hands the retry decision to the client (Claude Code SDK backoff), matching vllm's own contract.
- No changes to the other providers' configs (minimax, zai, ollama-local, anthropic-subscription, openai-codex stay uncapped by default).
- Not addressing vllm's per-user identity question — per-provider caps are independent by design (operator decision 2026-08-18).

## Acceptance Criteria

- [ ] Config parsing: a provider block with `maxConcurrentRequests: 8` and `maxConcurrentWaitSeconds: 30` loads and validates; a provider block without either field loads identically to today. Evidence: `go test -count=1 ./pkg/` passes and `grep -c 'maxConcurrentRequests' pkg/config_test.go` returns ≥1 (new Ginkgo rows assert both load paths).
- [ ] Lenient validation: a negative `maxConcurrentRequests` is treated as unlimited and a negative `maxConcurrentWaitSeconds` as the 30s default — no fail-closed rejection, the config loads either. Evidence: `go test -count=1 ./pkg/` passes (new Ginkgo row asserts a negative value loads and behaves as its fallback).
- [ ] Capped provider: with a cap of N, at most N requests are in flight upstream simultaneously; the (N+1)th request is held until one of the N completes. Evidence: `go test -count=1 ./pkg/handler/` passes (new `ConcurrencyLimit` row asserts the queued request completes after the first releases its slot).
- [ ] Queue timeout: a request that waits longer than `maxConcurrentWaitSeconds` (default 30) receives HTTP 429 whose JSON body contains `"type":"rate_limit_error"`. Evidence: `go test -count=1 ./pkg/handler/` passes (new `ConcurrencyLimit` row asserts status 429 + body contains `rate_limit_error`).
- [ ] Uncapped passthrough: a provider with `maxConcurrentRequests` absent or 0 forwards without queueing or 429s. Evidence: `go test -count=1 ./pkg/handler/` passes (new `ConcurrencyLimit` row asserts immediate pass-through with an uncapped limiter).
- [ ] Per-provider independence: saturating one capped provider's queue does not block another capped provider. Evidence: `go test -count=1 ./pkg/handler/` passes (new `ConcurrencyLimit` row asserts two independent limiters with independent slots).
- [ ] Reload: a second `CreateRouterFromConfig` call with a changed `maxConcurrentRequests` builds a limiter with the new cap (mirrors the SIGHUP reloader path). Evidence: `go test -count=1 ./pkg/factory/` passes (new wiring row asserts the rebuilt tree enforces the new N).
- [ ] Docs: `docs/config.md` documents both fields and `docs/config.example.yaml` shows them on a provider. Evidence: `grep -c 'maxConcurrentRequests' docs/config.md docs/config.example.yaml` returns ≥1 for each file.
- [ ] CHANGELOG: `CHANGELOG.md` has a bullet under `## Unreleased` mentioning `maxConcurrentRequests`. Evidence: `sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c maxConcurrentRequests` returns ≥1.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format / lint / vet / vulncheck clean
- `make test` — full Go suite passes, including the new Ginkgo rows named in the ACs
- `grep -c 'maxConcurrentRequests' docs/config.md` → ≥1
- `grep -c 'maxConcurrentRequests' docs/config.example.yaml` → ≥1
- `sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c maxConcurrentRequests` → ≥1

### Operator-executable (runs on the host after PR merge + release + install)

- `make install`; restart the launchd service (`de.bborbe.claude-code-router`).
- In `~/.config/claude-code-router/config.yaml` add `maxConcurrentRequests: 8` (+ `maxConcurrentWaitSeconds: 30`) to `seibert-vllm-default` and `seibert-dark-factory`; `kill -HUP $(pgrep claude-code-router)`.
- Under heavy parallel Claude Code load, `/tmp/claude-code-router.log` shows router-issued `[req] ... provider=seibert-vllm-default ... status=429` lines (queued → timed out) and NO vllm `max 8 per user` 429s reaching the client.
- Local soak before the config flip: run the router with `maxConcurrentRequests: 2` on `ollama-local` + a slow prompt; fire 3 concurrent `curl /v1/messages` → the third returns 429 after the wait.

## Desired Behavior

1. A provider with `maxConcurrentRequests: N` forwards at most N `/v1/*` requests to its upstream at any instant; the slot is held for the full request duration, including streaming SSE responses, so a long stream counts as in-flight.
2. Requests beyond N wait in a FIFO queue for a free slot.
3. A queued request that acquires a slot within `maxConcurrentWaitSeconds` is forwarded normally — the client sees the upstream's response, unchanged.
4. A queued request still waiting after `maxConcurrentWaitSeconds` is answered HTTP 429 with an Anthropic-shaped error body: `{"type":"error","error":{"type":"rate_limit_error","message": ...}}` (generic message; no internal state leaked).
5. `maxConcurrentWaitSeconds` defaults to 30 when absent or ≤ 0 on a capped provider.
6. A provider with `maxConcurrentRequests` absent, 0, or negative is unlimited: no queueing, no router-issued 429, byte-for-byte current behavior.
7. Each provider's semaphore is independent — saturation on one provider neither blocks nor is blocked by another, even when they share an upstream.
8. A SIGHUP config reload applies changed `maxConcurrentRequests` / `maxConcurrentWaitSeconds` values without a process restart (the existing reloader rebuilds limiters; the factory wiring test locks this in).

## Constraints

- Config schema: `Provider` gains `maxConcurrentRequests int` (yaml `maxConcurrentRequests`, `omitempty`) and `maxConcurrentWaitSeconds int` (yaml `maxConcurrentWaitSeconds`, `omitempty`); zero-value semantics must remain current behavior.
- Validation: lenient. A negative `maxConcurrentRequests` is treated as unlimited (same as absent/0); a negative `maxConcurrentWaitSeconds` is treated as the 30s default (same as absent/0). No value fails `config.Load`.
- The limiter sits between route dispatch and upstream proxy (wrap each provider's proxy in `pkg/factory/factory.go`); the model router's body-read, alias, `[1m]`-strip, key-routing, and system-lift flow are untouched.
- The limiter is a buffered-channel semaphore in a new handler `pkg/handler/concurrency-limiter.go`, matching the repo's existing small-handler style (e.g. `loopback.go`, `auth-middleware.go`).
- Queue timeout produces HTTP 429 only — never a 5xx (a 429 is client-retryable; a 5xx is not).
- No new Prometheus metrics: the limiter wraps the provider proxy inside the routed path, so a limiter-issued 429 flows through the existing `ObserveRequest` call and lands in `status_class="4xx_rate_limited"` plus the `[req] ... status=429` log line.
- Tests follow the repo's Ginkgo convention (`handler_suite_test.go`, `config_test.go`) and must not depend on real wall-clock 30s waits (use small explicit waits in tests).
- No AI attribution, no new dependencies (Go standard library suffices for a channel semaphore + timer).

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Upstream slow/stuck with cap reached | Queued requests accumulate; those past the wait get 429 (client retries with backoff) | Upstream recovers; queue drains; no operator action |
| Operator sets a negative value | Treated as its fallback (requests → unlimited; wait → 30s default); router starts normally | Correct the value if the fallback was unintended |
| Client disconnects while queued | No slot is held (slot acquired only at dispatch); the goroutine waits out the timer, then the 429 write to the dead connection fails harmlessly | None needed — connection teardown is the recovery |
| Two capped providers share an upstream (seibert case) | Independent queues: each caps at its own N; combined concurrency can reach 2N | Accepted per design (per-provider caps, operator decision); revisit only if vllm proves a shared per-instance ceiling |
| Flood of requests at a capped provider | Router never exceeds the cap; excess get 429 after the wait; memory bounded by request-rate × wait (one goroutine + timer per queued request) | 429s shed load; client backoff spaces retries |

## Security / Abuse

- The 429 response body must not reveal internal state (queue depth, upstream URL, provider name) — a generic `rate_limit_error` message only.
- The limiter sits inside the existing auth + routing path; it neither bypasses nor extends auth.
- An unauthenticated attacker flooding a capped provider cannot make the router exceed the cap or exhaust memory beyond the bounded queue.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Config fields + validation + `concurrency-limiter.go` + factory wiring + Ginkgo tests (incl. reload/wiring row) | 1–8 | 1–7 | — |
| 2 | `docs/config.md` + `docs/config.example.yaml` + CHANGELOG `## Unreleased` bullet | 5 (default 30 documented) | 8, 9 | prompt 1 (documents fields prompt 1 adds) |

## Do-Nothing Option

The router keeps forwarding bursts at `vllm.seibert.tools`; vllm keeps 429ing at 8 concurrent; Claude Code SDK retries with backoff. Cost: wasted wall-clock and tokens on every parallel burst (multiple sessions, sub-agents, dark-factory), and intermittent wedge conditions when several long streams overlap. The fix is one small Go change + a two-line config flip per seibert provider; deferring it keeps paying the burst tax indefinitely.

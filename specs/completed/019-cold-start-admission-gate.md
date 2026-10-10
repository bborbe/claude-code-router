---
status: completed
approved: "2026-10-10T13:12:54Z"
generating: "2026-10-10T13:54:56Z"
prompted: "2026-10-10T14:38:29Z"
verifying: "2026-10-10T15:42:47Z"
completed: "2026-10-10T15:42:55Z"
branch: dark-factory/cold-start-admission-gate
---

## Summary

- A burst of new sessions each carries a large uncached context; the resulting cold-prefill spike can push a model backend past its throughput ceiling and take it down. The router forwards every first request immediately and has no server-side bound on how much cold prefill it lets start at once.
- This change adds an optional per-provider cold-start admission gate with three knobs: a ceiling on in-flight cold-prefill tokens, the window that decides whether a session is new, and a cap on how many new sessions may start per minute.
- A request counts as cold when its `x-session-id` has not been seen inside the configured window. Cold requests are admitted while they fit the budget, and are otherwise held in a bounded queue and then refused with a 429 — never a 5xx, never an unbounded wait.
- The budget is released as soon as prefill is observed to have finished — the first streamed content delta — not on a timer and not at message start, so a long stream does not hold budget it no longer needs.
- Requests on a session id already seen inside the window are never held by this gate, and the gate is off unless an operator sets a knob: an unconfigured router behaves byte-for-byte as it does today.

## Problem

Observed 2026-10-10: 22 Claude sessions started within 2.8 minutes, each first request carrying a large uncached context. The uncached-input spike landed on a model backend already running at its throughput ceiling, and the backend went to zero. The router already reads `x-session-id` for upstream pool pinning, and already has per-provider concurrency caps and an adaptive 429 delay gate — but neither bounds the *start rate* or the *cold-prefill volume* of new sessions. Concurrency caps bound how many requests are in flight, not how much uncached work they represent; the 429 delay gate reacts to a provider that is already refusing work, which is too late for a backend that has already fallen over. Nothing in the router's server-side path stands between a burst of new sessions and the backend. Client-side rate limiting exists in the supervisor, but it only covers the clients the supervisor launches — the router is the last place that can bound every caller.

## Goal

The router becomes the admission point for new-session cold prefill. When an operator enables the gate on a provider, a burst of new sessions is spread out and bounded instead of arriving at once: the router admits cold requests only while their estimated prefill fits the configured in-flight budget, starts at most the configured number of new sessions per minute, and holds the excess in a bounded queue before refusing with a 429 that clients can back off from. Prefill budget is released the moment the backend starts streaming content, so the gate bounds *concurrent cold work* rather than total request duration. A request on a session the router has already seen inside the window passes through untouched, so established sessions see no added latency. With the knobs unset the router's request path is unchanged — the gate is opt-in per provider, because the safe budget and rate differ per upstream and the operator enables it on the providers that back the fleet. The gate also emits the cold time-to-first-token histogram that the sibling task *claude-code-router Backs Off on Rising First-Token Latency and Breaks the Circuit on Upstream Failures* needs as its input signal.

## Non-goals

- No change to the supervisor or any client-side spawn behaviour — this is the server-side backstop only; the client-side limit is a separate task.
- No 5xx-based or latency-based budget adaptation, and no circuit breaker — reacting to an unhealthy backend is the sibling task *claude-code-router Backs Off on Rising First-Token Latency and Breaks the Circuit on Upstream Failures*, which consumes this spec's time-to-first-token histogram.
- No `llm-proxy` changes.
- No per-model or per-session budget — the budget and the rate are keyed per provider, like the existing concurrency and throttle gates.
- No change to the existing concurrency caps, the 429 delay gate, or the session-pinning behaviour of `x-session-id`.
- No new Prometheus `status_class` value — the existing `4xx_rate_limited` classification covers the refusal; new series are additive.
- No persistent or spill-to-disk queue, and no unbounded wait — the queue is bounded and overflow is refused.
- No operator-tunable queue capacity, max wait, rate burst, or cost-estimate divisor — those are fixed internal constants; only the budget, the window, and the rate are knobs.
- No new scenario — the gate is reachable end-to-end by Ginkgo integration tests through the real dispatch path with an injected clock, so unit and integration tests in the implementation prompts are sufficient.

## Acceptance Criteria

- [ ] Config parsing: a provider block carrying all three knobs loads and validates; a provider block carrying none of them loads identically to today. Evidence: `go test -count=1 ./pkg/` passes and `grep -c 'coldPrefillBudgetTokens' pkg/config_test.go` returns ≥1 (new Ginkgo rows assert both the populated and the absent path).
- [ ] Lenient validation: a negative budget or negative rate is treated as disabled, and a negative window falls back to its 600-second default — no value fails `config.Load`. Evidence: `go test -count=1 ./pkg/` passes (a new Ginkgo row asserts each negative value loads and behaves as its documented fallback).
- [ ] Disabled is a byte-for-byte no-op: with the budget and the rate each absent, 0, or negative, the gate constructor returns the inner handler unchanged — no queueing, no added latency, no 429, no new counter. Evidence: `go test -count=1 ./pkg/handler/` passes (a new Ginkgo row asserts the constructed handler is `BeIdenticalTo` the inner handler, mirroring the existing `ConcurrencyLimiter` zero-cap row).
- [ ] Cold classification: a request whose session id was last seen inside the configured window is warm and is never held; a request whose session id is absent, unknown, or last seen outside the window is cold. Evidence: `go test -count=1 ./pkg/handler/` passes (new Ginkgo rows with an injected clock: a warm request forwards immediately while cold requests are held; a session id seen just before the window boundary classifies cold again; a request with no session id classifies cold).
- [ ] Budget admission and cost estimate: a cold request is admitted immediately when nothing is cold in flight; otherwise it is admitted only while in-flight cold tokens plus its estimate stay within the budget, and it waits when it would exceed it. The estimate is the body size divided by 3.5, computed as integer arithmetic with no floating point. Evidence: `go test -count=1 ./pkg/handler/` passes (a new Ginkgo row asserts the upstream is not invoked during a `Consistently` window and is invoked via `Eventually` once budget frees; a second row asserts a lone cold request is admitted even with a budget smaller than its estimate; a third drives bodies of known size and asserts the gate's in-flight token count — read through an accessor mirroring the concurrency limiter's `InFlight`, not the Prometheus gauge, which prompt 4 owns — equals `bytes * 2 / 7` for each).
- [ ] Budget release on first content delta: the reservation is released when the response stream emits its first `content_block_delta`, and not on `message_start` and not on a timer; it is also released when the response handler returns (stream end, upstream error) and when the client disconnects while held. Evidence: `go test -count=1 ./pkg/handler/` passes (new Ginkgo rows assert the gate's in-flight token count accessor drops to zero after a streamed `content_block_delta` while the handler is still writing; a `message_start`-only stream holds the reservation; an errored response and a client disconnect each release it).
- [ ] New-session rate: a provider admits at most the configured number of newly-seen session ids per minute, with two admitted immediately as the fixed burst; the excess waits in the same queue, and a session id is counted against the rate once, when its first request is admitted. Evidence: `go test -count=1 ./pkg/handler/` passes (a new Ginkgo row with an injected clock asserts the first two distinct ids are admitted immediately and the third distinct id is not admitted until the refill interval elapses; a second row asserts two concurrent first requests for one session id consume one rate unit).
- [ ] Bounded queue and refusal: at most 32 cold requests wait; a request that cannot be admitted within 30 seconds, or that arrives when the queue is full, is answered HTTP 429 whose body is the exact existing Anthropic-shaped `rate_limit_error` envelope, with an integer `Retry-After` header in the range 1–60. Evidence: `go test -count=1 ./pkg/handler/` passes (new Ginkgo rows assert status 429, body equality with the existing constant, and a `Retry-After` value inside the range for both the timeout and the queue-full path).
- [ ] Never 5xx and never dropped: no admission outcome produces a 5xx, and every request either reaches the upstream or receives the 429 refusal. Evidence: `go test -count=1 ./pkg/handler/` passes (a new Ginkgo row asserts that across a burst exceeding both budget and rate, every response is either a forwarded upstream response or a 429 — no 5xx, no hang).
- [ ] Per-provider independence: exhausting one provider's budget or rate neither delays nor blocks another provider, including two providers sharing one upstream. Evidence: `go test -count=1 ./pkg/handler/` passes (a new Ginkgo row asserts provider B forwards immediately while provider A is held).
- [ ] Metrics additive: delayed and refused cold requests increment `ccrouter_cold_admission_delayed_total{provider}` and `ccrouter_cold_admission_refused_total{provider}` by exactly 1 each, `ccrouter_cold_tokens_in_flight{provider}` is a gauge that reads non-zero while a cold request holds its reservation and returns to zero after release, and `ccrouter_cold_ttft_seconds{provider}` observes a cold request's time from dispatch to its first `content_block_delta`. Evidence: `go test -count=1 ./pkg/handler/` passes (new metrics Ginkgo rows assert each delta on an isolated registry and assert the gauge is non-zero mid-flight and zero after; negative evidence: the refusal still lands in the unchanged `4xx_rate_limited` class with no new `status_class` value).
- [ ] Logging, no session ids: each delayed and each refused cold request emits one INFO line with the `[coldgate]` prefix carrying the provider, the decision, and the reason, and no emitted log line contains a raw `x-session-id` value. Evidence: `go test -count=1 ./pkg/handler/` passes (a new Ginkgo row asserts on the captured log writer output that a `[coldgate] provider=… decision=delayed reason=budget` line and a `[coldgate] provider=… decision=refused reason=…` line each appear for the corresponding request, and that a distinctive session id driven through the gate appears nowhere in the captured output).
- [ ] Reload / wiring: a second factory construction with changed cold knobs rebuilds the gate enforcing the new values. Evidence: `go test -count=1 ./pkg/factory/` passes (a new wiring row asserts the rebuilt tree admits under the new budget where the old one refused).
- [ ] Docs: `docs/config.md` documents all three knobs, each with its meaning, its default and its zero-value semantics, under a section that links the existing `## 429 delay gate` section; `docs/config.example.yaml` shows all three commented on a provider; `docs/metrics.md` documents all four new series; `README.md` mentions the new configuration. Evidence: `grep -c 'coldPrefillBudgetTokens' docs/config.md docs/config.example.yaml README.md` returns ≥1 for each; each of the three knob names (`coldPrefillBudgetTokens`, `coldSessionWindowSeconds`, `newSessionRatePerMinute`) returns ≥1 from `grep -c` against `docs/config.md` AND against `docs/config.example.yaml` (all three documented in both files, not just one in one file); each of the four series names (`ccrouter_cold_admission_delayed_total`, `ccrouter_cold_admission_refused_total`, `ccrouter_cold_tokens_in_flight`, `ccrouter_cold_ttft_seconds`) returns ≥1 from `grep -c` against `docs/metrics.md`; and `sed -n '/coldPrefillBudgetTokens/,+8p' docs/config.md | grep -ci 'default'` returns ≥1 (the section states the default and the zero-value semantics, rather than only naming the token).
- [ ] CHANGELOG: `CHANGELOG.md` has a bullet under `## Unreleased` naming `coldPrefillBudgetTokens`. Evidence: `sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c coldPrefillBudgetTokens` returns ≥1.
- [ ] **Post-Deploy (Rung-2):** the nuke dev router runs the new build and its config carries the knobs — evidence: `kubectlnukedev -n dev logs deploy/claude-code-router-dev --since=15m | grep -c 'decision=refused'` returns ≥1 after a burst against the dev router, and `kubectlnukedev -n dev logs deploy/claude-code-router-dev --since=15m | grep -cE '\[req\].*status=5'` returns 0.
  - `deploy_check:` `kubectlnukedev -n dev get deploy/claude-code-router-dev -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0)`
- [ ] **Post-Deploy (Rung-3):** with the budget set to 65536 and the rate set to 4 per minute on a provider, ten new sessions fired concurrently within ten seconds produce at least six refusals, zero 5xx, and at least one request on an already-seen session id forwarded with no added delay. Evidence: `kubectlnukeprod -n prod logs deploy/claude-code-router-prod --since=15m | grep -c 'decision=refused'` returns ≥6; `kubectlnukeprod -n prod logs deploy/claude-code-router-prod --since=15m | grep -cE '\[req\].*status=5'` returns 0; the warm request's log line carries no cold-gate delay.
  - `deploy_check:` `kubectlnukeprod -n prod get deploy/claude-code-router-prod -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0)`

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format / lint / vet / vulncheck clean
- `make test` — full Go suite passes, including the new Ginkgo rows named in the Acceptance Criteria
- `grep -c 'coldPrefillBudgetTokens' docs/config.md` → ≥1
- `grep -c 'coldSessionWindowSeconds' docs/config.md` → ≥1
- `grep -c 'newSessionRatePerMinute' docs/config.md` → ≥1
- `sed -n '/coldPrefillBudgetTokens/,+8p' docs/config.md | grep -ci 'default'` → ≥1
- `grep -c 'coldPrefillBudgetTokens' docs/config.example.yaml` → ≥1
- `grep -c 'coldSessionWindowSeconds' docs/config.example.yaml` → ≥1
- `grep -c 'newSessionRatePerMinute' docs/config.example.yaml` → ≥1
- `grep -c 'coldPrefillBudgetTokens' README.md` → ≥1
- `grep -c 'ccrouter_cold_admission_delayed_total' docs/metrics.md` → ≥1
- `grep -c 'ccrouter_cold_admission_refused_total' docs/metrics.md` → ≥1
- `grep -c 'ccrouter_cold_tokens_in_flight' docs/metrics.md` → ≥1
- `grep -c 'ccrouter_cold_ttft_seconds' docs/metrics.md` → ≥1
- `sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c coldPrefillBudgetTokens` → ≥1

### Operator-executable (runs on the host after PR merge + release + install)

- `make install` — the only build path that injects the version via ldflags; `make build` and `go install @latest` leave `version=dev` and the deploy gate will correctly refuse.
- Restart the launchd service `de.bborbe.claude-code-router`; confirm `grep -o 'version=[^ ]*' /tmp/claude-code-router.log | tail -1` prints `version=` followed by the most recent release tag.
- In `~/.config/claude-code-router/config.yaml` set `coldPrefillBudgetTokens: 65536`, `coldSessionWindowSeconds: 600` and `newSessionRatePerMinute: 4` on a test provider; `kill -HUP $(pgrep claude-code-router)`.
- Fire ten new sessions concurrently within ten seconds against that provider and confirm: at least six receive HTTP 429 with a `rate_limit_error` body and a `Retry-After` header, `/tmp/claude-code-router.log` carries ≥6 `decision=refused` lines and ≥2 `decision=delayed` lines, `ccrouter_cold_tokens_in_flight{provider}` is non-zero during the burst and never above 65536 once any cold request is already in flight (a lone request larger than the whole budget is admitted alone and may exceed it by design), and no 5xx appears.
- Fire a request on an already-seen session id during the burst and confirm it is forwarded with no added latency and no cold-gate log line.
- Zero=disabled regression check: set `coldPrefillBudgetTokens: 0` and `newSessionRatePerMinute: 0` on a benign provider and confirm traffic flows with no `[coldgate]` lines and no added latency.
- Deploy to nuke dev, then to prod, as two manual operator steps: in `bborbe/nuke` `claude-code-router/Makefile` bump the pinned `VERSION` to the new release tag, run `make mirror` to copy the image into the nuke registry, then `make apply` for that stage. The router re-reads its config only on SIGHUP, so `rollout restart` the deployment afterwards and confirm the new version line in the pod log.

## Desired Behavior

1. A request is classified cold or warm from its `x-session-id` alone: warm when that id was last seen inside the configured session window, cold when the id is absent, unknown, or last seen outside the window. The map records the arrival time of the most recent request carrying each id, so the window slides with each request rather than anchoring to first sight. Warm requests bypass the gate entirely — no queueing, no added latency, no counter. A request with no session id is always cold, because the router cannot tell whether it is a new session.
2. A cold request carries a prefill estimate of its body size divided by 3.5, computed with integer arithmetic. It is admitted immediately when the provider has nothing cold in flight; otherwise it is admitted only while the provider's in-flight cold tokens plus its estimate stay within the configured budget. If it does not fit, it waits.
3. Independently of the budget, a provider admits at most the configured number of newly-seen session ids per minute, with two admitted immediately as the fixed burst. A first request for a session id beyond that allowance waits in the same queue. A session id is counted against the rate once, when its first request is admitted, so concurrent first requests for one session consume one rate unit.
4. Waiting cold requests occupy a bounded queue of 32. A request that cannot be admitted within 30 seconds, or that arrives when the queue is full, is answered HTTP 429 with the existing Anthropic-shaped `rate_limit_error` body and an integer `Retry-After` header between 1 and 60 seconds. No admission outcome produces a 5xx, and no request is dropped without an answer.
5. A cold request's reserved budget is released when the response stream emits its first `content_block_delta` — the point at which prefill has finished. It is not released on `message_start` and not on a timer. It is also released when the response handler returns for any reason (stream end, upstream error) and when the client disconnects while the request is held, so a dead or errored request never leaks budget.
6. Both gates are per provider and independent: exhausting one provider's budget or rate neither delays nor blocks another provider, including two providers that share one upstream. Both are off by default — with the budget and the rate absent, zero, or negative the gate is a no-op and the request path is byte-for-byte today's behaviour.
7. The gate emits four additive Prometheus series: `ccrouter_cold_admission_delayed_total{provider}` and `ccrouter_cold_admission_refused_total{provider}` counters, a `ccrouter_cold_tokens_in_flight{provider}` gauge that rises while cold requests hold their reservation and returns to zero as budget is released, and a `ccrouter_cold_ttft_seconds{provider}` histogram of cold time from dispatch to the first `content_block_delta`. The existing `status_class` enum is unchanged and a refusal still records through `4xx_rate_limited`.
8. Each delayed and each refused cold request emits one INFO log line prefixed `[coldgate]` and carrying the provider, the decision, and the reason — `[coldgate] provider=<name> decision=delayed reason=<budget|rate>` or `[coldgate] provider=<name> decision=refused reason=<budget|rate|queue_full|timeout>`. Raw session id values are never written to any log line. Config changes are applied by the existing SIGHUP reload path, which rebuilds the per-provider tree; admission state is in-memory, so a reload resets the window and the in-flight budget and the provider re-accumulates them.

## Constraints

- Config schema: the provider block gains `coldPrefillBudgetTokens int` (yaml `coldPrefillBudgetTokens,omitempty`), `coldSessionWindowSeconds int` (yaml `coldSessionWindowSeconds,omitempty`) and `newSessionRatePerMinute int` (yaml `newSessionRatePerMinute,omitempty`). Zero-value semantics must remain today's behaviour. The knobs are read at provider level only and are NOT copied onto `upstreams:` pool members — a cold knob on a member is silently ignored, mirroring the throttle knobs documented under `docs/config.md ## 429 delay gate`.
- Fixed internal constants, documented as defaults and not exposed as knobs: the rate burst is 2, the queue capacity is 32, the maximum wait is 30 seconds, the estimate divisor is 3.5, and the `Retry-After` clamp is 1–60 seconds. The session window IS a knob (`coldSessionWindowSeconds`); 600 seconds is its fallback default when the field is absent or negative, not a fixed constant.
- Keying and placement: the gate is per provider, constructed immediately after the upstream pool handler and before the throttle gate in `pkg/factory/factory.go`, so the existing 429 delay gate is outermost and cold admission is the last gate before the pool. The model router's body read, alias resolution, `[1m]` strip, key routing and system-lift flow are untouched.
- The session id is read from the request context. The session middleware strips the `X-Session-Id` header before dispatch and carries the value on the context, so the header is not available at the gate and must not be re-read.
- Body size is read from the request's `ContentLength`, which the model router has already set to the exact body length by the time a provider handler runs. The gate must not read or buffer the request body.
- The refusal reuses the existing `limiter429Body` constant (`pkg/handler/concurrency-limiter.go`). The body is the same static generic message — it must not carry queue depth, provider name, upstream URL, or any session-identifying value.
- The response is observed through a wrapper that preserves the existing SSE-safe flushing behaviour. Streaming must not be broken by the observation, and the response bytes must pass through unmodified.
- Validation is lenient: a negative budget or rate is treated as disabled; a negative window falls back to 600 seconds. No value fails `config.Load`.
- Gate state is mutated from concurrent request goroutines — the seen-session map, the in-flight token count, the rate token bucket and the queue are shared and must be safe under concurrency, and the tests must exercise concurrent requests.
- Time is taken from the injected clock (`github.com/bborbe/time`), never `time.Now()`, so the window and the rate refill are testable without wall-clock sleeps. Tests must not sleep through a real window.
- Queue waits honour context cancellation, so a disconnected client frees its queue slot immediately.
- The seen-session map is pruned to the window so it cannot grow without bound.
- No new dependencies — the Go standard library plus the existing `bborbe/*` libraries suffice.
- No AI attribution.

## Failure Modes

| Trigger | Expected behavior | Recovery | Concurrency |
|---|---|---|---|
| A burst of new sessions exceeds the budget | Cold requests beyond the budget wait in the bounded queue; the ones that cannot be admitted within 30s get 429 with `Retry-After`; established sessions keep flowing | Queue drains as streams release budget on their first content delta; operator confirms the drain with `grep -c 'decision=delayed' /tmp/claude-code-router.log` no longer increasing | Two requests racing for the last of the budget: exactly one is admitted, the other waits — the in-flight gauge never exceeds the configured budget once any cold request is already in flight (a lone request larger than the whole budget is admitted alone and may exceed it by design) |
| A burst of new sessions exceeds the rate | Newly-seen session ids beyond the per-minute allowance wait in the same queue, then 429 | The bucket refills; clients back off on `Retry-After`; operator confirms with `ccrouter_cold_admission_refused_total{provider}` flattening | Two concurrent first requests for one session id consume one rate unit, not two |
| Upstream never emits `content_block_delta` (long prefill, or an error before streaming) | The reservation is held while the request is genuinely prefilling and released when the handler returns | The response ending releases the budget; operator confirms `ccrouter_cold_tokens_in_flight{provider}` returns to 0 | — |
| Client disconnects while queued or while holding budget | The request is never forwarded, its queue slot frees immediately, and its reservation is released | None needed; operator confirms the gauge returns to 0 without the upstream being called | A disconnect racing an admission must not admit the request after the client is gone |
| Client sends a fresh random `x-session-id` per request | Every request classifies cold; the bounded queue and the rate cap bound the damage, and warm sessions are unaffected | Operator sets both `coldPrefillBudgetTokens: 0` and `newSessionRatePerMinute: 0` on that provider and confirms no `[coldgate]` lines appear in `/tmp/claude-code-router.log` | — |
| Operator sets a negative knob | Budget or rate negative → that check disabled while the other still applies; window negative → 600s default with both checks still active; the router starts normally | Operator edits the value and SIGHUPs. Window fallback (budget and rate positive): two requests on one session id 599 seconds apart, second is warm with no `[coldgate]` line — the window fell back to 600s rather than disabling the gate. Disabled check: no `[coldgate]` line carrying that check's reason appears, while a line for the other check still does | — |
| Two providers share one upstream | Independent gates: each bounds its own cold work; combined cold concurrency to the shared upstream can reach the sum of both budgets, which is accepted by design | None — independence matches the existing concurrency caps | — |
| Router restart or SIGHUP while requests are held | Admission state is in-memory; the rebuild resets the window, the budget and the bucket. In-flight requests from the old tree finish on the old tree | The provider re-accumulates state from the next requests; operator confirms the first post-reload burst is admitted fresh | A rebuild racing in-flight requests must not double-count or leak budget |
| Clock jumps or is injected | Window comparisons and rate refill shift in time; no arithmetic overflow because the budget and the estimate are bounded by the body-size cap | None — the gate uses the router's injected clock, monotonic in practice | — |

## Security / Abuse

- The session id is client-controlled and is used only as a grouping key for the window. It never grants access, is never used for authentication, and is never written to a log line in any form.
- The cost estimate is derived from the request body size, which is client-controlled but bounded by the existing `MaxRequestBodyBytes` cap, so a single request cannot reserve an unbounded amount of budget. Integer arithmetic on a bounded value cannot overflow.
- A client can force every one of its requests to classify cold by sending a fresh random session id each time. The consequence is bounded and fail-safe: the requests join the bounded queue and are refused with 429 once the budget or the rate is exhausted. The gate never turns the provider off, never answers 5xx, and never blocks a warm session — so the worst case is that the abusing client starves itself, not that it starves established sessions or the upstream.
- Bounded resources throughout: the queue is capped at 32 waiters, each wait is capped at 30 seconds, the budget is capped by configuration, and the seen-session map is pruned to the window so it cannot grow without bound.
- The refusal body is the existing static constant and must not leak internal state. Upstream response bodies pass through unchanged.
- A client that disconnects while waiting must never hold a queue slot or a budget reservation — the existing disconnect discipline of the concurrency limiter is the model.
- Config values are validated leniently and no new user input parsing, path handling, or credential handling is introduced.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Config fields + lenient validation + the disabled no-op constructor | — | 1, 2, 3 | — |
| 2 | Gate core: cold/warm classification, cost estimate, budget admission, release detection (first content delta, handler return, disconnect) + Ginkgo tests | 1, 2, 5, 6 | 4, 5, 6 | prompt 1 |
| 3 | Rate bucket + bounded queue + 429 refusal with `Retry-After` + concurrency-safe shared state + Ginkgo tests | 3, 4, 6 | 7, 8, 9, 10 | prompt 2 (uses the same queue and state) |
| 4 | Metrics (four series) + INFO log lines with no session ids + factory wiring and SIGHUP rebuild + Ginkgo tests | 7, 8 | 11, 12, 13 | prompts 2, 3 |
| 5 | `docs/config.md` + `docs/config.example.yaml` + `docs/metrics.md` + `README.md` + CHANGELOG `## Unreleased` bullet | 7 | 14, 15 | prompt 4 (documents the fields and series that shipped) |

Rationale: prompts 1 and 2 establish the config contract and the admission behaviour with its tests; prompt 3 adds the second admission check onto the queue prompt 2 built; prompt 4 makes the gate observable and wires it into the request tree; prompt 5 documents exactly what shipped. The Post-Deploy ACs are operator-executable and run from the spec's verification ladder after merge, not inside a prompt.

## Do-Nothing Option

The router keeps admitting every new session immediately, so a burst of cold prefills arrives at the model backend as one spike with nothing on the server side to spread it out. That is the condition that took the backend to zero on 2026-10-10, and the only mitigation in place is client-side limiting in the supervisor, which does not cover callers the supervisor does not launch. Cost: each recurrence is a full backend outage for every session using that provider, including the ones already running. The fix is one bounded handler plus three per-provider config values, off by default — the cost of doing it is a handler and a test suite; the cost of not doing it is paid again the next time a fleet spawns in a burst.

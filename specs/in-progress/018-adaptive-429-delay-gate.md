---
status: verifying
approved: "2026-08-27T18:31:43Z"
generating: "2026-08-27T18:32:19Z"
prompted: "2026-08-27T18:51:40Z"
verifying: "2026-08-27T19:08:41Z"
branch: dark-factory/adaptive-429-delay-gate
---

## Summary

- The router forwards every `/v1/*` request straight to the matched provider with no rate feedback; a provider under sustained 429 pressure keeps being re-stormed because the router forwards at full rate while clients retry immediately.
- This change adds an optional per-provider adaptive delay gate: when a windowed count of 429 responses from a provider exceeds a threshold, subsequent requests to that provider are briefly delayed before forwarding, giving the upstream a breathing window to recover.
- Classic AIMD: the pacing delay multiplies (×2) on each observed 429 and decays gradually (÷2 per clean window), bounded by a configurable maximum — never unbounded.
- The 429'd request is never retried by the router — only subsequent requests are paced. Threshold zero = disabled: the gate never engages and the request path is byte-for-byte today's behavior (strict superset).
- Two new lenient per-provider knobs (`throttle429Threshold`, `throttleMaxDelaySeconds`) plus an additive `ccrouter_throttled_total{provider}` counter; `docs/config.md`, `docs/config.example.yaml`, `docs/metrics.md`, and the CHANGELOG ship with it.

## Problem

The router is a faithful conduit: every `/v1/*` call is forwarded to the configured provider with no feedback from the provider's rate state. Observed live 2026-08-26: z.ai `glm-5.3-flash[1m]` (provider `zai/0`) entered a sustained 429 wall — one 200 per ~10s amid a wall of 429s. The router kept forwarding at full request rate while the client retried immediately, extending the storm against an upstream already refusing work. The concurrency limiter (spec 011) caps how many requests are in flight simultaneously but is not a rate gate: it does not react to observed upstream 429s, and the time-window gate (spec 014/017) only handles scheduled eligibility. Neither paces traffic toward a provider that is actively rate-limiting. The router is the natural place for the gate: it knows the provider, it already observes response status, and it can delay before forwarding.

## Goal

The router becomes the pacemaker per provider. When a windowed count of 429 responses from a provider exceeds a configured threshold, upcoming requests to that provider are delayed briefly before forwarding (AIMD: the delay grows multiplicatively on further 429s and decays on clean periods), so the provider gets a breathing window to recover instead of being re-stormed. The 429'd request is never re-sent by the router — only subsequent requests are paced — and a provider with the feature disabled (threshold zero) sees byte-for-byte today's behavior. After the change, any operator can turn the gate on for a rate-limited provider with two YAML knobs and observe pacing in the router log.

## Non-goals

- No router-side retry of the 429'd request — the response passes through unchanged, the router records the status only to adjust future pacing.
- No per-model throttling — the gate is keyed per provider, never per model.
- No cross-provider / shared throttle state — throttle state is strictly per provider; independence is a test case.
- No unbounded delay and no persistent / spill-to-disk queue — the pacing queue is bounded; overflow reuses the existing Anthropic-shaped `rate_limit_error` body (`limiter429Body`).
- No hard circuit-breaker / provider disable on repeated 429s — the gate is soft pacing, never a hard-off state; requests are always eventually forwarded (or answered 429 on overflow), never dropped silently.
- No touching the existing `4xx_rate_limited` classification (`pkg/handler/metrics.go:189`) — the new `throttled` counter is additive; no new `status_class` value.
- Do NOT add tunable knobs for the 60s observation window, the ×2 / ÷2 AIMD multipliers, the 1s initial delay, or the recovery floor — fixed internal constants (documented defaults); if a future consumer demands variation, that is a separate spec.
- No end-to-end scenario — the gate is reachable by Ginkgo integration tests through the real dispatch path (in-process handlers + injected clock), so unit + integration tests in the implementation prompt suffice.

## Acceptance Criteria

- [ ] Config parsing: a provider block with `throttle429Threshold: 3` and `throttleMaxDelaySeconds: 30` loads and validates; a provider block without either field loads identically to today. Evidence: `go test -count=1 ./pkg/` passes and `grep -c 'throttle429Threshold' pkg/config_test.go` returns ≥1 (new Ginkgo rows assert both load paths).
- [ ] Lenient validation: a negative `throttle429Threshold` is treated as disabled and a negative `throttleMaxDelaySeconds` as the 30s default — no fail-closed rejection, the config loads either. Evidence: `go test -count=1 ./pkg/` passes (new Ginkgo row asserts a negative value loads and behaves as its fallback).
- [ ] Disabled is a byte-for-byte no-op: with `throttle429Threshold` absent, 0, or negative, the gate constructor returns the inner handler unchanged — no delay, no 429, no `throttled` counter. Evidence: `go test -count=1 ./pkg/handler/` passes (new Ginkgo row asserts the constructed gate is `BeIdenticalTo` the inner handler, mirroring the `ConcurrencyLimiter` zero-cap row).
- [ ] Throttle trigger + delay applied: after the windowed 429 count reaches the threshold, the next request to that provider is not forwarded immediately — it waits the pacing delay. Evidence: `go test -count=1 ./pkg/handler/` passes (new Ginkgo row with a small explicit max delay asserts the upstream handler is NOT invoked during a `Consistently` window and IS invoked after the delay via `Eventually`).
- [ ] No router-side retry: a request whose upstream responds 429 is passed through unchanged to the client and the upstream sees exactly one invocation. Evidence: `go test -count=1 ./pkg/handler/` passes (new Ginkgo row asserts the upstream call count equals 1, the client receives the 429, and a `Consistently` window shows no second upstream invocation).
- [ ] AIMD increase, bounded: while throttled, each observed 429 doubles the pacing delay and the delay never exceeds `throttleMaxDelaySeconds`. Evidence: `go test -count=1 ./pkg/handler/` passes (new Ginkgo row, injected clock + delay accessor, asserts the delay doubles on a 429 and is capped at the max — no wall-clock sleep through the growing delay).
- [ ] Recovery: a clean window (no 429) halves the pacing delay; when the delay decays below the recovery floor the provider exits throttle and subsequent requests forward without delay. Evidence: `go test -count=1 ./pkg/handler/` passes (new Ginkgo row, injected clock, asserts delayed → clean window → delay halved → below floor → immediate forward).
- [ ] Overflow 429: when the bounded pacing queue is saturated, or a waiting request exceeds the wait bound, the router answers HTTP 429 whose JSON body equals the exact Anthropic-shaped `rate_limit_error` body (`limiter429Body`, `concurrency-limiter.go:18`). Evidence: `go test -count=1 ./pkg/handler/` passes (new Ginkgo row asserts status 429 and body equals the `limiter429Body` string — the security check that no internal state leaks).
- [ ] Per-provider independence: throttling provider A does not delay or block provider B. Evidence: `go test -count=1 ./pkg/handler/` passes (new Ginkgo row asserts B's request forwards immediately while A's is held).
- [ ] Metric additive: a paced request increments `ccrouter_throttled_total{provider=<name>}` by exactly 1 on an isolated registry, and a 429 response to the client still records through the unchanged `status_class="4xx_rate_limited"` path (no new status-class value). Evidence: `go test -count=1 ./pkg/handler/` passes (new metrics Ginkgo row asserts the counter delta equals 1; negative evidence: the 429 still lands in `4xx_rate_limited`).
- [ ] Reload / wiring: a second `CreateRouterFromConfig` call with changed throttle knobs rebuilds the gate enforcing the new values (mirrors the SIGHUP reloader path). Evidence: `go test -count=1 ./pkg/factory/` passes (new wiring row, real dispatch boundary, asserts the rebuilt tree paces under the new threshold/max-delay).
- [ ] Docs: `docs/config.md` documents both knobs, `docs/config.example.yaml` shows them on a provider, and `docs/metrics.md` documents the new counter. Evidence: `grep -c 'throttle429Threshold' docs/config.md docs/config.example.yaml` returns ≥1 for each file and `grep -c 'ccrouter_throttled_total' docs/metrics.md` returns ≥1.
- [ ] CHANGELOG: `CHANGELOG.md` has a bullet under `## Unreleased` mentioning `throttle429Threshold`. The current CHANGELOG has no `## Unreleased` header (last release v0.44.5 folded it) — the implementer must create the header AND the bullet. Evidence: `sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c throttle429Threshold` returns ≥1.

## Verification

## Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format / lint / vet / vulncheck clean
- `make test` — full Go suite passes, including the new Ginkgo rows named in the ACs
- `grep -c 'throttle429Threshold' docs/config.md` → ≥1
- `grep -c 'throttle429Threshold' docs/config.example.yaml` → ≥1
- `grep -c 'ccrouter_throttled_total' docs/metrics.md` → ≥1
- `sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c throttle429Threshold` → ≥1

## Operator-executable (runs on the host after PR merge + release + install)

- `make install`; restart the launchd service (`de.bborbe.claude-code-router`).
- In `~/.config/claude-code-router/config.yaml` add `throttle429Threshold: 3` and `throttleMaxDelaySeconds: 30` to the `zai/0` provider; `kill -HUP $(pgrep claude-code-router)`.
- While `zai/0` is in its 429 state, `/tmp/claude-code-router.log` shows the `[throttle] provider=zai/0 state=on` line, `[req] ... provider=zai/0 ...` lines carry a `latency=` that includes the pacing delay, and after a clean window the `[throttle] provider=zai/0 state=off` line appears.
- Zero=disabled regression check on a benign provider: set `throttle429Threshold: 0` on `ollama-local`; traffic flows with no `[throttle]` lines and no added `latency=`.

## Desired Behavior

1. A provider whose windowed 429 count reaches `throttle429Threshold` enters throttle; while throttled, every request destined for that provider waits the current pacing delay before the router forwards it. A request whose upstream then responds 429 is passed through unchanged — the router never re-sends it; the status is recorded only to adjust future pacing.
2. AIMD dynamics, all bounded: on entry the delay is 1s; each observed 429 doubles it (×2), capped at `throttleMaxDelaySeconds`; each clean 60s window (no 429) halves it (÷2); when the delay decays below 1s the provider exits throttle and requests forward without delay.
3. The pacing queue is bounded: while throttled, at most a fixed number of requests wait for their pacing turn; a request that cannot be paced within the wait bound (≤ `throttleMaxDelaySeconds`) is answered HTTP 429 with the exact Anthropic-shaped `rate_limit_error` body — never a hang, never an unbounded wait.
4. Feature-off default: `throttle429Threshold` absent, 0, or negative means the gate is a no-op — no delay, no pacing 429, no `throttled` counter, byte-for-byte current behavior. `throttleMaxDelaySeconds` defaults to 30 when absent or ≤ 0 on an enabled provider.
5. Each provider's throttle state is independent — throttle on provider A neither delays nor blocks provider B, even when both share an upstream.
6. The gate logs `[throttle] provider=<name> state=on` (INFO) on entry and `[throttle] provider=<name> state=off` (INFO) on recovery, and each paced request at V(4) — the operator's log observable that pacing is active and which provider it applies to.
7. A new Prometheus counter `ccrouter_throttled_total` labeled `{provider}` increments by 1 for each paced request; it is additive — `statusClass`'s 7-value enum and the existing `4xx_rate_limited` classification are untouched.
8. Config knobs `throttle429Threshold` and `throttleMaxDelaySeconds` are per-provider, leniently validated (never fail `config.Load`), and a SIGHUP config reload applies changed values without a process restart (the existing reloader rebuilds the provider tree).

## Constraints

- Config schema: `Provider` gains `throttle429Threshold int` (yaml `throttle429Threshold,omitempty`) and `throttleMaxDelaySeconds int` (yaml `throttleMaxDelaySeconds,omitempty`); zero-value semantics must remain current behavior. The knobs are read at provider level only — unlike `MaxConcurrentRequests` (per-upstream member caps), they are NOT copied into upstream members.
- Keying decision (resolves "per upstream/provider"): the gate wraps the per-provider pool handler at `pkg/factory/factory.go:287` (`NewUpstreamPoolHandler`), so throttle state and knobs are per provider. A provider with an `upstreams:` pool is throttled as one unit; per-member throttle would require the gate inside the pool selection and is out of scope.
- The gate sits between route dispatch and the upstream pool (wrap the `NewUpstreamPoolHandler` result, mirroring spec 011's limiter placement); the model router's body-read, alias, `[1m]`-strip, key-routing, and system-lift flow are untouched.
- The gate's forward is wrapped in the existing `statusRecorder` (its `Unwrap()` keeps SSE-safe flushing through `http.NewResponseController`); the observed status feeds the per-provider detector. The pacing 429 reuses `limiter429Body` (`pkg/handler/concurrency-limiter.go:18`) — the same static generic message, no internal state (queue depth, upstream URL, provider name) leaked.
- Validation: lenient. A negative `throttle429Threshold` is treated as disabled (same as absent/0); a negative `throttleMaxDelaySeconds` is treated as the 30s default. No value fails `config.Load`.
- Fixed internal constants (documented defaults, not knobs): 60s observation window, 1s initial delay, ×2 / ÷2 AIMD multipliers, 1s recovery floor, and the bounded pacing-queue capacity (implementation choice; the observable contract is that saturation → 429). Delay arithmetic is safe against overflow (×2 only applied while the delay is below the max).
- The gate never turns a provider off: a request to a throttled provider is always eventually forwarded (after the delay) or answered 429 on pacing-queue overflow — never dropped, never 5xx.
- No new Prometheus `status_class` value: the `throttled` counter is a separate `CounterVec` labeled `{provider}` (bounded by config, like other provider-labeled series); `statusClass` is unchanged.
- Tests follow the repo's Ginkgo convention and must not depend on real wall-clock waits beyond small explicit waits (e.g. 50ms). Windowed 429 counting uses the injected clock (`WithCurrentDateTime`), mirroring spec 014/017; AIMD/recovery rows assert the computed delay via an accessor on the gate (mirroring the concurrency limiter's `InFlight`) so they never sleep through growing real delays; one row uses a small explicit max delay to prove the sleep actually gates forwarding.
- No new dependencies (Go standard library plus existing `bborbe/*` libs suffice).
- No AI attribution.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Upstream enters a sustained 429 wall (z.ai case) | Windowed count reaches threshold → throttle on; subsequent requests paced (delay ×2 up to max); 429s pass through unchanged, never retried by the router | Clean window → delay decays → throttle off; no operator action |
| Request rate exceeds the pacing queue while throttled | Bounded queue fills; overflow answered HTTP 429 with `rate_limit_error` body | Client backoff spaces retries; queue drains; no operator action |
| Upstream recovers mid-storm (200s resume) | Clean window halves the delay; below the floor → exit throttle, forwarding resumes undelayed | None needed |
| Operator sets a negative knob | Threshold negative → disabled (no-op); max delay negative → 30s default; router starts normally | Operator edits the value if the fallback was unintended |
| Two providers under storm at once | Independent gates: each paces its own provider; no shared state, no cross-provider blocking | None — independence by design |
| Clock skew / injected clock jumps | Windowed counts shift in time; delay arithmetic stays capped by the max — no unbounded behavior | None — the gate uses the router's injected clock (libtime), monotonic in practice |
| Router restart / SIGHUP while throttled | Throttle state is in-memory per provider; a rebuild resets it to not-throttled | Provider re-accumulates the window on the next 429s — reversible by design, no persistent state |

## Security / Abuse

- The gate observes only the upstream's response status — client input never drives throttle state; a client cannot flip the gate except by inducing genuine upstream 429s (which is the provider's own rate limiting, and pacing is the intended response).
- The pacing 429 body is the static `limiter429Body` — it must not reveal queue depth, upstream URL, or provider name. Upstream 429 bodies pass through unchanged.
- Bounded resources: the pacing queue is bounded (memory bounded by queue capacity), each delay is capped by `throttleMaxDelaySeconds`, and overflow 429s shed load — no unbounded client hang, no unbounded goroutine accumulation.
- A client that disconnects while waiting for its pacing turn must never hold a pacing slot or be forwarded (mirrors the concurrency limiter's disconnect path).
- Gate state is mutated from concurrent request goroutines; updates must be safe under concurrency (the detector state transitions are the only shared mutable state — tests exercise concurrent requests).
- Config values are validated leniently; no new user input parsing or path handling is introduced.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Config fields + lenient validation + throttle-gate handler + factory wiring (incl. metric, log, reload/wiring rows) + Ginkgo tests | 1–8 | 1–11 | — |
| 2 | `docs/config.md` + `docs/config.example.yaml` + `docs/metrics.md` + CHANGELOG `## Unreleased` bullet | 8 (knobs documented) | 12, 13 | prompt 1 (documents fields + metric prompt 1 adds) |

Rationale: prompt 1 establishes the config contract, the gate behavior, the metric, and the log lines with their tests; prompt 2 documents the shipped knobs and counter. This mirrors spec 011's two-prompt shape — the code and the docs each have a single owner, and the docs prompt depends on the code prompt so it documents exactly what shipped.

## Do-Nothing Option

The router keeps forwarding at full rate into a provider in a 429 wall (z.ai `glm-5.3-flash[1m]`, observed 2026-08-26: one 200 per ~10s amid a wall of 429s), and immediate client retries keep re-storming an upstream already refusing work. The concurrency limiter caps simultaneous requests but does not react to observed rate-limit responses. Cost: every time any provider enters a rate-limited state, the storm window extends — wasted upstream capacity, wasted client retry tokens, and a longer effective outage. Deferring the change keeps paying that cost each time it happens; the fix is one bounded Go handler plus a two-line per-provider config flip, off by default.

## Verification Result

**Verified:** 2026-08-27T19:17:38Z (HEAD dd9e342)
**Binary:** installed dark-factory CLI (spec lifecycle); feature binary built from HEAD dd9e342 for the live run (standalone on 127.0.0.1:18788, not the installed router)
**Scenario:** no scenario file (spec Non-goal: no e2e scenario) — structural + live: full Go suite, AC greps, standalone run of the feature binary through config→factory→gate against a fake upstream returning 429
**Evidence:**
- `make test` PASS (pkg / pkg/factory / pkg/handler / pkg/reloader all ok); `make precommit` PASS ("ready to commit"); tree clean after
- AC greps: config_test.go throttle429Threshold x8; config.md x6; config.example.yaml x1; metrics.md x2; CHANGELOG `## Unreleased` bullet x1
- Live storm: `[throttle] provider=test state=on` after 3rd 429; paced `delay=1s`→`2s`→`4s` (AIMD x2); request latency 1.002s/2.003s/4.002s vs 1ms pre-gate; upstream 429 body passed through unchanged (never retried)
- Live recovery: after 60s clean window `[throttle] provider=test state=off`; next request latency=0s, no pacing line
**Verdict:** PASS

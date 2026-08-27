---
status: completed
spec: [018-adaptive-429-delay-gate]
summary: 'Documented the per-provider 429 delay gate (throttle429Threshold/throttleMaxDelaySeconds) in docs/config.md, docs/config.example.yaml, docs/metrics.md (ccrouter_throttled_total counter) and added a feat entry under a new ## Unreleased in CHANGELOG.md; make precommit green.'
execution_id: claude-code-router-adaptive-429-exec-056-spec-018-adaptive-429-docs-changelog
dark-factory-version: dev
created: "2026-08-27T19:01:00Z"
queued: "2026-08-27T19:08:52Z"
started: "2026-08-27T19:08:55Z"
completed: "2026-08-27T19:11:58Z"
branch: dark-factory/adaptive-429-delay-gate
---

# Docs + changelog: throttle429Threshold / throttleMaxDelaySeconds and ccrouter_throttled_total

<summary>
- The configuration reference documents both new per-provider fields (`throttle429Threshold`, `throttleMaxDelaySeconds`) in the schema block and in a dedicated "429 delay gate" section.
- The section explains the operator-relevant semantics: the 60s observation window, the threshold trigger, the AIMD pacing (1s entry, ×2 per 429, ÷2 per clean window, capped at the max), that the 429'd request is never retried, the bounded pacing queue with its Anthropic-shaped 429 overflow, the 30-second default max delay, and per-provider independence.
- The example config shows both fields on a provider as commented optional lines, so an operator copying the file does not accidentally change behavior.
- The metrics reference documents the new `ccrouter_throttled_total{provider}` counter and states that the `status_class` 7-value enum is unchanged.
- The changelog gains a `## Unreleased` section (none exists today — the last release v0.44.5 folded it) with a feature entry mentioning `throttle429Threshold`.
- No Go source is touched — this prompt documents what prompt 1 shipped.
</summary>

<objective>
Document the per-provider adaptive 429 delay gate in the operator-facing docs and changelog, so an operator can turn the gate on for a rate-limited provider (e.g. z.ai under a sustained 429 wall) with two YAML knobs and know exactly what the router does and how to observe it.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- This prompt depends on prompt 1 (`spec-018-adaptive-429-config-and-gate.md`): do not approve or execute until prompt 1 has completed and shipped `pkg/handler/throttle-gate.go` and the `defaultThrottleMaxDelaySeconds = 30` constant in `pkg/factory/factory.go` — the `<context>` reads them.
- Read `docs/config.md` — the `## Schema` YAML block's `providers:` sub-block (the commented optional lines: `# maxConcurrentRequests:` on line ~44 and `# maxConcurrentWaitSeconds:` on line ~45, followed by the `# window:` / `# days:` / `# upstreams:` comments), the `## Concurrency limit` section (lines ~170-190), and the section order: `## Schema` → `## Routing` → `## Aliases` → `## Model pools` → `## Requires leading system` → `## Concurrency limit` → `## Upstream pools` → `## Time-of-day windows` → `## Auth` → ... → `## Reload`. The new `## 429 delay gate` section slots in after `## Concurrency limit` and before `## Upstream pools`.
- Read `docs/config.example.yaml` — the `providers.ollama-local` block's commented optional lines (`# maxConcurrentRequests:` on line ~59 and `# maxConcurrentWaitSeconds:` on line ~60, followed by `# upstreams:` on line ~61). The new throttle lines go between the maxConcurrent lines and the `# upstreams:` line, following the file's existing commented-optional convention.
- Read `docs/metrics.md` — the `## Series` table (rows for `ccrouter_requests_total`, `ccrouter_request_duration_seconds`, `ccrouter_alias_resolutions_total`, `ccrouter_tokens_total`, lines ~20-23) and the notes below it. The new counter row goes after `ccrouter_tokens_total`.
- Read `CHANGELOG.md` — the file jumps straight from `# Changelog` to `## v0.44.5`; there is NO `## Unreleased` heading yet (verified). The new heading is created immediately above `## v0.44.5`. Released sections (`## v0.44.5` and below) are frozen — never edit them.
- Read the prompt-1 result (the behavior being documented): `pkg/handler/throttle-gate.go` (constants: `throttleObservationWindow = 60 * time.Second`, `throttleInitialDelay = time.Second`, ×2/÷2 multipliers, `throttleRecoveryFloor = time.Second`, `throttleMaxPacedRequests = 32`; the `[throttle] provider=<name> state=on/off` and V(4) paced log lines) and `pkg/factory/factory.go` (the `defaultThrottleMaxDelaySeconds = 30` constant). Document only what those actually ship — no forward-referencing.
- Read `docs/dod.md` — `docs/config.md` / `docs/config.example.yaml` / `docs/metrics.md` / `CHANGELOG.md` update rules.
- Coding plugin docs (in-container paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — CHANGELOG entry placement and phrasing (`feat:` prefix → minor bump; one bullet per logical change).
  - `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — docs conventions.
</context>

<requirements>
1. **`docs/config.md` — schema block.** In the `## Schema` YAML block's `providers:` sub-block, add two commented lines directly after the `# maxConcurrentWaitSeconds:` line (before the `# window:` comment), e.g.:

   ```yaml
    # throttle429Threshold: 3   # optional; enable adaptive pacing: once the 60s-windowed count of upstream 429s reaches this, subsequent /v1/* requests to THIS provider are delayed before forwarding (see ## 429 delay gate). Absent or 0 or negative = disabled.
    # throttleMaxDelaySeconds: 30 # optional; upper bound of the pacing delay while throttled (default 30)
   ```

2. **`docs/config.md` — new `## 429 delay gate` section.** Insert after the `## Concurrency limit` section and before `## Upstream pools`. Document, at the same level of care as the neighboring sections:
   - The two fields and their defaults: `throttle429Threshold` (absent, 0, or negative = disabled, byte-for-byte today's behavior) and `throttleMaxDelaySeconds` (default 30 when absent, 0, or negative on an enabled provider).
   - What the gate does: once the windowed count of upstream 429 responses within the fixed 60s observation window reaches `throttle429Threshold`, the provider enters throttle and every subsequent `/v1/*` request to it waits the current pacing delay before the router forwards it. The 429'd request itself is never retried — the response passes through unchanged; the status is observed only to adjust future pacing.
   - The AIMD dynamics, all bounded and fixed (not configurable): on entry the delay is 1s; each observed 429 doubles it (×2), capped at `throttleMaxDelaySeconds`; each clean 60s window (no 429) halves it (÷2); when it decays below 1s the provider exits throttle and requests forward undelayed.
   - The bounded pacing queue: while throttled, at most a fixed number of requests wait their pacing turn; a request that cannot be paced within the max delay is answered HTTP 429 with the same Anthropic-shaped `rate_limit_error` JSON body as the concurrency limiter (`{"type":"error","error":{"type":"rate_limit_error",...}}`), never a 5xx and never a hang. A client that disconnects while waiting is never forwarded and holds no slot.
   - Per-provider independence: each provider's throttle state is its own — throttling one provider neither delays nor blocks another, even when two providers share one upstream.
   - The knobs are read at provider level only — unlike `maxConcurrentRequests`/`maxConcurrentWaitSeconds` they are NOT copied onto `upstreams:` pool members; a throttle field on a member is silently ignored (set both on the provider block).
   - Validation is lenient: a negative `throttle429Threshold` is treated as disabled and a negative `throttleMaxDelaySeconds` as the 30s default — the config always loads.
   - SIGHUP reload applies changed values without a restart (the reloader rebuilds the per-provider gates). Throttle state is in-memory per provider, so a reload resets a throttled provider to not-throttled; it re-accumulates the window on the next 429s — reversible by design.
   - Observability: entry/exit log lines `[throttle] provider=<name> state=on` / `state=off` at INFO, each paced request at glog `-v` ≥ 4, and the model router's existing `[req] ... latency=` includes the pacing delay. A new additive counter `ccrouter_throttled_total{provider}` counts paced requests (see `docs/metrics.md`); the `status_class` enum and `4xx_rate_limited` classification are unchanged — an upstream 429 still lands in `4xx_rate_limited`.
   - Suggested motivating example: observed live 2026-08-26, z.ai `glm-5.3-flash[1m]` (provider `zai/0`) entered a sustained 429 wall. Set `throttle429Threshold: 3` and `throttleMaxDelaySeconds: 30` on that provider so the router paces traffic into the breathing window instead of re-storming an upstream already refusing work. Also state the zero=disabled regression check: on a benign provider, `throttle429Threshold: 0` keeps traffic flowing with no `[throttle]` lines and no added latency.

3. **`docs/config.example.yaml`.** Under `providers.ollama-local`, add the two fields as COMMENTED optional lines directly after the `# maxConcurrentWaitSeconds:` line and before the `# upstreams:` line, e.g.:

   ```yaml
    # throttle429Threshold: 3   # optional; enable adaptive pacing for a rate-limited provider (see docs/config.md ## 429 delay gate). Absent or 0 or negative = disabled.
    # throttleMaxDelaySeconds: 30 # optional; upper bound of the pacing delay while throttled (default 30)
   ```

   They MUST be commented (not active) — an operator copying this example must not get unexpected pacing on ollama-local. The file's `throttle429Threshold` text may appear only in comments or a disabled placeholder.

4. **`docs/metrics.md`.** Add a row to the `## Series` table after `ccrouter_tokens_total`:

   ```markdown
   | `ccrouter_throttled_total` | `provider` | counter | `1` (per paced request) |
   ```

   Add a short note (alongside the existing notes below the table) stating: this counter counts requests the 429 delay gate actually delayed before forwarding (`throttle429Threshold` enabled — see `docs/config.md ## 429 delay gate`); overflow 429s and non-paced requests do not increment it; `provider` is bounded by the YAML config like the other provider-labeled series; and it is additive — the `status_class` 7-value enum is unchanged, and upstream 429s still record through `4xx_rate_limited`.

5. **`CHANGELOG.md`.** Create the `## Unreleased` heading immediately above `## v0.44.5` and add one `feat:` entry under it mentioning `throttle429Threshold` (and `throttleMaxDelaySeconds` and `ccrouter_throttled_total`), following `changelog-guide.md` phrasing and the repo's existing entry style (e.g. the detail level of the `## v0.44.0` feat entry — the long detailed `feat:` style; `## v0.44.3`'s sole entry is `- docs:`, not a feat). Cover: the optional per-provider adaptive delay gate, the windowed 429 trigger, the bounded AIMD pacing (doubling per 429, halving per clean window, capped, off by default), that the 429'd request is never retried, the overflow 429 with the Anthropic-shaped body, per-provider independence, the new `ccrouter_throttled_total{provider}` counter, and that SIGHUP reload applies new values.
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Do NOT touch any Go source in this prompt — prompt 1 (`spec-018-adaptive-429-config-and-gate.md`) implemented the behavior; this is documentation and changelog only.
- Do NOT invent config knobs, headers, endpoints, or behavior beyond what the spec and prompt 1 define — `throttle429Threshold` + `throttleMaxDelaySeconds` + `ccrouter_throttled_total{provider}` are the entire surface (spec Non-goals). No per-model throttling, no cross-provider state, no circuit-breaker, no retry logic, no tunable window/multiplier/floor knobs, no new `status_class` value.
- Validation semantics are lenient: a negative `throttle429Threshold` is treated as disabled and a negative `throttleMaxDelaySeconds` as the 30s default — no fail-closed rejection, the config always loads (spec Constraints, AC 2).
- This prompt depends on prompt 1: do not approve or execute until prompt 1 has shipped `pkg/handler/throttle-gate.go` and the `defaultThrottleMaxDelaySeconds = 30` constant — the `<context>` reads them.
- Do NOT edit released `CHANGELOG.md` sections (`## v0.44.5` and below) — they are frozen history.
- The docs must describe behavior prompt 1 actually shipped — no forward-referencing unbuilt features.
- `make precommit` must remain green.
</constraints>

<verification>
make precommit

# Prompt-1 dependency shipped (fail loudly if mis-sequenced):
test -f pkg/handler/throttle-gate.go
grep -n 'defaultThrottleMaxDelaySeconds = 30' pkg/factory/factory.go

# AC 12 — both knobs documented:
grep -c 'throttle429Threshold' docs/config.md            # expect >=1
grep -c 'throttle429Threshold' docs/config.example.yaml  # expect >=1

# AC 12 — counter documented:
grep -c 'ccrouter_throttled_total' docs/metrics.md       # expect >=1

# AC 13 — changelog bullet under ## Unreleased:
sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c throttle429Threshold   # expect >=1

# Example stays behavior-neutral (fields commented only) — fail if any UNCOMMENTED occurrence exists:
! grep -nE '^[[:space:]]*throttle429Threshold' docs/config.example.yaml   # comment lines start with '#' and do not match
! grep -nE '^[[:space:]]*throttleMaxDelaySeconds' docs/config.example.yaml   # comment lines start with '#' and do not match
</verification>

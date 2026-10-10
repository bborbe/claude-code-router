---
status: approved
spec: [019-cold-start-admission-gate]
created: "2026-10-10T14:08:35Z"
queued: "2026-10-10T15:02:15Z"
branch: dark-factory/cold-start-admission-gate
---

# Docs + changelog: cold-start admission gate knobs and metrics

<summary>
- The configuration reference documents all three new per-provider knobs in the schema block and in a dedicated section that links the existing 429 delay gate section.
- That section states each knob's meaning, its default, and its zero-value semantics — that absent, zero, or negative turns that check off, and that a missing or negative window means the 600-second default.
- The section explains what the operator observes: cold requests held then spread out, a bounded queue, a refusal with a retry hint rather than a 5xx, and that established sessions are never held.
- The example config shows all three fields as commented optional lines on a provider, so copying the example does not change behaviour.
- The metrics reference documents all four new series and states that the rate-limited status class is unchanged.
- The README mentions the new configuration alongside the existing concurrency and pacing knobs.
- The changelog gains a feature bullet under the existing Unreleased heading naming the new knobs.
- No Go source is touched — this prompt documents what the earlier prompts shipped.
</summary>

<objective>
Document the per-provider cold-start admission gate in the operator-facing docs and changelog, so an operator can turn the gate on for a provider that backs a fleet with a single YAML knob set and know exactly what the router does, what it refuses, and how to observe it.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only. `make test` / `make precommit` are root targets; this repo's `Makefile` does NOT use `ROOTDIR`/`default.env`. `.git` may be masked in the container — NEVER put a bare `git` command in `<verification>`.
- This prompt depends on prompts 1–4: read the shipped `pkg/config.go` (the three `Provider` fields), `pkg/handler/cold-start-gate.go` (the fixed constants, the refusal, the `[coldgate]` log lines), and `pkg/handler/metrics.go` (the four series) — document only what those actually ship, no forward-referencing.
- Read `docs/config.md` — the `## Schema` YAML block's `providers:` sub-block (the commented optional lines `# throttle429Threshold:` and `# throttleMaxDelaySeconds:` near lines 46–47, followed by the `# window:` / `# days:` / `# upstreams:` comments), and the section order: `## Concurrency limit` → `## 429 delay gate` → `## Upstream pools` → `## Time-of-day windows` → `## Auth` → ... → `## Reload`. The new `## Cold-start admission gate` section slots in AFTER `## 429 delay gate` and before `## Upstream pools`, and its prose must link the 429 section.
- Read `docs/config.example.yaml` — the `providers.ollama-local` block's commented optional lines (`# maxConcurrentRequests:`, `# maxConcurrentWaitSeconds:`, `# throttle429Threshold:`, `# throttleMaxDelaySeconds:`, then `# upstreams:`). The three new lines go after the throttle lines and before `# upstreams:`, as comments.
- Read `docs/metrics.md` — the `## Series` table (its last row is `ccrouter_throttled_total`) and the notes below it; the four new rows go after that row, plus a short note.
- Read `README.md` — the example config block and the surrounding prose; add one mention of the new configuration near the existing config discussion.
- Read `CHANGELOG.md` — `## Unreleased` ALREADY EXISTS (its current bullet is a `build:` entry). APPEND the new bullet under it; do NOT create the heading and do NOT edit released sections.
- Read `docs/dod.md` — the `docs/config.md` / `docs/config.example.yaml` / `docs/metrics.md` / `README.md` / `CHANGELOG.md` update rules.
- Coding plugin docs (in-container paths): `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` (`feat:` prefix → minor bump; one bullet per logical change; never use the prompt filename), `documentation-guide.md`.
</context>

<requirements>
1. **`docs/config.md` — schema block.** In the `## Schema` YAML block's `providers:` sub-block, add three commented lines directly after the `# throttleMaxDelaySeconds:` line and before the `# window:` comment:
   ```yaml
    # coldPrefillBudgetTokens: 65536  # optional; enable cold-start admission: ceiling on in-flight cold-prefill tokens for THIS provider (see ## Cold-start admission gate). Absent or 0 or negative = budget check off.
    # coldSessionWindowSeconds: 600   # optional; window deciding whether a session is new; absent or 0 or negative = 600 (default 600)
    # newSessionRatePerMinute: 4      # optional; cap on newly-seen session ids admitted per minute; absent or 0 or negative = rate check off.
   ```
2. **`docs/config.md` — new `## Cold-start admission gate` section.** Insert after `## 429 delay gate` and before `## Upstream pools`. Document, at the same level of care as the neighbouring sections:
   - The three fields with their meanings and defaults: `coldPrefillBudgetTokens` (the ceiling on in-flight cold-prefill tokens; absent, `0`, or negative = budget check off), `coldSessionWindowSeconds` (the window that decides whether a session is new; **default** 600 when absent, `0`, or negative), and `newSessionRatePerMinute` (the cap on newly-seen session ids per minute, with a fixed burst of 2; absent, `0`, or negative = rate check off).
   - The **default** and the zero-value semantics must be stated explicitly for each field (a reader must be able to tell what happens when the field is unset).
   - What counts as cold vs warm: a request whose `x-session-id` was last seen inside the window is warm and is never held; a request whose id is absent, unknown, or last seen outside the window is cold.
   - What the gate does: cold requests are admitted while their estimated prefill fits the budget and the rate allowance, the excess waits in a bounded queue of 32, and a request that cannot be admitted within 30 seconds — or that arrives when the queue is full — is answered HTTP 429 with the same Anthropic-shaped `rate_limit_error` body as the concurrency limiter and an integer `Retry-After` header in the range 1–60. Never a 5xx, never a dropped request. The refusal body is the existing static generic constant — it carries no queue depth, provider name, upstream URL, or session-identifying value.
   - The estimate is the request body size divided by 3.5; budget is released the moment the response stream emits its first content delta (not on a timer, not at message start), and also when the response ends or the client disconnects.
   - Fixed internal constants, documented as defaults and NOT exposed as knobs: rate burst 2, queue capacity 32, max wait 30s, estimate divisor 3.5, `Retry-After` clamp 1–60s.
   - The knobs are read at provider level only and are NOT copied onto `upstreams:` pool members — a cold knob on a member is silently ignored (the same rule the linked `## 429 delay gate` section states).
   - Validation is lenient: a negative budget or rate disables that check while the other still applies; a negative window falls back to 600 seconds; no value fails `config.Load`.
   - SIGHUP applies changed values without a restart (the reloader rebuilds the per-provider gates); admission state is in-memory, so a reload resets the window, the budget, and the bucket.
   - Observability: each delayed and each refused cold request emits one INFO line `[coldgate] provider=<name> decision=delayed reason=<budget|rate>` or `[coldgate] provider=<name> decision=refused reason=<budget|rate|queue_full|timeout>`; raw session ids are never logged. Four additive series are documented in `docs/metrics.md`; the `status_class` enum and `4xx_rate_limited` classification are unchanged — a refusal still lands in `4xx_rate_limited`.
   - Link the `## 429 delay gate` section (e.g. "sits inside the 429 delay gate — see `## 429 delay gate`") so an operator can see how the two per-provider gates relate.
   - State the motivating case: a burst of new sessions each carrying a large uncached context can push a backend past its throughput ceiling; this gate is the server-side admission point for that burst.
3. **`docs/config.example.yaml`.** Under `providers.ollama-local`, add the three fields as COMMENTED optional lines directly after the `# throttleMaxDelaySeconds:` line and before the `# upstreams:` line. They MUST be commented (an operator copying the example must not get unexpected admission control).
4. **`docs/metrics.md`.** Add four rows to the `## Series` table after `ccrouter_throttled_total`:
   ```markdown
   | `ccrouter_cold_admission_delayed_total` | `provider` | counter | `1` (per delayed cold request) |
   | `ccrouter_cold_admission_refused_total` | `provider` | counter | `1` (per refused cold request) |
   | `ccrouter_cold_tokens_in_flight` | `provider` | gauge | `4096` (in-flight cold tokens) |
   | `ccrouter_cold_ttft_seconds` | `provider` | histogram | `0.842` (p95 bucket) |
   ```
   Add a short note: the two counters count cold requests the gate delayed (waited then admitted) and refused; the gauge rises while cold requests hold their reservation and returns to zero as budget is released; the histogram observes cold time from dispatch to the first content delta; all four are additive — the `status_class` 7-value enum is unchanged and a refusal still records through `4xx_rate_limited`.
5. **`README.md`.** Add one sentence to the config discussion mentioning the new per-provider cold-start admission configuration (the three knobs) alongside the existing concurrency and pacing knobs, linking `docs/config.md`. The token `coldPrefillBudgetTokens` must appear at least once.
6. **`CHANGELOG.md`.** APPEND one `feat:` bullet under the existing `## Unreleased` heading naming `coldPrefillBudgetTokens` (and the other two knobs and the four series), following `changelog-guide.md` phrasing and the repo's existing entry style. Do NOT create a new heading and do NOT edit released sections.
7. **Before finishing, re-run `<verification>` and confirm every command passes.** Walk requirements 1–6 against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- This prompt MUST run after prompts 1–4. If `pkg/handler/cold-start-gate.go` or `handler.NewColdStartGate` in `pkg/factory/factory.go` is absent, STOP and report a blocker rather than documenting unshipped behaviour — the daemon runs with `verificationGate=false`, so the `<verification>` guard below will not stop a mis-sequenced run on its own. Document only what those prompts actually shipped — no forward-referencing unbuilt features. If a documented detail is not in the shipped code, fix the doc, not the code.
- Do NOT touch any Go source in this prompt — this is documentation and changelog only.
- Do NOT invent config knobs, headers, endpoints, or behaviour beyond the three knobs and the four series: the rate burst 2, queue capacity 32, max wait 30s, estimate divisor 3.5, and `Retry-After` clamp 1–60s are FIXED internal constants, not knobs (spec Non-goals). No per-model or per-session budget, no circuit breaker, no new `status_class` value, no persistent queue.
- Validation semantics are lenient: a negative budget or rate disables that check; a negative (or zero) window falls back to 600 seconds — no fail-closed rejection, the config always loads.
- The example config lines MUST stay commented — an operator copying `docs/config.example.yaml` must not get unexpected admission control.
- Do NOT edit released `CHANGELOG.md` sections; APPEND to the existing `## Unreleased` heading only.
- No AI attribution in docs or comments.
- `make precommit` must remain green.
</constraints>

<verification>
make precommit

# Prompt 1-4 dependency shipped (fail loudly if mis-sequenced):
test -f pkg/handler/cold-start-gate.go
grep -n 'handler.NewColdStartGate' pkg/factory/factory.go

# AC 14 — all three knobs documented in config.md AND config.example.yaml:
grep -c 'coldPrefillBudgetTokens' docs/config.md            # expect >= 1
grep -c 'coldSessionWindowSeconds' docs/config.md           # expect >= 1
grep -c 'newSessionRatePerMinute' docs/config.md            # expect >= 1
grep -c 'coldPrefillBudgetTokens' docs/config.example.yaml  # expect >= 1
grep -c 'coldSessionWindowSeconds' docs/config.example.yaml # expect >= 1
grep -c 'newSessionRatePerMinute' docs/config.example.yaml  # expect >= 1

# AC 14 — the section states the default, not just the token:
sed -n '/^## Cold-start admission gate/,/^## /p' docs/config.md | grep -ci 'default'   # expect >= 1 — anchored to the NEW section, not the first schema-block mention

# AC 14 — README mentions the new configuration:
grep -c 'coldPrefillBudgetTokens' README.md                 # expect >= 1

# AC 14 — all four series documented:
grep -c 'ccrouter_cold_admission_delayed_total' docs/metrics.md   # expect >= 1
grep -c 'ccrouter_cold_admission_refused_total' docs/metrics.md   # expect >= 1
grep -c 'ccrouter_cold_tokens_in_flight' docs/metrics.md          # expect >= 1
grep -c 'ccrouter_cold_ttft_seconds' docs/metrics.md              # expect >= 1

# AC 15 — changelog bullet under the existing ## Unreleased:
sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c coldPrefillBudgetTokens   # expect >= 1

# Example stays behaviour-neutral (fields commented only — fail if any UNCOMMENTED occurrence exists):
! grep -nE '^[[:space:]]*coldPrefillBudgetTokens' docs/config.example.yaml
! grep -nE '^[[:space:]]*coldSessionWindowSeconds' docs/config.example.yaml
! grep -nE '^[[:space:]]*newSessionRatePerMinute' docs/config.example.yaml
</verification>

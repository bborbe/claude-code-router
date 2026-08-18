---
status: approved
spec: [011-max-concurrent-requests]
created: "2026-08-18T20:06:00Z"
queued: "2026-08-18T20:18:51Z"
---

# Docs + changelog: maxConcurrentRequests / maxConcurrentWaitSeconds

<summary>
- The configuration reference documents both new per-provider fields (`maxConcurrentRequests`, `maxConcurrentWaitSeconds`) in the schema block and in a dedicated "Concurrency limit" section.
- The section explains the operator-relevant semantics: absent, 0, or negative means unlimited (today's behavior unchanged), excess requests queue per-provider, the 30-second default wait, the clean Anthropic-shaped 429 the client retries, and that per-provider caps are independent even when two providers share one upstream.
- The example config shows both fields on a provider as commented optional lines, so an operator copying the file does not accidentally change behavior.
- The changelog gains a `## Unreleased` section (none exists today) with a feature entry mentioning `maxConcurrentRequests`.
- No Go source is touched — this prompt documents what prompt 1 shipped.
</summary>

<objective>
Document the per-provider concurrency-limiting config surface and behavior in the operator-facing docs and changelog, so an operator can cap a provider (e.g. the two seibert vllm entries at 8) and know exactly what the router does when the cap is reached.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- Read `docs/config.md` — the `## Schema` YAML block's `providers:` sub-block (per-provider keys `upstream`, `token`, `models`, `requiresLeadingSystem`, `allowedApiKeys`), and the section order: `## Schema` → `## Routing` → `## Aliases` → `## Requires leading system` → `## Auth` → `## Routing by API key` → ... → `## Example — all four providers` → `## Reload`. The new `## Concurrency limit` section slots in after `## Requires leading system` and before `## Auth`.
- Read `docs/config.example.yaml` — the `providers.ollama-local` block (its last field is `requiresLeadingSystem`), where the new optional fields are shown as commented lines following the file's existing commented-optional convention (e.g. the `# allowedApiKeys:` lines).
- Read `CHANGELOG.md` — the file currently jumps straight from `# Changelog` to `## v0.30.2`; there is NO `## Unreleased` heading yet (verified). The new heading is created immediately above `## v0.30.2`. Released sections (`## v0.30.2` and below) are frozen — never edit them.
- Read the prompt-1 result (the behavior being documented): `pkg/handler/concurrency-limiter.go` and `pkg/factory/factory.go` (the `defaultMaxConcurrentWaitSeconds = 30` constant). Document only what those actually ship — no forward-referencing.
- Read `docs/dod.md` — `docs/config.md` / `docs/config.example.yaml` / `CHANGELOG.md` update rules.
- Coding plugin docs (in-container paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — CHANGELOG entry placement and phrasing.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — docs conventions.
</context>

<requirements>
1. **`docs/config.md` — schema block.** In the `## Schema` YAML block's `providers:` sub-block, add two commented lines under the `<provider-key>:` entry (next to the existing commented `# allowedApiKeys:` line), e.g.:

   ```yaml
       # maxConcurrentRequests: 8   # optional; cap concurrent /v1/* requests to THIS provider (see ## Concurrency limit). Absent or 0 or negative = unlimited.
       # maxConcurrentWaitSeconds: 30 # optional; how long a queued request waits for a slot before HTTP 429 (default 30)
   ```

2. **`docs/config.md` — new `## Concurrency limit` section.** Insert after the `## Requires leading system` section and before `## Auth`. Document, at the same level of care as the neighboring sections (DB 5 must be documented — AC 8/9):
   - The two fields and their defaults: `maxConcurrentRequests` (absent, 0, or negative = unlimited, byte-for-byte today's behavior) and `maxConcurrentWaitSeconds` (default 30 when absent, 0, or negative on a capped provider).
   - What the router does when the cap is reached: excess requests queue in a per-provider semaphore; a request that frees a slot within the wait is forwarded normally and unchanged; a request still waiting when the wait elapses is answered HTTP 429 with an Anthropic-shaped `rate_limit_error` JSON body so the client's own backoff retries cleanly (never a 5xx).
   - The slot is held for the full request including streaming SSE responses; a client that disconnects while queued never holds a slot.
   - Per-provider caps are independent even when two providers share one upstream (the seibert case: `seibert-vllm-default` and `seibert-dark-factory` each cap at their own N; combined concurrency to the shared upstream can reach 2N — accepted by design).
   - Validation is lenient: a negative `maxConcurrentRequests` is treated as unlimited and a negative `maxConcurrentWaitSeconds` as the 30s default — the config always loads.
   - SIGHUP reload applies changed values without a restart (the reloader rebuilds the per-provider limiters).
   - No new metrics: router-issued 429s land in the existing `[req] ... status=429` log line and the `4xx_rate_limited` metrics class.
   - Suggested motivating example: set `maxConcurrentRequests: 8` on the two seibert vllm providers to stay under `vllm.seibert.tools`'s own per-user ceiling of 8.

3. **`docs/config.example.yaml`.** Under `providers.ollama-local` (after the `requiresLeadingSystem:` block), add the two fields as COMMENTED optional lines with a comment explaining the seibert use case, e.g.:

   ```yaml
       # maxConcurrentRequests: 8     # optional; cap concurrent /v1/* requests to this provider (see docs/config.md ## Concurrency limit). Absent or 0 or negative = unlimited.
       # maxConcurrentWaitSeconds: 30 # optional; how long a queued request waits for a slot before HTTP 429 (default 30)
   ```

   They MUST be commented (not active) — an operator copying this example must not get an unexpected 8-cap on ollama-local. The file's `maxConcurrentRequests` text may appear only in comments or a disabled placeholder.

4. **`CHANGELOG.md`.** Create the `## Unreleased` heading immediately above `## v0.30.2` and add one `feat:` entry under it mentioning `maxConcurrentRequests` (and `maxConcurrentWaitSeconds`), following `changelog-guide.md` phrasing. Cover: the optional per-provider cap, the queueing behavior, the 30s default wait, the 429 `rate_limit_error` response the client retries, per-provider independence, and that SIGHUP reload applies new values. Follow the repo's existing entry style (e.g. the `## v0.29.0` feat entry's detail level).
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Do NOT touch any Go source in this prompt — prompt 1 implemented the behavior; this is documentation and changelog only.
- Do NOT invent config knobs, headers, endpoints, or behavior beyond what the spec and prompt 1 define — `maxConcurrentRequests` + `maxConcurrentWaitSeconds` are the entire surface (spec Non-goals). No global cap, no per-model cap, no retry logic, no new metrics.
- Validation semantics are lenient: a negative `maxConcurrentRequests` is treated as unlimited and a negative `maxConcurrentWaitSeconds` as the 30s default — no fail-closed rejection, the config always loads (spec Constraints, AC 2, operator decision 2026-08-18).
- This prompt depends on prompt 1 (`1-spec-011-max-concurrent-requests-config-and-limiter.md`): do not approve or execute until prompt 1 has completed and shipped `pkg/handler/concurrency-limiter.go` and the `defaultMaxConcurrentWaitSeconds = 30` constant — the `<context>` reads them.
- Do NOT edit released `CHANGELOG.md` sections (`## v0.30.2` and below) — they are frozen history.
- The docs must describe behavior prompt 1 actually shipped — no forward-referencing unbuilt features.
- `make precommit` must remain green.
</constraints>

<verification>
make precommit

# AC 8 — both fields documented:
grep -c 'maxConcurrentRequests' docs/config.md          # expect >=1
grep -c 'maxConcurrentRequests' docs/config.example.yaml # expect >=1
grep -c 'maxConcurrentWaitSeconds' docs/config.md        # expect >=1

# AC 9 — changelog bullet under ## Unreleased:
sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c maxConcurrentRequests   # expect >=1

# Example stays behavior-neutral (fields commented only) — fail if any UNCOMMENTED occurrence exists:
! grep -nE '^[[:space:]]*maxConcurrentRequests' docs/config.example.yaml   # comment lines start with '#' and do not match
</verification>

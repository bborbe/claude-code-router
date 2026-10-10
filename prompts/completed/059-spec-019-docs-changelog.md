---
status: completed
spec: [019-per-host-concurrency-cap]
summary: 'Documented the upstreamHostLimits per-host concurrency cap and ccrouter_upstream_inflight gauge in docs/config.md, docs/config.example.yaml, docs/metrics.md, and added the CHANGELOG ## Unreleased feat bullet'
execution_id: claude-code-router-hostcap-exec-059-spec-019-docs-changelog
dark-factory-version: v0.196.0
created: "2026-10-10T15:30:08Z"
queued: "2026-10-10T16:10:53Z"
started: "2026-10-10T16:23:04Z"
completed: "2026-10-10T16:26:33Z"
---

# Docs + changelog: upstreamHostLimits and ccrouter_upstream_inflight

<summary>
- The configuration reference documents the new top-level `upstreamHostLimits` block, both in the schema overview and in a dedicated "Upstream host limits" section.
- That section explains:
  - how a host key is derived from an upstream URL;
  - that every provider and pool member on one host shares one budget;
  - the absent/zero/negative semantics;
  - the 429 on queue timeout;
  - that host slots are taken before member slots, and what that means when a member cap saturates;
  - reload behavior;
  - the new gauge.
- The docs stop claiming that two providers sharing one upstream can never be capped jointly. The old "accepted by design" wording is rewritten to point at the host cap.
- The vllm "suggested use" guidance moves from per-provider caps to one host cap on `vllm.seibert.tools`, the configuration that would have prevented the 2026-10-10 outage.
- The example config shows a commented host-cap block, so copying it changes nothing.
- The metrics reference documents `ccrouter_upstream_inflight{host}` in its Series table, with a cardinality note.
- The changelog gains a feature entry under `## Unreleased` mentioning `upstreamHostLimits`.
- No Go source is touched. This prompt documents what prompts 1 and 2 shipped.
</summary>

<objective>
Document the per-upstream-host concurrency cap and its in-flight gauge in the operator-facing docs and changelog. An operator can then cap a shared server such as `vllm.seibert.tools` with one config entry, understand how it composes with existing provider and member caps, and confirm on `/metrics` that it holds. The docs must also stop asserting the per-provider-only design that spec 019 deliberately reverses.
</objective>

<context>
- Repo root is the current working directory. Use repo-relative paths only.
- This prompt depends on prompts 1 and 2. Do not execute it until both have shipped:
  - `pkg/handler/host-limiter.go` (`HostLimiter`, `NewHostLimiter`, `Wrap`, `InFlight`);
  - `pkg/handler/upstream-inflight-collector.go`;
  - `UpstreamHostLimits` / `HostLimit` / `UpstreamHostKey` in `pkg/config.go`;
  - `hostLimiterFor` plus the collector registration in `pkg/factory/factory.go`;
  - the `ccrouter_upstream_inflight` const in `pkg/handler/metrics.go`.

  Read them first and document ONLY what they actually do. Do not forward-reference anything unbuilt.
- Read the spec for the operator-facing facts:
  - `specs/in-progress/019-per-host-concurrency-cap.md` (or `specs/completed/` if already moved): its Summary, Problem, Desired Behavior, Constraints and Failure Modes tables.
  - The motivating incident: `seibert-vllm-default`'s three pool members (16 / 32 / 32, time-windowed) and `seibert-dark-factory`'s member (16) all resolve to `vllm.seibert.tools`. Their caps summed to ~32 concurrent in the daytime window and ~48 at night or on weekends. On 2026-10-10 a fleet burst took the host down for ~17 minutes.
- Read `docs/config.md`:
  - The `## Schema` YAML block. Its top-level keys are `router:`, `allowedApiKeys:`, `default_token:` and `trace:`, then the commented `# model_pools:` block, then `providers:`.
  - The section order: `## Schema` → `## Routing` → `## Aliases` → `## Model pools` → `## Requires leading system` → `## Concurrency limit` → `## 429 delay gate` → `## Upstream pools` → `## Time-of-day windows` → `## Auth` → … → `## Reload` → `## Related`.
  - These bullets assert the reversed design or are now stale, and must be rewritten:
    - `## Concurrency limit` → the **"Per-provider caps are independent."** bullet, which ends "combined concurrency to the shared `vllm.seibert.tools` upstream can reach 2N — accepted by design".
    - `## Concurrency limit` → **"Observability is unchanged. No new metrics."**
    - `## Concurrency limit` → **"Suggested use."** (set `maxConcurrentRequests: 8` on both seibert vllm providers).
    - `## Upstream pools` → **"Per-member caps are independent."**, which says two members each allowing 8 do not share one global cap.
  - The `## 429 delay gate` bullet "Per-provider independence … even when two providers share one upstream" is about THROTTLE state, not concurrency caps. Leave it as is.
- Read `docs/config.example.yaml`:
  - The top-level commented optional keys (`# allowedApiKeys:`, `# default_token:`) above `providers:`.
  - The `ollama-local` provider's concurrency comment block: "# Concurrency cap for this provider … Example use: stay under a shared vllm.seibert.tools per-user ceiling of 8 by setting maxConcurrentRequests: 8 on each seibert vllm provider that points at it.", followed by the commented `# maxConcurrentRequests:` / `# maxConcurrentWaitSeconds:` lines.
- Read `docs/metrics.md`:
  - The `## Series` table. Its last row is `ccrouter_throttled_total`.
  - The explanatory paragraphs below it: the cardinality ceiling paragraph, and the `ccrouter_throttled_total` note.
  - **Coordination:** a sibling branch may also add per-provider `ccrouter_inflight_requests` rows here. Add only the one row and one note this spec owns, and do not touch other rows.
- Read `CHANGELOG.md`. Today it goes straight from the header to `## v0.47.3`, with NO `## Unreleased` heading. If one exists when you run, because another branch merged first, append the new bullet under the existing `## Unreleased`. Otherwise create `## Unreleased` immediately above the newest `## vX.Y.Z` heading. Released sections are frozen history, so never edit them.
- Read `docs/dod.md` for the doc-update rules (`docs/config.md`, `docs/config.example.yaml`, and `CHANGELOG.md` under `## Unreleased`).
- Coding plugin docs (in-container paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`: entry placement and phrasing (`feat:` prefix → minor bump, one bullet per logical change).
  - `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md`: docs conventions.
</context>

<requirements>
1. **`docs/config.md`, schema block.** In the `## Schema` YAML block, add a commented top-level block after the `# model_pools:` comment block and before `providers:`, in the same commented-optional style, e.g.:

   ```yaml
   # upstreamHostLimits:         # optional; cap concurrent /v1/* requests per upstream HOST, shared by every provider and pool member resolving to it (see ## Upstream host limits). A host not named here is unlimited.
   #   <host-key>:               # lowercased host of the upstream URL, plus ":port" only for a non-default port — e.g. vllm.seibert.tools, 127.0.0.1:8317
   #     maxConcurrentRequests: 8     # optional; Absent or 0 or negative = unlimited.
   #     maxConcurrentWaitSeconds: 30 # optional; how long a request waits for a host slot before HTTP 429 (default 30)
   ```

2. **`docs/config.md`, new `## Upstream host limits` section.** Insert it after `## Concurrency limit` and before `## 429 delay gate`. Match the care of the neighboring sections: a YAML snippet, then bold-lead bullets. Cover:
   - **What it bounds.** The total concurrent `/v1/*` requests reaching one upstream host, summed across every provider and pool member whose upstream resolves to it. Contrast this with per-provider and per-member caps, which each bound only their own traffic. This is the bound spec 011 deliberately left out, and spec 019 adds it. The per-provider and per-member caps stay in force.
   - **Host key.** The upstream URL's host, lowercased, with the port appended as `host:port` only when it is explicit and not the scheme's default (80 for http, 443 for https). The path is ignored. Give the examples `https://vllm.seibert.tools` and `https://vllm.seibert.tools/v1` → `vllm.seibert.tools`, and `http://127.0.0.1:8317` → `127.0.0.1:8317`. Matching is exact string equality, so write keys lowercased. Two upstreams that differ only by port are capped independently and need one entry each.
   - **Feature-off by default.** An absent `upstreamHostLimits`, or a host not named in it, is unlimited: no queueing, no router-issued 429, today's behavior. An entry naming a host no provider resolves to loads and is inert. There is no automatic derivation of host caps.
   - **At the cap.** A request takes a host slot before it is forwarded. At the cap it waits in a bounded queue. If a slot frees within `maxConcurrentWaitSeconds` the request forwards unchanged. If not, it gets HTTP 429 with the same Anthropic-shaped `rate_limit_error` body as the provider cap (`{"type":"error","error":{"type":"rate_limit_error",...}}`), never a 5xx, and the body names no host, queue depth or provider. The slot is held for the whole request, including SSE streams, and a client that disconnects while queued never takes one.
   - **Composition with provider/member caps.** The host cap is layered OUTSIDE the per-provider / per-member cap. A request takes its host slot first, then its member slot, so the effective limit at any instant is the lower of the two. Document the consequence from spec Failure Modes: when one provider's member cap is below the host cap and saturates, its queued requests still hold host slots, so requests to another provider on the same host can wait. The remedy is to raise that member cap to at least the host cap, or shorten that member's `maxConcurrentWaitSeconds` so requests queued behind it give up their host slots sooner. Lowering the member cap makes this worse, because more of that provider's requests then wait at the member level while still holding host slots.
   - **Defaults and leniency.** `maxConcurrentWaitSeconds` defaults to 30 when absent, 0 or negative on a capped host. A negative `maxConcurrentRequests` is unlimited. The config always loads, never fail-closed.
   - **SIGHUP applies changes.** A raised or lowered cap is live on the rebuilt handler tree without a restart, and a host removed from the map becomes unlimited. In-flight requests finish on the tree they started on.
   - **Observability.** `ccrouter_upstream_inflight{host="<host-key>"}` is a gauge with one series per host the router serves, uncapped hosts included. For a capped host it is the shared slot occupancy and never exceeds the cap, so a value pinned at the cap together with rising `4xx_rate_limited` means the cap is binding. For an uncapped host it is the live in-flight count. A host-cap 429 appears in the existing `[req] … status=429` log line and the `4xx_rate_limited` class of `ccrouter_requests_total`. See `docs/metrics.md`. Tuning signals: a cap set too low shows sustained 429s at low load with the gauge pinned at the cap; a cap set too high shows upstream 5xx or a latency rise with the gauge below the cap.
   - **Interaction with the 429 delay gate.** A host-cap 429 is issued beneath the provider's throttle gate, so on a provider with `throttle429Threshold` set it counts toward that threshold exactly like a member-cap 429. State this in one sentence.
   - **Suggested use (the 2026-10-10 incident).** `seibert-vllm-default` (three time-windowed pool members) and `seibert-dark-factory` all resolve to `vllm.seibert.tools`, so their caps summed to ~32 to 48 concurrent requests and a fleet burst took the host down. One entry bounds the host regardless of provider config:

     ```yaml
     upstreamHostLimits:
       vllm.seibert.tools:
         maxConcurrentRequests: 8
         maxConcurrentWaitSeconds: 30
     ```

     Verify with `curl -fsS http://127.0.0.1:8788/metrics | grep ccrouter_upstream_inflight`.

3. **`docs/config.md`, rewrite the reversed-design statements.**
   - Replace the `## Concurrency limit` bullet **"Per-provider caps are independent."** It must still say each provider's cap is its own budget. It must DROP "can reach 2N — accepted by design", and it must say that two providers sharing one upstream host CAN be jointly bounded with `upstreamHostLimits` (link `## Upstream host limits`).
   - Replace **"Observability is unchanged. No new metrics."** It keeps the statement that a router-issued 429 lands in `[req] … status=429` and `4xx_rate_limited`. It drops "No new metrics", and points to `ccrouter_upstream_inflight{host}` for per-host in-flight.
   - Replace the `## Concurrency limit` **"Suggested use."** bullet so the shared-host vllm case points at the host cap (`## Upstream host limits`) rather than at per-provider caps alone.
   - In `## Upstream pools`, amend **"Per-member caps are independent."** so it still says each member enforces its own cap, and adds that members on the same host still share an `upstreamHostLimits` budget when one is configured.
   - After editing, no line in `docs/config.md` may claim that providers or members sharing one upstream cannot be jointly capped (spec AC 11).

4. **`docs/config.example.yaml`.**
   - Add a COMMENTED top-level `upstreamHostLimits` block above `providers:`, alongside the other commented optional top-level keys, e.g.:

     ```yaml
     # upstreamHostLimits:          # optional; cap concurrent /v1/* requests per upstream HOST, shared by every provider / pool member resolving to it (see docs/config.md ## Upstream host limits). Unnamed hosts are unlimited.
     #   vllm.seibert.tools:
     #     maxConcurrentRequests: 8
     #     maxConcurrentWaitSeconds: 30
     ```

     It MUST stay commented, so an operator copying the example gets no new behavior.
   - Update the provider-level comment "Example use: stay under a shared vllm.seibert.tools per-user ceiling of 8 by setting maxConcurrentRequests: 8 on each seibert vllm provider that points at it." For a server shared by several providers, it should recommend the top-level `upstreamHostLimits` entry instead.

5. **`docs/metrics.md`.**
   - Add one row to the `## Series` table after `ccrouter_throttled_total`:

     ```markdown
     | `ccrouter_upstream_inflight` | `host` | gauge | `8` (at an 8-slot host cap) |
     ```

   - Add one note paragraph alongside the existing notes, in the same style as the `ccrouter_throttled_total` note. State that:
     - it is the current in-flight count per upstream host, summed across all providers and pool members resolving to it, read live at scrape;
     - for a host capped in `upstreamHostLimits` it is the shared semaphore occupancy and never exceeds the cap, and for an uncapped host it is the in-flight count;
     - every host the router serves has a series, and a config entry naming a host no provider uses has none;
     - **cardinality:** one series per distinct upstream host in the config, bounded by the YAML config like the other config-bounded labels and typically a handful. Add it to the ceiling statement;
     - `host` is the operator-configured upstream host key, never a request header or client value;
     - it is additive, and all existing series are unchanged.

6. **`CHANGELOG.md`.** Add one `feat:` bullet under `## Unreleased`. Create the heading immediately above the newest `## vX.Y.Z` if it is absent, or append to it if present. Match the detail level of the repo's longer `feat:` entries. The bullet must mention `upstreamHostLimits` and cover:
   - the optional top-level per-upstream-host cap shared by every provider and pool member resolving to the host;
   - the host-key rule;
   - host slot taken before member slot, with per-provider and per-member caps still enforced;
   - 429 `rate_limit_error` on queue timeout, never 5xx;
   - lenient validation, and off by default;
   - SIGHUP applies changes;
   - the new `ccrouter_upstream_inflight{host}` gauge;
   - the motivating 2026-10-10 `vllm.seibert.tools` outage.

7. **Self-check before finishing.** Run `make precommit`. Re-read `docs/config.md` end to end and confirm that every remaining `independent` line is accurate under the host cap (`grep -n 'independent' docs/config.md`). Then walk spec ACs 10, 11 and 12 against the edits.
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Do NOT touch any Go source. Prompts 1 and 2 implemented the behavior, and this prompt is documentation and changelog only.
- Document only what shipped: `upstreamHostLimits` (`maxConcurrentRequests`, `maxConcurrentWaitSeconds`) plus the `ccrouter_upstream_inflight{host}` gauge is the entire surface. Do not invent knobs, flags, auto-derived caps, per-host logging, queue-depth / wait-time / rejection metrics, start-rate limiting, circuit breakers or backoff (spec Non-goals).
- Per-provider and per-member `maxConcurrentRequests` / `maxConcurrentWaitSeconds` semantics are unchanged. The docs must say the host cap is an additional, coarser bound layered over them, not a replacement.
- Validation is lenient: a negative host `maxConcurrentRequests` is unlimited, a negative host `maxConcurrentWaitSeconds` is the 30s default, and the config always loads.
- `docs/config.example.yaml` stays behavior-neutral: the new block is commented only.
- Do NOT add, rename or document the per-provider gauges `ccrouter_inflight_requests` / `ccrouter_inflight_requests_peak`, which a sibling branch owns. In `docs/metrics.md`, add only this spec's row and note and leave other rows untouched.
- Do NOT edit released `CHANGELOG.md` sections (`## v0.47.3` and below), which are frozen history.
- No AI attribution.
- `make precommit` must remain green.
</constraints>

<verification>
make precommit

# Prompts 1+2 shipped (fail loudly if mis-sequenced):
test -f pkg/handler/host-limiter.go
test -f pkg/handler/upstream-inflight-collector.go
grep -n 'ccrouter_upstream_inflight' pkg/handler/metrics.go

# AC 10 — schema + gauge documented (each expect >=1):
grep -c 'upstreamHostLimits' docs/config.md
grep -c 'upstreamHostLimits' docs/config.example.yaml
grep -c 'ccrouter_upstream_inflight' docs/config.md
grep -c 'ccrouter_upstream_inflight' docs/metrics.md

# AC 11 — the "accepted by design" 2N statement and the per-member no-shared-cap claim are gone:
! grep -q 'accepted by design' docs/config.md
! grep -q 'do not share one global cap' docs/config.md
grep -n 'independent' docs/config.md

# AC 12 — changelog bullet under ## Unreleased (expect >=1):
awk '/^## /{sec=$0} /upstreamHostLimits/{print "sits under: " sec}' CHANGELOG.md
sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c upstreamHostLimits

# Example stays behavior-neutral — fail if any UNCOMMENTED upstreamHostLimits line exists:
! grep -qE '^[[:space:]]*upstreamHostLimits' docs/config.example.yaml
</verification>

---
status: completed
approved: "2026-08-20T20:11:08Z"
verifying: "2026-08-22T17:10:00Z"
completed: "2026-08-22T15:26:01Z"
branch: dark-factory/spec-016
---

## Summary

- The operator-facing `[req]` V1 log line names the provider that served a request but not which member of a multi-upstream pool handled it — with two providers in the real config already running pools, pool membership is invisible in the default log.
- Change the provider value on the `[req]` line to carry the selected member's zero-based index inline: `[req] POST /v1/messages model=deepseek-v4-flash provider=seibert-vllm-default/0 status=200 latency=4.787s in=0 out=599`.
- The `/N` suffix is ALWAYS emitted — a single-upstream provider logs `provider=X/0` (one-entry pool), never a bare `provider=X`. One consistent format.
- The index is the zero-based position of the selected member in the provider's `upstreams:` declaration order, taken from the pool selection that already returns it.
- No separate `upstream=` key and no URL/hostname on the `[req]` line; only the `[req]` V1 line changes — `[route]`, `[inbound.end]`, and metrics are untouched.

## Problem

claude-code-router supports multi-upstream pools per provider (spec 012), and two providers in the real config already run pools, but the `[req]` V1 log line shows only `provider=<name>`, not which pool member served the request. The `[route] session=<id> upstream=<url>` line does name the member, but it lives at glog V(2) — suppressed at default verbosity and on a separate line from `[req]`. So at default log level the operator cannot tell which server of a pool (which capacity/cost tier) served a given request, and has no way to correlate `[req]` latency to a specific member.

## Goal

After this work, an operator reading the default V(1) log can tell, for every request, which member of a provider's pool served it: the `[req]` line's provider value carries the member's zero-based index (`provider=<name>/<index>`), in both the alias and non-alias variants, with a consistent shape that always includes the suffix (single-upstream pools log `/0`).

## Non-goals

- Redesigning routing or selection — session pinning, weighted ring hash, least-loaded, round-robin tie-break, and time-window eligibility are all unchanged; the spec only reads the index that selection already returns.
- A separate `upstream=` key on the `[req]` line — the provider label carries the index.
- Logging the upstream URL or hostname instead of the index — the zero-based index is shortest, needs no parsing, and maps to `upstreams:` declaration order.
- Skipping the `/0` suffix for single-upstream pools — the suffix is always emitted.
- Any change to other log lines (`[route]`, `[inbound.end]`) or to metrics — only the `[req]` V1 line gains the suffix.
- Any change to the model-pool member index — the logged index is the upstream-pool member index, never the model-pool member index.

## Acceptance Criteria

- [ ] **Non-alias `[req]`, multi-member pool:** a request routed through a provider with a 2-member `upstreams:` pool emits `[req] POST /v1/messages model=<m> provider=<provider>/<index> status=<code> latency=<lat> in=<in> out=<out>` where `<index>` is the zero-based position (in `upstreams:` declaration order) of the member that served the request. Evidence: `go test -count=1 ./pkg/handler/` passes a Ginkgo row whose captured stderr matches `\[req\] .* provider=<pool-name>/\d+ status=` and the `\d+` is the index of the member that served (see AC 4's correlation method).
- [ ] **Alias variant:** a request resolved through an alias (`alias=` field present) emits the same `/N` suffix on the provider value. Evidence: `go test -count=1 ./pkg/handler/` passes a Ginkgo row whose captured stderr matches `\[req\] .* model=<orig> alias=<resolved> provider=<pool-name>/\d+ status=`.
- [ ] **Single-upstream `/0`:** a request served by a one-entry pool (a provider with a single `upstreams:` member or a legacy single `upstream:` provider) emits `provider=X/0` — the `/0` suffix always appears, never a bare `provider=X`. The `/0` evidence must include at least one row that dispatches through a **real one-entry `upstreamPoolHandler`** (a provider built as a one-member pool), so the pool handler's own publish path is exercised — not only a non-pool fallback stub. The existing `[req]` assertions in `pkg/handler/model-router_test.go` are extended so `provider=minimax` and `provider=default-fallback` match `provider=minimax/0` / `provider=default-fallback/0`. Evidence: `go test -count=1 ./pkg/handler/` passes; `grep -c 'provider=.*/0' pkg/handler/model-router_test.go` returns ≥1.
- [ ] **Index equals the actually-selected member (non-zero, defeats a hardcoded `/0` fake):** the `[req]` line's index matches the member the same request was dispatched to, and at least one correlation row asserts a **non-zero** index. Evidence: `go test -count=1 ./pkg/handler/` passes these Ginkgo rows, each pairing the captured `[req] provider=<name>/\d+` with the captured `[route] session=<id> upstream=<url>` line for that same request (the test knows each member's declaration index) —
  - (a) pinned-session row: a session-id chosen so the weighted ring hash pins to member 1 of a 2-member pool; assert `provider=<name>/1` and correlate with the `[route] upstream=<url-of-member-1>` line;
  - (b) keyless row: a least-loaded dispatch that resolves (round-robin tie-break) to member 1; assert `provider=<name>/1`;
  - (c) nested model-pool row: a request resolved through a model pool (the provider's `upstreamPoolHandler` nested at `pkg/factory/factory.go` model-pool wiring) asserts the upstream-pool member index — never the model-pool member index.
- [ ] **No new key, no URL, field order unchanged:** the `[req]` line gains only the `/N` suffix on the provider value. The whole line matches the anchored regexp `^\[req\] .* provider=[^ ]+/[0-9]+ status=[0-9]+ latency=[^ ]+ in=[^ ]+ out=[^ ]+$` — nothing after `out=`, no key inserted between existing fields — and the captured line contains no `upstream=` and no `http://`/hostname. Evidence: `go test -count=1 ./pkg/handler/` passes a row using the anchored regexp; a negative grep on the captured line for `upstream=` and `http` returns 0 matches.
- [ ] **Build green:** `make precommit` exits 0 in the worktree with the changes and extended tests in place. Evidence: exit code 0.
- [ ] **Docs match the shipped format:** `docs/config.md` and `docs/debug.md` document the `provider=<name>/<index>` format on the `[req]` line and no longer claim the `[req]` line is unchanged in the pool observability notes; a **new `## Unreleased` heading** at the top of `CHANGELOG.md` gains a feat bullet naming the provider index — never appended to the released `## vX.Y.Z` section (changelog fold guard; this repo is `autoRelease: true`). Evidence: `grep -n 'provider=' docs/config.md docs/debug.md` returns ≥1 line containing `/` per file; `awk '/^## /{n++} n==1' CHANGELOG.md | grep -c 'provider'` returns ≥1.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format / lint / vet / vulncheck clean
- `make test` — full Go suite passes, including the new and extended Ginkgo rows named in the ACs
- `go test -race -count=1 ./pkg/handler/` — the per-request propagation is race-clean (AC 4 concurrency guard)
- `grep -c 'provider=.*/0' pkg/handler/model-router_test.go` → ≥1 (AC 3)
- `grep -n 'provider=' docs/config.md docs/debug.md | grep -c '/'` → ≥1 per file (AC 7)

### Operator-executable (runs on the host after PR merge + release + install)

- `make install`; restart the launchd service (`de.bborbe.claude-code-router`).
- Send a request through a provider whose `upstreams:` has 2+ members (the real config already has two such providers); `/tmp/claude-code-router.log` shows `[req] ... provider=<name>/<N>` with N matching the member the request hit (cross-check against a V(2) capture: `curl http://127.0.0.1:8788/setloglevel/2`, then compare the `[route] upstream=` URL to the `[req]` index).
- A request through a single-upstream provider shows `provider=<name>/0`.

## Desired Behavior

1. The router appends `/N` to the provider value on both `[req]` V1 lines (alias and non-alias variants), where N is the zero-based index of the pool member that served the request in the provider's `upstreams:` declaration order. The line shape is `[req] POST /v1/messages model=<m> provider=<provider>/<index> status=<code> latency=<lat> in=<in> out=<out>`.
2. The suffix is always present: a request served by a one-entry pool (single `upstreams:` member or legacy single `upstream:` provider) logs `provider=X/0`, and a dispatch whose path contains no upstream pool handler (e.g. a default-fallback handler) also logs `/0` — the suffix is never conditionally omitted.
3. The index is produced by the upstream pool handler's member selection and published to the router's `[req]` emission before the dispatch completes. Because the leaf pool handler cannot replace the router's request object, the index travels in a value the router and the pool handler share per request — a request-context slot the router injects before dispatching and reads back after the dispatch returns, or an equivalent per-request recorder — with the router defaulting to `0` when no pool handler participated in the dispatch. The pool selection logic itself (weighted ring hash pinning, least-loaded, round-robin tie-break, eligibility windows) is untouched.
4. The logged index reflects the member selected by that exact request, recomputed per request and never cached globally: a pinned session keeps the same index across turns while eligibility is unchanged, a keyless request reflects the least-loaded choice at that moment, and the value is per-request (race-free under concurrent requests).
5. No new log keys and no URL/hostname: the `[req]` line gains only the `/N` suffix on the provider value; the existing key order is unchanged and `in=`/`out=` remain the final two fields. `[route]`, `[inbound.end]`, and all metric lines are unchanged.
6. Docs updated: `docs/config.md`'s pool observability notes and `docs/debug.md`'s V(1) row describe the `provider=<name>/<index>` format and drop the "the `[req]` line is unchanged" claim; a new `## Unreleased` CHANGELOG heading gains a feat bullet (never appended to a released section).

## Constraints

- Frozen `[req]` line contract: existing keys and order preserved, `provider` label carries the `/N` suffix, no `upstream=` key, no URL/hostname. Canonical shape: `[req] POST /v1/messages model=deepseek-v4-flash provider=seibert-vllm-default/0 status=200 latency=4.787s in=0 out=599`.
- The logged index is the upstream-pool member index from `upstreamPoolHandler.selectMember` — the zero-based position in `upstreams:` declaration order (the factory builds the pool's `members` slice in that order, so the returned index IS the declaration position). It is never the model-pool member index.
- A dispatch with no upstream pool handler in the path logs index `0` — uniform `/0`, no conditional omission.
- Selection, concurrency caps, eligibility windows, sampler gating of 200s, and always-log of non-200s are unchanged.
- Propagation must be per-request and race-free (`go test -race` clean) — a process-global index slot would corrupt the log under concurrency and is not acceptable.
- Both `[req]` variants emit the suffix identically.
- The existing `[req]` assertions in `pkg/handler/model-router_test.go` are extended (updated to the `/0` shape), not removed or replaced with weaker matches.
- Tests follow the repo's Ginkgo convention; no new dependencies; no AI attribution.
- Docs and CHANGELOG updates are part of this spec; `docs/config.md`'s and `docs/debug.md`'s statements that "the `[req]` line is unchanged" become false after this lands and must be rewritten.
- The CHANGELOG feat bullet goes under a **new `## Unreleased` heading** — never appended to a released `## vX.Y.Z` section (changelog fold guard; repo is `autoRelease: true`).

## Assumptions

- Every provider route handler in the factory is an `upstreamPoolHandler` (spec 012: legacy single-`upstream:` configs load as a one-entry pool), so `selectMember` returns a valid index on every pooled dispatch; the `/0` default only fires on handlers that are not pool handlers (test fallbacks).
- Selection logic and config schema are unchanged; the index is already computed and returned, so this spec only observes it.
- The router's `[req]` emission runs after the dispatch handler returns (existing flow), so a per-request slot written during dispatch is readable at emission time.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Pool handler fails to publish the index (selection ran, slot never written) | `[req]` falls back to `/0` — line shape stays consistent, but multi-upstream pools under-report (all `/0`) | AC 4's correlation row fails; fix the slot write in the pool handler; no operator action |
| Wrong layer publishes (model-pool member index written instead of upstream-pool index) | `[req]` index names the model-pool member, not the serving upstream | AC 4 nested-pool row asserts the upstream index; fix which `selectMember` result is published |
| Shared slot races under concurrent requests | `go test -race` fails or a burst shows wrong indices on some lines | per-request (context-scoped) slot only; concurrent Ginkgo row |
| SIGHUP reload mid-request | In-flight request finishes against the old tree and logs its own selection; subsequent requests use the new tree — per-request slot prevents cross-tree bleed | none |
| URL/hostname accidentally logged on the `[req]` line | violates AC 5 | AC 5 negative grep fails; strip before emission |

## Security / Abuse

- The index is derived from operator config (`upstreams:` declaration order), not from client input — no new trust boundary and no new input parsing.
- A client-controlled `x-session-id` or model string influences which member is selected and therefore which index is logged — the index is a small positional integer that reveals nothing beyond what the existing V(2) `[route] upstream=` line already shows.
- The `[req]` line never contains the upstream URL or hostname (AC 5), so V(1) disclosure stays at provider name + index.
- No config surface, secret, or credential is touched by this change.

## Suggested Decomposition

Prompts generated in this order — each row is one prompt.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Code: per-request index propagation (router injects a shared slot before dispatch, upstream pool handler publishes its `selectMember` result, router appends `/N` to both `[req]` variants with a `/0` default) + extended Ginkgo rows (both variants, `/0` single-upstream, multi-member index correlated with `[route]`, no-new-key/field-order, `-race` clean) | 1, 2, 3, 4, 5 | 1, 2, 3, 4, 5, 6 | — |
| 2 | Docs + CHANGELOG: `docs/config.md` pool observability notes, `docs/debug.md` V(1) row, CHANGELOG topmost-section feat bullet | 6 | 7 | prompt 1 (documents the shipped line shape) |

Rationale: prompt 1 delivers the behavior and its test evidence (all behavioral ACs); prompt 2 documents the exact format prompt 1 shipped — it must reference the canonical line shape decided by 1, so it depends on prompt 1.

## Do-Nothing Option

The operator keeps the current gap: at default verbosity the `[req]` line names only the provider, and finding which pool member served a request requires bumping to V(2) and cross-referencing a separate `[route] upstream=` line. Two providers in the real config already run pools, so the gap is live today. The change is a small, testable log-format addition with no routing or config impact — deferring it leaves pool membership invisible in the log the operator reads every day.

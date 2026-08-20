---
status: verifying
tags:
    - dark-factory
    - spec
approved: "2026-08-20T14:38:39Z"
verifying: "2026-08-20T15:38:01Z"
branch: dark-factory/global-default-provider-token
---

## Summary

- Today every provider block must declare its own `token:`, so one shared outbound key is copy-pasted across providers and drifts apart; each new provider adds another copy.
- This spec adds a top-level `default_token:` — one shared outbound key defined once, inherited by every provider and every `Upstream` pool member that declares no token of its own.
- Outbound auth resolution order (frozen): a provider's own `token:` wins → else the global default → else the client's `Authorization` header passes through unchanged (the subscription-OAuth case, preserved byte-for-byte).
- Existing configs load unchanged with and without the new field; SIGHUP applies a `default_token:` change without a restart.
- Outbound only — inbound key auth (`allowedApiKeys` / `x-api-key`), routing, session pinning, time windows, and concurrency caps are untouched.

## Problem

Every provider block in `config.yaml` must declare its own `token:`. The same key gets copy-pasted across providers, and any launcher or agent that talks to multiple providers must carry the key per-provider — or the operator maintains N identical YAML lines that drift apart. The multi-provider router made this duplication structural: each new provider means another copy of the same bearer token. Sibling spec 012 added per-`Upstream` pool-member `token:` — the same defaulting problem recurs one level down, and the global default applies there too so pool members without their own token inherit it. One shared key defined once removes the duplication and makes the router's outbound auth a single source of truth; providers needing a different key (separate vLLM quota, the off-peak window keys from spec 014) declare their own `token:` and override.

## Goal

The config can declare a top-level `default_token:`. Every provider — and every `Upstream` pool member — resolves its outbound `Authorization` in a fixed order: its own `token:` if declared, else the global default, else the client's `Authorization` passed through unchanged. The operator defines one shared key once; per-provider overrides stay possible; the no-token passthrough case behaves byte-for-byte as today.

## Non-goals

- Inbound auth changes — `allowedApiKeys` / `x-api-key` semantics are unchanged; this is outbound auth only (what the router sends upstream).
- Per-session keys — key selection is config-static, not session-scoped.
- Token rotation tooling — no expiry / refresh / rotation logic.
- Secrets management (TeamVault) integration — the key stays plain in `config.yaml` as today.
- `token:` on the router block — NOT used; the canonical location is the top-level `default_token:` field (one spelling, no named consumer for the alternative).
- Do NOT add a per-provider opt-out flag that disables the global default — invariant: the resolution order is fixed (own token → global → passthrough). A provider wanting a different key declares its own `token:`; an escape hatch that forces passthrough despite a global default is redundant with that and would be a regression on the Goal. If a consumer demands it, that is a separate spec.

## Acceptance Criteria

- [ ] **Config parse + validate + backward-compat load:** a top-level `default_token:` string parses and validates alongside the existing provider blocks; a value that is not a scalar (a nested mapping or list) fails `Load` with a parse error; configs with and without the field load unchanged. Evidence: `go test -count=1 ./pkg/` passes (new Ginkgo rows: valid parse, non-scalar rejection, backward-compat load of an existing config with zero edits); `grep -c 'default_token' pkg/config_test.go` ≥1.
- [ ] **Global default forwarded:** a provider WITHOUT its own `token:` and WITH the global default set sends `Authorization: Bearer <global>` upstream. Evidence: a factory wiring test dispatches through the real dispatch path to a local `httptest` upstream whose recorded `Authorization` header equals `Bearer <global>`; `go test -count=1 ./pkg/factory/` passes; the V(3) `[upstream.headers]` log line shows `Authorization` as `<redacted len=N>` and never the literal key.
- [ ] **Provider token overrides global:** a provider WITH its own `token:` sends that provider token upstream, overriding the global default. Evidence: the wiring harness records `Authorization: Bearer <provider-token>` where `<provider-token>` ≠ `<global>`; `go test -count=1 ./pkg/factory/` passes.
- [ ] **Neither → client passthrough:** a provider with NEITHER its own `token:` NOR a global default forwards the client's `Authorization` unchanged. Evidence: the existing no-op row in `pkg/handler/auth-swap-transport_test.go` stays green and a wiring test records the upstream `Authorization` equal to the client's original header; `go test -count=1 ./pkg/ ./pkg/handler/ ./pkg/factory/` passes.
- [ ] **Upstream pool-member fallback (spec 012):** a pool member WITHOUT its own `token:` inherits the global default; a member WITH one uses its own. Evidence: a wiring test with an `upstreams:` pool records `Bearer <global>` on a `token:`-less member's dispatch and `Bearer <member-token>` on a member with one; `go test -count=1 ./pkg/factory/` passes.
- [ ] **SIGHUP applies a change:** a second `CreateRouterFromConfig` with a changed `default_token:` rebuilds the tree and the rebuilt tree forwards the new token. Evidence: `go test -count=1 ./pkg/factory/` passes (new wiring row asserts the rebuilt tree sends the new token).

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format / lint / vet / vulncheck clean
- `make test` — full Go suite passes, including the new Ginkgo rows named in the ACs
- `grep -c 'default_token' docs/config.md docs/config.example.yaml` → ≥1 per file
- `sed -n '/^## Unreleased/,/^## /p' CHANGELOG.md | grep -c 'default_token'` → ≥1

### Operator-executable (runs on the host after PR merge + release + install)

- `make install`; restart the launchd service (`de.bborbe.claude-code-router`).
- Phase 1 (global default set): add `default_token: "<fake/limited key>"` to `~/.config/claude-code-router/config.yaml` and give one provider its own overriding `token:`; `kill -HUP $(pgrep claude-code-router)`; enable `curl http://127.0.0.1:8788/setloglevel/3`. Requests to the inheriting provider and the overriding provider produce `[upstream.headers]` lines showing `Authorization: <redacted len=N>` — the inheriting provider's `len` matches the global key's byte length, the overriding provider's matches the override key's, distinguishing them without printing either.
- Phase 2 (backward-compat passthrough): with `default_token:` removed, a no-token provider forwards the client's `Authorization` unchanged — the `[upstream.headers]` line reflects the client's own header and the existing OAuth-subscription flow still works.
- SIGHUP applies an edit: change the `default_token:` value, `kill -HUP`, and confirm the next request's `[upstream.headers]` `len=N` reflects the new key.
- Negative evidence: `grep -cF '<fake/limited key>' /tmp/claude-code-router.log` returns 0 — only redacted values ever reach the log.

## Desired Behavior

1. The config accepts an optional top-level `default_token:` string. Absent or empty = no global default, today's behavior. A value that is not a scalar (a nested mapping or list) is rejected at config load with a parse error.
2. Outbound auth per provider: the effective token is the provider's own `token:` if set, else the global default, else empty. An empty effective token keeps the auth-swap no-op contract — the client's `Authorization` header passes through unchanged (subscription-OAuth case). Resolution happens at wiring time from config, never from per-request client input.
3. The same resolution applies to every `Upstream` pool member (spec 012): the member's own `token:` wins, else the global default, else passthrough. Legacy single-`upstream:` providers resolve through their implicit single member exactly as today (the provider-level token already lands on that member).
4. SIGHUP hot-reload rebuilds the tree from config; a changed `default_token:` applies without a process restart (existing reloader).
5. The global default never appears in logs or trace files: the key flows only in the outbound `Authorization` header, and the existing redaction invariants (`[upstream.headers]` `<redacted len=N>`, trace `Authorization` redaction) cover it — a test asserts no literal key in captured log output.

## Constraints

- Frozen config schema: `Config` gains top-level `DefaultToken string yaml:"default_token,omitempty"` — the canonical location. The alternative `token:` on the router block is NOT used.
- Resolution order is frozen: provider/upstream `token:` (WINS) → global default → client `Authorization` passthrough. No per-provider opt-out flag to force passthrough while a global default is set.
- The no-op-on-empty contract of the auth-swap transport is unchanged — client passthrough stays byte-identical; existing `pkg/handler/auth-swap-transport_test.go` rows stay green.
- Resolution is applied at wiring (the factory seam that constructs each upstream's auth-swap transport with the member token), never from client-controlled per-request state.
- Inbound auth (`allowedApiKeys`, `x-api-key`), key routing, session pinning, time windows, concurrency caps — all unchanged.
- Configs without `default_token:` load byte-identically; validation adds no required-with interactions.
- Docs: `docs/config.md` `## Auth` token table gains the three-way resolution order; `docs/config.example.yaml` shows one global key with an overriding provider; CHANGELOG `## Unreleased` bullet.

## Assumptions

- One shared key covers most providers; providers needing a distinct key (separate vLLM quota, the spec-014 off-peak window keys) declare their own `token:` and override.
- A provider that must pass the client's `Authorization` through while a global default is set has no escape hatch — that combination is not supported by the frozen resolution order; if a consumer demands it, that is a separate spec.
- The operator's live smoke demonstrates passthrough on a config WITHOUT a global default (backward-compat run), because with one set every no-token provider inherits the global by design.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Mis-typed `default_token:` (nested mapping / list) | `Load` fails with a parse error; the router refuses to start | Correct the value; SIGHUP or restart re-applies |
| Global default set but a provider needs a different key | That provider sends the global key upstream instead of its own — the upstream may reject it | Operator adds the provider's own `token:` override (or removes the global default); SIGHUP re-applies; `[upstream.headers]` redacted lines confirm which key went out |
| Upstream rejects the forwarded token (401) | The upstream's own error response is forwarded unchanged (existing proxy behavior, untouched by this spec) | Operator verifies the intended key per provider via `[upstream.headers]`, corrects the config, SIGHUPs |
| SIGHUP reload mid-request | Old tree finishes in-flight requests; new tree serves subsequent ones (existing reloader semantics) | None |
| Operator edits config without SIGHUP | Running tree keeps the old default (existing behavior) | `kill -HUP`; the next `[upstream.headers]` line reflects the new key |

## Security / Abuse

- The global default is operator config, read only at load/wiring — never from client input. A client cannot influence which token the router sends, and the value is never echoed to a client or exposed via `/metrics` or admin endpoints.
- The real key never reaches logs or trace files: the existing redaction invariants cover the outbound `Authorization` header regardless of whether its value came from a provider token or the global default (see `docs/config.md` `## Trace` + `pkg/handler/logging-roundtripper_test.go`); a test asserts no literal key in captured log output (negative evidence).
- The key stays plain in `config.yaml` (`chmod 600`) exactly as provider tokens do today — no new secret-handling mechanism, no TeamVault integration (non-goal).
- No new trust boundary: the value is consumed only when building the router tree; the passthrough path (client-supplied `Authorization`) is unchanged and only reaches an upstream, never logs.

## Suggested Decomposition

Prompts generated in this order — each row is one prompt.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Config: top-level `DefaultToken` field (`default_token`) + parse/validation (non-scalar rejection) + backward-compat load rows | 1 | 1 | — |
| 2 | Factory resolution: effective-token resolution (member `token:` → global → passthrough) at the auth-swap wiring seam; wiring tests (provider inherits global, provider overrides, neither→passthrough, pool-member fallback, SIGHUP-rebuilt tree, no literal token in captured log) | 2, 3, 4, 5 | 2, 3, 4, 5, 6 | prompt 1 |
| 3 | Docs + CHANGELOG: `docs/config.md` `## Auth` resolution table, `docs/config.example.yaml` one-global-key-with-override, `## Unreleased` bullet | 1 (documented) | docs greps only | prompts 1–2 |

Rationale: prompt 1 lands the config contract and the field's load semantics; prompt 2 wires the resolution into the existing per-upstream auth-swap construction and proves the full matrix (inherit / override / passthrough / pool-member / SIGHUP); prompt 3 documents what 1–2 shipped and owns no acceptance criterion itself.

## Do-Nothing Option

Per-provider token duplication persists: N copies of the same key drift apart, each new provider (and each spec-012 pool member) adds another copy, and launcher/agent operators must carry the key per-provider. The fix is a config field plus a resolution step at existing wiring — deferring keeps paying the duplication tax on every provider and pool member.

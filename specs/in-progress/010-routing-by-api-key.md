---
status: verifying
approved: "2026-08-17T19:06:09Z"
generating: "2026-08-17T19:32:57Z"
prompted: "2026-08-17T19:32:57Z"
verifying: "2026-08-17T20:48:58Z"
branch: dark-factory/routing-by-api-key
---

## Summary

- The router routes each `/v1/*` request to the provider whose model glob matched — first match wins in declaration order. Two providers serving the same model glob (`deepseek-*`) can therefore never split traffic; the second provider's glob is dead config.
- vLLM (and most providers) meter quota per API key. The operator wants interactive Claude Code and dark-factory containers to reach the *same* deepseek models through *separate* API keys (separate quotas) at the same `vllm.seibert.tools` upstream.
- This spec adds **routing by presented API key**: a top-level `allowedApiKeys` registry plus a per-provider `allowedApiKeys` override. A request whose `x-api-key` matches a provider's list routes to that provider (its token/quota) — key match wins over model globs.
- The key carrier is the standard `x-api-key` header (clients set `ANTHROPIC_API_KEY`), no custom headers. The router already overwrites outbound `Authorization` with the provider token; it additionally strips `x-api-key` outbound.
- Supersedes the `x-router-key`/`auth.key`/`ROUTER_AUTH_KEY` inbound-auth mechanism shipped in spec 009 (v0.25.0): the non-loopback auth gate now validates `x-api-key` against the registry. Spec 009's admin-endpoint loopback guard and trace redaction stay.

## Problem

The router authenticates callers by a single shared key (`x-router-key`, spec 009) and routes purely by model name. Two independent limitations surface now that the listener binds `0.0.0.0:8788` and multiple consumers (interactive host sessions, dark-factory YOLO containers, cluster agents) share it:

1. **No quota separation.** `providers.seibert-vllm` and `providers.seibert-dark-factory` both legitimately want to serve `deepseek-*` — the first with the general token, the second with a dark-factory token that draws on a separate vLLM quota. The router walks providers in declaration order and the first `deepseek-*` glob wins, so all deepseek traffic lands on one quota and the second provider entry is unreachable.
2. **Auth and routing are decoupled.** The 009 key authenticates but cannot route. A dark-factory container and the operator's own session are indistinguishable to the router even though they must land on different quotas.

The natural caller identity is the API key each Anthropic-compatible client already sends via `x-api-key` (`ANTHROPIC_API_KEY`). Because the router replaces the outbound `Authorization` with the provider's token, the inbound key is not needed upstream — it can safely become both the auth credential and the routing selector.

## Goal

A caller's presented API key selects the provider — and therefore the quota — when it matches a provider's configured key; the same key authenticates non-loopback callers; keyless traffic routes byte-for-byte as it does today. One header (`x-api-key`), one registry (`allowedApiKeys`), a per-provider override, and no change to the routing contract for callers that present no key.

## Non-goals

- Do NOT add per-request quota accounting, rate limiting, or per-key usage tracking — authentication and routing only; quotas live in the upstream (vLLM).
- Do NOT add key rotation machinery, overlapping key windows, or a key-list with separate IDs — rotation is "edit the registry, SIGHUP", accepting that in-flight remote callers with a removed key get 401.
- Do NOT add new headers or client env vars — `x-api-key` via `ANTHROPIC_API_KEY` is the only surface.
- Do NOT change the model-glob routing contract for keyless requests, nor the `default_provider` fallback.
- Do NOT add mTLS or TLS termination — unchanged from 009.
- Do NOT keep the `x-router-key`/`auth.key`/`ROUTER_AUTH_KEY` auth path — it is superseded and removed, not bypassed (spec-verification supersession hygiene).
- Do NOT add per-model keys — keys are per-caller, routing is per-provider.

## Acceptance Criteria

- [ ] **Config parses the new fields:** top-level `allowedApiKeys` (string list) and per-provider `allowedApiKeys` (string list) parse — evidence: `config.Load` returns no error for fixtures covering both shapes (empty registry, populated registry, per-provider list, both together), AND an integration test with no `allowedApiKeys` anywhere asserts the request reaches the glob-selected provider with no 401 and no header mutation.
- [ ] **Ambiguous key claim rejected:** two providers declaring the same key in `allowedApiKeys` fails config load with a message naming the key and both providers — evidence: `config.Load` returns an error for the duplicate-claim fixture; the error text names the key.
- [ ] **Key wins over globs:** with two providers sharing a glob, a request carrying `x-api-key: K` where only provider B lists `K` reaches provider B's upstream (its token) even though the model glob would have selected provider A — evidence: integration test against two upstream test-servers asserts the request arrived only at B's server with B's `Authorization` value.
- [ ] **Valid-but-unclaimed key routes by globs:** a request with `x-api-key` present and in the registry but matching no provider's list routes exactly like a keyless request (glob → default provider) — evidence: integration test asserts the same upstream server and same body as the no-key case.
- [ ] **Keyless request unchanged:** a request with no `x-api-key` is forwarded byte-for-byte (body, headers except router-managed ones) to the glob-selected provider — evidence: integration test asserts the upstream test-server received the identical body and `Authorization` as before this change.
- [ ] **Non-loopback auth gate:** with `allowedApiKeys` non-empty, a non-loopback `POST /v1/messages` with no `x-api-key` or with a key not in the registry returns 401 and contacts no upstream; the presented key value appears nowhere in the log — evidence: HTTP 401, `grep -F '<presented-key-literal>'` over captured logs returns 0 lines, and the upstream test-server received 0 requests.
- [ ] **Loopback stays keyless:** with `allowedApiKeys` non-empty, a request from `127.0.0.1` with no `x-api-key` reaches the routing layer and is not rejected — evidence: response status is the upstream's (not 401); assert status != 401.
- [ ] **Key never forwarded upstream:** on any authenticated/key-routed request, the outbound request carries no `x-api-key` header in any letter case — evidence: upstream test-server's received header map, lower-cased, has no `x-api-key` entry (0 matches).
- [ ] **`Authorization` untouched by the auth layer:** an authenticated remote request to a token-less (pass-through) provider forwards the client's `Authorization` byte-for-byte while still stripping `x-api-key` — evidence: upstream test-server asserts received `Authorization` equals the client-sent value AND has no `x-api-key`.
- [ ] **Key comparison constant-time:** the gate uses a constant-time primitive against each registry key — evidence: `grep -rn 'ConstantTimeCompare' pkg/` returns ≥1 line, AND `grep -rn '== cfg\|cfg\.' pkg/handler/auth-middleware.go` shows no direct equality on the presented key.
- [ ] **009 auth path removed + migration guard:** `auth.key` and the `x-router-key` header no longer appear in the auth/routing path — evidence: `grep -rn 'x-router-key\|auth\.key' pkg/` returns 0 lines, AND a non-loopback request carrying only `x-router-key` (no `x-api-key`) returns 401. (`ROUTER_AUTH_KEY` is deliberately exempt from that grep: the fail-closed guard must name the env var to detect it — its presence is verified by the migration-guard evidence below, not by a removal grep.) A config still carrying the legacy `auth:` block fails config load — evidence: `config.Load` returns an error naming `auth` for the legacy-auth fixture. The binary refuses to start when the `ROUTER_AUTH_KEY` env var is set — evidence: launching the binary with `ROUTER_AUTH_KEY` non-empty exits non-zero and logs an error naming the variable (fail-closed migration guard, DB 7).
- [ ] **SIGHUP applies registry changes:** adding/removing a registry key and sending SIGHUP changes subsequent non-loopback requests without a restart — evidence: 401 after adding enforcement, non-401 after removing it, same process PID across both reloads.
- [ ] **Trace redaction covers `x-api-key`:** with trace enabled, a request carrying `x-api-key` writes a trace file in which that header's value is `***`, never the literal — evidence: integration test writes a trace, sends a known test key, `grep -rF '<known-test-key>' <test-temp-trace-dir>` returns 0 lines, AND `jq -r '.request.headers | to_entries[] | select(.key|ascii_downcase=="x-api-key") | .value' <trace-file>` equals `***`.
- [ ] **Docs + changelog updated:** `docs/config.md` documents `allowedApiKeys` (registry + per-provider) and the routing-by-key rule and the 009 supersession; `docs/dark-factory-integration.md` switches callers from `ANTHROPIC_CUSTOM_HEADERS` to `ANTHROPIC_API_KEY`; `docs/config.example.yaml` and README's example block show the new surface without the legacy `auth:`/`x-router-key` block; `CHANGELOG.md` gains an entry under `## Unreleased` — evidence: `grep -n 'allowedApiKeys' docs/config.md` returns ≥1 line, `grep -n 'ANTHROPIC_API_KEY' docs/dark-factory-integration.md` returns ≥1 line, `grep -Ec 'x-router-key|ROUTER_AUTH_KEY|auth:' docs/config.example.yaml README.md` returns 0 (the 009 example block removed, not left beside the new text), `grep -n '^## Inbound auth' docs/config.md` returns 0 lines (the 009 section and its live instructions removed), `grep -rn 'x-router-key\|ROUTER_AUTH_KEY\|ANTHROPIC_CUSTOM_HEADERS' docs/ | grep -vc 'supersed\|retired\|removed\|Migrating from 009'` returns 0 (the docs name the retired mechanism only inside supersession/migration statements telling the operator it is gone — every mention carries a marker word; no live-instruction mention remains), `sed -n '/^## Unreleased/,/^## v/p' CHANGELOG.md | grep -c 'api.key\|API key\|allowedApiKeys'` returns ≥1 line.
- [ ] **Post-Deploy (Rung-2):** the running router on the host routes by key end-to-end — a remote call with `x-api-key: <dark-factory-key>` and a deepseek model is served by the dark-factory provider's token/quota while a keyless loopback deepseek call is unchanged — evidence: host `curl` with the key returns a non-401 upstream response AND `/tmp/claude-code-router.log` shows a `[route]`/`[req]` line pairing the presented key's provider with the deepseek model; `grep -c 'ROUTER_AUTH_KEY' ~/.local/bin/claude-code-router.sh` returns 0 (the wrapper no longer injects the retired env var).
  - `deploy_check:` `grep -o 'version=[^ ]*' /tmp/claude-code-router.log | tail -1`
  - `deploy_target:` `$(git describe --tags --abbrev=0)`

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

```
make precommit
make test
grep -rn 'ConstantTimeCompare' pkg/
grep -rn 'x-router-key\|auth\.key' pkg/                    # expect 0 lines (ROUTER_AUTH_KEY exempt: the fail-closed guard in factory.go must name the env var — verified by AC 11's migration-guard evidence, not a removal grep)
grep -rn 'allowedApiKeys' pkg/config.go
```

### Operator-executable (runs on the host after PR merge, spec verification ladder)

**Precondition — `make install` is required** (the only build path that injects the version via ldflags; `make build`/`go install @latest` leave `version=dev` and the deploy gate correctly refuses). Build from a tag-clean HEAD.

```bash
make install
launchctl kickstart -k gui/$(id -u)/de.bborbe.claude-code-router
grep -o 'version=[^ ]*' /tmp/claude-code-router.log | tail -1   # equals the git describe tag

# Loopback keyless unchanged
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://127.0.0.1:8788/v1/messages   # non-401

# Remote without key → 401
HOST_IP=$(ipconfig getifaddr en0)
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://$HOST_IP:8788/v1/messages    # 401

# Remote with a registry key → routed, non-401
curl -s -o /dev/null -w '%{http_code}\n' -X POST -H "x-api-key: <registry-key>" \
  http://$HOST_IP:8788/v1/messages                                                  # non-401

# Legacy x-router-key no longer authenticates → 401
curl -s -o /dev/null -w '%{http_code}\n' -X POST -H "x-router-key: <old-key>" \
  http://$HOST_IP:8788/v1/messages                                                  # 401

# Secret-leak invariant
grep -rn "$(teamvault-cli password <teamvault-id>)" . --exclude-dir=.git | wc -l      # 0
```

## Desired Behavior

1. Config gains an optional top-level `allowedApiKeys:` (list of strings) and an optional per-provider `allowedApiKeys:` (list of strings). Absent, `null`, and an empty list are equivalent and all mean: no key enforcement and no key routing — the `/v1/*` path behaves byte-for-byte as it does today. A provider may not repeat a key another provider claims (validation error, AC 2). A key may appear in both the top-level registry and a provider's list — the registry is the auth superset and the single rotation point, the provider claim is the routing pin; only cross-provider duplicates are ambiguous and rejected.
2. The valid inbound key set for auth is the top-level registry when non-empty, else the union of all providers' `allowedApiKeys`. A non-loopback `/v1/*` request must carry an `x-api-key` header whose value is in that set; otherwise 401, with the value never logged (constant-time comparison, loopback exempt).
3. Routing: a request whose `x-api-key` value is in a provider's `allowedApiKeys` is dispatched to that provider (its outbound token), in declaration order — first provider whose list contains the presented key wins. The model glob is **not** consulted when a key matches; the key is the explicit override (AC 3).
4. A request whose `x-api-key` is valid (in the auth set) but matches no provider's list routes exactly like a keyless request — glob matching then `default_provider` (AC 4).
5. A request with no `x-api-key` routes exactly as before this change — glob matching then `default_provider`, with the body and all non-router-managed headers forwarded verbatim (AC 5).
6. Outbound hygiene: `x-api-key` is removed from the cloned outbound request in every letter case, for every provider, before forwarding. The existing `Authorization` behavior is unchanged: providers with `token:` get `Authorization: Bearer <token>`; token-less providers pass the client's `Authorization` through byte-for-byte (AC 9).
7. Supersession: the spec-009 `auth.key` field, the `ROUTER_AUTH_KEY` env var, and the `x-router-key` header are removed from the auth path. The non-loopback auth gate reads `x-api-key` only. Spec 009's unconditional admin-endpoint loopback guard (`/setloglevel/`, `/enabletrace`, `/disabletrace`, `/gc`) and the read-only-open boundary (`/healthz`, `/readiness`, `/metrics`, `HEAD /`) are unchanged. Trace redaction now covers `x-api-key` alongside `Authorization`; the 009-era `x-router-key` redaction is removed together with the header (AC 11, AC 13). Removal is guarded, not silent: a config still containing `auth:` fails load and the binary refuses to start with `ROUTER_AUTH_KEY` set, so an authenticated 009 deployment can never silently degrade to unauthenticated (AC 11).
8. A SIGHUP config reload applies added/removed/changed registry and provider keys to subsequent requests without a process restart, using the existing reload path. In-flight requests finish under the config they started with (AC 12).

## Constraints

- Frozen wiring seam: `buildMux` / `CreateRouterFromConfig` in `pkg/factory/factory.go` is where the `/v1/` middleware chain is registered. The auth middleware and the model router wire in here.
- Frozen invariant: `x-api-key` must be redacted to `***` in trace files by the same mechanism as `Authorization`/`x-api-key` today (`NewTraceMiddleware`, `pkg/handler/trace.go:116`) — a trace file must never contain the inbound key.
- Frozen behavior: the outbound `Authorization` swap in `pkg/handler/auth-swap-transport.go` and the token-less pass-through are unchanged; the auth layer operates on `x-api-key` and neither reads nor writes `Authorization`.
- Frozen default: a config with no `allowedApiKeys` routes exactly as the current release. This is what makes the change non-breaking for the existing single-user localhost setup.
- Frozen header name: `x-api-key`, populated by the standard `ANTHROPIC_API_KEY` env var. Chosen over `Authorization` (occupied by the client's subscription OAuth bearer for pass-through providers) and over inventing a new custom header (no plumbing to add on callers).
- Frozen reload path: existing SIGHUP `atomic.Pointer[http.Handler]` mux swap. No new signal, endpoint, or mechanism.
- Loopback detection must treat both IPv4 and IPv6 loopback as local (`127.0.0.0/8` and `::1`); the remote address comes from the connection, never from `X-Forwarded-For` — there is no trusted proxy in front of this router.
- Config validation lives in the existing `Config.Validate()` single-source-of-truth path per `docs/dod.md`. Duplicate key claims fail load (AC 2) rather than silently first-wins at runtime.
- Keys are literal strings in the operator's `chmod 600` config, consistent with provider `token:` fields. TeamVault is the system of record; the repo and any manifests must contain no literal key.
- The launchd wrapper `~/.local/bin/claude-code-router.sh` currently injects `ROUTER_AUTH_KEY` from TeamVault; it must stop doing so — the registry values live in the config (`chmod 600`), consistent with provider `token:` fields. No env var replaces `ROUTER_AUTH_KEY`.
- Migration guard (fail-closed): a config containing the 009-era `auth:` block fails `Config.Validate()`; the binary exits non-zero at startup when `ROUTER_AUTH_KEY` is set. An authenticated 009 deployment upgrading without `allowedApiKeys` must produce a load/startup error, never a silent fail-open (AC 11, DB 7).
- Go, Ginkgo v2 + Gomega, `github.com/bborbe/errors` for wrapping (never `fmt.Errorf`), handlers in `pkg/handler/`, `make precommit` gates all merges.
- `.dark-factory.yaml`: `workflow: direct`, `autoRelease: true`.

## Failure Modes

| Trigger | Expected behavior | Detection | Recovery |
|---|---|---|---|
| Operator upgrades to the new release with a 009-era config (`auth:` block) or a launchd wrapper still injecting `ROUTER_AUTH_KEY` | Config load fails naming `auth`; the binary refuses to start with `ROUTER_AUTH_KEY` set — the router never serves with auth silently disabled | Router won't start, or `config reload failed: auth` in the log | Remove the legacy `auth:` block / stop the wrapper's env injection, add `allowedApiKeys`, then SIGHUP or restart |
| Operator sets `allowedApiKeys` but forgets to configure a caller | That caller gets 401 on every request; loopback unaffected | Caller-side 401; router log shows the rejection with the caller's address | Set `ANTHROPIC_API_KEY` to a registry key on the caller, or remove the registry + SIGHUP to disable |
| Operator claims the same key on two providers | Config load fails with a message naming the key and both providers; router keeps serving the last good config | `dark-factory status` / router log `config reload failed: ...` | Remove the duplicate claim and SIGHUP; never silently first-wins |
| Registry reloaded while a remote caller is mid-session | In-flight requests finish under the old config; the next request with a removed key returns 401 | Caller sees a sudden 401 with no config change on its side | Update the caller's key; no router action needed |
| Caller sets `ANTHROPIC_API_KEY` to its routing key on a machine that also runs a Claude subscription | The env var overrides the Pro/Max subscription in `-p` mode on the caller (documented Claude Code behavior, unchanged by this router) | Caller-side model/subscription oddity | The operator's own host stays keyless loopback (never sets the env); remote callers are token-based by design |
| An upstream that expects `x-api-key` outbound (not `Authorization`) | The router strips `x-api-key` and the upstream rejects the forwarded request | Upstream 401/403 despite a valid provider token | All Anthropic-compatible upstreams in this config accept `Authorization`; if one does not, it needs its own provider-side auth handling — out of scope |
| Config file becomes invalid while adding the registry | Existing reload behavior: old config stays active, reload logs `config reload failed: ...` at WARNING | `grep 'config reload failed' /tmp/claude-code-router.log` | Fix the YAML and SIGHUP again |
| Operator rotates a key in TeamVault but not in the router config | Router keeps accepting the old key; callers configured from TeamVault get 401 | Caller-side 401 immediately after a rotation | Copy the new value into the registry and SIGHUP |

## Security / Abuse Cases

- **Attacker-controlled input:** the `x-api-key` header value on `/v1/*` and the remote address on every route. The header is compared, never parsed, logged, or interpolated; the remote address is read from the connection only — never from `X-Forwarded-For`, because honouring a forwarded header would let any remote caller claim to be loopback and bypass auth.
- **Trust boundary:** loopback ⇒ fully trusted (keyless, admin allowed); non-loopback ⇒ must present a registry key, and state-changing admin is refused outright. Read-only endpoints (`/healthz`, `/readiness`, `/metrics`, `HEAD /`) stay open to remote callers. This boundary is unchanged from 009; only the credential surface moves from `x-router-key` to `x-api-key`.
- **Timing side channel:** naive string equality on the presented key leaks a matching prefix through response timing. Comparison against each registry key is constant-time (AC 10).
- **Key disclosure paths, each closed explicitly:** not forwarded upstream (AC 8); not written to logs on rejection (AC 6); redacted in trace files (AC 13); absent from the repo, `docs/config.example.yaml`, and any manifest (AC 14 and the leak-grep in the operator rung). The one place the literal legitimately lives is the operator's `chmod 600` config file, alongside the provider tokens already there.
- **Fail-open vs fail-closed, deliberately mixed:** the `/v1/*` key check fails *open* when no `allowedApiKeys` is configured, because that is the existing single-user contract and silently 401-ing every localhost request on upgrade would be the worse failure. Once a registry is present, non-loopback fails *closed*. Duplicate key claims fail *closed at load* (refuse to start with an ambiguous map) rather than first-wins at runtime.
- **What can hang or exhaust:** nothing new. The gate is a header comparison plus a slice membership check on the existing request path — no goroutine, no timer, no external call.

## Suggested Decomposition

Prompts should be generated in this order — each row is a single prompt with a clear scope.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Config surface: top-level + per-provider `allowedApiKeys`, union/registry semantics, duplicate-claim validation in `Config.Validate()`, feature-off default | 1, 2 | 1, 2 | — |
| 2 | Auth gate evolution: valid-key set, `x-api-key` constant-time compare, loopback exempt, 401 without logging the key, strip `x-api-key` outbound, trace redaction of `x-api-key`, **remove** `x-router-key`/`auth.key`/`ROUTER_AUTH_KEY` path, SIGHUP toggle test | 2, 6, 7 | 6, 7, 8, 9, 10, 11, 12, 13 | prompt 1 |
| 3 | Routing by key in `model-router`: key match → that provider (key wins over globs), valid-but-unclaimed → glob routing, no key → unchanged; wire the presented key from the auth middleware via context | 3, 4, 5 | 3, 4, 5 | prompts 1, 2 |
| 4 | Docs + changelog: `docs/config.md` `allowedApiKeys` + routing rule + 009 supersession note, `docs/dark-factory-integration.md` switch to `ANTHROPIC_API_KEY`, `docs/config.example.yaml`, README example, CHANGELOG under `## Unreleased`; launchd wrapper `ROUTER_AUTH_KEY` removal note | 7, 8 (docs only — the behavioral content of both is implemented in prompt 2) | 14 | prompts 1-3 |

Rationale: prompt 1 lands the config surface and validation that prompts 2 and 3 consume (DB 1 spans prompts 1 and 2: prompt 1 owns the fields + validation, prompt 2 owns the gate behavior). Prompt 2 is the auth-half of the supersession (behavioral, fail-closed, incl. the migration guard), prompt 3 is the routing-half (behavioral, fail-open-equivalent for keyless) — they touch different middleware seams and different failure semantics, so they stay independent and can run after 1. Prompt 4 is doc/ops-only (its DB column is documentation of behaviors already implemented, never re-implementation) and runs last, once behavior is settled. AC 15 (post-deploy) and AC 1's global precommit gate are unmapped by design: AC 1 is the gate for every prompt, AC 15 is operator-executable post-deploy verification.

## Do-Nothing Option

Doing nothing keeps the router unable to split the same model across quotas: all deepseek traffic stays on the single `seibert-vllm` key, the dark-factory quota is unusable for deepseek models, and dark-factory containers share the operator's general quota — no separation between interactive and batch workloads, and no way to grant a bounded quota to a third consumer (e.g. cluster agents) without sharing the general key. The cost is not correctness but operability: one shared quota, no per-caller revocation, no way to add a consumer with a bounded key. The auth surface would additionally stay split across `x-router-key` (auth) and whatever the operator bolts on for routing, instead of the single standard `ANTHROPIC_API_KEY` surface this spec provides. The work is small (one config surface, one auth evolution, one routing rule) and reuses the 009 seam, so the cost of doing it now is bounded.

---
status: completed
approved: "2026-08-17T13:31:45Z"
verifying: "2026-08-17T18:01:38Z"
completed: "2026-08-17T18:50:25Z"
branch: dark-factory/inbound-api-key-auth
---

## Summary

- Add an optional inbound API-key check on the `/v1/*` inference path, so the router stops serving every caller that can reach its port.
- Auth is **toggleable by config**: no key configured ⇒ auth disabled and the router behaves exactly as it does today; key configured ⇒ enforced. SIGHUP applies the change with no restart.
- Requests from loopback are exempt regardless of configuration, so the operator's own Claude Code on the host keeps working keyless.
- The key travels in a dedicated `x-router-key` header, is validated in constant time, and is stripped before the request reaches any upstream — `Authorization` is never read or written by the auth layer.
- The state-changing admin endpoints (`/setloglevel/`, `/enabletrace`, `/disabletrace`, `/gc`) gain an always-on non-loopback guard, because their current "safe by bind address" argument dies the moment the listener moves to `0.0.0.0`. Read-only endpoints (`/healthz`, `/readiness`, `/metrics`, `HEAD /`) stay open so remote liveness probes keep working.

## Problem

The router authenticates nothing on the way in. The provider tokens in `~/.config/claude-code-router/config.yaml` are outbound credentials — they authenticate the router to MiniMax, GLM, the Seibert vLLM endpoint and ChatGPT. Nothing authenticates the caller. That is sound while the listener is `127.0.0.1:8788`, and `docs/config.md:142` says so explicitly for the admin endpoints: *"registered on the operator-local listener with no authentication — the same trust model as `/setloglevel`."* Both the dark-factory container integration (`docs/dark-factory-integration.md:21-30`) and the pending work to let cluster agents reach the host require binding `0.0.0.0:8788`, which voids that argument. Once the port is reachable, anyone who can route to the host gets free use of every provider subscription on the operator's credential, and — sharper — `POST /enabletrace`, which writes full request and response **bodies** to disk. Headers are redacted; bodies are not. A reachable port therefore becomes a data-capture switch for every request flowing through, including the operator's own interactive sessions.

## Goal

The router serves `/v1/*` to remote callers only when they present a configured shared key, rejects them with 401 otherwise, and never forwards that key upstream. Its state-changing admin endpoints answer only on loopback. An operator can turn the whole check on or off, or rotate the key, by editing one config field and sending SIGHUP — and a router with no key configured behaves byte-for-byte as it does today, so adopting this version is not a breaking change for the existing single-user localhost setup.

## Non-goals

- Do NOT add per-caller keys, key IDs, or scoped keys — one shared key for all remote callers. Revisit only if a second consumer needs distinct revocation.
- Do NOT add key rotation machinery, overlapping key windows, or a key-list — rotation is "edit the field, SIGHUP", accepting that in-flight remote callers with the old key get 401 until reconfigured.
- Do NOT add mTLS or TLS termination — the shared key plus a restricted firewall source is the proportionate control at this scale. The listener stays plain HTTP.
- Do NOT add rate limiting, quotas, or per-key accounting — separate concern from authentication.
- Do NOT make the admin-endpoint loopback guard toggleable — it is unconditional. A knob to disable it would re-open the `/enabletrace` body-capture hole, which is the sharpest risk in the Problem section.
- Do NOT guard `/healthz`, `/readiness`, `/metrics`, or `HEAD /` — read-only endpoints must keep answering remote liveness and health probes. Concrete consumers that require them reachable from a non-loopback address: k8s liveness/readiness probes (the cluster router deployment and any pod healthcheck against the relay), and Prometheus scraping `/metrics` (per `docs/metrics.md` the `ccrouter_*` series are aggregate telemetry, not state). The boundary is "remote callers may read state, not change it".
- Do NOT read, validate, or rewrite the `Authorization` header in the auth layer — it stays the client's own credential and keeps flowing to token-less providers untouched (see Constraints).
- Do NOT restrict or disable token-less (pass-through) providers for remote callers. An earlier draft of this design made pass-through loopback-only; it is unnecessary once the router key lives in its own header.
- Do NOT add a metrics counter for auth rejections — useful, but a separate change against `docs/metrics.md`.
- Do NOT persist any auth state, or add an HTTP endpoint to toggle auth at runtime — config + SIGHUP is the only control surface.
- Do NOT add auth between the in-cluster router and its own callers — in-cluster traffic, out of scope here.

## Acceptance Criteria

- [ ] `make precommit` exits 0 in the repo root — evidence: exit code
- [ ] **Auth disabled by default (no regression):** with no `auth.key` in the config, a non-loopback `POST /v1/messages` routes exactly as before — evidence: unit/integration test asserts the request reaches the routing layer and the response is the upstream's own status, not a router-generated 401; `grep -c 401` on the test's captured router responses returns 0
- [ ] **Missing key rejected:** with `auth.key` set, a non-loopback `POST /v1/messages` carrying no `x-router-key` header returns HTTP 401 — evidence: HTTP status code 401 from the test client
- [ ] **Wrong key rejected:** same request with `x-router-key: <wrong-value>` returns HTTP 401 — evidence: HTTP status code 401
- [ ] **Correct key passes through:** same request with the matching `x-router-key` reaches the routing layer and is forwarded to the provider the model glob selects — evidence: integration test asserts the upstream test-server received exactly one request with the expected path and body
- [ ] **Key never forwarded upstream:** on a successful authenticated request, the outbound request carries no `x-router-key` header, in any letter case — evidence: negative check — the upstream test-server's received header map, lower-cased, has no `x-router-key` entry (0 matches)
- [ ] **`Authorization` untouched by the auth layer:** an authenticated remote request to a token-less (pass-through) provider forwards the client's `Authorization` header byte-for-byte — evidence: upstream test-server asserts the received `Authorization` value equals the value sent by the client
- [ ] **Loopback stays keyless:** with `auth.key` set, a `POST /v1/messages` from `127.0.0.1` with no `x-router-key` reaches the routing layer and is NOT rejected — evidence: response status is the upstream's, not 401; assert status != 401
- [ ] **Rejection logged without echoing the key:** a rejected request emits one log line naming the event and the remote address, and the presented key value appears nowhere in the log — evidence: log capture contains a line matching `auth rejected`, AND `grep -F '<the-presented-key-literal>'` over the captured log returns 0 lines
- [ ] **Key comparison is constant-time:** the comparison uses a constant-time primitive against the configured key — evidence: `grep -rn 'ConstantTimeCompare' pkg/` returns ≥1 line, AND `grep -rn 'cfg.Auth.Key ==\|== cfg.Auth.Key\|Auth.Key !=' pkg/` returns 0 lines
- [ ] **Admin endpoints reject non-loopback:** `POST /enabletrace`, `POST /disabletrace`, `GET /setloglevel/2` and `POST /gc` from a non-loopback remote address each return HTTP 403 and do NOT change router state — evidence: status 403 for all four, AND a subsequent `/v1/*` request writes no trace file (`ls` count on the trace dir unchanged before vs after) AND `curl -s http://127.0.0.1:8788/metrics | grep -c '^go_gc_duration_seconds_count'` is unchanged before vs after (no GC executed by the remote probe)
- [ ] **Admin endpoints still work on loopback:** the same four calls from `127.0.0.1` return 2xx and take effect — evidence: 2xx status, AND `POST /enabletrace` followed by a `/v1/*` request increases the trace-dir file count by 1
- [ ] **Toggle via SIGHUP, no restart:** starting with no `auth.key`, adding one and sending SIGHUP causes a subsequent keyless non-loopback request to return 401; removing the field and sending SIGHUP again returns it to pass-through — evidence: 401 after the first reload, non-401 after the second, same process (assert PID unchanged across both reloads)
- [ ] **`docs/config.md` documents inbound auth:** a new `## Inbound auth` section documents the `auth.key` field, the `x-router-key` header, the loopback exemption and the toggle semantics — evidence: `grep -n 'x-router-key' docs/config.md` returns ≥1 line AND `grep -n 'Inbound auth' docs/config.md` returns ≥1 line, AND the admin-endpoint section's "no authentication" claim is corrected — `sed -n '/^## Trace/,/^## Example/p' docs/config.md | grep -c 'no authentication'` returns 0
- [ ] **Config example updated:** `docs/config.example.yaml` shows the `auth.key` field with a placeholder and a comment stating that omitting it disables the check — evidence: `grep -n 'x-router-key\|auth:' docs/config.example.yaml` returns ≥1 line, AND the file contains no value that looks like a real secret (`grep -Ec 'sk-|Bearer ' docs/config.example.yaml` returns 0)
- [ ] **README updated:** `README.md`'s "example config in full" block shows the `auth:` block with the same comment — evidence: `grep -n 'auth:' README.md` returns ≥1 line
- [ ] **CHANGELOG entry:** `CHANGELOG.md` gains an entry describing the inbound auth and the admin guard, under `## Unreleased` — evidence (evaluated at PR time, before the auto-release cuts a version section): `sed -n '/^## Unreleased/,/^## v/p' CHANGELOG.md | grep -c 'x-router-key'` returns ≥1 line (the implementer must create the `## Unreleased` heading — the file currently jumps straight to a version section)
- [ ] **Trace files redact the inbound key:** with trace enabled, a request carrying `x-router-key` writes a trace file in which that header's value is `***`, not the literal — evidence: the integration test enables trace into its own temp trace dir, sends a request with a known test key, then `grep -rF '<known-test-key>' <test-temp-trace-dir>` returns 0 lines AND `jq -r '.request.headers | to_entries[] | select(.key=="X-Router-Key" or .key=="x-router-key") | .value' <test-trace-file>` equals `***`
- [ ] **Post-Deploy (Rung-2):** the running router on the host enforces the key end-to-end — evidence: with `auth.key` set and the service reloaded, `curl -s -o /dev/null -w '%{http_code}' -X POST http://<non-loopback-host-ip>:8788/v1/messages` returns `401`, and the same call with the correct `x-router-key` header returns a non-401 status
  - `deploy_check:` `grep -o 'version=[^ ]*' /tmp/claude-code-router.log | tail -1`
  - `deploy_target:` `$(git describe --tags --abbrev=0)`

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

```
make precommit
make test
grep -rn 'x-router-key' docs/config.md docs/config.example.yaml
sed -n '/^## Trace/,/^## Example/p' docs/config.md | grep -c 'no authentication'
grep -n 'auth:' README.md
grep -rn 'ConstantTimeCompare' pkg/
```

### Operator-executable (runs on the host after PR merge, spec verification ladder)

**Precondition — the listener must be on `0.0.0.0:8788` for any remote-path check to be exercisable.** The installed plist binds `127.0.0.1:8788` today. The plist edit itself is owned by the reachability work ([[Prove the Nuke Cluster Can Reach the Router on Burn]] / `docs/dark-factory-integration.md` step 1) and is not part of this spec's diff; verify the current bind first, and only if it is still `127.0.0.1`, apply it with the correct reload mechanism:

```bash
lsof -nP -iTCP:8788 -sTCP:LISTEN            # must show *:8788; if it shows 127.0.0.1:8788:
# edit the plist -listen arg to 0.0.0.0:8788, then (NOT kickstart -k — that keeps cached args):
launchctl bootout gui/$(id -u)/de.bborbe.claude-code-router
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/de.bborbe.claude-code-router.plist
lsof -nP -iTCP:8788 -sTCP:LISTEN            # must now show *:8788 before any remote curl
```

Then install the new binary and confirm the version line. **`make install` is required** — it is the only build path that injects the version via ldflags; `make build` / `go install @latest` leave `version=dev`, and the deploy gate (`deploy_target` compare) will correctly refuse:

```bash
make install
launchctl kickstart -k gui/$(id -u)/de.bborbe.claude-code-router
grep -o 'version=[^ ]*' /tmp/claude-code-router.log | tail -1   # must equal the git describe tag — build from a tag-clean HEAD (git checkout <release-tag> first); a dirty or one-commit-past worktree yields a suffixed version (…-dirty / …-g<hash>) and the deploy gate correctly refuses

# Loopback stays keyless
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://127.0.0.1:8788/v1/messages   # expect non-401

# Remote without the key
HOST_IP=$(ipconfig getifaddr en0)
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://$HOST_IP:8788/v1/messages    # expect 401

# Remote with the key
curl -s -o /dev/null -w '%{http_code}\n' -X POST \
  -H "x-router-key: $(teamvault-cli password <teamvault-id>)" \
  http://$HOST_IP:8788/v1/messages                                                   # expect non-401

# State-changing admin endpoints refuse remote, still answer on loopback
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://$HOST_IP:8788/enabletrace     # expect 403
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://$HOST_IP:8788/gc              # expect 403
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://127.0.0.1:8788/enabletrace    # expect 2xx
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://127.0.0.1:8788/disabletrace   # expect 2xx

# Toggle off without restart
# (remove auth.key from the config, then:)
kill -HUP $(pgrep claude-code-router)
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://$HOST_IP:8788/v1/messages     # expect non-401

# Secret-leak invariant
grep -rn "$(teamvault-cli password <teamvault-id>)" . --exclude-dir=.git | wc -l      # expect 0
```

## Desired Behavior

1. Config gains an optional top-level `auth:` block with a single `key:` string field. Absent, `null`, and empty string are equivalent and all mean **auth disabled** — the `/v1/*` path behaves byte-for-byte as it does today, with no auth middleware effect on the hot path.
2. With `auth.key` non-empty, a request to `/v1/*` whose remote address is **not** loopback must carry an `x-router-key` header whose value equals the configured key; otherwise the router answers 401 and never contacts an upstream.
3. A request to `/v1/*` whose remote address **is** loopback bypasses the check entirely, whether or not a key is configured.
4. On a request that passes the check (or bypasses it), the `x-router-key` header is removed before the request is forwarded, in every letter case, so no upstream ever observes it.
5. The auth layer neither reads nor writes `Authorization`. Existing behavior stands unchanged: providers with a `token:` get their `Authorization` replaced by the router, providers without one get the client's header forwarded verbatim.
6. Key comparison is constant-time with respect to the presented value, so response timing does not reveal a matching prefix.
7. A rejection emits exactly one log line recording the event and the remote address, and never the presented or configured key value. Message text is lowercase per the repo's glog convention.
8. The four state-changing admin routes — `/setloglevel/`, `/enabletrace`, `/disabletrace`, `/gc` — reject any non-loopback remote address with 403 before executing any state change. This guard is unconditional: it does not consult `auth.key` and cannot be disabled by configuration. Read-only endpoints (`/healthz`, `/readiness`, `/metrics`, `HEAD /`) are not guarded.
9. A SIGHUP config reload applies a changed, added or removed `auth.key` to subsequent requests without a process restart, using the router's existing reload path. Requests already in flight finish under the config they started with.

## Constraints

- Frozen wiring seam: `buildMux` / `CreateRouterFromConfig` in `pkg/factory/factory.go` is where the `/v1/` middleware chain and the admin routes are registered today. Auth middleware and the admin guard wire in here.
- Frozen invariant: the `Authorization` / `x-api-key` redaction in `NewTraceMiddleware` (`pkg/handler/trace.go:116`) must not regress. Additionally, `x-router-key` must be redacted to `***` in trace files by the same mechanism — a trace file must never contain the inbound key.
- Frozen behavior: the outbound `Authorization` swap in `pkg/handler/auth-swap-transport.go` and the pass-through documented in `pkg/handler/anthropic-proxy.go:47` and `docs/config.md` §Auth are unchanged. The auth layer is strictly additive and operates on a different header.
- Frozen default: a config with no `auth:` block routes exactly as the current release. This is what makes the change non-breaking for the existing localhost-only setup, and is why the field is optional rather than required-with-empty-default.
- Frozen header name: `x-router-key`. Chosen over `Authorization` (occupied by the client's subscription OAuth bearer — confirmed 2026-08-17 by trace capture: a subscription client sends `Authorization` plus `Anthropic-Beta: …,oauth-2025-04-20,…` and no `x-api-key` at all) and over `x-api-key` (free, but the env var that would populate it, `ANTHROPIC_API_KEY`, overrides an active Pro/Max subscription in `-p` mode). Remote clients populate it via `ANTHROPIC_CUSTOM_HEADERS`.
- Frozen reload path: the existing SIGHUP `atomic.Pointer[http.Handler]` mux swap. No new reload mechanism, no new signal, no new endpoint.
- Loopback detection must treat both IPv4 and IPv6 loopback as local (`127.0.0.0/8` and `::1`), because the listener binds dual-stack on `0.0.0.0`/`*` and `docs/dark-factory-integration.md:44` shows the socket reported as IPv6.
- The remote address is taken from the connection, never from `X-Forwarded-For` or any other client-supplied header — there is no trusted proxy in front of this router.
- Config validation lives in the existing `Config.Validate()` single-source-of-truth path per `docs/dod.md`.
- The key value is a literal string in the operator's `~/.config/claude-code-router/config.yaml`, consistent with how provider `token:` fields already work, with `chmod 600` preserved. TeamVault is the system of record for the value; the repo and any manifests must contain no literal key.
- Go, Ginkgo v2 + Gomega, `github.com/bborbe/errors` for wrapping (never `fmt.Errorf`), handlers in `pkg/handler/`, `make precommit` gates all merges.
- `.dark-factory.yaml`: `workflow: direct`, `autoRelease: true`.

## Failure Modes

| Trigger | Expected behavior | Detection | Recovery |
|---|---|---|---|
| Operator sets `auth.key` but forgets to configure a remote caller | That caller gets 401 on every request; loopback unaffected | Caller-side 401; router log shows `auth rejected` with the caller's address | Set `ANTHROPIC_CUSTOM_HEADERS` with `x-router-key` on the caller, or remove `auth.key` + SIGHUP to disable |
| Config reloaded with a changed key while a remote caller is mid-session | In-flight requests finish under the old config; the next request with the old key returns 401 | Caller sees a sudden 401 with no config change on its side | Update the caller's key; no router action needed |
| `auth.key` set to a whitespace-only or accidentally-quoted value | Treated as a literal key, not as empty — a caller must present that exact string | Router log shows `auth rejected` for a caller the operator believes is configured correctly | Correct the config value and SIGHUP; validation rejects nothing here by design, so the symptom is 401 rather than a start-up failure |
| Config file becomes invalid while adding the field | Existing reload behavior: the old config stays active, reload logs `config reload failed: …` at WARNING | `grep 'config reload failed' /tmp/claude-code-router.log` | Fix the YAML and SIGHUP again; the router never served with a half-applied config |
| Remote caller reaches a state-changing admin endpoint | 403 before any state change; tracing is not enabled, log level and GC are unchanged | 403 at the caller; router log records the refused remote address — `grep 'admin refused' /tmp/claude-code-router.log \| awk '{print $NF}' \| sort -u` enumerates the probing sources | None needed — the guard is the recovery. The log grep above identifies which hosts are probing the port. |
| Listener still bound to `127.0.0.1` when the operator expects the guard to be load-bearing | Everything is loopback, so every request bypasses auth and every admin call succeeds — indistinguishable from auth being broken | `lsof -nP -iTCP:8788 -sTCP:LISTEN` shows `127.0.0.1:8788` rather than `*:8788` | Expected pre-condition, not a bug: the remote-path ACs cannot be exercised until the bind moves. Verify the bind before concluding auth is broken. |
| Trace enabled while remote auth is in use | Trace files redact `x-router-key` alongside `Authorization` and `x-api-key`; bodies are still captured verbatim | `grep -rn '<key literal>' ~/.claude-code-router/trace/` returns 0 | Unchanged from today: `POST /disabletrace` and `rm` the trace dir. The 5-minute TTL bounds accumulation. |
| Operator rotates the key in TeamVault but not in the router config | Router keeps accepting the old key; callers configured from TeamVault get 401 | Caller-side 401 immediately after a rotation | Copy the new value into the config and SIGHUP. No overlapping-key window exists by design (non-goal). |

## Security / Abuse Cases

- **Attacker-controlled input:** the `x-router-key` header value on `/v1/*`, and the remote address on every route. The header is compared, never parsed, logged, or interpolated. The remote address is read from the connection only — never from `X-Forwarded-For`, because no trusted proxy sits in front of this router; honouring a forwarded header would let any remote caller claim to be loopback and bypass both the auth check and the admin guard.
- **Trust boundary moves in this change.** Today's boundary is the loopback socket. After this change the boundary is: loopback ⇒ fully trusted (keyless, admin allowed); non-loopback ⇒ must present the key, and state-changing admin is refused outright. Read-only endpoints (`/healthz`, `/readiness`, `/metrics`, `HEAD /`) stay open to remote callers — a remote caller may read state (liveness, usage telemetry) but may not change it. That read/change split is deliberate: the in-cluster router healthchecks the relay, and per-provider token counts are low-sensitivity aggregate telemetry. Every behavior in this spec is expressed against that split rather than against the bind address, which is exactly what makes it survive the move to `0.0.0.0`.
- **Timing side channel:** naive string equality on the key leaks a matching prefix through response timing. Comparison is constant-time with respect to the presented value (DB 6).
- **Key disclosure paths, each closed explicitly:** not forwarded upstream (AC 6 / DB 4); not written to logs on rejection (AC 9 / DB 7); redacted in trace files (AC 18 / Constraints); absent from the repo, `docs/config.example.yaml`, and any manifest (AC 15, AC 16, AC 17 and the leak-grep in the operator verification rung). The one place the literal legitimately lives is the operator's `chmod 600` config file, alongside the provider tokens already there.
- **What the admin guard protects:** `POST /enabletrace` writes full request and response bodies to `~/.claude-code-router/trace/`, and `POST /gc` forces collection on a long-running daemon. Reachable and unauthenticated, the former is a remote data-capture switch over every request flowing through the router, including the operator's own interactive sessions. This is why the guard is unconditional and why a config knob to disable it is a stated non-goal.
- **Fail-closed vs fail-open, deliberately mixed:** the `/v1/*` check fails *open* when no key is configured, because that is the existing single-user contract and silently 401-ing every localhost request on upgrade would be the worse failure. The admin guard fails *closed* always, because there is no legitimate remote caller for it. Both choices are load-bearing; neither is a default that fell out of the implementation.
- **What can hang or exhaust:** nothing new. The check is a header comparison on the existing request path, adds no goroutine, no timer, no allocation beyond the header lookup, and no external call.

## Suggested Decomposition

Prompts should be generated in this order — each row is a single prompt with a clear scope.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Config surface: optional `auth.key` field, absent/empty ⇒ disabled, wired into `Config.Validate()`; loopback-detection helper covering IPv4 + IPv6 | 1, 3 | — (builds the seams prompts 2-3 consume; its acceptance is verified via the behavioral ACs those prompts own) | — |
| 2 | Auth middleware on `/v1/`: constant-time compare, 401 on missing/wrong, loopback bypass, strip header before forward, rejection logging without the key; `x-router-key` added to trace redaction; SIGHUP toggle test | 2, 4, 5, 6, 7 | 2-10, 13, 18 | prompt 1 |
| 3 | Admin-route guard: unconditional 403 for non-loopback on `/setloglevel/`, `/enabletrace`, `/disabletrace`, `/gc`; wire in `buildMux`; `/healthz`, `/readiness`, `/metrics`, `HEAD /` left unguarded | 8 | 11, 12 | prompt 1 |
| 4 | Docs + changelog: `docs/config.md` new `## Inbound auth` section and correction of the "no authentication" claim, `docs/config.example.yaml` placeholder, README example block, `CHANGELOG.md` under `## Unreleased`, and correction of the `docs/launchd-service.md:142` claim that `kickstart -k` handles `--listen` address changes (it keeps cached args — `bootout`/`bootstrap` is the mechanism; same knowledge belongs in the config-doc auth section) | 9 | 14, 15, 16, 17 | prompts 1-3 |

ACs 1 and 19 are unmapped by design: AC 1 is the global precommit gate for every prompt, AC 19 is operator-executable post-deploy verification. Rationale: prompt 1 lands the config field and the loopback predicate that both later prompts consume, and is unit-testable with no HTTP — it deliberately owns no behavioral AC because those all require the middleware to exist first. Prompts 2 and 3 are independent of each other — different routes, different failure semantics (fail-open vs fail-closed) — so splitting them keeps each prompt's test matrix small and stops the fail-open/fail-closed asymmetry from being reasoned about in one place. Prompt 4 is doc-only and runs last, once the behavior is settled. SIGHUP (DB 9) needs no implementation work — it rides the existing reload path — so it appears only as prompt 2's toggle test (AC 13) and prompt 4's documentation.

The two themes — `/v1/*` inbound auth and the admin loopback guard — ship as one spec deliberately: they are triggered by the same change (the listener moving to `0.0.0.0`), they share the loopback-detection helper, and the guard is near-zero additional cost on top of the middleware seam. Splitting would double the approve/audit/prompt cycles without halving the work; the DB-count and AC-surface budget overflow is bounded by the decomposition above.

## Do-Nothing Option

Doing nothing keeps the router unauthenticated, which is genuinely fine for as long as it stays on `127.0.0.1`. The cost is that it blocks the two changes that need the port reachable: the dark-factory container integration already documents the `0.0.0.0` bind as a required step, and cluster agents reaching the host cannot happen at all. Shipping either of those without this change means a port that hands any host on the network free use of four provider subscriptions on the operator's credential, plus a remote switch that writes every request body — including the operator's own sessions — to disk. The alternatives considered and rejected: a firewall source restriction alone (real defence in depth, but it authenticates a network location rather than a caller, and does nothing about the admin endpoints once a permitted host is compromised — worth having *as well*, not *instead*); mTLS (stronger, disproportionate to operate for one laptop and a handful of callers); and keeping the router loopback-only forever while running a separate authenticated proxy in front of it (two processes to reason about, and the proxy would need exactly this key check anyway).

## Verification Result

**Verified:** 2026-08-17T18:49:44Z (HEAD 2a2983e)
**Binary:** /Users/bborbe/Documents/workspaces/go/bin/claude-code-router (v0.26.0, `make install` path; running PID 79798)
**Scenario:** Operator-executable ladder from ## Verification replayed live against the deployed router on 0.0.0.0:8788; structural ACs re-grepped; go test refresh
**Evidence:**
- Deploy gate: `grep -o 'version=[^ ]*' /tmp/claude-code-router.log | tail -1` → `version=v0.26.0`, equals `git describe --tags --abbrev=0` = v0.26.0; line belongs to running PID 79798 (verified via ps)
- LAN 192.168.177.164 keyless /v1/messages → 401, `auth rejected` +1; wrong key → 401, +1
- LAN correct key → `auth rejected` delta 0 (accepted & forwarded; upstream 401 is the OAuth-less body)
- Loopback keyless → `auth rejected` delta 0 (bypass)
- LAN /enabletrace /disabletrace /setloglevel/2 /gc → 403 each, `admin refused` +1 each; loopback same → 200/200/200/200
- Key-literal leak grep: log 0, repo 0; `subtle.ConstantTimeCompare` at pkg/handler/auth-middleware.go:40
- `go test ./pkg/...` → ok (pkg, factory, handler, reloader)
- AC17: feature documented under released v0.24.0/v0.25.0; `## Unreleased` cut by auto-release after merge (restoring it would falsify the changelog)
**Verdict:** PASS

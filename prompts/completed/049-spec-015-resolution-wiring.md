---
status: completed
spec: [015-global-default-provider-token]
summary: Wired spec-015 effective-token resolution (member token → global default_token → client passthrough) into the factory's per-upstream auth-swap construction, flipped the auth-swap/logging nesting so the V(3) [upstream.headers] line reflects the swapped outbound Authorization, added a 7-row full-path wiring test file (inherit/override/passthrough/pool-fallback/SIGHUP-rebuild + no-literal-key security evidence), and added the CHANGELOG Unreleased entry
execution_id: claude-code-router-global-token-exec-049-spec-015-resolution-wiring
dark-factory-version: dev
created: "2026-08-20T14:51:00Z"
queued: "2026-08-20T15:11:09Z"
started: "2026-08-20T15:28:34Z"
completed: "2026-08-20T15:34:33Z"
---

# Factory resolution: effective outbound token (member `token:` → global `default_token:` → client passthrough) + wiring tests

<summary>
- The factory resolves each upstream member's effective outbound token at wiring time: the member's own `token:` (which for the legacy single-`upstream:` form already carries the provider-level `token:` via `normalizeUpstreams` / `UpstreamList`) wins, else the top-level `cfg.DefaultToken`, else empty — empty keeps the auth-swap transport's no-op contract (client `Authorization` passes through byte-for-byte).
- The auth-swap / logging transport nesting is FLIPPED in the factory so the V(3) `[upstream.headers]` line reflects the SWAPPED outbound `Authorization` instead of the client's pre-swap header — the spec AC 2 / operator-rung evidence (the `<redacted len=N>` distinguishes the inheriting global key from an overriding provider key) requires it, and it matches the behavior the `logging-roundtripper.go` doc comment already claims.
- Wire behavior is proven through the REAL dispatch path to local `httptest` upstreams that record the received `Authorization` header: provider inherits global, provider token overrides global, neither → client header passes through unchanged, pool-member fallback (a `token:`-less member inherits global, a member with one uses its own), and a SIGHUP-rebuilt `CreateRouterFromConfig` forwards a changed `default_token:`.
- A negative-evidence row captures glog output at V(3)/V(4) and asserts the literal global key never appears — only `<redacted len=N>` — covering the spec's Security redaction invariant end to end.
- The existing no-op row in `pkg/handler/auth-swap-transport_test.go` and every existing wiring row stay green unchanged — the transport itself is untouched, only the factory's token argument and transport nesting change.
</summary>

<objective>
Wire the spec-015 effective-token resolution into the factory's per-upstream auth-swap construction (member `token:` → global `default_token:` → client passthrough, applied at wiring time, never from client input) and reorder the auth-swap / logging nesting so the V(3) `[upstream.headers]` line reflects the swapped outbound header — all proven by full-path wiring tests, including pool-member fallback, the SIGHUP-rebuilt tree, and the no-literal-key-in-log guarantee.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- Read `pkg/config.go` — the `Config` struct (now carries `DefaultToken string yaml:"default_token,omitempty"` from spec-015 prompt 1), the `Upstream` struct (`Token` field), `normalizeUpstreams` (the legacy single-`upstream:` form copies `prov.Token` onto the synthesized single member), and `Provider.UpstreamList()` (the programmatic fallback does the same). The upshot: by the time the factory iterates `upstreams := prov.UpstreamList()`, each member's `up.Token` already carries the provider-level token for legacy configs — so member-level resolution covers providers and pool members uniformly. Do NOT change any of this.
- Read `pkg/factory/factory.go` — `CreateRouterFromConfig`'s per-upstream wiring loop (currently lines ~216–264): the `handler.NewLoggingRoundTripper(handler.NewAuthSwapTransport(handler.DefaultProxyTransport(), up.Token), ...)` transport construction, the `handler.UpstreamMember{...}` literal, and the `for _, up := range upstreams` loop's `ctx.Done()` checks. This prompt changes only the token argument (resolution) and the transport nesting — nothing else in the loop.
- Read `pkg/handler/auth-swap-transport.go` — `NewAuthSwapTransport(next http.RoundTripper, token string) http.RoundTripper`: returns `next` unchanged when `token == ""` (the no-op/passthrough contract), else wraps with a per-request clone that sets `Authorization: Bearer <token>`. The transport is NOT changed by this prompt.
- Read `pkg/handler/logging-roundtripper.go` — `NewLoggingRoundTripper(inner http.RoundTripper, bodySampler liblog.Sampler, currentDateTime libtime.CurrentDateTimeGetter)`; its V(3) `[upstream.headers]` line emits `RedactHeadersForLog(req.Header)` (Authorization redacted to `<redacted len=N>` where N = the byte length of the joined value, i.e. `len("Bearer " + token)`). NOTE the doc comment already claims the line shows "the outbound request headers (after the auth-swap transport has applied its Authorization rewrite)" — the current factory nesting does NOT deliver that; this prompt fixes the nesting to match the comment.
- Read the shared test helpers: `pkg/factory/upstream_pool_wiring_test.go` (`poolUpstream` / `newPoolUpstream` — the blocking httptest harness that records each request's headers in `u.hdrs` under `u.hdrsMu`; `unblock` / `closeUpstream`), `pkg/factory/model_pool_wiring_test.go` (`poolSlot` / `sessionPinnedToSlot`), `pkg/factory/auth_middleware_wiring_test.go` (`isolatedRegistry()`, `lowerCaseKeys`), `pkg/factory/trace_wiring_test.go` (`captureStderr`). All are package-`factory_test` and reusable. IMPORTANT: `newMessagesRequest`, `sessionedRequest`, `serveAsync` are already PACKAGE-LEVEL declarations in `time_window_wiring_test.go` — use them directly from the new file; do NOT re-declare them (that would hit `newMessagesRequest redeclared`). Only `probePinnedTo` is Describe-local (not needed here).
- Read `pkg/factory/time_window_wiring_test.go` — the SIGHUP-rebuild pattern (two `CreateRouterFromConfig` calls = the reloader's rebuild) and the `flag.Set("v", ...)` + `captureStderr` + `serveAsync` + `Eventually(... inFlight)` log-capture pattern this prompt's negative-evidence row mirrors.
- Coding plugin docs (in-container paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md` — factory wiring conventions.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `errors.Wrapf` in the factory (this prompt adds no new error path — only the token argument changes — but preserves the existing wraps).
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — glog verbosity (V(3) `[upstream.headers]`, V(4) body samples).
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, `Eventually` with small explicit waits.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md` — goroutines allowed in `_test.go` (the `serveAsync` dispatch).
</context>

<requirements>
1. **Effective-token resolution + transport nesting in `pkg/factory/factory.go`** (spec DB 2, AC 2/3/4/5/6). In `CreateRouterFromConfig`'s per-upstream loop, replace the current transport construction (anchor: the `transport := handler.NewLoggingRoundTripper(` line) with:
   ```go
   // Effective outbound token (spec 015): the member's own token wins (for
   // legacy single-upstream configs normalizeUpstreams/UpstreamList already
   // copied the provider-level token onto this member), else the top-level
   // default_token, else empty — an empty effective token keeps the
   // auth-swap no-op contract (client Authorization passes through).
   token := up.Token
   if token == "" {
       token = cfg.DefaultToken
   }
   // Auth-swap OUTER, logging INNER: the V(3) [upstream.headers] line (inside
   // the logging roundtripper) must reflect the SWAPPED outbound
   // Authorization — the operator evidence of which effective token went out
   // (spec 015 AC 2, <redacted len=N> distinguishes the global key from an
   // override key). With logging outer the line would show the client's
   // pre-swap header instead. An empty token returns the logging transport
   // unchanged (passthrough), identical to today's no-op wiring.
   transport := handler.NewAuthSwapTransport(
       handler.NewLoggingRoundTripper(
           handler.DefaultProxyTransport(),
           liblog.SamplerList{
               liblog.NewSampleTime(time.Second),
               liblog.NewSamplerGlogLevel(5),
           },
           libtime.NewCurrentDateTime(),
       ),
       token,
   )
   ```
   Semantics to preserve exactly:
   - Resolution happens at WIRING time from config (`up.Token` / `cfg.DefaultToken`), never from per-request client input (spec DB 2, Security). There is no other read of the global default anywhere — do not pass it to the router, the pool handler, or the model pools (a model-pool member dispatches through its provider's handler, which already carries the effective-token auth-swap, so model pools need NO change).
   - The no-op-on-empty contract is unchanged: `NewAuthSwapTransport(next, "")` returns `next`, so a member with neither its own token nor a global default gets the logging transport wrapping `DefaultProxyTransport` with NO swap — the client's `Authorization` flows to the upstream byte-for-byte (spec AC 4, constraint "client passthrough stays byte-identical").
   - Do NOT touch anything else in the loop (the `ctx.Done()` checks, the `NewAnthropicProxyHandler` call, the concurrency-limiter construction, the `handler.UpstreamMember{...}` literal, the `caps` / `inflights` bookkeeping all stay byte-identical). Do NOT add a helper function, a config knob, a Prometheus metric, or a log line (spec Non-goals — the observability surface is the existing redacted V(3)/V(4) lines).
   - Do NOT change `NewAuthSwapTransport` or `NewLoggingRoundTripper` signatures; do NOT touch `main.go` / `pkg/cli.go` / `CreateServer` (no signature change; `CreateRouterFromConfig`'s signature is unchanged).

2. **New wiring test file `pkg/factory/default_token_wiring_test.go`** (package `factory_test`). Reuse the package-level `poolUpstream` / `newPoolUpstream` harness (it records each request's headers in `u.hdrs` under `u.hdrsMu` BEFORE blocking — so headers are assertable as soon as the member's in-flight counter rises), `sessionPinnedToSlot`, `isolatedRegistry`, `captureStderr`, and `lowerCaseKeys`. Use the package-level helpers from `time_window_wiring_test.go` directly — `newMessagesRequest(model string) *http.Request` (the `{"model":%q,...}` /v1/messages body), `sessionedRequest(id, model string) *http.Request` (`handler.ContextWithSessionID`), and `serveAsync(h, rec, req) chan struct{}` — do NOT re-declare them (package scope, would collide). Header assertions read `u.hdrs` under `u.hdrsMu` (the `hdrs := u.hdrs.Clone()` pattern from the "strips x-session-id" row). Each row that blocks on the harness must `unblock()` / rely on `AfterEach`'s `closeUpstream()` (declare the `a`/`b` variables + `BeforeEach`/`AfterEach` exactly like the sibling wiring files). Configs are programmatic struct literals (as in the sibling wiring tests) — set `DefaultToken` directly on `&pkg.Config{}`. Rows (each `It`):
   - **AC 2 — a provider without its own token inherits the global default:** `cfg := &pkg.Config{Router: pkg.Router{DefaultProvider: "inherit"}, DefaultToken: "global-key-123", Providers: map[string]pkg.Provider{"inherit": {Models: []string{"m*"}, Upstreams: []pkg.Upstream{{Upstream: a.url, Weight: 1}}}}}`. Dispatch `newMessagesRequest("m1")` through `factory.CreateRouterFromConfig(context.Background(), cfg, isolatedRegistry())` async; `Eventually` server A's in-flight is 1; read A's headers under the mutex and assert `hdrs.Get("Authorization")` equals `"Bearer global-key-123"`. Then `a.unblock()`, `Eventually(done)`, `Expect(rec.Code).To(Equal(http.StatusOK))`.
   - **AC 2 (V(3) evidence) — the `[upstream.headers]` line shows `<redacted len=N>` and never the literal key:** same config as the inherit row; with `flag.Set("v", "3")` + `flag.Set("logtostderr", "true")` (save/restore both process-global flags as the sibling rows do), run the dispatch inside `captureStderr` (dispatch async, `Eventually` A's in-flight is 1 to guarantee the V(3) line — emitted before the upstream blocks — landed inside the capture window, then unblock and wait for `done` inside the same closure). Assert the captured output contains `[upstream.headers]`, contains `fmt.Sprintf("Authorization\":\"<redacted len=%d>", len("Bearer "+globalKey))` (the JSON-encoded redacted map — `encoding/json` sorts map keys, so `Authorization` precedes `Content-Type`), and does NOT contain the literal `"global-key-123"`. (N = `len("Bearer "+key)` because `RedactHeadersForLog` redacts the full joined header value including the `"Bearer "` prefix.)
   - **AC 3 — a provider's own token overrides the global default:** `cfg` with `DefaultToken: "global-key-123"` and a provider whose single `Upstreams` entry carries `Token: "override-key-456"`. Dispatch → server A records `Authorization: "Bearer override-key-456"` (assert it is NOT the global key). 
   - **AC 4 — neither → client Authorization passes through unchanged:** `cfg` with NO `DefaultToken` and a token-less provider; build the request with a client header `req.Header.Set("Authorization", "Bearer client-oauth-abc")` (the model router's key routing is not involved — no `allowedApiKeys` configured — so the client Authorization is not the routing key and flows to the proxy). Dispatch → server A records `Authorization: "Bearer client-oauth-abc"` byte-for-byte. (The transport-level no-op row in `pkg/handler/auth-swap-transport_test.go` already covers the isolated transport; this row proves the full dispatch path.)
   - **AC 5 — pool-member fallback:** one provider `pool` with `Upstreams` = [member A: `{Upstream: a.url, Weight: 1}` (no token), member B: `{Upstream: b.url, Weight: 1, Token: "member-b-key"}`], `DefaultToken: "global-key-123"`, `Models: ["m*"]`. Pick `idA := sessionPinnedToSlot(0, 1, 1)` and `idB := sessionPinnedToSlot(1, 1, 1)` (the weighted-ring slot helpers mirror production, so these pin deterministically). Dispatch `sessionedRequest(idA, "m1")` → A records `"Bearer global-key-123"` (token-less member inherits the global); dispatch `sessionedRequest(idB, "m2")` → B records `"Bearer member-b-key"` (member with a token uses its own, overriding the global). Assert both headers, 200s, and that the other member saw zero requests.
   - **AC 6 — SIGHUP rebuild forwards a changed default_token:** `cfg1` with `DefaultToken: "token-v1"` and a token-less provider → `router1 := factory.CreateRouterFromConfig(context.Background(), cfg1, isolatedRegistry())`; dispatch → A records `"Bearer token-v1"`. Then `cfg2` — a SECOND `CreateRouterFromConfig` exactly mirrors the reloader's SIGHUP rebuild — same provider, `DefaultToken: "token-v2"` → `router2 := ...`; dispatch the same model → A records `"Bearer token-v2"`. Name the `It(...)` so its description contains the word "rebuilt" (the sibling spec AC 6 evidence greps `rebuilt`). Assert both headers and 200s.
   - **Security — no literal key in captured log output (negative evidence):** within the inherit row's capture (or a dedicated row with `flag.Set("v", "5")` to also exercise the V(4) body-sample path), assert the captured stderr does NOT contain the literal global key string, and every `Authorization` occurrence is `"<redacted"`.

   IMPORTANT: this file MUST contain at least one occurrence of the literal `default_token` (the self-check below is `grep -c 'default_token' pkg/factory/*_test.go` ≥ 1 — the struct-literal `DefaultToken:` and `cfg.DefaultToken` reads do NOT match it, since the grep is case-sensitive and requires the underscore). Guarantee the match by naming the AC 6 row's `It` description "SIGHUP rebuild forwards a changed default_token" (which the spec AC 6 / reloader evidence also greps for "rebuilt").
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Resolution order is FROZEN: provider/upstream `token:` (WINS) → global `default_token:` → client `Authorization` passthrough. There is NO per-provider opt-out flag to force passthrough while a global default is set — a provider wanting a different key declares its own `token:` (spec Constraints, Non-goals, Assumptions). Do NOT add any flag, knob, metric, or log line.
- Resolution happens at WIRING time from config, never from client-controlled per-request state (spec DB 2, Security: "a client cannot influence which token the router sends"). The global default is read only in the per-upstream loop; do NOT pass it to the model router, pool handler, or model pools (a model-pool member already dispatches through a provider handler carrying the effective-token auth-swap).
- The no-op-on-empty contract of the auth-swap transport is UNCHANGED — client passthrough stays byte-identical; the existing rows in `pkg/handler/auth-swap-transport_test.go` stay green with zero edits (spec Constraints). The transport nesting flip is behaviorally neutral on the wire (same `Authorization` reaches the upstream) — it only changes what the V(3) log line reflects, which the spec AC 2 / operator rung require.
- The transport nesting flip (auth-swap OUTER, logging INNER) is REQUIRED by the spec's evidence: AC 2 asserts the V(3) line shows the swapped `Authorization` as `<redacted len=N>` and the operator rung distinguishes the inheriting key's `len` from an overriding key's — neither is satisfiable while logging is outer and sees the client's pre-swap header. The flip also aligns the factory with the `logging-roundtripper.go` doc comment. There is no test today depending on the old nesting (`[upstream.headers]` is referenced only in `logging-roundtripper_test.go`, which builds its own chain), so the existing suite validates the flip for free.
- Do NOT touch `pkg/config.go` (prompt 1 shipped the field; no resolution belongs in config), `pkg/handler/` (auth-swap transport and logging roundtripper are unchanged), `main.go`, `pkg/cli.go`, or `docs/`/`CHANGELOG.md` (prompt 3) in this prompt.
- Tests follow the repo's Ginkgo convention and must not depend on real wall-clock time or real waits — `Eventually` with small explicit waits ("1s", "10ms") exactly as the sibling wiring tests (spec Constraints "tests must not depend on real 30s waits").
- Do NOT add Prometheus metrics or config knobs — the observability surface is the existing redacted `[upstream.headers]` (V(3)) / `[upstream.req.body]` / `[upstream.resp.body]` (V(4)) lines (spec Non-goals; the 039-style metric-invention incident is a hard-reject precedent).
- No AI attribution in code or comments. `make precommit` must remain green — run it before declaring done. Follow `docs/dod.md`.
</constraints>

<verification>
make precommit

# Resolution + nesting landed (effective-token branch + auth-swap-outer construction):
grep -n 'token := up.Token\|token = cfg.DefaultToken\|NewAuthSwapTransport(' pkg/factory/factory.go

# AC 2 evidence — factory tests reference default_token:
grep -c 'default_token' pkg/factory/*_test.go   # expect >=1
grep -c 'rebuilt' pkg/factory/default_token_wiring_test.go   # expect >=1 (AC 6 row name)

# Focused rows pass:
go test -count=1 ./pkg/factory/ -ginkgo.focus='default'

# Full suite (including the untouched auth-swap no-op row):
go test -mod=mod -count=1 ./pkg/handler/...
go test -mod=mod -count=1 ./pkg/factory/...
go test -mod=mod -count=1 ./pkg/...
</verification>

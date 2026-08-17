---
status: approved
spec: ["010"]
created: "2026-08-17T19:12:47Z"
queued: "2026-08-17T19:50:37Z"
---

# Routing by key in model-router: key match wins over model globs

<summary>
- A request whose presented `x-api-key` is listed in a provider's `allowedApiKeys` is dispatched to that provider (its outbound token), skipping model-glob selection entirely — key match wins over globs.
- A request with a key that is in the auth registry but claimed by no provider routes exactly like a keyless request — glob matching then `default_provider`.
- A request with no key routes byte-for-byte as it does today — glob matching then `default_provider`, body and non-router-managed headers forwarded verbatim.
- The presented key travels from the auth middleware to the router via request context (prompt 2's seam), so the router never re-parses or logs the header.
- Duplicate key claims are already rejected at config load (prompt 1), so at most one provider can claim any presented key — no runtime ambiguity.
</summary>

<objective>
Make the model router consult the authenticated key before glob matching, so two providers sharing a model glob can split traffic by the caller's API key and its quota.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- Read `pkg/handler/model-router.go` — the `ModelRoute` struct (line 40), `NewModelRouter` (line 89), the `ProviderName`/`target` resolution loop (lines 199-222), and the `defaultProviderName` seeding for `requiresLeadingSystem` (lines 205-211). The key-routing branch is inserted before that glob walk.
- Read `pkg/handler/presented-api-key.go` — prompt 2 added `ContextWithPresentedApiKey` / `PresentedApiKeyFromContext`. The router reads the key via `PresentedApiKeyFromContext(r.Context())`; unit tests inject via `ContextWithPresentedApiKey`.
- Read `pkg/factory/factory.go` — the route-building loop (lines 157-164) where each `handler.ModelRoute` is appended. It gains `AllowedApiKeys: prov.AllowedApiKeys`.
- Read `pkg/handler/model-router_test.go` — the `labelHandler` helper (line 40), the `routes` fixture (line 78), and the `[req]`/`[route]` log assertions. The key-routing unit tests follow this shape.
- Read `pkg/factory/system_lift_wiring_test.go` — the existing single-provider integration-test shape (`factory.CreateRouterFromConfig` + `httptest.NewServer` upstream). Requirement 5's new Describe adds the second provider and second upstream for AC 3's "two providers sharing a glob" case.
- Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `bborbe/errors`, never `fmt.Errorf` (the router's existing error paths use `bberrors.Wrapf`).
- Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — lowercase `[route]`/`[req]` log line conventions, `V(n)` gating.
- Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — external `_test` package, Ginkgo v2 + Gomega.
- Read `docs/dod.md` — Definition of Done.
</context>

<requirements>
1. Add an `AllowedApiKeys` field to the `ModelRoute` struct in `pkg/handler/model-router.go`, after `RequiresLeadingSystem` (line 50). Exact shape:

   ```go
   // AllowedApiKeys carries this route's provider's `allowedApiKeys`
   // config list. When the request's presented x-api-key (from the auth
   // middleware's context) is in one of these lists, the router dispatches
   // to that provider directly — the key is an explicit override that wins
   // over model-glob matching. Because Config.Validate rejects a key
   // claimed by more than one provider, at most one route ever matches a
   // presented key. Nil or empty (the default for every route built from a
   // config that omits the field) means: this provider claims no keys, so
   // it is only reachable via glob routing.
   AllowedApiKeys []string
   ```

2. **In `NewModelRouter`'s handler, insert the key-routing branch immediately before the existing glob walk** (i.e. after the `defaultProviderName` seeding loop at lines 205-211 and before the `for _, route := range routes { ok, _ := path.Match(...) }` loop at line 212). Read the key from context once:

   ```go
   matchedByKey := false
   if presentedKey := PresentedApiKeyFromContext(r.Context()); presentedKey != "" {
       for _, route := range routes {
           if containsString(route.AllowedApiKeys, presentedKey) {
               providerName = route.ProviderName
               target = route.Handler
               requiresLeadingSystem = route.RequiresLeadingSystem
               glog.V(2).Infof("[route] key matched provider=%s", providerName)
               matchedByKey = true
               break
           }
       }
   }
   for _, route := range routes {
       if matchedByKey {
           break
       }
       ok, _ := path.Match(route.Pattern, model)
       if ok {
           providerName = route.ProviderName
           target = route.Handler
           requiresLeadingSystem = route.RequiresLeadingSystem
           glog.V(2).Infof("[route] model=%q matched %q -> provider=%s", model, route.Pattern, providerName)
           break
       }
   }
   ```

   - `containsString` is a small package-private helper (`func containsString(list []string, s string) bool`) — a plain linear scan over the tiny per-provider list. Plain string comparison is CORRECT here: this runs AFTER the auth gate has already validated the key in constant time (prompt 2), so no timing boundary remains; do NOT use `crypto/subtle` in the router.
   - When a key matches, `providerName`, `target`, and `requiresLeadingSystem` are all set from the matched route and `matchedByKey` is true; the glob walk is gated on `!matchedByKey`, so the key's dispatch is final — a glob can never re-select over a key match (spec DB 3). Keep the `matchedByKey` gate OUTSIDE the per-route match condition (the top-of-loop `break` exits on the first iteration, so the walk is skipped wholesale); do NOT check it only after `path.Match` succeeds.
   - `requiresLeadingSystem` seeding from the default provider (lines 205-211) stays for the keyless/glob path; the key-match branch overwrites it with the matched route's value, so system-lift behavior follows the KEY-selected provider (spec DB 3: the key pins the provider and everything provider-scoped follows it).
   - The key itself is never logged — the `[route]` line logs only `provider=<name>`. It is never written to the request body.

3. **Factory wiring in `pkg/factory/factory.go`:** in the route-building loop (lines 157-164), populate the new field on every `handler.ModelRoute`:

   ```go
   for _, pattern := range prov.Models {
       routes = append(routes, handler.ModelRoute{
           Pattern:               pattern,
           ProviderName:          name,
           Handler:               proxy,
           RequiresLeadingSystem: prov.RequiresLeadingSystem,
           AllowedApiKeys:        prov.AllowedApiKeys,
       })
   }
   ```

   All routes of one provider share the same handler and the same key list, so key matching against any of them resolves to the same dispatch — correct.

4. **Unit tests in `pkg/handler/model-router_test.go`** (package `handler_test`, Ginkgo v2 + Gomega). Extend the existing `routes` fixture so TWO providers share one glob, e.g. add `deepseek-*` matching both `seibert-vllm` (first) and `seibert-dark-factory` (second), and give only the second provider `AllowedApiKeys: []string{"dark-factory-key"}`. Build requests with `httptest.NewRequest` and inject the key via `handler.ContextWithPresentedApiKey(r.Context(), key)` — do NOT run the middleware in unit tests. Cases:
   - **AC 3 — key wins over globs:** body `{"model":"deepseek-v4-pro"}` with context key `dark-factory-key` (claimed only by the SECOND provider whose glob is later in declaration order) → the response body is the second provider's label, proving the key overrode the glob that would have selected the first provider.
   - **AC 3 — a key claimed by a provider whose glob would NOT match the model:** body `{"model":"claude-opus-4-7"}` (would glob-route to a different provider — `anthropic-subscription` via `claude-*` — not the key-holder's provider) with context key `dark-factory-key` → still the second provider's label (the key wins regardless of the model name).
   - **AC 4 — valid-but-unclaimed key routes by glob:** context key `registry-only-key` present but claimed by no provider → the request routes exactly as the keyless case does: glob match for `deepseek-v4-pro` → first provider's label.
   - **AC 5 — keyless unchanged:** no context key → `deepseek-v4-pro` → first provider's label (the glob-selected one); no `[route] key matched` log line (assert via `captureStderr` that no `key matched` line appears, and the existing `[req]` line still names the glob-selected provider).
   - **No provider claims keys at all:** a router built entirely without `AllowedApiKeys` behaves byte-for-byte as before — a key in context is ignored (glob routing), and a keyless request is unchanged.
   - **Key match does not disturb the model label / metrics path:** assert the existing `ccrouter_requests_total`-shape metric observation still fires with the KEY-selected `providerName` label for a key-matched request (follow the existing metric-observation assertion pattern in `pkg/handler/model-router_test.go`).

5. **Integration tests — ACs 3, 4, 5** in a new Describe in `pkg/factory/auth_middleware_wiring_test.go` (package `factory_test`, the same shape as the existing suite: `factory.CreateRouterFromConfig` + `httptest.NewRequest` + `mux.ServeHTTP` + two `httptest.NewServer` upstreams). Build a config with two providers sharing a glob (e.g. both `models: ["deepseek-*"]`), provider A token-less, provider B with `token: "b-token"` and `AllowedApiKeys: ["dark-factory-key"]`, top-level `AllowedApiKeys: ["dark-factory-key", "registry-only-key"]` (so the auth gate accepts both — spec DB 2: a presented key must be in the auth set to pass the gate), provider B claims only `["dark-factory-key"]`. AC 3 uses the claimed key, AC 4 the unclaimed `registry-only-key`. Each upstream server records `requestCount`, the received body, and the received `Authorization`:
   - **AC 3:** non-loopback request with `x-api-key: dark-factory-key` and body `{"model":"deepseek-v4-pro"}` → the request arrives ONLY at provider B's server (A's `requestCount` is 0), with B's `Authorization: Bearer b-token`, and the received body equals the sent body.
   - **AC 4:** non-loopback request with a key in the top-level registry but claimed by NO provider (top-level registry has two keys, B claims only one) and body `{"model":"deepseek-v4-pro"}` → arrives at the glob-selected provider A with the same body as the keyless case.
   - **AC 5:** loopback request (`req.RemoteAddr = "127.0.0.1:54321"`) with NO `x-api-key` and body `{"model":"deepseek-v4-pro"}` → arrives at provider A (glob-selected) with the identical body bytes; the auth layer's strip runs on the loopback bypass path too, so the received header map, lower-cased, has no `x-api-key` entry (0 matches). (A non-loopback keyless request against a non-empty registry is a 401 by spec DB 2 / AC 6 and belongs to prompt 2's gate tests, not here.)
   - Use `httptest.NewServer` ONLY for the upstreams; the client is always `httptest.NewRequest` served via `mux.ServeHTTP` (never `httptest.NewServer` for the client — a real loopback listener would defeat the non-loopback gate). Set `req.RemoteAddr` per case: `"10.0.0.1:12345"` for the non-loopback AC 3 / AC 4 cases, `"127.0.0.1:54321"` for the loopback AC 5 case. Assert on BOTH providers' `requestCount` to prove the request never reached the wrong server.

6. Do NOT change `docs/` or `CHANGELOG.md` in this prompt — prompt 4 owns them. Do NOT touch the auth middleware or the trace middleware — prompt 2 landed those.
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Do NOT change the model-glob routing contract for keyless requests, nor the `default_provider` fallback (spec Non-goals). The key branch is additive: it only fires when a presented key is in a provider's list.
- Do NOT use `crypto/subtle` in the router — the auth gate already did the constant-time check; routing membership is a plain string contains (spec AC 10's constant-time evidence is scoped to `pkg/handler/auth-middleware.go`).
- Do NOT log the presented key value at any level — the `[route]` line carries the provider name only.
- Do NOT add per-model keys, key rotation machinery, or a config knob (spec Non-goals). The config surface from prompt 1 is the only input.
- Use `github.com/bborbe/errors` for any error wrapping. Never `fmt.Errorf`.
- `glog` log messages: lowercase, `V(2)` gated for the `[route] key matched` detail line (consistent with the existing `[route]`/`[alias]` detail lines).
- Do NOT add a new external dependency. Stdlib and existing deps are sufficient.
- `make precommit` must remain green.
</constraints>

<verification>
make precommit

# New field on ModelRoute:
grep -n 'AllowedApiKeys \[\]string' pkg/handler/model-router.go

# Key branch reads the context seam and dispatches:
grep -n 'PresentedApiKeyFromContext\|\[route\] key matched' pkg/handler/model-router.go

# Factory populates the field per route:
grep -n 'AllowedApiKeys:        prov.AllowedApiKeys' pkg/factory/factory.go

# No crypto/subtle leaked into the router (auth gate only):
grep -rn 'subtle\.' pkg/handler/model-router.go   # expect 0 lines

# Full suite:
go test ./pkg/... -count=1
</verification>

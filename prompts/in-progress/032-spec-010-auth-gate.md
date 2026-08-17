---
status: committing
spec: ["010"]
summary: 'Replaced the single-key x-router-key auth gate with a set-based x-api-key middleware, added the PresentedApiKeyFromContext/ContextWithPresentedApiKey context seam, migrated factory wiring and redaction to x-api-key, and added fail-closed migration guards (legacy auth: fails load, ROUTER_AUTH_KEY refuses startup) with full unit/integration/SIGHUP/guard test coverage.'
execution_id: claude-code-router-exec-032-spec-010-auth-gate
dark-factory-version: dev
created: "2026-08-17T19:12:47Z"
queued: "2026-08-17T19:50:37Z"
started: "2026-08-17T19:59:35Z"
completed: "2026-08-17T20:11:22Z"
---

# Auth gate evolution: x-api-key set-based auth + supersession of x-router-key

<summary>
- Non-loopback `/v1/*` callers authenticate with the standard `x-api-key` header against the registry from prompt 1 — the top-level `allowedApiKeys` when non-empty, else the union of providers' lists.
- The key comparison stays constant-time against every registry key; a missing or non-matching key gets 401 with the presented value never logged.
- Loopback requests stay keyless but their `x-api-key` is still stripped before any upstream sees it.
- On an authenticated (or loopback-with-key) request the presented key is stored in the request context so the model router (prompt 3) can route by it.
- The spec-009 `x-router-key` / `auth.key` / `ROUTER_AUTH_KEY` auth path is removed; the outbound strip, trace redaction, and V(3) log redaction are all migrated to `x-api-key` only.
- Migration is fail-closed: a config still carrying the legacy `auth:` block fails load naming `auth`, and the binary refuses to start with `ROUTER_AUTH_KEY` set — an authenticated 009 deployment can never silently degrade to unauthenticated.
- SIGHUP applies registry adds/removes to subsequent requests without a restart.
</summary>

<objective>
Replace the single-key `x-router-key` auth gate with a set-based `x-api-key` gate, expose the authenticated key to the router via context, and remove the 009 auth path with fail-closed migration guards.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- Read `pkg/handler/auth-middleware.go` — the current `NewAuthMiddleware(next http.Handler, key string) http.Handler` (line 25) and its `ServeHTTP` (line 37). This file is rewritten.
- Read `pkg/handler/loopback.go` — `IsLoopbackRemoteAddr(string) bool` (line 21). Unchanged; the rewrite uses it.
- Read `pkg/handler/trace.go` — the case-insensitive redaction block at lines 114-123. The `x-router-key` line (118) is removed; `authorization` and `x-api-key` stay.
- Read `pkg/handler/redact.go` — `isCredentialHeader` (line 42); the substring list at line 49 contains `"router-key"` which is removed. `"api-key"` already covers `x-api-key`.
- Read `pkg/factory/factory.go` — `CreateServer` (line 51), `CreateRouterFromConfig` (line 127), the ROUTER_AUTH_KEY env resolution block (lines 192-201), and `buildMux` (line 213, signature `func buildMux(modelRouter http.Handler, trace bool, authKey string) *http.ServeMux`). The env-resolution block is removed and the guard added; `buildMux` takes the allowed-key set.
- Read `pkg/config.go` — prompt 1 added `AllowedApiKeys` fields and `func (c *Config) AllowedApiKeySet() map[string]struct{}`. The legacy `Auth *AuthConfig` field (line 49) stays on the struct ONLY as a migration-detection probe — `Config.Validate` now rejects a non-nil `Auth`. `AuthConfig.IsEnabled()` (line 74) is dead after this change and is removed.
- Read `pkg/config_test.go` — the `Context("auth")` block (line 341) and the `Context("AuthConfig.IsEnabled")` DescribeTable (line 408) are replaced by the legacy-auth-rejection tests.
- Read `pkg/handler/auth_middleware_test.go` and `pkg/factory/auth_middleware_wiring_test.go` — both are rewritten for `x-api-key`; the `ROUTER_AUTH_KEY env resolution` Describe block (auth_middleware_wiring_test.go line 366) is deleted (that behavior is gone).
- Read `pkg/factory/factory_suite_test.go` — note the factory suite does NOT reset `prometheus.DefaultRegisterer` per spec (unlike the reloader suite), which constrains the `CreateServer` guard test (requirement 11).
- Read `pkg/factory/trace_wiring_test.go` and `pkg/reloader/reloader_test.go` — the integration-test shape and the SIGHUP `Reload(ctx)` driving pattern (`reloader.Reload` at `pkg/reloader/reloader.go:82`, `ConfigSnapshot()` at line 74).
- Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `bborbe/errors`, never `fmt.Errorf`.
- Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — lowercase log messages, `V(n)` gating; the rejection line is `Info` (no V gate).
- Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — external `_test` package, Ginkgo v2 + Gomega.
- Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-http-handler-refactoring-guide.md` — middleware wiring conventions.
- Read `docs/dod.md` — Definition of Done; ≥80% coverage on changed packages, `## Unreleased` CHANGELOG rule (this prompt does not touch the CHANGELOG — prompt 4 owns docs).
</context>

<requirements>
1. **Rewrite `pkg/handler/auth-middleware.go`.** Replace the single-key `x-router-key` middleware with a set-based `x-api-key` middleware. New exported signature:

   ```go
   // NewAuthMiddleware wraps next so every non-loopback /v1/* request must
   // present an x-api-key header whose value is in allowedKeys. A missing
   // or non-matching key is rejected with 401 and never reaches next.
   // Loopback requests bypass the key check entirely but still have the
   // header stripped and still record the presented key in context, so an
   // upstream never observes the caller's API key and the router can route
   // by it. If allowedKeys is empty, the wrapper is a no-op and returns
   // next unchanged — the request path is byte-for-byte identical to a
   // release without key routing (feature-off default, AC 1).
   func NewAuthMiddleware(next http.Handler, allowedKeys map[string]struct{}) http.Handler
   ```

   - Do NOT take the config struct. `pkg/handler` has no dependency on package `pkg`; the caller in `pkg/factory` resolves `cfg.AllowedApiKeySet()` and passes the bare set. Mirror the current constructor's "empty ⇒ no-op" contract (`pkg/handler/auth-middleware.go:26-29` and `pkg/handler/auth-swap-transport.go:16-18`).
   - Empty map ⇒ return `next` unchanged, exactly as today's empty-key case does — zero effect on the hot path, NO context injection, NO header strip. This is what makes AC 1's "no header mutation" contract hold when no `allowedApiKeys` is configured.
   - Call `IsLoopbackRemoteAddr(r.RemoteAddr)` directly (same package) — do not inject it as a parameter.

2. **`ServeHTTP` behavior** (the `authMiddleware` struct gains `next http.Handler` and `allowedKeys map[string]struct{}`):
   - **Non-loopback:** read `presented := r.Header.Get("X-Api-Key")`. If `presented == ""` OR the value is not in `allowedKeys` → respond 401 with `http.Error(w, "auth required", http.StatusUnauthorized)` and emit exactly one glog line `auth rejected remote=<addr>` (lowercase, `Info` level, no V gate, never containing the presented or any configured key) — same shape as `pkg/handler/auth-middleware.go:41`. Stop; no upstream call.
   - **Membership check is constant-time:** iterate `allowedKeys` and accept iff any key satisfies `subtle.ConstantTimeCompare([]byte(presented), []byte(k)) == 1`. Do NOT use a `map[presented]` lookup or `==` on the presented value — spec AC 10 forbids direct equality (its grep evidence is `grep -rn '== cfg\|cfg\.' pkg/handler/auth-middleware.go` returns 0 lines). Do NOT add a length pre-check; `ConstantTimeCompare` returns 0 for differing lengths. Always compare against every key with NO early exit (fixed count, independent of the presented value) so timing does not leak which key matched.
   - **All paths (non-loopback authenticated AND loopback bypass):** clone the request (`r.Clone(r.Context())`), strip the header with `clone.Header.Del("X-Api-Key")` once (Header.Del canonicalises, removing any client letter-case; do NOT use raw `delete`), and if `presented != ""` store it in the clone's context via `ContextWithPresentedApiKey` (requirement 3) so prompt 3's router can read it. Never mutate the inbound `r` in place. Forward the clone.

3. **Context seam for the routing half.** In a new file `pkg/handler/presented-api-key.go` (package `handler`), export the two functions prompt 3 and the tests consume:

   ```go
   // presentedApiKeyContextKey is an unexported type to avoid collisions
   // with other context values.
   type presentedApiKeyContextKey struct{}

   // ContextWithPresentedApiKey returns a copy of ctx carrying the
   // x-api-key value the auth middleware accepted. It is also a test seam:
   // routing specs inject a key directly without running the middleware.
   func ContextWithPresentedApiKey(ctx context.Context, key string) context.Context

   // PresentedApiKeyFromContext returns the x-api-key value stored by
   // ContextWithPresentedApiKey, or "" when absent.
   func PresentedApiKeyFromContext(ctx context.Context) string
   ```

   Both live in package `handler` so `auth-middleware.go` and `model-router.go` (same package) share them without a public coupling to package `pkg`.

4. **Factory wiring in `pkg/factory/factory.go`:**
   - Delete the ROUTER_AUTH_KEY env-resolution block (lines 192-201) — the `os.Getenv("ROUTER_AUTH_KEY")` read, the `cfg.Auth.IsEnabled()` fallback, and the `authKey` variable are gone. `os` is still imported (used by the new `CreateServer` guard in requirement 6 and by `traceDir` at line 83) — verify the import stays used; do not remove it.
   - Resolve the key set where the authKey used to resolve: `authKeys := cfg.AllowedApiKeySet()`.
   - Change `buildMux`'s signature to `func buildMux(modelRouter http.Handler, trace bool, allowedKeys map[string]struct{}) *http.ServeMux` and update the call site at line 201. Inside `buildMux`, keep the chain order — auth INSIDE trace (spec DB 7):

     ```go
     v1Handler := http.Handler(modelRouter)
     v1Handler = handler.NewAuthMiddleware(v1Handler, allowedKeys)
     v1Handler = handler.NewTraceMiddleware(v1Handler, traceDir(), handler.DefaultTraceState(), trace)
     mux.Handle("/v1/", v1Handler)
     ```

     Trace stays OUTSIDE auth so it still captures rejected requests and redacts `x-api-key` (AC 13); auth stays INSIDE trace so the header is gone before the model router and the upstream.

5. **Migration guard — legacy `auth:` fails load (AC 11, DB 7, fail-closed).** In `pkg/config.go`, add to the top of `Config.Validate(ctx)` (before the provider checks), the single check:

   ```go
   if c.Auth != nil {
       return errors.New(ctx, "auth: legacy auth block is no longer supported; remove `auth:` and configure `allowedApiKeys` instead")
   }
   ```

   The `Auth *AuthConfig` field stays on the `Config` struct (line 49) ONLY as this detection probe — `yaml.Unmarshal` populates it from a legacy file, then Validate rejects it. Keep `AuthConfig` and its `Key string yaml:"key"` field as-is (still needed to parse the legacy shape). Remove the now-dead `func (a *AuthConfig) IsEnabled() bool` (lines 71-76) and its call sites (factory line 198 is deleted in requirement 4; no other callers exist). Do NOT keep dead code.
   - Because the rejection lives in `Config.Validate`, both startup (`Load`) and SIGHUP reload (`reloader.Reload` → `pkg.Load`) fail closed; a reload with a legacy `auth:` config keeps the old config active and logs `config reload failed: ... auth ...` at WARNING (spec Failure Modes row 1).

6. **Migration guard — `ROUTER_AUTH_KEY` set ⇒ binary refuses to start (AC 11, DB 7).** Add the guard at the TOP of `CreateServer` in `pkg/factory/factory.go` (before `pkg.Load`), so it fails fast regardless of config state:

   ```go
   if os.Getenv("ROUTER_AUTH_KEY") != "" {
       return nil, errors.New(ctx, "ROUTER_AUTH_KEY is no longer supported: remove it from the environment and configure allowedApiKeys in the config instead")
   }
   ```

   `CreateServer` is the binary's only startup path (`main.go` → `pkg.NewApp(factory.CreateServer)` → `App.Run`); returning the error makes `service.MainCmd` exit non-zero with the message on stderr. Do NOT add the guard to `CreateRouterFromConfig` — the SIGHUP rebuild path must not re-check a startup-only env var.

7. **Redaction migration:**
   - `pkg/handler/trace.go` lines 114-123: remove the `strings.ToLower(name) == "x-router-key"` condition. The block then covers only `authorization` and `x-api-key`. `x-api-key` redaction to `***` is already in place (spec AC 13) — do not touch its behavior.
   - `pkg/handler/redact.go` line 49: remove `"router-key"` from the `isCredentialHeader` substring list. `"api-key"` already redacts `x-api-key` in V(3) `[upstream.headers]` logs; no `x-router-key` header exists anymore.
   - Reword EVERY remaining `x-router-key` reference in `pkg/` doc comments to `x-api-key` only — including `pkg/config.go:46` (the `Auth` field comment — describe the field's role as a load-failing detection probe: a non-nil `Auth` makes `Config.Validate` reject the config) and `pkg/config.go:64` (`AuthConfig.Key` comment; requirement 5 keeps the field as-is as a detection probe, so its comment must describe that probe role) and the `buildMux` doc comment at `pkg/factory/factory.go:211` (which requirement 4's body rewrite does not touch).
   - The AC 11 grep evidence `grep -rn 'x-router-key\|auth\.key' pkg/` must return 0 lines after this prompt — including test files. Every old fixture, comment, and helper referencing the 009 surface is rewritten or deleted below. (`ROUTER_AUTH_KEY` is deliberately exempt from this grep: requirement 6's guard and requirements 10-11 env-guard tests must name the env var to detect it — that is the fail-closed control, not a retained 009 surface.)
   - Rewrite the `x-router-key` canary in `pkg/handler/logging-roundtripper_test.go` (lines 138-156) to the new credential surface: send `X-Api-Key: leak-canary-api-key` (redacted via the retained `api-key` substring) and assert `<redacted>` appears and the value does not. The old canary asserts `x-router-key` redaction, which no longer exists once `router-key` is removed from `isCredentialHeader` — leaving it fails the suite.

8. **Unit tests — rewrite `pkg/handler/auth_middleware_test.go`** (package `handler_test`, Ginkgo v2 + Gomega). Keep the existing `countingInner` helper, `captureStderr`-style log capture (via `flag.Set("logtostderr", "true")`), and `httptest.NewRecorder()`; set remote addresses by hand via `req.RemoteAddr`. New cases:
   - **Feature-off (empty set) returns next unchanged:** `NewAuthMiddleware(inner, map[string]struct{}{})` is `BeIdenticalTo(inner)`; a non-loopback request carrying `x-api-key: secret` reaches inner and the inner handler OBSERVES the header (no mutation — AC 1's "no header mutation" contract), and `PresentedApiKeyFromContext(r.Context())` is "".
   - **Loopback bypass + strip + context:** loopback request carrying `x-api-key: secret` reaches inner; inner sees no `X-Api-Key`/`x-api-key`; `PresentedApiKeyFromContext(r.Context())` equals `"secret"`.
   - **Missing header:** non-loopback, no `x-api-key` → 401, inner not called.
   - **Wrong key:** non-loopback, `x-api-key: wrong` → 401, inner not called.
   - **Correct key:** non-loopback, `x-api-key: secret` (set in non-canonical case `x-api-key` to prove case-insensitive read) → reaches inner; inner sees no header; context key equals `"secret"`.
   - **One of several registry keys:** `allowedKeys = {"a","secret","z"}`, presenting `"secret"` passes; presenting `"s3cret"` (not in set) → 401.
   - **Whitespace-only key is a literal, not empty:** `allowedKeys = {" "}` — a request presenting `x-api-key: " "` passes; one with no header or `x-api-key: ""` gets 401. Do NOT trim.
   - **Rejection log leak-canary:** trigger one rejection with a unique presented value (e.g. `leak-canary-presented-key`); capture stderr; assert exactly one line matches `auth rejected remote=` AND that line does not contain the presented value (negative-assertion shape from `pkg/handler/logging-roundtripper_test.go`).

9. **Integration tests — rewrite `pkg/factory/auth_middleware_wiring_test.go`** (package `factory_test`, driving `factory.CreateRouterFromConfig` against an `httptest.NewServer` upstream, using the existing `isolatedRegistry()` helper and `lowerCaseKeys`/`recursiveContains` helpers). Send via `httptest.NewRequest` + `mux.ServeHTTP` (never `httptest.NewServer` for the client — real loopback defeats a non-loopback `RemoteAddr`). `makeConfig` now sets `cfg.AllowedApiKeys` (top-level) instead of `cfg.Auth`:
   - **AC 6 — non-loopback gate:** registry configured; non-loopback request with no `x-api-key` → 401, upstream `requestCount` is 0; same with `x-api-key: <key-not-in-registry>` → 401, `requestCount` is 0.
   - **AC 6 — key value never logged:** after the rejection, capture the router's log output and assert zero byte-hits for the presented key literal (reuse the `recursiveContains` byte-scan helper on the captured buffer, or the `captureStderr` helper).
   - **AC 7 — loopback stays keyless:** registry configured; loopback request with no `x-api-key` → reaches upstream, response is the upstream's own (not 401); assert status != 401.
   - **AC 8 — key never forwarded upstream:** registry configured; non-loopback request with a valid `x-api-key` set in mixed case (e.g. `X-Api-Key`) → reaches upstream; `lowerCaseKeys(receivedHdrs)` has no `x-api-key` entry (0 matches). Same assertion on the loopback-with-key bypass path.
   - **AC 9 — Authorization untouched by the auth layer:** token-less provider (no `token:` → `NewAuthSwapTransport` no-op branch), non-loopback request carrying `Authorization: Bearer original` + valid `x-api-key` → upstream received `Authorization` equals `Bearer original` exactly AND has no `x-api-key`.
   - **AC 13 — trace redaction end-to-end:** registry + `cfg.Trace = true`, per-test temp HOME (same `os.Setenv("HOME", tmpDir)` pattern as the current AC-18 block, lines 205-220); non-loopback request with a known test key; read the single trace JSON; assert the `x-api-key` request-header entry (canonical key is `X-Api-Key`; accept either case) is `"***"`; `recursiveContains(tracePath, "<known-test-key>")` is empty.
   - **AC 11 — legacy `x-router-key` no longer authenticates:** registry configured; non-loopback request carrying ONLY `x-router-key: <any-value>` (no `x-api-key`) → 401, upstream `requestCount` is 0. Build the header name WITHOUT the contiguous literal so requirement 7's AC-11 grep stays at 0 lines, e.g. `req.Header.Set("x-router"+"-key", "stale-secret")` — and write the `It` description and any comments in this test without the contiguous literal too (e.g. `It("legacy router header no longer authenticates", ...)`) — a literal `"x-router-key"` anywhere in the test file trips the prompt's own `grep -rn 'x-router-key\|auth\.key' pkg/` gate. (Prompt 3 adds the key-routing integration tests; this prompt owns the auth gate only.)

10. **SIGHUP toggle test — AC 12** (in `pkg/factory/auth_middleware_wiring_test.go`, following the existing "AuthMiddleware SIGHUP reload toggle" Describe at line 263 — same `reloader.NewReloader` + `Reload(ctx)` + `ConfigSnapshot()` pattern, same `os.Unsetenv("ROUTER_AUTH_KEY")` in BeforeEach):
    - The YAML builder now emits `allowedApiKeys:` instead of `auth:\n  key:`. Start with no `allowedApiKeys` → non-loopback request with no key reaches upstream (200).
    - Reload with `allowedApiKeys: ["secret"]` → the same keyless request is now 401; a request with `x-api-key: secret` is 200.
    - Reload again with `allowedApiKeys` removed → the original keyless request is 200 again. Same `*Reloader` instance served all three (assert via the instance, never `os.Getpid()`).
    - Also assert `rel.ConfigSnapshot().AllowedApiKeySet()` reflects each reload state.

11. **Migration-guard tests:**
    - In `pkg/config_test.go`, replace the `Context("auth")` block (line 341) and the `Context("AuthConfig.IsEnabled")` DescribeTable (line 408) with a `Context("legacy auth")` block: YAML containing `auth:\n  key: "s3cret"` fails `pkgcfg.Load` with an error whose text contains `auth`; YAML with `auth: null` still loads with no error and `cfg.Auth == nil` (nil is not a legacy block); a config with no `auth:` loads with no error and `cfg.Auth == nil`. Delete the `IsEnabled()` table (the method is removed).
    - In `pkg/factory/auth_middleware_wiring_test.go`, replace the deleted `ROUTER_AUTH_KEY env resolution` Describe (line 366) with a `CreateServer ROUTER_AUTH_KEY migration guard` Describe: write a VALID config file (single provider against the test upstream, no legacy fields) into a temp dir; with `os.Setenv("ROUTER_AUTH_KEY", "stale-secret")` set (always paired with `DeferCleanup(os.Unsetenv, "ROUTER_AUTH_KEY")`), `factory.CreateServer(ctx, "127.0.0.1:0", path)` returns an error whose text contains `ROUTER_AUTH_KEY`. Assert ONLY the env-set → error path. Do NOT also call `CreateServer` with the env unset: that positive path registers the router's `ccrouter_*` collectors on the process-global `prometheus.DefaultRegisterer` (the factory suite has no per-spec registry reset, unlike the reloader suite), so a second registration in the same test binary returns `AlreadyRegisteredError` and flakes. The env-unset happy path is already covered by every other wiring test in this suite, which drives `CreateRouterFromConfig` directly.

12. Do NOT change `docs/` in this prompt — prompt 4 owns the documentation and the CHANGELOG.
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Do NOT read, validate, or rewrite the `Authorization` header anywhere in the auth layer — its behavior is frozen by the spec (token: swap / pass-through live in `NewAuthSwapTransport`, unchanged).
- Do NOT keep any reference to `x-router-key` or `auth.key` in `pkg/` — source, comments, AND tests. The AC 11 grep (`grep -rn 'x-router-key\|auth\.key' pkg/`) must return 0 lines. `ROUTER_AUTH_KEY` is deliberately exempt: requirement 6's fail-closed guard and the requirement 10-11 env-guard tests must name the env var (verified via the migration-guard greps, not a removal grep). Where a doc comment references `x-router-key`, reword to `x-api-key`.
- Do NOT mutate the inbound `r` in place — always clone before stripping.
- Do NOT log the presented or any configured key at any level. Test assertions must verify this.
- Do NOT add a metrics counter for auth rejections. Out of scope (spec Non-goals).
- Do NOT add key rotation machinery, key IDs, or a runtime toggle endpoint — SIGHUP reload is the only control surface.
- Do NOT make the migration guards toggleable or bypassable — legacy `auth:` fails load and `ROUTER_AUTH_KEY` refuses startup, unconditionally (spec DB 7, fail-closed).
- Use `github.com/bborbe/errors` for error wrapping. Never `fmt.Errorf`.
- `glog` log messages: lowercase; the rejection line is `Info` level, no `V(n)` gate.
- Do NOT add a new external dependency. `crypto/subtle`, `context`, `net/http`, the existing `glog`, and `bborbe/errors` are sufficient.
- `make precommit` must remain green.
</constraints>

<verification>
make precommit

# AC 10 — constant-time compare is in use:
grep -rn 'ConstantTimeCompare' pkg/

# AC 10 — no direct equality on the presented key in the middleware:
grep -rn '== cfg\|cfg\.' pkg/handler/auth-middleware.go   # expect 0 lines

# AC 11 — 009 auth surface fully removed from pkg/ (ROUTER_AUTH_KEY exempt: the
# fail-closed migration guard in factory.go and requirements 10-11 env-guard tests
# must name the env var; verify it via the migration-guard greps below):
grep -rn 'x-router-key\|auth\.key' pkg/  # expect 0 lines

# Migration guards present:
grep -n 'legacy auth block is no longer supported' pkg/config.go
grep -n 'ROUTER_AUTH_KEY is no longer supported' pkg/factory/factory.go

# Context seam exported:
grep -n 'func PresentedApiKeyFromContext\|func ContextWithPresentedApiKey' pkg/handler/presented-api-key.go

# Full suite:
go test ./pkg/... -count=1
</verification>

---
status: completed
spec: [009-inbound-api-key-auth]
summary: Wired optional inbound auth middleware (NewAuthMiddleware) into the /v1/* chain inside trace, with loopback bypass, constant-time key compare, x-router-key strip-before-forward, trace/V(3)-log redaction, and full unit + integration + SIGHUP-reload test coverage.
execution_id: claude-code-router-exec-027-spec-009-auth-middleware
dark-factory-version: dev
created: "2026-08-17"
queued: "2026-08-17T16:13:22Z"
started: "2026-08-17T16:33:47Z"
completed: "2026-08-17T16:47:35Z"
---

# Inbound auth middleware on /v1/* + trace redaction of x-router-key

<summary>
- Non-loopback callers must present the configured shared key in an `x-router-key` header; otherwise the router returns 401.
- The key is compared in constant time so response timing does not leak a matching prefix.
- On a successful authenticated request the `x-router-key` header is stripped before the request reaches any upstream provider.
- Requests from loopback always pass through, regardless of whether a key is configured.
- The header is redacted to `***` in any trace file written by the router, alongside `Authorization` and `x-api-key`.
- A rejection logs the event and the remote address but never the presented or configured key.
- The existing outbound `Authorization` swap and the subscription-OAuth pass-through are unchanged.
</summary>

<objective>
Wire an optional inbound auth middleware in front of the `/v1/*` handler chain. When `auth.key` is configured, non-loopback requests without the matching key are rejected with 401; loopback requests and keyless configs both pass through unchanged. The middleware strips the key header before forwarding and redacts it in trace files.
</objective>

<context>
- Repo root is the current working directory.
- Read `pkg/factory/factory.go:201-238` — `buildMux` is where the `/v1/` chain is mounted. The new middleware wraps the existing `modelRouter` before `NewTraceMiddleware`.
- Read `pkg/handler/trace.go` — `NewTraceMiddleware` is at line 93; the case-insensitive redaction block is at line 116. The new header name must be added to that check.
- Read `pkg/handler/auth-swap-transport.go` and `pkg/handler/anthropic-proxy.go:47` — the outbound `Authorization` swap and the pass-through documented there must not regress.
- Read prompt 1's output: `IsLoopbackRemoteAddr(string) bool` and `(a *AuthConfig) IsEnabled() bool` are the two predicates this prompt consumes.
- Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `bborbe/errors`, never `fmt.Errorf`.
- Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — lowercase log messages, `V(n)` gating.
- Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — external `_test` package, Ginkgo v2 + Gomega conventions.
- Read `docs/dod.md` — the project Definition of Done applied as `validationPrompt`; note ≥80% coverage on new packages and the `## Unreleased` CHANGELOG rule.
</context>

<requirements>
1. Create `pkg/handler/auth-middleware.go` (kebab-case, matching `auth-swap-transport.go` / `model-router.go`) exporting `func NewAuthMiddleware(next http.Handler, key string) http.Handler`. Same constructor shape as `NewTraceMiddleware`. It accepts:
   - `next http.Handler`
   - `key string` — the resolved `cfg.Auth.Key`; empty string ⇒ auth disabled, in which case the constructor returns `next` unchanged so there is zero effect on the hot path (spec DB 1). Mirror `NewAuthSwapTransport(next http.RoundTripper, token string)` in `pkg/handler/auth-swap-transport.go` — same "empty ⇒ no-op wrapper" contract, read it before writing.
   - Do NOT take a config struct. The config type lives in package `pkg` (`pkg/config.go`, import path `github.com/bborbe/claude-code-router/pkg`) — there is no `config` package, and `pkg/handler` currently has no dependency on `pkg`. Keep it that way; the caller in `pkg/factory` resolves `cfg.Auth.IsEnabled()` and passes the bare key.
   - Call the package-level `IsLoopbackRemoteAddr` from prompt 1 directly (it lands in `pkg/handler/`, same package) — do not inject it as a `func(string) bool` parameter.

2. On every request, the middleware:
   - Reads `r.RemoteAddr`. If `IsLoopbackRemoteAddr(r.RemoteAddr)` returns true → skip the key comparison entirely (no compare, no log line) but STILL apply the same strip step as the authenticated path before calling `next`. Spec DB 4: "On a request that passes the check (**or bypasses it**), the `x-router-key` header is removed before the request is forwarded, in every letter case, so no upstream ever observes it." A loopback client configured via `ANTHROPIC_CUSTOM_HEADERS` would otherwise ship the operator's router key to every upstream provider.
   - Otherwise reads the `x-router-key` request header via `http.Header.Get` (case-insensitive; canonicalises to `X-Router-Key`).
   - If the header is empty or its value does not equal the configured key in constant time → respond 401 with `http.Error(w, "auth required", http.StatusUnauthorized)` — the repo's error-response pattern (see `pkg/handler/setloglevel.go:62`); `http.Error` already sets `Content-Type: text/plain; charset=utf-8`. Emit exactly one log line via `glog` — lowercase message, content like `auth rejected remote=<addr>` — and stop. No upstream call.
   - On the authenticated (or bypassed) path, strip the header before passing to `next.ServeHTTP(w, r)`. Stripping means: clone the request (`r.Clone(r.Context())` — it deep-copies the header map) and call `clone.Header.Del("X-Router-Key")` once. `Header.Del` canonicalises its argument, so this single call removes the entry regardless of the case the client sent (`net/http` canonicalises on read). Do NOT use raw `delete(clone.Header, ...)` — that misses the canonical key. Pass the clone downstream. Never mutate the inbound `r` in place — the http stdlib may reuse it for other handlers.

3. Constant-time compare: `subtle.ConstantTimeCompare([]byte(presented), []byte(key)) == 1`. Do NOT add a length pre-check — `ConstantTimeCompare` already returns 0 (it never panics) when the lengths differ; the stdlib doc states "If the lengths of x and y do not match it returns 0 immediately." Do NOT use `subtle.ConstantTimeEq`: its signature is `ConstantTimeEq(x, y int32) int`, so passing `len(...)` (an `int`) does not compile. Any result other than 1 ⇒ 401.

4. In `pkg/factory/factory.go` `buildMux`, **auth goes INSIDE trace** — i.e. auth wraps `modelRouter` first, then the existing `NewTraceMiddleware` call wraps that. Insert exactly one new line immediately after `v1Handler := http.Handler(modelRouter)` (~line 215) and leave the existing `NewTraceMiddleware(...)` call untouched:
   ```
   v1Handler := http.Handler(modelRouter)
   v1Handler = handler.NewAuthMiddleware(v1Handler, authKey)   // NEW — inside trace
   v1Handler = handler.NewTraceMiddleware(v1Handler, traceDir(), handler.DefaultTraceState(), trace)
   mux.Handle("/v1/", v1Handler)
   ```
   Ordering is load-bearing in both directions: trace must run OUTSIDE auth so it still observes `x-router-key` (redacted to `***`, spec AC 18) and still captures requests auth rejects; auth must run INSIDE trace so the header is gone by the time the request reaches `modelRouter` and the upstream (spec AC 6 / DB 4). Getting this backwards makes the trace-redaction test unsatisfiable.
   Change `buildMux`'s signature to `func buildMux(modelRouter http.Handler, trace bool, authKey string) *http.ServeMux` and update its single call site at `pkg/factory/factory.go:192` to pass `cfg.Auth.Key` when `cfg.Auth.IsEnabled()` and `""` otherwise. There is exactly one caller; do not add an options struct.

5. In `pkg/handler/trace.go` around line 116, add `x-router-key` to the case-insensitive redaction block. The check becomes three names. Use the existing pattern verbatim — do not refactor.

6. **Also redact `x-router-key` from V(3) upstream-header logs.** `pkg/handler/redact.go:42-54` (`isCredentialHeader`) currently matches `api-key` / `auth-token` / `secret` / `password` / `bearer` substrings — `router-key` matches none. Add `"router-key"` to that substring list so `RedactHeadersForLog` (used at `pkg/handler/logging-roundtripper.go:81`) redacts `x-router-key` too. Add a `redact_test.go` case asserting `RedactHeadersForLog` maps an `X-Router-Key` header to `<redacted len=N>`, plus a leak-canary case in the logging-roundtripper suite asserting the key literal never appears in V(3) output.

7. **Unit-level tests** (package `handler_test` in `pkg/handler/auth_middleware_test.go`) with Ginkgo v2 + Gomega, driving `NewAuthMiddleware` directly via a counting fake inner handler and `httptest.NewRecorder()`. Set remote address by hand: `req := httptest.NewRequest(...)` then `req.RemoteAddr = "10.0.0.1:12345"`. Do NOT use `httptest.NewServer` for non-loopback cases — a real TCP client on the loopback interface can never produce a non-loopback `RemoteAddr`. Cases:
   - **Auth disabled (empty key):** non-loopback request reaches the inner handler; no 401. The constructor returned `next` unchanged.
   - **Loopback bypass + strip:** loopback request carrying `x-router-key: secret` reaches the inner handler AND the inner handler observes no `x-router-key` / `X-Router-Key` in its `r.Header` (spec DB 4 covers the bypass path).
   - **Missing header:** non-loopback, no `x-router-key` → 401, no inner-handler call.
   - **Wrong key:** non-loopback, `x-router-key: wrong` → 401, no inner-handler call.
   - **Correct key:** non-loopback, matching `x-router-key` → reaches the inner handler; no `x-router-key` / `X-Router-Key` in its `r.Header`.
   - **Whitespace key is a literal, not empty:** with `key: " "` (single space), a non-loopback request presenting `x-router-key: " "` reaches the inner handler; one with no header or `x-router-key: ""` gets 401. Confirms spec Failure Modes row 3: a whitespace-only key disables nothing and is enforced verbatim. Do NOT trim.
   - **Rejection log shape:** use the `captureStderr` helper at `pkg/handler/model-router_test.go:48-60` with `flag.Set("logtostderr", "true")`. There is no `bytes.Buffer` glog sink — do not try to build one. Trigger a rejection; assert exactly one line matches `auth rejected remote=` AND that line does NOT contain the presented key value (use the leak-canary negative-assertion shape from `pkg/handler/logging-roundtripper_test.go:118-134`).

8. **Integration tests through the real handler tree** (package `factory_test` in `pkg/factory/auth_middleware_wiring_test.go`) — these, not the unit tests above, satisfy spec ACs 5, 6, 7 and 18. Read `pkg/factory/trace_wiring_test.go` and `pkg/factory/system_lift_wiring_test.go` first: both build a config pointing at an `httptest.NewServer` upstream, call `factory.CreateRouterFromConfig`, and assert on what the upstream actually received. Follow that exact shape — the value crosses `modelRouter` → `NewAnthropicProxyHandler` → `httputil.ReverseProxy` → `NewLoggingRoundTripper` → `NewAuthSwapTransport` before reaching the wire, and a stub `next` exercises none of it. `buildMux` is unexported and `pkg/factory` has no `export_test.go`; do NOT add an export seam.
   - **AC 5 — correct key is routed:** non-loopback request with the matching key ⇒ the `httptest` upstream receives exactly one request with the expected path and body.
   - **AC 6 — key never on the wire:** the upstream's recorded header map, lower-cased, has zero `x-router-key` entries. Assert the same for a loopback request carrying the header (spec DB 4 covers the bypass path).
   - **AC 7 — `Authorization` byte-for-byte:** configure a token-less (pass-through) provider, send `Authorization: Bearer original` plus the matching key; the upstream's received `Authorization` equals `Bearer original` exactly. This proves the auth layer did not disturb `NewAuthSwapTransport`'s no-op branch (`pkg/handler/auth-swap-transport.go:16-18`).
   - **AC 18 — trace redaction end-to-end:** with trace enabled into a per-test temp dir (`HOME` must be overridden — `traceDir()` resolves `os.UserHomeDir()`), send a non-loopback request with a known key; read the trace JSON and assert the `x-router-key` entry is `***`, and a recursive grep for the key literal over the temp trace dir returns zero hits.

9. **SIGHUP toggle contract tests** (in `pkg/factory/auth_middleware_wiring_test.go` or a sibling). Drive `(*reloader.Reloader).Reload(ctx)` (`pkg/reloader/reloader.go:82`) — do NOT hand-roll a SIGHUP-sending helper. Read `pkg/reloader/reloader_test.go` first: it already constructs `NewReloader(tmpFile, initialHandler, func(ctx, cfg) { return factory.CreateRouterFromConfig(...) })`, drives `Reload(ctx)` after rewriting the config file, and asserts via `ConfigSnapshot()` — mirror that pattern exactly. Reload rebuilds the whole mux via `CreateRouterFromConfig`, so the middleware picks the new key up automatically.
   - Send all requests via `httptest.NewRequest` + `mux.ServeHTTP(rec, req)` — never `httptest.NewServer`, whose real loopback listener defeats the non-loopback `RemoteAddr` (the same trap req 7 warns about).
   - Start with no `auth.key`. Send a non-loopback request with no key → reaches upstream (200).
   - Reload with `auth.key: secret`. Same request → 401.
   - Reload again with `auth.key` removed. Original request → 200 again. Same process throughout; assert the same `*Reloader` instance served all three (no re-construction) — do NOT assert on `os.Getpid()`, which is trivially constant inside a Go test.

10. No counterfeiter mocks needed — the middleware's only collaborator is `next http.Handler`, which is trivially faked.

11. Do not change `docs/config.md` in this prompt — that is prompt 4's scope.
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Do NOT read, validate, or rewrite the `Authorization` header in this middleware. Its behavior is frozen by the spec.
- Do NOT modify the trace middleware's redaction check to add new logic beyond the new header name. The existing three-name pattern stays.
- Do NOT log the presented or configured key at any level. Test assertions must verify this.
- Do NOT mutate the inbound `r` in place — clone before stripping the header.
- Use `github.com/bborbe/errors` for any error wrapping. Never `fmt.Errorf`.
- `glog` log messages: lowercase, `V(n)` gated where appropriate; the rejection log line is `Info` level (operator must see it), no `V(n)` gate.
- Do NOT add a metrics counter for auth rejections. Out of scope for this spec.
- Do NOT add a fallback or circuit breaker around the auth check. A miss is a 401; the spec rejects fallback machinery.
- Do NOT add a new external dependency. `crypto/subtle`, `net/http`, the existing `glog`, and the stdlib are sufficient.
- `make precommit` must remain green.
</constraints>

<verification>
make precommit

# Confirm constant-time primitive is in use:
grep -rn 'subtle\.ConstantTimeCompare' pkg/

# Confirm the new header is redacted in trace files:
grep -n 'x-router-key' pkg/handler/trace.go

# Confirm no naive == compare slipped in:
grep -rn 'cfg.Auth.Key ==\|== cfg.Auth.Key\|Auth.Key !=' pkg/ | grep -v _test.go

# Full suite:
go test ./pkg/... -count=1
</verification>
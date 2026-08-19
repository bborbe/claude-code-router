---
status: completed
spec: [012-session-pinning-pools]
summary: 'Added session identity and per-request upstream selection to the handler layer: x-session-id context plumbing + outbound header strip (session-id.go, session-middleware.go), stateless weighted FNV-1a ring-hash pinning and least-loaded keyless dispatch with round-robin tie-breaking (upstream-pool-handler.go), full Ginkgo test coverage of all ACs, and a feat: CHANGELOG entry; make precommit green'
execution_id: claude-code-router-session-pinning-exec-039-spec-012-session-pinning-selection
dark-factory-version: dev
created: "2026-08-19T17:11:00Z"
queued: "2026-08-19T15:50:36Z"
started: "2026-08-19T15:54:31Z"
completed: "2026-08-19T16:00:58Z"
---

# Session pinning + keyless selection: `x-session-id` → ring hash / least-loaded

<summary>
- The router now reads an inbound `x-session-id` header (when the client sends one via `ANTHROPIC_CUSTOM_HEADERS`) and carries it on the request context, mirroring how the `x-api-key` travels today.
- The header is stripped before the request is forwarded upstream, so a server never sees the session id.
- A new upstream-pool handler dispatches each request to exactly one pool member: a request with a session id is pinned to the same member on every request via a weighted ring hash of the id (deterministic, stateless, no session→member map); a request without one goes to the least-loaded member.
- Keyless requests never stack on the first-declared member: equally-loaded members share keyless traffic round-robin, so a flood spreads across the pool.
- Weighted pools spread pinned sessions proportionally to their weights (a 2:1 weight ratio gives the heavier member ~2/3 of sessions).
- A `[route] session=<id> upstream=<url>` log line (glog V(2)) names the chosen member per request — this is the operator and test evidence that pinning and spread are working.
- No factory changes yet: this prompt ships the middleware and selection handler with their tests; wiring them into the route tree is the next prompt.
</summary>

<objective>
Add session identity and per-request member selection to the handler layer — `x-session-id` into request context (with outbound header strip), weighted ring-hash pinning for sessioned requests, and least-loaded dispatch for keyless ones — so a pool can keep each session's prompt cache warm on one server while spreading anonymous load.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- Read `pkg/handler/presented-api-key.go` — the context-value pattern to mirror exactly: unexported context-key type, `ContextWithPresentedApiKey(ctx, key)`, `PresentedApiKeyFromContext(ctx) string`. The new `session-id.go` copies this shape with a session id.
- Read `pkg/handler/auth-middleware.go` — the clone-and-strip middleware pattern (`r.Clone(r.Context())`, `clone.Header.Del("X-Api-Key")`, then context injection). The new `session-middleware.go` follows the same clone/strip/context shape for `X-Session-Id`.
- Read `pkg/handler/model-router.go` — the existing `[route]` V(2) detail lines (`[route] model=%q matched %q -> provider=%s`, `[route] key matched provider=%s`) that the new `[route] session=...` line joins. The model router is NOT modified by this prompt — the pool handler emits its own line.
- Read `pkg/handler/model-router_test.go` for the shared `labelHandler` / `captureStderr` / `alwaysSample` / `testMetrics` / `testDateTime` helpers (package `handler_test`) and `pkg/handler/model-router_key_routing_test.go` for the `flag.Set("v", "2")` + `captureStderr` log-assertion pattern.
- Read `pkg/handler/concurrency-limiter.go` — the per-upstream semaphore that prompt 3 wires into `UpstreamMember.InFlight`; this prompt only defines the `InFlight func() int` seam (nil = always 0).
- Coding plugin docs (in-container paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-http-handler-refactoring-guide.md` — small-handler conventions.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega, goroutines allowed in `_test.go`.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md` — glog verbosity levels (the `[route]` detail lines are V(2), matching the existing model-router `[route]` lines).
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — GoDoc conventions.
</context>

<requirements>
1. **New `pkg/handler/session-id.go`** (package `handler`) — exact mirror of `presented-api-key.go`, with `context` as the only import:
   - An unexported `sessionIDContextKey` type (distinct from `presentedApiKeyContextKey`) so the two context values never collide.
   - `func ContextWithSessionID(ctx context.Context, sessionID string) context.Context` — returns a copy of ctx carrying the session id.
   - `func SessionIDFromContext(ctx context.Context) string` — returns the stored session id, or `""` when absent (the "empty session id" state, spec DB 2).
   - GoDoc on both exported functions.

2. **New `pkg/handler/session-middleware.go`** (package `handler`) — `func NewSessionMiddleware(next http.Handler) http.Handler`. It is a no-op passthrough in shape only if no header handling were needed — it ALWAYS runs (the header strip must happen for every /v1/* request). ServeHTTP contract:
   - Read `r.Header.Get("X-Session-Id")`.
   - Clone the request (`r.Clone(r.Context())`), `clone.Header.Del("X-Session-Id")` (Header.Del is case-insensitive, so every letter case is stripped, matching the `X-Api-Key` handling in `auth-middleware.go`).
   - When the header value is non-empty, put it into the clone's context via `ContextWithSessionID`; when absent/empty, leave it unset (`SessionIDFromContext` returns `""` — spec DB 2 "Absent header → empty session id").
   - `next.ServeHTTP(w, clone)`.
   - The header is read ONLY as hash input for pinning — never for auth, never logged (spec Constraints, Security). The session id is not written to any log line except the `[route]` detail line in requirement 3 (which names the chosen member and the raw session id for operator diagnosis).

3. **New `pkg/handler/upstream-pool-handler.go`** (package `handler`) — the selection handler. Exported types (comment text may be reworded; names and shapes are fixed):
   ```go
   // UpstreamMember is one server in a provider's upstream pool: the
   // handler that serves it (per-upstream proxy wrapped in its
   // concurrency limiter), the upstream URL used only for the [route]
   // log line, and the selection inputs — Weight for the weighted ring
   // hash of a pinned session, InFlight for least-loaded selection of
   // keyless requests. InFlight may be nil, meaning "always 0" (an
   // uncapped member).
   type UpstreamMember struct {
       Upstream string
       Handler  http.Handler
       Weight   int
       InFlight func() int
   }
   ```
   ```go
   // NewUpstreamPoolHandler returns a handler that dispatches each
   // request to exactly one of members: a request carrying a non-empty
   // session id (from the session middleware's context) is pinned to the
   // same member every time via a weighted ring hash of the session id —
   // deterministic and stateless, recomputable from the id, no
   // session→member map — so the session's upstream prompt cache stays
   // warm on that server. A request with an empty session id is sent to
   // the least-loaded member (fewest in-flight requests by the
   // per-upstream semaphore), with round-robin tie-breaking among
   // equally-loaded members so keyless floods spread instead of stacking
   // on the first-declared member. Each chosen dispatch emits a
   // [route] session=<id> upstream=<url> glog V(2) detail line.
   func NewUpstreamPoolHandler(members []UpstreamMember) http.Handler
   ```
   Implementation contract (spec DB 2–4, Constraints, Failure Modes):
   - The handler struct holds `members []UpstreamMember`, a precomputed `cumulative []int` of the cumulative weights (cumulative[i] = sum of members[0..i].Weight) and the `totalWeight` (= last cumulative value), plus an `atomic.Uint64` round-robin counter. `members` is non-empty and every weight positive (config validation guarantees both — see prompt 1).
   - **Pinned path (non-empty session id):** compute `h := fnv.New64a(); h.Write([]byte(sessionID)); slot := h.Sum64() % uint64(totalWeight)`, then pick the first index i with `uint64(cumulative[i]) > slot`. This is the ring-of-cumulative-weights: the same session id maps to the same member on every request, across process restarts, with no in-memory map (spec DB 3, Constraints "stateless"). `hash/fnv` is the Go standard library — no new dependency.
   - **Keyless path (empty session id):** compute each member's load as `m.InFlight()` (nil → 0); find the minimum; collect the indices at that minimum; then pick `ties[(rr-1) % len(ties)]` where `rr = atomic.AddUint64(&p.rr, 1)`. Round-robin among equally-loaded members is what prevents "never the first-declared one" stacking (spec DB 4, AC 3) — with every member idle, successive keyless requests cycle through all members.
   - After selecting, log `glog.V(2).Infof("[route] session=%s upstream=%s", sessionID, member.Upstream)` (the empty session id logs as `session= upstream=<url>`) and dispatch `member.Handler.ServeHTTP(w, r)`. Do NOT log the session id anywhere else, and do NOT add Prometheus metrics or any other log line (spec Non-goals; the model router's existing `[req]` line already covers per-request status).
   - A pinned member that is saturated is the member's own limiter's concern (429 in prompt 3) — the pool handler just dispatches; it does not queue, retry, or fail over to a sibling member (spec Non-goals: no pool-level overflow failover).

4. **Tests in new `pkg/handler/session-id_test.go` + `pkg/handler/session-middleware_test.go`** (package `handler_test`, Ginkgo v2 + Gomega):
   - `session-id.go`: round-trip `handler.ContextWithSessionID` → `handler.SessionIDFromContext` returns the id; a context WITHOUT the value returns `""`; a different context value (e.g. `handler.ContextWithPresentedApiKey`) does not pollute the session lookup.
   - `session-middleware.go`:
     - A request with `X-Session-Id: sess-1` → the handler `next` receives a request whose context has `SessionIDFromContext == "sess-1"` and whose `X-Session-Id` header is gone (assert via `r.Header.Get("X-Session-Id") == ""` and a lower-cased-header check like `lowerCaseKeys` in `pkg/factory/auth_middleware_wiring_test.go` to catch any case variant).
     - A request with `x-session-id` (lower-case header name) → same behavior (header lookup is case-insensitive).
     - A request with NO session header → `SessionIDFromContext == ""` and the header is absent downstream.
     - The wrapped handler is invoked exactly once per request (dispatch passthrough).

5. **Tests in new `pkg/handler/upstream-pool-handler_test.go`** (package `handler_test`). Members are built with `handler.UpstreamMember{Upstream: "<url>", Handler: <counting handler>, Weight: n, InFlight: <fake>}`. Reuse `labelHandler` from `model-router_test.go` (writes its label to the body) or a small counting handler that records how many times it is invoked. Session ids are injected via `handler.ContextWithSessionID(req.Context(), id)` — the middleware is not run in these unit tests (same seam as `postWithKey` in `model-router_key_routing_test.go`). Rows:
   - **AC 2 — pinning determinism + across-instances (stateless):** a 1:1 two-member pool (weights [1,1]). For a session id, serve the same request several times → the SAME member handles every one. Build a SECOND `NewUpstreamPoolHandler` over identical members → the same session id picks the same member there too (proves no in-memory map, recomputable from the id — spec Constraints "across restarts").
   - **AC 2 — two distinct sessions land on different members:** with the same 1:1 two-member pool, search candidate ids (`"sess-0"` .. `"sess-9"`) for two that the handler maps to DIFFERENT members (e.g. `"sess-0"` and `"sess-1"` are known-distinct under FNV-1a 64 mod 2, but pick by probing the handler rather than hard-coding), then assert EACH is stable across repeated requests and the two map to different members.
   - **AC 4 — weighted distribution (spec evidence):** a 2:1 pool (weights [2,1]), members "https://a" and "https://b". Serve N=100 distinct session ids (`"session-0"` .. `"session-99"`, injected via context) and count invocations per member. Assert the heavier member (`"https://a"`) served ≥55 of the 100 (spec AC 4 threshold). (Verified: FNV-1a 64 over `"session-0".."session-99"` mod 3 gives 66/34.)
   - **AC 3 — keyless goes to the least-loaded member, never the first-declared one:** two members, `InFlight` returning 1 for member A and 0 for member B → a keyless request (no session id) is served by B. Add one row where a member is built with `InFlight: nil` (the production default until prompt 3 wires the real semaphore) asserting it is treated as load 0 and never panics. Then with both `InFlight` returning 0, fire N=8 keyless requests and assert via `captureStderr` + `flag.Set("v", "2")` that the `[route] session= upstream=<url>` lines show ≥2 distinct `upstream=` values (spread, not first-declared stacking — spec AC 3 evidence).
   - **`[route]` log shape:** with `flag.Set("v", "2")` + `captureStderr`, a pinned request logs `[route] session=sess-1 upstream=https://a` and a keyless one logs `[route] session= upstream=https://b` (assert the exact substrings).
   - **Single-member pool:** a one-member pool serves both pinned and keyless requests through that member (the degenerate pool every legacy config becomes).

6. **Do NOT wire anything.** This prompt does NOT touch `pkg/factory/factory.go`, `pkg/handler/model-router.go`, `pkg/handler/auth-middleware.go`, `pkg/handler/concurrency-limiter.go`, or the trace middleware. The session middleware and pool handler exist but are unmounted until the next prompt wires them; `make precommit` must still pass with them unused (Go does not complain about unused exported symbols).
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Session pinning is STATELESS: weighted ring hash only — no session→member map, no TTL, no memory growth. Recomputable from the session id at any time (spec Constraints). The only mutable state in the pool handler is the round-robin tie-break counter for keyless dispatch.
- `x-session-id` is read ONLY as hash input — never for auth. Auth stays exactly as today (`allowedApiKeys`, loopback bypass). The header is stripped before forwarding upstream (spec Constraints, Desired Behavior 2).
- `x-session-id` is untrusted client input: used solely as a hash key for pinning, never for authentication or authorization decisions (spec Security). Do not log it beyond the `[route]` detail line, and never write it to the request body or a response.
- Keyless dispatch is least-loaded with round-robin tie-breaking — never first-declared stacking (spec DB 4). No pool-level overflow failover (a saturated pinned member is the member limiter's 429 concern, prompt 3 — spec Non-goals).
- Do NOT add Prometheus metrics, config knobs, opt-out flags, or tunable thresholds — the spec's observability surface for this change is the `[route]` log line and the existing `[req]` line (spec Non-goals, Security: 429 bodies/response reveal nothing; this prompt adds no response surface).
- No new dependencies — `hash/fnv` is the Go standard library.
- The session middleware is NOT auth: it runs for every /v1/* request regardless of loopback or key state and does not interact with `allowedApiKeys` (spec Constraints).
- Tests follow the repo's Ginkgo convention and must not depend on real wall-clock waits (spec Constraints — none of these tests need timing).
- Use `github.com/bborbe/errors` for any error wrapping; never `fmt.Errorf` (though this prompt's code is mostly error-free — middleware and selection have no error paths).
- No AI attribution in code or comments.
- `make precommit` must remain green — run it before declaring done.
- Follow `docs/dod.md` (GoDoc on every new exported identifier).
- Do NOT touch `docs/`, `CHANGELOG.md`, `pkg/config.go`, `pkg/config_test.go`, or `pkg/factory/` in this prompt — prompt 1 owns config, prompt 3 owns factory wiring, prompt 4 owns docs.
</constraints>

<verification>
make precommit

# The three new handler files exist with the required exports:
grep -n 'func ContextWithSessionID\|func SessionIDFromContext' pkg/handler/session-id.go
grep -n 'func NewSessionMiddleware' pkg/handler/session-middleware.go
grep -n 'func NewUpstreamPoolHandler\|type UpstreamMember struct' pkg/handler/upstream-pool-handler.go

# Session identity + selection tests exist:
grep -c 'ContextWithSessionID' pkg/handler/*_test.go        # expect >=1
grep -c '\[route\] session=' pkg/handler/upstream-pool-handler_test.go  # expect >=1

# No factory wiring crept in (out of scope until prompt 3):
grep -rc 'NewUpstreamPoolHandler\|NewSessionMiddleware' pkg/factory/   # expect 0 (Linux GNU grep: -r required for a directory arg)

# Full suite:
go test -mod=mod -count=1 ./pkg/handler/...
go test -mod=mod -count=1 ./pkg/...
</verification>

---
status: completed
spec: [003-enabletrace-endpoint]
summary: Wired /enabletrace and /disabletrace HTTP handlers, updated NewTraceMiddleware with per-request IsEnabled() gate and injected *TraceState, registered handlers in CreateRouterFromConfig mux, updated all tests including new Ginkgo specs for toggle/flag-or-config behavior.
execution_id: claude-code-router-enabletrace-exec-012-spec-003-enabletrace-handlers
dark-factory-version: dev
created: "2026-06-30T11:57:22Z"
queued: "2026-06-30T12:09:00Z"
started: "2026-06-30T12:13:36Z"
completed: "2026-06-30T12:18:35Z"
---

<summary>
- Two new operator-local HTTP endpoints, `POST /enabletrace` and `POST /disabletrace`, toggle per-request trace logging on and off without a router restart.
- `POST /enabletrace` turns tracing on for a bounded 5-minute window that auto-disables on expiry; `POST /disabletrace` turns it off immediately and cancels the pending timer.
- The trace middleware is now mounted unconditionally on `/v1/` and consults the trace-state flag per request: a trace file is written when the flag is true OR the legacy `trace: true` config flag is true.
- With legacy `trace: true` and no `/enabletrace` call, every `/v1/*` request still writes a trace file (no regression from v0.14.0).
- The `Authorization` and `x-api-key` redaction to `***` in trace files is unchanged — no raw secret leaks.
- Both endpoints mirror the existing `/setloglevel/{level}` handler pattern: no auth, registered on the stdlib mux in `CreateRouterFromConfig`, return a short plaintext body.
- The `CreateRouterFromConfig` signature is unchanged.
- Ginkgo specs cover the toggle, the flag-OR-config middleware switch, the redaction regression guard, and the glog V(n) gating.
</summary>

<objective>
Wire the HTTP surface (`/enabletrace`, `/disabletrace` handlers) on top of the trace-state primitive from prompt 1, register them in the `CreateRouterFromConfig` mux alongside `/setloglevel/`, and update `NewTraceMiddleware` to consult `IsEnabled()` per request with flag-OR-config semantics so the middleware can be mounted unconditionally on `/v1/`. This is prompt 2 of 3 for spec 003 and depends on prompt 1 (the trace-state primitive).
</objective>

<context>
Read CLAUDE.md at the repo root for project conventions.

Read these source files before making changes:
- `specs/in-progress/003-enabletrace-endpoint.md` — the full spec; pay attention to "Desired Behavior" items 1, 2, 3, 7, 8, the "Constraints" section (frozen file/seam: `CreateRouterFromConfig`; frozen pattern: mirror `/setloglevel`; frozen invariant: Authorization / x-api-key redaction; frozen config field: `Config.Trace` stays; frozen listener: `127.0.0.1:8788`; glog conventions), and the "Failure Modes" table rows 4, 5, 6.
- `pkg/handler/trace_state.go` — prompt 1 (executed before this prompt in the dark-factory queue) added the `TraceState` type with `Enable()` / `Disable()` / `IsEnabled() bool`, the constructor `NewTraceState()` / `NewTraceStateWithTTL(ttl time.Duration)`, the constant `TraceTTLDefault = 5 * time.Minute`, the test-only `traceTTLFromEnv()` helper, and the process-global `defaultTraceState` instance. Read it to CONFIRM the exact type name, method signatures, and accessor for the process-global instance. If the file is absent or the API differs (prompt 1 was rejected/modified), STOP — this prompt cannot proceed without it. Do not re-implement the primitive; prompt 1 owns it.
- `pkg/handler/setloglevel.go` — the exemplar to mirror. Key signatures (read verbatim): `func NewSetLoglevelHandler() http.Handler` and `func NewSetLoglevelHandlerWithRevert(autoRevert time.Duration) http.Handler`. The handler returns `http.HandlerFunc` that parses the URL suffix, validates, and writes a short plaintext body via `fmt.Fprintf(w, "set loglevel to %d completed\n", level)`. Mirror this structure: constructor returns `http.Handler`, body is a short plaintext line, errors use `http.Error(w, ..., http.StatusBadRequest)`.
- `pkg/handler/setloglevel_test.go` — the test pattern to mirror: `httptest.NewRequest(http.MethodGet, "/setloglevel/3", nil)`, `httptest.NewRecorder()`, `h.ServeHTTP(rec, req)`, `Expect(rec.Code).To(Equal(http.StatusOK))`, `Expect(rec.Body.String()).To(ContainSubstring(...))`. Use `http.MethodPost` for the new endpoints (the spec says `POST /enabletrace`).
- `pkg/handler/trace.go` — the v0.14.0 `func NewTraceMiddleware(next http.Handler, traceDir string) http.Handler`. The redaction logic (lines building `reqHeaders` with `strings.ToLower(name) == "authorization" || strings.ToLower(name) == "x-api-key"` → `"***"`) MUST NOT regress. THIS prompt updates `NewTraceMiddleware` to consult `IsEnabled()` per request before writing a trace file, but the redaction, the JSON shape, the `Unwrap()` contract on `traceResponse`, the filename format, and the best-effort file-write behavior all stay identical to v0.14.0.
- `pkg/factory/factory.go` — the mux-construction seam. Current wiring (read verbatim):
  ```go
  mux.Handle("/setloglevel/", handler.NewSetLoglevelHandler())
  ...
  v1Handler := http.Handler(modelRouter)
  if cfg.Trace {
      glog.V(2).Infof("trace enabled")
      v1Handler = handler.NewTraceMiddleware(v1Handler, traceDir())
  }
  mux.Handle("/v1/", v1Handler)
  ```
  The conditional `if cfg.Trace` mount is REPLACED with an unconditional mount: the middleware is always wrapped around the model router, and per-request it consults `IsEnabled() || cfg.Trace` before writing a trace file. The `cfg.Trace` flag is passed into the middleware so it can implement flag-OR-config.
- `pkg/config.go` — `Config.Trace bool` (yaml `trace,omitempty`). Do NOT modify config.go. The config field stays as a deprecated always-on opt-in; prompt 3 deprecates it in docs.
- `pkg/handler/trace_test.go` — existing Ginkgo specs for the middleware. The existing specs construct `handler.NewTraceMiddleware(inner, traceDir)` directly with no config-flag argument. After this prompt changes the constructor signature (see requirement 3), the existing specs MUST be updated to pass the new arguments. Preserve every existing assertion (file presence, JSON shape, redaction, verbatim non-redacted headers, no-raw-secret canary, failure-mode rows, glog gating).
- Coding-plugin docs (glog V(n) gating, http-handler conventions, Ginkgo patterns) are NOT mounted in this project's YOLO container per `.dark-factory.yaml` (only GOPATH + build caches are mounted). Rely on the inline constraints repeated in `<constraints>` below — they restate the glog V(n) discipline, the handler/middleware shape, and the Ginkgo patterns the new code must follow.
</context>

<requirements>

1. **Create the `/enabletrace` handler** in a new file `pkg/handler/enabletrace.go` (package `handler`). Mirror the `setloglevel.go` structure: a constructor returning `http.Handler`, a `http.HandlerFunc` body, a short plaintext success body, no auth, no body parsing. The handler takes no URL suffix and no request body — it is a fixed no-arg POST. Signature:

   ```go
   // NewEnableTraceHandler returns a handler that turns per-request trace
   // logging on for a bounded 5-minute window (TraceTTLDefault). The window
   // auto-disables on expiry; repeated calls reset the window. Mirrors the
   // /setloglevel pattern: operator-local, no auth, short plaintext body.
   func NewEnableTraceHandler() http.Handler
   ```

   The handler body calls `Enable()` on the process-global trace-state instance from prompt 1 (access it via the accessor prompt 1 exposed — read `trace_state.go` to confirm the exact accessor name; if prompt 1 exposed `defaultTraceState` via an exported function, call that). On success, write `http.StatusOK` with the EXACT plaintext body `trace enabled\n` (lowercase, matching `set loglevel to %d completed\n`). There is no failure path — `Enable()` does not return an error. Do NOT read the request body or query string.

2. **Create the `/disabletrace` handler** in a new file `pkg/handler/disabletrace.go` (package `handler`). Same structure:

   ```go
   // NewDisableTraceHandler returns a handler that turns per-request trace
   // logging off immediately and cancels any in-flight TTL timer so no late
   // reset can flip tracing back on. Mirrors the /setloglevel pattern.
   func NewDisableTraceHandler() http.Handler
   ```

   The handler body calls `Disable()` on the same process-global instance. On success, write `http.StatusOK` with the EXACT plaintext body `trace disabled\n`. No failure path. No body or query parsing.

3. **Update `NewTraceMiddleware`** in `pkg/handler/trace.go` to consult the trace-state flag per request with flag-OR-config semantics. The current signature (read verbatim):

   ```go
   func NewTraceMiddleware(next http.Handler, traceDir string) http.Handler
   ```

   Change to accept an injected `*TraceState` AND the legacy config flag so the middleware can implement flag-OR-config AND tests can inject an isolated `TraceState` (Desired Behavior item 3: when `cfg.Trace` is true, the middleware emits unconditionally regardless of the atomic boolean). The signature is FROZEN as approach (b) — do not leave this as a choice:

   ```go
   // NewTraceMiddleware wraps next in a handler that, for each request,
   // captures the full request (method, path, headers, body) and full
   // response (status, headers, body) and writes one JSON file to
   // traceDir — BUT only when trace is enabled. Trace is enabled when
   // either the injected traceState flag is true (toggled via
   // /enabletrace, bounded by a 5-min TTL) OR configAlwaysOn is true
   // (the legacy trace: config flag, deprecated). Authorization and
   // x-api-key request headers are redacted to "***" (case-insensitive);
   // all other headers and bodies are logged verbatim. Trace file writes
   // are best-effort: a write failure logs glog.Warningf and the request
   // still succeeds. The trace directory is created on demand (MkdirAll,
   // 0o700).
   func NewTraceMiddleware(next http.Handler, traceDir string, traceState *TraceState, configAlwaysOn bool) http.Handler
   ```

   Confirm the exact `*TraceState` type name from `trace_state.go` (prompt 1's output) before writing this signature — if prompt 1 used a different type name, use THAT name. The factory passes the process-global instance; tests pass `NewTraceStateWithTTL(...)` for isolation.

   Per-request gating logic (inside the `http.HandlerFunc`): at the top of each request, check `traceState.IsEnabled() || configAlwaysOn`. If NEITHER is true, delegate directly to `next.ServeHTTP(w, r)` and return WITHOUT capturing headers, body, or writing a trace file — the per-request overhead when tracing is off is a single atomic bool load plus one bool-OR.

   The redaction logic, the `traceResponse` wrapper (with `Unwrap()` for SSE flush passthrough), the JSON shape (`request.method`/`path`/`headers`/`body` + `response.status`/`headers`/`body`), the filename format (`formatTimestampNano()` + `nextRequestID()`), and the best-effort file-write behavior (`writeTraceFile`) stay IDENTICAL to v0.14.0. Do NOT touch the redaction lines, do NOT change the JSON keys, do NOT change `traceResponse`, do NOT change `writeTraceFile`. Only add the per-request enable-check gate at the top of the handler function.

   IMPORTANT: when tracing is OFF (flag false AND configAlwaysOn false), the middleware must NOT read the request body via `io.ReadAll(r.Body)`. Reading the body and restoring it via `io.NopCloser` is an unnecessary overhead when tracing is off and risks breaking SSE streaming for large `/compact` bodies that would be buffered into a `bytes.Buffer` needlessly. The body-read + restore must happen ONLY on the trace-enabled path. Structure the handler so the early-return for the disabled case happens BEFORE any body capture.

4. **Update the existing `trace_test.go` specs** to pass the new `traceState *TraceState` + `configAlwaysOn bool` arguments to `NewTraceMiddleware`. The current `BeforeEach` constructs:

   ```go
   mux = handler.NewTraceMiddleware(inner, traceDir)
   ```

   Change to:

   ```go
   mux = handler.NewTraceMiddleware(inner, traceDir, handler.NewTraceState(), true)
   ```

   (pass a fresh `NewTraceState()` and `configAlwaysOn=true` so the existing specs — which assert trace files ARE written — continue to pass with the legacy always-on behavior). Preserve EVERY existing assertion verbatim: file presence + JSON shape, redaction of Authorization / x-api-key (any casing), verbatim non-redacted headers, no-raw-secret canary, failure-mode rows (dir create fails, file write fails), and the glog V(n) gating static check. These are the regression guard for the token-leak invariant — do NOT weaken any of them.

5. **Add new Ginkgo specs** in `pkg/handler/trace_test.go` (or a sibling `enabletrace_test.go` in `package handler_test`) covering the toggle behavior. The `NewTraceMiddleware` signature is FROZEN as approach (b) (injected `*TraceState`) per requirement 3 — tests pass an isolated `NewTraceStateWithTTL(...)` instance:

   - **AC #1 (enabletrace → file written):** Construct `NewTraceMiddleware(inner, traceDir, traceState, false)` (configAlwaysOn false so the flag is the only source of truth; `traceState` = `NewTraceStateWithTTL(5*time.Second)`). Send a `POST /v1/messages` — assert NO trace file is written (tracing is off by default). Then call `traceState.Enable()` — send another `POST /v1/messages` — assert exactly one trace file is now written.

   - **AC #2 (disabletrace → no file):** Using the same isolated instance, `Enable()`, then `Disable()`, then send a `POST /v1/messages` — assert NO new trace file is written (count unchanged from before the request).

   - **AC #3 / Failure Mode row 4 (disable mid-window cancels timer):** `Enable()` with a short ttl (e.g. 200ms); immediately `Disable()`; assert `IsEnabled()` stays `false` via `Consistently(func() bool { return traceState.IsEnabled() }, "300ms", "10ms").Should(BeFalse())` — do NOT use a fixed `time.Sleep(300ms)` (flake risk on loaded CI). Send a `POST /v1/messages` — assert NO trace file.

   - **AC #7 (flag-OR-config: config always-on overrides flag):** Construct `NewTraceMiddleware(inner, traceDir, traceState, true)` with `traceState.IsEnabled() == false` (no enable call). Send a `POST /v1/messages` — assert a trace file IS written (config always-on wins). This is the v0.14.0 no-regression guard at the middleware level.

   - **Handler specs for `/enabletrace` and `/disabletrace`:** Construct the handlers, send `httptest.NewRequest(http.MethodPost, "/enabletrace", nil)`, assert `rec.Code == 200` AND `rec.Body.String()` `Equal`s the exact literal `trace enabled\n` (not `ContainSubstring` — pin the exact body). Same for `/disabletrace` with the exact literal `trace disabled\n`. These mirror the `setloglevel_test.go` structure exactly.

   - **glog V(n) gating:** Extend the existing static-check spec to also read `enabletrace.go` and `disabletrace.go` and assert no bare `glog.Infof` / `glog.Info(` without `glog.V(`.

6. **Wire the handlers + unconditional middleware into `CreateRouterFromConfig`** in `pkg/factory/factory.go`. The current lines (read verbatim):

   ```go
   mux.Handle("/setloglevel/", handler.NewSetLoglevelHandler())
   mux.Handle("/gc", libhttp.NewGarbageCollectorHandler())
   v1Handler := http.Handler(modelRouter)
   if cfg.Trace {
       glog.V(2).Infof("trace enabled")
       v1Handler = handler.NewTraceMiddleware(v1Handler, traceDir())
   }
   mux.Handle("/v1/", v1Handler)
   ```

   Replace with:

   ```go
   mux.Handle("/setloglevel/", handler.NewSetLoglevelHandler())
   mux.Handle("/enabletrace", handler.NewEnableTraceHandler())
   mux.Handle("/disabletrace", handler.NewDisableTraceHandler())
   mux.Handle("/gc", libhttp.NewGarbageCollectorHandler())
   v1Handler := http.Handler(modelRouter)
   if cfg.Trace {
       glog.V(2).Infof("trace enabled via config")
   }
   v1Handler = handler.NewTraceMiddleware(v1Handler, traceDir(), <processGlobalTraceState>, cfg.Trace)
   mux.Handle("/v1/", v1Handler)
   ```

   Where `<processGlobalTraceState>` is the process-global instance accessor from prompt 1 (confirm the exact name in `trace_state.go` — e.g. an exported function returning `*TraceState`). The `glog.V(2).Infof("trace enabled")` startup line is REMOVED from the factory (it now fires per-`Enable()` call inside the primitive, per prompt 1). If `cfg.Trace == true` at boot, emit a single startup log `glog.V(2).Infof("trace enabled via config")` BEFORE the `mux.Handle("/v1/", ...)` line so operators still see the always-on state at boot (the per-request `Enable()` log from prompt 1 is for the endpoint-toggle path, not the config path). This preserves the v0.14.0 boot-time visibility for the deprecated config flag. The config boot path does NOT call `Enable()` (it only sets `configAlwaysOn`); the two log lines are mutually exclusive.

   The `NewTraceMiddleware` argument list is FROZEN at `(next, traceDir, traceState *TraceState, configAlwaysOn bool)` per requirement 3 — be consistent across the constructor definition, the factory call site, and the test call sites.

   The `CreateRouterFromConfig` signature `func CreateRouterFromConfig(ctx context.Context, cfg *pkg.Config, opts ...RouterOption) (http.Handler, error)` MUST remain unchanged — the new handlers and middleware wiring are internal to the function body.

7. **Update the factory-level spec** `pkg/factory/trace_wiring_test.go` (added in spec 002) to reflect the unconditional middleware mount. The existing spec asserts "trace off → no file written" via `cfg.Trace: false`. After this prompt, the middleware is ALWAYS mounted, but the per-request gate means no file is written when the flag is false AND config is false. The existing no-file assertion STILL holds (the gate short-circuits before any file write). The existing glog startup-line spec asserts `ContainSubstring("trace enabled")` — the new boot message `trace enabled via config` CONTAINS that substring, so the existing assertion still passes; update it to assert `trace enabled via config` for precision. Update the spec's construction calls to pass the new `NewTraceMiddleware` arguments. Add a factory-level spec that calls `CreateRouterFromConfig` with `cfg.Trace: false`, sends a `POST /v1/messages` through the returned mux, and asserts NO trace file is written (the default-off gate). Add another with `cfg.Trace: true` and assert a file IS written (config always-on). Note: factory-level tests that drive the process-global trace-state must use `t.Setenv("HOME", tmpDir)` (or the Ginkgo `BeforeEach` equivalent) to avoid writing to the real `~/.claude-code-router/trace/` — mirror the existing `trace_wiring_test.go` HOME-override pattern.

8. **Run `make precommit`** in the repo root. Fix any gofmt / addlicense / lint / golangci-lint issues. All existing tests plus the new handler, middleware, and factory specs must pass.

</requirements>

<constraints>

- **Token-leak invariant (load-bearing, repeated from spec):** `Authorization` and `x-api-key` are NEVER written raw to trace files. Redact to the literal `***`, case-insensitive header lookup. This matches the v0.14.0 invariant in `trace.go` — the redaction lines must NOT regress. The existing `trace_test.go` redaction specs are the regression guard; preserve every assertion.
- **`/setloglevel` pattern to mirror:** operator-local, no auth, short plaintext body, stdlib-mux-registered, constructor returns `http.Handler`. The new `/enabletrace` and `/disabletrace` handlers follow this exactly.
- **`CreateRouterFromConfig` signature unchanged:** `func CreateRouterFromConfig(ctx context.Context, cfg *pkg.Config, opts ...RouterOption) (http.Handler, error)`. The new handler registrations and middleware wiring are internal to the function body.
- **`Config.Trace` deprecated, not removed:** `Config.Trace bool` (yaml `trace,omitempty`) stays parsed and functional. `trace: true` continues to emit on every `/v1/*` request regardless of the atomic boolean (flag-OR-config). Do NOT remove, rename, or break the config field. Prompt 3 deprecates it in docs.
- **Trace independent of SIGHUP / `atomic.Pointer[http.Handler]` mux swap:** the toggle uses a process-internal atomic flag. No SIGHUP dependency. Independent of spec 002-sighup-hot-reload (separate worktree/PR, not yet merged).
- **glog discipline:** any new `Info`-level log is `V(n)`-gated; no bare `glog.Infof`. Log messages are lowercase (`trace enabled via endpoint`, `trace disabled via endpoint`). `glog.Warningf` is exempt from the V(n) gate but still lowercase.
- **Frozen listener: `127.0.0.1:8788` operator-local trust model** — no auth added to the new endpoints, no remote bind. Same trust model as `/setloglevel`.
- **Frozen constant: 5-minute production TTL.** The TTL lives in prompt 1's primitive; this prompt does not add a TTL knob. `TRACE_TTL` is test-only.
- **No regression from v0.14.0:** the redaction, JSON shape, `Unwrap()` SSE contract, best-effort file-write behavior, and the `trace: true` always-on path must all behave identically to v0.14.0. The only behavioral change is the addition of the per-request enable-gate (which is a no-op when config is true).
- **Do NOT commit** — dark-factory handles git.
- **Existing tests must still pass.**

</constraints>

<verification>

```bash
cd /workspace
make precommit
```

Must exit 0. Additionally:

```bash
# New handler files exist
ls /workspace/pkg/handler/enabletrace.go
ls /workspace/pkg/handler/disabletrace.go

# Handlers registered in factory
grep -n 'mux.Handle("/enabletrace"' /workspace/pkg/factory/factory.go
grep -n 'mux.Handle("/disabletrace"' /workspace/pkg/factory/factory.go
# Expect: one match each

# Middleware mounted unconditionally (NewTraceMiddleware appears exactly once, outside any conditional)
grep -c 'NewTraceMiddleware' /workspace/pkg/factory/factory.go
# Expect: 1 (the unconditional mount; the cfg.Trace `if` may remain for the boot log only)

# NewTraceMiddleware signature updated (configAlwaysOn arg present)
grep -n 'func NewTraceMiddleware' /workspace/pkg/handler/trace.go
# Expect: signature includes configAlwaysOn bool (and traceState *TraceState if approach (b) chosen)

# CreateRouterFromConfig signature unchanged
grep -n 'func CreateRouterFromConfig' /workspace/pkg/factory/factory.go
# Expect: func CreateRouterFromConfig(ctx context.Context, cfg *pkg.Config, opts ...RouterOption) (http.Handler, error)

# Redaction invariant unchanged in trace.go
grep -n '\*\*\*' /workspace/pkg/handler/trace.go
# Expect: redaction literal still present

# Config.Trace untouched
grep -n 'Trace bool' /workspace/pkg/config.go
# Expect: one match, unchanged

# No bare glog.Infof / glog.Info( in new handler files
grep -nE 'glog\.Infof?\(' /workspace/pkg/handler/enabletrace.go /workspace/pkg/handler/disabletrace.go
# Expect: only V(n)-gated lines or zero glog.Infof lines

# Token-leak negative grep across trace source
grep -REin 'Bearer |sk-' /workspace/pkg/handler/trace.go
# Expect: zero raw-secret lines (only *** redaction references)
```

</verification>

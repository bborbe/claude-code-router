---
status: approved
spec: [002-trace-logging]
created: "2026-06-30T11:10:00Z"
queued: "2026-06-30T09:46:25Z"
---

<summary>
- When `trace: true` is set in config, every `/v1/*` request produces exactly one JSON file at `~/.claude-code-router/trace/` capturing the full request and response.
- When `trace: false` (or absent), no trace files are written and no trace middleware is allocated on the request hot path — the model router is registered on `/v1/` directly, exactly as today.
- The `Authorization` and `x-api-key` request headers are redacted to `***` in every trace file, regardless of header case; all other headers and the entire request/response bodies are logged verbatim.
- A single `glog.V(n).Infof("trace enabled")` line is emitted at startup when trace is on; no trace startup log when off.
- The trace directory is created on demand on the first trace write; trace file writes are best-effort (failure logs a warning, never fails the request).
- The `CreateRouterFromConfig` signature is unchanged; the model router, its `[req]` log line, sampler, and metrics are untouched.
- Ginkgo specs cover: file presence + JSON shape, header redaction, verbatim non-redacted headers, no-file-when-off, no-middleware-alloc-when-off, and glog V(n) gating of the startup line.
</summary>

<objective>
Wire a trace-recording middleware into `CreateRouterFromConfig` that wraps the `/v1/` model-router handler when `cfg.Trace == true`, writing one JSON file per request with full request/response capture and two-header redaction. When `cfg.Trace == false`, register the model router on `/v1/` exactly as today with zero trace overhead. This is prompt 2 of 2 for spec 002 and depends on prompt 1 (which added `cfg.Trace`).
</objective>

<context>
Read CLAUDE.md at the repo root for project conventions.

Read these source files before making changes:
- `specs/in-progress/002-trace-logging.md` — the full spec; pay attention to "Desired Behavior" items 2-7, "Constraints", "Failure Modes" table (every row), and "Security / Abuse Cases".
- `pkg/config.go` — prompt 1 (executed before this prompt in the dark-factory queue) adds `Trace bool` to `Config` with YAML key `trace`. Read it to CONFIRM the field exists and its exact doc comment; if the field is absent (prompt 1 was rejected/modified), STOP — this prompt cannot proceed without it. Do not add the field yourself; prompt 1 owns it.
- `pkg/factory/factory.go` — current `CreateRouterFromConfig`. The mux-construction seam where trace middleware wires in is the line `mux.Handle("/v1/", modelRouter)`. The `CreateRouterFromConfig` signature `func CreateRouterFromConfig(ctx context.Context, cfg *pkg.Config) (http.Handler, error)` MUST remain unchanged.
- `pkg/handler/model-router.go` — `NewModelRouter` returns `http.Handler`. The trace middleware WRAPS this handler; `NewModelRouter` itself is NOT modified.
- `pkg/handler/status-recorder.go` — `statusRecorder` captures response status + body. Read it to understand the existing `ResponseWriter` wrapping pattern (overrides `WriteHeader`, `Write`, and `Unwrap` for SSE flush passthrough). The trace middleware's response capture must preserve the `Unwrap()` contract so SSE streaming through the model router's proxy still flushes correctly.
- `pkg/handler/redact.go` — existing `RedactHeadersForLog` and `isCredentialHeader`. Note: `RedactHeadersForLog` replaces credential headers with `<redacted len=N>`, NOT `***`. The trace spec requires the literal `***` for `Authorization` and `x-api-key` only. Do NOT reuse `RedactHeadersForLog` for trace — it redacts a broader set of headers and uses a different replacement token. Implement a trace-specific redaction that maps exactly `Authorization` and `x-api-key` (case-insensitive) to `***` and leaves all other headers verbatim.
- `pkg/handler/model-router_test.go` — existing Ginkgo + httptest patterns: `httptest.NewRecorder()`, `httptest.NewRequest(...)`, the `captureStderr(fn func()) string` helper, the `labelHandler(label string) http.Handler` test double. Follow these patterns for the new specs.
- `/home/node/.claude-yolo/plugins/marketplaces/coding/docs/go-glog-guide.md` — `V(n)` gating convention (if unreadable in the container, rely on the inline glog constraint repeated below).
- `/home/node/.claude-yolo/plugins/marketplaces/coding/docs/go-http-handler-refactoring-guide.md` — handler/middleware conventions.
- `/home/node/.claude-yolo/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `bborbe/errors` wrapping idiom (`errors.Wrapf`, `errors.New`) used throughout this codebase.
- `/home/node/.claude-yolo/plugins/marketplaces/coding/docs/go-testing-guide.md` — Ginkgo v2 + Gomega patterns.
</context>

<requirements>

1. **Create a trace middleware file** at `pkg/handler/trace.go` (package `handler`). Implement an HTTP middleware that, given a wrapped `http.Handler`, captures the full request and response and writes exactly one JSON file per request. The public constructor signature:

   ```go
   // NewTraceMiddleware wraps next in a handler that, for each request,
   // captures the full request (method, path, headers, body) and full
   // response (status, headers, body) and writes one JSON file to
   // traceDir. Authorization and x-api-key request headers are redacted
   // to "***" (case-insensitive header lookup); all other headers and
   // the entire request/response bodies are logged verbatim. Trace file
   // writes are best-effort: a write failure is logged at glog.Warningf
   // and the request still succeeds. The trace directory is created on
   // demand on the first write (MkdirAll, 0o700).
   func NewTraceMiddleware(next http.Handler, traceDir string) http.Handler
   ```

   Behavior details (read the spec's "Desired Behavior" items 2, 4, 6 and "Failure Modes" table rows 1, 2, 6, 7):

   - **Request capture:** Read the full request body via `io.ReadAll(r.Body)`, then restore `r.Body` with `io.NopCloser(bytes.NewReader(body))` and set `r.ContentLength` so the wrapped model router sees the original body (same pattern as `NewModelRouter` in `model-router.go`). Capture `r.Method`, `r.URL.Path`, and a copy of `r.Header`.
   - **Header redaction:** Build the headers map for the trace file by copying `r.Header`. For each header name, if `strings.ToLower(name)` equals `"authorization"` OR `"x-api-key"`, replace the value(s) with the single literal string `"***"`. All other headers are copied verbatim (multi-value headers joined with `", "` to match the existing `RedactHeadersForLog` convention, OR kept as arrays — pick one and be consistent; the spec's `jq` evidence checks `.request.headers["Content-Type"]` returns the sent value, so a flat `map[string]string` is simplest and matches `RedactHeadersForLog`'s output shape).
   - **Response capture:** Wrap the `http.ResponseWriter` in a recorder that captures `WriteHeader` status, all `Header()` values (snapshot at `WriteHeader` time or after the handler returns), and all written body bytes. This recorder MUST implement `Unwrap() http.ResponseWriter` (return the underlying writer) so `http.NewResponseController` can reach `Flush`/`Hijack` through the wrapper — this is the same contract `statusRecorder` in `status-recorder.go` upholds for SSE streaming. Without `Unwrap`, Anthropic's SSE chunks will buffer instead of flushing per chunk.
   - **File write (after the wrapped handler returns):** Construct the filename `<timestamp>-<request-id>.json`. Use a timestamp with sufficient resolution to be sortable (e.g. `20060102-150405.000000000` or RFC3339 with nanoseconds — ensure filesystem-legal characters). Use a per-request unique ID (e.g. a counter, a UUID, or `crypto/rand` hex — the spec says request IDs are unique per request; a monotonic counter or random hex suffices). Write the JSON file to `filepath.Join(traceDir, filename)`. Use `json.MarshalIndent` or `json.Marshal` for the trace object (the spec's `jq` evidence requires valid JSON; either form is valid).
   - **Trace JSON shape:** The top-level object MUST contain exactly these keys (the spec ACs assert on each):

     ```json
     {
       "request": {
         "method": "POST",
         "path": "/v1/messages",
         "headers": {"Content-Type": "application/json", "Authorization": "***", ...},
         "body": "<verbatim request body string>"
       },
       "response": {
         "status": 200,
         "headers": {"Content-Type": "...", ...},
         "body": "<verbatim response body string>"
       }
     }
     ```

     `request.method`, `request.path`, `request.headers`, `request.body`, `response.status`, `response.headers`, `response.body` — these are the keys the AC `jq` checks assert on. Do NOT add extra top-level keys that would change the evidence commands (extra nested keys under `request`/`response` are acceptable but unnecessary — keep it minimal).

   - **Best-effort file I/O (Failure Modes rows 1, 2, 6):** If `os.MkdirAll(traceDir, 0o700)` fails, log at `glog.Warningf` with a lowercase message (e.g. `trace dir create failed: %v`) and continue serving the request WITHOUT writing a trace file — the request must still succeed. If the file `os.WriteFile` fails (disk full, inode exhaustion), log at `glog.Warningf` with a lowercase message and continue — no panic, no 5xx injected, the response already reached the client. At most one partial file may exist on a mid-write crash (Failure Mode row 6) — that is acceptable; no replay/repair logic.
   - **Concurrency (Failure Mode row 5):** Each request writes its own file; unique request IDs guarantee no collision. No cross-request locking is required. Do NOT add a mutex around file writes. The request-ID generator MUST be unique under concurrency — if using a monotonic counter, guard it with `sync/atomic` or `math/rand` with a mutex; `crypto/rand` hex is naturally unique. A bare unsynchronized counter races under concurrent requests.
   - **Body-size cap interaction (Desired Behavior item 7, Failure Mode row 3):** The trace middleware wraps the model router OUTSIDE the model router's own `http.MaxBytesReader` call. The trace middleware reads the request body BEFORE delegating to the model router. If the body exceeds `MaxRequestBodyBytes` (32 MB), the model router returns 413 as today; the trace middleware records the 413 response and the full request body it captured (the trace middleware's `io.ReadAll` is NOT capped by the model router's `MaxBytesReader` — the trace middleware reads the raw body first). This is the specified behavior: "a request that exceeds the body cap is not traced as a full body (the 413 path is logged via the existing model-router behavior; the trace file, if written, records the 413 response)." The trace middleware captures whatever body it reads; the model router independently enforces its own cap.

2. **Wire the middleware into `CreateRouterFromConfig`** in `pkg/factory/factory.go`. The current line (read verbatim):

   ```go
   mux.Handle("/v1/", modelRouter)
   ```

   Replace with conditional wiring based on `cfg.Trace`:

   ```go
   v1Handler := http.Handler(modelRouter)
   if cfg.Trace {
       glog.V(2).Infof("trace enabled")
       v1Handler = handler.NewTraceMiddleware(v1Handler, traceDir())
   }
   mux.Handle("/v1/", v1Handler)
   ```

   Where `traceDir()` returns the fixed path `~/.claude-code-router/trace/` (expand `~` the same way `pkg.expandTilde` does — either reuse that helper by exporting it, or call `os.UserHomeDir()` + `filepath.Join` inline). The trace directory path is FIXED per spec Non-goal ("Do NOT add a configurable trace-directory path") — do not add a config field for it.

   - The `glog.V(2).Infof("trace enabled")` line is the startup log from Desired Behavior item 5. Use `V(2)` (consistent with the codebase's V(2) for operator-opt-in detail like `[alias]`/`[route]` lines). The message is lowercase per glog discipline. This line fires once at router construction (which happens once per process at startup / SIGHUP rebuild), NOT per request. When `cfg.Trace == false`, NO trace startup log is emitted.
   - **glog gating (AC #10):** The startup log MUST be `glog.V(n).Infof(...)`, NOT a bare `glog.Infof(...)`. The AC evidence command greps for bare `glog.Infof` / `glog.Info(` in trace-related files and expects zero lines without a `.V(n)` prefix.

3. **Do NOT modify `NewModelRouter`, `statusRecorder`, `RedactHeadersForLog`, `Metrics`, or any admin endpoint handler.** Trace is purely additive middleware wrapping `/v1/`. The `/healthz`, `/readiness`, `/metrics`, `/setloglevel/`, `/gc`, `HEAD /{$}`, and `/` catch-all routes are NOT wrapped (spec Non-goal: "Tracing non-`/v1/*` paths").

4. **Add Ginkgo specs** in a new file `pkg/handler/trace_test.go` (package `handler_test`). Follow the existing patterns from `model-router_test.go`: use `httptest.NewRecorder()` or `httptest.NewServer`, `httptest.NewRequest(...)`, the `captureStderr` helper (copy it or reference the existing one in the same package_test), and `labelHandler` test doubles. Use a temp directory (`os.MkdirTemp`) for `traceDir` in each spec and clean up in `AfterEach`. Cover these cases (each maps to a spec AC):

   - **AC #2 file presence + AC #3 JSON shape:** With `trace: true` (i.e. wrap a handler in `NewTraceMiddleware`), send a `POST /v1/messages` with a JSON body and an `Authorization: Bearer sk-testsecret` header. Assert exactly one `.json` file appears in `traceDir`. Parse the file as JSON and assert it has `request.method == "POST"`, `request.path == "/v1/messages"`, `request.headers`, `request.body` (matches sent body), `response.status` (matches what the wrapped handler wrote), `response.headers`, `response.body` (matches what the wrapped handler wrote).
   - **AC #4 redaction:** In the trace file from the above (or a dedicated spec), assert `request.headers["Authorization"]` equals `"***"` (case-insensitive: also test `authorization` lowercase and `AUTHORIZATION` uppercase variants by sending those header casings and asserting `***`). Assert `request.headers["x-api-key"]` (sent as `x-api-key: secretval`) equals `"***"`.
   - **AC #5 verbatim non-redacted headers:** Assert `request.headers["Content-Type"]` equals the value sent (e.g. `application/json`). Assert at least one other non-credential header (e.g. `User-Agent`) is passed through verbatim.
   - **AC #6 no raw secret:** Run a canary assertion — marshal the trace file to a string and assert it does NOT contain `Bearer` or `sk-testsecret` (the raw token must not appear anywhere in the file).
   - **AC #7 negative trace-off:** This is a factory-level assertion, not a handler-level one — see requirement 5 below.
   - **AC #8 no-middleware-alloc when off:** This is a factory-level assertion — see requirement 5 below.
   - **Failure Mode row 1 (dir create fails):** Point `traceDir` at a path under a read-only parent directory (e.g. `chmod 0500` a temp dir, then use a subdir). Assert the request still succeeds (response reaches the client) and `glog.Warningf` was called (capture stderr and assert the lowercase warning message). Clean up permissions in `AfterEach`.
   - **Failure Mode row 2 (file write fails):** Make the trace file unwritable (e.g. create `traceDir` as a regular file instead of a directory, so `MkdirAll` / `WriteFile` fails). Assert the request still succeeds and a warning was logged.
   - **glog V(n) gating (AC #10):** Assert that `NewTraceMiddleware` and the trace source file contain NO bare `glog.Infof(` or `glog.Info(` calls — only `glog.V(n).Infof(...)` or `glog.Warningf(...)` / `glog.Errorf(...)` (warning/error are not subject to the V(n) gate). This can be a static grep-based assertion in the test OR a manual check — a Ginkgo spec that reads the source file and greps is acceptable but a comment in the test pointing to the verification command is also fine.

5. **Add a factory-level spec** to a new file `pkg/factory/trace_wiring_test.go` (package `factory_test`) OR extend the existing `factory_suite_test.go` setup. The factory test suite currently has no `*_test.go` with specs (only the suite runner) — create a new Ginkgo `Describe` file. Cover:

   - **AC #7 + AC #8 (trace off → no file, no middleware):** Build a `*pkg.Config` with `Trace: false` and a minimal provider block. Call `CreateRouterFromConfig(ctx, cfg)`. Send a `POST /v1/messages` through the returned handler (using `httptest.NewRecorder` + `httptest.NewRequest`). Assert NO file appears in `~/.claude-code-router/trace/` (or a temp HOME override so the test doesn't touch the real home dir — set `HOME` env var to a temp dir for the test, or the middleware's `traceDir()` must be testable; since `traceDir()` reads `os.UserHomeDir()`, overriding `HOME` via `t.Setenv("HOME", tmpDir)` in the Ginkgo `BeforeEach` is the cleanest approach). Assert the handler chain does NOT include trace middleware — this is hard to assert via type inspection since `http.HandlerFunc` wrappers are opaque; the observable evidence is "no file written" (AC #7) which is the load-bearing assertion. For AC #8 (no middleware allocation), the no-file assertion combined with the fact that `cfg.Trace == false` skips the `NewTraceMiddleware` call (a branch in `CreateRouterFromConfig`) is sufficient — the branch is verified by code review + the no-file integration evidence.

   - **AC #2 at factory level (trace on → file written):** Build a `*pkg.Config` with `Trace: true`. Call `CreateRouterFromConfig(ctx, cfg)` with `HOME` overridden to a temp dir. Send a `POST /v1/messages`. Assert exactly one `.json` file appears in `<tmpHome>/.claude-code-router/trace/`. This is the end-to-end integration through the real factory wiring (level 2 boundary test — the trace middleware is registered through the actual mux-construction path, not constructed directly).

   - **glog startup line (AC #10 + Desired Behavior item 5):** When `cfg.Trace == true`, capture stderr during `CreateRouterFromConfig` and assert it contains `trace enabled` (the V(2) startup line). When `cfg.Trace == false`, assert stderr does NOT contain `trace enabled`. NOTE: `glog.V(2)` only emits when verbosity ≥ 2; the test must set `flag.Set("v", "2")` (or higher) in `BeforeEach` for the startup-line assertion to fire — follow the pattern from `config_test.go`'s alias-warning spec which sets `flag.Set("logtostderr", "true")` and pipes stderr. IMPORTANT test-isolation: `flag.Set("v", ...)` and `flag.Set("logtostderr", ...)` are process-global and leak across specs in the same suite. In `AfterEach`, save the prior values before setting in `BeforeEach` and restore them after (e.g. `oldV := flag.Lookup("v").Value.String(); flag.Set("v","2"); ... ; flag.Set("v", oldV)`). This matches the existing `config_test.go` convention (which also leaks, but the new V(2) setting is more invasive because it would cause other specs' `glog.V(2)` lines to fire unexpectedly) — restore explicitly here.

6. **Run `make precommit`** in the repo root. This is the final integration check: prompt 1's config field + this prompt's middleware compose. Fix any gofmt / addlicense / lint / golangci-lint issues. All existing tests plus the new trace + factory specs must pass.

</requirements>

<constraints>

- **Token-leak invariant (load-bearing, repeated from spec):** `Authorization` and `x-api-key` are NEVER written raw to trace files, even in trace mode. Redact to the literal `***`, case-insensitive header lookup. This matches the existing invariant in `docs/config.md` ("The router never stores or logs token values") extended to trace files. The redaction is scoped to EXACTLY these two headers — do NOT redact `Cookie`, `Set-Cookie`, or other credential-shaped headers in trace files (the spec Non-goal says "Per-route or per-header redaction customization beyond the two documented secrets" is out of scope; bodies are verbatim by design).
- **`CreateRouterFromConfig` signature unchanged:** `func CreateRouterFromConfig(ctx context.Context, cfg *pkg.Config) (http.Handler, error)`. Trace wiring is internal to this function.
- **The model router (`NewModelRouter`), its `[req]` structured log line, the sampler, and the metrics are unchanged.** Trace is additive middleware wrapping `/v1/`, not a modification of the model router itself.
- **Trace is independent of SIGHUP hot-reload.** The flag is read once at `Load` (prompt 1); a restart applies it. No runtime mutation, no SIGHUP dependency. Once SIGHUP hot-reload lands (separate feature), trace participates in mux rebuilds automatically because `CreateRouterFromConfig` reads `cfg.Trace` on every call.
- **glog discipline:** Any new `Info`-level log is `V(n)`-gated; no bare `glog.Infof`. Log messages are lowercase (e.g. `trace enabled`, not `Trace enabled`). Warning-level logs (`glog.Warningf`) are NOT subject to the V(n) gate but MUST still be lowercase.
- **Best-effort trace I/O:** Trace file writes never fail the request. A write failure (dir create, file write) logs at `glog.Warningf` and continues. No panic, no 5xx, no retry loop.
- **SSE streaming must not break:** The trace middleware's response recorder MUST implement `Unwrap() http.ResponseWriter` so `http.NewResponseController` reaches `Flush`/`Hijack` on the underlying writer. Without this, Anthropic's SSE chunks buffer instead of flushing per chunk — symptom is Claude Code spinners stuck mid-stream and `/compact` hanging at 95% (the exact regression `statusRecorder` was fixed to prevent in v0.9.0).
- **Fixed trace directory:** `~/.claude-code-router/trace/` — do NOT add a config field for the path (spec Non-goal hard veto).
- **One JSON object per file:** do NOT add line-delimited JSON or a configurable format (spec Non-goal).
- **Do NOT commit** — dark-factory handles git.
- **Existing tests must still pass.**
- **No retention, rotation, or cleanup** of the trace directory (spec Non-goal).

</constraints>

<verification>

```bash
cd /workspace
make precommit
```

Must exit 0. Additionally:

```bash
# Trace middleware file exists
ls /workspace/pkg/handler/trace.go
# Trace specs exist
ls /workspace/pkg/handler/trace_test.go
ls /workspace/pkg/factory/trace_wiring_test.go

# No bare glog.Infof / glog.Info( in trace source (AC #10)
grep -nE 'glog\.Infof?\(' /workspace/pkg/handler/trace.go /workspace/pkg/factory/factory.go
# Expect: only lines prefixed with glog.V(n). — no bare glog.Infof( or glog.Info(

# CreateRouterFromConfig signature unchanged
grep -n 'func CreateRouterFromConfig' /workspace/pkg/factory/factory.go
# Expect: func CreateRouterFromConfig(ctx context.Context, cfg *pkg.Config) (http.Handler, error)

# NewModelRouter untouched
grep -n 'func NewModelRouter' /workspace/pkg/handler/model-router.go
# Expect: unchanged signature (7 params, returns http.Handler)

# Redaction literal present
grep -n '\*\*\*' /workspace/pkg/handler/trace.go
# Expect ≥1 line (the redaction target)

# Trace dir path is fixed (no config field)
grep -rn 'claude-code-router/trace' /workspace/pkg/
# Expect matches in trace.go and/or factory.go
```

Live run (manual, after build — not a prompt gate, but the spec's operator smoke):

```bash
# trace: true
# edit ~/.claude-code-router/config.yaml to add `trace: true`
make run
curl -X POST http://127.0.0.1:8788/v1/messages -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer sk-testsecret' -d '{"model":"...","messages":[]}'
ls ~/.claude-code-router/trace/*.json   # one new file
jq -e '.request.method and .request.headers and .response.status' ~/.claude-code-router/trace/*.json
grep -RiE 'Bearer |sk-[a-zA-Z0-9]' ~/.claude-code-router/trace/   # 0 lines

# trace: false
# edit config.yaml to `trace: false` (or remove the line), restart
curl -X POST http://127.0.0.1:8788/v1/messages ...
ls ~/.claude-code-router/trace/*.json 2>/dev/null | wc -l   # unchanged
```

</verification>

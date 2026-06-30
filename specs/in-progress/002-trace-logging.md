---
status: verifying
approved: "2026-06-30T09:37:43Z"
generating: "2026-06-30T09:46:22Z"
prompted: "2026-06-30T09:46:22Z"
verifying: "2026-06-30T10:07:52Z"
branch: dark-factory/trace-logging
---

## Summary

- A `trace:` boolean in `config.yaml` toggles per-request trace logging for `/v1/*` traffic. When `true`, every request writes one JSON file capturing the full request and response to `~/.claude-code-router/trace/`. When `false`, no files are written and the middleware is absent from the hot path.
- Each trace file captures request method, path, all headers (with `Authorization` and `x-api-key` redacted to `***`), full request body, response status, response headers, and full response body.
- The feature wires into the existing mux-construction seam (`CreateRouterFromConfig`), wrapping the `/v1/` routes only. It is independent of and does not gate on the SIGHUP hot-reload work (spec 002); once 002 lands, trace participates in mux rebuilds automatically.
- Token-leak discipline already enforced for glog applies to trace files: `Authorization` and `x-api-key` are never written raw, even in trace mode. Bodies are logged verbatim by design (operator's data, operator's disk).

## Problem

Operators debugging routing decisions, alias resolution, provider responses, and streaming failures have no per-request artifact. The existing structured `[req]` log line carries method/path/model/provider/status/latency but omits headers and bodies, and it is sampler-gated on 200s. When an upstream returns an unexpected body, an auth header is being rewritten incorrectly, or a model-field rewrite misshapes the payload, the operator has to reproduce against the upstream directly — losing the router's view. A one-file-per-request trace gives a complete, offline-reviewable record of what the router saw and what it forwarded.

## Goal

When `trace: true` is set in `config.yaml`, every `/v1/*` request produces exactly one JSON file at `~/.claude-code-router/trace/<timestamp>-<request-id>.json` containing the complete request (method, path, headers with `Authorization` + `x-api-key` redacted, body) and complete response (status, headers, body). When `trace: false` (or absent), no trace files are written and no trace middleware is allocated on the request hot path. The flag is read once at config load; a restart applies it.

## Non-goals

- Retention, rotation, or cleanup of the trace directory. The operator runs `rm` manually.
- Redacting secrets inside request/response BODIES. Only the `Authorization` and `x-api-key` HEADERS are redacted; body content is logged verbatim by design — operator's data, operator's disk.
- Structured or rotating log aggregation. One JSON file per request is the v1 format.
- Tracing non-`/v1/*` paths (`/healthz`, `/readiness`, `/metrics`, `/setloglevel/`, `/gc`, `/`, `HEAD /`).
- Per-route or per-header redaction customization beyond the two documented secrets.
- SIGHUP hot-reload of the `trace` flag. Separate feature (spec 002); trace participates automatically once it lands, not a trace concern.
- Do NOT add a configurable trace-directory path, redaction-list override, or body-size cap for trace files — invariants; if a future consumer demands variation, that is a separate spec.
- Do NOT add a configurable trace-output format (line-delimited JSON, etc.) — one JSON object per file is the v1 format.

## Acceptance Criteria

- [ ] `make precommit` exits 0 in the changed module — evidence: exit code
- [ ] With `trace: true` in `config.yaml`, a `curl POST /v1/messages` causes exactly one new file to appear at `~/.claude-code-router/trace/<timestamp>-<request-id>.json` — evidence: `ls ~/.claude-code-router/trace/*.json` returns exactly one more file after the request than before
- [ ] That trace file is valid JSON containing the keys `request.method`, `request.path`, `request.headers`, `request.body`, `response.status`, `response.headers`, `response.body`, and `request.method`/`request.path`/`response.status` match the curl invocation and upstream status — evidence: `jq -e '.request.method and .request.path and .request.headers and .request.body and .response.status and .response.headers and .response.body' <file>` exits 0 AND `jq -r '.request.method' <file>` matches the sent method AND `jq -r '.request.path' <file>` matches the sent path AND `jq -r '.response.status' <file>` matches the upstream status
- [ ] In every trace file, the `request.headers.Authorization` and `request.headers.x-api-key` values (case-insensitive header lookup) are the literal string `***` — evidence: `jq -r '.request.headers | to_entries[] | select(.key | ascii_downcase | IN("authorization","x-api-key")) | .value' <file>` returns only `***` lines
- [ ] Non-redacted headers are logged verbatim (redaction is scoped to only `Authorization` + `x-api-key`) — evidence: `jq -r '.request.headers["Content-Type"]' <file>` matches the `Content-Type` header sent by the curl invocation
- [ ] No raw secret material appears in any trace file — evidence: `grep -RiE 'Bearer |sk-[a-zA-Z0-9]' ~/.claude-code-router/trace/` returns 0 lines across all trace files (negative evidence)
- [ ] With `trace: false` (or `trace` absent) in `config.yaml`, a `curl POST /v1/messages` writes zero files to `~/.claude-code-router/trace/` — evidence: `ls ~/.claude-code-router/trace/*.json 2>/dev/null | wc -l` is unchanged before vs after the request (negative evidence)
- [ ] With `trace: false`, no trace-middleware allocation appears on the request hot path — evidence: a unit test asserts the handler returned by `CreateRouterFromConfig` with `cfg.Trace == false` registers the model router on `/v1/` directly, not wrapped in a trace-recording handler (handler-chain assertion via type/identity check, distinct from AC #8's no-file observable)
- [ ] `docs/config.md` documents the `trace` flag, its `true`/`false` values, the trace-file location `~/.claude-code-router/trace/`, and the header-redaction behavior — evidence: `grep -n 'trace' docs/config.md` returns line ≥1 AND `grep -n 'claude-code-router/trace' docs/config.md` returns line ≥1
- [ ] `CHANGELOG.md` has a `## Unreleased` section containing at least one bullet mentioning `trace` — evidence: `grep -n '## Unreleased' CHANGELOG.md` returns line ≥1 AND `grep -ni 'trace' CHANGELOG.md` returns ≥1 line at or after the `## Unreleased` line
- [ ] glog Info calls added by this feature are `V(n)`-gated (no bare `glog.Infof`) — evidence: `grep -n 'glog.Infof\|glog.Info(' $(git grep -l 'trace' -- '*.go')` returns 0 lines without a `.V(n)` prefix on the same call (negative evidence)

## Verification

```
make precommit
```

Live run (manual, after build):

```
# trace: true
# edit ~/.claude-code-router/config.yaml to add `trace: true`
make run
curl -X POST http://localhost:PORT/v1/messages -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer sk-testsecret' -d '{"model":"...","messages":[]}'
ls ~/.claude-code-router/trace/*.json   # one new file
jq -e '.request.method and .request.headers and .response.status' ~/.claude-code-router/trace/*.json
grep -RiE 'Bearer |sk-[a-zA-Z0-9]' ~/.claude-code-router/trace/   # 0 lines

# trace: false
# edit config.yaml to `trace: false` (or remove the line), restart
curl -X POST http://localhost:PORT/v1/messages ...
ls ~/.claude-code-router/trace/*.json 2>/dev/null | wc -l   # unchanged
```

## Desired Behavior

1. `config.yaml` accepts a top-level boolean `trace` field. When absent, it defaults to `false`. `pkg.Load` parses it into `Config`.
2. When `cfg.Trace == true`, `CreateRouterFromConfig` wraps the `/v1/` model-router handler in a trace-recording middleware that captures the full request (method, path, all headers, full body) and full response (status, all headers, full body) and writes exactly one JSON file per request to `~/.claude-code-router/trace/<timestamp>-<request-id>.json`.
3. When `cfg.Trace == false`, `CreateRouterFromConfig` registers the model router on `/v1/` exactly as today — no trace middleware in the handler chain, no trace file writes, no per-request trace allocation.
4. The trace middleware redacts the `Authorization` and `x-api-key` request headers to the literal `***` in every trace file, regardless of case. All other headers and the entire request body and response body are logged verbatim.
5. A single `glog.V(n).Infof("trace enabled")` line (lowercase message) is emitted at startup when `cfg.Trace == true`; when `false`, no trace-related startup log is emitted.
6. The trace directory `~/.claude-code-router/trace/` is created on demand (first trace write) if it does not exist; it is not created when `trace: false`.
7. Trace middleware observes the existing `MaxRequestBodyBytes` 32 MB cap path — a request that exceeds the body cap is not traced as a full body (the 413 path is logged via the existing model-router behavior; the trace file, if written, records the 413 response).
8. The trace flag is read once at `Load`; changing it requires a router restart. No runtime mutation, no SIGHUP dependency.

## Constraints

- `Config` struct gains a `Trace bool` field (YAML key `trace`); the existing `Router`, `Providers`, `Aliases` fields and their YAML keys are unchanged. Backward compatibility: a config without `trace:` loads exactly as before (defaults `false`).
- `CreateRouterFromConfig` signature is unchanged: `func CreateRouterFromConfig(ctx context.Context, cfg *pkg.Config) (http.Handler, error)`. Trace wiring is internal to this function.
- The model router (`NewModelRouter`), its `[req]` structured log line, the sampler, and the metrics are unchanged. Trace is additive middleware wrapping `/v1/`, not a modification of the model router itself.
- Existing `make precommit` gates (gofmt, golangci-lint, Ginkgo v2 + Gomega tests) pass.
- glog discipline: any new `Info`-level log is `V(n)`-gated; no bare `glog.Infof`. Log messages are lowercase (e.g. `trace enabled`, not `Trace enabled`).
- Token-leak invariant: `Authorization` and `x-api-key` are never written raw to trace files. This matches the existing invariant documented in `docs/config.md` ("The router never stores or logs token values") extended to trace files.
- `.dark-factory.yaml` workflow (`direct`, `autoGeneratePrompts: false`) is unchanged — manual prompt generation after approve.
- No `Post-Deploy` AC markers: all ACs are build-time / local-run / file-content evidence; no deployed-cluster freshness check applies.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---------|-------------------|----------|
| `trace: true` but `~/.claude-code-router/trace/` cannot be created (permission denied, disk full) | The router logs the creation error at `glog.Warningf` and continues serving `/v1/*` requests without trace files; no request is failed due to trace-write error | Operator fixes the directory permissions/disk and restarts the router; trace files resume |
| Trace-file write fails mid-request (disk full, inode exhaustion) | The request is still served correctly (response reaches the client); the trace write is best-effort and logged at `glog.Warningf`; no panic, no 5xx injected | Operator frees disk/inodes; subsequent requests trace normally |
| Request body exceeds `MaxRequestBodyBytes` (32 MB) | The model router returns 413 as today; the trace middleware records the 413 response (no full request body captured past the cap) | Operator reduces request size; no trace-specific recovery |
| `trace` key present but non-boolean (e.g. `trace: maybe`, `trace: 123`) | `yaml.Unmarshal` into `Config.Trace bool` fails `Load` with the existing "parse config" error path | Operator corrects the YAML; restart |
| `trace: "yes"` / `"no"` / `"on"` / `"off"` (quoted YAML 1.1 bool words) | `gopkg.in/yaml.v3` applies YAML 1.1 bool coercion even to quoted strings, so `"yes"`→`true`, `"no"`→`false`. These do NOT error — they coerce. Operator's quoted `"yes"` silently enables trace. (Verified at implementation time; the test in `config_test.go` documents this coercion.) | Operator uses an unambiguous bool (`true`/`false`) or a non-coercible value to surface a parse error |
| Concurrent `/v1/*` requests with `trace: true` | Each request writes its own `<timestamp>-<request-id>.json` file; request IDs are unique per request, so no file collision; no cross-request locking required | None — correctness holds under concurrency |
| Router crash mid-trace-write | At most one partial `<timestamp>-<request-id>.json` file on disk; no response was lost to the client (trace write happens after the response is observed) | Operator `rm`s the partial file; no replay/repair needed |
| Clock skew / non-monotonic timestamp | Two files may share a timestamp prefix; the `<request-id>` suffix disambiguates; file ordering by name is best-effort, not a correctness invariant | None — uniqueness preserved by request-id suffix |

## Security / Abuse Cases

- What an attacker can control: the request path, method, headers, and body (they are the client). The response headers and body come from the upstream provider.
- What crosses trust boundaries: client-supplied request data is written to disk verbatim (bodies) and with two-header redaction (Authorization, x-api-key). Upstream response data is written verbatim.
- What can hang, retry forever, or race: trace file I/O is synchronous with request handling — a slow disk could add latency to `/v1/*` requests. This is an accepted v1 trade-off (operator's local disk; trace is opt-in). No retry loop on write failure (best-effort, one warning per failure).
- What data/path/input must be validated: the trace-directory path is fixed (`~/.claude-code-router/trace/`), not derived from request input. The filename `<timestamp>-<request-id>.json` uses a request-generated ID, not client-supplied input. No path traversal surface.
- Secret-at-rest: trace files contain verbatim request/response bodies which may include API keys, PII, or conversation content. This is by design (operator's data, operator's disk). The `~/.claude-code-router/trace/` directory inherits the operator's home-directory permissions; `config.yaml` already requires `chmod 600` per `docs/config.md`, and operators should apply the same discipline to the trace directory. The two header redactions (`Authorization`, `x-api-key`) are the only enforced redactions; body redaction is a non-goal.

## Suggested Decomposition

This spec touches two code layers (config + factory/middleware). It is within single-spec scope (DB × AC = 8 × 12 = 96 > 50 by the raw count, but the ACs are tightly coupled to a single behavior — trace on/off — and decomposition into multiple specs would split a single invariant across artifacts). A single implementation prompt is appropriate; if the prompt-creator splits, the natural seam is config-field vs middleware-wiring:

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Add `Trace bool` to `Config` + `Load` parsing + `docs/config.md` doc + CHANGELOG | 1, 8 | precommit, config-doc, changelog | — |
| 2 | Trace middleware in `CreateRouterFromConfig` wrapping `/v1/` + file write + header redaction + startup glog + tests | 2, 3, 4, 5, 6, 7 | file-presence, json-shape, redaction, negative-trace-off, no-middleware-alloc, glog-gating | prompt 1 (reads `cfg.Trace`) |

Rationale: prompt 1 establishes the flag the middleware reads; prompt 2 consumes it at the mux seam. Ordering matters because prompt 2's tests assert on `cfg.Trace == true/false` paths that prompt 1 introduces.

## Do-Nothing Option

Without this feature, operators continue debugging from the sampler-gated `[req]` log line and must reproduce upstream issues against the provider directly, losing the router's view of rewritten bodies and swapped auth headers. For a single-operator router on a local machine this is tolerable but slow for any non-trivial routing or alias-resolution issue. The cost of the feature is one opt-in boolean and a best-effort file write; the do-nothing cost is per-incident reproduction time. Recommend: ship the feature.

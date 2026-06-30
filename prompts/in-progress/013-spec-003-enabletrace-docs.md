---
status: approved
spec: [003-enabletrace-endpoint]
created: "2026-06-30T11:57:22Z"
queued: "2026-06-30T12:09:00Z"
---

<summary>
- The operator docs document the two new `/enabletrace` and `/disabletrace` endpoints, the bounded 5-minute TTL, and the auto-disable-on-expiry behavior.
- The docs mark the legacy `trace:` config flag as deprecated while confirming it still works as an always-on opt-in.
- The changelog gains an entry for the new toggle endpoints above the existing `## v0.14.0` section.
- No code changes in this prompt — docs and changelog only.
</summary>

<objective>
Ship the operator-facing documentation for the `/enabletrace` + `/disabletrace` endpoints and the `trace:` flag deprecation, plus the CHANGELOG entry. This is prompt 3 of 3 for spec 003 and depends on prompts 1 and 2 (the behavior must be settled before documenting it).
</objective>

<context>
Read CLAUDE.md at the repo root for project conventions.

Read these source files before making changes:
- `specs/in-progress/003-enabletrace-endpoint.md` — the full spec; pay attention to "Acceptance Criteria" items 10 and 11 (the doc + changelog evidence commands), "Non-goals" (do NOT remove the `trace:` flag — deprecate only; do NOT add a TTL config knob), and "Desired Behavior" items 1-8.
- `docs/config.md` — current config reference. The `## Schema` block already has a `trace: <bool>` line (added in spec 002). The `## Trace` section documents the v0.14.0 always-on behavior. This prompt EXTENDS the `## Trace` section with the new endpoints + TTL + deprecation note, and updates the `## Reload` section to mention the runtime toggle.
- `CHANGELOG.md` — top entry is `## v0.14.0` (there is NO `## Unreleased` section; spec 002's prompt 1 referenced creating one but the shipped CHANGELOG jumps from the header intro directly to `## v0.14.0`). Read the actual current top of file to confirm — do NOT assume a `## Unreleased` section exists. This prompt adds a new version section ABOVE `## v0.14.0` (the spec AC #11 says "either `## Unreleased` if that pattern is adopted, or a new version section above `## v0.14.0`"; since the repo has no `## Unreleased` convention, use a new version section `## v0.15.0`).
- `pkg/handler/enabletrace.go` and `pkg/handler/disabletrace.go` — prompt 2 (executed before this prompt) added these handlers. Read them to confirm the exact plaintext response bodies and the endpoint paths so the doc matches the implementation verbatim.
- `pkg/handler/trace_state.go` — prompt 1 added the `TraceTTLDefault = 5 * time.Minute` constant. Read it to confirm the exact value to document (5 minutes).
- `/home/node/.claude-yolo/plugins/marketplaces/coding/docs/changelog-guide.md` — bullet style for this project (bold lead phrase, operator-facing behavior). If unreadable in the container, mirror the existing `## v0.14.0` bullet style in CHANGELOG.md.
- `/home/node/.claude-yolo/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — doc conventions.
- If any container doc path above is unreadable in the YOLO container, fall back to the inline constraints repeated in `<constraints>` below.
</context>

<requirements>

1. **Update `docs/config.md` — `## Trace` section.** The current section (read verbatim) documents only the v0.14.0 config-flag behavior. EXTEND it (do not replace the existing content) with the new runtime-toggle behavior. Add the following content, adapted for prose flow but preserving every fact:

   - **New endpoints:** `POST /enabletrace` and `POST /disabletrace`, registered on the operator-local listener (`127.0.0.1:8788`), no auth (same trust model as `/setloglevel`). `curl -X POST http://127.0.0.1:8788/enabletrace` turns tracing on for a bounded 5-minute window; `curl -X POST http://127.0.0.1:8788/disabletrace` turns it off immediately and cancels the pending timer.
   - **TTL auto-disable:** the 5-minute window auto-disables on expiry — if the operator forgets `/disabletrace`, tracing stops on its own. Repeated `/enabletrace` calls reset the window (each cancels the prior timer and starts a fresh 5-minute window).
   - **No persistence:** the toggle is in-memory only. A router restart resets tracing to off (no trace files written until the next `/enabletrace`). The toggle does NOT depend on config reload or SIGHUP.
   - **Deprecation of `trace:` config flag:** the top-level `trace: true` config flag still works as an always-on opt-in (emits on every `/v1/*` request regardless of the toggle state — flag-OR-config), but is now deprecated in favor of the bounded `/enabletrace` toggle. Leaving `trace: true` on indefinitely is a disk and privacy hazard (full request/response bodies); the bounded toggle is the recommended path for capturing a single problematic exchange.
   - **Redaction unchanged:** `Authorization` and `x-api-key` are redacted to `***` in every trace file regardless of whether tracing was enabled via the config flag or the `/enabletrace` endpoint.
   - **No new knobs:** the 5-minute TTL is a hardcoded constant, not configurable via config file, query param, or request body.

2. **Update `docs/config.md` — `## Reload` section.** The current section says "Config changes require a router restart (no hot-reload in v1)". Add a sentence noting that the trace toggle (`/enabletrace` / `/disabletrace`) is the exception: it is a runtime toggle that does NOT require a restart, and does NOT participate in config reload (it is process-internal state). Keep this concise — one or two sentences.

3. **Update `docs/config.md` — `## Schema` block.** The existing `trace:` line is:

   ```
   trace: <bool>                         # optional; default false. When true, writes one JSON file per /v1/* request to ~/.claude-code-router/trace/
   ```

   Append `(deprecated — use POST /enabletrace for bounded trace windows; see ## Trace)` to the inline comment so operators scanning the schema see the deprecation pointer. Do NOT remove the line.

4. **Add a CHANGELOG entry** in `CHANGELOG.md`. Read the actual current top of the file to confirm there is no `## Unreleased` section (the shipped file jumps from the header intro to `## v0.14.0`). Insert a new `## v0.15.0` section immediately after the header intro lines (after the "Please choose versions by Semantic Versioning." line) and BEFORE `## v0.14.0`. Add one bullet mirroring the existing bullet style (bold lead phrase, operator-facing behavior):

   ```markdown
   ## v0.15.0

   - **feat: runtime trace toggle via /enabletrace and /disabletrace.** Two new operator-local HTTP endpoints (`POST /enabletrace`, `POST /disabletrace`) toggle per-request trace logging without a router restart. `enabletrace` turns tracing on for a bounded 5-minute window that auto-disables on expiry (repeated calls reset the window); `disabletrace` turns it off immediately and cancels the pending timer. The trace middleware is now mounted unconditionally on `/v1/` and consults a process-internal atomic flag per request (flag-OR-config: the legacy `trace:` config flag still works as an always-on opt-in, now deprecated). No persistence across restarts; the toggle does not depend on config reload or SIGHUP. `Authorization` and `x-api-key` redaction to `***` is unchanged. See [docs/config.md#trace](docs/config.md).
   ```

   Style matches the existing `## v0.14.0` bullet (bold lead phrase, operator-facing behavior, cross-link to docs). Do NOT add a `## Unreleased` section — the repo convention is per-version sections.

5. **Run `make precommit`** in the repo root. Doc-only changes should not break the build, but the changelog/config.md are linted if the project has markdownlint in precommit. Fix any issues.

</requirements>

<constraints>

- **Token-leak invariant (load-bearing, repeated from spec):** the docs MUST state that `Authorization` and `x-api-key` are redacted to `***` in every trace file, regardless of whether tracing was enabled via the config flag or the `/enabletrace` endpoint. No regression from v0.14.0 documented behavior.
- **`Config.Trace` deprecated, not removed:** the docs MUST mark `trace:` as deprecated but MUST confirm it still works as an always-on opt-in. Do NOT document its removal.
- **Frozen constant: 5-minute production TTL.** The docs MUST state the TTL is 5 minutes and is NOT configurable via config file, query param, or request body (spec Non-goal hard veto). Do NOT document `TRACE_TTL` — it is internal test-only, not an operator-facing knob.
- **`/setloglevel` pattern to mirror:** the docs should note the new endpoints follow the same operator-local, no-auth trust model as `/setloglevel`.
- **glog discipline:** no new glog calls in this prompt (docs only), but the constraint governs the codebase and is restated for completeness — any `Info`-level log is `V(n)`-gated, lowercase messages.
- **`CreateRouterFromConfig` signature unchanged:** no code changes in this prompt.
- **Trace independent of SIGHUP / `atomic.Pointer[http.Handler]` mux swap:** the docs MUST state the toggle is process-internal state that does not depend on config reload or SIGHUP.
- **Do NOT commit** — dark-factory handles git.
- **Existing tests must still pass** (no code changes, but `make precommit` includes the test suite).
- **No code changes in this prompt.** Only `docs/config.md` and `CHANGELOG.md`.

</constraints>

<verification>

```bash
cd /workspace
make precommit
```

Must exit 0. Additionally (the spec AC #10 + #11 evidence commands):

```bash
# AC #10: docs/config.md documents both endpoints
grep -n 'enabletrace' /workspace/docs/config.md
# Expect: ≥1 line
grep -n 'disabletrace' /workspace/docs/config.md
# Expect: ≥1 line
grep -ni 'deprecat' /workspace/docs/config.md
# Expect: ≥1 line (the deprecation note on the trace: flag)

# TTL documented
grep -ni '5.*min\|five.*min' /workspace/docs/config.md
# Expect: ≥1 line mentioning the 5-minute window

# Redaction still documented (no regression)
grep -n '\*\*\*' /workspace/docs/config.md
# Expect: ≥1 line (the *** redaction literal)

# AC #11: CHANGELOG entry above v0.14.0
grep -n '## v0.15.0' /workspace/CHANGELOG.md
# Expect: line number LESS than the v0.14.0 line
grep -ni 'enabletrace\|trace.*ttl\|disabletrace' /workspace/CHANGELOG.md
# Expect: ≥1 line at or above the v0.14.0 section

# Schema deprecation pointer
grep -n 'trace:' /workspace/docs/config.md
# Expect: the schema line now includes a deprecation pointer
```

</verification>

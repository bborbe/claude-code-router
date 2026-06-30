---
status: completed
spec: [002-trace-logging]
summary: 'Added Trace bool field to Config struct with yaml tag, backward-compat Ginkgo specs, docs/config.md trace section, docs/config.example.yaml trace flag, and CHANGELOG.md ## Unreleased bullet'
execution_id: claude-code-router-trace-exec-009-spec-002-trace-config
dark-factory-version: dev
created: "2026-06-30T11:10:00Z"
queued: "2026-06-30T09:46:25Z"
started: "2026-06-30T09:47:53Z"
completed: "2026-06-30T09:50:33Z"
---

<summary>
- Operators can set a top-level `trace: true` (or `false`, or omit it) in `config.yaml` to toggle per-request trace logging.
- A config without `trace:` loads exactly as before (defaults to `false`) — full backward compatibility.
- `docs/config.md` documents the new `trace` flag, its values, the trace-file location, and the header-redaction behavior.
- `CHANGELOG.md` gains a `## Unreleased` section with a bullet mentioning `trace`.
- No middleware or file-writing code is added in this prompt — only the config field, its parsing, and the docs.
</summary>

<objective>
Add the `trace` boolean to the parsed config so prompt 2's middleware can read `cfg.Trace`. Ship the operator-facing documentation (config reference + changelog) so the flag is self-discoverable. This is prompt 1 of 2 for spec 002.
</objective>

<context>
Read CLAUDE.md at the repo root for project conventions.

Read these source files before making changes:
- `specs/in-progress/002-trace-logging.md` — the full spec; pay attention to "Desired Behavior" items 1 and 8, "Constraints" (Config struct + backward compatibility), and "Acceptance Criteria" (config-doc, changelog).
- `pkg/config.go` — current `Config` struct and `Load` function. The `Trace bool` field is added here.
- `pkg/config_test.go` — existing Ginkgo specs for `Load`; follow the same `write(yaml)` helper + `pkgcfg.Load(context.Background(), p)` pattern for new specs.
- `docs/config.md` — current schema reference; the `trace` flag doc slots in after the `## Schema` block and the `## Auth` table's "never stores or logs token values" line is extended to mention trace files.
- `docs/config.example.yaml` — current example config; add a commented `trace:` line.
- `CHANGELOG.md` — no `## Unreleased` section exists today (top entry is `## v0.13.0`); create one at the top.
- `/home/node/.claude-yolo/plugins/marketplaces/coding/docs/changelog-guide.md` — for the bullet style this project uses (if the path is unreadable in the container, fall back to the existing `## v0.13.0` bullet style in CHANGELOG.md).
- `/home/node/.claude-yolo/plugins/marketplaces/coding/docs/go-glog-guide.md` — for the `V(n)` gating convention referenced by the spec's glog discipline constraint (no new glog calls are added in THIS prompt, but the constraint is load-bearing for prompt 2 and is repeated below).
</context>

<requirements>

1. **Add the `Trace bool` field to `Config`** in `pkg/config.go`. The current struct (read verbatim):

   ```go
   type Config struct {
       Router    Router              `yaml:"router"`
       Providers map[string]Provider `yaml:"providers"`
       Aliases map[string]string `yaml:"aliases,omitempty"`
   }
   ```

   Add a `Trace` field with the YAML key `trace` and a doc comment. Place it after `Aliases`:

   ```go
   // Trace, when true, enables per-request trace logging for /v1/*
   // requests: every request writes one JSON file capturing the full
   // request and response to ~/.claude-code-router/trace/. When false
   // (or absent), no trace files are written and no trace middleware
   // is allocated on the request hot path. Read once at Load; a
   // restart applies it.
   Trace bool `yaml:"trace,omitempty"`
   ```

   Do NOT add `omitempty` validation logic — `omitempty` on a bool only suppresses the key when serializing; `yaml.Unmarshal` into a `bool` with the key absent leaves the zero value `false`, which is the desired default. No changes to `Router`, `Providers`, `Aliases`, or their YAML keys.

2. **No change to `Load` or `Validate` is required.** `yaml.Unmarshal(data, c)` already populates `Config.Trace` from the `trace:` YAML key. A config without `trace:` parses with `Trace == false` (the bool zero value) — backward compatible by construction. Do NOT add a validation rule for `Trace` (any bool value is valid; non-boolean values like `trace: "yes"` are rejected by `yaml.Unmarshal`'s existing bool-coercion error path, which surfaces as the existing "parse config" error from `Load`).

3. **Add Ginkgo specs to `pkg/config_test.go`** in the existing `Context("Load")` block (or a new `Context("trace")` sibling — match the file's style). Follow the existing `write(yaml string) string` helper and `pkgcfg.Load(context.Background(), p)` call pattern. Add at minimum:

   - A spec that loads a config WITH `trace: true` and asserts `cfg.Trace` is `true`.
   - A spec that loads a config WITHOUT any `trace:` key and asserts `cfg.Trace` is `false` (backward-compat — the most important spec).
   - A spec that loads a config with `trace: false` explicitly and asserts `cfg.Trace` is `false`.
   - A spec that loads a config with `trace: "yes"` (quoted string, non-boolean) and asserts `Load` returns an error via the existing "parse config" path. NOTE: unquoted `yes`/`no`/`on`/`off` are coerced to bool by `gopkg.in/yaml.v3` (YAML 1.1 compat) — only the *quoted* string `"yes"` actually errors; pin this behavior with the quoted form.

   Use the same minimal provider block as the existing specs (one provider with `upstream` + `models` + a `default_provider` that matches). Do not invent a new test helper; reuse `write`.

4. **Update `docs/config.md`** — add the `trace` flag to the `## Schema` block. Insert the `trace:` line at the top level (same indentation as `router:` and `providers:`), with a comment:

   ```yaml
   trace: <bool>                         # optional; default false. When true, writes one JSON file per /v1/* request to ~/.claude-code-router/trace/
   ```

   Add a new `## Trace` section after the `## Auth` section (and before `## Example — all four providers`). Content to include (adapt prose for flow, preserve the facts):

   - The `trace` flag is a top-level boolean. When `true`, every `/v1/*` request produces exactly one JSON file at `~/.claude-code-router/trace/<timestamp>-<request-id>.json` containing the complete request (method, path, headers, body) and complete response (status, headers, body).
   - When `false` (or absent), no trace files are written and no trace middleware is on the request hot path.
   - The `Authorization` and `x-api-key` request headers are redacted to `***` in every trace file, regardless of header case. All other headers and the entire request/response bodies are logged verbatim — operator's data, operator's disk.
   - This extends the existing invariant from the `## Auth` table ("The router never stores or logs token values") to trace files. Concretely: edit the `## Auth` table row at `docs/config.md` that currently says "The router never stores or logs token values." — append `; trace files inherit the same invariant — see ## Trace.` so the Auth section points operators at the trace-redaction behavior. Do not leave the claim only in the new `## Trace` section; the Auth line itself must be updated.
   - The flag is read once at config load; changing it requires a router restart (see `## Reload`).
   - No retention, rotation, or cleanup is provided — the operator runs `rm` manually.

5. **Update `docs/config.example.yaml`** — add a commented `trace:` line near the top level (after the `router:` block or at the bottom before `aliases:`). Example:

   ```yaml
   # Per-request trace logging for /v1/* traffic.
   # When true, writes one JSON file per request to ~/.claude-code-router/trace/
   # (Authorization + x-api-key headers redacted to ***; bodies verbatim).
   # Default false — omit or set false for no trace files.
   trace: false
   ```

6. **Add a `## Unreleased` section to `CHANGELOG.md`.** No `## Unreleased` section exists today (the top entry is `## v0.13.0`). Insert `## Unreleased` immediately after the header intro lines (after the "Please choose versions by Semantic Versioning." line) and BEFORE `## v0.13.0`. Add one bullet:

   ```markdown
   - **feat: per-request trace logging.** New optional top-level `trace:` boolean in `~/.claude-code-router/config.yaml`. When `true`, every `/v1/*` request writes one JSON file to `~/.claude-code-router/trace/<timestamp>-<request-id>.json` capturing the full request (method, path, headers, body) and response (status, headers, body). `Authorization` and `x-api-key` headers are redacted to `***`; all other headers and bodies are logged verbatim. When `false` (or absent), no trace files are written and no trace middleware is allocated. Read once at config load; restart to apply. See [docs/config.md#trace](docs/config.md).
   ```

   Style matches existing bullets (bold lead phrase, operator-facing behavior). The middleware wiring lands in prompt 2; this bullet describes the complete user-facing feature.

7. **Run `make precommit`** in the repo root. Fix any gofmt / addlicense / lint issues. All existing tests plus the new config specs must pass.

</requirements>

<constraints>

- **Token-leak invariant (load-bearing, repeated from spec):** `Authorization` and `x-api-key` are never written raw, even in trace mode. This prompt adds the config field only (no file-writing code), but the doc text MUST state the redaction behavior so operators know the invariant before enabling trace.
- **Backward compatibility:** A config without `trace:` loads exactly as before (`Config.Trace == false`). No validation error, no behavior change.
- **glog discipline:** Any new `Info`-level log is `V(n)`-gated; no bare `glog.Infof`. Log messages are lowercase. (No new glog calls are added in THIS prompt, but this constraint governs prompt 2 and is restated so the agent has the full invariant set.)
- **`CreateRouterFromConfig` signature unchanged:** `func CreateRouterFromConfig(ctx context.Context, cfg *pkg.Config) (http.Handler, error)`. Do not touch the factory in this prompt.
- **The model router (`NewModelRouter`), its `[req]` log line, the sampler, and the metrics are unchanged.** Trace is additive middleware wrapping `/v1/`, not a modification of the model router.
- **Trace is independent of SIGHUP hot-reload.** The flag is read once at `Load`; no runtime mutation, no SIGHUP dependency. (Separate feature, spec 002's SIGHUP note refers to a different spec.)
- **Do NOT commit** — dark-factory handles git.
- **Existing tests must still pass.**
- **Do NOT add a configurable trace-directory path, redaction-list override, body-size cap, or trace-output format** — these are spec Non-goals (hard veto).
- **Doc accuracy:** The trace-file location `~/.claude-code-router/trace/` and the redaction targets (`Authorization`, `x-api-key` → `***`) MUST appear verbatim in `docs/config.md` — operators copy-paste from the doc.

</constraints>

<verification>

```bash
cd /workspace
make precommit
```

Must exit 0. Additionally:

```bash
# Config field present
grep -n 'Trace bool' /workspace/pkg/config.go
# Expect one match with yaml tag `trace,omitempty`
grep -n 'trace,omitempty' /workspace/pkg/config.go
# Expect one match (the yaml tag on the Trace field)

# Backward-compat spec exists
grep -n 'trace' /workspace/pkg/config_test.go
# Expect ≥3 matches (true, false-absent, false-explicit specs)

# Doc covers trace + location + redaction
grep -n 'trace' /workspace/docs/config.md
# Expect ≥1 line
grep -n 'claude-code-router/trace' /workspace/docs/config.md
# Expect ≥1 line
grep -n '\*\*\*' /workspace/docs/config.md
# Expect ≥1 line (the redaction literal in the trace section)

# Example config has the flag
grep -n 'trace' /workspace/docs/config.example.yaml
# Expect ≥1 line

# Changelog has Unreleased + trace bullet
grep -n '## Unreleased' /workspace/CHANGELOG.md
# Expect line ≥1
grep -ni 'trace' /workspace/CHANGELOG.md
# Expect ≥1 line at or after the ## Unreleased line
```

</verification>

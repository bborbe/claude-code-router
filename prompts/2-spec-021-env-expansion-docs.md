---
status: draft
spec: [021-config-env-expansion]
created: "2026-10-12T08:48:00Z"
branch: dark-factory/config-env-expansion
---

# Docs: environment variable expansion in the config file

<summary>
- The configuration reference gains a section documenting that `${NAME}` in the config file is resolved from the process environment at load.
- The section documents the `$${` escape for a literal `${`, the single-pass rule, and that expansion covers the whole document including comments.
- It documents the two failure modes: an unset variable fails the load by name, and a malformed reference fails the load naming the byte offset.
- It explains that a set-but-empty variable resolves while an unset one fails.
- It tells the operator to quote the reference (`token: "${NAME}"`) so an expanded value containing YAML-significant characters is preserved.
- It states that SIGHUP re-runs expansion, so a rotated environment is picked up without a config edit.
- It states that a failed expansion leaves the router unstarted and a failed reload leaves the previous handler in place.
- It states that error messages name the variable and never its value.
- The changelog gains a feature entry under a new `## Unreleased` heading.
- No Go source is touched; this prompt documents what prompt 1 shipped.
</summary>

<objective>
Document the `${NAME}` expansion syntax so an operator can move a provider token or an inbound API key out of the plaintext config file and into the router's environment, and can diagnose a failed load without the failure itself disclosing a secret. The docs must state the exact syntax, the escape, the failure modes, the quoting rule, and the reload behaviour.
</objective>

<context>
- Repo root is the current working directory. Use repo-relative paths only.
- **This prompt depends on prompt 1** (`1-spec-021-env-expansion.md`). Do not execute it until prompt 1 has shipped. Its postconditions are: `expandEnvRefs` exists in `pkg/config.go` and uses `os.LookupEnv`; `Load` calls it between `os.ReadFile` and `yaml.Unmarshal`; the syntax is `${NAME}`, the escape is `$${`, an unset variable fails the load by name, an unterminated `${` or an invalid name fails the load naming the byte offset, expansion is a single pass over the whole document, and a set-but-empty variable resolves. If any of those is absent, STOP and report the failure — do NOT implement the Go side here and do NOT document syntax that is not shipped. (`grep -n 'os.LookupEnv' pkg/config.go` and `grep -n 'func expandEnvRefs' pkg/config.go` are the cheap precondition checks.)
- Read the shipped implementation before writing a word of the section, so the docs describe what the code actually does: `pkg/config.go`'s `expandEnvRefs` and `Load`.
- Read `specs/in-progress/021-config-env-expansion.md` (or `specs/completed/` if it has moved) — its Desired Behavior 1–8, Constraints, Failure Modes table, and Security section are the source of the operator-facing facts.
- **Read `docs/config.md` and match its structure.** Current heading order: `# claude-code-router config` → `## Schema` (a fenced YAML block that ends immediately before `## Routing`) → `## Routing` → `## Aliases` → `## Model pools` → `## Requires leading system` → `## Concurrency limit` → `## Upstream host limits` → `## 429 delay gate` → `## Cold-start admission gate` → `## TTFT backpressure and circuit breaker` → `## Upstream pools` → `## Time-of-day windows` → `## Auth` → `## Routing by API key` → `## Trace` → `## Example — all four providers` → `## Switching mid-session` → `## Reload` → `## Related`.
  Match the established voice of a neighbouring section such as `## Concurrency limit` / `## Upstream host limits`: a short lead-in sentence, a `yaml` fenced snippet, then bold-lead bullets (`- **What it bounds.** …`).
  The `## Reload` section already documents SIGHUP and the `config reload failed: …` WARNING; link to it rather than repeating it.
- Read `docs/dod.md` — it requires a `CHANGELOG.md` entry under `## Unreleased` for this change (and `docs/config.md` to be updated when config behaviour changes).
- Read `CHANGELOG.md`: today it goes straight from the header block to `## v0.55.0`, with **no** `## Unreleased` heading. Released sections are frozen history. The highest `## vX.Y.Z` heading drifts between prompt authoring and execution — read the file and use whichever version heading is highest; never trust a hard-coded number.
- Coding plugin docs (in-container paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md` — `feat:` prefix (minor bump), one bullet per logical change, specificity.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md` — docs conventions.
</context>

<requirements>
1. **Add a new section `## Environment variable expansion` to `docs/config.md`.** Insert it immediately after the `## Schema` section's fenced YAML block and immediately before `## Routing`. Do NOT edit the `## Schema` YAML block: this change adds no new YAML field, so the schema is unchanged.

   The section must contain, in the voice of its neighbours:

   a. A lead-in sentence: at load, every `${NAME}` occurring anywhere in the config file text is replaced by the value of environment variable `NAME`, so the config file can name a secret instead of containing it.

   b. A `yaml` fenced snippet showing the quoting rule, e.g.:
      ```yaml
      providers:
        anthropic:
          upstream: https://api.anthropic.com
          token: "${ANTHROPIC_API_KEY}"   # resolved from the environment at load and at every reload
          models: ["claude-*"]
      ```

   c. **Syntax.** The reference is `${NAME}` where `NAME` is an environment-variable identifier: `[A-Za-z_][A-Za-z0-9_]*`. The literal text `${NAME}` must appear in the section body.

   d. **The escape.** `$${` renders a literal `${` and performs no lookup; the literal text `$${` must appear in the section body. State that a `$` that begins neither a reference nor an escape is passed through unchanged, so a bare `$` and a bare `$$` survive verbatim.

   e. **Document-wide.** Expansion is applied to the whole file, not to a whitelist of fields — so a reference anywhere (a provider `token:`, a top-level `allowedApiKeys:` entry, an `upstream:`, an `aliases:` value, and a field added in a later release) is covered with no code change here. The direct consequence: expansion covers **comments too**, so a comment in a real config that mentions a reference must write it escaped, e.g. `# example: $${NAME}`; an unescaped `${UNSET}` inside a comment fails the load.

   f. **Single pass.** An expanded value is never re-scanned, so a value that itself contains `${OTHER}` stays literal and can never trigger a second lookup.

   g. **Unset vs empty.** A variable that is *unset* fails the load, naming the variable and never its value: the router does not start, and a reload leaves the previous handler in place. A variable that is *set to the empty string* resolves — the load succeeds and the empty value is used. The distinction matters: exporting a variable to an empty value is not the same as not exporting it.

   h. **Malformed references.** An unterminated `${` fails the load naming the byte offset; a reference whose name is not a valid identifier fails the load naming the byte offset. Give both examples, e.g. `token: ${UNCLOSED` and `token: ${not-a-name}`.

   i. **Quoting.** The document is parsed as YAML *after* expansion, so an expanded value containing YAML-significant characters (`#`, `: `, a leading `-` / `*` / `&` / `%`, a newline) can truncate the value or break the parse when the reference is written unquoted. Author the reference as a quoted scalar — `token: "${NAME}"` — which keeps the expanded value inside a quoted scalar and preserves it verbatim.

   j. **Reload.** SIGHUP re-runs expansion against the config file and the environment as they stand at reload time, so a rotated environment is picked up without a config edit (link `## Reload`).

   k. **Security.** Expansion reads only the process environment; it adds no file, network or TeamVault access to the router. Error messages name the variable, never its value, so a failed start is diagnosable without the failure itself disclosing a secret. The config file's permissions are unchanged by this feature.

2. **`CHANGELOG.md`.** Add one `feat:` bullet under a new `## Unreleased` heading, created immediately above the newest released heading (`## v0.55.0` at the time of writing — read the file and use whichever `## vX.Y.Z` heading is highest). Match the detail level of the repo's longer `feat:` entries. The bullet must contain the phrase `environment variable` and must mention: that `${NAME}` in the config file is resolved from the process environment at load and at every reload; the `$${` escape for a literal `${`; that expansion covers the whole document including comments; that an unset variable fails the load by name and a malformed reference fails it by byte offset; that a set-but-empty variable resolves; that a value is expanded in a single pass and never re-expanded; and that no secret value appears in an error message or a log line. Do NOT edit any released `## vX.Y.Z` section.

2b. **`README.md`.** Add one clause pointing at the new `docs/config.md` section — a value may be written `token: "${NAME}"` and resolved from the process environment at load and at every reload. `docs/dod.md` requires `README.md` updated when configuration changes, and the repo's own precedent (`prompts/completed/065-spec-020-metrics-wiring-docs.md`, requirement 8) updated `README.md` for a smaller config change. Do NOT inline the full syntax — one clause plus the `docs/config.md` link.

3. **Before finishing, re-run `<verification>` and read its OUTPUT — not its exit status.** The executor does not fail the run on a non-zero exit, so a silent empty grep is not a pass. Every `grep -n` line must print a match; every `grep -c` line must print a count ≥ 1; the ordering `awk` must print `ORDER OK`; the precondition greps must print a match, not the `MISSEQUENCED:` line. Then re-read the new section end to end and check each claim against the shipped `expandEnvRefs` implementation — every documented behaviour must be one the code actually has.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT touch any Go source, any `*_test.go` file, `go.mod`, or `go.sum`. Prompt 1 implemented the behaviour; this prompt is documentation and changelog only.
- Document only what shipped: the `${NAME}` reference, the `$${` escape, the unset-variable failure, the malformed-reference failure, the single-pass rule, the set-but-empty rule, the document-wide scope, the quoting rule, and reload re-expansion. Do NOT document a fallback value (`${NAME:-default}`), a default-value form, a nested or recursive expansion, per-field opt-in, a whitelist, an escape other than `$${`, a way to read a file or a secret store, or any new YAML field — none of those exist.
- Do NOT add a new YAML field to the `## Schema` block or to `docs/config.example.yaml`: this change adds no config field, so `docs/config.example.yaml` is deliberately out of scope.
- Do NOT edit released `CHANGELOG.md` sections (the highest `## vX.Y.Z` heading and below) — they are frozen history.
- No AI attribution.
- `make precommit` must remain green.
</constraints>

<verification>
make precommit

# Precondition — prompt 1 shipped. The executor does NOT fail the run on a non-zero exit,
# so make a mis-sequencing visible in the OUTPUT rather than relying on an exit code:
grep -n 'os.LookupEnv' pkg/config.go || echo 'MISSEQUENCED: prompt 1 has not shipped (os.LookupEnv absent)'
grep -n 'func expandEnvRefs' pkg/config.go || echo 'MISSEQUENCED: prompt 1 has not shipped (expandEnvRefs absent)'

# AC8 — the section documents the syntax (each expects a count >= 1):
grep -c '\${NAME}' docs/config.md
grep -c '\$\${' docs/config.md

# The section exists and sits between ## Schema and ## Routing — assert the ORDER mechanically
# (a plain grep of the three headings always matched, because ## Schema and ## Routing always exist):
grep -n '^## Environment variable expansion' docs/config.md || echo 'ORDER FAIL: section heading absent'
awk '/^## Schema/{s=NR} /^## Environment variable expansion/{e=NR} /^## Routing/{r=NR} END{ if (s && e && r && s<e && e<r) print "ORDER OK"; else print "ORDER FAIL s="s" e="e" r="r }' docs/config.md

# The failure modes and the quoting rule are documented:
grep -n 'byte offset' docs/config.md
grep -n 'fails the load' docs/config.md
grep -n 'token: "\${' docs/config.md

# The remaining required facts must also be present (presence checks, weakest tier — the
# end-to-end re-read in requirement 3 is the real content check):
grep -nE 'single pass|never re-?scanned|not re-?expanded' docs/config.md
grep -nE 'whole file|document as a whole|comments' docs/config.md
grep -nE 'empty string|set to the empty' docs/config.md

# No undocumented syntax leaked in (fallback/default forms must NOT appear):
! grep -qE '\$\{[A-Za-z_][A-Za-z0-9_]*:-' docs/config.md

# CHANGELOG — a feat bullet under ## Unreleased:
grep -n '^## Unreleased' CHANGELOG.md
sed -n '/^## Unreleased/,/^## v/p' CHANGELOG.md | grep -c 'environment variable'
sed -n '/^## Unreleased/,/^## v/p' CHANGELOG.md | grep -c '^- feat:'
</verification>

<!--
OPEN QUESTION (changelog scope — flag at audit).
The spec lists no acceptance criterion for CHANGELOG.md; its AC8 covers docs/config.md only. The
repo's own `docs/dod.md` ("CHANGELOG.md has an entry under ## Unreleased") and the precedent of the
sibling docs prompts (e.g. `prompts/completed/059-spec-019-docs-changelog.md`,
`prompts/completed/061-spec-019-cold-gate-docs-changelog.md`) both put the changelog entry with the
docs prompt, so requirement 2 carries it here. If the reviewer wants the changelog strictly out of
scope for this spec, drop requirement 2 and the three CHANGELOG verification lines together — do not
keep the verification without the requirement.

OPEN QUESTION (README scope — flag at audit).
`docs/dod.md` also says "README.md updated if the change affects usage, configuration, or install
steps". This feature adds a config-file syntax, so a one-line pointer from README to the new
`docs/config.md` section would be defensible. The spec scopes its documentation acceptance criterion
to `docs/config.md`, so README.md is left untouched here to avoid scope creep. Decide at audit.
-->

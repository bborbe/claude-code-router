---
status: completed
spec: [020-ttft-backpressure-and-circuit-breaker]
summary: Added the two optional per-provider config knobs ttftBackpressureThresholdSeconds and breakerFailureThreshold to pkg/config.go with lenient zero-value semantics, added yaml-boundary test rows, and extracted the spec-018 throttle Context into pkg/config_throttle_test.go to stay inside the 2000-line revive limit
execution_id: claude-code-router-backpressure-exec-062-spec-020-config-fields
dark-factory-version: v0.196.0
created: "2026-10-11T09:12:32Z"
queued: "2026-10-11T10:05:40Z"
started: "2026-10-11T10:06:55Z"
completed: "2026-10-11T10:12:30Z"
---

# Config: TTFT backpressure and circuit-breaker provider knobs

<summary>
- A provider can opt into TTFT backpressure by setting a threshold in seconds on its provider block.
- A provider can opt into a circuit breaker by setting a consecutive-failure threshold on its provider block.
- Both knobs are optional and off by default: a provider that sets neither is byte-for-byte unchanged, and every existing config still loads.
- A negative or zero value is treated as disabled — the config always loads and never fails validation because of these two fields.
- The knobs live at provider level only, exactly like the throttle and cold-start knobs; putting one on an `upstreams:` pool member is silently ignored, as it is for the sibling knobs.
- Nothing in this change reads, interprets, or acts on the values — this prompt only fixes the config contract the two behaviours will consume.
</summary>

<objective>
Add the two optional per-provider config fields the TTFT backpressure loop and the circuit breaker read — `ttftBackpressureThresholdSeconds` and `breakerFailureThreshold` — with lenient validation and zero-value semantics that preserve today's behaviour exactly, so the behaviour prompts that follow have a stable config contract to consume.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only. Single-module Go repo (`go.mod` at root). This repo's `Makefile` includes only `tools.env` + `Makefile.docker` — plain `make test` / `make precommit` is correct, no `ROOTDIR` override. `.git` may be masked in the container — NEVER put a bare `git` command in `<verification>`.
- Read `pkg/config.go` — the `Provider` struct (its last field today is `NewSessionRatePerMinute int \`yaml:"newSessionRatePerMinute,omitempty"\``) and the GoDoc style of the sibling knobs `Throttle429Threshold`, `ColdPrefillBudgetTokens`, `ColdSessionWindowSeconds`, `NewSessionRatePerMinute`. Every one of them documents: the enabling condition, the absent/0/negative semantics, and "Read at provider level only — NOT copied onto upstream members". Mirror that voice exactly.
- Read `pkg/config.go`'s `Validate` / `normalizeUpstreams` — confirm there is NO per-knob validation today (the throttle and cold knobs are never validated; the wiring resolves `<= 0`). This change adds no validation.
- Read `pkg/config_test.go` — the existing `Context("throttle429Threshold", ...)` (around line 1093) is the exact shape to mirror: a local `loadProvider(extra string) (*pkgcfg.Config, error)` closure that writes a fixture and calls `pkgcfg.Load`, then rows asserting each field. Note the file-length rule below.
- Read `pkg/config_coldstart_test.go` — the spec-019 precedent for splitting a new knob's rows into a sibling test file so `pkg/config_test.go` stays inside the 2000-line `file-length-limit` (`.golangci.yml` → `revive` → `file-length-limit`, `max: 2000`). `pkg/config_test.go` is currently 1928 lines.
- Coding plugin docs (in-container paths): `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` (Ginkgo v2 + Gomega, external `_test` package), `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` (GoDoc on every field), `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`.
</context>

<requirements>
1. **Add two fields to `Provider` in `pkg/config.go`**, immediately after `NewSessionRatePerMinute`, with exactly these names, types, and yaml tags:
   ```go
   	// TtftBackpressureThresholdSeconds, when > 0, enables the per-provider
   	// TTFT backpressure loop (spec 020): once per fixed 60s window the
   	// provider's median cold time-to-first-token (seconds) is compared with
   	// this threshold — a median above it, or any upstream failure observed in
   	// the window, halves the effective cold-prefill budget; a clean window
   	// grows it back by a fixed step. It has an effect only when the cold-start
   	// gate is enabled (ColdPrefillBudgetTokens > 0), because it adjusts that
   	// budget; with no cold budget configured the knob is inert. Absent, 0, or
   	// negative disables the loop — byte-for-byte current behaviour. Read at
   	// provider level only — NOT copied onto upstream members (like the
   	// throttle and cold knobs).
   	TtftBackpressureThresholdSeconds int `yaml:"ttftBackpressureThresholdSeconds,omitempty"`
   	// BreakerFailureThreshold, when > 0, enables the per-provider circuit
   	// breaker (spec 020): after this many consecutive upstream failures — a
   	// 5xx response, including the router's reverse proxy answering 502/504
   	// for an upstream timeout — the provider is opened and every request to it
   	// is answered HTTP 429 with a Retry-After for the fixed open period,
   	// without invoking the upstream. A non-5xx response resets the count. It is
   	// independent of the cold-start gate. Absent, 0, or negative disables the
   	// breaker — byte-for-byte current behaviour. Read at provider level only —
   	// NOT copied onto upstream members.
   	BreakerFailureThreshold int `yaml:"breakerFailureThreshold,omitempty"`
   ```

2. **Lenient validation — add none.** Do NOT add any check in `Validate` that rejects a negative or zero value of either field. Do NOT change `normalizeUpstreams`, `UpstreamList`, `Upstream`, or `HostLimit`. A negative `ttftBackpressureThresholdSeconds` and a negative `breakerFailureThreshold` must each load with no error and are resolved to "disabled" at wiring (a later prompt).

3. **Provider level only.** Do NOT copy either field onto `Upstream` pool members and do NOT add them to `normalizeUpstreams`'s synthesized single-member literal. A knob placed on an `upstreams:` member is silently ignored, exactly like `throttle429Threshold` / `coldPrefillBudgetTokens` / `newSessionRatePerMinute`.

4. **Tests in `pkg/config_test.go`.** Add a new `Context("ttftBackpressureThresholdSeconds and breakerFailureThreshold", func() { ... })` inside the existing `Describe("Config", ...)` (place it directly after the existing `Context("throttle429Threshold", ...)`). Reuse the same fixture shape: a local `loadProvider(extra string) (*pkgcfg.Config, error)` closure that writes a base provider block and appends `extra`, then calls `pkgcfg.Load`. Rows (AC 1 and AC 2 evidence — the yaml tag is what these rows defend, so every fixture goes through `Load`, never a struct literal):
   - loads both fields when a provider sets `ttftBackpressureThresholdSeconds: 20` and `breakerFailureThreshold: 5`;
   - leaves both `0` when a provider sets neither — identical to today;
   - loads only `ttftBackpressureThresholdSeconds`, leaving `breakerFailureThreshold` `0` (and the converse);
   - loads a negative `ttftBackpressureThresholdSeconds` with no error;
   - loads a negative `breakerFailureThreshold` with no error;
   - loads explicit zeroes as valid.
   **File-length rule:** after adding the Context run `wc -l pkg/config_test.go`. It must stay ≤ 2000 (`revive` `file-length-limit` `max: 2000`; `skipBlankLines`/`skipComments` are false, so `wc -l` is the exact count). This addition is expected to cross the limit (1928 + ~75 ≈ 2003), so perform the extraction: create `pkg/config_throttle_test.go` as a **self-contained spec** mirroring `pkg/config_coldstart_test.go` — the `package pkg_test` header and imports, then a new top-level `var _ = Describe("Config throttle429Threshold", func() { ... })` carrying its own `var dir string`, the `BeforeEach`/`AfterEach` MkdirTemp/RemoveAll pair, and the local `write := func(yaml string) string` closure. The extracted `Context("throttle429Threshold", ...)` cannot be pasted at file top level unchanged — a top-level `Context` is legal in Ginkgo v2 (it is an alias for `Describe`), but its `write`/`dir` and the `BeforeEach`/`AfterEach` pair come from the enclosing `Describe`'s scaffolding, so all of that must be duplicated into the new file. Then delete the original block from `pkg/config_test.go` and confirm `wc -l pkg/config_test.go` ≤ 2000 and `go test -count=1 ./pkg/` passes.
5. **Before finishing, re-run `<verification>` and confirm every command passes.** Walk requirements 1–4 against the change.

**Scope note:** this prompt fixes only the config contract — AC 3's disabled-no-op evidence (`go test -count=1 ./pkg/handler/`: breaker constructor identical to the inner handler, budget never adapted, no counter series, no log lines) is unreachable from a config-only change and lands with sibling prompts 2/3/4.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT touch `pkg/handler/`, `pkg/factory/`, `pkg/reloader/`, `docs/`, `README.md`, or `CHANGELOG.md` in this prompt — later prompts own the behaviours, the wiring, and the documentation.
- Config schema is the single source of truth: the fields live on `pkg/config.go`'s `Provider`; no inline parse logic anywhere else.
- Zero-value semantics must remain today's behaviour: a provider with neither field set is byte-for-byte unchanged.
- Validation is lenient: no value fails `config.Load`. Do NOT add an opt-out flag, a tunable default, or any extra knob — only the two fields above.
- No new dependencies — the Go standard library plus the existing `bborbe/*` libraries suffice.
- No AI attribution in code or comments.
- `make precommit` must remain green — run it before declaring done. Follow `docs/dod.md` (GoDoc on every new exported field).
</constraints>

<verification>
make precommit

# Both fields + yaml tags on Provider:
grep -n 'ttftBackpressureThresholdSeconds' pkg/config.go
grep -n 'breakerFailureThreshold' pkg/config.go
grep -n 'TtftBackpressureThresholdSeconds\|BreakerFailureThreshold' pkg/config.go

# Validate is untouched (one definition, no new rejection branch):
grep -c 'func (c \*Config) Validate' pkg/config.go     # expect 1
! grep -nE 'ttftBackpressureThresholdSeconds|breakerFailureThreshold' pkg/config.go | grep -i 'error'

# Test rows exist in pkg/config_test.go (AC 1 evidence):
grep -c 'ttftBackpressureThresholdSeconds' pkg/config_test.go     # expect >= 1
grep -c 'breakerFailureThreshold' pkg/config_test.go              # expect >= 1

# File stays inside the file-length limit:
test -f pkg/config_throttle_test.go                               # extraction target exists
wc -l pkg/config_test.go                                          # expect <= 2000

# Config suite:
go test -count=1 ./pkg/
</verification>

<!--
OPEN QUESTION (resolved — flag at audit): the spec's AC 1 names the evidence
`grep -c 'ttftBackpressureThresholdSeconds' pkg/config_test.go`, so this prompt places the new
rows in pkg/config_test.go rather than a sibling file. pkg/config_test.go is 1928 lines against a
2000-line revive file-length-limit, and this addition is expected to cross it (1928 + ~75 ≈ 2003), so
requirement 4 directs an unconditional extraction of the spec-018 throttle Context into
pkg/config_throttle_test.go — not a conditional fallback.
-->

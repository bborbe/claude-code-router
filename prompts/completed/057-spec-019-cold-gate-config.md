---
status: completed
spec: [019-cold-start-admission-gate]
summary: Added the three per-provider cold-start admission knobs (coldPrefillBudgetTokens, coldSessionWindowSeconds, newSessionRatePerMinute) to the Provider struct with lenient zero-value semantics, plus yaml-boundary Ginkgo tests; no validation, Upstream, or normalizeUpstreams changes.
execution_id: claude-code-router-cold-start-admission-exec-057-spec-019-cold-gate-config
dark-factory-version: v0.196.0
created: "2026-10-10T14:08:35Z"
queued: "2026-10-10T15:02:15Z"
started: "2026-10-10T15:02:17Z"
completed: "2026-10-10T15:05:25Z"
branch: dark-factory/cold-start-admission-gate
---

# Cold-start admission gate: per-provider config knobs

<summary>
- An operator can opt a single provider into cold-start admission control with three new optional YAML fields on that provider's block.
- The first field sets a ceiling on how much uncached "cold prefill" work the router lets start at once for that provider.
- The second field sets the window that decides whether a session is new: a request whose session id was last seen inside this window is treated as already established.
- The third field caps how many brand-new sessions the provider may start per minute.
- A provider block that sets none of the three fields loads exactly as it does today, and the router's request path is unchanged for it.
- Negative or zero values are never rejected: a negative budget or rate means "that check off", and a missing or negative window means "use the 600-second default".
- The three fields are read at provider level only — a copy placed on an individual upstream pool member is silently ignored, exactly like the existing 429-delay-gate knobs.
- No behaviour changes yet: this prompt ships only the configuration surface and its tests; the gate itself, its metrics, its wiring, and the docs land in later prompts.
</summary>

<objective>
Add the three per-provider cold-start admission knobs to the router's configuration surface — the in-flight cold-prefill token budget, the session window, and the new-session rate — with lenient (never fail-closed) zero-value semantics, so a later prompt can build the gate that consumes them and an unconfigured provider keeps today's byte-for-byte behaviour.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only. This is a single-module Go repo (`go.mod` at root) with `pkg/` + `pkg/factory/` + `pkg/handler/` splits; `make test` and `make precommit` are root targets.
- The container may mask `.git` (this repo's `.dark-factory.yaml` does not set `hideGit`, but the daemon can pass `--set hideGit=true`) and this repo's `Makefile` includes only `tools.env` and `Makefile.docker` — it does NOT reference `ROOTDIR` or `default.env`, so plain `make test` / `make precommit` is the correct form and no `ROOTDIR` override is needed. NEVER put a bare `git` command in `<verification>`.
- Read `pkg/config.go` — the `Provider` struct. Its current last field is `ThrottleMaxDelaySeconds int` (yaml `throttleMaxDelaySeconds,omitempty`), the spec-018 adaptive 429 gate knob. The three new fields go immediately after it. Also read `Config.Validate(ctx context.Context) error` and `normalizeUpstreams` — confirm that the throttle knobs are NOT copied onto `Upstream` members and that validation is lenient there; the cold knobs follow the identical pattern.
- Read `pkg/config_test.go` — the `write(yaml string) string` helper and the `Context("throttle429Threshold")` block (its `loadProvider(extra string)` helper and its six rows: both fields, neither, partial, negative threshold, negative max delay, explicit zeroes). The new cold-knob rows mirror this block exactly, including the "yaml-boundary tests through `pkgcfg.Load`, never struct literals" rule. The suite is Ginkgo v2 + Gomega in `package pkg_test` (`pkg/pkg_suite_test.go`).
- Read `docs/config.md` — the `## 429 delay gate` section's note that the throttle knobs are "read at provider level only and are NOT copied onto `upstreams:` pool members — a throttle field on a member is silently ignored". The cold knobs carry the identical scoping. (Do NOT edit `docs/` in this prompt — prompt 4 owns documentation.)
- Coding plugin docs (in-container paths):
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md` — external `_test` package, Ginkgo v2 + Gomega row shapes.
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` — GoDoc conventions (every new field needs a complete GoDoc comment).
  - `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md` — `github.com/bborbe/errors` conventions (used by `Config.Validate`; this prompt adds no new error).
- Definition of Done: `docs/dod.md` (single-source-of-truth config in `pkg/config.go`; no inline parse logic elsewhere).
</context>

<requirements>
1. **Add three fields to the `Provider` struct in `pkg/config.go`**, immediately after `ThrottleMaxDelaySeconds` (its current last field). Exact shape — the field names, types, yaml tags, and zero-value semantics are fixed; the GoDoc wording may be polished but must state the meaning, the default, and the zero-value semantics:

   ```go
   // ColdPrefillBudgetTokens, when > 0, enables the per-provider cold-start
   // admission gate (spec 019): the ceiling on in-flight cold-prefill tokens
   // the router admits for this provider at once. A request counts as cold
   // when its session id (from the request context) was last seen outside
   // the session window, and carries a prefill estimate of its body size
   // divided by 3.5 (integer arithmetic). Cold requests are admitted while
   // their estimate fits the budget; the excess waits in a bounded queue and
   // is then refused with HTTP 429. Absent, 0, or negative disables the
   // budget check — byte-for-byte current behaviour when the rate is also
   // absent/0/negative. Read at provider level only — NOT copied onto
   // upstream members (unlike MaxConcurrentRequests).
   ColdPrefillBudgetTokens int `yaml:"coldPrefillBudgetTokens,omitempty"`
   // ColdSessionWindowSeconds is the window that decides whether a session
   // is new (spec 019): a request whose session id was last seen inside this
   // window is warm and is never held; a request whose id is absent,
   // unknown, or last seen outside the window is cold. Absent, 0, or
   // negative resolves to the 600-second default at wiring. Read at provider
   // level only.
   ColdSessionWindowSeconds int `yaml:"coldSessionWindowSeconds,omitempty"`
   // NewSessionRatePerMinute, when > 0, caps how many newly-seen session ids
   // this provider admits per minute (spec 019), with a fixed burst of 2
   // admitted immediately. A first request for an id beyond that allowance
   // waits in the same bounded queue as a budget-blocked request. Absent, 0,
   // or negative disables the rate check. Read at provider level only — NOT
   // copied onto upstream members.
   NewSessionRatePerMinute int `yaml:"newSessionRatePerMinute,omitempty"`
   ```

   All three are plain `int` with `omitempty` — a provider block without any of them must unmarshal to the zero value and behave exactly as today. Do NOT add any other field, flag, or threshold (spec Non-goals: the rate burst, queue capacity, max wait, estimate divisor, and `Retry-After` clamp are fixed internal constants, NOT knobs).

2. **No new validation in `Config.Validate` (`pkg/config.go`).** Do NOT add any check on these three fields. Validation is lenient (spec Constraints, AC 2): a negative budget or rate is treated as "that check disabled", a negative (or zero) window as the 600-second default, all resolved at wiring by a later prompt — so no value ever fails `config.Load`. Zero is NOT rejected either. Do NOT add a cross-field check, an upper bound, or any non-integer handling (yaml.v3 rejects a non-int at unmarshal).

3. **Do NOT add the fields to the `Upstream` struct** and do NOT copy them in `normalizeUpstreams` (`pkg/config.go`). Spec Constraints: the knobs are read at provider level only and are NOT copied onto `upstreams:` pool members — a cold knob on a member is silently ignored, mirroring the throttle knobs. `MaxConcurrentRequests` / `MaxConcurrentWaitSeconds` are the only fields copied onto a synthesized member; leave that list untouched.

4. **Config tests in `pkg/config_test.go`** (`package pkg_test`, Ginkgo v2 + Gomega, using the existing `write()` helper and `pkgcfg.Load(context.Background(), p)`). Add a new `Context("coldPrefillBudgetTokens")` block directly after the existing `Context("throttle429Threshold")` block, with its own `loadProvider(extra string) (*pkgcfg.Config, error)` helper mirroring the throttle block's (a single `anthropic` provider with `upstream: https://api.anthropic.com`, `models: ["claude-*"]`, plus the `extra` YAML). These are yaml-boundary tests — a wrong yaml tag would silently leave the field zero, so every fixture goes through `Load`, never a struct literal. Rows:
   - **AC 1 (all three set):** a block carrying `coldPrefillBudgetTokens: 65536`, `coldSessionWindowSeconds: 600`, `newSessionRatePerMinute: 4` loads with no error; assert all three fields equal those values on `cfg.Providers["anthropic"]`.
   - **AC 1 (none set = identical to today):** a block carrying none of the three loads with no error; assert all three fields are `0`.
   - **AC 1 (partial):** only `coldPrefillBudgetTokens: 65536` set → loads; `ColdPrefillBudgetTokens == 65536` and the other two are `0` (their defaults resolve at wiring, not at load).
   - **AC 2 (negative budget):** `coldPrefillBudgetTokens: -1` loads with no error; assert the field is `-1` (the gate resolves `<= 0` to "budget check off" at wiring).
   - **AC 2 (negative window):** `coldSessionWindowSeconds: -1` loads with no error; assert the field is `-1` (the gate resolves `<= 0` to the 600-second default at wiring).
   - **AC 2 (negative rate):** `newSessionRatePerMinute: -1` loads with no error; assert the field is `-1` (the gate resolves `<= 0` to "rate check off" at wiring).
   - **Boundary (explicit zeroes are valid):** `coldPrefillBudgetTokens: 0`, `coldSessionWindowSeconds: 0`, `newSessionRatePerMinute: 0` loads with no error; assert all three are `0`.
   - **AC 1 evidence grep:** the new block must contain the literal token `coldPrefillBudgetTokens` (it will, via the YAML fixtures and the field assertions), so `grep -c 'coldPrefillBudgetTokens' pkg/config_test.go` returns `>= 1`.
   - The existing `Context("Load")` and `Context("throttle429Threshold")` rows must still pass unchanged.

5. **Before finishing, re-run `<verification>` and confirm every command passes.** Walk each requirement above against the change: the three fields exist with the exact yaml tags, `Config.Validate` gained no new check, `Upstream`/`normalizeUpstreams` are untouched, and every new Ginkgo row is present and green.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Config schema is fixed (spec Constraints): the provider block gains `coldPrefillBudgetTokens int` (yaml `coldPrefillBudgetTokens,omitempty`), `coldSessionWindowSeconds int` (yaml `coldSessionWindowSeconds,omitempty`) and `newSessionRatePerMinute int` (yaml `newSessionRatePerMinute,omitempty`). Zero-value semantics MUST remain today's behaviour. The knobs are read at provider level only and are NOT copied onto `upstreams:` pool members — a cold knob on a member is silently ignored.
- Fixed internal constants, documented as defaults and NOT exposed as knobs (spec Non-goals): the rate burst is 2, the queue capacity is 32, the maximum wait is 30 seconds, the estimate divisor is 3.5, and the `Retry-After` clamp is 1–60 seconds. The session window IS a knob (`coldSessionWindowSeconds`); 600 seconds is its fallback default when the field is absent or negative, not a fixed constant. Do NOT add any config knob, opt-out flag, or tunable threshold beyond the three fields.
- Validation is lenient: a negative budget or rate is treated as disabled; a negative (or zero) window falls back to 600 seconds. No value fails `config.Load`.
- No new dependencies — the Go standard library plus the existing `bborbe/*` libraries suffice.
- Do NOT touch `pkg/handler/`, `pkg/factory/`, `docs/`, or `CHANGELOG.md` in this prompt — the gate handler, the factory wiring, and the documentation are later prompts. This prompt is the configuration surface and its tests only.
- No AI attribution in code or comments.
- `make precommit` must remain green — run it before declaring done.
- Follow `docs/dod.md`: single-source-of-truth config in `pkg/config.go`; GoDoc on every new field.
</constraints>

<verification>
make precommit

# AC 1 — the three fields + yaml tags landed:
grep -n 'ColdPrefillBudgetTokens\|coldPrefillBudgetTokens' pkg/config.go
grep -n 'ColdSessionWindowSeconds\|coldSessionWindowSeconds' pkg/config.go
grep -n 'NewSessionRatePerMinute\|newSessionRatePerMinute' pkg/config.go

# AC 2 — no new fail-closed validation for the cold knobs:
! grep -n 'coldPrefillBudgetTokens.*must\|ColdPrefillBudgetTokens.*must be' pkg/config.go

# The cold knobs are NOT copied onto upstream members:
! grep -n 'ColdPrefillBudgetTokens:' pkg/config.go
! grep -n 'ColdSessionWindowSeconds:' pkg/config.go
! grep -n 'NewSessionRatePerMinute:' pkg/config.go

# AC 1/2 — config test rows exist:
grep -c 'coldPrefillBudgetTokens' pkg/config_test.go        # expect >= 1
grep -c 'coldSessionWindowSeconds' pkg/config_test.go       # expect >= 1
grep -c 'newSessionRatePerMinute' pkg/config_test.go        # expect >= 1

# Config package suite:
go test -count=1 ./pkg/
</verification>

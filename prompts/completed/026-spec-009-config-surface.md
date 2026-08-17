---
status: completed
spec: [009-inbound-api-key-auth]
summary: Landed the optional auth.key config surface (Config.Auth *AuthConfig with IsEnabled()) and the IsLoopbackRemoteAddr IPv4/IPv6 loopback helper in pkg/handler/loopback.go, with Ginkgo/Gomega YAML round-trip + table tests, docs/config.md + docs/config.example.yaml + README + CHANGELOG updates, and a green make precommit
execution_id: claude-code-router-exec-026-spec-009-config-surface
dark-factory-version: dev
created: "2026-08-17"
queued: "2026-08-17T16:13:22Z"
started: "2026-08-17T16:13:41Z"
completed: "2026-08-17T16:23:38Z"
---

# Config surface + loopback helper for inbound router auth

<summary>
- Operators can declare a shared router key in the YAML config under `auth.key` to enforce inbound authentication on the `/v1/*` path.
- Omitting the field (or leaving it empty) means authentication is disabled and the router behaves exactly as it does today.
- A reusable helper decides whether a remote address is loopback, covering both IPv4 and IPv6.
- Existing config-loading and SIGHUP hot-reload continue to work without changes.
- No code paths are affected yet; this prompt only adds the seam the next prompts consume.
</summary>

<objective>
Land the optional `auth.key` config field on the operator's YAML and a loopback-detection helper, so that prompts 2 and 3 can consume them as a stable seam without each prompt rediscovering the wiring.
</objective>

<context>
- Repo root is the current working directory. Repo-relative paths only.
- Read `pkg/config.go` — find the `Config` struct and the existing `Config.Validate(ctx)` method. The new field lives there.
- Read `pkg/factory/factory.go:201-238` — `buildMux` is the seam prompts 2 and 3 will wire into. No changes here in this prompt.
- Read `docs/dod.md` — single-source-of-truth rule for config validation. New field validation goes through `Config.Validate(ctx)`.
- Error wrapping: use `github.com/bborbe/errors` (`errors.New(ctx, ...)` / `errors.Wrapf(ctx, err, ...)`), never `fmt.Errorf`. Follow the existing style in `Config.Validate` and `Config.validateAliases` in `pkg/config.go`.
- Read `pkg/config_test.go` — the `Context("Load")` block is the exemplar for temp-file YAML tests; follow its `write()` helper and Ginkgo/Gomega style.
</context>

<requirements>
1. Add a new optional top-level `Auth` field on the `Config` struct in `pkg/config.go` of type `*AuthConfig` (pointer so absent loads cleanly without a zero-value ambiguity between "disabled" and "empty string"). The new type lives in the same file (or a small new sibling — your call).
2. `AuthConfig` has exactly one field: `Key string` (yaml tag `key`).
3. Add a helper (sibling function or method) `func (a *AuthConfig) IsEnabled() bool` returning true iff the receiver is non-nil and `Key != ""`. Empty string and nil both mean disabled. This is the single check the middleware will use.
4. `Config.Validate(ctx)` must accept the new field with no new error paths. Absent, `null`, and empty `key` are all valid and all mean disabled (spec DB1). Per the spec's Failure Modes table, a whitespace-only or accidentally-quoted key is treated as a literal key — validation deliberately rejects nothing here; the symptom is a 401 at request time, never a start-up failure.
5. Add a package-level helper `func IsLoopbackRemoteAddr(addr string) bool` in a new file `pkg/handler/loopback.go` (package `handler`), with tests in `pkg/handler/loopback_test.go` (package `handler_test`). Prompts 2 and 3 both consume it from `pkg/factory/factory.go`, so the location is fixed, not a judgment call. It must treat BOTH IPv4 loopback (`127.0.0.0/8` — any address starting with `127.`) and IPv6 loopback (`::1`) as local. The Go stdlib provides `net.ParseIP` and the resulting `IP.IsLoopback()` covers both — use it; do not hand-roll prefix matching. The remote address MUST come from the connection only — never from `X-Forwarded-For` or any other client-supplied header. There is no trusted proxy in front of this router; honouring a forwarded header would let any remote caller claim to be loopback and bypass both the auth check and the admin guard. Document this in the helper's doc comment.
6. Handle the `RemoteAddr` format. The Go http server produces strings like `"127.0.0.1:54321"` or `"[::1]:54321"`. Strip the port before parsing. Handle both bracketed-IPv6 and plain-IPv4 forms.
7. Tests with Ginkgo v2 + Gomega (the repo convention):
   - `AuthConfig.IsEnabled()` table test: nil ⇒ false; `&AuthConfig{}` ⇒ false; `&AuthConfig{Key: ""}` ⇒ false; `&AuthConfig{Key: "x"}` ⇒ true.
   - YAML round-trip through the real loader (this is the boundary test — a struct-literal test does NOT satisfy it). Follow the `Context("Load")` pattern in `pkg/config_test.go`: write a temp config file, call `pkg.Load(ctx, path)`, and assert:
     - YAML containing `auth:\n  key: "s3cret"` yields `cfg.Auth != nil` and `cfg.Auth.Key == "s3cret"` and `cfg.Auth.IsEnabled() == true`
     - YAML with no `auth:` block yields `cfg.Auth == nil` and `cfg.Auth.IsEnabled() == false`, and `Load` returns no error
     - YAML with `auth:\n  key: ""` yields `IsEnabled() == false` and no error
     A wrong yaml tag would leave `Auth` nil and silently disable auth — this test is the only pre-runtime guard against that.
   - `IsLoopbackRemoteAddr` table test: `"127.0.0.1:1234"` ⇒ true; `"127.1.2.3:1234"` ⇒ true (whole /8); `"[::1]:1234"` ⇒ true; `"10.0.0.1:1234"` ⇒ false; `"[2001:db8::1]:1234"` ⇒ false; `""` ⇒ false; `"not-an-address"` ⇒ false.
8. Counterfeiter mock for any new interface introduced. None expected — keep the surface narrow.
</requirements>

<constraints>
- Do NOT commit. Dark-factory handles git.
- Do NOT touch the `/v1/*` middleware chain or any admin route handler. Those changes belong to prompts 2 and 3.
- Do NOT modify `Config.Load`, the SIGHUP reload path, or the YAML schema beyond the new `auth:` block. Existing configs without `auth:` must parse and validate exactly as before.
- Use `github.com/bborbe/errors` for error wrapping. Never `fmt.Errorf`.
- Do NOT log the key value at any level (no Info, no Debug, no error). Logging policies are loud — the field is sensitive.
- Do NOT introduce a metrics counter, a runtime toggle endpoint, or any new HTTP route.
- Do NOT add a new dependency in `go.mod` unless required. Stdlib `net`, `crypto/subtle` (used by prompt 2), and the existing `bborbe/errors` are sufficient here.
- `make precommit` must remain green. Run it before declaring done.
- Follow `docs/dod.md`: handler placement, error wrapping, single-source-of-truth validation, glog conventions, `chmod 600` for any file that holds a real secret (the example file uses a placeholder).
</constraints>

<verification>
make precommit

# Verify the field and its yaml tag landed in the Config struct:
grep -n 'yaml:"key"' pkg/config.go

# Full suite (config lives in package `pkg` — pkg/config.go; there is no pkg/config/ directory):
go test ./pkg/... -count=1
</verification>
# Changelog

All notable changes to this project will be documented in this file.

Please choose versions by [Semantic Versioning](http://semver.org/).

## Unreleased

- feat: add the optional per-provider 429 delay gate (`throttle429Threshold` + `throttleMaxDelaySeconds`, spec 018) so the router paces `/v1/*` requests into a provider under a sustained 429 wall instead of re-storming an upstream already refusing work — once the windowed count of upstream 429 responses within the fixed 60s observation window reaches `throttle429Threshold`, the provider enters throttle and every subsequent request to it waits the current pacing delay before forwarding (off by default: `throttle429Threshold` absent, 0, or negative disables it, byte-for-byte today's behavior; `throttleMaxDelaySeconds` defaults to 30 when absent, 0, or negative); the pacing delay follows bounded AIMD, fixed and not configurable — on entry it is 1s, each observed 429 doubles it (×2) capped at `throttleMaxDelaySeconds`, each clean 60s window halves it (÷2), and below the 1s recovery floor the provider exits throttle; the 429'd request is never retried — the response passes through unchanged and the status is observed only to adjust future pacing; the pacing queue is bounded (at most 32 requests wait their pacing turn), a request that cannot be paced within the max delay is answered HTTP 429 with the same Anthropic-shaped `rate_limit_error` JSON body as the concurrency limiter (never a 5xx, never a hang), and a client that disconnects while waiting is never forwarded and holds no slot; each provider's throttle state is its own — throttling one provider neither delays nor blocks another even when two providers share one upstream — and the knobs are read at provider level only (never copied onto `upstreams:` pool members, a throttle field on a member is silently ignored); validation is lenient (negative threshold = disabled, negative max delay = 30s default — the config always loads); a SIGHUP reload rebuilds the per-provider gates so changed values are live without a restart, though throttle state is in-memory per provider so a reload resets a throttled provider to not-throttled (it re-accumulates on the next 429s — reversible by design); observability is `[throttle] provider=<name> state=on` / `state=off` at INFO, each paced request at glog `-v` ≥ 4, the pacing delay included in the existing `[req] ... latency=`, and a new additive `ccrouter_throttled_total{provider}` counter (the `status_class` 7-value enum and `4xx_rate_limited` classification are unchanged — an upstream 429 still lands in `4xx_rate_limited`); documented in `docs/config.md` (new `## 429 delay gate` section + schema reference), `docs/config.example.yaml` (commented examples), and `docs/metrics.md` (new counter row + note).

## v0.44.5

- chore: update github.com/bborbe/errors to v1.5.21, github.com/bborbe/log to v1.6.25

## v0.44.4

- chore: update go module dependencies

## v0.44.3

- docs: document the dark-factory ↔ router auth model in `docs/dark-factory-integration.md` and `docs/config.md` — `ANTHROPIC_AUTH_TOKEN` (the router's `allowedApiKeys` registry key) is the router-path carrier for local Docker containers, which arrive at the host loopback and bypass the key gate; the router swaps the token outbound for the provider's real key (`seibert-dark-factory` → vLLM) or passes it through for Anthropic subscriptions; truly remote (non-loopback) callers must present the registry key as `x-api-key` via `ANTHROPIC_API_KEY`.

## v0.44.2

- chore: update go module dependencies

## v0.44.1

- chore: update Go to 1.27.0 and github.com/bborbe/argument/v2 to v2.12.36, github.com/bborbe/errors to v1.5.20, github.com/bborbe/http to v1.26.23, github.com/bborbe/log to v1.6.23, github.com/bborbe/run to v1.9.37, github.com/bborbe/service to v1.10.9, github.com/bborbe/time to v1.27.10

## v0.44.0

- feat: wire the spec-017 weekday `days:` filter into the shipped spec-014 eligibility path — `handler.UpstreamMember` gains `Days *pkg.Days` (`pkg/handler/upstream-pool-handler.go`), and `memberEligible` becomes the window AND days conjunction `(window absent OR window.Contains(now)) AND (days absent OR days.Contains(now))`, so a member whose weekday is not in its `days:` set is excluded from session pinning and keyless least-loaded selection alike (the weekday resolves in the member's attached IANA location — the inline days location, else the window from/until location, else UTC — never the router host's local day); a provider whose pool has no eligible member (window AND days both exclude now) falls through to the next matching provider or `default_provider` with the unchanged V(2) `[route] provider=<p> window=closed -> <fallback>` line — eligibility, never an error or 429, no model-pool or router changes; the factory (`pkg/factory/factory.go`) copies each member's `Days` onto the runtime pool member (the same `*pkg.Days` copy as `Window`), so a `days:` change applies on SIGHUP when the reloader rebuilds the pool tree; fixed-clock Ginkgo rows prove the weekend all-day (Sat+Sun Berlin, ineligible Mon-Fri), three-member complementary (weekend/day/night, one eligible member per day+time with the distinct 16/50/50 limiter caps traveling with each member), offset-boundary (UTC Friday evening = Berlin Saturday), and location-boundary (Europe/Berlin vs UTC at a shared instant) behaviors through the real dispatch path.
- feat: add the optional `days:` weekday eligibility block to the config contract (`pkg/config.go`, spec 017) — `Days{Weekdays, Location}` parsed from the comma-separated lowercase weekday-name list (`monday`..`sunday`, Go `time.Weekday.String()` lowercased) with an optional trailing inline IANA location (e.g. `"saturday, sunday Europe/Berlin"`), carried per `Upstream` member and on the legacy single-`upstream:` provider form (a provider-level days is copied onto the synthesized one-member pool by `normalizeUpstreams`, and onto the `Provider.UpstreamList()` fallback for programmatic configs); a provider-level days combined with an `upstreams:` list is rejected at load, an unknown weekday name and an empty value fail at yaml parse, and a days-only member (no `window:`) whose value carries no inline location fails validation (fail-closed, so a days-only member can never silently resolve its weekday in UTC and drift from its sibling members' calendar); `Days.Contains(now, window)` resolves the weekday in the inline days location, else the member's window from/until location, else UTC; configs without any `days:` load byte-for-byte as before. Config contract only — the selection-time eligibility wiring ships separately.
- feat: document the optional per-upstream `days:` weekday eligibility block in `docs/config.md` (new `days:` content in the existing `## Time-of-day windows` section, extended schema reference, and a three-member complementary weekend/day/night worked example) and `docs/config.example.yaml` (commented examples) — a pool member can declare an optional `days:` weekday allow-list as a sibling of `window:` (a comma-separated list of lowercase English weekday names `monday`..`sunday` with an optional trailing inline IANA location, e.g. `"saturday, sunday Europe/Berlin"`), on `upstreams:` entries and on the legacy single-`upstream:` provider form (a provider-level days is copied onto the implicit single member exactly like the provider-level `window:`); a member is eligible only while `(window absent OR window contains "now") AND (days absent OR today's weekday is in days)`, the weekday resolved in the member's attached IANA location (the inline days location, else the window `from`/`until` location, else UTC — never the router host's local day), and a member with `days:` but no `window:` is eligible all day on those weekdays (the weekend unlimited-key use case — the `window:` block has no all-day value); absent `days:` is byte-for-byte today; a days-only member whose `days:` carries no inline location is rejected at config load (fail-closed, so it can never silently resolve its weekday in UTC), as are unknown weekday names, empty values, and a provider-level `days:` combined with an `upstreams:` list; a member outside its `days:` is ineligible for both session pinning and keyless least-loaded selection, and a provider whose pool has no eligible member falls through declaration order to the next matching provider or `default_provider` with the unchanged V(2) `[route] provider=<p> window=closed -> <fallback>` line — eligibility, never an error or a 429; a SIGHUP reload rebuilds the pool tree so a `days:` change is live without a restart.

## v0.43.0

- chore: update dependencies
- chore: update Go to 1.26.6 and update dependencies
- feat: Log upstream pool member index (provider=<name>/<index>) in [req] line

## v0.42.1

- chore: Reorder the `format:` target so `gofmt -w` runs last, after golines, so golines' wrapping is normalized before the gofmt lint check passes
- chore: Bump golangci-lint to v2.13.1 (fixes staticcheck `buildir` panic on Go 1.27 AST) and errcheck to v1.20.0 (fixes `package "context" without types` on Go 1.27) in `tools.env`
- refactor: Remove the dead `providerHandler == nil` guard in `CreateRouterFromConfig` (`pkg/factory/factory.go`) — `NewUpstreamPoolHandler` always returns a non-nil handler, so the guard tripped staticcheck SA4023 once golangci-lint was bumped to v2.13.1

## v0.42.0

- feat: resolve each upstream member's effective outbound bearer token at wiring time in the factory (`pkg/factory/factory.go`, spec 015) — the member's own `token:` wins (for legacy single-`upstream:` configs `normalizeUpstreams`/`UpstreamList` already copied the provider-level token onto the member), else the top-level `default_token:`, else empty, where an empty effective token keeps the auth-swap transport's no-op contract and the client's `Authorization` passes through byte-for-byte; the auth-swap transport now wraps the logging roundtripper (auth-swap outer, logging inner) so the V(3) `[upstream.headers]` line reflects the SWAPPED outbound `Authorization` as `<redacted len=N>` — matching the logging roundtripper's documented behavior — while the literal key never appears in logs or trace files.
- feat: document the optional top-level `default_token:` field (`Config.DefaultToken`) in `docs/config.md` (new `## Auth` three-way resolution order + schema reference) and `docs/config.example.yaml` (commented example) — one shared outbound bearer key inherited by every provider and every `Upstream` pool member that declares no `token:` of its own; the frozen three-way outbound-auth resolution order is applied at wiring time (a provider/member `token:` wins, else the global `default_token:`, else the client's `Authorization` passes through unchanged — byte-for-byte today's subscription-OAuth behavior), with the factory resolving the effective token per upstream member and no per-provider opt-out to force passthrough while a global default is set; the V(3) `[upstream.headers]` log line reflects the swapped outbound `Authorization` as `<redacted len=N>` (the auth-swap transport now wraps the logging roundtripper), so an operator can distinguish the inheriting key from an overriding key's `len` without either key reaching the log; a config edit to `default_token:` applies on SIGHUP without a restart; the key is operator config read only at wiring (never from client input) and is redacted like every other token (`display:"length"`, never in logs or trace files).

## v0.41.1

- fix: thread context cancellation into the time-window `eligibleIndices()` eligibility scans (upstream pool handler + model pool) so a cancelled request aborts the scan, and add `display:"length"` to `ModelRoute.AllowedApiKeys`. These are the PR-review fixes landed after v0.41.0 was cut on the feature branch.

## v0.41.0

- feat: document the per-upstream `window:` time-of-day eligibility block in `docs/config.md` (new `## Time-of-day windows` section + schema reference) and `docs/config.example.yaml` (commented examples) — a pool member can declare an optional `window:` with `from` / `until` as `"HH:MM <location>"` time-of-day values, each carrying its IANA location inline (no separate timezone field), on `upstreams:` entries and on the legacy single-`upstream:` provider form (a provider-level window is copied onto the implicit single member like the caps); a member whose window does not contain "now" (the injected clock, evaluated in the value's attached location) is ineligible for that dispatch — excluded from session pinning and keyless least-loaded selection; when no member of a provider's pool is eligible the provider is ineligible and the model falls through declaration order to the next matching provider or `default_provider`, with a closed window being eligibility only (never a router error, never a 429) and the fall-through logged as `[route] provider=<p> window=closed -> <fallback>`; overnight windows (`from` > `until`) wrap; a session pinned to a member whose window closes re-resolves on its next request while an in-flight request completes; config validation rejects malformed times, unknown IANA locations, a window with only one boundary, and a provider-level window combined with an `upstreams:` list; SIGHUP reload rebuilds the pool tree so window changes are live without a restart.

## v0.40.0

- feat: wire the spec-014 time-window eligibility filter into pool selection — `pkg.Window.Contains(now)` (`pkg/config.go`) evaluates a member's window (half-open `[From, Until)`, overnight wrap, `From == Until` = empty, evaluated in the value's attached IANA location) against the injected `libtime.CurrentDateTimeGetter`; `handler.UpstreamMember` gains `Window`/`Now`, and the upstream pool handler (`pkg/handler/upstream-pool-handler.go`) and model pool (`pkg/handler/model-pool.go`) recompute the weighted ring / least-loaded / overflow selection over the eligible subset per request (a member whose window closes mid-session re-resolves to an eligible member on the next request, in-flight requests complete; nil window = always eligible, byte-for-byte today's behavior); the model router (`pkg/handler/model-router.go`) skips a provider whose pool has no eligible member in key routing and the glob walk, falling through to the next eligible provider or `default_provider` with a V(2) `[route] provider=<p> window=closed -> <fallback>` line — a closed window is eligibility, never an error or 429; the factory (`pkg/factory/factory.go`) gains a `WithCurrentDateTime(clock)` option seam (default `libtime.NewCurrentDateTime()`), wires each member's `Window` + clock, and rebuilds both pool tree and router clock on SIGHUP so an edited `window:` is live without a restart.

## v0.39.0

- feat: add the optional `window:` eligibility block to the config contract (`pkg/config.go`, spec 014) — `Window{From, Until}` pairs of `libtime.TimeOfDay` values parsed from the `"HH:MM <location>"` form (e.g. `"18:00 Europe/Berlin"`), carried per `Upstream` member and on the legacy single-`upstream:` provider form (a provider-level window is copied onto the synthesized one-member pool by `normalizeUpstreams`, and onto the `Provider.UpstreamList()` fallback for programmatic configs); a provider-level window combined with an `upstreams:` list is rejected at load, malformed times and unknown IANA locations fail at yaml parse, a `window:` missing either `from` or `until` fails validation, and an overnight wrap (`from: "22:00"` `until: "06:00"`) loads cleanly; configs without any `window:` load byte-for-byte as before. Config contract only — the eligibility filter ships separately.

## v0.38.1

- fix: add `display:"length"` redaction tags to every secret config field (`Token`, both `AllowedApiKeys`, `AuthConfig.Key`, `Upstream.Token`) so bearer tokens and API keys are never printed in startup logs; thread context cancellation through the upstream-pool and model-pool selection loops (`pinSlot`, `leastLoaded`, `overflowTarget`, `providerKeys`, pool constructors) and wrap the bare `normalizeUpstreams` error; decompose the model-pool table build into `buildModelPools` / `buildPoolMember` / `sumInFlight` / `providerSaturated` so the factory stays under the maintidx/gocognit gates. These are the PR-review fixes landed after v0.38.0 was cut on the feature branch.

## v0.38.0

- feat: document the `model_pools:` block in `docs/config.md` (new `## Model pools` section + schema reference) and `docs/config.example.yaml` (commented two-member example) — a top-level `model_pools:` map turns an invented model name (e.g. `coding`) into an ordered list of `ModelPoolMember`s, each with a required `provider` (must exist under `providers:`) and `model` (the fixed concrete model string that provider sees), plus optional `weight` (default 1, negative rejected at load) and `overflow` (default false); a client-sent `model: <poolname>` is resolved by the pool pre-step BEFORE alias/key/glob routing — the body's `model` field is rewritten to the member's concrete model (`rewriteModelField`) and routed through that member's provider, whose own upstream pool, session pinning, and caps then apply (see `## Upstream pools`); a client setting `x-session-id` is pinned to the same member on every request via a stateless weighted ring hash of the id (same session stays on the same member across requests and restarts for cache warmth), an idless request goes to the least-loaded member with round-robin tie-breaking so bursts spread instead of stacking on the first-declared member, `weight` scales each member's share of pinned sessions, a pinned member whose provider is saturated overflows to the least-loaded sibling only when it declares `overflow: true` (the default keeps the request on its pinned member and its provider's own limiter answers HTTP 429), a non-pool model name falls through to the existing alias + provider-glob routing untouched (`model_pools:` and `aliases:` are independent), config validation rejects an unknown provider / negative weight / duplicate `(provider, model)` pair / empty member list naming the pool, and a SIGHUP reload rebuilds the pool table so pool edits are live without a restart.
- feat: resolve a client-sent `model: <poolname>` as a pre-step in the model router before alias/key/glob routing (`pkg/handler/model-pool.go`, `NewModelRouterWithPools`): the pool's weighted FNV-1a ring hash pins each session id to one member whose concrete model is written into the request body and whose provider serves it, an idless request goes to the least-loaded member with round-robin tie-breaking so bursts never stack on the first-declared member, a pinned member whose provider is saturated overflows to the least-loaded sibling only when it declares `overflow: true` (the default keeps the request on its pinned member and its provider's own limiter answers), and a non-pool model name falls through to today's alias + key + glob routing unchanged; `pkg/factory/factory.go` builds the pool table from the `model_pools:` config with real per-provider load/saturation closures and rebuilds it on SIGHUP, so adding, removing, or re-weighting a pool member is live without a restart.

## v0.37.0

- feat: add the `model_pools:` config contract and `ModelPoolMember` type (`pkg/config.go`) — a top-level `model_pools:` block maps an invented model name to an ordered list of members, each naming a `provider`, a fixed concrete `model`, an optional `weight` (absent or 0 resolves to the default 1, negative is rejected), and an optional `overflow` flag; `Config.Validate` rejects a member whose provider is unknown, a pool with an empty member list, and a duplicate `(provider, model)` pair within one pool (the same pair in two different pools is not a duplicate). The new block is optional and the `aliases:` block parses exactly as before — existing configs load byte-for-byte unchanged. Config contract only — routing behavior is unchanged.

## v0.36.0

- feat: document the `Provider.upstreams:` pool schema and per-entry `Upstream` fields (`upstream` / `token` / `weight` / `maxConcurrentRequests` / `maxConcurrentWaitSeconds`) in `docs/config.md` (new `## Upstream pools` section) and `docs/config.example.yaml` — `upstreams:` is the alternative to the single `upstream:` for a provider with more than one server (the two are mutually exclusive; a provider setting both fails to load), while the legacy single `upstream:` / `token:` / provider-level-caps form loads unchanged as a one-entry pool with weight 1 whose caps are the provider-level values. A client that sets an `x-session-id` header is pinned to the same pool member on every request via a stateless weighted ring hash of the id (the header is stripped outbound, used for pinning only, never for auth), a keyless request is dispatched to the least-loaded member with round-robin tie-breaking among equally-loaded members, each member independently enforces its own `maxConcurrentRequests` cap (a request queuing past `maxConcurrentWaitSeconds` is answered HTTP 429 with the Anthropic-shaped `rate_limit_error` body — never a shared pool cap), a down member surfaces as the existing sanitized 502 (no probe-and-rotate), and a SIGHUP reload rebuilds the pool tree from the edited config; each dispatch logs `[route] session=<id> upstream=<url>` at glog V(2).

## v0.35.0

- feat: wire the per-upstream pool into the factory and mount session pinning end-to-end — `CreateRouterFromConfig` (`pkg/factory/factory.go`) builds one `handler.UpstreamMember` per entry in `Provider.UpstreamList()`, each with its own reverse proxy, token-swap transport, and concurrency limiter, wrapped in `handler.NewUpstreamPoolHandler`, so a provider's pool enforces per-server `maxConcurrentRequests` caps (a saturated pinned member answers HTTP 429 with the Anthropic-shaped `rate_limit_error` body — never a shared pool cap — and uncapped members stay unlimited); `buildMux` mounts `handler.NewSessionMiddleware` on `/v1/*` so `x-session-id` is stripped outbound before any upstream sees it; the generalized limiter exposes `InFlight()` (`pkg/handler/concurrency-limiter.go`) as the real per-member semaphore occupancy the pool's least-loaded selection reads; legacy single-`upstream:` providers wire as one-member pools and behave byte-for-byte as before, and a SIGHUP reload rebuilds the pool tree with the new members and caps.

## v0.34.0

- feat: add per-session upstream pinning and keyless least-loaded dispatch at the handler layer — every /v1/* request's `x-session-id` header is stripped outbound and carried on the request context (`handler.ContextWithSessionID` / `handler.SessionIDFromContext` in `pkg/handler/session-id.go`, `handler.NewSessionMiddleware` in `pkg/handler/session-middleware.go`), and `handler.NewUpstreamPoolHandler` (`pkg/handler/upstream-pool-handler.go`) dispatches each request to exactly one `UpstreamMember`: a non-empty session id is pinned to the same member on every request via a stateless weighted FNV-1a ring hash of the id (no session→member map, recomputable across restarts, so the session's prompt cache stays warm on one server), while a keyless request goes to the least-loaded member with round-robin tie-breaking so anonymous floods spread instead of stacking on the first-declared member; each dispatch emits a `[route] session=<id> upstream=<url>` glog V(2) detail line. Handler layer only — the middleware and pool handler are not yet wired into the route tree.

## v0.33.0

- feat: add the `Provider.upstreams:` pool schema and `Upstream` type (`pkg/config.go`) — a provider can declare a list of servers, each with its own `upstream`, `token`, `weight`, and per-server `maxConcurrentRequests`/`maxConcurrentWaitSeconds`; the legacy single `upstream:` form loads unchanged as a one-entry pool with weight 1 whose caps are the provider-level values (`Config.Validate` normalizes via `normalizeUpstreams`, exposed as `Provider.UpstreamList()`). Declaring both `upstream:` and `upstreams:` is rejected at load, a negative `weight` is rejected, and `weight: 0` or an absent key resolves to the default 1. Config contract only — routing behavior is unchanged.

## v0.32.0

- refactor: add `ctx.Done()` cancellation checks to the config/factory validation and wiring loops (`Validate`, `validateAliases`, `CreateRouterFromConfig`), and rename `RouterOption` to `RouterOptionFunc` per the function-type naming convention — clears recurring pr-review bot findings

## v0.31.0

- feat: add optional per-provider concurrency limits — `maxConcurrentRequests` and `maxConcurrentWaitSeconds` (`Provider.MaxConcurrentRequests` / `Provider.MaxConcurrentWaitSeconds` in `pkg/config.go`). A provider with a positive `maxConcurrentRequests` caps how many `/v1/*` requests reach its upstream at once: excess requests queue in a per-provider semaphore (`handler.NewConcurrencyLimiter` in `pkg/handler/concurrency-limiter.go`) and are forwarded unchanged when a slot frees within `maxConcurrentWaitSeconds` (default 30 when absent, 0, or negative, resolved by `pkg/factory/factory.go`); a request still waiting when the wait elapses is answered HTTP 429 with an Anthropic-shaped `rate_limit_error` JSON body so the client's own backoff retries cleanly, never a 5xx. The slot is held for the full request including streaming SSE responses; a client that disconnects while queued never holds a slot. Caps are per-provider and independent even when two providers share one upstream; validation is lenient (absent/0/negative `maxConcurrentRequests` = unlimited, negative wait = 30s default — the config always loads); SIGHUP reload applies changed values without a restart. Absent or non-positive values leave today's behavior byte-for-byte unchanged.

## v0.30.2

- fix: answer Claude Code's `{ANTHROPIC_BASE_URL}/api/hello` connectivity probe with a bare 200 (`handler.NewHelloHandler`, registered in `buildMux` ahead of the `/` catch-all) so the per-session HEAD probes stop flooding the unknown-path 404 log — the logger keeps surfacing every other unmatched route.

## v0.30.1

- fix: preserve provider declaration order when building routes (`Config.ProviderOrder` captured during YAML unmarshal, used by `CreateRouterFromConfig`), so "first glob match wins" is deterministic when two providers share a model glob — without it the keyless path was a per-restart coin flip over Go map iteration order, which could route all deepseek traffic to the wrong quota (spec 010).

## v0.30.0

- feat: add optional Traefik Ingress template (gated on `ingress.enabled`) so the router can be reached over TLS from outside the cluster (e.g. laptop `ANTHROPIC_BASE_URL` → `https://claude-code-router.quant.benjamin-borbe.de`). Per-cluster values set host + tlsSecret (tls-quant). Chart bumped 0.1.0 → 0.2.0.

## v0.29.0

- feat: route by the presented `x-api-key` (top-level `allowedApiKeys` registry + per-provider `allowedApiKeys` override, `Config.AllowedApiKeys` / `Provider.AllowedApiKeys` / `Config.AllowedApiKeySet()` in `pkg/config.go`): a key claimed by a provider's list dispatches the request to that provider (its outbound token) before model-glob selection, overriding globs (`pkg/handler/model-router.go`); a valid-but-unclaimed key routes by globs exactly like a keyless request, which is unchanged. The non-loopback auth gate (`pkg/handler/auth-middleware.go`) now validates `x-api-key` against the registry (constant-time comparison, loopback exempt, the header still stripped outbound), and trace files redact `x-api-key` alongside `Authorization`. The spec-009 `x-router-key` / `auth.key` / `ROUTER_AUTH_KEY` auth path is removed with fail-closed migration guards — a config still carrying `auth:` fails to load (`Config.Validate`) and the binary refuses to start with `ROUTER_AUTH_KEY` set (`pkg/factory/factory.go`). Registry changes apply on SIGHUP without a restart.

## v0.28.0

- feat: route by presented API key before model-glob matching in the model router (`pkg/handler/model-router.go`). When a request's authenticated `x-api-key` is claimed by a provider's `allowedApiKeys` list, the router dispatches to that provider directly (its outbound token), overriding model-glob selection — the glob walk is skipped wholesale (key wins over globs). A key present in the auth registry but claimed by no provider routes by glob exactly like the keyless case, and keyless requests are byte-for-byte unchanged (glob match then `default_provider`). The key travels from the auth middleware to the router via request context and is never logged or written to the body; the V(2)-gated `[route] key matched provider=<name>` detail line names the provider only. `pkg/factory/factory.go` populates the new `handler.ModelRoute.AllowedApiKeys` field from each provider's config list, so a key claimed by a provider pins routing for any of that provider's model globs.

## v0.27.0

- feat: add the optional top-level `allowedApiKeys` registry and per-provider `allowedApiKeys` list (`Config.AllowedApiKeys` / `Provider.AllowedApiKeys` / `Config.AllowedApiKeySet()` in `pkg/config.go`). The top-level registry is the auth superset and single rotation point for non-loopback `/v1/*` callers; a per-provider list pins key-routing to that provider. The valid inbound key set is the top-level registry when non-empty, else the union of all providers' lists. `Config.Validate` rejects a key claimed by two providers with an error naming the key and both providers; a key in both the top-level registry and a provider's list, or repeated within one provider's own list, is not a duplicate. Absent, `null`, and empty everywhere mean no key enforcement and no key routing — existing configs behave byte-for-byte as today.

## v0.26.0

- feat: add container packaging — Dockerfile (scratch, multi-stage) + Makefile.docker `buca` block for building and pushing `bborbe/claude-code-router` images. Follows `go-dockerfile-guide.md` (ca-certs + zoneinfo, `-mod=vendor` via `check-go-mod`, registry-parameterized). Local dev `make build` renamed to `build-local`.
- feat: add Helm chart (`helm/`) — Deployment + Service + ConfigMap (config.yaml) + Secret (existingSecret/secretEnv), published via `make helm-publish` to `oci://registry-1.docker.io/bborbe/claude-code-router`. Git-rest chart pattern; config mounts at `CONFIG_PATH`, LISTEN from env, probes + prometheus annotations on the same port. For the fleet-facing cluster router (burn relay + GLM/MiniMax fallback).

## v0.25.0

- feat: Resolve the inbound auth key env-first from the `ROUTER_AUTH_KEY` environment variable in `CreateRouterFromConfig` (`pkg/factory/factory.go`) — a non-empty value wins over the config's `auth.key` (so a config with no `auth:` block still enables auth, and a launchd wrapper can inject the TeamVault secret without the raw key ever appearing in the config file); when the env var is empty the config's `auth.key` literal applies unchanged, so existing configs keep working

## v0.24.0

- feat: Add optional inbound auth on `/v1/*` for non-loopback callers via the `x-router-key` header, configurable as `auth.key` (absent or empty ⇒ disabled; loopback exempt; SIGHUP applies the change)
- feat: Enforce an unconditional loopback-only guard on `/setloglevel`, `/enabletrace`, `/disabletrace` and `/gc` (403 for non-loopback), protecting the admin endpoints once the listener binds beyond `127.0.0.1`

## v0.23.0

- feat: enforce loopback-only access on the four state-changing admin endpoints (`/setloglevel/`, `/enabletrace`, `/disabletrace`, `/gc`) via `handler.NewAdminLoopbackGuard` (`pkg/handler/admin_loopback_guard.go`), wired at the `buildMux` registration site in `pkg/factory/factory.go`. Any non-loopback request is refused with HTTP 403 (`admin endpoint loopback-only`) before handler logic runs, so a remote caller can never toggle tracing (body capture), force GC, or change log levels even when they are the only other caller once the listener moves to `0.0.0.0:8788`. The guard is unconditional (no config knob to disable it), reads the remote address only from the connection (`r.RemoteAddr`, never `X-Forwarded-For` / `X-Real-IP`), and emits exactly one `admin refused path=<method+path> remote=<addr>` line per refusal. The inner handlers are wrapped, not modified; read-only endpoints (`/healthz`, `/readiness`, `/metrics`, `HEAD /`) stay open to remote callers so health probes keep working.

## v0.22.0

- feat: enforce optional inbound auth on `/v1/*` when `auth.key` is configured. `handler.NewAuthMiddleware` (`pkg/handler/auth-middleware.go`) rejects non-loopback requests that do not present the matching key in the `x-router-key` header with 401 (constant-time compare via `crypto/subtle`), bypasses the check for loopback requests, and strips the header from a cloned request before it reaches any upstream, so a key carried via `ANTHROPIC_CUSTOM_HEADERS` never leaks to a provider. Auth-less configs pass through byte-for-byte (the constructor returns `next` unchanged, zero hot-path effect). The header is redacted to `***` in trace files (alongside `Authorization` / `x-api-key`) and to `<redacted len=N>` in V(3) `[upstream.headers]` logs. A rejection emits exactly one `auth rejected remote=<addr>` line that never contains the presented or configured key. SIGHUP reloads pick up `auth.key` changes because the whole mux is rebuilt via `CreateRouterFromConfig`.

## v0.21.0

- feat: add the optional top-level `auth:` config block with a single `key` field (`Config.Auth` / `AuthConfig.IsEnabled()` in `pkg/config.go`). Absent, `null`, and an empty `key` all mean inbound authentication is disabled and the router behaves byte-for-byte as today; `Config.Validate` rejects nothing. Also add `handler.IsLoopbackRemoteAddr` (`pkg/handler/loopback.go`), which classifies both IPv4 (`127.0.0.0/8`) and IPv6 (`::1`) loopback from a connection-supplied `http.Request.RemoteAddr` — the remote address is never taken from `X-Forwarded-For`. This lands the seam the inbound-auth middleware and admin-route guard prompts consume; no request-path behavior changes yet.

## v0.20.0

- feat: add the optional per-provider `requiresLeadingSystem` config field — glob patterns naming models whose chat template rejects a `system`-role message that is not the conversation's first entry. For a matching model the router lifts misplaced system messages into the top-level `system` block, preserving order, and logs `[system-lift] model=<model> moved=<n>` at V(2). Fixes qwen3.8 through ollama, which returned `HTTP 500 system message must be at the beginning` on every Claude Code request. Scoped per model rather than per provider, since the restriction lives in each model's chat template — qwen3.6 and qwen3.8 disagree behind one provider. Absent or empty means byte-identical forwarding, so existing configs are unaffected; an uninterpretable body is forwarded unchanged with one warning rather than failing the request.

## v0.19.2

- fix: bump Go toolchain to 1.26.6, fixing stdlib vulnerabilities GO-2026-5026, GO-2026-5972, GO-2026-6089, GO-2026-6090
- fix: bump golang.org/x/mod to v0.40.0 and golang.org/x/text to v0.41.0, fixing GO-2026-6179, GO-2026-6180 (sumdb tlog verification bypass and unauthenticated hash lookup) and GO-2026-5970 (infinite loop on invalid input)

## v0.19.1

- Bump Go toolchain to 1.26.5
- Update bborbe/argument, errors, http, log, run, service, time dependencies
- Update transitive bborbe dependencies (collection, kv, math, parse, sentry, validation)

## v0.19.0

- feat: migrate default config path to XDG `~/.config/claude-code-router/config.yaml`, falling back to the legacy `~/.claude-code-router/config.yaml` when only that exists. New installs land in the XDG location; existing installs keep working unchanged. `--config-path` / `CONFIG_PATH` still override unconditionally.

## v0.18.1

- fix: strip the `[1m]` context-window marker Claude Code appends to model names (e.g. `deepseek-v4-pro-max[1m]`) before dispatch. The suffix is a client-side annotation marking a 1M-token context window; upstreams (notably seibert-vLLM) reject the literal suffixed name with 4xx. The router now trims a trailing `[1m]` from the request `model` field, rewrites the body so the upstream sees the canonical name, and emits the cleaned name as the metrics label so `[1m]` and non-`[1m]` requests share a single `ccrouter_requests_total` / `ccrouter_tokens_total` series. Stripping is universal (all providers), only fires on an exact trailing `[1m]` (never mid-string), and preserves all other body fields. Live evidence: 25 requests (`deepseek-v4-pro-max[1m]` ×3, `deepseek-v4-pro-fast[1m]` ×20, `deepseek-v4-pro[1m]` ×2) were 100% 4xx before this fix.

## v0.18.0

- feat: add `ccrouter_tokens_total{provider,model,direction}` counter fed from the already-landed `ExtractUsage` tee. Operators can chart `sum by (provider, direction) (rate(ccrouter_tokens_total[5m]))` to see LLM token throughput per provider and per model. Direction is bounded to `input`/`output`; zero, negative, non-numeric, and unknown-direction inputs are dropped at the `ObserveTokens` seam so bad upstream data never inflates Prometheus. Non-2xx responses do not increment the counter — token counting is a strict success-path observation.
- feat: expand `ccrouter_requests_total{status_class}` from 4 buckets (`2xx`/`3xx`/`4xx`/`5xx`) to a 7-value taxonomy (`2xx`, `3xx`, `4xx_auth` for 401/403, `4xx_rate_limited` for 429, `4xx_bad_request` for other 4xx, `5xx_upstream` for upstream 5xx, `5xx_router` for router-side 5xx). Operators can now alert on `status_class="4xx_rate_limited"` specifically instead of any 4xx, distinguish auth failures from body-parse failures, and separate upstream faults from router-side rejections. See `docs/metrics.md` for updated Grafana + alerting examples. **Breaking:** this is a clean supersede — dashboards or alerts built against `status_class="4xx"` or `status_class="5xx"` will return empty on merge and must be updated to the 7-value enum.
- feat: route the three router-side early-return paths (body-too-large 413, body-read-failed 400, alias-rewrite-failed 500) through `metrics.ObserveRequest(..., isRouterError=true)` so they emit `4xx_bad_request` / `5xx_router` in `ccrouter_requests_total`. Previously these paths bypassed metrics entirely — an operator staring at Grafana saw a healthy service while requests were being rejected at the door.
- fix: model label on `ccrouter_requests_total` and `ccrouter_tokens_total` no longer emits `model=""` for probe traffic or misshapen bodies. New sentinel chain (post-alias resolved → pre-alias original → `_unknown_`) resolves the label at the call site; the exported `handler.UnknownModelLabel = "_unknown_"` constant provides the sentinel. Every "top-N models" Grafana panel now breaks down real model names — no empty-string row hiding the sum of no-model-field traffic.
- **Breaking:** `handler.Metrics.ObserveRequest` signature gains a 5th positional argument `isRouterError bool`. The happy path at `NewModelRouter` passes `false`; the three router-side early-return paths pass `true`. Existing `metrics_test.go` call sites (8) were updated in lockstep.

## v0.17.2

- fix: extract token counts for gzip-encoded upstream responses. v0.17.0's extractor + v0.17.1's split-event widening both scanned text markers over raw response bytes; live trace of the primary production provider (`anthropic-subscription` via Cloudflare) revealed that Anthropic serves `Content-Encoding: gzip` on both JSON and SSE responses, so `usage` / `event: message_` never appear in the tail and 5/5 post-v0.17.1 200s still logged `in=- out=-`. Additionally, real non-streaming JSON responses reach ~500 KB gzipped — far beyond the previous 64 KB tail bound, and gzip is not self-synchronizing so a mid-stream tail is unrecoverable. Fix: grow `TailBufferBytes` from 64 KiB to 2 MiB (frozen constant, no YAML field), decode `Content-Encoding: gzip` (case-insensitive, whitespace-trimmed) before running the existing SSE/JSON scans, bound decompression at 8 MiB to defend against decompression bombs, and pass `Content-Encoding` from the reverse-proxied response through the extractor. Missing header and `identity` behave as no-ops; `br` / `deflate` / `zstd` / chained encodings log `in=- out=-` (deferred to a follow-up bug if observed in production). Corrupt or truncated gzip returns `noUsage` cleanly via the pre-existing `defer recover()` and empty-tail guard. The `Unwrap()` chain, the `[req]` line format, and the minimax + uncompressed-SSE paths are unchanged; the request path (including `Accept-Encoding`) is untouched. See [specs/in-progress/006-bug-tokens-gzip-decompress.md](specs/in-progress/006-bug-tokens-gzip-decompress.md).

## v0.17.1

- fix: extract token counts for Anthropic SSE responses. v0.17.0's extractor worked for JSON responses (`minimax`, 452/457 = 99% success) but returned the `noUsage` sentinel for 100% of `anthropic-subscription` 200 responses (0/19) because (a) `Content-Type` sniffing via `rec.Header()` was unreliable for reverse-proxied SSE responses and (b) Anthropic splits `input_tokens` (in the `message_start` event) from `output_tokens` (in the terminal `message_delta` event), while the extractor scanned only the terminal event. Fix: detect SSE via `Content-Type` OR a content scan for the `event: message_` marker, and scan for BOTH `message_start` (for `input_tokens`) and terminal `message_delta` (for `output_tokens`), combining the two into a single `TokenUsage`. Partial-data behavior: `message_start` only → `in=<N> out=-`; `message_delta` only → `in=- out=<M>`; neither → `in=- out=-`. The `Unwrap()` chain, the 64 KB tail buffer, and the `[req]` line format are unchanged; the `minimax` JSON path is unchanged. See [specs/in-progress/005-bug-anthropic-tokens-not-extracted.md](specs/in-progress/005-bug-anthropic-tokens-not-extracted.md).

## v0.17.0

- feat: wire token-usage extraction into `NewModelRouter` (prompt 3). The bounded tail buffer (`usageRecorder` from prompt 1) is inserted between the response writer and the upstream handler so every response byte is teed; `ExtractUsage` (prompt 2) runs after the handler returns to pull `input_tokens`/`output_tokens` from the tail. The `[req]` log line at `V(1)` now appends `in=<N> out=<N>` for 200 responses (SSE `message_delta` usage or non-streaming JSON `usage` block) and `in=- out=-` for non-200 or missing-usage paths. The sampler gate, `V(1)` gating, field order, and the `Unwrap()` chain are all unchanged. See [specs/in-progress/004-log-input-output-tokens.md](specs/in-progress/004-log-input-output-tokens.md).

- feat: token-usage extractor (prompt 2). `ExtractUsage(tail []byte, contentType string) TokenUsage` scans the bounded tail buffer for input/output token counts: SSE path detects `Content-Type: text/event-stream` and locates the terminal `event: message_delta` block to parse `usage.input_tokens`/`usage.output_tokens`; non-streaming JSON path parses the top-level `usage` object. Every parse failure (truncated buffer, malformed SSE/JSON, missing usage, zero tokens) returns the sentinel `noUsage` (`"-"`/`"-"`) — never a panic and never an error that aborts the `[req]` log line. Accompanied by Ginkgo table-driven specs covering SSE single-event, SSE multi-event, SSE with full-buffer filler, SSE with terminal-event eviction, non-streaming JSON with usage, non-streaming JSON without usage, non-streaming JSON with present-zero, malformed JSON, malformed SSE, empty tail, content-type mismatch, and panic-safety cases. Prompt 3 wires this into `NewModelRouter`. See [specs/in-progress/004-log-input-output-tokens.md](specs/in-progress/004-log-input-output-tokens.md).

- feat: `usageRecorder` response-writer primitive tees every byte written to the response into a bounded tail buffer (`TailBufferBytes` = 64 KB) that retains only the last bytes written, so the terminal SSE `message_delta` event (and non-streaming JSON `usage` body) survives for later token-count extraction. Write-through path is unchanged (no added latency); `Unwrap()` returns the wrapped `*statusRecorder` so `http.NewResponseController` still reaches the underlying Flusher/Hijacker (SSE flush regression guard). Not yet wired into `NewModelRouter` — extraction (prompt 2) and wiring (prompt 3) ship separately. See [specs/in-progress/004-log-input-output-tokens.md](specs/in-progress/004-log-input-output-tokens.md).

## v0.16.0

- **feat: EnableTrace/DisableTrace endpoints with 5-min TTL.** Two new operator-local HTTP endpoints (`POST /enabletrace`, `POST /disabletrace`) toggle per-request trace logging without a router restart. `enabletrace` turns tracing on for a bounded 5-minute window that auto-disables on expiry (repeated calls reset the window); `disabletrace` turns it off immediately and cancels the pending timer. The trace middleware is now mounted unconditionally on `/v1/` and consults a process-internal atomic flag per request (flag-OR-config: the legacy `trace:` config flag still works as an always-on opt-in, now deprecated). No persistence across restarts; the toggle does not depend on config reload or SIGHUP. `Authorization` and `x-api-key` redaction to `***` is unchanged. See [docs/config.md#trace](docs/config.md).

## v0.15.0

- feat: SIGHUP-driven hot config reload. The router now picks up config file edits without a process restart: sending SIGHUP rebuilds the entire request-dispatch handler tree from the freshly loaded YAML and atomically swaps it in via `atomic.Value`. In-flight requests finish against the old tree undisturbed. Malformed config (missing file, invalid YAML, validation failure) is rejected and the old config stays active. A panic during mux rebuild is recovered and logged. Token values are never logged — only provider counts.
- fix: prevent `signal: hangup` process termination after reloader test suite exits. The package-level SIGHUP interceptor now stays registered for the entire test process lifetime (instead of being repeatedly reset by per-test `signal.Reset` calls that created a race window). An `AfterSuite` hook drains and stops the interceptor before Go's exit sequence. Additionally, each spec gets a fresh `prometheus.DefaultRegisterer` via `BeforeEach` to silence duplicate-collector warnings.

## v0.14.0

- **feat: per-request trace logging.** New optional top-level `trace:` boolean in `~/.claude-code-router/config.yaml`. When `true`, every `/v1/*` request writes one JSON file to `~/.claude-code-router/trace/<timestamp>-<request-id>.json` capturing the full request (method, path, headers, body) and response (status, headers, body). `Authorization` and `x-api-key` headers are redacted to `***`; all other headers and bodies are logged verbatim. When `false` (or absent), no trace files are written and no trace middleware is allocated. Read once at config load; restart to apply. See [docs/config.md#trace](docs/config.md).

## v0.13.0

- feat: `HEAD /` returns 200 OK instead of falling through to the catch-all 404 logger. Claude Code's HTTP client probes the base URL for liveness before its first `/v1/messages` on a fresh connection, which previously emitted a `[404] HEAD /` line ahead of every real request.

## v0.12.0

- raise `MaxRequestBodyBytes` from 1 MB to 32 MB to match the Anthropic API ceiling — long Claude Code sessions (full conversation history + tool definitions + sub-agent results) routinely exceed 1 MB and were rejected with a confusing 413 that surfaced as `Request too large (max 32MB)` from the client.

## v0.11.2

- **Breaking**: `NewLoggingRoundTripper` gains 3rd `currentDateTime libtime.CurrentDateTimeGetter` param; replaces `time.Now()` with injected clock in TTFB measurement (factory + tests updated — closes the no-time-now-direct rule violation; bot-deferred follow-up from PR #12/PR #18).

## v0.11.1

- **Breaking**: `Load`, `Validate`, `CreateServer`, `CreateRouterFromConfig` signatures gain `ctx context.Context` as first positional parameter. Internal API — no external callers.
- refactor: replace 15× `fmt.Errorf` in `pkg/config.go` and `pkg/factory/factory.go` with `bberrors.Wrapf(ctx, ...)` and `bborbe/errors.New`. Threads `context.Context` through `Load`, `Validate`, `CreateServer`, `CreateRouterFromConfig`.

## v0.11.0

- **Breaking**: `NewModelRouter` gains 7th `currentDateTime libtime.CurrentDateTimeGetter` param; replaces `time.Now()` with injected clock (factory + tests updated)
- **Breaking**: `NewLoggingRoundTripper` gains `bodySampler liblog.Sampler` param; adds V(4) `[upstream.req.body]`/`[upstream.resp.body]` lines with 4 KB body sampling and Bearer-token redaction via `RedactBearerTokensInBody`
- refactor: replace 3× `fmt.Errorf` in `rewriteModelField` with `bberrors.Wrapf(ctx, ...)`, threading `r.Context()` through the call
- refactor: inline `logReq` back into `NewModelRouter` (prior extraction was a naive gocognit-driven fix — reverted)
- deps: promote `github.com/bborbe/errors` and `github.com/bborbe/time` to direct deps; bump multiple indirect deps (sentry-go, prometheus, golang.org/x/tools, etc.)

## v0.10.1

- **feat: V(4) request+response body sample logging via SamplerList.** New `[upstream.req.body]` and `[upstream.resp.body]` glog V(4) lines per upstream RoundTrip, gated by `liblog.SamplerList{NewSampleTime(1s), NewSamplerGlogLevel(5)}` — body dumps fire at most 1/second at V(4), OR unconditionally at V(5) for deep-debug sessions. Body captured up to 4 KB; total length printed alongside (`body_len=N sample=...`) so operators know if truncation happened. `Bearer\s+\S+` substrings are regex-redacted via new `RedactBearerTokensInBody` helper in `pkg/handler/redact.go` — defense-in-depth for the rare case where Anthropic echoes a credential in a `metadata:` SSE field. **Breaking: `handler.NewLoggingRoundTripper` signature gains a `liblog.Sampler` parameter** (factory updated, no external callers).
- **refactor: move alias counter pre-initialization from factory into `NewMetrics` constructor.** `NewMetrics` now takes `aliases map[string]string` and seeds `ccrouter_alias_resolutions_total{alias,resolved}` series to zero for each declared alias, so the wiring sits next to the counter it primes instead of one call layer up in `CreateRouterFromConfig`. A `nil` aliases map is safe (no panic, zero iterations). Operator-side observability guarantee preserved: alerts for unhit aliases still evaluate to `0` instead of no-data.

## v0.10.0

- **fix: cap inbound `/v1/*` request body at 1 MB via `http.MaxBytesReader`.** Closes a pre-existing concern raised by the bot reviewer on PR #6: `io.ReadAll(r.Body)` in `NewModelRouter` had no size bound, so an adversarial / accidental multi-GB upload could exhaust router memory. 1 MB cap is ~10x typical Anthropic-shaped payloads (<100 KB system+context+user+attachments). Over-limit requests return HTTP 413 + generic "request body too large" body — no internal state leaked. Threat model is low (personal-tool, local-only listener behind macOS firewall), so the change is defensive rather than urgent.
- **feat: V(3) outbound header logging with credential redaction.** New `[upstream.headers]` glog V(3) line per upstream RoundTrip — dumps the request headers (after the auth-swap transport applied its `Authorization`) so operators can see exactly what went on the wire. `Authorization`, `Cookie`, `Set-Cookie`, and any header name matching `api-key`/`auth-token`/`secret`/`password`/`bearer` get value-redacted as `<redacted len=N>`. Helper `RedactHeadersForLog` lives in `pkg/handler/redact.go` for reuse by upcoming V(4) body-sample work. Silent at default V(1)/V(2); enable via `curl http://127.0.0.1:8788/setloglevel/3`.

## v0.9.1

- **fix: raise `DefaultProxyTransport.ResponseHeaderTimeout` from 60s to 5min.** Long-generation requests (e.g. `/compact` on a large session, big code-gen prompts) regularly need 60-300s before Anthropic sends the first byte of response headers. The old 60s cap produced `net/http: timeout awaiting response headers` 502s mid-flight, which in claude-code manifested as `/compact` appearing to hang at 95% — claude-code's SDK silently retried after each 502, so what looked like a stuck 7-minute `/compact` was actually multiple stuck 60s rounds plus one successful round. Bump to 5 minutes covers the worst observed case while still bounding a genuinely-wedged connection.
- **fix: raise HTTP server `WriteTimeout` to 10min and `ReadTimeout` to 5min (were 30s each).** libhttp.NewServer's defaults capped each leg of the streaming chain — `WriteTimeout=30s` killed any SSE response that streamed for more than 30 seconds (router → claude, common on `/compact` body streaming); `ReadTimeout=30s` killed any request body that took more than 30 seconds to upload (claude → router, relevant for `/compact`'s large session context). Both are wrong defaults for an LLM-proxy use case. Kept finite (not 0) so genuinely wedged Anthropic outages surface as clean server-side timeouts the operator can investigate instead of piling up goroutines forever as claude-code's SDK retries. Worst-observed `/compact` body stream was ~1min, so 10min WriteTimeout is generous 10x headroom; ReadTimeout 5min similarly. `ReadHeaderTimeout=10s` and `IdleTimeout=60s` stay at defaults.
- **debug: V(4) `[upstream]` log line per upstream RoundTrip.** New `NewLoggingRoundTripper` (in `pkg/handler/logging-roundtripper.go`) wraps the auth-swap transport; logs `[upstream] METHOD host/path ttfb=X status=N` at glog V(4). Silent at default verbosity; bump via `curl http://127.0.0.1:8788/setloglevel/4` for one debug session (auto-reverts after 5min). Useful for distinguishing "Anthropic slow to send first byte" (high TTFB) from "Anthropic slow to stream body" (low TTFB, high `[req]` latency).
- **docs: inline the full sample config in `README.md`** so new operators see the YAML shape at a glance instead of clicking through to `docs/config.example.yaml`. Adds the canonical 3-badge set (Go Reference, Go Report Card, DeepWiki) per `readme-guide.md` — was CI-only before.
- **docs: scrub internal-org references** from public docs. Removes the `seibert-vllm` provider example, teamvault token-paste hints, and the `→ seibert-vllm` example comment from `README.md`, `docs/config.example.yaml`, `docs/config.md`, and `docs/dark-factory-integration.md`. Replaced with generic `<YOUR_MINIMAX_API_KEY>` / `<your MiniMax API key>` placeholders. Public-repo hygiene — the docs should be useful to anyone setting up a router, not gated on internal credential-store access.

## v0.9.0

- **feat: Prometheus `/metrics` endpoint.** Replace the `# metrics not enabled in v1 skeleton` stub with `promhttp.Handler()` against the default Prometheus registry (matches go-skeleton convention — also exposes `go_gc_*`, `go_memstats_*`, `process_*` runtime series for spotting GC pressure / memory growth on the long-running router daemon). Three `ccrouter_*` application series: `ccrouter_requests_total{provider,model,status_class}` counter, `ccrouter_request_duration_seconds_bucket{provider,model}` histogram (LLM-shaped buckets 100ms…60s), `ccrouter_alias_resolutions_total{alias,resolved}` counter. Cardinality ~1k application series total at 5 providers × 15 models. Metrics emit unconditionally per request (NOT sampled — log sampling stays at the V(1) `[req]` line). Operator scrape config + Grafana queries in `docs/metrics.md`. Closes the open backlog item under [[Multi-Provider Claude Code Proxy]].
- **breaking: `handler.NewModelRouter` signature gains a `*handler.Metrics` parameter** (last positional). Same pattern as PR #6 `defaultProviderName` and PR #8 `sampler` adds. `factory.CreateRouterFromConfig` already threads it; no YAML config-format change.
- **fix: SSE flush passthrough in `statusRecorder`.** Add `Unwrap() http.ResponseWriter` so `http.NewResponseController` (Go 1.20+) can reach the underlying writer's `Flush`/`Hijack`/`SetReadDeadline`/`SetWriteDeadline` through the wrapper. Without it, SSE chunks from Anthropic piled up in an intermediate buffer instead of flushing per chunk — symptom was Claude Code spinners stuck mid-stream and `/compact` appearing to hang at 95% (bytes did arrive, just all at once when the response closed). Regression introduced when `statusRecorder` was extracted into the model-router (PR #6); affected every SSE response since v0.6.0.
- **fix: pre-initialize `ccrouter_alias_resolutions_total{alias, resolved}` for each configured alias.** Per [go-prometheus-metrics-guide.md#counter-pre-initialization](https://github.com/bborbe/coding/blob/master/docs/go-prometheus-metrics-guide.md): without `.Add(0)` at startup, `rate(ccrouter_alias_resolutions_total[5m]) > X` alert expressions return no-data (not zero) until the alias is first hit, so alerts can't fire on a system that's broken but hasn't yet routed a single aliased request. Request counter labels include unbounded `model` so pre-init doesn't apply there.

## v0.8.0

- **refactor: flatten `pkg/cli` + `pkg/config` into `pkg/`.** Aligns with [[Go Package Layout Guide]] — default is a single flat `pkg/` package with two conventional exceptions (`pkg/factory/` + `pkg/handler/`); none of the 5 split triggers (cycle break, >30 files, etc.) apply to `cli` or `config`. Removes `pkg/cli/cli.go` (1 file) and `pkg/config/config*.go` (3 files) — files moved to `pkg/cli.go`, `pkg/config.go`, `pkg/config_test.go` with `package pkg`. Duplicate `pkg/config/config_suite_test.go` dropped (`pkg/pkg_suite_test.go` already covers the `pkg_test` suite). Import-only impact: `cli.NewApp` → `pkg.NewApp`, `config.Load` / `config.Config` → `pkg.Load` / `pkg.Config`. No external callers; factory + main updated.

## v0.7.0

- **feat: sample 200 `[req]` log lines.** `NewModelRouter` gains a `log.Sampler` parameter (factory passes `liblog.DefaultSamplerFactory.Sampler()` — `SamplerList{NewSampleTime(10s), NewSamplerGlogLevel(4)}`). Non-200 responses are always logged (errors are signal); 200s are logged at most once per 10s, OR unconditionally when `-v` ≥ 4 — so `curl /setloglevel/4` brings back per-request visibility for deep debug. Steady-state log becomes operator-readable under concurrent /model traffic.
- **feat: log unknown-path 404s.** New `NewNotFoundHandler` registered at `/` in the factory's mux. Catches anything not matched by `/v1/`, `/healthz`, `/readiness`, `/metrics`, `/setloglevel/`, or `/gc`. Logs at V(1) as `[404] METHOD path` so probes / typos (`/messages` without `/v1`) surface in the operator log instead of vanishing into stdlib's bare 404 default.
- **breaking: `handler.NewModelRouter` signature gains a `liblog.Sampler` parameter** (last positional). Same shape as PR #6's `defaultProviderName` add — `factory.CreateRouterFromConfig` already threads it; no YAML config-format change.

## v0.6.0

- **feat: structured per-request log line.** Replace the two-line `[route]` + `[req]` pair with a single structured line at glog V(1): `[req] POST /v1/messages model=m3 alias=MiniMax-M3-highspeed provider=minimax status=200 latency=842ms`. Fields cover incoming model, alias resolution (if any), provider name from the YAML config, HTTP status, and total wall-time latency rounded to ms. Alias-resolution + route-match detail demoted to V(2). Outer `NewLoggingHandler` middleware removed — admin endpoints (`/healthz`, `/readiness`, etc.) no longer log per request.
- **feat: runtime log-level toggle via `/setloglevel/<level>`.** Replace the noop stub with a real handler backed by `bborbe/log.LogLevelSetter`. `curl http://127.0.0.1:8788/setloglevel/3` bumps verbosity without restarting the launchd agent; auto-reverts to V(1) after 5 minutes so a forgotten bump can't leave the router in verbose mode indefinitely. Returns 400 on a non-integer level.
- **breaking: `handler.NewModelRouter` signature change.** New `defaultProviderName string` parameter (positional, after `routes`) so the fallback path appears in the structured log. `handler.ModelRoute` gains a `ProviderName` field. `factory.CreateRouterFromConfig` already threads these through — no YAML config-format change required.

## v0.5.1

- docs: add `docs/dark-factory-integration.md` — end-to-end recipe for routing dark-factory's YOLO containers through the local router. Covers the 4 required changes (router `0.0.0.0` bind, claude-yolo tinyproxy allowlist, `--add-host=host.docker.internal:host-gateway` for Linux portability, `~/.dark-factory/config.yaml` redirect), the platform matrix (Docker Desktop / OrbStack / Rancher Desktop auto-resolve `host.docker.internal`; raw Linux `dockerd` doesn't), verification curl/launchd procedure, and failure-mode table.

## v0.5.0

- feat: Add `aliases:` YAML block to router config for mapping short model names to full model strings, with collision validation and orphan-target warning
- feat: ModelRouter consults `aliases` map and rewrites the request body's `.model` field on a hit before glob-routing; upstream sees the resolved full model name; emits `[alias] <short> -> <resolved>` at glog V(1). Nil/empty aliases map is a no-op (backward compatible). Body rewrite preserves all other top-level fields. Returns 500 if JSON re-marshal fails mid-flight.
- README refreshed for v0.4.0 reality: drop "v1 skeleton state" language, correct config path `~/.dark-factory/...` → `~/.claude-code-router/...`, add Install step 2 (create config from example), add "Switching providers mid-session" section showing `/model` usage. Replaced lone "WICHTIG" with "IMPORTANT" (English consistency).
- launchd + systemd service docs now include the `-config-path` flag in `ProgramArguments` / `ExecStart` — without it the binary loads the default path (`~/.claude-code-router/config.yaml`), which is fine, but explicit-in-the-doc avoids the "where do I plug the config?" question.
- Service docs note the config file must exist before `launchctl bootstrap` / `systemctl --user enable --now` — agent crash-loops if config is missing.
- **Model aliases.** New optional `aliases:` block in `~/.claude-code-router/config.yaml` maps short names to full model identifiers (e.g. `qwen: qwen3.6:35b-a3b-coding-nvfp4`). Operator types `/model qwen`; the router rewrites the request body's `.model` field to the full name single-hop, before provider routing — the upstream always sees the full name. Validation: hard error on alias-key colliding with a provider name; glog warning when an alias target matches no provider glob. Configs without `aliases:` continue to load unchanged. See [docs/config.md#aliases](docs/config.md#aliases).

## v0.4.0

- **Multi-provider routing via YAML config.** Router now loads `~/.claude-code-router/config.yaml` (override with `--config-path`) and dispatches `/v1/*` requests by the body's `model` field. Each provider declares its upstream URL, optional `token:` (replaces Authorization with `Bearer <token>`; absent = forward client's OAuth bearer untouched), and a list of `filepath.Match` glob patterns. Unmatched models fall through to `router.default_provider`.
- New packages: `pkg/config` (YAML loader + validation), `pkg/handler/NewModelRouter` (body-parses `model` field, glob-matches, dispatches), `pkg/handler/NewAuthSwapTransport` (per-request Authorization swap, request cloned so caller's headers aren't mutated).
- `pkg/factory.CreateRouterFromConfig` wires per-provider proxies + the model-router; `factory.CreateServer` signature changed to `(listen, configPath)` and now returns `(run.Func, error)` to surface config-load failures.
- Sample config at `docs/config.example.yaml`; full schema reference in `docs/config.md`.
- Mid-session switching: `/model <name>` in Claude Code is all that's needed — no router restart.

## v0.3.0

- Mount Anthropic reverse proxy on `/v1/` — every Claude Code request (`/v1/messages`, `/v1/models`, etc.) now forwards verbatim to `https://api.anthropic.com`. The Authorization header (subscription OAuth bearer) passes through untouched; upstream errors surface as `502 Bad Gateway` with the error message. Task 3 will add model-name routing to other providers.
- Add `pkg/handler/NewAnthropicProxyHandler` (wraps `libhttp.NewProxy`) with 3 Ginkgo specs: POST forward + body preservation, Authorization header pass-through, 502 on upstream transport failure.

## v0.2.0

- Add `pkg/handler/NewLoggingHandler` middleware; wrap the router in it so every request emits `[req] METHOD path -> STATUS` at glog `V(1)`. Makes router activity visible during local testing (essential for diagnosing whether Claude Code reached the router when `/v1/messages` is still 404 in the skeleton state).

## v0.1.2

- `make run` now sets `-listen=127.0.0.1:8788 -logtostderr -v=2` so router activity is visible on stderr during local testing (previously ran with defaults and no log output).

## v0.1.1

- Add `docs/launchd-service.md` and `docs/systemd-user-service.md` — copy-paste install for macOS launchd and Linux systemd-user (mirrors the semantic-search docs pattern; no install.sh script to maintain).
- README rewritten with a 3-step Install (binary → service → `clauder`) and a dedicated `clauder` shell-function section explaining why it sets only `ANTHROPIC_BASE_URL` (subscription OAuth bearer would break under `ANTHROPIC_API_KEY`).

## v0.1.0

- Initial scaffold copied from `go-skeleton`, stripped to a local CLI tool shape (no k8s, no Kafka, no BoltKV, no Sentry, no Prometheus).
- Minimal `main.go` binds an HTTP listener on `127.0.0.1:8788` (configurable via `--listen`).
- `pkg/handler/healthz.go` returns `200 OK` on `/healthz`.
- `pkg/factory/factory.go` wires the router via `CreateRouter()`.
- BSD-2 license, GitHub Actions CI inherited from skeleton.

# claude-code-router config

The router loads its provider list from a YAML file. Default path (XDG):

```
~/.config/claude-code-router/config.yaml
```

Falls back to the legacy `~/.claude-code-router/config.yaml` if the XDG directory doesn't exist yet but the legacy one does.

Override with `--config-path` or `CONFIG_PATH` env var — an explicit value always wins over both defaults.

## Schema

```yaml
router:
  default_provider: <provider-key>     # required; must match a key under providers:

allowedApiKeys:                  # optional; list of API keys authenticating non-loopback /v1/* requests (see ## Routing by API key). Absent or empty disables key enforcement and key routing.
  - "<key>"

default_token: <string>             # optional; one shared outbound key inherited by every provider / pool member that declares no token: of its own (see ## Auth). A provider's own token: overrides it; with neither set, the client's Authorization header passes through unchanged.

trace: <bool>                         # optional; default false. When true, writes one JSON file per /v1/* request to ~/.claude-code-router/trace/ (deprecated — use POST /enabletrace for bounded trace windows; see ## Trace)

# model_pools:               # optional; invented model names that resolve to a choice of providers (see ## Model pools). Each entry carries provider/model/weight/overflow.
#   <poolname>:
#     - provider: <provider-key>   # required; must exist under providers:
#       model: <concrete-model>    # required; the fixed model string that provider sees
#       weight: 1                  # optional; default 1. Relative share of pinned sessions this member receives.
#       overflow: false            # optional; default false. If true, a saturated pinned member may fail over to a sibling member.

providers:
  <provider-key>:
    upstream: <URL>                    # required; e.g. https://api.anthropic.com
    token: <string>                    # optional; if absent, client's Authorization header passes through
    models:                            # filepath.Match glob patterns
      - "<pattern>"
      - ...
    requiresLeadingSystem:             # optional; glob patterns for models whose chat template rejects a non-leading system message (see ## Requires leading system)
      - "<pattern>"
      - ...
    # allowedApiKeys: # optional; list of keys that route to THIS provider, overriding model-glob selection (see ## Routing by API key)
    # maxConcurrentRequests: 8   # optional; cap concurrent /v1/* requests to THIS provider (see ## Concurrency limit). Absent or 0 or negative = unlimited.
    # maxConcurrentWaitSeconds: 30 # optional; how long a queued request waits for a slot before HTTP 429 (default 30)
    # throttle429Threshold: 3   # optional; enable adaptive pacing: once the 60s-windowed count of upstream 429s reaches this, subsequent /v1/* requests to THIS provider are delayed before forwarding (see ## 429 delay gate). Absent or 0 or negative = disabled.
    # throttleMaxDelaySeconds: 30 # optional; upper bound of the pacing delay while throttled (default 30)
    # window:                    # optional; legacy single-upstream form only — applies to the implicit single member (see ## Time-of-day windows). Cannot be combined with an upstreams: list.
    #   from: "08:00 Europe/Berlin"
    #   until: "18:00 Europe/Berlin"
    # days:                       # optional; legacy single-upstream form only — applies to the implicit single member (see ## Time-of-day windows). Cannot be combined with an upstreams: list.
    # upstreams:                  # optional; alternative to `upstream:` — a pool of servers. Each entry carries upstream/token/weight/maxConcurrentRequests/maxConcurrentWaitSeconds (see ## Upstream pools). Mutually exclusive with `upstream:`.
    #   - upstream: <URL>
    #     token: <string>         # optional; per-member token (defaults to the provider token semantics: absent = pass client's Authorization through)
    #     weight: 1               # optional; default 1. Relative share of pinned sessions this member receives.
    #     window:                 # optional; per-member time-of-day eligibility window (see ## Time-of-day windows). Values are "HH:MM <location>", e.g. "18:00 Europe/Berlin". from/until required when the block is present. A member outside its window is ineligible for dispatch.
    #       from: "08:00 Europe/Berlin"
    #       until: "18:00 Europe/Berlin"
    #     days:                    # optional; per-member weekday eligibility allow-list (see ## Time-of-day windows). Comma-separated lowercase weekday names (monday..sunday) with an optional trailing IANA location, e.g. "saturday, sunday Europe/Berlin". A member outside its days is ineligible for dispatch.
    #     maxConcurrentRequests: 8   # optional; per-member cap. Absent or 0 or negative = unlimited.
    #     maxConcurrentWaitSeconds: 30 # optional; per-member queue wait before HTTP 429 (default 30)
```

## Routing

On every `/v1/*` request, the router:

1. Reads the JSON body, extracts the top-level `model` field
2. Walks the providers' `models:` lists in declaration order
3. First glob that matches → forwards to that provider's upstream
4. No match (or non-JSON body / no model field) → falls back to `default_provider`

Glob syntax is Go's `filepath.Match` — `*` matches any run of characters, `?` matches one, `[abc]` is a character class. Patterns with literal `[` need to use `*` (e.g. `deepseek-v4-flash*` not `deepseek-v4-flash[1m]`).

## Aliases

The optional top-level `aliases:` block maps short operator-typed model names to the full model identifier the upstream expects. Aliases are resolved router-side BEFORE provider routing — the upstream always sees the full model name.

```yaml
aliases:
  qwen: qwen3.6:35b-a3b-coding-nvfp4
  minimax: MiniMax-M3-highspeed
  deepseek: deepseek-v4-flash-2025-12-01
  opus: claude-opus-4-7
```

Then in any Claude Code session:

```
> /model qwen      # router sees "qwen", rewrites body .model to "qwen3.6:35b-a3b-coding-nvfp4", routes via qwen* glob to ollama-local
```

### Semantics

- **Single-hop.** If `aliases: {a: b, b: c}` and the request uses `model: a`, the upstream receives `model: b` — NOT `c`. The router resolves once and forwards.
- **Case-sensitive.** `aliases["Qwen"]` and `aliases["qwen"]` are distinct entries (same byte-exact match as provider glob keys).
- **Optional.** Configs without an `aliases:` block route exactly as before. Backward-compatible.
- **Log line.** On a hit, the router logs `[alias] qwen -> qwen3.6:35b-a3b-coding-nvfp4` at glog `V(2)` — visible in `/tmp/claude-code-router.log` when the router runs with `-v=2` or higher (raise the level with `curl http://127.0.0.1:8788/setloglevel/2`).

### Validation

| Condition | Behavior |
|---|---|
| Alias key equals a provider name (e.g. `aliases: { minimax: ... }` AND `providers: { minimax: ... }`) | **Error** at `config.Load` — daemon refuses to start. Operator must rename the alias key or the provider. |
| Alias target matches no provider's `models:` glob (e.g. `aliases: { foo: typo-name }` where no provider lists `typo-name*`) | **Warning** at startup via glog (`[config] alias target "typo-name" (from alias key "foo") matches no provider glob`); config still loads. At runtime, requests using that alias get rewritten to the typo string and fall through to `default_provider`, which likely returns 404. Operator notices the warning at startup. |

## Model pools

The optional top-level `model_pools:` block maps an invented model name (e.g. `coding`) to an ordered list of members, each naming a provider, a fixed concrete model, an optional weight, and an optional overflow flag. A client sends `model: <poolname>`; the router picks one member, rewrites the request body's `model` field to that member's fixed concrete model, and routes through that member's provider — the client never sees which member it got. The provider then applies its own upstream pool + session pinning + caps (see ## Upstream pools).

```yaml
model_pools:
  coding:
    - provider: deepseek-pool       # required; must exist under providers:
      model: deepseek-v4-flash      # required; the fixed concrete model this member serves
      weight: 2                     # optional; default 1. Share of pinned sessions this member receives.
      overflow: true                # optional; default false. Allow failover to a sibling when this member's provider is saturated.
    - provider: minimax-pool
      model: MiniMax-2.7
```

- **Per-member fields.** `provider` is required and must name a key under `providers:` — an unknown provider fails config load with an error naming the pool. `model` is required; the fixed concrete model string that provider sees — it may itself match that provider's `models:` globs, which is the normal case. `weight` is optional and defaults to 1; a negative weight is rejected at config load, and `weight: 0` and an absent key both resolve to the default 1. `weight` is the relative share of PINNED sessions this member receives — a 2:1 weight over two members sends roughly 2/3 of pinned sessions to the heavier member. `overflow` is optional and defaults to false (see failover below).
- **Session pinning.** A client that sets an `x-session-id` header (e.g. `ANTHROPIC_CUSTOM_HEADERS='{"x-session-id":"<id>"}'` in Claude Code) is pinned to the same pool member on every request via a weighted ring hash of the id — deterministic and stateless (FNV-1a over the id, no session→member map), so the member's prompt cache stays warm and the session consistently sees one provider's model. The same id over a two-member pool resolves to the same member across requests and restarts. The header is used only as the selection key, never for auth, and is stripped before forwarding (see ## Upstream pools).
- **Idless least-loaded.** A request without `x-session-id` is sent to the least-loaded member — the fewest in-flight requests at the member's provider — with round-robin tie-breaking among equally-loaded members, so an idless burst (e.g. dark-factory containers) spreads across the members instead of stacking on the first-declared one.
- **Overflow failover.** When the pinned member's provider is saturated (every capped upstream at its concurrency cap — see ## Upstream pools) and the member declares `overflow: true`, the request fails over to the least-loaded sibling member — availability over cache warmth, which costs nothing here because members are different providers/caches anyway. The `[route]` line names the member that actually served the request. With `overflow: false` (the default), the request stays on its pinned member and the provider's own concurrency semantics apply — it waits and answers HTTP 429 with the Anthropic-shaped `rate_limit_error` body (see ## Concurrency limit / ## Upstream pools).
- **Fall-through.** A model name that is not a configured pool name is untouched — it flows through the existing alias + provider-glob routing exactly as before. `model_pools:` names do not interact with `aliases:`; the two blocks are independent ("one name → one model" vs "one name → a choice of models").
- **Validation.** An unknown provider, a negative weight, a duplicate `(provider, model)` pair within a pool, and an empty member list all fail config load with an error naming the pool (the same pair in two different pools is not a duplicate). `weight: 0` and an absent weight both mean the default 1.
- **Observability.** Each pool resolution logs `[route] model=<poolname> -> provider=<provider> model=<concrete>` at glog `V(2)` — the same verbosity as the `[route] model=... matched ...` detail lines — the operator evidence of which member served a session. The always-on `[req]` line's provider value carries the serving upstream-pool member's zero-based index (`provider=<name>/<index>`); the `ccrouter_requests_total` / `ccrouter_tokens_total` metrics are unchanged, and the metrics' model label is the concrete member model the upstream saw.
- **SIGHUP applies changes.** A change to `model_pools:` (add/remove a member, change a weight or overflow flag) applies on SIGHUP without a restart — the reloader rebuilds the pool table (see ## Reload).
- **Security.** A pool name is ordinary client input like any model string — it never widens access; resolution only selects among configured members and their providers' existing auth. The rewritten body carries only the member's configured concrete model — a client cannot inject an arbitrary model string via a pool name.

A short worked example: two Claude Code sessions with distinct `x-session-id` values both send `model: coding` — the weighted ring hash pins each id to its own slot, so each session lands on its own member consistently (`deepseek-pool/deepseek-v4-flash` and `minimax-pool/MiniMax-2.7`), and each upstream log sees only its own model name in the body.

## Requires leading system

Claude Code puts a `system`-role entry inside the message list, after the first user turn, in addition to the dedicated top-level `system` block. Some models' chat templates reject any system entry that is not first and answer `HTTP 500 {"type":"error","error":{"type":"api_error","message":"system message must be at the beginning"}}` before inference starts.

Example:

```yaml
providers:
  ollama-local:
    upstream: http://localhost:11434
    token: ollama
    models:
      - "qwen*"
    requiresLeadingSystem:
      - "qwen3.8*"
```

### Semantics

- **Glob syntax.** Same `filepath.Match` syntax as `models:` — `*`, `?`, `[abc]`. Matched against the FULLY RESOLVED model name, i.e. after alias resolution and after the `[1m]` suffix is stripped.
- **Opt-in, default off.** Absent, `null`, and an empty list are equivalent and all mean: never transform anything for this provider. A config that does not mention the field routes byte-for-byte as before.
- **What the transform does.** Every `system`-role entry that is not at index 0 of `messages` is removed from the list and its content appended to the top-level `system` block, in the order the entries appeared. Content given as a plain string becomes one `{"type":"text","text":"..."}` block; content already given as a block list is appended block for block. The surviving messages keep their relative order.
- **What it does not do.** A `system` entry already at index 0 stays there and is NOT copied into the top-level block, even when a top-level block also exists — the upstream then receives system content in both places, which is the shape Claude Code already sends today and which upstreams accept. No merging, deduplication, summarisation, or reordering beyond moving entries.
- **Untransformed paths.** A request whose model does not match, or that has no misplaced system entry, is forwarded as the exact bytes received. Same for a body the transform cannot interpret (`messages` not a list, an entry that is not an object, a system `content` that is neither a string nor a block list): the request is forwarded unchanged with one `WARNING` line and the client gets the upstream's own status — the transform never turns a request into a router-generated error.
- **Scope is the matched route.** The patterns come from the provider whose `models:` glob matched. A request that matches no provider glob and falls through to `default_provider` is never transformed — if you need the transform for such a model, give that provider a `models:` glob that matches it.
- **Log line.** On a fire, the router logs `[system-lift] model=qwen3.8:27b-mlx moved=2` at glog `V(2)` — the same verbosity as the `[alias]` and `[1m-strip]` detail lines, so it is invisible at the always-on `V(1)` default. Raise the level with `curl http://127.0.0.1:8788/setloglevel/2` (auto-reverts after 5 minutes) before grepping `/tmp/claude-code-router.log` for it. The line carries the model name and a count only — never system-message content.

### Why per model and not per provider

ollama's system-position restriction is a property of each MODEL's chat template, not of the ollama server, so two models behind the same provider legitimately disagree. Verified 2026-08-15 with identical curl payloads against the same ollama instance: `qwen3.6:35b-a3b-coding-nvfp4` → 200, `qwen3.8:27b-mlx` → 500, `qwen3.8:27b-mtp-q4_K_M` → 500 — all three match the `ollama-local` provider's `qwen*` glob. A provider-wide boolean switch would therefore silently rewrite prompts for models that never needed it, which is why there is no such switch and no global toggle, environment variable, or CLI flag.

### Validation

| Condition | Behavior |
|---|---|
| Malformed glob pattern (e.g. `[`) | Config load fails with `provider "<name>": invalid requiresLeadingSystem glob "["`; router refuses to start |
| Well-formed pattern that matches no model the operator actually uses | Not an error; no warning. The symptom is that no `[system-lift]` line ever appears while the upstream keeps returning 500, and the fix is to widen the pattern. |

## Concurrency limit

Two optional per-provider fields cap how many `/v1/*` requests the router forwards to a provider's upstream at the same time:

```yaml
providers:
  <provider-key>:
    upstream: <URL>
    maxConcurrentRequests: 8        # optional; cap concurrent /v1/* requests to this provider. Absent or 0 or negative = unlimited.
    maxConcurrentWaitSeconds: 30    # optional; how long a queued request waits for a slot before HTTP 429 (default 30)
```

- **Feature-off by default.** `maxConcurrentRequests` absent, `0`, or negative means unlimited — no queueing, no router-issued 429, byte-for-byte today's behavior. Existing configs are unaffected.
- **What happens at the cap.** Excess requests queue in a per-provider semaphore. A queued request that frees a slot within `maxConcurrentWaitSeconds` is forwarded to the upstream normally and unchanged — the client cannot tell it queued. A request still waiting when the wait elapses is answered HTTP 429 with an Anthropic-shaped `rate_limit_error` JSON body (`{"type":"error","error":{"type":"rate_limit_error",...}}`), never a 5xx, so the client's own backoff retries cleanly.
- **The 30s default wait.** `maxConcurrentWaitSeconds` defaults to 30 when absent, `0`, or negative on a capped provider (a provider with `maxConcurrentRequests > 0`). It is only consulted on a capped provider.
- **The slot is held for the whole request.** The concurrency slot is held from dispatch until the upstream round-trip returns, including streaming SSE responses that run for minutes. A client that disconnects while still queued never acquires a slot, so no concurrency is lost to dead connections.
- **Per-provider caps are independent.** Two providers that share one upstream each apply their own cap. The seibert case: `seibert-vllm-default` and `seibert-dark-factory` each cap at their own N, so combined concurrency to the shared `vllm.seibert.tools` upstream can reach 2N — accepted by design, each provider's queue is its own.
- **Validation is lenient.** A negative `maxConcurrentRequests` is treated as unlimited and a negative `maxConcurrentWaitSeconds` as the 30s default — the config always loads, never fail-closed.
- **SIGHUP applies changes.** Both fields live in the same config that hot-reloads on SIGHUP — the reloader rebuilds the per-provider limiters, so a changed cap (or its removal) is live without a restart.
- **Observability is unchanged.** No new metrics. A router-issued 429 appears in the existing `[req] ... status=429` log line and the existing `4xx_rate_limited` class of `ccrouter_requests_total` — the same class as an upstream's own 429.
- **Suggested use.** `vllm.seibert.tools` enforces its own per-user ceiling of 8 concurrent requests. Set `maxConcurrentRequests: 8` on both seibert vllm providers (`seibert-vllm-default` and `seibert-dark-factory`) so the router queues instead of the upstream rejecting.

## 429 delay gate

An optional per-provider gate paces `/v1/*` requests into a provider whose upstream is under a sustained 429 wall, instead of re-storming an upstream already refusing work. Two fields turn it on:

```yaml
providers:
  <provider-key>:
    upstream: <URL>
    throttle429Threshold: 3     # optional; enable adaptive pacing: once the 60s-windowed count of upstream 429s reaches this, subsequent /v1/* requests to THIS provider are delayed before forwarding (see below). Absent or 0 or negative = disabled.
    throttleMaxDelaySeconds: 30  # optional; upper bound of the pacing delay while throttled (default 30)
```

- **Feature-off by default.** `throttle429Threshold` absent, `0`, or negative means disabled — no pacing, no added latency, byte-for-byte today's behavior. Existing configs are unaffected.
- **What the gate does.** Once the windowed count of upstream 429 responses within the fixed 60s observation window reaches `throttle429Threshold`, the provider enters throttle and every subsequent `/v1/*` request to it waits the current pacing delay before the router forwards it. The 429'd request itself is never retried — the response passes through unchanged; the status is observed only to adjust future pacing.
- **The AIMD dynamics**, all bounded and fixed (not configurable): on entry the delay is 1s; each observed 429 doubles it (×2), capped at `throttleMaxDelaySeconds`; each clean 60s window (no 429) halves it (÷2); when it decays below 1s the provider exits throttle and requests forward undelayed.
- **The bounded pacing queue.** While throttled, at most 32 requests wait their pacing turn; a request that cannot be paced within the max delay is answered HTTP 429 with the same Anthropic-shaped `rate_limit_error` JSON body as the concurrency limiter (`{"type":"error","error":{"type":"rate_limit_error",...}}`), never a 5xx and never a hang. A client that disconnects while waiting is never forwarded and holds no slot.
- **Per-provider independence.** Each provider's throttle state is its own — throttling one provider neither delays nor blocks another, even when two providers share one upstream.
- **Provider-level only.** Unlike `maxConcurrentRequests` / `maxConcurrentWaitSeconds`, the throttle knobs are read at provider level only and are NOT copied onto `upstreams:` pool members — a throttle field on a member is silently ignored (set both on the provider block).
- **Validation is lenient.** A negative `throttle429Threshold` is treated as disabled and a negative `throttleMaxDelaySeconds` as the 30s default — the config always loads, never fail-closed.
- **SIGHUP applies changes.** Both fields live in the same config that hot-reloads on SIGHUP — the reloader rebuilds the per-provider gates, so changed values are live without a restart. Throttle state is in-memory per provider, so a reload resets a throttled provider to not-throttled; it re-accumulates the window on the next 429s — reversible by design.
- **Observability.** Entry/exit log lines `[throttle] provider=<name> state=on` / `state=off` at INFO, each paced request at glog `-v` ≥ 4, and the model router's existing `[req] ... latency=` includes the pacing delay. A new additive counter `ccrouter_throttled_total{provider}` counts paced requests (see `docs/metrics.md`); the `status_class` enum and `4xx_rate_limited` classification are unchanged — an upstream 429 still lands in `4xx_rate_limited`.
- **Suggested use.** Observed live 2026-08-26, z.ai `glm-5.3-flash[1m]` (provider `zai/0`) entered a sustained 429 wall. Set `throttle429Threshold: 3` and `throttleMaxDelaySeconds: 30` on that provider so the router paces traffic into the breathing window instead of re-storming an upstream already refusing work. The zero=disabled regression check: on a benign provider, `throttle429Threshold: 0` keeps traffic flowing with no `[throttle]` lines and no added latency.

## Upstream pools

A provider with more than one server replaces the single `upstream:` field with an `upstreams:` list — a pool of servers the router spreads sessions and keyless traffic across. Each entry carries its own URL, optional token, weight, and per-server concurrency cap:

```yaml
providers:
  <provider-key>:
    upstreams:
      - upstream: http://vllm-1:8000
        token: "<key>"
        weight: 2
        maxConcurrentRequests: 8
        maxConcurrentWaitSeconds: 30
      - upstream: http://vllm-2:8000
        weight: 1
```

- **Schema recap.** `upstreams:` is the alternative to `upstream:` for a provider with more than one server; the two are mutually exclusive, and a provider setting both fails to load. The legacy single `upstream:` / `token:` / provider-level `maxConcurrentRequests` / `maxConcurrentWaitSeconds` form loads unchanged as a one-entry pool with weight 1 — the provider-level caps become the single entry's caps, so nothing an operator has today needs editing.
- **Per-entry fields.** `upstream` is required on every entry. `token` is per-member: absent means the client's `Authorization` header passes through, exactly like the provider-level token semantics. `weight` defaults to 1; a negative weight is rejected at config load, and `weight: 0` or an absent key resolves to 1. `maxConcurrentRequests` / `maxConcurrentWaitSeconds` are the per-member cap with the same spec-011 semantics as the provider-level fields (see ## Concurrency limit).
- **Session pinning.** A client that sets an `x-session-id` header (e.g. `ANTHROPIC_CUSTOM_HEADERS='{"x-session-id":"<id>"}'` in Claude Code) is pinned to the same pool member on every request via a weighted ring hash of the id — deterministic and stateless (FNV-1a over the id, no session→member map), so the session's upstream prompt cache stays warm on that one server, including across restarts. The header is stripped before forwarding, so upstreams never see it, and it is used only for pinning, never for auth. An absent header means a keyless request.
- **Keyless least-loaded.** A request without `x-session-id` is sent to the least-loaded member — the fewest in-flight requests by that member's concurrency semaphore — with round-robin tie-breaking among equally-loaded members, so keyless floods (e.g. dark-factory containers) spread across the pool instead of stacking on the first-declared server.
- **Per-member caps are independent.** Each member enforces its own `maxConcurrentRequests` — two members each allowing 8 do not share one global cap of 8. A request that queues past `maxConcurrentWaitSeconds` is answered HTTP 429 with the same Anthropic-shaped `rate_limit_error` body as the provider-level cap (see ## Concurrency limit).
- **Member down.** A member that fails answers with the existing sanitized 502 (unchanged behavior) — there is no probe-and-rotate. The operator removes or repairs the member and SIGHUPs, and the next `[route]` log line reflects the pool without it.
- **Client disconnect while queued.** No slot is held — a slot is acquired only at dispatch — so a client that disconnects while queued never consumes concurrency, and the 429 write to the dead connection fails harmlessly (spec-011 semantics).
- **Observability.** Each dispatch logs `[route] session=<id> upstream=<url>` at glog `V(2)` — the same verbosity as the `[alias]` and `[1m-strip]` detail lines — the operator evidence that a session is pinned and that keyless load is spreading. The always-on `[req]` line names the serving member by its zero-based pool index: `provider=<name>/<index>` (member 0 = the first declared upstream, member 1 = the second, …); the suffix is always present, so `provider=X/0` on a single-upstream provider is indistinguishable from a one-member pool by shape. `[route]`, `[inbound.end]`, and the metrics are unchanged.
- **SIGHUP applies changes.** A change to `upstreams:` (add/remove a member, change a weight or cap) applies on SIGHUP without a restart — the reloader rebuilds the pool tree from the edited config.
- **Suggested use.** Five DeepSeek vLLM instances under vllm's per-user ceiling of 8 concurrent requests: give each member `maxConcurrentRequests: 8` so the router queues instead of the upstream rejecting, and pin every Claude Code session to its own member so each session's prompt cache stays warm on one server:

```yaml
providers:
  seibert-vllm-default:
    upstreams:
      - upstream: http://vllm-1:8000
        weight: 1
        maxConcurrentRequests: 8
      - upstream: http://vllm-2:8000
        weight: 1
        maxConcurrentRequests: 8
    models:
      - "deepseek-v4-*"
```

With two Claude Code sessions (each set `ANTHROPIC_CUSTOM_HEADERS='{"x-session-id":"<unique id>"}'`), the `[route] session=<id> upstream=<url>` lines name a distinct member per session, and every request within one session names the same member — two sessions, two servers, warm caches on both.

## Time-of-day windows

A pool member can declare an optional `window:` block that restricts when it is eligible for a dispatch — so one provider can serve the same endpoint with different keys at different times of day (the normal-rate key during business hours, the unlimited key off-peak) with no operator action at the boundary:

```yaml
providers:
  <provider-key>:
    upstreams:
      - upstream: <URL>
        token: "<day-key>"
        window:
          from: "08:00 Europe/Berlin"
          until: "18:00 Europe/Berlin"
      - upstream: <URL>
        token: "<night-key>"
        window:
          from: "18:00 Europe/Berlin"
          until: "08:00 Europe/Berlin"
```

The legacy single `upstream:` provider form carries the same `window:` at provider level, applied to its implicit single member (see the schema recap below).

- **What it is.** A member's `window:` has two fields, `from` and `until`, each a time-of-day value in the `"HH:MM <location>"` form (e.g. `"18:00 Europe/Berlin"`). Each value carries its IANA location inline — the boundary is the location's wall clock, never the router host's local time, and there is no separate timezone field. The legacy single `upstream:` / `token:` provider form accepts the same `window:` at provider level; it applies to the implicit single member of the one-entry pool (see the note below).
- **Eligibility semantics.** A member is ELIGIBLE for a dispatch only while "now" (the router's injected clock — see `WithCurrentDateTime` in `pkg/factory/factory.go`) is inside `[from, until)` — the `from` boundary is inclusive, the `until` boundary exclusive. A member whose window does not contain "now" is INELIGIBLE: it is skipped by BOTH session pinning (the weighted ring hash only considers eligible members) and keyless least-loaded selection, and it is not a valid overflow target. A member with no `window:` is always eligible — today's behavior, byte-for-byte. Two members whose windows overlap at the same moment are BOTH eligible and selected by the normal pinning / least-loaded rules (see ## Upstream pools) — overlap does not prefer one member over another.
- **Overnight wrap.** `from` after `until` wraps overnight: `from: "22:00"` `until: "06:00"` covers 02:00 and excludes 14:00. `from` == `until` is an empty window (never eligible) — avoid it.
- **Provider fall-through.** When no member of a provider's pool is eligible, the provider itself is ineligible for that dispatch: the model falls through declaration order to the next matching provider that has an eligible member, then to `default_provider`. A closed window is ELIGIBILITY, never a failure — no router error, no HTTP 429, no health check, no probing. The complementary-window config below guarantees at least one eligible member per period, so the fall-through is the safety net, not the normal path.
- **Session re-resolution.** Pinning is stateless: a session pinned to a member whose window closes mid-session re-resolves to an eligible member on its next request (the cache on the old member is lost — unavoidable, its key is unusable), and a stream already dispatched completes even if the boundary passes mid-request.
- **Observability.** When the router falls through because the first matching provider's pool is fully closed, it logs `[route] provider=<p> window=closed -> <fallback>` at glog V(2) — the same verbosity as the `[route] session=<id> upstream=<url>` and `[route] model=... matched ...` detail lines — the operator evidence that the window boundary is behaving as configured. Each dispatch still logs the normal `[route]` line naming the serving member.
- **Validation.** Malformed times (e.g. `"25:00 Europe/Berlin"`) and unknown IANA locations (e.g. `"18:00 Mars/Olympus"`) are rejected at config load, as is a `window:` with only one boundary (a `window:` block present but missing `from` or `until` fails validation — the block is all-or-nothing). A provider-level `window:` combined with an `upstreams:` list is rejected — windows live on pool members. A valid-but-wrong location (e.g. `Europe/London` for `Europe/Berlin`) passes validation and shifts the boundary by its offset — the operator verifies the `[route] … window=closed` lines against the expected boundary (see Observability) and corrects the value via SIGHUP.
- **SIGHUP.** A change to a member's `window:` (or the member list) applies on SIGHUP without a restart — the reloader rebuilds the pool tree from the edited config (see ## Reload).
- **Security.** The window is server-side config + server clock, evaluated per request; a client cannot influence which window applies, and the window never widens access or bypasses `allowedApiKeys`. The complementary-window config below guarantees the off-peak unlimited key is only ever used inside its window.

A member can go one step further and restrict eligibility to a WEEKDAY subset rather than (or alongside) a time window.

- **What it is.** A member can declare an optional `days:` weekday allow-list as a sibling of `window:` — a comma-separated list of lowercase English weekday names (`monday`..`sunday`) with an optional trailing inline IANA location, e.g. `"saturday, sunday Europe/Berlin"`. Absent = every day — byte-for-byte today's behavior. The legacy single `upstream:` provider form carries the same `days:` at provider level, applied to its implicit single member, exactly like the provider-level `window:`.
- **Eligibility AND.** A member is ELIGIBLE for a dispatch only while BOTH conditions hold: `(window absent OR window contains "now") AND (days absent OR today's weekday is in days)`. The weekday is resolved in the member's attached IANA location, in precedence order: the inline location on the `days:` value, else the `window:` `from`/`until` location, else UTC — so the boundary is the location's calendar, never the router host's local day. A member with `days:` but no `window:` is eligible ALL DAY on those weekdays — this is how the weekend use case below expresses "all day on Saturday and Sunday" (the `window:` block has no all-day value).
- **Location + fail-closed rule.** A member with `days:` and no `window:` MUST carry the inline location on its `days:` value — config load rejects a days-only member whose `days:` has no location, so it can never silently resolve its weekday in UTC and drift from its sibling members' calendar. A member with `days:` AND a `window:` may omit the inline location — the window's `from`/`until` location governs both boundaries.
- **Validation.** Unknown weekday names (e.g. `funday`) and an empty value (`days: ""`) are rejected at config load, as is a provider-level `days:` combined with an `upstreams:` list. The 7 canonical names are `monday` `tuesday` `wednesday` `thursday` `friday` `saturday` `sunday` (lowercase; no abbreviations, no `monday..friday` ranges, no numeric indices). A valid-but-wrong location (e.g. `Europe/London` for `Europe/Berlin`) passes validation and shifts both the weekday and time boundaries by its offset — verify the `[route]` lines against the expected boundary and correct via SIGHUP.
- **Ineligibility semantics (unchanged from `window:`).** A member outside its `days:` is excluded from BOTH session pinning (the weighted ring hash only considers eligible members) and keyless least-loaded selection; when no member of a provider's pool is eligible the provider falls through declaration order to the next matching provider or `default_provider`, logged as `[route] provider=<p> window=closed -> <fallback>` at V(2) — eligibility, never an error, never a 429 (the fall-through line keeps the `window=closed` wording).
- **SIGHUP.** A change to a member's `days:` (or the member list) applies on SIGHUP without a restart — the reloader rebuilds the pool tree from the edited config (see ## Reload).
- **Security.** `days:` is server-side config + the router's injected clock, evaluated per request; a client cannot influence which member applies, and it never widens access or bypasses `allowedApiKeys`.

The operator pattern this feature exists for: one provider serving the same endpoint with two keys, each restricted to its own period:

```yaml
providers:
  seibert-vllm-default:
    upstreams:
      - upstream: http://vllm:8000
        token: "<normal-rate-key>"
        maxConcurrentRequests: 16
        window:
          from: "08:00 Europe/Berlin"
          until: "18:00 Europe/Berlin"
      - upstream: http://vllm:8000
        token: "<unlimited-off-peak-key>"
        maxConcurrentRequests: 50
        window:
          from: "18:00 Europe/Berlin"
          until: "08:00 Europe/Berlin"
    models:
      - "deepseek-v4-*"
```

With complementary windows, business hours (08:00–18:00 Europe/Berlin) are served by the day member/key at its 16-request cap, and off-peak by the night member/key at its 50-request cap — exactly one eligible member per period, so the unlimited key is never touched during business hours and no operator action is needed at the boundary. The `[route]` lines name the day member during business hours and the night member off-peak.

Adding `days:` extends the same one-member-per-period pattern across the weekday boundary: the unlimited key serves all day on weekends, while the day/night keys own Monday–Friday. One provider, three members — the weekday-day member (normal-rate key, cap 16, business-hours window), the weekday-night member (unlimited key, cap 50, off-peak window), and the weekend member (`days: "saturday, sunday Europe/Berlin"`, the SAME unlimited off-peak key, cap 50, NO `window:` — so it is eligible ALL DAY on Saturday and Sunday):

```yaml
providers:
  seibert-vllm-default:
    upstreams:
      - upstream: http://vllm:8000
        token: "<normal-rate-key>"
        maxConcurrentRequests: 16
        days: "monday, tuesday, wednesday, thursday, friday"
        window:
          from: "08:00 Europe/Berlin"
          until: "18:00 Europe/Berlin"
      - upstream: http://vllm:8000
        token: "<unlimited-off-peak-key>"
        maxConcurrentRequests: 50
        days: "monday, tuesday, wednesday, thursday, friday"
        window:
          from: "18:00 Europe/Berlin"
          until: "08:00 Europe/Berlin"
      - upstream: http://vllm:8000
        token: "<unlimited-off-peak-key>"
        maxConcurrentRequests: 50
        days: "saturday, sunday Europe/Berlin"
    models:
      - "deepseek-v4-*"
```

Exactly one eligible member exists per (day, time): the day member during Mon–Fri business hours (08:00–18:00 Europe/Berlin, its 16-request cap), the night member Mon–Fri off-peak (18:00–08:00, its 50-request cap), and the weekend member all day Saturday and Sunday (50-request cap, no window to close). The unlimited key therefore serves all day on weekends and is never touched Monday–Friday, with no operator action at any day/time boundary. Note the full weekday list is written out (`monday, tuesday, ...`) because the format has no `monday..friday` range sugar — the `[route]` lines name the weekend member all day Sat+Sun, the day member during Mon–Fri business hours, and the night member Mon–Fri off-peak.

The per-entry `days:` sits alongside the per-entry `window:` / `weight` / `maxConcurrentRequests` / `maxConcurrentWaitSeconds` fields documented in ## Upstream pools, and the provider-level `days:` on the legacy single-`upstream:` form behaves exactly like the provider-level `window:` (copied onto the implicit single member).

## Auth

A provider — and every `Upstream` pool member (see ## Upstream pools) — resolves its outbound `Authorization` at wiring time in this frozen three-way order:

1. **Its own `token:` wins when set.** A pool member's per-entry `token:`, or a legacy provider-level `token:` (which `normalizeUpstreams` copies onto the implicit single member), replaces the outbound `Authorization` with `Bearer <token>`.
2. **Else the top-level `default_token:`.** A provider/member with no `token:` of its own inherits the shared global key — the outbound `Authorization` becomes `Bearer <default_token>`.
3. **Else passthrough.** With neither set, the client's `Authorization` header is forwarded verbatim — Claude Code's subscription OAuth bearer passes through untouched and the router never holds it.

There is NO per-provider opt-out that forces passthrough while a global default is set — a provider needing a different key (a separate vLLM quota, the off-peak window keys from ## Time-of-day windows) declares its own `token:` and overrides.

| `token:` field | Behavior |
|---|---|
| set | Replace the outbound `Authorization` with `Bearer <token>` — overrides `default_token:`; used for fixed-token providers (MiniMax, Ollama, vLLM) |
| absent/empty + `default_token:` set | Replace the outbound `Authorization` with `Bearer <default_token>` — one shared key defined once, no per-provider copies |
| absent/empty + no `default_token:` | Forward the client's `Authorization` header verbatim — used for Anthropic subscription (Claude Code's OAuth bearer passes through untouched) |

- **Top-level placement.** `default_token:` is a top-level config key at the same level as `providers:` / `router:`; absent or empty means no global default — today's behavior. The resolution applies uniformly at the member level: a pool member's per-entry `token:` (see ## Upstream pools) and the legacy provider-level `token:` (copied onto the implicit single member) both override the global default.
- **SIGHUP.** A change to `default_token:` applies on SIGHUP without a restart — the reloader rebuilds the router tree from the edited config (see ## Reload), and the next request's `[upstream.headers]` `len=N` reflects the new key.
- **Security / redaction.** The global default is operator config read only at wiring — never from client input, so a client cannot influence which token the router sends. Like every token it flows only in the outbound `Authorization` header, is never echoed to a client or exposed via `/metrics` or admin endpoints, and never reaches logs or trace files: the V(3) `[upstream.headers]` line shows it as `<redacted len=N>` — the `len` distinguishes the inheriting key from an overriding key without printing either, the operator's live smoke evidence — and trace files redact `Authorization` (see ## Trace). The router never stores or logs token values, `default_token:` included; trace files inherit the same invariant — see ## Trace.
- **Worked note.** With `default_token:` set, every no-token provider inherits it — the passthrough case exists on configs WITHOUT a global default (backward-compatible), which is the subscription-OAuth flow.

## Routing by API key

Two optional config fields define key-based authentication and routing: a top-level `allowedApiKeys` registry and an optional per-provider `allowedApiKeys` list.

```yaml
allowedApiKeys:                       # optional; registry of keys that authenticate non-loopback /v1/* requests
  - "<KEY>"
  - "<KEY2>"

providers:
  <provider-key>:
    allowedApiKeys:                   # optional; this provider's routing pin
      - "<KEY>"
```

- **Feature-off by default.** Absent, `null`, and empty lists are equivalent and all mean: no key enforcement and no key routing — the `/v1/*` path behaves byte-for-byte exactly as it does today. Existing single-user localhost configs are unaffected.
- **Valid inbound key set.** The set of keys that authenticate a non-loopback caller is the top-level registry when non-empty, else the union of all providers' lists. Adding or removing a key there is the rotation operation.
- **Duplicate claims fail load.** Two providers declaring the same key in their `allowedApiKeys` is ambiguous and rejected at config load with an error naming the key and both providers — never a silent first-wins at runtime. A key in both the top-level registry and a provider's list is not a duplicate.
- **The routing rule.** A request whose presented `x-api-key` value is in a provider's list is dispatched to that provider (its outbound token), overriding model-glob selection. A key that is valid but claimed by no provider routes exactly like a keyless request — glob match, then `default_provider`. A keyless request is unchanged.
- **The non-loopback auth gate.** With a non-empty registry, every non-loopback `/v1/*` request must present a registry key in the `x-api-key` header or it is refused with `401 Unauthorized` (constant-time comparison via `crypto/subtle`; neither the presented nor the configured key is ever logged). Loopback requests are exempt — the operator's own Claude Code on the host keeps working keyless — but their `x-api-key` is still stripped outbound. Loopback classification is the **source** address (`ip.IsLoopback()`): on macOS Docker Desktop, container traffic via `host.docker.internal` arrives at the host loopback, so local YOLO containers are exempt even though the destination IP is non-loopback (verified 2026-08-24).
- **Outbound hygiene.** `x-api-key` is stripped from a cloned request before it is forwarded, in every letter case, so a caller's key never reaches an upstream. `Authorization` behavior is unchanged: a provider with a `token:` sends `Bearer <token>`, a provider without one passes the client's header through.
- **SIGHUP applies changes.** Both `allowedApiKeys` fields live in the same config that hot-reloads on SIGHUP — edit the file, `kill -HUP $(pgrep claude-code-router)`, and new keys (or their removal) are live without a restart.
- **Caller side.** How a caller presents a key depends on whether the request arrives loopback:
  - **Truly remote caller** (another host, a cluster pod, LAN): must set `ANTHROPIC_API_KEY` — Claude Code sends it as `x-api-key`, which the router validates and routes by. This is the `allowedApiKeys` gate in action.
  - **Local Docker container** (macOS Docker Desktop, `host.docker.internal`): arrives at the host loopback, so the gate is **exempt** — the container carries `ANTHROPIC_AUTH_TOKEN` (the registry key) as `Authorization: Bearer`, which dark-factory's `validateClaudeAuth` requires and the router passes on to its outbound token swap. See [dark-factory-integration.md](dark-factory-integration.md) step 4.
  - Failure-modes caveat: on a machine that also runs a Claude subscription, `ANTHROPIC_API_KEY` overrides the subscription OAuth in `-p` mode — the operator's own host stays keyless loopback.
- **Supersession.** This replaces the spec-009 `x-router-key` / `auth.key` / `ROUTER_AUTH_KEY` mechanism, which is removed. A config still carrying an `auth:` block fails to load, and the binary refuses to start with `ROUTER_AUTH_KEY` set.
- **Sensitive.** Keys are literal strings, like the provider `token:` fields — no format checks, no length limits. Keep the config at `chmod 600`; the router never logs a key. Never commit a literal key.

### Migrating from 009

The retired `ROUTER_AUTH_KEY` environment variable must be removed from the launchd wrapper `~/.local/bin/claude-code-router.sh`, which currently injects it from TeamVault. No env var replaces it: the registry values live in the `chmod 600` config file like the provider tokens, read via `allowedApiKeys` rather than injected at startup. An operator who forgets to remove it gets the fail-closed startup error — the binary refuses to start while `ROUTER_AUTH_KEY` is still set. Completing the migration: remove the `ROUTER_AUTH_KEY` injection from the wrapper, configure `allowedApiKeys` in the config, then SIGHUP (or restart) the router.

## Trace

The `trace:` flag is a top-level boolean. When `true`, every `/v1/*` request produces exactly one JSON file at `~/.claude-code-router/trace/<timestamp>-<request-id>.json` containing the complete request (method, path, headers, body) and complete response (status, headers, body).

When `false` (or absent), no trace files are written and no trace middleware is on the request hot path.

The `Authorization` and `x-api-key` request headers are redacted to `***` in every trace file, regardless of header case. All other headers and the entire request/response bodies are logged verbatim — operator's data, operator's disk.

The flag is read once at config load; changing it requires a router restart (see ## Reload).

No retention, rotation, or cleanup is provided — the operator runs `rm` manually.

### Runtime toggle via `/enabletrace` and `/disabletrace`

Two operator-local HTTP endpoints toggle trace logging at runtime without a router restart:

- `POST /enabletrace` — turns tracing on for a bounded 5-minute window that auto-disables on expiry. Repeated calls reset the window (each cancels the prior timer and starts a fresh 5-minute window).
- `POST /disabletrace` — turns tracing off immediately and cancels the pending TTL timer.

Both endpoints are registered on the operator-local listener (`127.0.0.1:8788`) and, like `/setloglevel` and `/gc`, are guarded by an unconditional loopback-only check: a non-loopback request is refused with `403 Forbidden` before handler logic runs. The guard is always on (no config knob to disable it) and is the protection once the listener binds beyond `127.0.0.1` — a remote caller can never toggle tracing (body capture), force GC, or change log levels. Example:

```bash
curl -X POST http://127.0.0.1:8788/enabletrace   # trace enabled
curl -X POST http://127.0.0.1:8788/disabletrace  # trace disabled
```

The 5-minute window auto-disables on expiry — if the operator forgets `/disabletrace`, tracing stops on its own. The 5-minute TTL is a hardcoded constant (not configurable via config file, query param, or request body).

The toggle is process-internal state only — it does not persist across router restarts, and it does not depend on config reload or SIGHUP.

### Deprecation of `trace:` config flag

The legacy `trace: true` config flag still works as an always-on opt-in (trace middleware is mounted when `IsEnabled() || cfg.Trace` is true — flag-OR-config). However, leaving `trace: true` on indefinitely is a disk and privacy hazard (full request/response bodies written to disk for every request). Use the bounded `/enabletrace` toggle instead for capturing a single problematic exchange.

## Example — all four providers

```yaml
router:
  default_provider: anthropic-subscription

# allowedApiKeys:            # optional; uncomment + list keys to enable non-loopback auth and key routing

providers:

  anthropic-subscription:
    upstream: https://api.anthropic.com
    # no token: → forward client's Authorization (subscription OAuth)
    models:
      - "claude-opus-*"
      - "claude-sonnet-*"
      - "claude-haiku-*"
      - "opus"
      - "sonnet"
      - "haiku"

  minimax:
    upstream: https://api.minimax.io/anthropic
    token: "<your MiniMax API key>"
    models:
      - "MiniMax-*"

  ollama-local:
    upstream: http://localhost:11434
    token: "ollama"                   # Ollama's literal-string convention
    models:
      - "qwen*"
    requiresLeadingSystem:            # qwen3.8's chat template rejects a non-leading system message
      - "qwen3.8*"

aliases:
  qwen: qwen3.6:35b-a3b-coding-nvfp4
  minimax: MiniMax-M3-highspeed
  deepseek: deepseek-v4-flash-2025-12-01
  opus: claude-opus-4-7
```

`chmod 600 ~/.claude-code-router/config.yaml` since it holds API tokens.

## Switching mid-session

The router decides per-request. To switch backends inside a Claude Code session, just use `/model <name>`:

```
> /model qwen                   # alias → next request rewritten to qwen3.6:35b-a3b-coding-nvfp4, routed to ollama-local
> /model minimax                # alias → next request rewritten to MiniMax-M3-highspeed, routed to minimax
> /model claude-opus-4-7        # no alias match, glob routes to anthropic-subscription
```

No router restart, no Claude Code restart. The session stays alive across switches.

## Reload

Edit the config file and send SIGHUP to the running router to pick up the change without restarting the process or dropping in-flight requests:

```bash
kill -HUP $(pgrep claude-code-router)
```

On success the router logs one line at `config reloaded old_providers=N new_providers=M` and serves new requests from the updated config. Requests already in flight finish against the config they started under. An invalid config (missing file, invalid YAML, validation failure) is rejected: the old config stays active and the router logs `config reload failed: ...` at WARNING.

A full process restart is still needed to change the `--listen` address or TLS material — those are not hot-reloadable.

`launchctl kickstart -k` still works for a hard restart after a binary upgrade, and `systemctl --user restart` re-reads the unit file on Linux; neither is required for config edits. On macOS, a `--listen` change is NOT picked up by `kickstart -k` (it restarts with the cached args) — edit the plist, then `launchctl bootout gui/$(id -u)/de.bborbe.claude-code-router` followed by `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/de.bborbe.claude-code-router.plist`.

## Related

- [README.md](../README.md) — install, `clauder` shell function
- [docs/launchd-service.md](launchd-service.md) — macOS service setup
- [docs/systemd-user-service.md](systemd-user-service.md) — Linux service setup

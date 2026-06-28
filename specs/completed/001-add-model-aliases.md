---
status: completed
approved: "2026-06-28T09:33:43Z"
generating: "2026-06-28T09:41:36Z"
prompted: "2026-06-28T09:41:36Z"
verifying: "2026-06-28T09:54:07Z"
completed: "2026-06-28T10:20:59Z"
branch: dark-factory/add-model-aliases
---

## Summary

- Operators currently type full model strings to switch backends mid-session — `/model qwen3.6:35b-a3b-coding-nvfp4` is 30+ characters of error-prone typing.
- Add a declarative `aliases:` block to `~/.claude-code-router/config.yaml` so short names (`qwen`, `minimax`, `deepseek`, `opus`) resolve to the full model name at router-side before provider routing.
- Resolution is single-hop (no alias-of-alias chaining); the upstream always sees the resolved full model name so Ollama / vLLM / MiniMax load the right model.
- The router logs `[alias] qwen -> qwen3.6:35b-a3b-coding-nvfp4` so the operator can see resolution in `/tmp/claude-code-router.log` and debug typos.
- Validation fails loudly on alias-key collision with a provider name; warns when an alias target matches no provider glob.

## Problem

`/model qwen3.6:35b-a3b-coding-nvfp4` is the actual model identifier Claude Code accepts and sends, but typing it correctly every time is friction the operator pays per session. Mid-session backend switching is the headline feature of [Multi-Provider Claude Code Proxy](../23%20Goals/Multi-Provider%20Claude%20Code%20Proxy.md) (shipped 2026-06-28 at v0.4.0), and short ergonomic aliases turn that feature from "works in theory" into "actually used in practice."

## Goal

The operator can type `/model qwen` (or `/model minimax`, `/model deepseek`, `/model opus`) in any `claude-obsidian-*.sh` session and the router transparently rewrites the request body's `.model` field to the full string before routing to a provider. The mapping is declarative (`aliases:` block in the YAML config); adding a new alias requires only a config edit + router restart, no code change.

## Non-goals

- **Alias-of-alias chaining** (`qwen → qwen-fast → qwen3.6:...`) — v1 single-hop only. Extend later if nested resolution proves useful.
- **Per-user / per-session aliases** — aliases are operator-level config only.
- **UI or dashboard for managing aliases** — plain YAML edit is sufficient.
- **Automatic alias discovery from provider model lists** — static config only.
- **Suggesting aliases on misspell** ("Did you mean qwen?") — out of scope; the operator sees the model name in the log and corrects.

## Acceptance Criteria

- [ ] **Config schema accepts an `aliases:` block.** Evidence: a config file containing
      ```yaml
      aliases:
        qwen: qwen3.6:35b-a3b-coding-nvfp4
      providers: { ... }
      router: { default_provider: anthropic-subscription }
      ```
      loads via `config.Load(path)` and returns `cfg.Aliases["qwen"] == "qwen3.6:35b-a3b-coding-nvfp4"` (asserted in a Ginkgo spec under `pkg/config/config_test.go`).

- [ ] **Validation errors on alias-key colliding with a provider name.** Evidence: a config with `aliases: { minimax: ... }` AND `providers: { minimax: { ... } }` makes `config.Load` return an error whose `.Error()` contains the substring `alias key "minimax" collides with provider name` (asserted in `pkg/config/config_test.go`).

- [ ] **Validation warns when an alias target matches no provider glob.** Evidence: a config with `aliases: { foo: bar-1 }` where no provider's `models:` glob matches `bar-1` triggers a glog warning at WARNING level containing `alias target "bar-1"` and the substring `matches no provider`. The config still loads successfully (warning, not error). Asserted in `pkg/config/config_test.go` via captured glog output.

- [ ] **ModelRouter rewrites the request body's `.model` field when an alias matches.** Evidence: an `httptest`-driven Ginkgo spec under `pkg/handler/model-router_test.go` sends `POST /v1/messages` with body `{"model":"qwen"}` through a `NewModelRouter` configured with `aliases={"qwen": "qwen3.6:35b-a3b-coding-nvfp4"}` and an upstream that captures the forwarded request. The captured request body's `.model` field equals `qwen3.6:35b-a3b-coding-nvfp4`. The router log contains `[alias] qwen -> qwen3.6:35b-a3b-coding-nvfp4` at glog `V(1)`.

- [ ] **Alias miss passes through unchanged.** Evidence: sending `{"model":"claude-opus-4-7"}` with the same `aliases` config (no key for `claude-opus-4-7`) results in the captured upstream request body's `.model` field still equal to `claude-opus-4-7`. No `[alias]` log line is emitted. Asserted in `pkg/handler/model-router_test.go`.

- [ ] **Post-Install:** Live smoke test on the operator's macOS launchd-managed router after merge. Evidence: with `aliases: { qwen: qwen3.6:35b-a3b-coding-nvfp4 }` in `~/.claude-code-router/config.yaml`, after `launchctl kickstart -k gui/$(id -u)/de.bborbe.claude-code-router`, running `/model qwen` inside any `claude-obsidian-*.sh` session and sending one message → `/tmp/claude-code-router.log` shows `[alias] qwen -> qwen3.6:35b-a3b-coding-nvfp4` AND `[route] model="qwen3.6:35b-a3b-coding-nvfp4" matched "qwen*"` AND `[req] POST /v1/messages -> 200`.
  - `deploy_check:` `strings $(command -v claude-code-router) 2>/dev/null | grep -cF '[alias] %s -> %s' | head -1`
  - `deploy_target:` `1` (the new log-format literal `[alias] %s -> %s` from `pkg/handler/model-router.go` is compiled into the running binary — pre-fix binary returns `0`)

## Verification

```bash
# Inside the dark-factory container (container working tree)
make precommit
```

Plus the Ginkgo specs added under `pkg/config/config_test.go` and `pkg/handler/model-router_test.go` are exercised by the `make test` step inside `precommit`.

After merge (operator-side smoke):

```bash
# 1. Add aliases block to live config
$EDITOR ~/.claude-code-router/config.yaml

# 2. Reload the launchd-managed router
launchctl kickstart -k gui/$(id -u)/de.bborbe.claude-code-router

# 3. In any clauder session
> /model qwen
> what model are u?

# 4. Verify router log
tail -10 /tmp/claude-code-router.log
# Expect: [alias] qwen -> qwen3.6:35b-a3b-coding-nvfp4
# Expect: [route] model="qwen3.6:35b-a3b-coding-nvfp4" matched "qwen*"
# Expect: [req] POST /v1/messages -> 200
```

## Desired Behavior

1. `pkg/config/Config` struct gains an exported field `Aliases map[string]string` with YAML tag `yaml:"aliases,omitempty"`.
2. `Config.Validate()` returns a non-nil error when an alias key equals any provider key (case-sensitive match on the YAML keys). Error message names both the colliding key and the conflict ("alias key X collides with provider name X").
3. `Config.Validate()` emits a `glog.Warningf` (not an error) when an alias target string matches no provider's `models:` glob via `path.Match` across all providers. Validation still returns nil — operator sees the warning at startup but the config loads.
4. `pkg/handler/NewModelRouter` accepts an additional `aliases map[string]string` parameter. Before glob-matching for provider dispatch, it looks up the body's `.model` field in the aliases map; on hit, it rewrites the body's `.model` to the resolved value via `json.Unmarshal` → set field → `json.Marshal`, preserving all other body fields verbatim. It emits `glog.V(1).Infof("[alias] %s -> %s", short, resolved)`.
5. `pkg/factory.CreateRouterFromConfig` passes `cfg.Aliases` into `NewModelRouter` (single new wiring line).

## Constraints

- **Backward compatibility.** Configs without an `aliases:` block (i.e. all configs in operator use today) MUST continue to load and route exactly as before. `cfg.Aliases == nil` is the normal case; ModelRouter treats nil and empty map identically (no-op alias lookup).
- **Single-hop only.** If an alias target itself appears as an alias key, do NOT recurse. The router resolves once and forwards the result; if the operator wants `qwen → qwen-fast → full-name`, they configure `qwen: full-name` directly.
- **Body preservation.** The router's existing contract (downstream proxy receives the body intact and re-readable) MUST hold across the alias rewrite. Only the top-level `.model` field changes; all other fields, key order in non-JSON contexts, and byte fidelity outside the JSON object are out of scope (this is a JSON-only path).
- **`ContentLength`.** The rewritten body's length may differ from the original (resolved name is longer than the alias key). `r.ContentLength` MUST be updated to match the new body length.
- **Glob comparison case-sensitivity.** Alias key lookup is exact-string (case-sensitive), matching the existing `path.Match` glob semantics in provider routing.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Alias key collides with a provider name | `config.Load` returns error at router startup; daemon `KeepAlive=true` causes restart loop; `/tmp/claude-code-router.log` shows the validation error on each retry | Operator edits config to remove the colliding key or rename one of them; `launchctl kickstart -k` reloads |
| Alias target is a typo (matches no provider glob) | Warning logged at startup; router starts; runtime requests using that alias get rewritten to the typo'd string, fall through to `default_provider` (likely anthropic-subscription), upstream returns 404 / model-unknown | Operator sees the startup warning; edits config to fix the typo; `launchctl kickstart -k` reloads |
| Body is not valid JSON (e.g. raw text) | Existing `extractModel` returns empty string; alias lookup misses; body forwarded unchanged to default provider (same as today) | None — behavior matches today's no-aliases path |
| Body JSON-rewrite fails mid-flight (corrupt body, write error during re-marshal) | Router returns `500 Internal Server Error` to Claude Code; server log captures the error string | Operator inspects log; fixes the alias config if the resolved value contains invalid JSON characters (unlikely with sane model names) |

## Suggested Decomposition

Prompts should be generated in this order — each row is a single prompt with a clear scope.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | `pkg/config` schema + Validate (collision error + warn-on-orphan) | 1, 2, 3 | 1, 2, 3 | — |
| 2 | `pkg/handler/model-router.go` alias-rewrite path + Ginkgo specs (hit, miss, body preservation) | 4 | 4, 5 | prompt 1 (consumes `cfg.Aliases` type) |
| 3 | `pkg/factory/CreateRouterFromConfig` wiring + `docs/config.md` + `docs/config.example.yaml` + CHANGELOG `## Unreleased` bullet | 5 | (none new — exercises 1–5 together) | prompts 1, 2 |

Rationale: prompt 1 establishes the config shape consumed by everything else; prompt 2 is the standalone handler change that doesn't need factory wiring to test; prompt 3 connects them + ships the operator-facing documentation. The Post-Install AC is operator-side after merge, not a separate prompt.

## Do-Nothing Option

Operators continue typing `/model qwen3.6:35b-a3b-coding-nvfp4` (30+ chars) every time they want Qwen. The Multi-Provider goal's headline benefit (per-turn cost+capability matching) gets less use because the typing friction discourages mid-session switching. The full-model-name strings are easy to typo, leading to silent fall-throughs to `default_provider` (anthropic-subscription) — operator notices the wrong model is responding, has to debug. Total ergonomic tax: real but bounded; the router still works correctly without aliases.

## Related

- Parent goal: [Multi-Provider Claude Code Proxy](https://github.com/bborbe/claude-code-router) — v0.4.0 shipped 2026-06-28; this spec is a follow-up enhancement
- Vault task: `[[Add Model Aliases to Claude Code Router]]`
- Existing routing: `pkg/handler/model-router.go` (`NewModelRouter`)
- Existing config: `pkg/config/config.go` (`Config`, `Validate`)
- Existing factory: `pkg/factory/factory.go` (`CreateRouterFromConfig`)

---
status: approved
approved: "2026-08-15T10:25:11Z"
branch: dark-factory/requires-leading-system-message
---

## Summary

- Claude Code puts a `system`-role message *inside* the conversation list, after the user turn — on top of the dedicated top-level system block. Ollama's qwen3.8 chat template refuses that shape and answers with an HTTP 500 error before it even starts thinking.
- The router hands request bodies to the upstream almost verbatim, so today **every** Claude Code request aimed at qwen3.8 fails. The operator's `cc-personal-qwen` launcher, pinned to `qwen3.8:27b-mlx`, is dead.
- The restriction is per **model**, not per provider: qwen3.6 on the very same Ollama instance accepts the identical payload and answers normally. A provider-wide switch would silently rewrite prompts for models that never needed it.
- Add an opt-in, per-provider list of model name patterns. When the routed model matches, the router moves every out-of-place system message to the top-level system block, keeping their order; everything else is forwarded untouched, byte for byte.
- Configs that do not mention the new setting behave exactly as they do today.

## Problem

Claude Code sends conversations to the router in which a `system`-role entry appears in the middle of the message list, after the first user turn, in addition to the dedicated top-level system field. Ollama's qwen3.8 chat template rejects any system entry that is not the very first one and answers `HTTP 500 {"type":"error","error":{"type":"api_error","message":"system message must be at the beginning"}}` in roughly 90ms, before inference starts. Because the router forwards bodies unchanged apart from the model name, that rejection reaches the operator on every single request: qwen3.8 is currently unusable through the router, and the operator has been keeping a hand-rolled standalone Python shim alive to work around it. The same payload succeeds against qwen3.6 on the same Ollama server, so the router cannot treat this as a property of the provider — it is a property of specific models behind that provider.

## Goal

An operator can name, per provider, which models reject non-leading system messages. Requests routed to a model matching one of those names arrive upstream in a shape those models accept — all system content in the top-level system block, in original order, nothing lost — and qwen3.8 answers Claude Code normally. Requests to every other model, and every request under a config that does not use the new setting, reach the upstream exactly as they do today.

## Non-goals

- No provider-level boolean "fix system messages" switch — verified wrong: qwen3.6 and qwen3.8 share a provider and disagree. Do NOT add one; matching is per-model-name pattern only. If a future upstream needs provider-wide behaviour, that is a separate spec.
- No global / router-wide toggle, no environment variable, no CLI flag for this behaviour, and no per-request opt-out header — the pattern list is the only control surface.
- No auto-detection or upstream probing ("try, and on 500 retry transformed") — declarative config only.
- No complexity-based or content-based routing.
- No fix to Ollama's qwen3.8 chat template — that belongs in an upstream issue against Ollama.
- No change to how any other provider's payloads are handled.
- No removal of the operator's standalone `sysfix-proxy.py` shim — tracked in the vault task, not here.
- No merging, deduplication, summarisation or reordering of system content beyond moving entries and preserving their relative order.

## Acceptance Criteria

- [ ] **Config accepts the per-provider pattern list.** A YAML config containing
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
      loads successfully and the parsed provider exposes exactly `["qwen3.8*"]` for that field — evidence: Ginkgo assertion in `pkg/config_test.go`; `make precommit` exits 0.

- [ ] **Invalid pattern is rejected at load time.** A config whose `requiresLeadingSystem` contains the malformed pattern `[` makes config loading return a non-nil error whose message contains the substrings `requiresLeadingSystem` and `[` and the provider name — evidence: Ginkgo assertion on the returned error string in `pkg/config_test.go`.

- [ ] **Matching model: non-leading system entries are lifted, order preserved.** A request body whose message list is `[user, system("A"), assistant, system("B")]` with a top-level system block already containing one text block, routed to model `qwen3.8:27b-mlx` under the config above, reaches the capturing upstream with (a) zero system-role entries left in the message list, (b) the remaining entries in their original relative order, and (c) a top-level system block of exactly three text blocks whose texts are, in order, the pre-existing one, then `A`, then `B` — evidence: Ginkgo assertion on the JSON captured by an `httptest` upstream in `pkg/handler/model-router_test.go`.

- [ ] **Non-matching model on the same provider is untouched.** The identical request body routed to `qwen3.6:35b-a3b-coding-nvfp4` (matches `qwen*`, does not match `qwen3.8*`) arrives at the capturing upstream byte-identical to the body the client sent — evidence: negative evidence, `Expect(captured).To(Equal(original))` byte-slice comparison in `pkg/handler/model-router_test.go`, and the forwarded `Content-Length` equals the original length.
      **Precondition (required for byte-identity to be meaningful):** the test fixture config declares no `aliases` and the request model name carries no trailing `[1m]` suffix, so neither of the pre-existing rewrite paths (`model-router.go:138,171`) fires. Both re-marshal via `rewriteModelField`, and Go map marshalling reorders keys — byte-identity is destroyed on those paths by design, not by this change.

- [ ] **Provider without the field is untouched.** The identical request body routed through a provider whose config omits `requiresLeadingSystem` entirely (and, separately, one that sets it to an empty list) arrives byte-identical at the capturing upstream, and no transform log line is emitted — evidence: byte-slice equality assertion plus captured glog output containing zero occurrences of `[system-lift]`, in `pkg/handler/model-router_test.go`. Same fixture precondition as the criterion above.

- [ ] **Unparsable body degrades, never fails the request.** A request routed to a *matching* model whose `messages` field is a JSON string rather than a list (and, separately, one whose message list contains a non-object entry) reaches the upstream byte-identical, the client receives the upstream's own status rather than a router-generated 5xx, and the router does not panic — evidence: byte-slice equality assertion, status-code assertion against the `httptest` upstream's response, and captured glog output containing exactly one warning line, in `pkg/handler/model-router_test.go`. Same fixture precondition as the two criteria above (no aliases, no `[1m]` suffix), so the byte-identity assertion is meaningful.

- [ ] **Transform log line has the specified shape.** A request that triggers the transform emits exactly one captured log line containing `[system-lift]`, the resolved model name, and `moved=2`, and containing neither the string `A` nor `B` from the lifted system contents — evidence: Ginkgo assertion on captured glog output in `pkg/handler/model-router_test.go` (positive counterpart to the negative assertion above; the log level is set explicitly in the test, so this does not depend on the operator's runtime verbosity).

- [ ] **String-form system content is normalised into text blocks.** A lifted system entry whose content is the plain string `hello` appears in the top-level system block as exactly one entry equal to `{"type":"text","text":"hello"}`; a lifted entry whose content is already a block list is appended unchanged, block for block — evidence: Ginkgo assertion on the captured upstream JSON in `pkg/handler/model-router_test.go`.

- [ ] **A system entry that is already first stays where it is.** A request whose message list begins with a system-role entry, routed to a matching model, arrives with that first entry still at index 0 of the message list and not copied into the top-level system block — evidence: negative evidence, Ginkgo assertion that the captured message list length is unchanged and its first entry still has role `system`.

- [ ] **`docs/config.md` records the semantics and the reason the field is model-scoped.** `docs/config.md` documents the field's glob semantics, its empty-means-no-transform default, **and** why the scope is per-model rather than per-provider — naming that ollama's system-position restriction is a property of each model's chat template, so `qwen3.6` and `qwen3.8` behave differently behind one provider (verified 2026-08-15 by identical curl payloads: qwen3.6 → 200, qwen3.8 → 500) — evidence: `grep -c 'requiresLeadingSystem' docs/config.md` ≥1 plus the section containing both `qwen3.6` and `chat template`.
      *(The bare "field appears in `docs/config.md` / `docs/config.example.yaml` / `README.md`" mandate is not restated here — `docs/dod.md:31-34` already enforces it per prompt as this project's configured `validationPrompt`. This criterion covers only the part the DoD does not: the recorded rationale, which otherwise dies with the spec.)*

- [ ] **Post-Deploy (Rung-2):** the live router serves qwen3.8 successfully. With `requiresLeadingSystem: ["qwen3.8*"]` in the operator's live config and the router restarted, a `POST /v1/messages` carrying a non-leading system-role entry and `"model":"qwen3.8:27b-mlx"` returns HTTP 200 with a generated assistant message (not `system message must be at the beginning`), and — **after raising the log level, since `[system-lift]` is `V(2)` and V(1) is the always-on default (`docs/debug.md`)** — `/tmp/claude-code-router.log` contains a `[system-lift]` line naming the model. The operator MUST run `curl http://127.0.0.1:8788/setloglevel/2` before the probe; without it this criterion false-fails a correct implementation. Note the level auto-reverts after 5 minutes, so run the probe and the grep inside that window — evidence: curl HTTP status 200 plus a log-line grep returning ≥1.
  - `deploy_check:` `strings $(command -v claude-code-router) 2>/dev/null | grep -qF '[system-lift]' && echo present || echo absent`
  - `deploy_target:` `present`

## Verification

### Container-executable (runs inside the dark-factory container at prompt time)

```bash
make precommit
grep -c 'requiresLeadingSystem' docs/config.md docs/config.example.yaml README.md
# CHANGELOG: two independent checks, NOT one combined grep — a valid entry such as
# "feat: lift non-leading system messages for models that reject them" satisfies the
# DoD and the conventional-prefix constraint but would fail a single 'feat:.*field' grep.
grep -n '^- feat:' CHANGELOG.md          # expect >= 1 under ## Unreleased
grep -n 'requiresLeadingSystem' CHANGELOG.md   # expect >= 1 (may be the same line)
# go.mod: assert NO DRIFT, not "zero exclude/replace" — go.mod:52 carries a
# pre-existing `exclude (cloud.google.com/go v0.26.0)` block unrelated to this
# change. A zero-match gate false-fails immediately and can only be satisfied by
# deleting that block. The DoD line means "do not ADD one".
git diff --quiet -- go.mod go.sum && echo "go.mod/go.sum unchanged"
```

### Operator-executable (runs on the host after merge)

```bash
# 1. Add the field to the live config, then restart the router
$EDITOR ~/.config/claude-code-router/config.yaml
launchctl kickstart -k gui/$(id -u)/de.bborbe.claude-code-router

# 1.5 Raise the log level — REQUIRED before step 3.
#     `[system-lift]` is V(2) (same level as [alias] / [1m-strip]); V(1) is the
#     always-on default, so step 3 greps zero on a CORRECT build without this.
#     Auto-reverts after 5 min — run steps 2-3 inside that window.
curl -s http://127.0.0.1:8788/setloglevel/2

# 2. Send a Claude-Code-shaped body with a non-leading system message
curl -s -o /tmp/qwen38.json -w '%{http_code}\n' http://localhost:<port>/v1/messages \
  -H 'content-type: application/json' \
  -d '{"model":"qwen3.8:27b-mlx","max_tokens":64,
       "system":[{"type":"text","text":"top"}],
       "messages":[{"role":"user","content":"hi"},
                   {"role":"system","content":"inline"}]}'
# Expect: 200, and /tmp/qwen38.json contains an assistant message,
#         not "system message must be at the beginning"

# 3. Confirm the transform fired
grep -c 'system-lift' /tmp/claude-code-router.log   # expect >= 1

# 4. Confirm qwen3.6 still works unchanged
#    (same body with "model":"qwen3.6:35b-a3b-coding-nvfp4") -> 200
```

## Desired Behavior

1. Each provider in the config gains an optional list of model-name glob patterns declaring "models behind this provider reject a system message that is not first". Patterns use the same matching syntax as the existing model-routing patterns. Absent or empty list means: never transform anything for this provider.
2. Config loading rejects a malformed pattern in that list with an error naming the provider and the offending pattern, in the same place and style the existing model patterns are validated.
3. After the router has decided which provider a request goes to, and after the model name has been fully resolved (aliases and the `[1m]` suffix already applied), the router checks the resolved model name against the matched provider's pattern list. No match, or an empty list, means the body is forwarded byte-identical — no parse-and-re-marshal round trip that could reorder or renumber anything.
4. On a match, the router removes every system-role entry from the conversation list except one occupying the first position, and appends their content to the top-level system block in the order the entries appeared. Content given as a plain string becomes a single text block; content already given as a block list is appended block for block. A request with no misplaced system entry is forwarded byte-identical even when the model matches. A system entry that is already first is left in place and **not** copied into the top-level block, even when a top-level system block is also present — the upstream then legitimately receives system content in both places, which is the shape Claude Code already sends today and which the upstream accepts; deduplicating or merging the two is explicitly not this change's job.
5. When the transform fires, the router emits one detail log line prefixed with the frozen literal `[system-lift]`, naming the resolved model and the number of entries moved, at the same verbosity level as the existing `[alias]` and `[1m-strip]` detail lines. The line never contains system-message content.
6. If the body cannot be transformed (not a JSON object, message list not a list, an entry not an object), the router forwards the original body unchanged rather than failing the request, and logs the reason once at warning level.

## Constraints

- **Backwards compatibility is absolute.** Every config in operator use today omits the new field and MUST keep loading and routing with identical bytes on the wire. Absent and empty list are equivalent.
- **Model-scoped, never provider-scoped.** The decision uses the resolved model name only. qwen3.6 and qwen3.8 share the `ollama-local` provider and must diverge.
- **Byte fidelity on the no-transform path.** When the model does not match, the request body forwarded upstream is the same byte sequence the router received (subject to the existing alias / `[1m]` rewrites, which are unchanged by this work).
- **Ordering is content-preserving.** No system text may be dropped, merged, reordered relative to other lifted entries, or truncated.
- **Content-Length correctness.** Whenever the body changes, the forwarded content length matches the new body exactly, as the existing rewrite paths already do.
- **Existing behaviour must not regress:** alias resolution, `[1m]` suffix stripping, glob routing, default-provider fallback, metrics labels, token extraction, trace capture, and the request-body size limit all keep working as they do today, and the existing test suites keep passing.
- **Config single source of truth.** The new field lives on the config struct and is validated by the config's own validation step; no parsing logic elsewhere.
- **Project Definition of Done applies** — see `docs/dod.md`: GoDoc on exported items, Ginkgo/Gomega coverage ≥ 80% on changed packages, `docs/config.md` + `docs/config.example.yaml` + `README.md` + `CHANGELOG.md` updated, all repo references to config fields updated, no `exclude` / `replace` in `go.mod`.
- **CHANGELOG prefix.** The repo auto-releases; the entry must carry a conventional prefix (`feat:`). An unprefixed bullet was rejected by bot review on PR #36.

## Failure Modes

| Trigger | Expected behavior | Detection | Recovery |
|---|---|---|---|
| Malformed glob pattern in the new field | Config load fails at startup with an error naming the provider and pattern; the service does not serve traffic with a half-understood config | Startup error line in `/tmp/claude-code-router.log`; supervisor restart loop | Operator fixes the pattern, restarts the router, raises the log level (`curl http://127.0.0.1:8788/setloglevel/2` — `[route]` is V(2), invisible at the default V(1)), then confirms a `[route]` line for a test request |
| Field present but matching no model the operator actually uses | No transform ever fires; qwen3.8 keeps returning 500 from the upstream | Absence of `[system-lift]` lines in the log while 500s continue — **only meaningful after `curl http://127.0.0.1:8788/setloglevel/2`**, since `[system-lift]` is V(2); at the default V(1) its absence proves nothing | Operator widens the pattern (`qwen3.8*`), restarts, re-runs the curl check until it returns 200 |
| Request body is not a JSON object, or the message list is not a list of objects | Body forwarded unchanged; request behaves exactly as it does today; one warning line, no content logged | Warning line in the log; upstream status unchanged | None needed — degraded path equals today's behaviour |
| No misplaced system entry in a matching request | Forwarded byte-identical; no log line | Absence of `[system-lift]` for that request | None needed |
| Upstream (Ollama) down or the model not pulled | Transform still runs; the upstream connection error or model-not-found surfaces to Claude Code exactly as it does today | Existing `[req] ... status=` log line with the upstream's status | Operator starts Ollama / pulls the model; no router change |
| Claude Code changes its payload shape (system entries move, or the top-level system field arrives as a plain string rather than a block list) | Router handles a string-form top-level system field by normalising it to a text block before appending; any shape it cannot interpret falls back to forwarding unchanged plus one warning | Warning line; 500s reappear from the upstream | Operator reports it; a follow-up spec adapts the transform |
| Very large system content (near the request body size limit) | Existing request-size limit governs; a body over the limit is rejected before the transform is reached, as today | Existing `request body too large` log + HTTP 413 | Operator reduces context; unchanged from today |

## Security / Abuse Cases

- **Attacker-controlled surface:** the entire request body. It is already attacker-shaped input today (the router parses the model field out of it) and already bounded by the existing max-body-size reader; the transform runs after that limit is enforced, so memory stays bounded.
- **Trust boundary:** the router rewrites a prompt on behalf of the client. The transform must be content-preserving — moving text between two fields the upstream already treats as system instructions does not add privilege, but dropping or reordering text would silently change model behaviour. Hence the order-preservation and no-transform-unless-matched constraints.
- **Hostile shapes:** deeply nested or oddly typed message entries (numbers, nulls, arrays where objects are expected) must not panic the router or send it into an unbounded loop — the transform makes a single pass over a flat message list, and any shape it does not recognise takes the forward-unchanged path.
- **Log hygiene:** system prompts routinely contain private repository context. The `[system-lift]` line carries the model name and a count only — never message content — matching the existing redaction posture of the trace and logging paths.
- **No new network calls, no new filesystem writes, no new deserialisation of untrusted data beyond the JSON the router already parses.**

## Suggested Decomposition

Prompts should be generated in this order — each row is a single prompt with a clear scope.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Config: new per-provider pattern list + load-time pattern validation + Ginkgo specs | 1, 2 | 1, 2 | — |
| 2 | Router transform: lift non-leading system entries, string→text-block normalisation, no-match byte fidelity, log line shape, degrade-on-unparsable + Ginkgo specs | 3, 4, 5, 6 | 3, 4, 5, 6, 7, 8, 9 | prompt 1 (consumes the config shape) |
| 3 | Wiring the provider's patterns through to the router, plus `docs/config.md` (including the model-scoped rationale), `docs/config.example.yaml`, `README.md`, `CHANGELOG.md` | 3 (wiring half) | 10 | prompts 1, 2 |

Rationale: prompt 1 fixes the config contract everything else reads; prompt 2 is a self-contained handler change testable against a capturing test upstream without any config wiring; prompt 3 connects them and ships the operator-facing documentation. AC 11 (Post-Deploy) is an operator-side check after merge, not a prompt.

**Size note:** 6 DBs × 11 ACs = 66, above the 50 threshold, but the change spans only two code layers (config + handler) plus docs — under the 3-layer split threshold. The seam between "a config field" and "its single consumer" is artificial, so this decomposition table is the mitigation rather than splitting the spec. The bare "field is mentioned in docs/README" mandates are deliberately **not** ACs here: `docs/dod.md:31-34` is this project's configured `validationPrompt` and already enforces them on every prompt.

## Do-Nothing Option

qwen3.8 stays unusable through the router: every Claude Code request returns HTTP 500 in ~90ms, and the `cc-personal-qwen` launcher stays broken. The operator keeps a standalone Python shim in the request path as a permanent second proxy hop — undocumented, untested, unmonitored, and outside the router's config, logging, metrics and trace story, which is exactly the fragmentation the router exists to remove. The alternative of pinning the launcher back to qwen3.6 forfeits the newer model entirely. Waiting for an upstream Ollama chat-template fix is unbounded in time and, even if it lands, the same class of model-specific payload restriction will recur — a declarative per-model escape hatch in the router is the durable answer either way.

## Related

- Verified 2026-08-15: identical curl payloads, same Ollama instance — `qwen3.6:35b-a3b-coding-nvfp4` → 200; `qwen3.8:27b-mlx` → 500; `qwen3.8:27b-mtp-q4_K_M` → 500. All three match the `ollama-local` provider's `qwen*` pattern.
- Upstream rejection originates in Ollama's `routes.go` chat-template validation; worth a separate upstream issue.
- Project Definition of Done: `docs/dod.md`
- Config reference: `docs/config.md`, `docs/config.example.yaml`

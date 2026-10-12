---
status: completed
summary: Host-slot queue timeouts now observe ccrouter_queue_wait_seconds under the request's provider label, so the rejection counter and queue-wait histogram no longer disagree.
execution_id: claude-code-router-host-queue-wait-exec-066-host-slot-queue-wait-observation
dark-factory-version: v0.196.0
created: "2026-10-12T08:42:16Z"
queued: "2026-10-12T08:42:16Z"
started: "2026-10-12T08:43:06Z"
completed: "2026-10-12T08:48:13Z"
---

# Observe host-slot queue waits on the provider-labelled histogram

<summary>
- A request that gives up waiting for a shared per-host concurrency slot is refused with the same "queue timeout" reason as one that gives up waiting for a provider slot.
- Only the provider-slot wait is measured, so the refusal is counted while the wait behind it is invisible — two views of one event that disagree.
- Observed on the live production router: two host-slot refusals counted, zero waits measured, across every provider.
- The cause is that the shared host-budget wrapper is built without a provider name and without the wait histogram, so its measurement call quietly does nothing.
- The wrapper is in fact built per upstream, so it can carry the provider name — only the slot budget itself is shared between them.
- This change passes the provider name and the wait histogram into that wrapper, so a host-slot wait is measured under the same provider the refusal is already attributed to.
- No new metric and no new refusal reason: the existing series simply stop contradicting each other.
- A test proves the measurement now happens: the existing host-slot timeout spec gains the assertion that the wait is recorded.
</summary>

<objective>
Make a request refused for waiting too long on a shared per-host concurrency slot report how long it waited, under the same provider label its refusal is already attributed to — so the rejection counter and the queue-wait histogram describe one event consistently instead of two series that disagree.
</objective>

<context>
Read `docs/dod.md` for Definition of Done criteria.

Files to read (all of them, before editing):
- `pkg/handler/host-limiter.go` — `HostLimiter`, `NewHostLimiter`, and `Wrap`, which returns a `&concurrencyLimiter{...}` struct literal for the capped path. Read the `Wrap` GoDoc comment: it is the claim this change corrects.
- `pkg/handler/concurrency-limiter.go` — the `concurrencyLimiter` struct (`provider` and `queueWait` fields), `NewConcurrencyLimiter` (the canonical constructor), the nil-guarded `observeQueueWait`, and `ServeHTTP`'s waiting path. Both the acquire branch and the `case <-timer.C:` timeout branch call `observeQueueWait`; the timeout branch then calls `writeRateLimited(w, r, 0, rejectionReasonQueueTimeout)`.
- `pkg/factory/factory.go` — the per-upstream loop where `memberHandler` is built with `handler.NewConcurrencyLimiter(proxy, name, ...)` and then wrapped via `hostLimiterFor(...).Wrap(memberHandler)`. `name` is the provider and is in scope at that call. Also read `hostLimiterFor`: it returns a **shared** `*handler.HostLimiter` per host key — what is shared is the semaphore, not the wrapper.
- `pkg/handler/metrics.go` — the `QueueWaitSeconds` field, its `HistogramOpts.Help` text, and `PreInitRejection`.
- `pkg/handler/host-limiter_test.go` — the existing specs, in particular `It("answers HTTP 429 with the static rate_limit_error body on queue timeout")`, which this change extends.
- `docs/metrics.md` — the metrics table and the prose paragraphs beneath it.

Clock note: `Wrap` sets `now: realNow` explicitly and MUST keep doing so. The struct literal bypasses `NewConcurrencyLimiter`'s nil-default, and an earlier defect on this file was exactly a nil clock panicking on the first host-slot timeout.
</context>

<requirements>

1. **`pkg/handler/host-limiter.go::Wrap`** — add two positional parameters: `provider string` and `queueWait *prometheus.HistogramVec`. The capped path returns `&concurrencyLimiter{next: next, provider: provider, sem: h.sem, wait: h.wait, now: realNow, queueWait: queueWait}`. Add the `github.com/prometheus/client_golang/prometheus` import.

2. **`pkg/handler/host-limiter.go` — the `Wrap` GoDoc comment** — rewrite it. It must no longer claim the wrapper carries no provider label. State the true shape: `Wrap` is called once per upstream member, so each wrapper carries its own provider while the semaphore is shared across every provider on that host; `queueWait` is the same `ccrouter_queue_wait_seconds` histogram the member limiter observes, so a host-slot wait is attributed to the provider the request routed to — the same label its rejection counter already carries.

3. **`pkg/factory/factory.go`** — pass `name` and `metrics.QueueWaitSeconds` at the `Wrap` call site. Do not construct a second histogram or a second limiter.

4. **The new parameters break every existing `Wrap` call site** — update all 14, passing `""` and `nil` so no existing spec starts observing a histogram. Run `grep -rn '\.Wrap(' pkg/` to enumerate them: 9 in `pkg/handler/host-limiter_test.go` (hint, may drift: ~lines 33, 34, 59, 60, 86, 112, 129, 152, 166) and 5 in `pkg/handler/upstream-inflight-collector_test.go` (hint: ~lines 31, 49, 96, 102, 124). The compiler catches any you miss, so a green build is the check. `hostInFlightCounter` is unaffected: it never queues and never times out, so `Wrap` still returns it when `h.sem == nil`.

5. **`docs/metrics.md`** — add a prose paragraph after the metrics table (the row is a 4-column cell, so prose belongs beneath it) saying the `ccrouter_queue_wait_seconds` observation covers both the member limiter and the host limiter, and that a request which waits for a host slot and then a member slot contributes two observations, because it waited twice on two different resources. `_count` therefore counts queue-wait events, not requests that queued.

6. **`pkg/handler/host-limiter_test.go`** — extend the existing `It("answers HTTP 429 with the static rate_limit_error body on queue timeout")` spec rather than adding a near-duplicate. It already builds a cap-1 `HostLimiter` with a 50ms wait, holds the slot with a blocking handler, and asserts the second request gets 429 with `expected429Body`, `rate_limit_error`, and no entry into the handler. What it lacks is the queue-wait observation: wrap with a provider label and a real `prometheus.NewHistogramVec` built from `QueueWaitBucketsSeconds`, and assert `ccrouter_queue_wait_seconds_count{provider="<label>"}` is 1 with `_sum` at approximately the full configured wait.

7. **`CHANGELOG.md`** — add an `## Unreleased` bullet: a host-slot timeout now observes `ccrouter_queue_wait_seconds` under the request's provider label, so it no longer increments `ccrouter_rejected_total{reason="queue_timeout"}` without a matching wait observation. Note the breaking change: `HostLimiter.Wrap` gains two positional parameters (`provider string`, `queueWait *prometheus.HistogramVec`).

8. **Acceptance — the change is correct when:** `Wrap` takes `provider` and `queueWait` and the capped literal sets both while still setting `now: realNow`; every `Wrap` call site compiles; a host-slot timeout records exactly one `ccrouter_queue_wait_seconds` observation for the wrapped provider at approximately the full configured wait; a host-slot timeout still answers HTTP 429 with `reason="queue_timeout"`; the unlimited path behaves exactly as before; the `Wrap` GoDoc no longer asserts the wrapper cannot carry a provider label; `CHANGELOG.md` carries the `## Unreleased` bullet.

9. **Self-check before finishing** — re-run `<verification>` and confirm it passes, then walk each acceptance item in requirement 8 against the change and report any you cannot satisfy as a blocker.

</requirements>

<constraints>

- Do NOT commit — dark-factory handles git
- Do NOT add a reason value — a host-slot timeout stays `reason="queue_timeout"`
- Do NOT add a separate `ccrouter_host_queue_wait_seconds` metric or any host-labelled histogram
- Do NOT change `NewConcurrencyLimiter`'s signature, or the member limiter's observation behaviour
- Do NOT change `NewHostLimiter`, or how the semaphore is shared across providers on one host
- Do NOT change `writeRateLimited`, `limiter429Body`, or the `Retry-After` handling
- No dashboard or `grafana-instance` YAML is edited in this repo — dashboards live in `bborbe/nuke`
- Do NOT change the `ccrouter_rejected_total` help text
- Existing tests must still pass

</constraints>

<verification>

```bash
cd /workspace
grep -n 'queueWait' pkg/handler/host-limiter.go        # expect: the new parameter and the literal field
make precommit                                         # expect: exit 0
```

</verification>

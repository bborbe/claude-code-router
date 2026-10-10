# Prometheus Metrics

The `/metrics` endpoint exposes router telemetry via `promhttp.Handler()` against the Prometheus default registry. Application series use the `ccrouter_` prefix to avoid collisions with other local exporters; the default registry also exposes Go runtime series (`go_gc_*`, `go_memstats_*`, `process_*`) — useful for spotting GC pressure or memory growth on the long-running router daemon.

## Prometheus scrape config

```yaml
scrape_configs:
  - job_name: claude-code-router
    static_configs:
      - targets: ['127.0.0.1:8788']  # or host.docker.internal:8788 from a container
    metrics_path: /metrics
    scrape_interval: 15s
```

## Series

| Metric | Labels | Type | Example value |
|---|---|---|---|
| `ccrouter_requests_total` | `provider`, `model`, `status_class` | counter | `3` |
| `ccrouter_request_duration_seconds` | `provider`, `model` | histogram | `0.842` (p95 bucket) |
| `ccrouter_alias_resolutions_total` | `alias`, `resolved` | counter | `1` |
| `ccrouter_tokens_total` | `provider`, `model`, `direction` | counter | `42` (input) / `17` (output) |
| `ccrouter_cache_tokens_total` | `provider`, `model`, `direction` | counter | `5000` (read) / `200` (creation) |
| `ccrouter_throttled_total` | `provider` | counter | `1` (per paced request) |
| `ccrouter_upstream_inflight` | `host` | gauge | `8` (at an 8-slot host cap) |
| `ccrouter_inflight_requests` | `provider` | gauge | `3` (currently executing) |
| `ccrouter_inflight_requests_peak` | `provider` | gauge | `5` (max over the last 60 s) |

`status_class` is one of `2xx`, `3xx`, `4xx_auth` (401/403), `4xx_rate_limited` (429), `4xx_bad_request` (all other 4xx — including the router-side 413 body-too-large and 400 body-read-failed early returns), `5xx_upstream` (5xx from the upstream provider), `5xx_router` (5xx from a router-side rejection — currently the alias-rewrite-failed path), or the raw status code for out-of-range values. The `5xx_upstream`/`5xx_router` split is driven by an `isRouterError` argument at the `ObserveRequest` call site — see `pkg/handler/metrics.go`. Cardinality ceiling: 5 providers × 15 models × 7 status classes = 525 request-counter series (~450 in practice — some (provider, model, status_class) tuples never fire) + 5 × 15 × 2 directions = 150 tokens-counter series + 5 × 15 × len(buckets) = 750 histogram bucket series + alias counter bounded by YAML config + 2 in-flight gauge series per provider (current + peak) + one `ccrouter_upstream_inflight` series per distinct upstream host in the config (bounded by the YAML config like the other config-bounded labels, typically a handful) = ~1.5k total. Operators sizing Prometheus retention should plan for the ~1.5k combined-series ceiling.

`direction` on `ccrouter_tokens_total` is bounded to `input` or `output`; any other direction value is dropped at `ObserveTokens` and never reaches Prometheus. Non-2xx responses do not increment `ccrouter_tokens_total` (token counting is a strict success-path observation — a failed upstream call does not carry a trustworthy usage object).

`ccrouter_cache_tokens_total` is the prompt-cache counterpart, kept **separate** from `ccrouter_tokens_total` so billed (fresh input + output) and true (fresh + cache + output) usage stay distinguishable. `direction` is bounded to `read` (tokens served from a reused prompt cache — Anthropic `cache_read_input_tokens`) or `creation` (tokens written into the cache — Anthropic `cache_creation_input_tokens`); any other value is dropped at `ObserveCacheTokens`. Non-Anthropic providers that emit no cache fields keep their cache series at zero (never created — the zero-drop rule applies); providers that *do* report Anthropic-shaped cache counts get real series, including ones (such as Seibert vLLM) that zero the `message_start` usage block and send the true cumulative counts in the terminal `message_delta` — the extractor prefers whichever of the two events carries a positive value. Same success-path rule as the tokens counter.

`model` on all `ccrouter_*` series resolves through a sentinel chain (post-alias resolved model → pre-alias original model → `_unknown_`), so no `model=""` empty label ever reaches Prometheus. The `_unknown_` sentinel also appears as the `provider` label value on the three router-side early-return paths (body-too-large, body-read-failed, alias-rewrite-failed) where routing never resolved a provider.

`ccrouter_throttled_total` counts requests the 429 delay gate actually delayed before forwarding (`throttle429Threshold` enabled — see `docs/config.md ## 429 delay gate`); overflow 429s and non-paced requests do not increment it. `provider` is bounded by the YAML config like the other provider-labeled series. The counter is additive — the `status_class` 7-value enum is unchanged, and upstream 429s still record through `4xx_rate_limited`.

`ccrouter_upstream_inflight` is the current number of `/v1/*` requests in flight to one upstream host, summed across every provider and pool member whose upstream resolves to it, read live at scrape time. For a host capped in `upstreamHostLimits` (see `docs/config.md ## Upstream host limits`) it is the shared semaphore's occupancy and never exceeds the cap; for an uncapped host it is the live in-flight count. Every host the router serves has a series, uncapped hosts included; a config entry naming a host no provider uses has none. `host` is the operator-configured upstream host key (the lowercased host of the upstream URL, plus `:port` only for a non-default port) — never a request header or client value, so a client cannot influence the label. Cardinality is one series per distinct upstream host in the config, bounded by the YAML config like the other config-bounded labels and typically a handful. The series is additive; all existing series are unchanged.

`ccrouter_inflight_requests` is the current number of requests dispatched to the provider and not yet returned — incremented immediately before the provider handler is invoked and decremented on every exit, including success, upstream error, client cancel, and a panic in the handler. The provider handler wraps the 429 throttle gate (pacing delay) and the per-upstream concurrency limiters (queue wait, overflow 429), so the gauge counts requests that are **queued, paced, or executing** — not just those with an open upstream socket. A value above the sum of the provider's `maxConcurrentRequests` therefore means requests are queueing (or waiting on the pacing delay) rather than running. The gauge is incremented on the dispatch path only; the router-side early returns (body too large, body read failed, alias/pool/`[1m]` rewrite failed) never dispatch upstream and never touch it.

`ccrouter_inflight_requests_peak` is the maximum of `ccrouter_inflight_requests` over a sliding **60 s** window, computed at collect time from per-second maxima. It is **not reset on read** — several scrapers or pushgateway pushers all see the same value — and it is always at least the current count, so a request still open after the window has passed keeps `peak >= current`. The window is wide enough that a burst which starts and ends between two 15 s scrapes is still visible for several scrapes. A config reload (SIGHUP) builds a fresh `Metrics` and thus a fresh in-flight collector: the current gauge briefly under-reports (it restarts from the live request set, which is empty at reload) and the peak resets to zero.

## Grafana queries

**Requests per second by provider:**
```promql
sum by (provider) (rate(ccrouter_requests_total[5m]))
```

**p95 latency by provider:**
```promql
histogram_quantile(0.95, sum by (le, provider) (rate(ccrouter_request_duration_seconds_bucket[5m])))
```

**Error-class breakdown by provider** — distinguishes 429 quota exhaustion, 401/403 auth failures, generic 4xx client errors, upstream 5xx, and router-side 5xx:
```promql
sum by (provider, status_class) (rate(ccrouter_requests_total{status_class=~"4xx_.*|5xx_.*"}[5m]))
```

**429 rate specifically** (subscription-quota near-exhaustion signal):
```promql
sum by (provider) (rate(ccrouter_requests_total{status_class="4xx_rate_limited"}[5m]))
```

**Tokens/s by provider and direction** — LLM token throughput broken down by input vs output:
```promql
sum by (provider, direction) (rate(ccrouter_tokens_total[5m]))
```

**Tokens/s by model** — per-model verbosity comparison:
```promql
sum by (model, direction) (rate(ccrouter_tokens_total[5m]))
```

**In-flight requests by provider** — current parallelism, to compare against the summed per-upstream `maxConcurrentRequests`:
```promql
sum by (provider) (ccrouter_inflight_requests)
```

**Peak in-flight requests by provider** — the highest parallelism seen in the last 60 s, catches bursts a 15 s scrape would miss:
```promql
max by (provider) (ccrouter_inflight_requests_peak)
```

**Average concurrency without the new metric** — the same view derived from the existing latency histogram's `_sum` and `_count`, useful for cross-checking the gauge or for dashboards that predate it:
```promql
sum by (provider) (rate(ccrouter_request_duration_seconds_sum[5m]))
```

## Alerting example

```yaml
groups:
  - name: claude-code-router
    rules:
      - alert: AnthropicQuotaNearExhaustion
        expr: |
          sum by (provider) (
            rate(ccrouter_requests_total{provider="anthropic-subscription",status_class="4xx_rate_limited"}[5m])
          ) * 60 > 10
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "429 spike on {{ $labels.provider }} — >10 429/min for 5 min; subscription quota may be near exhaustion"
```

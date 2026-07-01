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

`status_class` is one of `2xx`, `3xx`, `4xx_auth` (401/403), `4xx_rate_limited` (429), `4xx_bad_request` (all other 4xx — including the router-side 413 body-too-large and 400 body-read-failed early returns), `5xx_upstream` (5xx from the upstream provider), `5xx_router` (5xx from a router-side rejection — currently the alias-rewrite-failed path), or the raw status code for out-of-range values. The `5xx_upstream`/`5xx_router` split is driven by an `isRouterError` argument at the `ObserveRequest` call site — see `pkg/handler/metrics.go`. Cardinality ceiling: 5 providers × 15 models × 7 status classes = 525 request-counter series (~450 in practice — some (provider, model, status_class) tuples never fire) + 5 × 15 × 2 directions = 150 tokens-counter series + 5 × 15 × len(buckets) = 750 histogram bucket series + alias counter bounded by YAML config = ~1.5k total. Operators sizing Prometheus retention should plan for the ~1.5k combined-series ceiling.

`direction` on `ccrouter_tokens_total` is bounded to `input` or `output`; any other direction value is dropped at `ObserveTokens` and never reaches Prometheus. Non-2xx responses do not increment `ccrouter_tokens_total` (token counting is a strict success-path observation — a failed upstream call does not carry a trustworthy usage object).

`model` on all `ccrouter_*` series resolves through a sentinel chain (post-alias resolved model → pre-alias original model → `_unknown_`), so no `model=""` empty label ever reaches Prometheus. The `_unknown_` sentinel also appears as the `provider` label value on the three router-side early-return paths (body-too-large, body-read-failed, alias-rewrite-failed) where routing never resolved a provider.

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

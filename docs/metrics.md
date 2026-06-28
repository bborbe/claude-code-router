# Prometheus Metrics

The `/metrics` endpoint exposes router telemetry via `promhttp.HandlerFor` backed by a fresh `prometheus.NewRegistry()` (process-level `go_*` series are excluded by default). All series use the `ccrouter_` prefix to avoid collisions with other local exporters.

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

`status_class` is one of `2xx`, `3xx`, `4xx`, `5xx`, or the raw status code for out-of-range values. Cardinality: ~1k series at 5 providers × 15 models × 3 status classes (225 counter series + 750 histogram bucket series + alias counter bounded by YAML config).

## Grafana queries

**Requests per second by provider:**
```promql
sum by (provider) (rate(ccrouter_requests_total[5m]))
```

**p95 latency by provider:**
```promql
histogram_quantile(0.95, sum by (le, provider) (rate(ccrouter_request_duration_seconds_bucket[5m])))
```

**4xx rate by provider** (covers 429 quota exhaustion + all other client errors; if you need 429-specifically, expand `status_class` into a per-status-code label in `ObserveRequest`):
```promql
sum by (provider) (rate(ccrouter_requests_total{status_class="4xx"}[5m]))
```

## Alerting example

```yaml
groups:
  - name: claude-code-router
    rules:
      - alert: AnthropicQuotaNearExhaustion
        expr: |
          sum by (provider) (
            rate(ccrouter_requests_total{provider="anthropic-subscription",status_class="4xx"}[5m])
          ) * 60 > 10
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "429 spike on {{ $labels.provider }} — >10 4xx/min for 5 min; quota may be near exhaustion"
```

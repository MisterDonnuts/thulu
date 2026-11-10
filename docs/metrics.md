# Metrics

Thulu exposes Prometheus metrics at `GET /metrics` (OpenMetrics format). It needs the bearer token in `OCTOPUS_METRICS_TOKEN`, or, with no token set, an admin of the operator workspace.

```
curl -H "Authorization: Bearer $OCTOPUS_METRICS_TOKEN" http://localhost:8585/metrics
```

---

## Initialization

`metrics.Init()` is called once at server startup. It creates a **separate Prometheus registry** (not the default global registry) and registers all metric families plus standard Go runtime and process collectors.

---

## HTTP middleware

`metrics.Middleware(next)` wraps the entire handler chain. It skips health and metrics paths to avoid noise:

- `/health`, `/health/live`, `/health/ready`, `/metrics` are excluded

For all other requests, it records:
- `octopus_http_requests_total` (counter)
- `octopus_http_request_duration_seconds` (histogram)

---

## Path normalization

High-cardinality path segments (task IDs, project IDs, etc.) are collapsed to `{id}` to keep metric cardinality manageable.

**Rule**: a path segment is treated as an ID if:
- It is ≥ 8 characters long, **and**
- Every character is a hex digit (`0-9`, `a-f`, `A-F`) or a hyphen `-`

Examples:
```
/tasks/abc1234f/comments     →  /tasks/{id}/comments
/projects/def98765/worktrees →  /projects/{id}/worktrees
/auth/me                     →  /auth/me      (unchanged)
```

---

## Metric families

### 1. `octopus_http_requests_total`

Type: **Counter**

Labels: `method`, `path`, `status`

Total HTTP requests processed. `path` is normalized (see above). `status` is the HTTP status code as a string (`"200"`, `"404"`, etc.).

```promql
# Error rate
rate(octopus_http_requests_total{status=~"5.."}[5m])
  / rate(octopus_http_requests_total[5m])
```

---

### 2. `octopus_http_request_duration_seconds`

Type: **Histogram**

Labels: `method`, `path`

Buckets: Prometheus default (`.005`, `.01`, `.025`, `.05`, `.1`, `.25`, `.5`, `1`, `2.5`, `5`, `10`).

```promql
# p99 latency
histogram_quantile(0.99,
  rate(octopus_http_request_duration_seconds_bucket[5m])
)
```

---

### 3. `octopus_sandboxes_active`

Type: **Gauge**

No labels.

Number of sandbox containers currently acquired (not yet released). Incremented on `Acquire`, decremented on `Release`.

```promql
octopus_sandboxes_active
```

---

### 4. `octopus_tasks_by_stage`

Type: **Gauge**

Labels: `board`, `stage`

Number of tasks in each (board, stage) pair, refreshed by the periodic sweep.

The `board` label is not optional detail: a stage id only means something
within its board, so `review` on the bugfix flow and `review` on the feature
flow are different columns and summing them yields a number nobody asked for.
Cardinality stays bounded — boards are few and admin-created, and both label
values are slugs by construction.

```promql
# Total active tasks on the default board (its terminal column is `done`)
sum(octopus_tasks_by_stage{board="default", stage!="done"})

# Per-stage bar chart for one flow
octopus_tasks_by_stage{board="bugfix"}

# Work in flight across every flow
sum by (board) (octopus_tasks_by_stage)
```

---

### 4b. `octopus_tasks_by_type`

Type: **Gauge**

Labels: `board`, `type` (empty = untyped)

Number of tasks per board and task type. Separate from `tasks_by_stage` on
purpose: board × stage × type would multiply series for a question nobody asks
of a metric ("how many bugfixes are in review?" is a board filter), while "how
much of the backlog is bugs" is exactly what this answers.

```promql
# Share of work by type on the default board
octopus_tasks_by_type{board="default"}

# Everything still untyped, across flows
sum(octopus_tasks_by_type{type=""})
```

---

### 5. `octopus_agent_runs_total`

Type: **Counter**

Labels: `agent_type`, `stop_reason`

Total agent container runs by agent type and exit reason. `stop_reason` values include `success`, `error_max_turns`, `error_exit`, `stopped`.

```promql
# Max-turns rate by agent type
rate(octopus_agent_runs_total{stop_reason="error_max_turns"}[1h])
```

Called via `metrics.RecordAgentRun(agentType, stopReason)` from the task dispatcher's exit handler.

---

### 6. `octopus_tokens_consumed_total`

Type: **Counter**

Labels: `model`, `kind`

`kind` values: `input`, `output`, `cache_read`, `cache_creation`

Total LLM tokens consumed, broken down by model and token type. Updated at end of each agent run via `metrics.RecordTokenUsage(...)`.

```promql
# Total output tokens per model in the last hour
sum by (model) (
  rate(octopus_tokens_consumed_total{kind="output"}[1h])
) * 3600
```

---

### 7. `octopus_cost_usd_total`

Type: **Counter**

Labels: `model`

Estimated cumulative cost in USD per model. Updated alongside token metrics.

```promql
# Daily cost estimate
sum(increase(octopus_cost_usd_total[24h]))
```

---

### 8. `octopus_auth_events_total`

Type: **Counter**

Labels: `kind`, `result`

`kind` values: `register` (account provisioned on first federated sign-in), `logout`, `sso`, `api_key_created`, `api_key_revoked`

`result` values: `ok`, `fail`

```promql
# Failed SSO round-trips in last 5 minutes (misconfigured IdP, or probing)
rate(octopus_auth_events_total{kind="sso",result="fail"}[5m]) * 300
```

Called via `metrics.RecordAuthEvent(kind, result)` from auth handlers.

---

## Go runtime + process metrics

The registry also includes:
- `go_goroutines`, `go_gc_duration_seconds`, `go_memstats_*` (Go collector)
- `process_cpu_seconds_total`, `process_resident_memory_bytes`, `process_open_fds` (process collector)

---

## Grafana setup tips

1. Add Prometheus data source pointing to `http://<octopus-host>:8585/metrics`
2. Suggested panels:

| Panel | Query |
|-------|-------|
| Request rate | `rate(octopus_http_requests_total[1m])` |
| Error rate | `rate(octopus_http_requests_total{status=~"5.."}[1m])` |
| p95 latency | `histogram_quantile(0.95, rate(octopus_http_request_duration_seconds_bucket[5m]))` |
| Active sandboxes | `octopus_sandboxes_active` |
| Tasks by stage | `octopus_tasks_by_stage` |
| Tasks by type | `octopus_tasks_by_type` |
| Daily cost | `sum(increase(octopus_cost_usd_total[24h]))` |
| Token throughput | `rate(octopus_tokens_consumed_total[5m])` |
| Auth failures | `rate(octopus_auth_events_total{result="fail"}[5m])` |

3. Set scrape interval to `15s` in `prometheus.yml`:

```yaml
scrape_configs:
  - job_name: octopus
    static_configs:
      - targets: ['octopus:8585']
    scrape_interval: 15s
```

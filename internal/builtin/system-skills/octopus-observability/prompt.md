# A project's observability

Thulu shows how each environment of a project is doing in two places: the
fixed panel, with nine signals that are the same for every service, and the
dashboards of the app's own metrics. In both, you propose and a person approves
on the project page. No proposal collects or shows anything until then, and
there is no tool to approve.

This skill covers the `octopus-api` tools. To read the cloud (inventory,
metrics, logs) there is `thulu-cloud` with its own manual.

What you do not do here: instrument the app or touch infrastructure. If
telemetry is missing, that is another task (see [When telemetry is
missing](#when-telemetry-is-missing)).

## Getting started

1. `environment_list(project_id)`: the `environment_id`, the `provider`,
   whether it has a cloud credential (`has_credential`) and the
   `ingest_endpoint`.
2. `environment_mapping_list` and `dashboard_list`: what already exists and in
   what state. Proposing on top without looking duplicates the work of whoever
   reviews.

## The fixed panel: mappings

The nine signals: `latency_p50`, `latency_p95`, `error_rate`, `throughput`,
`cpu`, `memory`, `saturation`, `restarts`, `cost_daily`. `saturation` counts
what a hard limit already rejected (a throttled Lambda, a full queue): it is
the "running out of room" signal where `cpu` means nothing.

A mapping says where a signal of a service comes from in that environment.

1. With `thulu-cloud`, `discover_resources(environment_id)` returns the
   inventory and, per resource, a `suggested` block with the source of each
   signal.
2. Decide which resource belongs to this project in this environment by
   reading the repo: Terraform, compose, the deploy workflow. If you are torn
   between two, say so in the task instead of picking one.
3. Propose the whole set at once:

```
environment_mapping_propose(project_id=..., environment_id=..., task_id=$TASK_ID,
  mappings=[
    { service: "api", signal: "latency_p95",
      source: <the suggested of latency_p95, whole, with unit and scale> },
    { service: "api", signal: "cpu", not_applicable: true,
      note: "Lambda: there is no CPU to read. Saturation comes from Throttles." },
    ...
  ])
```

- Copy `suggested` whole. Without `unit` and `scale`, a latency in nanoseconds
  shows up as a reasonable-looking number that is a thousand times too large.
- A signal the architecture cannot produce goes with `not_applicable: true`
  and a `note` that says why. Leaving it out leaves an empty tile that nobody
  can tell apart from a broken collector.
- If `discover_resources` could not look (a permission is missing), that is
  not `not_applicable`: it is a permission that has to be requested.
- The batch is validated as a whole: if one mapping is wrong, none is saved
  and the error says which one.
- `task_id` always: it adds to the task a review approval linked to the
  proposal, which closes when the person approves.
- On approval, each proposal becomes `active` and the one it replaces becomes
  `disabled`.
- Only `aws` and `gcp` are collected. A `prometheus` or `other` environment
  accepts mappings, but nothing is read.
- If the app sends OTLP traces to Thulu, `latency_*`, `error_rate` and
  `throughput` come from there, and `cpu` and `memory` from its metrics, with
  no mapping.

## Dashboards of the app's own metrics

A metric the app sends over OTLP that is not one of the nine registers itself
as `discovered`. It is only charted when a person approves it (`approved`).

1. `custom_metric_list`: each metric with its `kind` (`counter` or `gauge`),
   its labels and its status. If the one you are asked for is `discovered`,
   ask a person to approve it; if it does not show up, the app is not sending
   it, and that is what you report.
2. `chart_types_list`: the closed catalog and its rules.
3. `dashboard_propose` with `task_id`.
4. `dashboard_data` on what you proposed, to see that the panels show numbers
   and not gaps. It works before approval.

| Type | Series | `group_by` | For |
|---|---|---|---|
| `line` | 1 to 4 | optional | change over time |
| `area` | 1 | required, counters only | how a total splits over time |
| `bar` | 1 | required | comparing values of a label |
| `stat` | 1 | forbidden | one number with its trend |
| `table` | 1 to 6 | required | several measures per value of a label |

- Counter: `sum`, `avg`, `max`, `min`. Gauge: `avg`, `max`, `min`, `last`.
  Summing gauges ("total p95 of three services") gives a meaningless number
  that looks fine.
- Each series names `metric`, `service` and `agg`. `group_by` and the keys of
  `where` have to be labels of that metric.
- Up to 12 panels per dashboard. `range_hours` goes up to 2160 (the 90 days
  that are kept; 24 by default). `width`: 1 is half a row, 2 the full row.
- A label with more than 20 values, or a panel with more than 12 groups, folds
  the rest into `other`. If `other` weighs a lot, group by another label or
  filter with `where`.
- To change an active dashboard, propose one with the same `name`: on
  approval, the previous one is archived.
- If a panel looks odd, `custom_metric_series(metric_id)` gives the raw
  points. It takes the row's id, not the name.

## When telemetry is missing

If a signal or a metric does not arrive, the deliverable is saying so
precisely: what is missing, in which service, and what the app would have to
send. Instrumenting is production code and deserves its own task
(`task_create`).

That task needs two pieces of data:

- **Where to send:** `OTEL_EXPORTER_OTLP_ENDPOINT` = the environment's
  `ingest_endpoint`. The SDK appends `/v1/traces` and `/v1/metrics` on its
  own. If it comes back empty, the Thulu operator has not configured the
  public URL yet.
- **With which token:** an admin generates it in the environment. In the code
  and in the config it goes as a reference to a secret
  (`OTEL_EXPORTER_OTLP_HEADERS=X-Octopus-Token=${OCTOPUS_INGEST_TOKEN}`), never
  the value. Thulu also accepts `Authorization: Bearer`, for proxies that
  only forward that header.

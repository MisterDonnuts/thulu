# thulu-cloud: reading the deployed infrastructure

A **read-only** window onto the cloud where the project runs. You can't write
anything with this tool: the server defines no write tool at all.

Credentials are resolved server-side per environment. **You will never see a
key**, and don't ask for one: the design is like this on purpose, because you
also read logs, which are text anyone could have written.

## The right order

1. `environments_list(project_id)` → get the `environment_id` of the environment
   you care about (look at `is_production`).
2. `discover_resources(environment_id)` → what exists in that account.
3. Only then, `metrics_query` / `error_groups` / `logs_search`.

Don't guess `environment_id`: if you ask for one that doesn't belong to a
project of your task, the call fails (and it's right that it fails).

## `discover_resources` proposes, it doesn't decide

It returns the inventory and, per resource, a `suggested` block with the metric
sources that resource type knows how to serve. That's the deterministic part.

**Your part is the matching:** an account has 5 load balancers; which one is
*this* project's in prod? That isn't in the cloud, it's in the repo. Read the
Terraform, the `docker-compose`, the deploy workflow, the `serverless.yml`.
That's where the service name and the environment tag are.

If the matching is ambiguous, **say so**. A wrong mapping is worse than a
missing one: nobody audits a chart that looks reasonable.

`discover_resources` also returns `gaps`: what it did **not** find. Report it
just like what it found: "there is no tracing" is a finding, not a silence.

**Tell absence apart from blindness.** A `gap` that starts with `could not
enumerate <kind>:` doesn't say the resource doesn't exist: it says we couldn't
look (a permission is missing, the service is disabled, the region is a
different one). That does **not** justify a `not_applicable`; it justifies
asking for the permission. The same errors come separately in `probe_errors` if
you want the detail. Only the other `gaps` claim absence, and we only emit them
when the query answered.

## Look at the `provider` before writing a query

`aws` and `gcp` can both be read, but **they don't speak the same language**.
If you copy the `suggested` block from `discover_resources` as is, this is
already solved; it only matters if you write a source by hand.

| | AWS | GCP |
|---|---|---|
| Metric source | `{namespace, metric_name, dimensions, stat}` | `{metric_type, filter, aligner, reducer}` |
| `logs_search` takes | a Logs Insights query | a Cloud Logging filter |
| Error grouping | regex over the logs | Error Reporting (falling back to logs) |
| `deploys` | not connected yet | Cloud Run revisions, real |

**Units already come normalized**: latency in **ms** and utilization in
**percent (0–100)**, wherever they come from. GCP reports utilization as a 0–1
ratio and Cloud Functions measures in nanoseconds; each source's `scale`
corrects it. That's why you should **copy `suggested` whole, with `unit` and
`scale`**: if you drop them, the panel shows a number a thousand times larger
and it looks reasonable.

On GCP there are two things worth knowing up front:

- **`cost_daily` can't be read through the API.** Billing detail lives in a
  BigQuery export the account owner has to enable. Leave the signal unmapped and say
  so.
- **`restarts` only exists on GKE.** Cloud Run doesn't expose it.

## `error_groups` is the most valuable

Exceptions grouped by message with counts and first/last seen. A group with a
recent `first_seen` and a growing count is a regression that points at specific
code, which is exactly what you can fix.

Compare against the window of the last deploy before concluding.

## Cost and windows

Every call costs **the account owner** money. Logs Insights charges per GB scanned,
so a wide window over logs is genuinely expensive.

- Start narrow (1–2 h) and widen only if needed.
- There is a range cap per call; if it rejects you, don't fight it, split the
  query.
- Output is capped. If you see `truncated`, **narrow the window**, don't raise
  the limit.
- If a response carries `partial: true`, the query didn't finish: you have
  incomplete data and you have to say so.

## What this tool doesn't do

- **It doesn't restart, scale or apply anything.** Read-only.
- **`deploys` on AWS isn't connected** to CodeDeploy yet: it returns
  `unavailable: true`. Meanwhile, Thulu knows when each PR was merged; use
  that as an approximation of the deploy time. On GCP it does return real Cloud
  Run revisions (GKE and Cloud Functions not yet).
- **Only `aws` and `gcp`.** A `prometheus` or `other` environment will reject
  the call with a clear message, and nothing is collected from it.

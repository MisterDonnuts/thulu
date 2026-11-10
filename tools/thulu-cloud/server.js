// Cloud credentials are resolved here and must never reach the agent: it reads third-party-written
// logs,
// so a credential in its context is one injected log line from exfiltration.

import { AsyncLocalStorage } from "node:async_hooks";
import { timingSafeEqual } from "node:crypto";

import express from "express";
import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { SSEServerTransport } from "@modelcontextprotocol/sdk/server/sse.js";
import {
  ListToolsRequestSchema,
  CallToolRequestSchema,
} from "@modelcontextprotocol/sdk/types.js";
import {
  CloudWatchClient,
  GetMetricDataCommand,
} from "@aws-sdk/client-cloudwatch";
import {
  CloudWatchLogsClient,
  StartQueryCommand,
  GetQueryResultsCommand,
  DescribeLogGroupsCommand,
} from "@aws-sdk/client-cloudwatch-logs";
import {
  ElasticLoadBalancingV2Client,
  DescribeLoadBalancersCommand,
  DescribeTargetGroupsCommand,
} from "@aws-sdk/client-elastic-load-balancing-v2";
import { ECSClient, ListClustersCommand, ListServicesCommand } from "@aws-sdk/client-ecs";
import { LambdaClient, ListFunctionsCommand } from "@aws-sdk/client-lambda";
import { RDSClient, DescribeDBInstancesCommand } from "@aws-sdk/client-rds";
import { ApiGatewayV2Client, GetApisCommand } from "@aws-sdk/client-apigatewayv2";
import { APIGatewayClient, GetRestApisCommand } from "@aws-sdk/client-api-gateway";
import { JWT } from "google-auth-library";

import { t, tLog } from "./i18n.js";

const PORT = Number(process.env.PORT || 3000);
const OCTOPUS_API = process.env.OCTOPUS_API || "http://host.docker.internal:8585";
// Daemon calls use the caller's run-scoped key from the SSE handshake (set by
// images/snippets/mcp-json.js);
// this server holds no key of its own.
const callerKey = new AsyncLocalStorage();
const sessionKeys = new Map();

function keyFromRequest(req) {
  const direct = req.headers["x-api-key"];
  if (typeof direct === "string" && direct.trim()) return direct.trim();
  const bearer = req.headers.authorization;
  if (typeof bearer === "string" && bearer.startsWith("Bearer ")) {
    // i18n-ignore HTTP auth scheme, not a message
    return bearer.slice("Bearer ".length).trim();
  }
  return "";
}

function sameKey(a, b) {
  if (!a || !b) return false;
  const x = Buffer.from(String(a));
  const y = Buffer.from(String(b));
  return x.length === y.length && timingSafeEqual(x, y);
}

function octopusAuthHeaders() {
  const key = callerKey.getStore() || "";
  if (!key) {
    throw new Error(t("errors.no_credential"));
  }
  return { "X-API-Key": key };
}

const LOG_CAP_BYTES = Number(process.env.LOG_CAP_BYTES || 60 * 1024);
const MAX_LOG_ROWS = Number(process.env.MAX_LOG_ROWS || 200);
const MAX_RANGE_HOURS = Number(process.env.MAX_RANGE_HOURS || 24 * 31);
const QUERY_POLL_MS = 700;
const QUERY_TIMEOUT_MS = Number(process.env.QUERY_TIMEOUT_MS || 25_000);

function ok(value) {
  return {
    content: [
      { type: "text", text: typeof value === "string" ? value : JSON.stringify(value, null, 2) },
    ],
  };
}

function clip(str, cap, label) {
  if (str.length <= cap) return str;
  return `${str.slice(0, cap)}\n${t("text.truncated", { label, bytes: str.length - cap })}`;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function callOctopus(path) {
  let r;
  try {
    r = await fetch(`${OCTOPUS_API}${path}`, { headers: octopusAuthHeaders() });
  } catch (err) {
    throw new Error(t("errors.octopus_unreachable", { url: OCTOPUS_API, err: err?.message ?? err }));
  }
  const text = await r.text();
  if (!r.ok) {
    throw new Error(t("errors.octopus_http", { path, status: r.status, body: text.trim() }));
  }
  try {
    return JSON.parse(text);
  } catch {
    return { raw: text };
  }
}

// The daemon is the authorization boundary: it derives reach from the caller's key and refuses
// environments
// outside it. The task check below is defense in depth only.

// Caches must be keyed by the caller's key: a daemon answer is only that key's, and an env-only key
// would
// serve another workspace (or a keyless /collect) what the daemon granted someone else.
function callerCacheKey(...parts) {
  return JSON.stringify([callerKey.getStore() || "", ...parts]);
}

const envCache = new Map();
const ENV_TTL_MS = 45_000;

async function environmentMeta(envId) {
  const key = callerCacheKey(envId);
  const hit = envCache.get(key);
  if (hit && Date.now() - hit.at < ENV_TTL_MS) return hit.value;
  const value = await callOctopus(`/internal/environments/${encodeURIComponent(envId)}`);
  envCache.set(key, { at: Date.now(), value });
  return value;
}

// Keyed per tool too: policy can allow one tool and refuse another on the same environment.
const credCache = new Map();
const CRED_TTL_MS = 45_000;

async function credentialFor(envId, tool) {
  const key = callerCacheKey(envId, tool);
  const hit = credCache.get(key);
  if (hit && Date.now() - hit.at < CRED_TTL_MS) return hit.value;
  const qs = new URLSearchParams({
    server: "thulu-cloud",
    tool,
    environment_id: envId,
  });
  let value;
  try {
    value = await callOctopus(`/internal/credentials/resolve?${qs}`);
  } catch (err) {
    // Surface the daemon's 403 decision wording as-is; its "no credential" case is deliberately
    // vague so it
    // cannot be used to probe other tenants' environments.
    throw new Error(explainCredentialFailure(err, envId, tool));
  }
  credCache.set(key, { at: Date.now(), value });
  return value;
}

function explainCredentialFailure(err, envId, tool) {
  const msg = String(err?.message ?? err);
  const body = msg.slice(msg.indexOf("{"));
  try {
    const detail = JSON.parse(body)?.decision?.detail;
    if (detail) return t("errors.credential_refused", { tool, environment: envId, detail });
  } catch {
    // Not a decision payload — fall through to the transport error.
  }
  return msg;
}

const taskProjectsCache = new Map();
const TASK_TTL_MS = 60_000;

async function projectsForTask(taskId) {
  const key = callerCacheKey(taskId);
  const hit = taskProjectsCache.get(key);
  if (hit && Date.now() - hit.at < TASK_TTL_MS) return hit.ids;
  const task = await callOctopus(`/tasks/${encodeURIComponent(taskId)}`);
  const ids = Array.isArray(task.project_ids) ? task.project_ids : [];
  taskProjectsCache.set(key, { at: Date.now(), ids });
  return ids;
}

async function authorizedEnvironment(envId, ctx, tool) {
  if (!envId) throw new Error(t("errors.environment_id_required"));
  const env = await environmentMeta(envId);
  if (ctx?.taskId) {
    const allowed = await projectsForTask(ctx.taskId);
    if (!allowed.includes(env.project_id)) {
      throw new Error(
        t("errors.environment_not_linked", {
          environment: envId,
          project: env.project_id,
          task: ctx.taskId,
        }),
      );
    }
  }
  return withCredential(env, await credentialFor(envId, tool));
}

function withCredential(env, resolved) {
  if (!resolved?.payload) {
    throw new Error(t("errors.no_credential_granted", { environment: env.environment_id }));
  }
  if (resolved.provider && env.provider && resolved.provider !== env.provider) {
    throw new Error(
      t("errors.credential_provider_mismatch", {
        credential: resolved.name,
        credential_provider: resolved.provider,
        environment: env.environment_id,
        provider: env.provider,
      }),
    );
  }
  return { ...env, kind: resolved.kind, payload: resolved.payload, credential_name: resolved.name };
}

function awsConfig(env) {
  const cfg = { region: env.region || "us-east-1" };
  const p = env.payload || {};
  if (env.kind === "aws_keys") {
    if (!p.access_key_id || !p.secret_access_key) {
      throw new Error(t("errors.aws_keys_incomplete"));
    }
    cfg.credentials = {
      accessKeyId: p.access_key_id,
      secretAccessKey: p.secret_access_key,
      ...(p.session_token ? { sessionToken: p.session_token } : {}),
    };
  } else if (env.kind === "aws_role") {
    // Deliberately unsupported: falling back to the default provider chain would query the pod's
    // account, not the monitored one.
    throw new Error(t("errors.aws_role_unsupported"));
  } else {
    throw new Error(
      t("errors.credential_kind_not_aws", { environment: env.environment_id, kind: env.kind }),
    );
  }
  return cfg;
}

function requireAWS(env) {
  if (env.provider !== "aws") {
    throw new Error(t("errors.aws_only", { provider: env.provider }));
  }
  return awsConfig(env);
}

const SUPPORTED = new Set(["aws", "gcp"]);

function requireSupported(env, tool) {
  if (!SUPPORTED.has(env.provider)) {
    throw new Error(
      t("errors.provider_unsupported", {
        tool,
        providers: [...SUPPORTED].join(", "),
        environment: env.environment_id,
        provider: env.provider,
      }),
    );
  }
  return env.provider;
}

// Must match the scope the daemon validates with (internal/credentials/provider_gcp.go). Read-only
// is enforced by
// the IAM role and internal/credentials/catalog.go; Google has no read-only scope for Error
// Reporting, Cloud SQL Admin or Cloud Functions.
const GCP_SCOPES = ["https://www.googleapis.com/auth/cloud-platform"];

const gcpClients = new Map();
const GCP_CLIENT_TTL_MS = 10 * 60 * 1000;

function parseServiceAccount(env) {
  const p = env.payload || {};
  if (env.kind !== "gcp_sa") {
    throw new Error(
      t("errors.credential_kind_not_gcp", { environment: env.environment_id, kind: env.kind }),
    );
  }
  let sa;
  const raw = p.service_account_json;
  if (raw) {
    try {
      sa = JSON.parse(raw);
    } catch (err) {
      throw new Error(t("errors.service_account_json_invalid", { err: err?.message ?? err }));
    }
  } else if (p.client_email && p.private_key) {
    sa = { client_email: p.client_email, private_key: p.private_key, project_id: p.project_id };
  } else {
    throw new Error(t("errors.gcp_sa_without_json"));
  }
  if (!sa.client_email || !sa.private_key) {
    throw new Error(t("errors.service_account_incomplete"));
  }
  // env.account wins: one service account can be granted several projects, and the key file names
  // only its own.
  const projectId = env.account || sa.project_id;
  if (!projectId) {
    throw new Error(t("errors.gcp_project_unknown"));
  }
  return { sa, projectId };
}

function gcpClient(env) {
  const hit = gcpClients.get(env.environment_id);
  if (hit && Date.now() - hit.at < GCP_CLIENT_TTL_MS) return hit;
  const { sa, projectId } = parseServiceAccount(env);
  const client = new JWT({
    email: sa.client_email,
    key: sa.private_key,
    scopes: GCP_SCOPES,
  });
  const entry = { at: Date.now(), client, projectId };
  gcpClients.set(env.environment_id, entry);
  return entry;
}

async function gcpFetch(env, url, { method = "GET", body } = {}) {
  const { client } = gcpClient(env);
  let headers;
  try {
    const got = await client.getRequestHeaders(url);
    // google-auth-library 9.x returns a plain object, 10.x a WHATWG Headers that spreads to {} and
    // drops
    // Authorization, turning every call into a misleading 401.
    headers = typeof got?.forEach === "function" && typeof got?.get === "function"
      ? Object.fromEntries(got.entries())
      : { ...got };
  } catch (err) {
    throw new Error(t("errors.gcp_auth_failed", { err: err?.message ?? err }));
  }
  const init = { method, headers: { ...headers, Accept: "application/json" } };
  if (body !== undefined) {
    init.headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }
  let r;
  try {
    r = await fetch(url, init);
  } catch (err) {
    throw new Error(t("errors.host_unreachable", { host: new URL(url).host, err: err?.message ?? err }));
  }
  const text = await r.text();
  let parsed;
  try {
    parsed = text ? JSON.parse(text) : {};
  } catch {
    parsed = { raw: text };
  }
  if (!r.ok) {
    const msg = parsed?.error?.message || text.trim() || `HTTP ${r.status}`;
    const status = parsed?.error?.status ? ` [${parsed.error.status}]` : "";
    throw new Error(
      t("errors.google_http", { path: new URL(url).pathname, status: r.status, detail: status, msg }),
    );
  }
  return parsed;
}

function parseRange(args) {
  const to = args.to ? new Date(args.to) : new Date();
  const from = args.from ? new Date(args.from) : new Date(to.getTime() - 60 * 60 * 1000);
  if (isNaN(from.getTime()) || isNaN(to.getTime())) {
    throw new Error(t("errors.range_not_rfc3339"));
  }
  if (to <= from) throw new Error(t("errors.range_inverted"));
  const hours = (to - from) / 3_600_000;
  if (hours > MAX_RANGE_HOURS) {
    throw new Error(t("errors.range_too_wide", { hours: Math.round(hours), max: MAX_RANGE_HOURS }));
  }
  return { from, to };
}

const SIGNAL_SOURCES = {
  alb: (dim) => ({
    // TargetResponseTime is in seconds; scale to ms so every latency source shares one unit.
    latency_p50: { namespace: "AWS/ApplicationELB", metric_name: "TargetResponseTime", dimensions: dim, stat: "p50", unit: "ms", scale: 1000 },
    latency_p95: { namespace: "AWS/ApplicationELB", metric_name: "TargetResponseTime", dimensions: dim, stat: "p95", unit: "ms", scale: 1000 },
    error_rate: { namespace: "AWS/ApplicationELB", metric_name: "HTTPCode_Target_5XX_Count", dimensions: dim, stat: "Sum", unit: "count" }, // i18n-ignore CloudWatch metric and statistic names
    throughput: { namespace: "AWS/ApplicationELB", metric_name: "RequestCount", dimensions: dim, stat: "Sum", unit: "count" }, // i18n-ignore CloudWatch metric and statistic names
  }),
  ecs: (dim) => ({
    cpu: { namespace: "AWS/ECS", metric_name: "CPUUtilization", dimensions: dim, stat: "Average", unit: "percent" }, // i18n-ignore CloudWatch metric and statistic names
    memory: { namespace: "AWS/ECS", metric_name: "MemoryUtilization", dimensions: dim, stat: "Average", unit: "percent" }, // i18n-ignore CloudWatch metric and statistic names
  }),
  lambda: (dim) => ({
    latency_p95: { namespace: "AWS/Lambda", metric_name: "Duration", dimensions: dim, stat: "p95", unit: "ms" }, // i18n-ignore CloudWatch metric and statistic names
    error_rate: { namespace: "AWS/Lambda", metric_name: "Errors", dimensions: dim, stat: "Sum", unit: "count" }, // i18n-ignore CloudWatch metric and statistic names
    throughput: { namespace: "AWS/Lambda", metric_name: "Invocations", dimensions: dim, stat: "Sum", unit: "count" }, // i18n-ignore CloudWatch metric and statistic names
    saturation: { namespace: "AWS/Lambda", metric_name: "Throttles", dimensions: dim, stat: "Sum", unit: "count" }, // i18n-ignore CloudWatch metric and statistic names
  }),
  apigateway_http: (apiID) => {
    const dim = { ApiId: apiID };
    return {
      latency_p50: { namespace: "AWS/ApiGateway", metric_name: "Latency", dimensions: dim, stat: "p50", unit: "ms" }, // i18n-ignore CloudWatch metric and statistic names
      latency_p95: { namespace: "AWS/ApiGateway", metric_name: "Latency", dimensions: dim, stat: "p95", unit: "ms" }, // i18n-ignore CloudWatch metric and statistic names
      error_rate: { namespace: "AWS/ApiGateway", metric_name: "5xx", dimensions: dim, stat: "Sum", unit: "count" }, // i18n-ignore CloudWatch metric and statistic names
      throughput: { namespace: "AWS/ApiGateway", metric_name: "Count", dimensions: dim, stat: "Sum", unit: "count" }, // i18n-ignore CloudWatch metric and statistic names
    };
  },
  // REST APIs (v1) use dimension ApiName and metric 5XXError; the HTTP API spelling yields a
  // permanently empty chart.
  apigateway_rest: (apiName) => {
    const dim = { ApiName: apiName };
    return {
      latency_p50: { namespace: "AWS/ApiGateway", metric_name: "Latency", dimensions: dim, stat: "p50", unit: "ms" }, // i18n-ignore CloudWatch metric and statistic names
      latency_p95: { namespace: "AWS/ApiGateway", metric_name: "Latency", dimensions: dim, stat: "p95", unit: "ms" }, // i18n-ignore CloudWatch metric and statistic names
      error_rate: { namespace: "AWS/ApiGateway", metric_name: "5XXError", dimensions: dim, stat: "Sum", unit: "count" }, // i18n-ignore CloudWatch metric and statistic names
      throughput: { namespace: "AWS/ApiGateway", metric_name: "Count", dimensions: dim, stat: "Sum", unit: "count" }, // i18n-ignore CloudWatch metric and statistic names
    };
  },
  rds: (dim) => ({
    cpu: { namespace: "AWS/RDS", metric_name: "CPUUtilization", dimensions: dim, stat: "Average", unit: "percent" }, // i18n-ignore CloudWatch metric and statistic names
    memory: { namespace: "AWS/RDS", metric_name: "FreeableMemory", dimensions: dim, stat: "Average", unit: "bytes" }, // i18n-ignore CloudWatch metric and statistic names
  }),
};

function dimsToList(dimensions) {
  return Object.entries(dimensions || {}).map(([Name, Value]) => ({ Name, Value: String(Value) }));
}

// GCP reports utilization as a 0..1 ratio and Cloud Functions time in ns, hence scale.
// The 5xx class label is "5xx" on Cloud Run but "500" on the HTTPS load balancer.
const GCP_SIGNAL_SOURCES = {
  cloud_run: (service) => {
    const base = `resource.labels.service_name="${service}"`;
    return {
      latency_p50: { metric_type: "run.googleapis.com/request_latencies", filter: base, aligner: "ALIGN_PERCENTILE_50", reducer: "REDUCE_MEAN", unit: "ms", scale: 1 },
      latency_p95: { metric_type: "run.googleapis.com/request_latencies", filter: base, aligner: "ALIGN_PERCENTILE_95", reducer: "REDUCE_MEAN", unit: "ms", scale: 1 },
      error_rate: { metric_type: "run.googleapis.com/request_count", filter: `${base} AND metric.labels.response_code_class="5xx"`, aligner: "ALIGN_DELTA", reducer: "REDUCE_SUM", unit: "percent", scale: 100, divide_by: { metric_type: "run.googleapis.com/request_count", filter: base } },
      throughput: { metric_type: "run.googleapis.com/request_count", filter: base, aligner: "ALIGN_DELTA", reducer: "REDUCE_SUM", unit: "count", scale: 1 },
      cpu: { metric_type: "run.googleapis.com/container/cpu/utilizations", filter: base, aligner: "ALIGN_PERCENTILE_95", reducer: "REDUCE_MEAN", unit: "percent", scale: 100 },
      memory: { metric_type: "run.googleapis.com/container/memory/utilizations", filter: base, aligner: "ALIGN_PERCENTILE_95", reducer: "REDUCE_MEAN", unit: "percent", scale: 100 },
    };
  },
  https_lb: (urlMap) => {
    const base = `resource.labels.url_map_name="${urlMap}"`;
    return {
      latency_p50: { metric_type: "loadbalancing.googleapis.com/https/total_latencies", filter: base, aligner: "ALIGN_PERCENTILE_50", reducer: "REDUCE_MEAN", unit: "ms", scale: 1 },
      latency_p95: { metric_type: "loadbalancing.googleapis.com/https/total_latencies", filter: base, aligner: "ALIGN_PERCENTILE_95", reducer: "REDUCE_MEAN", unit: "ms", scale: 1 },
      error_rate: { metric_type: "loadbalancing.googleapis.com/https/request_count", filter: `${base} AND metric.labels.response_code_class="500"`, aligner: "ALIGN_DELTA", reducer: "REDUCE_SUM", unit: "percent", scale: 100, divide_by: { metric_type: "loadbalancing.googleapis.com/https/request_count", filter: base } },
      throughput: { metric_type: "loadbalancing.googleapis.com/https/request_count", filter: base, aligner: "ALIGN_DELTA", reducer: "REDUCE_SUM", unit: "count", scale: 1 },
    };
  },
  gke_container: (cluster, namespace, container) => {
    const parts = [`resource.labels.cluster_name="${cluster}"`];
    if (namespace) parts.push(`resource.labels.namespace_name="${namespace}"`);
    if (container) parts.push(`resource.labels.container_name="${container}"`);
    const base = parts.join(" AND ");
    return {
      cpu: { metric_type: "kubernetes.io/container/cpu/limit_utilization", filter: base, aligner: "ALIGN_MEAN", reducer: "REDUCE_MEAN", unit: "percent", scale: 100 },
      memory: { metric_type: "kubernetes.io/container/memory/limit_utilization", filter: base, aligner: "ALIGN_MEAN", reducer: "REDUCE_MEAN", unit: "percent", scale: 100 },
      restarts: { metric_type: "kubernetes.io/container/restart_count", filter: base, aligner: "ALIGN_DELTA", reducer: "REDUCE_SUM", unit: "count", scale: 1 },
    };
  },
  cloudsql: (databaseID) => {
    const base = `resource.labels.database_id="${databaseID}"`;
    return {
      cpu: { metric_type: "cloudsql.googleapis.com/database/cpu/utilization", filter: base, aligner: "ALIGN_MEAN", reducer: "REDUCE_MEAN", unit: "percent", scale: 100 },
      // Not database/memory/utilization: on Postgres it counts page cache and reads 1.0 even while
      // total_usage is far below the quota.
      memory: { metric_type: "cloudsql.googleapis.com/database/memory/total_usage", filter: base, aligner: "ALIGN_MEAN", reducer: "REDUCE_MEAN", unit: "percent", scale: 100, divide_by: { metric_type: "cloudsql.googleapis.com/database/memory/quota" } },
    };
  },
  cloud_function: (name) => {
    const base = `resource.labels.function_name="${name}"`;
    return {
      latency_p95: { metric_type: "cloudfunctions.googleapis.com/function/execution_times", filter: base, aligner: "ALIGN_PERCENTILE_95", reducer: "REDUCE_MEAN", unit: "ms", scale: 1e-6 },
      error_rate: { metric_type: "cloudfunctions.googleapis.com/function/execution_count", filter: `${base} AND metric.labels.status!="ok"`, aligner: "ALIGN_DELTA", reducer: "REDUCE_SUM", unit: "count", scale: 1 },
      throughput: { metric_type: "cloudfunctions.googleapis.com/function/execution_count", filter: base, aligner: "ALIGN_DELTA", reducer: "REDUCE_SUM", unit: "count", scale: 1 },
    };
  },
};

function applyScale(value, source) {
  if (value === null || value === undefined) return null;
  const k = Number(source?.scale);
  return Number.isFinite(k) && k !== 0 ? value * k : value;
}

async function fetchMetricSeries(env, specs, from, to, periodSeconds) {
  const cw = new CloudWatchClient(requireAWS(env));
  const queries = specs.map((s, i) => ({
    Id: `q${i}`,
    MetricStat: {
      Metric: {
        Namespace: s.source.namespace,
        MetricName: s.source.metric_name,
        Dimensions: dimsToList(s.source.dimensions),
      },
      Period: periodSeconds,
      Stat: s.source.stat || "Average", // i18n-ignore CloudWatch statistic name
    },
    ReturnData: true,
  }));

  const out = await cw.send(
    new GetMetricDataCommand({
      StartTime: from,
      EndTime: to,
      MetricDataQueries: queries,
      ScanBy: "TimestampAscending",
    }),
  );

  const byId = new Map((out.MetricDataResults || []).map((r) => [r.Id, r]));
  return specs.map((s, i) => {
    const r = byId.get(`q${i}`);
    const points = (r?.Timestamps || []).map((ts, j) => ({
      at: new Date(ts).toISOString(),
      value: applyScale(r.Values?.[j] ?? null, s.source),
    }));
    return { ...s, points, status: r?.StatusCode || "Unknown" }; // i18n-ignore CloudWatch status code
  });
}

function gcpPointValue(v) {
  if (!v) return null;
  if (v.doubleValue !== undefined) return Number(v.doubleValue);
  if (v.int64Value !== undefined) return Number(v.int64Value);
  if (v.boolValue !== undefined) return v.boolValue ? 1 : 0;
  if (v.distributionValue) {
    const d = v.distributionValue;
    const count = Number(d.count || 0);
    if (!count) return null;
    return Number(d.mean ?? 0);
  }
  return null;
}

function gcpMetricFilter(source) {
  if (!source?.metric_type) throw new Error(t("errors.gcp_metric_type_missing"));
  const extra = String(source.filter || "").trim();
  return `metric.type="${source.metric_type}"${extra ? ` AND ${extra}` : ""}`;
}

async function gcpRawPoints(env, projectId, source, from, to, periodSeconds) {
  const params = new URLSearchParams({
    filter: gcpMetricFilter(source),
    "interval.startTime": from.toISOString(),
    "interval.endTime": to.toISOString(),
    "aggregation.alignmentPeriod": `${periodSeconds}s`,
    "aggregation.perSeriesAligner": source.aligner || "ALIGN_MEAN",
    "aggregation.crossSeriesReducer": source.reducer || "REDUCE_MEAN",
    view: "FULL",
  });
  const url = `https://monitoring.googleapis.com/v3/projects/${encodeURIComponent(projectId)}/timeSeries?${params}`;
  const body = await gcpFetch(env, url);
  const ts = body.timeSeries || [];
  const points = (ts[0]?.points || [])
    .map((p) => ({
      at: new Date(p.interval?.endTime || p.interval?.startTime).toISOString(),
      value: gcpPointValue(p.value),
    }))
    .filter((p) => !isNaN(new Date(p.at).getTime()))
    // Monitoring returns newest-first; callers, including /collect's freshest-point pick, assume
    // ascending.
    .reverse();
  return { points, seriesMatched: ts.length };
}

function divideSeries(numer, denom) {
  const byTime = new Map(denom.map((p) => [p.at, p.value]));
  return numer
    .map((p) => {
      const d = byTime.get(p.at);
      if (d === null || d === undefined || d === 0 || p.value === null) return null;
      return { at: p.at, value: p.value / d };
    })
    .filter(Boolean);
}

async function fetchGCPSeries(env, specs, from, to, periodSeconds) {
  const { projectId } = gcpClient(env);
  const results = await Promise.all(
    specs.map(async (s) => {
      try {
        const { points: raw, seriesMatched } = await gcpRawPoints(
          env, projectId, s.source, from, to, periodSeconds,
        );
        let values = raw;
        const div = s.source.divide_by;
        if (div?.metric_type) {
          // Both queries must use the same from/to: Monitoring anchors alignment to the interval,
          // so buckets otherwise never line up.
          const { points: denom } = await gcpRawPoints(
            env, projectId, { filter: s.source.filter, ...div }, from, to, periodSeconds,
          );
          values = divideSeries(raw, denom);
          if (raw.length && !values.length) {
            // Must error, not NoData: the collector zero-fills counters with no answer, so a lost
            // denominator would draw a false 0%.
            throw new Error(t("errors.divide_by_empty", { metric: div.metric_type }));
          }
        }
        const points = values.map((p) => ({ at: p.at, value: applyScale(p.value, s.source) }));
        // i18n-ignore series status values, the same enum CloudWatch answers
        return { ...s, points, status: seriesMatched ? "Complete" : "NoData", series_matched: seriesMatched };
      } catch (err) {
        // i18n-ignore series status value, the same enum CloudWatch answers
        return { ...s, points: [], status: "Error", error: err?.message ?? String(err) };
      }
    }),
  );
  return results;
}

function collectSeries(env, specs, from, to, periodSeconds, tool) {
  return requireSupported(env, tool) === "gcp"
    ? fetchGCPSeries(env, specs, from, to, periodSeconds)
    : fetchMetricSeries(env, specs, from, to, periodSeconds);
}

function usableSource(provider, source) {
  if (!source) return false;
  return provider === "gcp" ? Boolean(source.metric_type) : Boolean(source.namespace && source.metric_name);
}

async function runInsightsQuery(env, logGroups, queryString, from, to, limit) {
  if (!logGroups.length) {
    throw new Error(t("errors.no_log_groups"));
  }
  const logs = new CloudWatchLogsClient(requireAWS(env));
  const started = await logs.send(
    new StartQueryCommand({
      logGroupNames: logGroups,
      startTime: Math.floor(from.getTime() / 1000),
      endTime: Math.floor(to.getTime() / 1000),
      queryString,
      limit,
    }),
  );
  const queryId = started.queryId;
  const deadline = Date.now() + QUERY_TIMEOUT_MS;

  for (;;) {
    const res = await logs.send(new GetQueryResultsCommand({ queryId }));
    if (res.status === "Complete") return res.results || [];
    if (res.status === "Failed" || res.status === "Cancelled") {
      throw new Error(t("errors.insights_query_ended", { status: res.status.toLowerCase() }));
    }
    if (Date.now() > deadline) {
      return {
        partial: true,
        reason: t("text.query_still_running", { status: res.status, ms: QUERY_TIMEOUT_MS }),
        results: res.results || [],
      };
    }
    await sleep(QUERY_POLL_MS);
  }
}

function rowsToObjects(results) {
  const rows = Array.isArray(results) ? results : results.results || [];
  return rows.map((row) => Object.fromEntries(row.map((f) => [f.field, f.value])));
}

function logGroupsFor(env, explicit) {
  if (explicit?.length) return explicit;
  const sel = env.selectors || {};
  return Object.entries(sel)
    .filter(([k]) => k === "log_group" || k.startsWith("log_group"))
    .map(([, v]) => v);
}

// A failed probe is not absence: only claim a kind is missing if its probe answered, or an IAM gap
// reads as
// a fact about the monitored stack.
function probeState(out) {
  out.resources = [];
  out.probe_errors = [];
  const failed = new Map();
  return {
    fail(kind, err) {
      const msg = String(err?.message || err);
      failed.set(kind, msg);
      out.probe_errors.push({ kind, error: msg });
    },
    found: (...kinds) => kinds.some((k) => out.resources.some((r) => r.kind === k)),
    unknown: (...kinds) => kinds.some((k) => failed.has(k)),
  };
}

// Probe failures go into gaps too: the skill tells the agent to read gaps only.
function unknownGaps(out) {
  return out.probe_errors.map(
    ({ kind, error }) =>
      t("text.gap_probe_failed", { kind, error }),
  );
}

async function discoverAWS(env) {
  const cfg = requireAWS(env);
  const out = { provider: "aws", region: cfg.region, account: env.account || "" };
  const probe = probeState(out);
  const note = (kind, err) => probe.fail(kind, err);

  try {
    const elb = new ElasticLoadBalancingV2Client(cfg);
    const lbs = await elb.send(new DescribeLoadBalancersCommand({}));
    const tgs = await elb.send(new DescribeTargetGroupsCommand({}));
    for (const lb of lbs.LoadBalancers || []) {
      const dimValue = (lb.LoadBalancerArn || "").split(":loadbalancer/")[1];
      const targets = (tgs.TargetGroups || [])
        .filter((tg) => (tg.LoadBalancerArns || []).includes(lb.LoadBalancerArn))
        .map((tg) => tg.TargetGroupName);
      out.resources.push({
        kind: "alb",
        name: lb.LoadBalancerName,
        arn: lb.LoadBalancerArn,
        dns: lb.DNSName,
        scheme: lb.Scheme,
        target_groups: targets,
        suggested: dimValue ? SIGNAL_SOURCES.alb({ LoadBalancer: dimValue }) : {},
      });
    }
  } catch (err) {
    note("alb", err);
  }

  try {
    const ecs = new ECSClient(cfg);
    const clusters = await ecs.send(new ListClustersCommand({}));
    for (const clusterArn of clusters.clusterArns || []) {
      const cluster = clusterArn.split("/").pop();
      const svcs = await ecs.send(new ListServicesCommand({ cluster: clusterArn }));
      for (const svcArn of svcs.serviceArns || []) {
        const service = svcArn.split("/").pop();
        out.resources.push({
          kind: "ecs_service",
          name: service,
          cluster,
          arn: svcArn,
          suggested: SIGNAL_SOURCES.ecs({ ClusterName: cluster, ServiceName: service }),
        });
      }
    }
  } catch (err) {
    note("ecs", err);
  }

  try {
    const lambda = new LambdaClient(cfg);
    const fns = await lambda.send(new ListFunctionsCommand({ MaxItems: 100 }));
    for (const fn of fns.Functions || []) {
      out.resources.push({
        kind: "lambda",
        name: fn.FunctionName,
        runtime: fn.Runtime,
        suggested: SIGNAL_SOURCES.lambda({ FunctionName: fn.FunctionName }),
      });
    }
  } catch (err) {
    note("lambda", err);
  }

  try {
    const agv2 = new ApiGatewayV2Client(cfg);
    const apis = await agv2.send(new GetApisCommand({ MaxResults: "100" }));
    for (const api of apis.Items || []) {
      out.resources.push({
        kind: "apigateway_http",
        name: api.Name,
        api_id: api.ApiId,
        protocol: api.ProtocolType,
        endpoint: api.ApiEndpoint,
        suggested: api.ApiId ? SIGNAL_SOURCES.apigateway_http(api.ApiId) : {},
      });
    }
  } catch (err) {
    note("apigateway_http", err);
  }

  try {
    const agv1 = new APIGatewayClient(cfg);
    const rest = await agv1.send(new GetRestApisCommand({ limit: 100 }));
    for (const api of rest.items || []) {
      out.resources.push({
        kind: "apigateway_rest",
        name: api.name,
        api_id: api.id,
        suggested: api.name ? SIGNAL_SOURCES.apigateway_rest(api.name) : {},
      });
    }
  } catch (err) {
    note("apigateway_rest", err);
  }

  try {
    const rds = new RDSClient(cfg);
    const dbs = await rds.send(new DescribeDBInstancesCommand({}));
    for (const db of dbs.DBInstances || []) {
      out.resources.push({
        kind: "rds",
        name: db.DBInstanceIdentifier,
        engine: db.Engine,
        suggested: SIGNAL_SOURCES.rds({ DBInstanceIdentifier: db.DBInstanceIdentifier }),
      });
    }
  } catch (err) {
    note("rds", err);
  }

  try {
    const logs = new CloudWatchLogsClient(cfg);
    const groups = await logs.send(new DescribeLogGroupsCommand({ limit: 50 }));
    out.log_groups = (groups.logGroups || []).map((g) => g.logGroupName);
  } catch (err) {
    note("log_groups", err);
  }

  out.gaps = unknownGaps(out);
  if (!probe.found("alb", "lambda") && !probe.unknown("alb", "lambda")) {
    out.gaps.push(t("text.gap_aws_no_serving"));
  }
  if (probe.found("lambda") &&
      !probe.found("apigateway_http", "apigateway_rest") &&
      !probe.unknown("apigateway_http", "apigateway_rest")) {
    out.gaps.push(t("text.gap_lambda_without_gateway"));
  }
  if (!out.log_groups?.length && !probe.unknown("log_groups")) {
    out.gaps.push(t("text.gap_no_log_groups"));
  }
  return out;
}

async function gcpLogEntries(env, { filter, from, to, limit }) {
  const { projectId } = gcpClient(env);
  const clauses = [
    `timestamp >= "${from.toISOString()}"`, // i18n-ignore Cloud Logging filter syntax
    `timestamp <= "${to.toISOString()}"`, // i18n-ignore Cloud Logging filter syntax
  ];
  const extra = String(filter || "").trim();
  if (extra) clauses.push(`(${extra})`);
  const body = await gcpFetch(env, "https://logging.googleapis.com/v2/entries:list", {
    method: "POST",
    body: {
      resourceNames: [`projects/${projectId}`],
      filter: clauses.join(" AND "),
      orderBy: "timestamp desc", // i18n-ignore Cloud Logging sort order
      pageSize: limit,
    },
  });
  return (body.entries || []).map((e) => ({
    at: e.timestamp,
    severity: e.severity || "DEFAULT",
    message:
      e.textPayload ??
      (e.jsonPayload ? JSON.stringify(e.jsonPayload) : undefined) ??
      (e.protoPayload ? JSON.stringify(e.protoPayload) : ""),
    resource: e.resource?.labels?.service_name || e.resource?.labels?.container_name || e.resource?.type || "",
    trace: e.trace || undefined,
  }));
}

const ERROR_PERIODS = [
  [1, "PERIOD_1_HOUR"],
  [6, "PERIOD_6_HOURS"],
  [24, "PERIOD_1_DAY"],
  [24 * 7, "PERIOD_1_WEEK"],
  [24 * 30, "PERIOD_30_DAYS"],
];

function errorReportingPeriod(from, to) {
  const hours = (to - from) / 3_600_000;
  for (const [max, period] of ERROR_PERIODS) {
    if (hours <= max) return period;
  }
  return "PERIOD_30_DAYS";
}

async function gcpErrorGroups(env, from, to, limit) {
  const { projectId } = gcpClient(env);
  const params = new URLSearchParams({
    "timeRange.period": errorReportingPeriod(from, to),
    pageSize: String(limit),
    order: "COUNT_DESC",
  });
  const url = `https://clouderrorreporting.googleapis.com/v1beta1/projects/${encodeURIComponent(projectId)}/groupStats?${params}`;
  let reportingError = null;
  try {
    const body = await gcpFetch(env, url);
    const stats = body.errorGroupStats || [];
    if (stats.length) {
      return {
        source: "error_reporting",
        groups: stats.map((g) => ({
          head: (g.representative?.message || "").split("\n")[0].slice(0, 200),
          occurrences: Number(g.count || 0),
          first_seen: g.firstSeenTime,
          last_seen: g.lastSeenTime,
          affected_users: Number(g.affectedUsersCount || 0),
          services: (g.affectedServices || []).map((s) => s.service).filter(Boolean),
          group_id: g.group?.groupId,
        })),
      };
    }
  } catch (err) {
    reportingError = err?.message ?? String(err);
  }

  const entries = await gcpLogEntries(env, {
    filter: 'severity >= "ERROR"', // i18n-ignore Cloud Logging filter syntax
    from,
    to,
    limit: Math.min(MAX_LOG_ROWS, limit * 20),
  });
  const byHead = new Map();
  for (const e of entries) {
    const head = String(e.message || "").split("\n")[0].slice(0, 200);
    if (!head) continue;
    const g = byHead.get(head) || { head, occurrences: 0, first_seen: e.at, last_seen: e.at };
    g.occurrences += 1;
    if (e.at < g.first_seen) g.first_seen = e.at;
    if (e.at > g.last_seen) g.last_seen = e.at;
    byHead.set(head, g);
  }
  return {
    source: "cloud_logging",
    fallback_reason: reportingError ?? t("text.error_reporting_empty"),
    groups: [...byHead.values()].sort((a, b) => b.occurrences - a.occurrences).slice(0, limit),
  };
}

async function discoverGCP(env) {
  const { projectId } = gcpClient(env);
  const out = { provider: "gcp", project: projectId, region: env.region || "" };
  const probe = probeState(out);
  const note = (kind, err) => probe.fail(kind, err);
  const P = encodeURIComponent(projectId);

  try {
    const body = await gcpFetch(env, `https://run.googleapis.com/v2/projects/${P}/locations/-/services?pageSize=100`);
    for (const svc of body.services || []) {
      const parts = String(svc.name || "").split("/");
      const name = parts.pop();
      const location = parts[3];
      out.resources.push({
        kind: "cloud_run",
        name,
        location,
        url: svc.uri,
        last_deployed: svc.updateTime,
        suggested: GCP_SIGNAL_SOURCES.cloud_run(name),
      });
    }
  } catch (err) {
    note("cloud_run", err);
  }

  try {
    const body = await gcpFetch(env, `https://compute.googleapis.com/compute/v1/projects/${P}/global/urlMaps`);
    for (const um of body.items || []) {
      out.resources.push({
        kind: "https_lb",
        name: um.name,
        suggested: GCP_SIGNAL_SOURCES.https_lb(um.name),
      });
    }
  } catch (err) {
    note("https_lb", err);
  }

  try {
    const body = await gcpFetch(env, `https://container.googleapis.com/v1/projects/${P}/locations/-/clusters`);
    for (const c of body.clusters || []) {
      out.resources.push({
        kind: "gke_cluster",
        name: c.name,
        location: c.location,
        suggested: GCP_SIGNAL_SOURCES.gke_container(c.name, "", ""),
        note: t("text.gke_narrow_filter"),
      });
    }
  } catch (err) {
    // The kind must match the kind pushed on success, or the gap check reports a permission error
    // as "no GKE cluster".
    note("gke_cluster", err);
  }

  try {
    const body = await gcpFetch(env, `https://sqladmin.googleapis.com/v1/projects/${P}/instances`);
    for (const inst of body.items || []) {
      const databaseID = `${projectId}:${inst.name}`;
      out.resources.push({
        kind: "cloudsql",
        name: inst.name,
        engine: inst.databaseVersion,
        region: inst.region,
        suggested: GCP_SIGNAL_SOURCES.cloudsql(databaseID),
      });
    }
  } catch (err) {
    note("cloudsql", err);
  }

  try {
    const body = await gcpFetch(env, `https://cloudfunctions.googleapis.com/v2/projects/${P}/locations/-/functions?pageSize=100`);
    for (const fn of body.functions || []) {
      const name = String(fn.name || "").split("/").pop();
      out.resources.push({
        kind: "cloud_function",
        name,
        runtime: fn.buildConfig?.runtime,
        suggested: GCP_SIGNAL_SOURCES.cloud_function(name),
      });
    }
  } catch (err) {
    note("cloud_function", err);
  }

  try {
    const body = await gcpFetch(env, `https://logging.googleapis.com/v2/projects/${P}/logs?pageSize=50`);
    out.log_names = body.logNames || [];
  } catch (err) {
    note("log_names", err);
  }

  out.gaps = unknownGaps(out);
  const serving = ["cloud_run", "https_lb", "cloud_function"];
  if (!probe.found(...serving) && !probe.unknown(...serving)) {
    out.gaps.push(t("text.gap_gcp_no_serving"));
  }
  if (!probe.found("gke_cluster") && !probe.unknown("gke_cluster")) {
    out.gaps.push(t("text.gap_no_gke"));
  }
  out.gaps.push(t("text.gap_gcp_cost"));
  return out;
}

async function gcpDeploys(env, from, to) {
  const { projectId } = gcpClient(env);
  const P = encodeURIComponent(projectId);
  const svcBody = await gcpFetch(env, `https://run.googleapis.com/v2/projects/${P}/locations/-/services?pageSize=100`);
  const services = (svcBody.services || []).map((s) => s.name).filter(Boolean);
  // One call per service; capped so large accounts do not hit Google rate limits.
  const capped = services.slice(0, 20);
  const deploys = [];
  for (const full of capped) {
    try {
      const body = await gcpFetch(env, `https://run.googleapis.com/v2/${full}/revisions?pageSize=50`);
      for (const rev of body.revisions || []) {
        const at = rev.createTime;
        if (!at) continue;
        const created = new Date(at);
        if (created < from || created > to) continue;
        deploys.push({
          at,
          service: String(full).split("/").pop(),
          revision: String(rev.name || "").split("/").pop(),
          image: rev.containers?.[0]?.image,
        });
      }
    } catch {
      // A single service we cannot read is not worth failing the list over.
    }
  }
  deploys.sort((a, b) => (a.at < b.at ? -1 : 1));
  return {
    deploys,
    truncated_services: services.length > capped.length ? services.length - capped.length : 0,
  };
}

const TOOLS = [
  {
    scope: "read",
    name: "environments_list",
    description: t("tools.environments_list.description"),
    inputSchema: {
      type: "object",
      properties: { project_id: { type: "string", description: t("tools.environments_list.params.project_id") } },
      required: ["project_id"],
    },
  },
  {
    scope: "read",
    name: "discover_resources",
    description: t("tools.discover_resources.description"),
    inputSchema: {
      type: "object",
      properties: { environment_id: { type: "string" } },
      required: ["environment_id"],
    },
  },
  {
    scope: "read",
    name: "metrics_query",
    description: t("tools.metrics_query.description"),
    inputSchema: {
      type: "object",
      properties: {
        environment_id: { type: "string" },
        sources: { type: "array", description: t("tools.metrics_query.params.sources"), items: { type: "object" } },
        from: { type: "string", description: t("tools.metrics_query.params.from") },
        to: { type: "string", description: t("tools.metrics_query.params.to") },
        period_seconds: { type: "number", description: t("tools.metrics_query.params.period_seconds") },
      },
      required: ["environment_id", "sources"],
    },
  },
  {
    scope: "read",
    name: "error_groups",
    description: t("tools.error_groups.description"),
    inputSchema: {
      type: "object",
      properties: {
        environment_id: { type: "string" },
        log_groups: { type: "array", items: { type: "string" }, description: t("tools.error_groups.params.log_groups") },
        from: { type: "string" },
        to: { type: "string" },
        limit: { type: "number", description: t("tools.error_groups.params.limit") },
      },
      required: ["environment_id"],
    },
  },
  {
    scope: "read",
    name: "logs_search",
    description: t("tools.logs_search.description"),
    inputSchema: {
      type: "object",
      properties: {
        environment_id: { type: "string" },
        query: { type: "string", description: t("tools.logs_search.params.query") },
        log_groups: { type: "array", items: { type: "string" } },
        from: { type: "string" },
        to: { type: "string" },
        limit: { type: "number" },
      },
      required: ["environment_id"],
    },
  },
  {
    scope: "read",
    name: "deploys",
    description: t("tools.deploys.description"),
    inputSchema: {
      type: "object",
      properties: {
        environment_id: { type: "string" },
        from: { type: "string" },
        to: { type: "string" },
      },
      required: ["environment_id"],
    },
  },
];

// Keep the write rung even with no write tools, or a future write tool silently vanishes from
// thulu-cloud@write sessions.
const SCOPE_IMPLIES = { read: new Set(["read"]), write: new Set(["read", "write"]) };

function toolsForScope(scope) {
  const allow = SCOPE_IMPLIES[scope] || SCOPE_IMPLIES.read;
  return TOOLS.filter((tool) => allow.has(tool.scope));
}
// Scope keywords inside an explicit list expand to that scope's tools, matching thulu-github and
// octopus-api.
function toolsForExplicit(names) {
  const allowed = new Set();
  for (const name of names) {
    if (SCOPE_IMPLIES[name]) {
      for (const tool of toolsForScope(name)) allowed.add(tool.name);
      continue;
    }
    allowed.add(name);
  }
  return TOOLS.filter((tool) => allowed.has(tool.name));
}

const HANDLERS = {
  async environments_list(args) {
    if (!args.project_id) throw new Error(t("errors.project_id_required"));
    const envs = await callOctopus(`/projects/${encodeURIComponent(args.project_id)}/environments`);
    return ok(envs);
  },

  async discover_resources(args, ctx) {
    const env = await authorizedEnvironment(args.environment_id, ctx, "discover_resources");
    return ok(requireSupported(env, "discover_resources") === "gcp" ? await discoverGCP(env) : await discoverAWS(env));
  },

  async metrics_query(args, ctx) {
    if (!Array.isArray(args.sources) || args.sources.length === 0) {
      throw new Error(t("errors.sources_empty"));
    }
    const { from, to } = parseRange(args);
    const env = await authorizedEnvironment(args.environment_id, ctx, "metrics_query");
    const specs = args.sources.map((source, i) => ({ id: source.id ?? `s${i}`, source }));
    const series = await collectSeries(env, specs, from, to, Number(args.period_seconds) || 300, "metrics_query");
    return ok({ environment_id: env.environment_id, provider: env.provider, from, to, series });
  },

  async error_groups(args, ctx) {
    const { from, to } = parseRange({ from: args.from, to: args.to ?? undefined });
    const limit = Math.min(Number(args.limit) || 20, 100);
    const env = await authorizedEnvironment(args.environment_id, ctx, "error_groups");
    if (requireSupported(env, "error_groups") === "gcp") {
      const res = await gcpErrorGroups(env, from, to, limit);
      return ok({ environment_id: env.environment_id, provider: "gcp", from, to, ...res });
    }
    // i18n-ignore Logs Insights query
    const query = `
      fields @message, @timestamp
      | filter @message like /(?i)(error|exception|traceback|panic|fatal)/
      | parse @message /(?<head>^.{0,200})/
      | stats count(*) as occurrences,
              earliest(@timestamp) as first_seen,
              latest(@timestamp) as last_seen
          by head
      | sort occurrences desc
      | limit ${limit}`;
    const res = await runInsightsQuery(env, logGroupsFor(env, args.log_groups), query, from, to, limit);
    const groups = rowsToObjects(res);
    return ok({
      environment_id: env.environment_id,
      from,
      to,
      partial: res.partial ?? false,
      groups,
    });
  },

  async logs_search(args, ctx) {
    const { from, to } = parseRange(args);
    const limit = Math.min(Number(args.limit) || 50, MAX_LOG_ROWS);
    const env = await authorizedEnvironment(args.environment_id, ctx, "logs_search");
    if (requireSupported(env, "logs_search") === "gcp") {
      const rows = await gcpLogEntries(env, { filter: args.query, from, to, limit });
      return ok(
        clip(
          JSON.stringify({ environment_id: env.environment_id, provider: "gcp", rows }, null, 2),
          LOG_CAP_BYTES,
          "logs",
        ),
      );
    }
    const query = args.query || "fields @timestamp, @message | sort @timestamp desc"; // i18n-ignore Logs Insights query
    const res = await runInsightsQuery(env, logGroupsFor(env, args.log_groups), query, from, to, limit);
    const rows = rowsToObjects(res);
    return ok(clip(JSON.stringify({ environment_id: env.environment_id, partial: res.partial ?? false, rows }, null, 2), LOG_CAP_BYTES, "logs"));
  },

  async deploys(args, ctx) {
    const { from, to } = parseRange(args);
    const env = await authorizedEnvironment(args.environment_id, ctx, "deploys");
    if (requireSupported(env, "deploys") === "gcp") {
      const { deploys, truncated_services: truncated } = await gcpDeploys(env, from, to);
      return ok({
        environment_id: env.environment_id,
        provider: "gcp",
        from,
        to,
        deploys,
        source: "cloud_run_revisions",
        ...(truncated ? { note: t("text.deploys_truncated", { count: truncated }) } : {}),
        ...(deploys.length ? {} : { note_empty: t("text.deploys_none") }),
      });
    }
    return ok({
      environment_id: env.environment_id,
      from,
      to,
      deploys: [],
      unavailable: true,
      reason: t("text.deploys_aws_unavailable"),
    });
  },
};

function buildServer({ taskId, projectId, scope, allowedTools, catalog = false, listOnly = false }) {
  const exposed = allowedTools ? toolsForExplicit(allowedTools) : toolsForScope(scope);
  const exposedNames = new Set(exposed.map((tool) => tool.name));

  const server = new Server(
    { name: "thulu-cloud", version: "0.1.0" },
    { capabilities: { tools: {} } },
  );

  // A catalog session lists every tool, but CallTool still answers only from exposedNames.
  const listed = catalog ? TOOLS : exposed;
  server.setRequestHandler(ListToolsRequestSchema, async () => ({
    tools: listed.map(({ scope: _s, ...rest }) => rest),
  }));

  server.setRequestHandler(CallToolRequestSchema, async (req) => {
    if (listOnly) {
      return { isError: true, content: [{ type: "text", text: t("errors.list_only_session") }] };
    }
    const name = req.params.name;
    if (!exposedNames.has(name)) {
      const known = TOOLS.find((tool) => tool.name === name);
      const reason = known
        ? (allowedTools
          ? t("errors.tool_not_allowed", { name })
          : t("errors.tool_scope_required", { name, required: known.scope, scope }))
        : t("errors.unknown_tool", { name });
      return { isError: true, content: [{ type: "text", text: reason }] };
    }
    const handler = HANDLERS[name];
    if (!handler) {
      return {
        isError: true,
        content: [{ type: "text", text: t("errors.tool_without_handler", { name }) }],
      };
    }
    try {
      return await handler(req.params.arguments ?? {}, { taskId, projectId, scope });
    } catch (err) {
      const status = err?.$metadata?.httpStatusCode ? ` (HTTP ${err.$metadata.httpStatusCode})` : "";
      return {
        isError: true,
        content: [
          {
            type: "text",
            text: t("errors.tool_failed", { tool: name, status, err: err?.message ?? String(err) }),
          },
        ],
      };
    }
  });

  return server;
}

const TOOL_LIST_SEP = /[\s,+]+/;

const app = express();
// Do not app.use(express.json()) globally: SSEServerTransport.handlePostMessage reads the raw body
// stream.
const transports = new Map();

app.get("/health", (_req, res) => res.json({ ok: true, tools: TOOLS.length }));

app.get("/sse", async (req, res) => {
  const taskId = String(req.query.task ?? "") || null;
  const projectId = String(req.query.project ?? "") || null;
  const toolsRaw = String(req.query.tools ?? "");

  const scope = String(req.query.scope ?? "") === "write" ? "write" : "read";
  // catalog=1 is the daemon's picker probe (internal/mcp/probe.go): it widens tools/list only,
  // never CallTool.
  const catalog = String(req.query.catalog ?? "") === "1";
  let allowedTools = null;
  if (toolsRaw) {
    allowedTools = toolsRaw.split(TOOL_LIST_SEP).map((s) => s.trim()).filter(Boolean);
  }

  const key = keyFromRequest(req);
  if (!key && String(req.query.catalog ?? "") !== "1") {
    res.status(401).json({ error: t("errors.handshake_no_key") });
    return;
  }
  const listOnly = !key;
  const transport = new SSEServerTransport("/messages", res);
  transports.set(transport.sessionId, transport);
  sessionKeys.set(transport.sessionId, key);
  res.on("close", () => {
    transports.delete(transport.sessionId);
    sessionKeys.delete(transport.sessionId);
  });
  const server = buildServer({ taskId, projectId, scope, allowedTools, catalog, listOnly });
  await server.connect(transport);
});

app.post("/messages", async (req, res) => {
  const sessionId = String(req.query.sessionId ?? "");
  const transport = transports.get(sessionId);
  if (!transport) {
    res.status(404).send(t("errors.session_not_found"));
    return;
  }
  // The POST must repeat the handshake's key: a session id travels in URLs and is not a credential.
  const key = sessionKeys.get(sessionId) || "";
  if (key && !sameKey(key, keyFromRequest(req))) {
    res.status(401).json({ error: t("errors.message_key_mismatch") });
    return;
  }
  await callerKey.run(key, () => transport.handlePostMessage(req, res));
});

app.post("/collect", express.json({ limit: "1mb" }), async (req, res) => {
  const { environment_id: envId, mappings, environment, credential } = req.body || {};
  if (!envId || !Array.isArray(mappings)) {
    res.status(400).json({ error: t("errors.collect_body_invalid") });
    return;
  }
  // The daemon sends the environment and credential it authorized; this path holds no caller key,
  // so it must never call back or touch the caches.
  if (environment?.environment_id !== envId || !credential?.payload) {
    res.status(400).json({ error: t("errors.collect_target_missing") });
    return;
  }
  try {
    const env = withCredential(environment, credential);
    if (!SUPPORTED.has(env.provider)) {
      res.json({
        environment_id: envId,
        points: [],
        skipped: t("text.collect_provider_unsupported", { provider: env.provider }),
      });
      return;
    }
    const to = new Date();
    const backfill = new Set(mappings.filter((m) => m?.backfill).map((m) => m.id));
    const backfillHours = Math.min(Number(req.body.backfill_hours) || 0, MAX_RANGE_HOURS);
    const windowMs = backfill.size && backfillHours > 0 ? backfillHours * 3600 * 1000 : 10 * 60 * 1000;
    const from = new Date(to.getTime() - windowMs);
    const specs = mappings
      .filter((m) => usableSource(env.provider, m?.source))
      .map((m) => ({ id: m.id, service: m.service, signal: m.signal, source: m.source }));
    if (!specs.length) {
      res.json({
        environment_id: envId,
        points: [],
        ...(mappings.length
          ? {
              skipped: t("text.collect_no_usable_source", {
                count: mappings.length,
                provider: env.provider,
              }),
            }
          : {}),
      });
      return;
    }
    const series = await collectSeries(env, specs, from, to, 300, "collect");
    // Only the freshest point: earlier buckets were written by the previous tick. Backfilled
    // mappings get all of them.
    const points = [];
    for (const s of series) {
      const known = s.points.filter((p) => p.value !== null);
      const keep = backfill.has(s.id) ? known : known.slice(-1);
      for (const p of keep) {
        points.push({ mapping_id: s.id, service: s.service, signal: s.signal, at: p.at, value: p.value });
      }
    }
    // GCP errors are caught per series, so all-failed must become a 502, or a rejected credential
    // reads as "no traffic".
    const failed = series.filter((s) => s.error);
    if (failed.length === series.length && failed.length > 0) {
      res.status(502).json({ error: failed[0].error });
      return;
    }
    res.json({
      environment_id: envId,
      points,
      ...(failed.length ? { errors: failed.map((s) => ({ mapping_id: s.id, signal: s.signal, error: s.error })) } : {}),
    });
  } catch (err) {
    res.status(502).json({ error: err?.message ?? String(err) });
  }
});

app.listen(PORT, () => {
  console.log(tLog("log.listening", { port: PORT }));
  console.log(tLog("log.octopus_api", { url: OCTOPUS_API }));
  console.log(tLog("log.sse_endpoint"));
  console.log(tLog("log.collector"));
  console.log(tLog("log.health"));
});

const shutdown = (sig) => {
  console.log(tLog("log.shutting_down", { signal: sig }));
  process.exit(0);
};
process.on("SIGTERM", () => shutdown("SIGTERM"));
process.on("SIGINT", () => shutdown("SIGINT"));

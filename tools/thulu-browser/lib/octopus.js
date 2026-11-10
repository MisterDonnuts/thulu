// Daemon calls use the caller's run-scoped key from the SSE handshake (callerKey), never a
// server-wide key.

import { AsyncLocalStorage } from "node:async_hooks";

import { t } from "../i18n.js";

export const OCTOPUS_API = process.env.OCTOPUS_API || "";

export const callerKey = new AsyncLocalStorage();

export function keyFromRequest(req) {
  const direct = req.headers["x-api-key"];
  if (typeof direct === "string" && direct.trim()) return direct.trim();
  const bearer = req.headers.authorization;
  if (typeof bearer === "string" && bearer.startsWith("Bearer ")) {
    return bearer.slice("Bearer ".length).trim(); // i18n-ignore HTTP auth scheme
  }
  return "";
}

function authHeaders() {
  const key = callerKey.getStore() || "";
  if (!key) {
    throw new Error(t("errors.no_credential"));
  }
  return { "X-API-Key": key };
}

export class OctopusHttpError extends Error {
  constructor(method, route, status, body) {
    super(t("errors.octopus_http", { method, route, status, body: body.trim().slice(0, 200) }));
    this.name = "OctopusHttpError";
    this.status = status;
  }
}

async function call(method, route, init = {}) {
  if (!OCTOPUS_API) throw new Error(t("errors.octopus_api_unset"));
  const r = await fetch(`${OCTOPUS_API}${route}`, {
    method,
    ...init,
    headers: { ...authHeaders(), ...(init.headers || {}) },
  });
  const text = await r.text();
  if (!r.ok) throw new OctopusHttpError(method, route, r.status, text);
  try { return JSON.parse(text); } catch { return { raw: text }; }
}

export function withExtension(name, ext, fallback = "file") {
  const base = (name && String(name).trim()) || `${fallback}-${Date.now()}`;
  return base.toLowerCase().endsWith(`.${ext}`) ? base : `${base}.${ext}`;
}

export async function uploadAttachment(taskId, filename, buf, mimeType) {
  const fd = new FormData();
  fd.append("file", new Blob([buf], { type: mimeType }), filename);
  return call("POST", `/tasks/${encodeURIComponent(taskId)}/attachments`, { body: fd });
}

// The daemon caps the markdown body at 256 KB; embedded attachments must already be on the task.
export async function postReport(taskId, { kind, title, body, instanceId = "" }) {
  return call("POST", `/tasks/${encodeURIComponent(taskId)}/reports`, {
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ kind, title, body, instance_id: instanceId }),
  });
}

export async function listTestCases(taskId) {
  return call("GET", `/tasks/${encodeURIComponent(taskId)}/test-cases`);
}

export async function recordTestCaseRun(taskId, caseKey, run) {
  return call("POST", `/tasks/${encodeURIComponent(taskId)}/test-cases/${encodeURIComponent(caseKey)}/runs`, {
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(run),
  });
}

export async function getRecording() {
  return call("GET", "/evidence/recording");
}

// Never log this value or return it to the model.
export async function getRecordingSecret(name) {
  const r = await call("GET", `/evidence/recording/secrets/${encodeURIComponent(name)}`);
  if (typeof r?.value === "string") return r.value;
  if (typeof r?.raw === "string") return r.raw;
  if (typeof r === "string" || typeof r === "number") return String(r);
  throw new Error(t("errors.secret_unreadable", { name }));
}

export async function saveRecordingTake(take) {
  return call("PUT", "/evidence/recording/take", {
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ take }),
  });
}

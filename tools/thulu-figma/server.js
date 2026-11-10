// SECURITY: the Figma token is resolved server-side via /internal/credentials/resolve with the
// caller's run key and must never reach the agent.
// Every tool is a GET; define nothing that writes to Figma.

import { AsyncLocalStorage } from "node:async_hooks";
import { timingSafeEqual } from "node:crypto";

import express from "express";
import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { SSEServerTransport } from "@modelcontextprotocol/sdk/server/sse.js";
import { ListToolsRequestSchema, CallToolRequestSchema } from "@modelcontextprotocol/sdk/types.js";

import { t, tLog } from "./i18n.js";
import {
  parseFileRef,
  parseTeamRef,
  normalizeNodeId,
  summarizeTree,
  summarizeNode,
  summarizeLibrary,
  summarizeComments,
} from "./figma.js";

const PORT = Number(process.env.PORT || 3000);
const OCTOPUS_API = process.env.OCTOPUS_API || "";
// Never take this from a request: the Figma token is sent to it.
const FIGMA_API_URL = (process.env.FIGMA_API_URL || "https://api.figma.com").replace(/\/+$/, "");
const SERVER_NAME = "thulu-figma";

const OUTPUT_CAP_BYTES = Number(process.env.OUTPUT_CAP_BYTES || 60 * 1024);
const MAX_TREE_NODES = Number(process.env.MAX_TREE_NODES || 500);
const MAX_NODE_SUMMARIES = Number(process.env.MAX_NODE_SUMMARIES || 400);
const MAX_NODE_IDS = 20;
const MAX_RENDER_IDS = 10;
const MAX_INLINE_IMAGES = 4;
const MAX_INLINE_BYTES = Number(process.env.MAX_INLINE_BYTES || 1.5 * 1024 * 1024);
const FIGMA_TIMEOUT_MS = Number(process.env.FIGMA_TIMEOUT_MS || 30_000);

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

// A session id travels in a URL and is not a credential: every POST must carry the key the session
// was opened with.
function sameKey(a, b) {
  if (!a || !b) return false;
  const x = Buffer.from(String(a));
  const y = Buffer.from(String(b));
  return x.length === y.length && timingSafeEqual(x, y);
}

function octopusAuthHeaders() {
  const key = callerKey.getStore() || "";
  if (!key) throw new Error(t("errors.no_credential"));
  return { "X-API-Key": key };
}

async function callOctopus(method, path, init = {}) {
  if (!OCTOPUS_API) throw new Error(t("errors.octopus_api_unset"));
  let r;
  try {
    r = await fetch(`${OCTOPUS_API}${path}`, {
      method,
      ...init,
      headers: { ...octopusAuthHeaders(), ...(init.headers || {}) },
    });
  } catch (err) {
    throw new Error(t("errors.octopus_unreachable", { url: OCTOPUS_API, err: err?.message ?? err }));
  }
  const text = await r.text();
  if (!r.ok) {
    const e = new Error(t("errors.octopus_http", { method, path, status: r.status, body: text.trim().slice(0, 400) }));
    e.body = text;
    throw e;
  }
  try {
    return JSON.parse(text);
  } catch {
    return { raw: text };
  }
}

// Cached per (caller key, tool): the daemon decides per tool, and what it granted one run must not
// serve another.
const tokenCache = new Map();
const TOKEN_TTL_MS = 45_000;

async function figmaToken(tool) {
  const cacheKey = JSON.stringify([callerKey.getStore() || "", tool]);
  const hit = tokenCache.get(cacheKey);
  if (hit && Date.now() - hit.at < TOKEN_TTL_MS) return hit.value;
  const qs = new URLSearchParams({ server: SERVER_NAME, tool });
  let resolved;
  try {
    resolved = await callOctopus("GET", `/internal/credentials/resolve?${qs}`);
  } catch (err) {
    throw new Error(explainCredentialFailure(err, tool));
  }
  const token = resolved?.payload?.token;
  if (resolved?.provider && resolved.provider !== "figma") {
    throw new Error(t("errors.credential_not_figma", { credential: resolved.name, provider: resolved.provider }));
  }
  if (!token) throw new Error(t("errors.no_figma_credential"));
  tokenCache.set(cacheKey, { at: Date.now(), value: token });
  return token;
}

function explainCredentialFailure(err, tool) {
  try {
    const detail = JSON.parse(err?.body ?? "")?.decision?.detail;
    if (detail) return t("errors.credential_refused", { tool, detail });
  } catch {
    // Not a decision payload: fall through to the transport error.
  }
  return String(err?.message ?? err);
}

async function figmaGet(path, token, query = {}) {
  const url = new URL(`${FIGMA_API_URL}${path}`);
  for (const [k, v] of Object.entries(query)) {
    if (v !== undefined && v !== null && v !== "") url.searchParams.set(k, String(v));
  }
  let r;
  try {
    r = await fetch(url, {
      headers: { "X-Figma-Token": token, Accept: "application/json" },
      signal: AbortSignal.timeout(FIGMA_TIMEOUT_MS),
    });
  } catch (err) {
    throw new Error(t("errors.figma_unreachable", { url: FIGMA_API_URL, err: err?.message ?? err }));
  }
  const text = await r.text();
  if (!r.ok) {
    let msg = text.trim().slice(0, 300);
    try {
      const body = JSON.parse(text);
      msg = body.err || body.message || msg;
    } catch {
      // Keep the raw text.
    }
    if (r.status === 429) {
      throw new Error(t("errors.figma_rate_limited", { path, retry: r.headers.get("retry-after") || "?" }));
    }
    throw new Error(t("errors.figma_http", { path, status: r.status, msg }));
  }
  return JSON.parse(text);
}

// Image URLs come from Figma's answer, never from the agent; plain http is allowed only for the
// configured API origin (tests).
function fetchableImageURL(raw) {
  try {
    const u = new URL(raw);
    return u.protocol === "https:" || u.origin === new URL(FIGMA_API_URL).origin;
  } catch {
    return false;
  }
}

async function download(url) {
  const r = await fetch(url, { signal: AbortSignal.timeout(FIGMA_TIMEOUT_MS) });
  if (!r.ok) throw new Error(t("errors.image_download", { status: r.status }));
  return Buffer.from(await r.arrayBuffer());
}

async function uploadAttachment(taskId, filename, buf, mimeType) {
  const fd = new FormData();
  fd.append("file", new Blob([buf], { type: mimeType }), filename);
  return callOctopus("POST", `/tasks/${encodeURIComponent(taskId)}/attachments`, { body: fd });
}

function ok(value) {
  const text = typeof value === "string" ? value : JSON.stringify(value);
  const body =
    text.length <= OUTPUT_CAP_BYTES
      ? text
      : `${text.slice(0, OUTPUT_CAP_BYTES)}\n${t("text.truncated", { bytes: text.length - OUTPUT_CAP_BYTES })}`;
  return { content: [{ type: "text", text: body }] };
}

function requireFile(args) {
  const ref = parseFileRef(args.file_key);
  if (!ref) throw new Error(t("errors.file_key_invalid", { value: String(args.file_key ?? "") }));
  return ref;
}

function nodeIdsFrom(args, ref, max) {
  const given = Array.isArray(args.node_ids)
    ? args.node_ids
    : typeof args.node_ids === "string"
      ? args.node_ids.split(",")
      : [];
  const ids = given.map(normalizeNodeId).filter(Boolean);
  if (!ids.length && ref.nodeId) ids.push(ref.nodeId);
  if (!ids.length) throw new Error(t("errors.node_ids_required"));
  if (ids.length > max) throw new Error(t("errors.too_many_nodes", { count: ids.length, max }));
  for (const id of ids) {
    if (!/^[A-Za-z0-9:;_-]+$/.test(id)) throw new Error(t("errors.node_id_invalid", { id }));
  }
  return ids;
}

const RENDER_MIME = { png: "image/png", jpg: "image/jpeg", svg: "image/svg+xml", pdf: "application/pdf" };

const clampInt = (v, lo, hi, def) => {
  const n = Number.parseInt(v, 10);
  return Number.isFinite(n) ? Math.min(hi, Math.max(lo, n)) : def;
};

const fileLink = (key, nodeId) =>
  `https://www.figma.com/design/${key}${nodeId ? `?node-id=${nodeId.replace(/:/g, "-")}` : ""}`;

const fileKeyParam = { type: "string", description: t("params.file_key") };

const TOOLS = [
  {
    scope: "read",
    name: "file_get",
    description: t("tools.file_get.description"),
    inputSchema: {
      type: "object",
      properties: {
        file_key: fileKeyParam,
        depth: { type: "number", description: t("tools.file_get.params.depth") },
        library: { type: "boolean", description: t("tools.file_get.params.library") },
      },
      required: ["file_key"],
    },
  },
  {
    scope: "read",
    name: "nodes_get",
    description: t("tools.nodes_get.description"),
    inputSchema: {
      type: "object",
      properties: {
        file_key: fileKeyParam,
        node_ids: { type: "array", items: { type: "string" }, description: t("tools.nodes_get.params.node_ids") },
        depth: { type: "number", description: t("tools.nodes_get.params.depth") },
      },
      required: ["file_key"],
    },
  },
  {
    scope: "read",
    name: "images_render",
    description: t("tools.images_render.description"),
    inputSchema: {
      type: "object",
      properties: {
        file_key: fileKeyParam,
        node_ids: { type: "array", items: { type: "string" }, description: t("tools.images_render.params.node_ids") },
        format: { type: "string", enum: ["png", "jpg", "svg", "pdf"], description: t("tools.images_render.params.format") },
        scale: { type: "number", description: t("tools.images_render.params.scale") },
        inline: { type: "boolean", description: t("tools.images_render.params.inline") },
        attach: { type: "boolean", description: t("tools.images_render.params.attach") },
      },
      required: ["file_key"],
    },
  },
  {
    scope: "read",
    name: "comments_list",
    description: t("tools.comments_list.description"),
    inputSchema: {
      type: "object",
      properties: {
        file_key: fileKeyParam,
        include_resolved: { type: "boolean", description: t("tools.comments_list.params.include_resolved") },
        limit: { type: "number", description: t("tools.comments_list.params.limit") },
      },
      required: ["file_key"],
    },
  },
  {
    scope: "read",
    name: "team_projects_list",
    description: t("tools.team_projects_list.description"),
    inputSchema: {
      type: "object",
      properties: { team_id: { type: "string", description: t("tools.team_projects_list.params.team_id") } },
      required: ["team_id"],
    },
  },
  {
    scope: "read",
    name: "project_files_list",
    description: t("tools.project_files_list.description"),
    inputSchema: {
      type: "object",
      properties: { project_id: { type: "string", description: t("tools.project_files_list.params.project_id") } },
      required: ["project_id"],
    },
  },
];

const HANDLERS = {
  async file_get(args, token) {
    const ref = requireFile(args);
    const depth = clampInt(args.depth, 1, 4, 2);
    const file = await figmaGet(`/v1/files/${ref.fileKey}`, token, { depth });
    const tree = summarizeTree(file.document, { maxNodes: MAX_TREE_NODES });
    const out = {
      file_key: ref.fileKey,
      name: file.name,
      last_modified: file.lastModified,
      version: file.version,
      editor_type: file.editorType,
      link: fileLink(ref.fileKey),
      depth,
      pages: tree.pages,
    };
    if (tree.omitted_nodes) out.omitted_nodes = tree.omitted_nodes;
    if (args.library !== false) Object.assign(out, summarizeLibrary(file));
    return ok(out);
  },

  async nodes_get(args, token) {
    const ref = requireFile(args);
    const ids = nodeIdsFrom(args, ref, MAX_NODE_IDS);
    const depth = clampInt(args.depth, 0, 6, 3);
    // Always send depth >= 1: an absent depth returns the whole subtree, and depth 0 still needs
    // children to count them.
    const res = await figmaGet(`/v1/files/${ref.fileKey}/nodes`, token, { ids: ids.join(","), depth: Math.max(1, depth) });
    const budget = { left: MAX_NODE_SUMMARIES, omitted: 0 };
    const nodes = [];
    const missing = [];
    for (const id of ids) {
      const entry = res.nodes?.[id];
      if (!entry?.document) {
        missing.push(id);
        continue;
      }
      nodes.push(summarizeNode(entry.document, { depth, styles: entry.styles ?? {}, budget }));
    }
    const out = { file_key: ref.fileKey, name: res.name, nodes };
    if (missing.length) out.missing = missing;
    if (budget.omitted) out.omitted_nodes = budget.omitted;
    return ok(out);
  },

  async images_render(args, token, ctx) {
    const ref = requireFile(args);
    const ids = nodeIdsFrom(args, ref, MAX_RENDER_IDS);
    const format = Object.hasOwn(RENDER_MIME, args.format) ? args.format : "png";
    const scale = Math.min(4, Math.max(0.01, Number(args.scale) || 1));
    const raster = format === "png" || format === "jpg";
    const inline = args.inline === undefined ? raster : Boolean(args.inline) && raster;
    if (args.attach && !ctx.taskId) throw new Error(t("errors.attach_without_task"));

    const res = await figmaGet(`/v1/images/${ref.fileKey}`, token, { ids: ids.join(","), format, scale });
    if (res.err) throw new Error(t("errors.render_failed", { err: res.err }));

    const images = [];
    const content = [];
    let inlined = 0;
    for (const id of ids) {
      const url = res.images?.[id];
      const row = { node_id: id, url: url || null };
      if (!url) {
        row.error = t("text.render_empty");
        images.push(row);
        continue;
      }
      const wantBytes = (inline && inlined < MAX_INLINE_IMAGES) || args.attach;
      if (wantBytes && fetchableImageURL(url)) {
        try {
          const buf = await download(url);
          const mime = RENDER_MIME[format];
          if (args.attach) {
            const name = `figma-${ref.fileKey}-${id.replace(/[^A-Za-z0-9]+/g, "-")}.${format}`;
            const att = await uploadAttachment(ctx.taskId, name, buf, mime);
            row.attachment = { id: att?.id ?? null, filename: name, bytes: buf.length };
          }
          if (inline && inlined < MAX_INLINE_IMAGES) {
            if (buf.length > MAX_INLINE_BYTES) {
              row.inline = t("text.inline_too_large", { bytes: buf.length, max: MAX_INLINE_BYTES });
            } else {
              content.push({ type: "image", data: buf.toString("base64"), mimeType: mime });
              row.inline = true;
              inlined++;
            }
          }
        } catch (err) {
          row.error = err?.message ?? String(err);
        }
      }
      images.push(row);
    }
    const summary = {
      file_key: ref.fileKey,
      format,
      scale,
      images,
      note: t("text.render_urls_expire"),
    };
    return { content: [{ type: "text", text: JSON.stringify(summary) }, ...content] };
  },

  async comments_list(args, token) {
    const ref = requireFile(args);
    const res = await figmaGet(`/v1/files/${ref.fileKey}/comments`, token, { as_md: "true" });
    const out = summarizeComments(res.comments, {
      includeResolved: Boolean(args.include_resolved),
      limit: clampInt(args.limit, 1, 200, 50),
    });
    return ok({ file_key: ref.fileKey, ...out });
  },

  async team_projects_list(args, token) {
    const teamId = parseTeamRef(args.team_id);
    if (!teamId) throw new Error(t("errors.team_id_invalid", { value: String(args.team_id ?? "") }));
    const res = await figmaGet(`/v1/teams/${teamId}/projects`, token);
    return ok({
      team_id: teamId,
      name: res.name,
      projects: (res.projects ?? []).map((p) => ({ id: String(p.id), name: p.name })),
    });
  },

  async project_files_list(args, token) {
    const projectId = String(args.project_id ?? "").trim();
    if (!/^\d+$/.test(projectId)) throw new Error(t("errors.project_id_invalid", { value: projectId }));
    const res = await figmaGet(`/v1/projects/${projectId}/files`, token);
    return ok({
      project_id: projectId,
      name: res.name,
      files: (res.files ?? []).map((f) => ({ key: f.key, name: f.name, last_modified: f.last_modified })),
    });
  },
};

const SCOPE_IMPLIES = { read: new Set(["read"]), write: new Set(["read", "write"]) };

function toolsForScope(scope) {
  const allow = SCOPE_IMPLIES[scope] || SCOPE_IMPLIES.read;
  return TOOLS.filter((tool) => allow.has(tool.scope));
}

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

function buildServer({ taskId, scope, allowedTools, catalog = false, listOnly = false }) {
  const exposed = allowedTools ? toolsForExplicit(allowedTools) : toolsForScope(scope);
  const exposedNames = new Set(exposed.map((tool) => tool.name));
  const server = new Server({ name: SERVER_NAME, version: "0.1.0" }, { capabilities: { tools: {} } });

  // catalog=1 (the daemon's picker probe) lists every tool; CallTool still answers from
  // exposedNames. Listing is not granting.
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
        ? allowedTools
          ? t("errors.tool_not_allowed", { name })
          : t("errors.tool_scope_required", { name, required: known.scope, scope })
        : t("errors.unknown_tool", { name });
      return { isError: true, content: [{ type: "text", text: reason }] };
    }
    try {
      const token = await figmaToken(name);
      return await HANDLERS[name](req.params.arguments ?? {}, token, { taskId });
    } catch (err) {
      return {
        isError: true,
        content: [{ type: "text", text: t("errors.tool_failed", { tool: name, err: err?.message ?? String(err) }) }],
      };
    }
  });

  return server;
}

// The dashboard sends "+", mcp-config.sh sends commas, and a stale image may send a raw "+" that
// decodes as a space.
const TOOL_LIST_SEP = /[\s,+]+/;

const app = express();
// Do NOT app.use(express.json()): SSEServerTransport.handlePostMessage reads the raw request stream
// and a body parser would consume it first.
const transports = new Map();

app.get("/health", (_req, res) => res.json({ ok: true, tools: TOOLS.length }));

app.get("/sse", async (req, res) => {
  const taskId = String(req.query.task ?? "") || null;
  const toolsRaw = String(req.query.tools ?? "");
  const scope = String(req.query.scope ?? "") === "write" ? "write" : "read";
  const catalog = String(req.query.catalog ?? "") === "1";
  const allowedTools = toolsRaw ? toolsRaw.split(TOOL_LIST_SEP).map((s) => s.trim()).filter(Boolean) : null;

  const key = keyFromRequest(req);
  if (!key && !catalog) {
    res.status(401).json({ error: t("errors.handshake_no_key") });
    return;
  }
  const transport = new SSEServerTransport("/messages", res);
  transports.set(transport.sessionId, transport);
  sessionKeys.set(transport.sessionId, key);
  res.on("close", () => {
    transports.delete(transport.sessionId);
    sessionKeys.delete(transport.sessionId);
  });
  const server = buildServer({ taskId, scope, allowedTools, catalog, listOnly: !key });
  await server.connect(transport);
});

app.post("/messages", async (req, res) => {
  const sessionId = String(req.query.sessionId ?? "");
  const transport = transports.get(sessionId);
  if (!transport) {
    res.status(404).send(t("errors.session_not_found"));
    return;
  }
  const key = sessionKeys.get(sessionId) || "";
  if (key && !sameKey(key, keyFromRequest(req))) {
    res.status(401).json({ error: t("errors.message_key_mismatch") });
    return;
  }
  await callerKey.run(key, () => transport.handlePostMessage(req, res));
});

app.listen(PORT, () => {
  console.log(tLog("log.listening", { port: PORT }));
  console.log(tLog("log.octopus_api", { url: OCTOPUS_API }));
  console.log(tLog("log.figma_api", { url: FIGMA_API_URL }));
  console.log(tLog("log.sse_endpoint"));
  console.log(tLog("log.health"));
});

const shutdown = (sig) => {
  console.log(tLog("log.shutting_down", { signal: sig }));
  process.exit(0);
};
process.on("SIGTERM", () => shutdown("SIGTERM"));
process.on("SIGINT", () => shutdown("SIGINT"));

import express from "express";
import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { SSEServerTransport } from "@modelcontextprotocol/sdk/server/sse.js";
import {
  ListToolsRequestSchema,
  CallToolRequestSchema,
} from "@modelcontextprotocol/sdk/types.js";
import { promises as fs } from "fs";
import path from "path";
import { randomUUID } from "crypto";

import { t, tLog, withLocale } from "./i18n.js";
import { closeBrowser, getBrowser } from "./lib/browser.js";
import { accessibleElements, captureElementInfo, getElementAt, serializeDOM } from "./lib/dom.js";
import { HOST_GATEWAY, callerHostLoopback, closeHostProxy, hostContextOptions } from "./lib/host-proxy.js";
import { callerKey, keyFromRequest, uploadAttachment, withExtension } from "./lib/octopus.js";
import { admitHandshake, admitMessage, grantedTools } from "./lib/session-auth.js";
import { countByKind, describeFinding, findingsFor, groupFindings, ignorePatterns } from "./lib/page-findings.js";
import { VITALS, describeAction, describeVitals, formatMetric, perfFor } from "./lib/perf.js";
import { COLOR_SCHEMES, CPU_MAX, NETWORKS, describeProfile, emulation, throttleContext } from "./lib/throttle.js";
import {
  EVIDENCE_TOOLS,
  clearEvidenceStores,
  discardSessionTake,
  evidenceHandlers,
  recordAction,
  typedText,
} from "./evidence/tools.js";

const VERSION = "2.2.0";
const PORT = Number(process.env.PORT || 3000);
// A per-run browser shares its agent's network namespace and binds 127.0.0.1 so only that agent
// reaches it.
const LISTEN_HOST = process.env.LISTEN_HOST || "0.0.0.0";
const REQUIRED_KEY = (process.env.MCP_REQUIRED_KEY || "").trim();
const CATALOG_ONLY = ["1", "true"].includes(String(process.env.MCP_CATALOG_ONLY || "").trim().toLowerCase());
// ?tools= may narrow this grant, never widen it (lib/session-auth.js).
const GRANTED_TOOLS = String(process.env.MCP_ALLOWED_TOOLS || "").trim();
const DEFAULT_TIMEOUT_MS = 30_000;
const SESSION_IDLE_MS = Number(process.env.SESSION_IDLE_MS || 10 * 60 * 1000);
const AGENTS_ROOT = process.env.AGENTS_ROOT || "/agents-shared";
const MAX_STEPS = 500;
const METRICS_STEPS = 20;

const sessionKeys = new Map();

const sessions = new Map();

function touchSession(id) {
  const s = sessions.get(id);
  if (s) s.lastActivity = Date.now();
  return s;
}

function requireSession(id) {
  const s = touchSession(id);
  if (!s) throw new Error(t("errors.session_not_found", { id }));
  return s;
}

setInterval(async () => {
  const now = Date.now();
  for (const [id, s] of sessions) {
    if (now - s.lastActivity > SESSION_IDLE_MS) {
      await discardSessionTake(s);
      try { await s.context.close(); } catch {}
      if (s.meta.videoDir) {
        try {
          const files = await fs.readdir(s.meta.videoDir);
          const webm = files.find((f) => f.endsWith(".webm"));
          if (webm && s.meta.instance_id && s.meta.agent_type) {
            await persistVideo(path.join(s.meta.videoDir, webm), s.meta);
          }
        } catch {}
      }
      sessions.delete(id);
      console.log(tLog("log.session_expired", { id }));
    }
  }
}, 60_000);

function validatePath(p) {
  const resolved = path.resolve(p);
  const allowed = [AGENTS_ROOT, "/tmp"];
  const ok = allowed.some((root) => resolved.startsWith(path.resolve(root) + path.sep) || resolved === path.resolve(root));
  if (!ok) throw new Error(t("errors.path_outside", { path: p, allowed: allowed.join(", ") }));
  return resolved;
}

function sanitizeSlug(s, fallback) {
  const cleaned = String(s || "")
    .replace(/[^a-zA-Z0-9._-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 120);
  return cleaned || fallback;
}

function slugFromURL(u) {
  try {
    const parsed = new URL(u);
    const host = parsed.hostname.replace(/[^a-z0-9.-]/gi, "");
    const pth = parsed.pathname.replace(/[^a-z0-9]+/gi, "-").replace(/^-+|-+$/g, "");
    return pth ? `${host}-${pth}` : host;
  } catch {
    return "screenshot";
  }
}

async function persistScreenshot(buf, meta, filename) {
  const { instance_id, agent_type } = meta;
  const at = sanitizeSlug(agent_type, "agent");
  const inst = sanitizeSlug(instance_id, "instance");
  const slug = filename
    ? sanitizeSlug(filename, "screenshot")
    : `screenshot-${Date.now()}`;
  const dstDir = path.join(AGENTS_ROOT, at, "runs", inst, "screenshots");
  await fs.mkdir(dstDir, { recursive: true });
  await fs.writeFile(path.join(dstDir, `${slug}.png`), buf);
  return `/workspace/runs/${inst}/screenshots/${slug}.png`;
}

async function persistVideo(srcPath, meta, filename) {
  const { instance_id, agent_type } = meta;
  const at = sanitizeSlug(agent_type, "agent");
  const inst = sanitizeSlug(instance_id, "instance");
  const slug = filename
    ? sanitizeSlug(filename, "recording")
    : `recording-${Date.now()}`;
  const dstDir = path.join(AGENTS_ROOT, at, "runs", inst, "videos");
  await fs.mkdir(dstDir, { recursive: true });
  const dstPath = path.join(dstDir, `${slug}.webm`);
  await fs.copyFile(srcPath, dstPath);
  await fs.rm(path.dirname(srcPath), { recursive: true }).catch(() => {});
  return `/workspace/runs/${inst}/videos/${slug}.webm`;
}

async function withPage(fn) {
  const browser = await getBrowser();
  const context = await browser.newContext(await hostContextOptions());
  const page = await context.newPage();
  try {
    return await fn(page);
  } finally {
    await context.close();
  }
}

async function handleScreenshot(args) {
  const {
    url,
    viewport_width = 1440,
    viewport_height = 900,
    full_page = true,
    instance_id,
    agent_type,
    filename,
    inline,
    task_id,
  } = args;
  if (!url) throw new Error(t("errors.url_required"));

  const persisting = Boolean(instance_id && agent_type);
  const wantInline = inline === undefined ? !persisting : Boolean(inline);

  const buf = await withPage(async (page) => {
    await page.setViewportSize({ width: viewport_width, height: viewport_height });
    await page.goto(url, { waitUntil: "networkidle", timeout: DEFAULT_TIMEOUT_MS });
    return page.screenshot({ fullPage: full_page, type: "png" });
  });

  const content = [];
  let summary = t("text.screenshot_of", { url, width: viewport_width, height: viewport_height, full_page, bytes: buf.length });

  // On successful upload, do not also return base64 inline: the bytes must stay out of the model's
  // context.
  if (task_id) {
    const fn = withExtension(filename || slugFromURL(url) + `-${Date.now()}`, "png", "screenshot");
    try {
      const att = await uploadAttachment(task_id, fn, buf, "image/png");
      summary += t("text.attached", { task: task_id, file: fn, id: att.id ?? "?", bytes: buf.length });
      content.push({ type: "text", text: summary });
      return { content };
    } catch (err) {
      summary += t("text.attach_failed", { err: err.message });
    }
  }

  if (persisting) {
    try {
      const agentPath = await persistScreenshot(buf, { instance_id, agent_type }, filename || slugFromURL(url) + `-${Date.now()}`);
      summary += t("text.saved", { path: agentPath });
    } catch (err) {
      summary += t("text.persist_failed", { err: err.message });
      content.push({ type: "image", data: buf.toString("base64"), mimeType: "image/png" });
    }
  }

  if (wantInline && !content.length) {
    content.push({ type: "image", data: buf.toString("base64"), mimeType: "image/png" });
  }
  content.push({ type: "text", text: summary });
  return { content };
}

async function handleGetText(args) {
  const { url } = args;
  const text = await withPage(async (page) => {
    await page.goto(url, { waitUntil: "networkidle", timeout: DEFAULT_TIMEOUT_MS });
    return page.evaluate(() => document.body?.innerText ?? "");
  });
  return { content: [{ type: "text", text }] };
}

async function handleNewSession(args) {
  const {
    viewport_width = 1440,
    viewport_height = 900,
    instance_id,
    agent_type,
    record_video = false,
    video_filename,
    task_id,
    ignore_errors = [],
    blind = false,
  } = args;
  // Refuse a bad profile or pattern before a context exists.
  const profile = emulation(args);
  ignorePatterns(ignore_errors);
  const browser = await getBrowser();
  const contextOptions = { viewport: { width: viewport_width, height: viewport_height }, ...(await hostContextOptions()) };
  if (profile.color_scheme) contextOptions.colorScheme = profile.color_scheme;
  let videoDir = null;
  if (record_video) {
    videoDir = `/tmp/thulu-video-${randomUUID()}`;
    await fs.mkdir(videoDir, { recursive: true });
    contextOptions.recordVideo = { dir: videoDir, size: { width: viewport_width, height: viewport_height } };
  }
  const context = await browser.newContext(contextOptions);
  const page = await context.newPage();
  await throttleContext(context, profile);
  const findings = findingsFor(page);
  findings.ignore(ignore_errors);
  findings.protect(callerKey.getStore());
  await perfFor(page).ready;
  const id = randomUUID();
  sessions.set(id, {
    context,
    page,
    lastActivity: Date.now(),
    log: [],
    steps: [],
    meta: { instance_id, agent_type, viewport_width, viewport_height, videoDir, video_filename, task_id,
      emulation: profile, ignore_errors, blind: Boolean(blind) },
    journey: newJourney(),
  });
  const who = { id, agent: agent_type || "unknown", instance: instance_id || "unknown" };
  console.log(record_video ? tLog("log.session_created_recording", who) : tLog("log.session_created", who));
  return {
    content: [
      {
        type: "text",
        text: [
          t("text.session_created", { id, width: viewport_width, height: viewport_height }),
          Object.keys(profile).length ? t("text.session_profile", { profile: describeProfile(profile) }) : null,
          record_video ? t("text.session_recording") : null,
          blind ? t("text.session_blind") : null,
          t("text.session_usage", { minutes: SESSION_IDLE_MS / 60000 }),
        ].filter(Boolean).join("\n"),
      },
    ],
  };
}

// Every action runs through here: it opens a step (findings that arrive from now until the next step
// starts belong to it) and, when measured, times how long the page took to respond. `from: "end"`
// starts the clock after fn returns, for typing, where the keystrokes themselves are not the wait.
async function act(s, tool, fn, { measure = true, from = "start" } = {}) {
  const step = { n: (s.steps.at(-1)?.n ?? 0) + 1, tool, start: Date.now() };
  s.steps.push(step);
  if (s.steps.length > MAX_STEPS) s.steps.splice(0, s.steps.length - MAX_STEPS);
  const perf = perfFor(s.page);
  const before = s.page.url();
  let timer = null;
  try {
    if (measure) await perf.ready;
    if (measure && from === "start") timer = perf.begin();
    const out = await fn();
    if (measure && !timer) timer = perf.begin();
    if (timer) step.perf = await timer.end();
    if (out?.what) step.what = out.what;
    step.findings = groupFindings(findingsFor(s.page).since(step.start), [{ step: step.n, start: step.start }]).findings;
    trackJourney(s.journey, step, { before, after: s.page.url(), back: tool === "browser_go_back" && out?.moved !== false });
    return { out, step };
  } catch (err) {
    step.error = String(err?.message ?? err).split("\n")[0];
    trackJourney(s.journey, step, { before, after: s.page.url(), back: false });
    throw err;
  }
}

// How a person got through the page, which a blind tester's run is judged on. The stage agent
// compares it with the spec's shortest path; the tool only counts.
function newJourney() {
  return { actions: 0, navigations: 0, backs: 0, dead: 0, errors: 0, first: 0, last: 0, seen: new Set() };
}

function trackJourney(j, step, { before, after, back }) {
  if (!j) return;
  j.actions += 1;
  if (!j.first) j.first = step.start;
  j.last = Date.now();
  if (before && before !== "about:blank") j.seen.add(pageKey(before));
  const moved = Boolean(after) && after !== before && after !== "about:blank";
  if (moved || back) {
    j.navigations += 1;
    // A page already seen is a way back, whether by go_back or by clicking to it again.
    if (back || j.seen.has(pageKey(after))) j.backs += 1;
  }
  if (after && after !== "about:blank") j.seen.add(pageKey(after));
  if (step.perf?.dead) j.dead += 1;
  j.errors += (step.findings ?? []).reduce((n, f) => n + f.count, 0);
}

function journeyOf(j) {
  return { actions: j.actions, navigations: j.navigations, backs: j.backs, dead_clicks: j.dead, errors: j.errors,
    time_on_task_ms: j.first ? j.last - j.first : 0 };
}

function pageKey(u) {
  try {
    const x = new URL(u);
    return `${x.origin}${x.pathname}`;
  } catch {
    return u;
  }
}

// A blind session hides how the page is built: no selectors, no code, no log of them.
function refuseBlind(s, tool) {
  if (s?.meta?.blind) throw new Error(t("errors.blind_tool", { tool }));
}

// What a step adds to its log entry: the step number, its timing and what the page complained about.
function stepFields(step) {
  return {
    step: step.n,
    ...(step.perf ? { perf: step.perf } : {}),
    ...(step.findings?.length ? { findings: step.findings } : {}),
  };
}

// The lines a step adds to the tool's answer.
function stepLines(step) {
  const lines = [];
  if (step.perf) lines.push(describeAction(step.perf));
  const found = step.findings ?? [];
  if (found.length) {
    const total = found.reduce((n, f) => n + f.count, 0);
    lines.push(t("text.step_findings", { count: total, list: found.slice(0, 3).map(describeFinding).join("; ") }));
  }
  return lines.length ? `\n${lines.join("\n")}` : "";
}

function stepWindows(s) {
  return s.steps.map((st) => ({ step: st.n, start: st.start }));
}

async function handleCloseSession(args) {
  const { session_id } = args;
  const s = sessions.get(session_id);
  if (!s) {
    return { content: [{ type: "text", text: t("text.session_gone", { id: session_id }) }] };
  }
  const unfinished = s.take?.title;
  await discardSessionTake(s);
  // Closing the context finalizes the .webm; it must happen before the video is read.
  try { await s.context.close(); } catch {}
  sessions.delete(session_id);
  console.log(tLog("log.session_closed", { id: session_id }));

  let videoLine = "";
  if (s.meta.videoDir) {
    try {
      const files = await fs.readdir(s.meta.videoDir);
      const webm = files.find((f) => f.endsWith(".webm"));
      if (webm) {
        const agentPath = await persistVideo(
          path.join(s.meta.videoDir, webm),
          s.meta,
          s.meta.video_filename
        );
        videoLine = t("text.video_saved", { path: agentPath });
      } else {
        videoLine = t("text.video_missing");
      }
    } catch (err) {
      videoLine = t("text.video_persist_failed", { err: err.message });
    }
  }

  return {
    content: [
      {
        type: "text",
        text: t("text.session_closed", { id: session_id, count: s.log.length }) + videoLine +
          (unfinished ? t("text.take_unfinished", { title: unfinished }) : ""),
      },
    ],
  };
}

// Inside a take the Director plays and times the action; the step takes its measure from there.
async function recorded(s, tool, kind, args) {
  if (kind === "type" && args.secret) findingsFor(s.page).protect(args.text);
  const before = s.take.metrics.length;
  const { out: played, step } = await act(s, tool, async () => {
    const st = await recordAction(s, kind, args);
    return { st, what: st.label || st.target || st.url || st.key || st.direction || "" };
  }, { measure: false });
  if (s.take.metrics.length > before) {
    const { step: _chapter, label: _label, kind: _kind, ...perf } = s.take.metrics.at(-1);
    step.perf = perf;
  }
  const logged = { ...args, session_id: undefined, ...(args.secret ? { text: undefined } : {}) };
  if (played.st.target) logged.selector = played.st.target;
  s.log.push({ t: Date.now(), tool, args: logged, ...stepFields(step) });
  const st = played.st;
  // A blind tester hears neither the selector the take resolved nor the address, as in browser_get_state.
  const what = st.label || (s.meta.blind ? st.kind
    : [st.kind, st.target || st.url || st.key || st.direction || ""].join(" ").trim());
  const said = s.meta.blind ? t("text.recorded_blind", { what, title: await s.page.title() })
    : t("text.recorded", { what, url: s.page.url() });
  return { content: [{ type: "text", text: said + stepLines(step) }] };
}

async function handleNavigate(args) {
  const { session_id, url, wait_until = "networkidle" } = args;
  const s = requireSession(session_id);
  // A blind tester gets the start URL and finds the rest through the page, as a person would. A
  // first navigation that failed does not use it up.
  if (s.meta.blind && s.meta.navigated) throw new Error(t("errors.blind_navigate"));
  if (s.take) {
    const out = await recorded(s, "browser_navigate", "goto", args);
    s.meta.navigated = true;
    return out;
  }
  const { step } = await act(s, "browser_navigate", async () => {
    await s.page.goto(url, { waitUntil: wait_until, timeout: DEFAULT_TIMEOUT_MS });
    return { what: url };
  });
  s.meta.navigated = true;
  const title = await s.page.title();
  const finalUrl = s.page.url();
  s.log.push({ t: Date.now(), tool: "browser_navigate", args: { url, wait_until }, ...stepFields(step) });
  return {
    content: [{ type: "text", text: t("text.navigated", { url: finalUrl, title }) + stepLines(step) }],
  };
}

async function handleGetState(args) {
  const { session_id } = args;
  const s = requireSession(session_id);
  if (s.meta.blind) return blindState(s);
  const elements = await serializeDOM(s.page);
  const url = s.page.url();
  const title = await s.page.title();
  const text = [
    t("text.state_header", { title, url }),
    ...elements,
    ``,
    t("text.state_total", { count: elements.length }),
  ].join("\n");
  return { content: [{ type: "text", text }] };
}

function stateLabel(state) {
  switch (state) {
    case "disabled": return t("labels.state_disabled");
    case "checked": return t("labels.state_checked");
    case "expanded": return t("labels.state_expanded");
    case "collapsed": return t("labels.state_collapsed");
    case "required": return t("labels.state_required");
    case "invalid": return t("labels.state_invalid");
    default: return state;
  }
}

// What a screen reader would announce: role, name and state, the page title and its host only.
async function blindState(s) {
  const elements = await accessibleElements(s.page);
  let host = "";
  try {
    host = new URL(s.page.url()).host;
  } catch {}
  const lines = elements.map((e) => {
    const extra = [
      e.value ? t("text.blind_value", { value: e.value }) : null,
      ...e.states.map((st) => stateLabel(st)),
      e.host ? t("text.blind_link_host", { host: e.host }) : null,
    ].filter(Boolean);
    const name = e.name ? JSON.stringify(e.name) : t("text.blind_unnamed");
    return t("text.blind_element", { n: e.n, role: e.role, name, extra: extra.length ? ` (${extra.join(", ")})` : "" });
  });
  const text = [
    t("text.blind_state_header", { title: await s.page.title(), host }),
    ...lines,
    ``,
    t("text.state_total", { count: elements.length }),
  ].join("\n");
  return { content: [{ type: "text", text }] };
}

async function handleGoBack(args) {
  const { session_id } = args;
  const s = requireSession(session_id);
  if (s.take) throw new Error(t("errors.go_back_in_take"));
  const { out, step } = await act(s, "browser_go_back", async () => {
    const from = s.page.url();
    const res = await s.page.goBack({ waitUntil: "load", timeout: DEFAULT_TIMEOUT_MS });
    // Back from the session's first page lands on the blank tab it opened with (and answers null, as
    // a same-document back does): return to the page, or a blind tester is left with nothing.
    if (s.page.url() === "about:blank" && from !== "about:blank") {
      await s.page.goForward({ waitUntil: "load", timeout: DEFAULT_TIMEOUT_MS });
      return { what: s.page.url(), moved: false };
    }
    return { what: s.page.url(), moved: res !== null || s.page.url() !== from };
  });
  s.log.push({ t: Date.now(), tool: "browser_go_back", args: {}, ...stepFields(step) });
  if (!out.moved) return { content: [{ type: "text", text: t("text.go_back_none") }] };
  const where = s.meta.blind ? await s.page.title() : s.page.url();
  return { content: [{ type: "text", text: t("text.went_back", { where }) + stepLines(step) }] };
}

async function handleGetTextSession(args) {
  const { session_id } = args;
  const s = requireSession(session_id);
  const text = await s.page.evaluate(() => document.body?.innerText ?? "");
  return { content: [{ type: "text", text }] };
}

async function handleScreenshotSession(args) {
  const { session_id, filename, inline, full_page = false, task_id } = args;
  const s = requireSession(session_id);
  const { instance_id, agent_type, viewport_width, viewport_height } = s.meta;
  const taskId = task_id || s.meta.task_id;
  const persisting = Boolean(instance_id && agent_type);
  const wantInline = inline === undefined ? !persisting : Boolean(inline);

  const buf = await s.page.screenshot({ fullPage: full_page, type: "png" });
  const content = [];
  let summary = t("text.screenshot_session", { width: viewport_width, height: viewport_height, full_page, bytes: buf.length, url: s.page.url() });

  if (taskId) {
    const fn = withExtension(filename, "png", "screenshot");
    try {
      const att = await uploadAttachment(taskId, fn, buf, "image/png");
      summary += t("text.attached", { task: taskId, file: fn, id: att.id ?? "?", bytes: buf.length });
      content.push({ type: "text", text: summary });
      return { content };
    } catch (err) {
      summary += t("text.attach_failed", { err: err.message });
    }
  }

  if (persisting) {
    try {
      const agentPath = await persistScreenshot(buf, { instance_id, agent_type }, filename);
      summary += t("text.saved", { path: agentPath });
    } catch (err) {
      summary += t("text.persist_failed", { err: err.message });
      content.push({ type: "image", data: buf.toString("base64"), mimeType: "image/png" });
    }
  }

  if (wantInline && !content.length) {
    content.push({ type: "image", data: buf.toString("base64"), mimeType: "image/png" });
  }
  content.push({ type: "text", text: summary });
  return { content };
}

async function handleClick(args) {
  const { session_id, index, selector } = args;
  const s = requireSession(session_id);
  if (s.take) return recorded(s, "browser_click", "click", args);
  if (index === undefined && !selector) throw new Error(t("errors.index_or_selector"));
  let info = null;
  const { step } = await act(s, "browser_click", async () => {
    if (index !== undefined) {
      info = await captureElementInfo(s.page, index);
      const el = await getElementAt(s.page, index);
      await el.click({ timeout: DEFAULT_TIMEOUT_MS });
    } else {
      await s.page.click(selector, { timeout: DEFAULT_TIMEOUT_MS });
    }
    return { what: elementWhat(info, selector) };
  });
  s.log.push({ t: Date.now(), tool: "browser_click", args: { index, selector }, elementInfo: info, ...stepFields(step) });
  const newUrl = s.page.url();
  return {
    content: [
      {
        type: "text",
        text: t("text.clicked", { element: info ? ` <${info.tag}>${info.text ? ` "${info.text}"` : ""}` : "", url: newUrl }) +
          stepLines(step),
      },
    ],
  };
}

function elementWhat(info, selector) {
  if (!info) return selector || "";
  return `<${info.tag}>${info.text ? ` "${info.text}"` : ""}`;
}

async function handleType(input) {
  const s = requireSession(input.session_id);
  const args = await typedText(input);
  const { index, selector, text, clear_first = true, secret } = args;
  if (s.take) return recorded(s, "browser_type", "type", args);
  if (index === undefined && !selector) throw new Error(t("errors.index_or_selector"));
  if (secret) findingsFor(s.page).protect(text);
  let info = null;
  const { step } = await act(s, "browser_type", async () => {
    if (index !== undefined) {
      info = await captureElementInfo(s.page, index);
      const el = await getElementAt(s.page, index);
      if (clear_first) await el.fill("");
      await el.type(text);
    } else {
      if (clear_first) await s.page.fill(selector, "");
      await s.page.type(selector, text);
    }
    return { what: elementWhat(info, selector) };
  }, { from: "end" });
  const shown = secret ? t("text.secret_value", { name: secret }) : `"${text}"`;
  s.log.push({ t: Date.now(), tool: "browser_type", args: { index, selector, text: secret ? undefined : text, secret, clear_first },
    elementInfo: info, ...stepFields(step) });
  return {
    content: [
      {
        type: "text",
        text: t("text.typed", {
          value: shown,
          target: info ? t("text.typed_into", { element: `<${info.tag}>${info.placeholder ? ` (${info.placeholder})` : ""}` }) : "",
        }) + stepLines(step),
      },
    ],
  };
}

async function handleSelect(args) {
  const { session_id, index, selector, value } = args;
  const s = requireSession(session_id);
  if (s.take) return recorded(s, "browser_select", "select", args);
  if (index === undefined && !selector) throw new Error(t("errors.index_or_selector"));
  let info = null;
  const { step } = await act(s, "browser_select", async () => {
    if (index !== undefined) {
      info = await captureElementInfo(s.page, index);
      const el = await getElementAt(s.page, index);
      await el.selectOption(value);
    } else {
      await s.page.selectOption(selector, value);
    }
    return { what: elementWhat(info, selector) };
  });
  s.log.push({ t: Date.now(), tool: "browser_select", args: { index, selector, value }, elementInfo: info, ...stepFields(step) });
  return {
    content: [
      {
        type: "text",
        text: t("text.selected", {
          value,
          target: info ? t("text.selected_in", { element: `<select>${info.name && !s.meta.blind ? ` name="${info.name}"` : ""}` }) : "",
        }) + stepLines(step),
      },
    ],
  };
}

async function handleScroll(args) {
  const { session_id, direction, amount = 500 } = args;
  const s = requireSession(session_id);
  if (s.take) return recorded(s, "browser_scroll", "scroll", args);
  const scrollMap = {
    down: `window.scrollBy(0, ${amount})`,
    up: `window.scrollBy(0, -${amount})`,
    top: "window.scrollTo(0, 0)",
    bottom: "window.scrollTo(0, document.body.scrollHeight)",
  };
  const expr = scrollMap[direction];
  if (!expr) throw new Error(t("errors.scroll_direction"));
  const { step } = await act(s, "browser_scroll", async () => {
    await s.page.evaluate(expr);
    return { what: direction };
  });
  s.log.push({ t: Date.now(), tool: "browser_scroll", args: { direction, amount }, ...stepFields(step) });
  return {
    content: [
      {
        type: "text",
        text: (direction === "up" || direction === "down"
          ? t("text.scrolled_by", { direction, amount })
          : t("text.scrolled", { direction })) + stepLines(step),
      },
    ],
  };
}

async function handleHover(args) {
  const { session_id, index, selector } = args;
  const s = requireSession(session_id);
  if (s.take) return recorded(s, "browser_hover", "hover", args);
  if (index === undefined && !selector) throw new Error(t("errors.index_or_selector"));
  let info = null;
  const { step } = await act(s, "browser_hover", async () => {
    if (index !== undefined) {
      info = await captureElementInfo(s.page, index);
      const el = await getElementAt(s.page, index);
      await el.hover();
    } else {
      await s.page.hover(selector);
    }
    return { what: elementWhat(info, selector) };
  });
  s.log.push({ t: Date.now(), tool: "browser_hover", args: { index, selector }, elementInfo: info, ...stepFields(step) });
  return {
    content: [
      {
        type: "text",
        text: t("text.hovered", { element: info ? ` <${info.tag}>${info.text ? ` "${info.text}"` : ""}` : "" }) + stepLines(step),
      },
    ],
  };
}

async function handlePressKey(args) {
  const { session_id, key } = args;
  const s = requireSession(session_id);
  if (s.take) return recorded(s, "browser_press_key", "press", args);
  const { step } = await act(s, "browser_press_key", async () => {
    await s.page.keyboard.press(key);
    return { what: key };
  });
  s.log.push({ t: Date.now(), tool: "browser_press_key", args: { key }, ...stepFields(step) });
  return { content: [{ type: "text", text: t("text.pressed", { key }) + stepLines(step) }] };
}

async function handleRunScript(args) {
  const { session_id, name, code, path: scriptPath, params = {} } = args;
  const s = requireSession(session_id);
  refuseBlind(s, "browser_run_script");

  let scriptCode;
  let scriptLabel;

  if (scriptPath) {
    const resolved = validatePath(scriptPath);
    scriptCode = await fs.readFile(resolved, "utf-8");
    scriptLabel = scriptPath;
  } else if (name) {
    const { agent_type } = s.meta;
    if (!agent_type) {
      throw new Error(t("errors.no_agent_type"));
    }
    const scriptFile = path.join(AGENTS_ROOT, agent_type, "scripts", "browser", name.endsWith(".js") ? name : `${name}.js`);
    scriptCode = await fs.readFile(scriptFile, "utf-8");
    scriptLabel = name;
  } else if (code) {
    scriptCode = code;
    scriptLabel = t("labels.inline");
  } else {
    throw new Error(t("errors.script_source_required"));
  }

  // A script opens a step for page errors, but is not timed: it may do anything, waits included.
  const { out: result, step } = await act(s, "browser_run_script", async () => {
    if (/^\s*export\s+default\s/.test(scriptCode)) {
      const tmpFile = path.join("/tmp", `thulu-script-${randomUUID()}.mjs`);
      try {
        await fs.writeFile(tmpFile, scriptCode);
        const mod = await import(tmpFile);
        return await mod.default(s.page, params);
      } finally {
        await fs.unlink(tmpFile).catch(() => {});
      }
    }
    const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
    const fn = new AsyncFunction("page", "params", scriptCode);
    return fn(s.page, params);
  }, { measure: false });
  step.what = scriptLabel;

  s.log.push({
    t: Date.now(),
    tool: "browser_run_script",
    args: { name, scriptPath, params, codeLength: scriptCode.length },
    ...stepFields(step),
  });

  return {
    content: [
      {
        type: "text",
        text: t("text.script_executed", {
          name: scriptLabel,
          result: result !== undefined ? JSON.stringify(result, null, 2) : t("labels.no_return_value"),
        }),
      },
    ],
  };
}

async function handleListScripts(args) {
  const { session_id, dir } = args;
  let scriptDir = dir;

  if (session_id) refuseBlind(sessions.get(session_id), "browser_list_scripts");
  if (!scriptDir && session_id) {
    const s = sessions.get(session_id);
    if (s?.meta?.agent_type) {
      scriptDir = path.join(AGENTS_ROOT, s.meta.agent_type, "scripts", "browser");
    }
  }
  if (!scriptDir) {
    throw new Error(t("errors.dir_required"));
  }

  let files;
  try {
    files = await fs.readdir(scriptDir);
  } catch {
    return {
      content: [{ type: "text", text: t("text.no_scripts_dir", { dir: scriptDir }) }],
    };
  }

  const jsFiles = files.filter((f) => f.endsWith(".js") || f.endsWith(".mjs"));
  if (!jsFiles.length) {
    return { content: [{ type: "text", text: t("text.no_scripts", { dir: scriptDir }) }] };
  }

  const descriptions = await Promise.all(
    jsFiles.map(async (f) => {
      const content = await fs.readFile(path.join(scriptDir, f), "utf-8");
      const match = content.match(/\/\/\s*description:\s*(.+)/i);
      const desc = match ? match[1].trim() : t("labels.no_description");
      return `  ${f.replace(/\.m?js$/, "")} — ${desc}`;
    })
  );

  return {
    content: [
      {
        type: "text",
        text: t("text.scripts_in", { dir: scriptDir, list: descriptions.join("\n") }),
      },
    ],
  };
}

async function handleGetSessionLog(args) {
  const { session_id } = args;
  const s = requireSession(session_id);
  refuseBlind(s, "browser_get_session_log");
  return {
    content: [
      {
        type: "text",
        text: JSON.stringify(s.log, null, 2),
      },
    ],
  };
}

function sessionFindings(s) {
  return findingsFor(s.page).group(stepWindows(s));
}

function stepVitals(perf) {
  const v = perf?.vitals ?? {};
  return Object.fromEntries(VITALS.map((m) => [m, v[m] ?? null]));
}

async function handleMetrics(args) {
  const { session_id, format = "text" } = args;
  const s = requireSession(session_id);
  const viewport = { width: s.meta.viewport_width, height: s.meta.viewport_height };
  const page = await perfFor(s.page).snapshot();
  const { findings } = sessionFindings(s);
  const counts = countByKind(findings);
  const steps = s.steps.filter((st) => st.perf).slice(-METRICS_STEPS);
  if (format === "json") {
    const out = {
      profile: { ...s.meta.emulation, viewport },
      page,
      steps: steps.map((st) => ({
        step: st.n, tool: st.tool, what: st.what ?? "", url: st.perf.vitals?.url ?? "",
        response_ms: st.perf.response_ms, first_ms: st.perf.first_ms, event_ms: st.perf.event_ms,
        settled_ms: st.perf.settled_ms, requests: st.perf.requests, dead: st.perf.dead,
        ...stepVitals(st.perf), ...(st.perf.vitals?.inp_bound ? { inp_bound: true } : {}),
      })),
      findings: counts,
      journey: journeyOf(s.journey),
    };
    return { content: [{ type: "text", text: JSON.stringify(out, null, 2) }] };
  }
  const lines = [
    t("text.metrics_header", { profile: describeProfile(s.meta.emulation, { viewport }) }),
    page.url ? t("text.metrics_page", { url: page.url, vitals: describeVitals(page) || t("labels.no_data") }) : null,
  ];
  if (steps.length) {
    lines.push("", t("text.metrics_steps", { count: steps.length }));
    for (const st of steps) {
      const response = st.perf.dead ? t("text.metrics_dead") : formatMetric("response", st.perf.response_ms);
      const vitals = describeVitals(st.perf.vitals, { ratings: false });
      lines.push(t("text.metrics_step", { n: st.n, tool: st.tool, what: st.what ?? "", response,
        vitals: vitals || t("labels.no_data") }));
    }
  } else {
    lines.push("", t("text.metrics_no_steps"));
  }
  lines.push("", t("text.metrics_findings", { http: counts.http, failed: counts.failed, console: counts.console,
    pageerror: counts.pageerror }));
  const j = journeyOf(s.journey);
  lines.push(t("text.journey_line", { actions: j.actions, navigations: j.navigations, backs: j.backs,
    dead: j.dead_clicks, errors: j.errors, time: formatMetric("response", j.time_on_task_ms) }));
  return { content: [{ type: "text", text: lines.filter((l) => l !== null).join("\n") }] };
}

async function handleFindings(args) {
  const { session_id } = args;
  const s = requireSession(session_id);
  const { findings, dropped } = sessionFindings(s);
  if (!findings.length) return { content: [{ type: "text", text: t("text.findings_none") }] };
  const lines = [t("text.findings_header", { count: findings.reduce((n, f) => n + f.count, 0) })];
  const byStep = new Map();
  for (const f of findings) byStep.set(f.step, [...(byStep.get(f.step) ?? []), f]);
  for (const [n, list] of byStep) {
    const st = s.steps.find((x) => x.n === n);
    lines.push("", t("text.findings_step", { n, tool: st?.tool ?? "", what: st?.what ?? "" }));
    for (const f of list) lines.push(`  - ${describeFinding(f)}`);
  }
  if (dropped) lines.push("", t("text.findings_dropped", { count: dropped }));
  return { content: [{ type: "text", text: lines.join("\n") }] };
}

async function handleWait(args) {
  const { session_id, ms } = args;
  const s = requireSession(session_id);
  if (s.take) return recorded(s, "browser_wait", "beat", args);
  const clamped = Math.min(Math.max(Number(ms) || 1000, 0), 10_000);
  await new Promise((r) => setTimeout(r, clamped));
  s.log.push({ t: Date.now(), tool: "browser_wait", args: { ms: clamped } });
  return { content: [{ type: "text", text: t("text.waited", { ms: clamped }) }] };
}

async function handleExportScript(args) {
  const { session_id, path: outputPath, description = "" } = args;
  const s = requireSession(session_id);
  refuseBlind(s, "browser_export_script");

  const lines = [
    // i18n-ignore the header browser_list_scripts parses
    `// description: ${description || t("text.exported_from_session", { id: session_id })}`,
    t("text.export_generated", { date: new Date().toISOString() }),
    t("text.export_steps", { count: s.log.length }),
    ``,
    `import { chromium } from "playwright";`, // i18n-ignore generated Playwright code
    ``,
    `export default async function run(page, params = {}) {`, // i18n-ignore generated Playwright code
  ];

  for (const entry of s.log) {
    const { tool, args: a, elementInfo: ei } = entry;
    switch (tool) {
      case "browser_navigate":
        lines.push(`  await page.goto("${a.url}", { waitUntil: "${a.wait_until || "networkidle"}" });`); // i18n-ignore generated Playwright code
        break;
      case "browser_click":
        if (ei?.selector) lines.push(`  await page.click("${ei.selector}"); // ${ei.text || ""}`); // i18n-ignore generated Playwright code
        else if (a.selector) lines.push(`  await page.click("${a.selector}");`); // i18n-ignore generated Playwright code
        else lines.push(t("text.export_todo_click", { index: a.index }));
        break;
      case "browser_type":
        if (ei?.selector || a.selector) {
          const value = a.secret ? `params[${JSON.stringify(a.secret)}]` : JSON.stringify(a.text);
          lines.push(`  await page.fill(${JSON.stringify(ei?.selector || a.selector)}, ${value});`); // i18n-ignore generated Playwright code
        }
        else lines.push(t("text.export_todo_type", { index: a.index, text: a.text }));
        break;
      case "browser_select":
        if (ei?.selector) lines.push(`  await page.selectOption("${ei.selector}", "${a.value}");`); // i18n-ignore generated Playwright code
        else if (a.selector) lines.push(`  await page.selectOption("${a.selector}", "${a.value}");`); // i18n-ignore generated Playwright code
        else lines.push(t("text.export_todo_select", { index: a.index, value: a.value }));
        break;
      case "browser_scroll":
        if (a.direction === "top") lines.push(`  await page.evaluate(() => window.scrollTo(0, 0));`); // i18n-ignore generated Playwright code
        else if (a.direction === "bottom") lines.push(`  await page.evaluate(() => window.scrollTo(0, document.body.scrollHeight));`); // i18n-ignore generated Playwright code
        else lines.push(`  await page.evaluate(() => window.scrollBy(0, ${a.direction === "up" ? -a.amount : a.amount}));`); // i18n-ignore generated Playwright code
        break;
      case "browser_hover":
        if (ei?.selector) lines.push(`  await page.hover("${ei.selector}");`); // i18n-ignore generated Playwright code
        else if (a.selector) lines.push(`  await page.hover("${a.selector}");`); // i18n-ignore generated Playwright code
        break;
      case "browser_press_key":
        lines.push(`  await page.keyboard.press("${a.key}");`); // i18n-ignore generated Playwright code
        break;
      case "browser_wait":
        lines.push(`  await page.waitForTimeout(${a.ms});`); // i18n-ignore generated Playwright code
        break;
      case "browser_run_script":
        lines.push(`  // browser_run_script: ${a.name || a.scriptPath || "(inline)"}`);
        break;
      default:
        lines.push(`  // ${tool}: ${JSON.stringify(a)}`);
    }
  }

  lines.push(`}`);
  const scriptCode = lines.join("\n");

  if (outputPath) {
    const resolved = validatePath(outputPath);
    await fs.mkdir(path.dirname(resolved), { recursive: true });
    await fs.writeFile(resolved, scriptCode);
    return {
      content: [
        {
          type: "text",
          text: t("text.script_exported", { path: outputPath, code: scriptCode }),
        },
      ],
    };
  }

  return { content: [{ type: "text", text: scriptCode }] };
}

const TOOLS = [
  {
    name: "screenshot",
    description: t("tools.screenshot.description"),
    inputSchema: {
      type: "object",
      properties: {
        url: { type: "string" },
        viewport_width: { type: "integer", default: 1440 },
        viewport_height: { type: "integer", default: 900 },
        full_page: { type: "boolean", default: true },
        task_id: { type: "string", description: t("tools.screenshot.params.task_id") },
        filename: { type: "string", description: t("tools.screenshot.params.filename") },
        instance_id: { type: "string", description: t("tools.screenshot.params.instance_id") },
        agent_type: { type: "string", description: t("tools.screenshot.params.agent_type") },
        inline: { type: "boolean", description: t("tools.screenshot.params.inline") },
      },
      required: ["url"],
    },
  },
  {
    name: "get_text",
    description: t("tools.get_text.description"),
    inputSchema: {
      type: "object",
      properties: {
        url: { type: "string" },
      },
      required: ["url"],
    },
  },

  {
    name: "browser_new_session",
    description: t("tools.browser_new_session.description"),
    inputSchema: {
      type: "object",
      properties: {
        viewport_width: { type: "integer", default: 1440 },
        viewport_height: { type: "integer", default: 900 },
        task_id: { type: "string", description: t("tools.browser_new_session.params.task_id") },
        instance_id: { type: "string", description: t("tools.browser_new_session.params.instance_id") },
        agent_type: { type: "string", description: t("tools.browser_new_session.params.agent_type") },
        record_video: { type: "boolean", default: false, description: t("tools.browser_new_session.params.record_video") },
        video_filename: { type: "string", description: t("tools.browser_new_session.params.video_filename", { timestamp: "{timestamp}" }) },
        network: { type: "string", enum: NETWORKS, description: t("tools.browser_new_session.params.network") },
        cpu_throttle: { type: "number", minimum: 1, maximum: CPU_MAX,
          description: t("tools.browser_new_session.params.cpu_throttle") },
        color_scheme: { type: "string", enum: COLOR_SCHEMES, description: t("tools.browser_new_session.params.color_scheme") },
        ignore_errors: { type: "array", items: { type: "string" },
          description: t("tools.browser_new_session.params.ignore_errors") },
        blind: { type: "boolean", default: false, description: t("tools.browser_new_session.params.blind") },
      },
    },
  },
  {
    name: "browser_close_session",
    description: t("tools.browser_close_session.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: { type: "string" },
      },
      required: ["session_id"],
    },
  },

  {
    name: "browser_navigate",
    description: t("tools.browser_navigate.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: { type: "string" },
        url: { type: "string" },
        wait_until: {
          type: "string",
          enum: ["load", "domcontentloaded", "networkidle"],
          default: "networkidle",
          description: t("tools.browser_navigate.params.wait_until"),
        },
        label: { type: "string", description: t("tools.browser_navigate.params.label") },
      },
      required: ["session_id", "url"],
    },
  },
  {
    name: "browser_get_state",
    description: t("tools.browser_get_state.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: { type: "string" },
      },
      required: ["session_id"],
    },
  },
  {
    name: "browser_get_text",
    description: t("tools.browser_get_text.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: { type: "string" },
      },
      required: ["session_id"],
    },
  },
  {
    name: "browser_screenshot",
    description: t("tools.browser_screenshot.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: { type: "string" },
        task_id: { type: "string", description: t("tools.browser_screenshot.params.task_id") },
        filename: { type: "string", description: t("tools.browser_screenshot.params.filename") },
        full_page: { type: "boolean", default: false },
        inline: { type: "boolean", description: t("tools.browser_screenshot.params.inline") },
      },
      required: ["session_id"],
    },
  },

  {
    name: "browser_click",
    description: t("tools.browser_click.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: { type: "string" },
        index: { type: "integer", description: t("tools.browser_click.params.index") },
        selector: { type: "string", description: t("tools.browser_click.params.selector") },
        label: { type: "string", description: t("tools.browser_click.params.label") },
      },
      required: ["session_id"],
    },
  },
  {
    name: "browser_type",
    description: t("tools.browser_type.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: { type: "string" },
        index: { type: "integer", description: t("tools.browser_type.params.index") },
        selector: { type: "string", description: t("tools.browser_type.params.selector") },
        text: { type: "string", description: t("tools.browser_type.params.text") },
        clear_first: { type: "boolean", default: true },
        secret: {
          type: "string",
          description: t("tools.browser_type.params.secret"),
        },
        label: { type: "string", description: t("tools.browser_type.params.label") },
      },
      required: ["session_id"],
    },
  },
  {
    name: "browser_select",
    description: t("tools.browser_select.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: { type: "string" },
        index: { type: "integer", description: t("tools.browser_select.params.index") },
        selector: { type: "string", description: t("tools.browser_select.params.selector") },
        value: { type: "string", description: t("tools.browser_select.params.value") },
        label: { type: "string", description: t("tools.browser_select.params.label") },
      },
      required: ["session_id", "value"],
    },
  },
  {
    name: "browser_scroll",
    description: t("tools.browser_scroll.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: { type: "string" },
        direction: {
          type: "string",
          enum: ["down", "up", "top", "bottom"],
        },
        amount: {
          type: "integer",
          default: 500,
          description: t("tools.browser_scroll.params.amount"),
        },
        label: { type: "string", description: t("tools.browser_scroll.params.label") },
      },
      required: ["session_id", "direction"],
    },
  },
  {
    name: "browser_hover",
    description: t("tools.browser_hover.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: { type: "string" },
        index: { type: "integer", description: t("tools.browser_hover.params.index") },
        selector: { type: "string", description: t("tools.browser_hover.params.selector") },
        label: { type: "string", description: t("tools.browser_hover.params.label") },
      },
      required: ["session_id"],
    },
  },
  {
    name: "browser_press_key",
    description: t("tools.browser_press_key.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: { type: "string" },
        key: { type: "string", description: t("tools.browser_press_key.params.key") },
        label: { type: "string", description: t("tools.browser_press_key.params.label") },
      },
      required: ["session_id", "key"],
    },
  },

  {
    name: "browser_run_script",
    description: t("tools.browser_run_script.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: { type: "string" },
        name: { type: "string", description: t("tools.browser_run_script.params.name") },
        path: { type: "string", description: t("tools.browser_run_script.params.path") },
        code: { type: "string", description: t("tools.browser_run_script.params.code") },
        params: { type: "object", description: t("tools.browser_run_script.params.params") },
      },
      required: ["session_id"],
    },
  },
  {
    name: "browser_list_scripts",
    description: t("tools.browser_list_scripts.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: { type: "string" },
        dir: { type: "string", description: t("tools.browser_list_scripts.params.dir") },
      },
    },
  },

  {
    name: "browser_wait",
    description: t("tools.browser_wait.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: { type: "string" },
        ms: { type: "integer", description: t("tools.browser_wait.params.ms"), default: 1000 },
        label: { type: "string", description: t("tools.browser_wait.params.label") },
      },
      required: ["session_id", "ms"],
    },
  },

  {
    name: "browser_go_back",
    description: t("tools.browser_go_back.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: { type: "string" },
      },
      required: ["session_id"],
    },
  },
  {
    name: "browser_get_session_log",
    description: t("tools.browser_get_session_log.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: { type: "string" },
      },
      required: ["session_id"],
    },
  },
  {
    name: "browser_metrics",
    description: t("tools.browser_metrics.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: { type: "string" },
        format: { type: "string", enum: ["text", "json"], default: "text", description: t("tools.browser_metrics.params.format") },
      },
      required: ["session_id"],
    },
  },
  {
    name: "browser_findings",
    description: t("tools.browser_findings.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: { type: "string" },
      },
      required: ["session_id"],
    },
  },
  {
    name: "browser_export_script",
    description: t("tools.browser_export_script.description"),
    inputSchema: {
      type: "object",
      properties: {
        session_id: { type: "string" },
        path: { type: "string", description: t("tools.browser_export_script.params.path") },
        description: { type: "string", description: t("tools.browser_export_script.params.description") },
      },
      required: ["session_id"],
    },
  },
  ...EVIDENCE_TOOLS,
];

const HANDLERS = {
  screenshot: handleScreenshot,
  get_text: handleGetText,
  browser_new_session: handleNewSession,
  browser_close_session: handleCloseSession,
  browser_navigate: handleNavigate,
  browser_get_state: handleGetState,
  browser_get_text: handleGetTextSession,
  browser_screenshot: handleScreenshotSession,
  browser_click: handleClick,
  browser_type: handleType,
  browser_select: handleSelect,
  browser_scroll: handleScroll,
  browser_hover: handleHover,
  browser_press_key: handlePressKey,
  browser_run_script: handleRunScript,
  browser_list_scripts: handleListScripts,
  browser_wait: handleWait,
  browser_go_back: handleGoBack,
  browser_get_session_log: handleGetSessionLog,
  browser_metrics: handleMetrics,
  browser_findings: handleFindings,
  browser_export_script: handleExportScript,
  ...evidenceHandlers({ requireSession }),
};

function buildServer({ allowedTools, listOnly = false } = {}) {
  const exposed = allowedTools
    ? TOOLS.filter((t) => allowedTools.includes(t.name))
    : TOOLS;

  const server = new Server(
    { name: "thulu-browser", version: VERSION },
    { capabilities: { tools: {} } }
  );

  server.setRequestHandler(ListToolsRequestSchema, async () => ({ tools: exposed }));

  server.setRequestHandler(CallToolRequestSchema, async (req) => {
    if (listOnly) {
      return { isError: true, content: [{ type: "text", text: t("errors.list_only_session") }] };
    }
    if (allowedTools && !allowedTools.includes(req.params.name)) {
      return {
        isError: true,
        content: [{ type: "text", text: t("errors.tool_not_allowed", { name: req.params.name }) }],
      };
    }
    const handler = HANDLERS[req.params.name];
    if (!handler) {
      return {
        isError: true,
        content: [{ type: "text", text: t("errors.unknown_tool", { name: req.params.name }) }],
      };
    }
    try {
      return await handler(req.params.arguments ?? {});
    } catch (err) {
      return {
        isError: true,
        content: [{ type: "text", text: t("errors.tool_failed", { message: err.message ?? String(err) }) }],
      };
    }
  });

  return server;
}

// The dashboard stores the allowlist "+"-separated (commas would break the MCP_TOOLS/label
// encoding); mcp-config.sh turns it into commas,
// but stale agent images still send "+", which query parsing decodes as a space.
const TOOL_LIST_SEP = /[\s,+]+/;

function splitToolList(raw) {
  if (!raw) return null;
  return raw.split(TOOL_LIST_SEP).map((t) => t.trim()).filter(Boolean);
}

const app = express();
// Do not use express.json(): SSEServerTransport reads the raw request stream itself.

const transports = new Map();
const sessionLocales = new Map();
const sessionHostLoopback = new Set();

app.get("/health", (_req, res) =>
  res.json({ ok: true, sessions: sessions.size, version: VERSION })
);

app.get("/sse", async (req, res) => {
  const admitted = admitHandshake({
    key: keyFromRequest(req),
    catalog: String(req.query.catalog ?? "") === "1",
    requiredKey: REQUIRED_KEY,
    catalogOnly: CATALOG_ONLY,
  });
  if (!admitted.ok) {
    res.status(admitted.status).json({ error: admitted.error });
    return;
  }
  const allowedTools = grantedTools(splitToolList(String(req.query.tools ?? "")), splitToolList(GRANTED_TOOLS));
  const transport = new SSEServerTransport("/messages", res);
  transports.set(transport.sessionId, transport);
  sessionKeys.set(transport.sessionId, admitted.key);
  sessionLocales.set(transport.sessionId, String(req.query.locale ?? ""));
  if (String(req.query.host_loopback ?? "") === "1") {
    if (HOST_GATEWAY) sessionHostLoopback.add(transport.sessionId);
    else console.log(tLog("log.host_loopback_unconfigured"));
  }
  res.on("close", () => {
    transports.delete(transport.sessionId);
    sessionKeys.delete(transport.sessionId);
    sessionLocales.delete(transport.sessionId);
    sessionHostLoopback.delete(transport.sessionId);
  });
  const server = buildServer({ allowedTools, listOnly: admitted.listOnly });
  await server.connect(transport);
});

app.post("/messages", async (req, res) => {
  const sessionId = String(req.query.sessionId ?? "");
  const transport = transports.get(sessionId);
  if (!transport) {
    res.status(404).send(t("errors.mcp_session_not_found"));
    return;
  }
  // Every POST must repeat the handshake's key: knowing a session id does not grant ownership of
  // it.
  const key = sessionKeys.get(sessionId) || "";
  if (!admitMessage(key, keyFromRequest(req))) {
    res.status(401).json({ error: t("errors.message_key_mismatch") });
    return;
  }
  await callerKey.run(key, () =>
    callerHostLoopback.run(sessionHostLoopback.has(sessionId), () =>
      withLocale(sessionLocales.get(sessionId), () => transport.handlePostMessage(req, res)),
    ),
  );
});

app.listen(PORT, LISTEN_HOST, () => {
  console.log(tLog("log.listening", { version: VERSION, host: LISTEN_HOST, port: PORT }));
  let mode = tLog("log.mode_shared");
  if (CATALOG_ONLY) mode = tLog("log.mode_catalog_only");
  else if (REQUIRED_KEY) mode = tLog("log.mode_per_run");
  console.log(mode);
  console.log(tLog("log.agents_root", { path: AGENTS_ROOT }));
  if (HOST_GATEWAY) console.log(tLog("log.host_gateway", { host: HOST_GATEWAY }));
  console.log(tLog("log.session_idle", { ms: SESSION_IDLE_MS }));
  console.log(tLog("log.tools_available", { count: TOOLS.length }));
  console.log(tLog("log.sse_endpoint"));
  console.log(tLog("log.health_endpoint"));
});

const shutdown = async (sig) => {
  console.log(tLog("log.shutting_down", { signal: sig }));
  for (const s of sessions.values()) {
    await discardSessionTake(s);
    try { await s.context.close(); } catch {}
  }
  sessions.clear();
  clearEvidenceStores();
  await closeBrowser();
  await closeHostProxy();
  process.exit(0);
};
process.on("SIGTERM", () => shutdown("SIGTERM"));
process.on("SIGINT", () => shutdown("SIGINT"));

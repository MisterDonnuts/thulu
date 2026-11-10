import { t } from "../i18n";
import { fmtBytes } from "./infra";
import { fmtDate } from "./time";
import {
  badgeHTML,
  buttonHTML,
  checkHTML,
  codeBlockHTML,
  disclosureHTML,
  emptyStateHTML,
  esc,
  fieldHTML,
  historyPreviewHTML,
  sectionHeadHTML,
  type HistoryRowOptions,
} from "./ui";

export interface InitParam {
  name: string;
  description?: string;
  default?: string;
  required?: boolean;
}

export interface InitCache {
  key: string[];
  paths: string[];
}

export interface InitManifest {
  params: InitParam[];
  ports: number[];
  ready: { command?: string; timeout_seconds?: number };
  workdir: string;
  setup?: string;
  tools?: Record<string, string>;
  cache?: InitCache;
  files?: Record<string, string>;
}

export interface SetupPrewarm {
  id: string;
  setup_key: string;
  snapshot_key?: string;
  status: "running" | "ok" | "failed" | "skipped" | "pruned";
  reason?: string;
  started_at: string;
  finished_at?: string;
  size_bytes: number;
  error?: string;
}

export interface PrewarmStatus {
  available: boolean;
  eligible: boolean;
  running: boolean;
  prewarms: SetupPrewarm[];
}

export interface InitScript {
  project_id: string;
  revision: number;
  script: string;
  manifest: InitManifest;
  author_kind?: "user" | "agent";
  author_label?: string;
  note?: string;
  restored_from?: number;
  created_at?: string;
}

export interface InitRevision {
  revision: number;
  author_kind: "user" | "agent";
  author_label?: string;
  note?: string;
  restored_from?: number;
  size: number;
  created_at: string;
}

export interface InitScriptInput {
  script: string;
  manifest: InitManifest;
  note: string;
}

export interface InitParamRow {
  name: string;
  default: string;
  description: string;
  required: boolean;
}

export interface InitFileRow {
  path: string;
  content: string;
}

export interface InitForm {
  script: string;
  params: InitParamRow[];
  ports: string;
  ready: string;
  timeout: string;
  workdir: string;
  setup: string;
  tools: string;
  cacheKey: string;
  cachePaths: string;
  files: InitFileRow[];
  note: string;
}

// Mirrors the server's limits in project/initscript.go; change together. The server checks again.
export const MAX_SCRIPT_BYTES = 64 * 1024;
export const DEFAULT_TIMEOUT = 300;
export const MAX_TIMEOUT = 900;
const PARAM_NAME = /^[A-Z][A-Z0-9_]*$/;
const TOOL_NAME = /^[a-z0-9][a-z0-9._/:@+-]*$/;
const TOOL_VERSION = /^[A-Za-z0-9][A-Za-z0-9._+:-]*$/;
export const MAX_TOOLS = 16;
export const MAX_CACHE_ENTRIES = 32;
export const MAX_FILES = 32;
export const MAX_FILE_PATH_BYTES = 256;
export const MAX_FILE_BYTES = 64 * 1024;
export const MAX_FILES_TOTAL_BYTES = 256 * 1024;
// eslint-disable-next-line no-control-regex -- control characters are what it looks for
const CONTROL_CHARS = /[\u0000-\u001f\u007f]/;

const byteLen = (s: string) => new TextEncoder().encode(s).length;

export function validFilePath(path: string): boolean {
  if (!path || CONTROL_CHARS.test(path) || byteLen(path) > MAX_FILE_PATH_BYTES) return false;
  return path.split("/").every((seg) => seg !== "" && seg !== "." && seg !== "..");
}

export function parseFiles(rows: InitFileRow[]): {
  files?: Record<string, string>;
  error?: string;
} {
  const files: Record<string, string> = {};
  let total = 0;
  let count = 0;
  for (const r of rows) {
    const path = r.path.trim();
    if (!path && !r.content) continue;
    if (!path) return { error: t("lib.init_script.errors.file_path_empty") };
    if (!validFilePath(path))
      return { error: t("lib.init_script.errors.file_path", { path, max: MAX_FILE_PATH_BYTES }) };
    if (Object.hasOwn(files, path))
      return { error: t("lib.init_script.errors.file_duplicate", { path }) };
    if (r.content.includes("\u0000"))
      return { error: t("lib.init_script.errors.file_nul", { path }) };
    const size = byteLen(r.content);
    if (size > MAX_FILE_BYTES)
      return { error: t("lib.init_script.errors.file_too_large", { path, max: MAX_FILE_BYTES }) };
    total += size;
    files[path] = r.content;
    count++;
  }
  if (count > MAX_FILES)
    return { error: t("lib.init_script.errors.too_many_files", { max: MAX_FILES }) };
  if (total > MAX_FILES_TOTAL_BYTES)
    return { error: t("lib.init_script.errors.files_too_large", { max: MAX_FILES_TOTAL_BYTES }) };
  return count ? { files } : {};
}

export function filePaths(files?: Record<string, string>): string[] {
  return Object.keys(files ?? {}).sort();
}

export function parseTools(text: string): { tools: Record<string, string>; invalid: string[] } {
  const tools: Record<string, string> = {};
  const invalid: string[] = [];
  const entries: string[] = [];
  for (const chunk of text.split(/[,\n]+/)) {
    const c = chunk.trim().replace(/\s*=\s*/g, "=");
    if (!c) continue;
    const words = c.split(/\s+/);
    if (words.every((w) => w.includes("="))) entries.push(...words);
    else entries.push(c);
  }
  for (const raw of entries) {
    const at = raw.indexOf("=");
    const name = at > 0 ? raw.slice(0, at) : "";
    const version = at > 0 ? raw.slice(at + 1) : "";
    if (!TOOL_NAME.test(name) || !TOOL_VERSION.test(version) || name in tools) invalid.push(raw);
    else tools[name] = version;
  }
  return { tools, invalid };
}

export function toolsText(tools?: Record<string, string>): string {
  return Object.keys(tools ?? {})
    .sort()
    .map((k) => `${k}=${tools?.[k]}`)
    .join(", ");
}

export function parseCachePaths(text: string): { paths: string[]; invalid: string[] } {
  const paths: string[] = [];
  const invalid: string[] = [];
  for (const raw of text.split(/[,\n]+/)) {
    const p = raw.trim();
    if (!p) continue;
    const parts = p.split("/");
    if (p.startsWith("/") || parts.includes("..") || p === "." || parts[0] === ".git")
      invalid.push(p);
    else if (!paths.includes(p)) paths.push(p);
  }
  return { paths, invalid };
}

export const SCRIPT_TEMPLATE =
  // i18n-ignore the shebang, not a message
  "#!/usr/bin/env bash\nset -euo pipefail\n\n";

export function parsePorts(text: string): { ports: number[]; invalid: string[] } {
  const ports: number[] = [];
  const invalid: string[] = [];
  for (const raw of text.split(/[\s,]+/)) {
    if (!raw) continue;
    const n = Number(raw);
    if (!Number.isInteger(n) || n < 1 || n > 65535) invalid.push(raw);
    else if (!ports.includes(n)) ports.push(n);
  }
  return { ports, invalid };
}

export function formToInput(f: InitForm): { input?: InitScriptInput; error?: string } {
  if (!f.script.trim()) return { error: t("lib.init_script.errors.script_empty") };
  if (new TextEncoder().encode(f.script).length > MAX_SCRIPT_BYTES)
    return { error: t("lib.init_script.errors.script_too_large", { max: MAX_SCRIPT_BYTES }) };

  const params: InitParam[] = [];
  const seen = new Set<string>();
  for (const p of f.params) {
    const name = p.name.trim();
    if (!name && !p.default && !p.description.trim()) continue;
    if (!PARAM_NAME.test(name)) return { error: t("lib.init_script.errors.param_name", { name }) };
    if (name.startsWith("OCTOPUS_"))
      return { error: t("lib.init_script.errors.param_reserved", { name }) };
    if (seen.has(name)) return { error: t("lib.init_script.errors.param_duplicate", { name }) };
    seen.add(name);
    const out: InitParam = { name };
    if (p.default) out.default = p.default;
    if (p.description.trim()) out.description = p.description.trim();
    if (p.required) out.required = true;
    params.push(out);
  }

  const { ports, invalid } = parsePorts(f.ports);
  if (invalid.length)
    return { error: t("lib.init_script.errors.ports", { ports: invalid.join(", ") }) };

  let timeout = DEFAULT_TIMEOUT;
  if (f.timeout.trim()) {
    timeout = Number(f.timeout);
    if (!Number.isInteger(timeout) || timeout < 1 || timeout > MAX_TIMEOUT)
      return { error: t("lib.init_script.errors.timeout", { max: MAX_TIMEOUT }) };
  }

  const workdir = f.workdir.trim() || ".";
  if (workdir.startsWith("/") || workdir.split("/").includes(".."))
    return { error: t("lib.init_script.errors.workdir") };

  if (new TextEncoder().encode(f.setup).length > MAX_SCRIPT_BYTES)
    return { error: t("lib.init_script.errors.setup_too_large", { max: MAX_SCRIPT_BYTES }) };
  const tools = parseTools(f.tools);
  if (tools.invalid.length)
    return { error: t("lib.init_script.errors.tools", { tools: tools.invalid.join(", ") }) };
  if (Object.keys(tools.tools).length > MAX_TOOLS)
    return { error: t("lib.init_script.errors.too_many_tools", { max: MAX_TOOLS }) };
  const key = parseCachePaths(f.cacheKey);
  const paths = parseCachePaths(f.cachePaths);
  const badPath = [...key.invalid, ...paths.invalid];
  if (badPath.length)
    return { error: t("lib.init_script.errors.cache_path", { paths: badPath.join(", ") }) };
  if (key.paths.length > MAX_CACHE_ENTRIES || paths.paths.length > MAX_CACHE_ENTRIES)
    return { error: t("lib.init_script.errors.too_many_cache", { max: MAX_CACHE_ENTRIES }) };
  const files = parseFiles(f.files);
  if (files.error) return { error: files.error };

  const manifest: InitManifest = {
    params,
    ports,
    ready: { command: f.ready.trim(), timeout_seconds: timeout },
    workdir,
  };
  if (f.setup.trim()) manifest.setup = f.setup;
  if (Object.keys(tools.tools).length) manifest.tools = tools.tools;
  if (key.paths.length || paths.paths.length)
    manifest.cache = { key: key.paths, paths: paths.paths };
  if (files.files) manifest.files = files.files;

  return { input: { script: f.script, manifest, note: f.note.trim() } };
}

// Matches the signature format the daemon writes: "agent <name> · task <id> · stage <id>".
export function parseAgentLabel(
  label?: string,
): { agent: string; task: string; stage: string } | null {
  const m = /^agent (.+) · task (\S+) · stage (\S+)$/.exec(label ?? "");
  return m ? { agent: m[1], task: m[2], stage: m[3] } : null;
}

let stageLabel: (id: string) => string = (id) => id;

export function setStageLabels(fn: (id: string) => string): void {
  stageLabel = fn;
}

const shortTask = (id: string) => id.slice(0, 6);

export function authorText(r: { author_kind?: string; author_label?: string }): string {
  const run = parseAgentLabel(r.author_label);
  if (run)
    return t("lib.init_script.author.agent_run", {
      agent: run.agent,
      stage: stageLabel(run.stage),
      task: shortTask(run.task),
    });
  if (r.author_label) return r.author_label;
  return r.author_kind === "agent"
    ? t("lib.init_script.author.agent")
    : t("lib.init_script.author.unknown");
}

export function authorHTML(r: { author_kind?: string; author_label?: string }): string {
  const run = parseAgentLabel(r.author_label);
  if (!run) return esc(authorText(r));
  const href = `/tasks/#task-${encodeURIComponent(run.task)}`;
  const task = `<a class="is-task-link" href="${esc(href)}">${esc(t("lib.init_script.author.task", { task: shortTask(run.task) }))}</a>`;
  return t("lib.init_script.author.agent_run_html", {
    agent: esc(run.agent),
    stage: esc(stageLabel(run.stage)),
    task,
  });
}

export function statusHTML(sc: InitScript): string {
  if (!sc.revision) return `<span>${esc(t("lib.init_script.status.none"))}</span>`;
  const badge = authorBadge(sc.author_kind);
  return `<span>${t("lib.init_script.status.revision_html", {
    revision: sc.revision,
    author: authorHTML(sc),
    date: esc(sc.created_at ? fmtDate(sc.created_at) : ""),
  })}</span>${badge}`;
}

function authorBadge(kind?: string): string {
  return kind === "agent"
    ? badgeHTML(t("lib.init_script.author.agent_badge"), "accent", {
        shape: "pill",
        edge: "solid",
        icon: "i-bot",
      })
    : "";
}

export function paramRowHTML(p: InitParam, i: number, readOnly: boolean): string {
  const dis = readOnly ? " disabled" : "";
  const del = buttonHTML({
    iconOnly: true,
    icon: "trash",
    variant: "danger",
    label: t("lib.init_script.params.remove"),
    attrs: { "data-param-del": i },
  });
  return `<div class="is-param" data-param-row="${i}">
    <input data-p="name" class="mono" value="${esc(p.name)}" placeholder="${esc(t("lib.init_script.params.name_placeholder"))}" aria-label="${esc(t("lib.init_script.params.name"))}"${dis} />
    <input data-p="default" value="${esc(p.default ?? "")}" placeholder="${esc(t("lib.init_script.params.default"))}" aria-label="${esc(t("lib.init_script.params.default"))}"${dis} />
    <input data-p="description" value="${esc(p.description ?? "")}" placeholder="${esc(t("lib.init_script.params.description"))}" aria-label="${esc(t("lib.init_script.params.description"))}"${dis} />
    ${checkHTML({ label: t("lib.init_script.params.required"), checked: !!p.required, disabled: readOnly, cls: "is-req", inputAttrs: { "data-p": "required" } })}
    ${readOnly ? "" : del}
  </div>`;
}

export function fileRowHTML(f: InitFileRow, i: number, readOnly: boolean): string {
  const dis = readOnly ? " disabled" : "";
  const del = buttonHTML({
    iconOnly: true,
    icon: "trash",
    variant: "danger",
    label: t("lib.init_script.files.remove"),
    attrs: { "data-file-del": i },
  });
  // The HTML parser eats the newline right after <textarea>; it is there so a file's own leading
  // newline survives, since any changed byte changes the setup key.
  return `<div class="is-file" data-file-row="${i}">
    <div class="is-file-head">
      <input data-f="path" class="mono" value="${esc(f.path)}" placeholder="${esc(t("lib.init_script.files.path_placeholder"))}" aria-label="${esc(t("lib.init_script.files.path"))}"${dis} />
      ${readOnly ? "" : del}
    </div>
    <textarea data-f="content" class="is-script is-file-content mono" rows="6" spellcheck="false" aria-label="${esc(t("lib.init_script.files.content"))}"${dis}>\n${esc(f.content)}</textarea>
  </div>`;
}

function rowEl(html: string): HTMLElement {
  const tpl = document.createElement("template");
  tpl.innerHTML = html;
  return tpl.content.firstElementChild as HTMLElement;
}

export function paramRowEl(p: InitParam, i: number, readOnly: boolean): HTMLElement {
  return rowEl(paramRowHTML(p, i, readOnly));
}

export function fileRowEl(f: InitFileRow, i: number, readOnly: boolean): HTMLElement {
  return rowEl(fileRowHTML(f, i, readOnly));
}

export function editorHTML(sc: InitScript, readOnly: boolean): string {
  const m = sc.manifest;
  const dis = readOnly ? " disabled" : "";
  const params = m.params.map((p, i) => paramRowHTML(p, i, readOnly)).join("");
  const timeout = m.ready.timeout_seconds ?? DEFAULT_TIMEOUT;
  const files = filePaths(m.files)
    .map((path, i) => fileRowHTML({ path, content: m.files?.[path] ?? "" }, i, readOnly))
    .join("");
  const installOpen =
    Boolean(m.setup) ||
    (m.cache?.paths.length ?? 0) > 0 ||
    Object.keys(m.tools ?? {}).length > 0 ||
    files !== "";
  return `<div class="is-editor form-stack">
    <div class="is-status muted">${statusHTML(sc)}</div>
    ${sc.note ? `<p class="is-last-note muted">${esc(t("lib.init_script.status.note", { note: sc.note }))}</p>` : ""}
    ${sc.revision || readOnly ? "" : `<div data-starter-slot></div>`}

    ${fieldHTML({
      label: t("lib.init_script.fields.script"),
      for: "is-script",
      hint: t("lib.init_script.fields.script_hint"),
      control: `<textarea id="is-script" class="is-script mono" rows="14" spellcheck="false"${dis}>${esc(sc.script || (readOnly ? "" : SCRIPT_TEMPLATE))}</textarea>`,
    })}

    <div class="is-grid">
      ${fieldHTML({
        label: t("lib.init_script.fields.params"),
        hint: t("lib.init_script.fields.params_hint"),
        control: `<div class="is-params" data-params>${params || emptyStateHTML({ size: "inline", body: t("lib.init_script.params.none"), attrs: { "data-params-empty": true } })}</div>
        ${readOnly ? "" : buttonHTML({ label: t("lib.init_script.params.add"), size: "sm", icon: "plus", attrs: { "data-param-add": true } })}`,
      })}
      <div class="form-stack">
        ${checkedFieldHTML({
          label: t("lib.init_script.fields.ports"),
          id: "is-ports",
          hint: t("lib.init_script.fields.ports_hint"),
          input: `value="${esc(m.ports.join(", "))}" inputmode="numeric" placeholder="3000, 5173"${dis}`,
        })}
        ${fieldHTML({
          label: t("lib.init_script.fields.ready"),
          for: "is-ready",
          hint: t("lib.init_script.fields.ready_hint"),
          control: `<input id="is-ready" class="mono" value="${esc(m.ready.command ?? "")}" placeholder="${esc(t("lib.init_script.fields.ready_placeholder"))}"${dis} />`,
        })}
        <div class="is-pair">
          ${checkedFieldHTML({
            label: t("lib.init_script.fields.timeout"),
            id: "is-timeout",
            input: `type="number" min="1" max="${MAX_TIMEOUT}" value="${timeout}"${dis}`,
          })}
          ${checkedFieldHTML({
            label: t("lib.init_script.fields.workdir"),
            id: "is-workdir",
            input: `class="mono" value="${esc(m.workdir || ".")}" placeholder="."${dis}`,
          })}
        </div>
      </div>
    </div>

    ${disclosureHTML({
      variant: "fields",
      ruled: true,
      summary: t("lib.init_script.install.title"),
      open: installOpen,
      cls: "is-install",
      bodyHTML: `<div class="form-stack">
        <p class="field-hint">${esc(t("lib.init_script.install.hint"))}</p>
        ${fieldHTML({
          label: t("lib.init_script.fields.setup"),
          for: "is-setup",
          hint: t("lib.init_script.fields.setup_hint"),
          control: `<textarea id="is-setup" class="is-script is-setup mono" rows="5" spellcheck="false" placeholder="${esc(t("lib.init_script.fields.setup_placeholder"))}"${dis}>${esc(m.setup ?? "")}</textarea>`,
        })}
        <div class="is-pair is-triple">
          ${checkedFieldHTML({
            label: t("lib.init_script.fields.tools"),
            id: "is-tools",
            hint: t("lib.init_script.fields.tools_hint"),
            input: `class="mono" value="${esc(toolsText(m.tools))}" placeholder="${esc(t("lib.init_script.fields.tools_placeholder"))}"${dis}`,
          })}
          ${checkedFieldHTML({
            label: t("lib.init_script.fields.cache_key"),
            id: "is-cache-key",
            hint: t("lib.init_script.fields.cache_key_hint"),
            input: `class="mono" value="${esc((m.cache?.key ?? []).join(", "))}" placeholder="${esc(t("lib.init_script.fields.cache_key_placeholder"))}"${dis}`,
          })}
          ${checkedFieldHTML({
            label: t("lib.init_script.fields.cache_paths"),
            id: "is-cache-paths",
            hint: t("lib.init_script.fields.cache_paths_hint"),
            input: `class="mono" value="${esc((m.cache?.paths ?? []).join(", "))}" placeholder="${esc(t("lib.init_script.fields.cache_paths_placeholder"))}"${dis}`,
          })}
        </div>
        ${fieldHTML({
          label: t("lib.init_script.fields.files"),
          hint: t("lib.init_script.fields.files_hint"),
          control: `<div class="is-files" data-files>${files || emptyStateHTML({ size: "inline", body: t("lib.init_script.files.none"), attrs: { "data-files-empty": true } })}</div>
          ${readOnly ? "" : buttonHTML({ label: t("lib.init_script.files.add"), size: "sm", icon: "plus", attrs: { "data-file-add": true } })}`,
        })}
      </div>
      <div class="is-prewarm" data-prewarm></div>`,
    })}

    ${
      readOnly
        ? ""
        : fieldHTML({
            label: t("lib.init_script.fields.note"),
            for: "is-note",
            optional: true,
            hint: t("lib.init_script.fields.note_hint"),
            control: `<input id="is-note" maxlength="500" placeholder="${esc(t("lib.init_script.fields.note_placeholder"))}" />`,
          })
    }
  </div>`;
}

export function historyRows(revs: InitRevision[]): HistoryRowOptions[] {
  return revs.map((r) => ({
    action: t("lib.init_script.history.row", { revision: r.revision }),
    badgesHTML: authorBadge(r.author_kind),
    metaHTML: [
      `${esc(authorText(r))} · ${esc(fmtDate(r.created_at))}`,
      r.restored_from
        ? esc(t("lib.init_script.history.restored_from", { revision: r.restored_from }))
        : "",
      r.note ? esc(r.note) : "",
    ]
      .filter(Boolean)
      .join("<br>"),
  }));
}

export function revisionHTML(sc: InitScript, current: number, canRestore: boolean): string {
  const m = sc.manifest;
  const files = filePaths(m.files);
  const facts = [
    m.ports.length ? t("lib.init_script.history.ports", { ports: m.ports.join(", ") }) : "",
    m.ready.command ? t("lib.init_script.history.ready", { command: m.ready.command }) : "",
    m.params.length
      ? t("lib.init_script.history.params", { params: m.params.map((p) => p.name).join(", ") })
      : "",
    m.workdir && m.workdir !== "."
      ? t("lib.init_script.history.workdir", { workdir: m.workdir })
      : "",
    m.setup ? t("lib.init_script.history.setup", { lines: m.setup.trim().split("\n").length }) : "",
    Object.keys(m.tools ?? {}).length
      ? t("lib.init_script.history.tools", { tools: toolsText(m.tools) })
      : "",
    m.cache?.key.length
      ? t("lib.init_script.history.cache_key", { files: m.cache.key.join(", ") })
      : "",
    m.cache?.paths.length
      ? t("lib.init_script.history.cache_paths", { paths: m.cache.paths.join(", ") })
      : "",
    files.length
      ? t("lib.init_script.history.files", { count: files.length, files: files.join(", ") })
      : "",
  ].filter(Boolean);
  return historyPreviewHTML({
    when: "",
    whenHTML: `${esc(t("lib.init_script.history.row", { revision: sc.revision }))} · ${authorHTML(sc)} · ${esc(sc.created_at ? fmtDate(sc.created_at) : "")}`,
    action:
      canRestore && sc.revision !== current
        ? { label: t("lib.init_script.history.restore") }
        : undefined,
    facts,
    body: { text: sc.script },
    files: files.map((path) => ({ path, text: m.files?.[path] ?? "" })),
  });
}

export function prewarmStateText(s: SetupPrewarm["status"]): string {
  switch (s) {
    case "running":
      return t("lib.init_script.prewarm.state.running");
    case "ok":
      return t("lib.init_script.prewarm.state.ok");
    case "skipped":
      return t("lib.init_script.prewarm.state.skipped");
    case "pruned":
      return t("lib.init_script.prewarm.state.pruned");
    case "failed":
      return t("lib.init_script.prewarm.state.failed");
  }
}

export function shortKey(k?: string): string {
  return (k ?? "").slice(0, 12);
}

export function prewarmHTML(st: PrewarmStatus, canRun: boolean): string {
  if (!st.available)
    return `<p class="muted is-prewarm-line">${esc(t("lib.init_script.prewarm.unavailable"))}</p>`;
  if (!st.eligible)
    return `<p class="muted is-prewarm-line">${esc(t("lib.init_script.prewarm.not_eligible"))}</p>`;
  const lastOK = st.prewarms.find((p) => p.status === "ok");
  const last = st.prewarms.at(0);
  const lines: string[] = [];
  lines.push(
    lastOK
      ? t("lib.init_script.prewarm.snapshot", {
          key: shortKey(lastOK.snapshot_key ?? lastOK.setup_key),
          date: fmtDate(lastOK.finished_at ?? lastOK.started_at),
          size: fmtBytes(lastOK.size_bytes),
        })
      : t("lib.init_script.prewarm.no_snapshot"),
  );
  if (st.running) lines.push(t("lib.init_script.prewarm.running"));
  else if (last && last !== lastOK && last.status !== "pruned")
    lines.push(
      t("lib.init_script.prewarm.last", {
        state: prewarmStateText(last.status),
        date: fmtDate(last.finished_at ?? last.started_at),
      }),
    );
  const err = !st.running && last?.error && last !== lastOK ? last.error : lastOK?.error;
  const btn = canRun
    ? buttonHTML({
        label: t("lib.init_script.prewarm.run"),
        size: "sm",
        icon: "refresh",
        disabled: st.running,
        attrs: { "data-prewarm-run": true },
      })
    : "";
  return `${sectionHeadHTML({
    level: 4,
    title: t("lib.init_script.prewarm.title"),
    asideHTML: `<span class="field-hint">${esc(t("lib.init_script.prewarm.hint"))}</span>`,
    actionsHTML: btn,
  })}
    ${lines.map((l) => `<p class="is-prewarm-line">${esc(l)}</p>`).join("")}
    ${
      err
        ? `<p class="is-prewarm-reason">${esc(prewarmReason(err))}</p>
    ${disclosureHTML({
      variant: "code",
      summary: t("lib.init_script.prewarm.detail"),
      bodyHTML: codeBlockHTML({ text: err, multiline: true, maxHeight: "sm", tone: "danger" }),
    })}`
        : ""
    }`;
}

// Matches the daemon's error text in both languages (internal/i18n: sandbox, imageref); change
// together.
const PREWARM_REASONS: Array<{ test: RegExp; key: string }> = [
  {
    test: /is not present on the Docker host|no está en el host de Docker|ErrImagePull|ImagePullBackOff|no such image|pull image|descargar la imagen/i,
    key: "lib.init_script.prewarm.reason.image",
  },
  {
    test: /no zstd|no tiene zstd|OCTOPUS_CACHE_DIR|snapshots directory|directorio de snapshots|no setsid|no tiene setsid/i,
    key: "lib.init_script.prewarm.reason.sandbox",
  },
  {
    test: /installing the tools failed|instalación de herramientas/i,
    key: "lib.init_script.prewarm.reason.tools",
  },
  {
    test: /setup script failed|script de setup falló/i,
    key: "lib.init_script.prewarm.reason.setup",
  },
  {
    test: /none of the cache paths|ninguna de las rutas de caché/i,
    key: "lib.init_script.prewarm.reason.no_paths",
  },
  {
    test: /took more than|tardó más de/i,
    key: "lib.init_script.prewarm.reason.timeout",
  },
  {
    test: /without an exit code|sin código de salida/i,
    key: "lib.init_script.prewarm.reason.lost",
  },
  {
    test: /too large to prewarm|demasiado grande para precalentar/i,
    key: "lib.init_script.prewarm.reason.too_large",
  },
];

export function prewarmReason(err: string): string {
  const rule = PREWARM_REASONS.find((r) => r.test.test(err));
  return rule ? t(rule.key) : t("lib.init_script.prewarm.reason.unknown");
}

export const CHECKED_FIELDS = [
  "is-ports",
  "is-timeout",
  "is-workdir",
  "is-tools",
  "is-cache-key",
  "is-cache-paths",
] as const;

export type CheckedField = (typeof CHECKED_FIELDS)[number];

export interface FieldCheck {
  errors: Partial<Record<CheckedField, string>>;
  warnings: Partial<Record<CheckedField, string>>;
}

export function checkFields(f: InitForm, rootFiles: string[] | null = null): FieldCheck {
  const errors: FieldCheck["errors"] = {};
  const warnings: FieldCheck["warnings"] = {};
  const ports = parsePorts(f.ports);
  if (ports.invalid.length)
    errors["is-ports"] = t("lib.init_script.errors.ports", { ports: ports.invalid.join(", ") });
  if (f.timeout.trim()) {
    const n = Number(f.timeout);
    if (!Number.isInteger(n) || n < 1 || n > MAX_TIMEOUT)
      errors["is-timeout"] = t("lib.init_script.errors.timeout", { max: MAX_TIMEOUT });
  }
  const workdir = f.workdir.trim() || ".";
  if (workdir.startsWith("/") || workdir.split("/").includes(".."))
    errors["is-workdir"] = t("lib.init_script.errors.workdir");
  const tools = parseTools(f.tools);
  if (tools.invalid.length)
    errors["is-tools"] = t("lib.init_script.errors.tools", { tools: tools.invalid.join(", ") });
  else if (Object.keys(tools.tools).length > MAX_TOOLS)
    errors["is-tools"] = t("lib.init_script.errors.too_many_tools", { max: MAX_TOOLS });
  for (const [id, text] of [
    ["is-cache-key", f.cacheKey],
    ["is-cache-paths", f.cachePaths],
  ] as const) {
    const parsed = parseCachePaths(text);
    if (parsed.invalid.length)
      errors[id] = t("lib.init_script.errors.cache_path", { paths: parsed.invalid.join(", ") });
    else if (parsed.paths.length > MAX_CACHE_ENTRIES)
      errors[id] = t("lib.init_script.errors.too_many_cache", { max: MAX_CACHE_ENTRIES });
  }
  if (rootFiles && workdir === "." && !errors["is-cache-key"]) {
    const missing = parseCachePaths(f.cacheKey).paths.filter(
      (p) => !p.includes("/") && !rootFiles.includes(p),
    );
    if (missing.length)
      warnings["is-cache-key"] = t("lib.init_script.warnings.lockfile_missing", {
        files: missing.join(", "),
      });
  }
  return { errors, warnings };
}

export function fieldWarnID(id: CheckedField): string {
  return `${id}-warn`;
}

function checkedFieldHTML(o: {
  label: string;
  id: CheckedField;
  hint?: string;
  input: string;
}): string {
  return fieldHTML({
    label: o.label,
    for: o.id,
    hint: o.hint,
    warnId: fieldWarnID(o.id),
    errorId: `${o.id}-error`,
    control: `<input id="${o.id}" ${o.input} />`,
  });
}

export interface InitStarter {
  stack: string;
  marker: string;
  script: string;
  setup: string;
  cacheKey: string[];
  cachePaths: string[];
}

// GOMODCACHE defaults outside the repo, and snapshots only keep paths inside the workdir.
// i18n-ignore a shell command
const GO_CACHE_ENV = 'export GOMODCACHE="$PWD/.cache/go-mod"';

const STARTERS: Array<{
  marker: string;
  stackKey: string;
  setup: string[];
  run: string[];
  key: (files: string[]) => string;
  paths: string[];
}> = [
  {
    marker: "pnpm-lock.yaml",
    stackKey: "lib.init_script.starter.stacks.pnpm",
    setup: ["corepack enable", "pnpm install --frozen-lockfile"], // i18n-ignore shell
    run: ["pnpm run dev"], // i18n-ignore shell
    key: () => "pnpm-lock.yaml",
    paths: ["node_modules"],
  },
  {
    marker: "yarn.lock",
    stackKey: "lib.init_script.starter.stacks.yarn",
    setup: ["corepack enable", "yarn install --frozen-lockfile"], // i18n-ignore shell
    run: ["yarn dev"], // i18n-ignore shell
    key: () => "yarn.lock",
    paths: ["node_modules"],
  },
  {
    marker: "package-lock.json",
    stackKey: "lib.init_script.starter.stacks.npm",
    setup: ["npm ci"], // i18n-ignore shell
    run: ["npm run dev"], // i18n-ignore shell
    key: () => "package-lock.json",
    paths: ["node_modules"],
  },
  {
    marker: "go.mod",
    stackKey: "lib.init_script.starter.stacks.go",
    setup: [GO_CACHE_ENV, "go mod download"], // i18n-ignore shell
    run: [GO_CACHE_ENV, "go run ."], // i18n-ignore shell
    key: (files) => (files.includes("go.sum") ? "go.sum" : "go.mod"),
    paths: [".cache/go-mod"],
  },
  {
    marker: "requirements.txt",
    stackKey: "lib.init_script.starter.stacks.pip",
    // i18n-ignore shell
    setup: ["python3 -m venv .venv", ".venv/bin/pip install -r requirements.txt"],
    run: [". .venv/bin/activate", "python app.py"], // i18n-ignore shell
    key: () => "requirements.txt",
    paths: [".venv"],
  },
];

export function starterFor(files: string[]): InitStarter | null {
  const s = STARTERS.find((st) => files.includes(st.marker));
  if (!s) return null;
  return {
    stack: t(s.stackKey),
    marker: s.marker,
    setup: s.setup.join("\n"),
    script:
      SCRIPT_TEMPLATE + `# ${t("lib.init_script.starter.run_comment")}\n${s.run.join("\n")}\n`,
    cacheKey: [s.key(files)],
    cachePaths: [...s.paths],
  };
}

export function applyStarter(sc: InitScript, st: InitStarter): InitScript {
  const manifest: InitManifest = {
    ...sc.manifest,
    setup: st.setup,
    cache: { key: [...st.cacheKey], paths: [...st.cachePaths] },
  };
  return { ...sc, script: st.script, manifest };
}

export function starterHTML(st: InitStarter): string {
  return `<div class="is-starter" data-starter>
    <span>${esc(t("lib.init_script.starter.found", { stack: st.stack, file: st.marker }))}</span>
    ${buttonHTML({ label: t("lib.init_script.starter.use"), size: "sm", attrs: { "data-starter-use": true } })}
  </div>`;
}

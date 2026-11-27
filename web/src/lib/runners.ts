import { api, ApiError } from "./api";
import { apiBase } from "./auth";
import { relTime } from "./activity";
import type { LLMProfile } from "./llm-profiles";
import {
  badgeHTML,
  bindCodeCopy,
  buttonHTML,
  calloutHTML,
  codeBlockHTML,
  confirmDialog,
  esc,
  listRowHTML,
  makeToast,
  rowGroupBoxedHTML,
  stepListHTML,
  type StepItem,
} from "./ui";
import { t } from "../i18n";

// A runner runs agents on a person's machine with their git and their claude login; the code never
// leaves it. These helpers back the Runners section in Settings and the "On a machine" origin in
// New project.

export interface Runner {
  id: string;
  name: string;
  hostname?: string;
  version?: string;
  created_at: string;
  last_seen_at?: string;
  revoked_at?: string;
  online: boolean;
  // The protocol the runner spoke last; 0 or missing until it first connects.
  protocol?: number;
}

// What a machine reports about one engine's CLI: whether it is on the PATH and whose login it holds.
export interface EngineState {
  engine: string;
  installed: boolean;
  version?: string;
  logged_in: boolean;
  email?: string;
  org?: string;
  method?: string;
  error?: string;
  checked_at?: string;
}

export interface RunnerRepo {
  path: string;
  name: string;
  branch?: string;
  origin?: string;
  project_id?: string;
  project_name?: string;
}

// What the machine lets its runs reach of its own tooling. Decided on the machine
// (runner.json); the dashboard only shows it.
export interface RunnerLocalTools {
  mode: "thulu" | "selected" | "all";
  mcp?: string[];
  bypass_disabled?: boolean;
  allowed_tools?: number;
}

// Whether the machine takes new runs, or waits for memory to free up (min_free_memory_mb in its
// runner.json).
export interface RunnerAdmission {
  state: "accepting" | "paused_low_memory";
  available_mb?: number;
  min_free_mb?: number;
  since?: string;
}

export interface RunnerRepos {
  roots: string[];
  repos: RunnerRepo[];
  truncated?: boolean;
  reported: boolean;
  scanned_at?: string;
  local_tools?: RunnerLocalTools;
  admission?: RunnerAdmission;
}

// sizeText writes MB the way the runner's own log does: "768 MB", "1.2 GB", "2 GB".
function sizeText(mb: number): string {
  if (mb < 1024) return `${Math.round(mb)} MB`;
  return `${Math.round((mb / 1024) * 10) / 10} GB`;
}

// admissionLines says, for an online runner's row, that it takes no runs while memory is short.
export function admissionLines(a: RunnerAdmission | undefined): string[] {
  if (a?.state !== "paused_low_memory") return [];
  return [
    t("lib.runners.row.paused_low_memory", {
      available: sizeText(a.available_mb ?? 0),
      needs: sizeText(a.min_free_mb ?? 0),
    }),
  ];
}

// localToolsLines describes a runner's local_tools for its row, one sentence per line.
export function localToolsLines(lt: RunnerLocalTools | undefined): string[] {
  if (!lt) return [];
  let mode: string;
  if (lt.mode === "all") mode = t("lib.runners.row.local_tools_all");
  else if (lt.mode === "selected")
    mode = lt.mcp?.length
      ? t("lib.runners.row.local_tools_selected", { mcp: lt.mcp.join(", ") })
      : t("lib.runners.row.local_tools_selected_none");
  else mode = t("lib.runners.row.local_tools_thulu");
  const lines = [`${mode} ${t("lib.runners.row.local_tools_hint")}`];
  if (lt.bypass_disabled)
    lines.push(
      lt.allowed_tools ? t("lib.runners.row.bypass_off") : t("lib.runners.row.bypass_off_empty"),
    );
  return lines;
}

export interface RunnerToken {
  token: string;
  expires_at: string;
}

const toast = makeToast(3500);

export function withWs(path: string, ws = ""): string {
  if (!ws) return path;
  return `${path}${path.includes("?") ? "&" : "?"}workspace=${encodeURIComponent(ws)}`;
}

export async function loadRunners(ws = ""): Promise<Runner[]> {
  return (await api.get<Runner[] | null>(withWs("/runners", ws))) ?? [];
}

// null only when this daemon runs nothing on machines (503). Any other failure reads as no machine
// known, so one failed request neither hides machine profiles nor blames the runners for it.
export async function loadRunnersOrOff(ws = ""): Promise<Runner[] | null> {
  try {
    return await loadRunners(ws);
  } catch (e) {
    return e instanceof ApiError && e.status === 503 ? null : [];
  }
}

export function activeRunners(list: Runner[]): Runner[] {
  return list.filter((r) => !r.revoked_at);
}

export async function loadRunnerRepos(id: string, ws = ""): Promise<RunnerRepos> {
  return api.get<RunnerRepos>(withWs(`/runners/${encodeURIComponent(id)}/repos`, ws));
}

export function runnerStatus(
  r: Runner,
  now = Date.now(),
): { label: string; tone: "ok" | "neutral" | "danger" } {
  if (r.revoked_at) return { label: t("lib.runners.status.revoked"), tone: "danger" };
  if (r.online) return { label: t("lib.runners.status.online"), tone: "ok" };
  if (!r.last_seen_at) return { label: t("lib.runners.status.never"), tone: "neutral" };
  return {
    label: t("lib.runners.status.seen", { when: relTime(r.last_seen_at, now) }),
    tone: "neutral",
  };
}

// "Mac de Ana · conectado": what a select option or a badge says about a runner.
export function runnerLabel(r: Runner, now = Date.now()): string {
  return `${r.name} · ${runnerStatus(r, now).label}`;
}

// The three commands that put a machine to work, in the order a person runs them. The token is
// single use and lasts an hour; the folder is a placeholder the person changes.
export function setupCommands(
  token: string,
  base = apiBase(),
): { register: string; allow: string; start: string } {
  return {
    register: `thulu runner register -url ${base} -token ${token}`,
    allow: `thulu runner allow ~/code`, // i18n-ignore a shell command
    start: `thulu runner service install`, // i18n-ignore a shell command
  };
}

export function setupStepsHTML(token: RunnerToken, now = Date.now()): string {
  const cmd = setupCommands(token.token);
  const minutes = Math.max(1, Math.round((Date.parse(token.expires_at) - now) / 60000));
  const steps: StepItem[] = [
    {
      state: "now",
      title: t("lib.runners.setup.register_title"),
      detail: t("lib.runners.setup.register_detail", { minutes }),
    },
    {
      state: "pending",
      title: t("lib.runners.setup.allow_title"),
      detail: t("lib.runners.setup.allow_detail"),
    },
    {
      state: "pending",
      title: t("lib.runners.setup.start_title"),
      detail: t("lib.runners.setup.start_detail"),
    },
  ];
  return (
    stepListHTML(steps, { compact: true }) +
    codeBlockHTML({
      text: [cmd.register, cmd.allow, cmd.start].join("\n"),
      multiline: true,
      wrap: true,
      copy: true,
      label: t("lib.runners.setup.copy"),
    })
  );
}

export async function createRunnerToken(ws = "", name = ""): Promise<RunnerToken> {
  return api.post<RunnerToken>(withWs("/runners/tokens", ws), { name });
}

// Polls until a runner that was not in `known` shows up, the signal is aborted, or ten minutes pass.
export async function waitForNewRunner(
  known: Set<string>,
  ws: string,
  signal: AbortSignal,
  every = 3000,
): Promise<Runner | null> {
  const deadline = Date.now() + 10 * 60_000;
  // A function, so the check after the await reads the signal again instead of a narrowed copy.
  const stopped = () => signal.aborted;
  while (!stopped() && Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, every));
    if (stopped()) return null;
    try {
      const fresh = activeRunners(await loadRunners(ws)).find((r) => !known.has(r.id));
      if (fresh) return fresh;
    } catch {
      // The next tick tries again.
    }
  }
  return null;
}

function errorText(e: unknown): string {
  return e instanceof ApiError ? e.message : t("lib.runners.errors.unknown");
}

// --- Settings → Runners ---------------------------------------------------------------------

interface PanelState {
  runners: Runner[];
  repos: Map<string, RunnerRepos>;
  projects: Map<string, number>;
  token: RunnerToken | null;
  error: string;
  wait: AbortController | null;
}

const panel: PanelState = {
  runners: [],
  repos: new Map(),
  projects: new Map(),
  token: null,
  error: "",
  wait: null,
};

function runnerRowHTML(r: Runner, repos: RunnerRepos | undefined, projects: number): string {
  const st = runnerStatus(r);
  const meta = [
    r.hostname,
    r.version ? t("lib.runners.row.version", { version: r.version }) : "",
    projects
      ? t("lib.runners.row.projects", { count: projects })
      : t("lib.runners.row.no_projects"),
  ]
    .filter(Boolean)
    .join(" · ");
  const lines: string[] = [];
  if (!r.revoked_at) {
    lines.push(
      repos?.reported
        ? repos.roots.length
          ? t("lib.runners.row.folders", { folders: repos.roots.join(", ") })
          : t("lib.runners.row.no_folders")
        : t("lib.runners.row.not_reported"),
      ...localToolsLines(repos?.local_tools),
      // A runner that went offline last said this, and may say otherwise when it comes back.
      ...(r.online ? admissionLines(repos?.admission) : []),
    );
  }
  return listRowHTML({
    title: r.name,
    badgesHTML: badgeHTML(st.label, st.tone),
    metaHTML: [meta, ...lines].map((l) => esc(l)).join("<br>"),
    dim: !!r.revoked_at,
    wrap: true,
    actions: r.revoked_at
      ? []
      : [
          {
            label: t("lib.runners.row.revoke"),
            variant: "danger",
            attrs: { "data-runner-revoke": r.id },
          },
        ],
  });
}

function paintPanel(slot: HTMLElement) {
  const active = activeRunners(panel.runners);
  const rows = panel.runners.map((r) =>
    runnerRowHTML(r, panel.repos.get(r.id), panel.projects.get(r.id) ?? 0),
  );
  const list = rows.length
    ? rowGroupBoxedHTML(rows.join(""))
    : calloutHTML({
        icon: "monitor",
        title: t("lib.runners.empty.title"),
        body: t("lib.runners.empty.body"),
      });
  const setup = panel.token
    ? `<div class="runner-setup">${setupStepsHTML(panel.token)}<p class="muted runner-wait">${esc(t("lib.runners.setup.waiting"))}</p></div>`
    : "";
  slot.innerHTML =
    `<p class="runner-lede">${esc(t("lib.runners.lede"))}</p>` +
    (panel.error ? calloutHTML({ tone: "danger", icon: "alert", body: panel.error }) : "") +
    list +
    `<div class="form-actions runner-add">${buttonHTML({
      label: active.length ? t("lib.runners.add_another") : t("lib.runners.add"),
      icon: "plus",
      variant: panel.token ? "default" : "primary",
      attrs: { "data-runner-add": "" },
    })}</div>` +
    setup;
  bindCodeCopy(slot);
}

async function refreshPanel(slot: HTMLElement) {
  try {
    panel.runners = await loadRunners();
    panel.error = "";
  } catch (e) {
    panel.error = t("lib.runners.errors.load", { error: errorText(e) });
  }
  try {
    const projects = (await api.get<Array<{ runner_id?: string }> | null>("/projects")) ?? [];
    panel.projects = new Map();
    for (const p of projects) {
      if (p.runner_id) panel.projects.set(p.runner_id, (panel.projects.get(p.runner_id) ?? 0) + 1);
    }
  } catch {
    // The count is a hint; the list still shows.
  }
  await Promise.all(
    activeRunners(panel.runners).map(async (r) => {
      try {
        panel.repos.set(r.id, await loadRunnerRepos(r.id));
      } catch {
        panel.repos.delete(r.id);
      }
    }),
  );
  paintPanel(slot);
}

let panelBound = false;

export async function loadRunnersPanel(slot: HTMLElement): Promise<void> {
  if (!panelBound) {
    panelBound = true;
    slot.addEventListener("click", (e) => {
      const target = e.target as HTMLElement;
      if (target.closest("[data-runner-add]")) void addMachine(slot);
      const revoke = target.closest<HTMLElement>("[data-runner-revoke]");
      if (revoke) void revokeRunner(slot, revoke.dataset.runnerRevoke ?? "");
    });
  }
  await refreshPanel(slot);
}

async function addMachine(slot: HTMLElement) {
  try {
    panel.token = await createRunnerToken();
  } catch (e) {
    panel.error = t("lib.runners.errors.token", { error: errorText(e) });
    paintPanel(slot);
    return;
  }
  panel.error = "";
  paintPanel(slot);
  panel.wait?.abort();
  const wait = new AbortController();
  panel.wait = wait;
  const fresh = await waitForNewRunner(new Set(panel.runners.map((r) => r.id)), "", wait.signal);
  if (!fresh || wait.signal.aborted) return;
  panel.token = null;
  toast(t("lib.runners.toast.registered", { name: fresh.name }), "ok");
  await refreshPanel(slot);
  // The runner reports its folders right after its first claim, a moment after it shows up: look a
  // few more times so the row doesn't stay on "hasn't reported" until the next visit.
  const stopped = () => wait.signal.aborted;
  for (let i = 0; i < 5 && !stopped() && !panel.repos.get(fresh.id)?.reported; i++) {
    await new Promise((resolve) => setTimeout(resolve, 2000));
    await refreshPanel(slot);
  }
}

// What revoking a machine stops: its projects, and every AI-engine profile on it by name, an
// engine's default included, since every agent without a profile of its own follows that one.
export function revokeMessage(id: string, profiles: MachineProfileRef[]): string {
  const names = profiles
    .filter((p) => p.runner_id === id)
    .map((p) =>
      p.is_default ? t("lib.runners.confirm.default_profile", { name: p.name }) : p.name,
    );
  const msg = t("lib.runners.confirm.message");
  return names.length
    ? `${msg} ${t("lib.runners.confirm.profiles", { names: names.join(", ") })}`
    : msg;
}

type MachineProfileRef = Pick<LLMProfile, "name" | "runner_id" | "is_default">;

async function machineProfiles(): Promise<MachineProfileRef[]> {
  try {
    return (await api.get<{ profiles: LLMProfile[] | null }>("/llm-profiles")).profiles ?? [];
  } catch {
    // The dialog still warns about profiles in general.
    return [];
  }
}

async function revokeRunner(slot: HTMLElement, id: string) {
  const r = panel.runners.find((x) => x.id === id);
  if (!r) return;
  const ok = await confirmDialog({
    title: t("lib.runners.confirm.title", { name: r.name }),
    message: revokeMessage(id, await machineProfiles()),
    confirmLabel: t("lib.runners.confirm.button"),
    danger: true,
  });
  if (!ok) return;
  try {
    await api.del(`/runners/${encodeURIComponent(id)}`);
  } catch (e) {
    toast(t("lib.runners.errors.revoke", { error: errorText(e) }), "err");
    return;
  }
  toast(t("lib.runners.toast.revoked", { name: r.name }), "ok");
  await refreshPanel(slot);
}

export function stopRunnersPanel(): void {
  panel.wait?.abort();
  panel.wait = null;
}

// "On Ana's Mac": the badge a project, a board card or a task shows when its agents run on a
// machine. A project whose runner was revoked keeps the badge so nobody wonders why runs stopped.
export function runnerBadgeHTML(runnerID: string | undefined, list: Runner[]): string {
  if (!runnerID) return "";
  const r = list.find((x) => x.id === runnerID);
  const name = r?.name ?? t("lib.runners.badge.unknown");
  const title = r ? runnerStatus(r).label : "";
  return badgeHTML(t("lib.runners.badge.on", { name }), r?.revoked_at ? "danger" : "info", {
    icon: "monitor",
    title,
  });
}

// A task runs on at most one machine; its Thulu projects travel to that machine. The daemon refuses
// two machines, so the new-task form says it before the task exists.
export function mixWarning(
  picked: Array<{ name: string; runner_id?: string }>,
  list: Runner[],
): string {
  const ids = new Set(picked.filter((p) => p.runner_id).map((p) => p.runner_id));
  if (ids.size < 2) return "";
  const names = [...ids].map(
    (id) => list.find((r) => r.id === id)?.name ?? t("lib.runners.badge.unknown"),
  );
  return t("lib.runners.mix.machines", { machines: names.join(", ") });
}

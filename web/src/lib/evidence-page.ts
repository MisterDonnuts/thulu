// The daemon has no evidence topic on the event channel, so batches are polled:
// every 5 s while one is queued or running, every 30 s otherwise.

import { t } from "../i18n";
import { viewerFor, type Viewer } from "./activity";
import { api, ApiError } from "./api";
import { roleWithin } from "./api-keys";
import { apiBase, authDisabled, requireAuth, sessionInfo } from "./auth";
import { events, TASKS_TOPIC } from "./events";
import {
  batchHTML,
  capturesHTML,
  caseCardHTML,
  caseGroupsGridHTML,
  caseGroupsRowsHTML,
  caseRowHTML,
  chaptersHTML,
  checksTableHTML,
  envGroupsHTML,
  failuresHTML,
  historyHTML,
  offProps,
  prodBadgeHTML,
  runnerErrorHTML,
  sandboxReasonHTML,
  statusPillHTML,
  type ListCtx,
} from "./evidence-html";
import {
  EMPTY_CASE_FILTERS,
  SECRET_NAME,
  batchCounts,
  batchesSignature,
  batchesURL,
  browserTimeZone,
  caseFiltersFromQuery,
  caseFiltersToQuery,
  caseHash,
  casesURL,
  checksSummary,
  caseRunsOn,
  ciHookCurl,
  cronFromSchedule,
  scheduleTimeOK,
  ENV_SLUG,
  envKind,
  envLabel,
  envLongLabel,
  envsForProjects,
  NOT_RUN,
  PROJECT_NONE,
  groupCases,
  groupEnvs,
  hasOwnCaseFilters,
  permission,
  pickProject,
  reasonLabel,
  sandboxBranch,
  showProjectPicker,
  slugify,
  durationLabel,
  isActiveBatch,
  isActiveRun,
  isHttpURL,
  isRecorded,
  isSandboxFailed,
  isValidCron,
  lastBatchByEnv,
  libraryIndex,
  listOf,
  normalizeSecretName,
  pageQuery,
  parseCaseHash,
  parseChapters,
  parseHosts,
  parseTags,
  pickRun,
  runsEnvFromQuery,
  scheduleFromCron,
  scheduleLabel,
  statusLabel,
  tabFromQuery,
  tagsOf,
  type LibraryStatus,
  timeZones,
  triggerLabel,
  unwrap,
  visibleStatuses,
  whenLabel,
  totalOf,
  type CaseFilters,
  type CasesResponse,
  type EvBatch,
  type EvCase,
  type EvCaseDetail,
  type EvContext,
  type EvEnvironment,
  type EvRun,
  type EvidenceTab,
  type Permission,
  type RecordingAgents,
  type SchedulePreset,
  cardStatus,
  recordingAgentLabel,
  recordingPick,
  recordingPromptHref,
  scheduleScopeLine,
  sortCases,
  type CaseSort,
  CASE_SORTS,
} from "./evidence-view";
import { initKeyboard } from "./keyboard";
import { md } from "./markdown";
import { replaceURL } from "./page";
import { bindDrawer } from "./drawer";
import { bindModal } from "./modal";
import {
  EMPTY_FILTERS,
  QA_COLUMNS,
  countsSummary,
  filtersFromQuery,
  filtersToQuery,
  groupByTask,
  matchedCovered,
  matchedTotal,
  tableBodyHTML,
  testCasesURL,
  toggleStatus,
  type QAFilters,
  type QAResponse,
} from "./qa-view";
import { setText, startPoller } from "./reactive";
import type { BoardsResponse, Stage } from "./stages";
import { labelTagHTML } from "./chips";
import {
  casePriorityTagHTML,
  caseProgressHTML,
  priorityLabel,
  type TestCaseStatus,
} from "./test-cases";
import {
  $,
  bindMenu,
  bindTabs,
  buttonHTML,
  CARD_TARGET,
  checkHTML,
  clearFormErrors,
  confirmDialog,
  disclosureHTML,
  downloadBlob,
  emptyRowHTML,
  emptyStateHTML,
  esc,
  filterChipsHTML,
  groupHeadHTML,
  icoSvg,
  makeToast,
  mdHTML,
  sectionHeadHTML,
  segmentedValue,
  setChipsPressed,
  setCount,
  setFieldError,
  setFormError,
  setSegmentedValue,
  type MenuHandle,
  dateInvalidMessage,
  readDateInput,
  writeDateInput,
} from "./ui";

interface Project {
  id: string;
  name: string;
}

type RunScope = { kind: "case"; c: EvCase } | { kind: "all" } | { kind: "env"; envId: string };

interface LoginRow {
  uid: number;
  name: string;
  existing: boolean;
  op: "keep" | "replace" | "remove";
  value: string;
}

const VIEW_KEY = "octopus.evidence.view";
const SORT_KEY = "octopus.evidence.sort";

function readSort(): CaseSort {
  try {
    const v = localStorage.getItem(SORT_KEY) ?? "";
    return (CASE_SORTS as string[]).includes(v) ? (v as CaseSort) : "newest";
  } catch {
    return "newest";
  }
}
const PROJECT_KEY = "octopus.evidence.project";

function rememberedProject(): string {
  try {
    return localStorage.getItem(PROJECT_KEY) ?? "";
  } catch {
    return "";
  }
}

function rememberProject(id: string): void {
  try {
    if (id) localStorage.setItem(PROJECT_KEY, id);
    else localStorage.removeItem(PROJECT_KEY);
  } catch {
    // private window: the pick lasts for this visit
  }
}

const errMsg = (e: unknown, fallback: string): string =>
  e instanceof ApiError && e.message ? e.message : fallback;

function setHTML(el: HTMLElement, html: string): void {
  if (el.dataset.sig === html) return;
  el.innerHTML = html;
  el.dataset.sig = html;
}

function busy(btn: HTMLButtonElement, label: string): () => void {
  const was = btn.innerHTML;
  btn.disabled = true;
  btn.textContent = label;
  return () => {
    btn.disabled = false;
    btn.innerHTML = was;
  };
}

function readView(search: string): "grid" | "list" {
  const v = new URLSearchParams(search).get("view");
  if (v === "list" || v === "grid") return v;
  try {
    return localStorage.getItem(VIEW_KEY) === "list" ? "list" : "grid";
  } catch {
    return "grid";
  }
}

export function startEvidencePage(signal: AbortSignal): void {
  requireAuth();
  const toast = makeToast(4000);
  const API = apiBase();
  const artifactURL = (id: string): string => `${API}/evidence/artifacts/${encodeURIComponent(id)}`;
  const mdText = (src: string): string =>
    src.trim() ? md(src, { mediaBase: API }) : `<p class="ev-blank">—</p>`;

  const first = tabFromQuery(location.search);
  const firstQuery = new URLSearchParams(location.search);
  const state = {
    tab: first,
    caseFilters: first === "cases" ? caseFiltersFromQuery(location.search) : EMPTY_CASE_FILTERS,
    runsEnv: first === "runs" ? runsEnvFromQuery(location.search) : "",
    tasksQuery: first === "tasks" ? location.search : "",
    view: readView(first === "cases" ? location.search : ""),
    sort: readSort(),
    envs: [] as EvEnvironment[],
    envsLoaded: false,
    projects: new Map<string, string>(),
    cases: [] as EvCase[],
    counts: {} as Partial<Record<LibraryStatus, number>>,
    casesLoaded: false,
    tagOptions: [] as string[],
    batches: [] as EvBatch[],
    batchesLoaded: false,
    batchSig: "",
    lastBatchFetch: 0,
    openBatches: new Set<string>(),
    batchRuns: new Map<string, EvRun[]>(),
    canWrite: true,
    canAdmin: true,
    viewer: null as Viewer | null,
    ctx: null as EvContext | null,
    ctxSettled: false,
    pickerOn: false,
    project: firstQuery.get("project_id") ?? "",
    batchLogs: new Map<string, string>(),
    envTarget: first === "environments" ? (firstQuery.get("env") ?? "") : "",
  };

  let flash: { id: string; until: number } | null = null;

  const envById = (id: string): EvEnvironment | undefined => state.envs.find((e) => e.id === id);
  const envName = (id: string): string => {
    const env = envById(id);
    return env ? envLabel(env) : "";
  };
  const mixesProjects = (): boolean =>
    state.pickerOn && (!state.project || state.project === PROJECT_NONE);
  const envTitle = (env: EvEnvironment): string =>
    mixesProjects() ? envLongLabel(env) : envLabel(env);
  const envOnCase = (env: EvEnvironment, c: EvCase): string =>
    mixesProjects() && env.project_id && env.project_id !== c.project_id
      ? envLongLabel(env)
      : envLabel(env);
  const withUnassigned = (project: string): boolean => Boolean(project) && project !== PROJECT_NONE;
  const envNames = (): Map<string, string> => new Map(state.envs.map((e) => [e.id, envTitle(e)]));
  const writeReason = (): string => (state.canWrite ? "" : "viewer");
  const wsCan = (
    action: "create_case" | "run" | "create_environment" | "create_production_environment",
  ): Permission =>
    state.ctx?.can[action] ??
    (action === "create_production_environment"
      ? { allowed: state.canAdmin, reason: state.canWrite ? "production" : "viewer" }
      : { allowed: state.canWrite, reason: "viewer" });
  const envsInView = (): EvEnvironment[] => {
    if (!state.pickerOn || !state.project) return state.envs;
    if (state.project === PROJECT_NONE) return state.envs.filter((e) => caseRunsOn("", e));
    return state.envs.filter((e) => envKind(e) === "standalone" || e.project_id === state.project);
  };
  const projectName = (id: string): string =>
    state.projects.get(id) ?? state.ctx?.projects.find((p) => p.id === id)?.name ?? "";
  const projectList = (): Array<{ id: string; name: string }> =>
    state.ctx?.projects.length
      ? state.ctx.projects.map((p) => ({ id: p.id, name: state.projects.get(p.id) ?? p.name }))
      : [...state.projects].map(([id, name]) => ({ id, name }));
  const findCase = (id: string): EvCase | undefined =>
    state.cases.find((c) => c.id === id) ?? (drawer.detail?.id === id ? drawer.detail : undefined);
  const listCtx = (): ListCtx => ({
    envs: state.envs,
    projects: state.projects,
    envName: envOnCase,
    envFirst: state.caseFilters.envId,
    canWrite: state.canWrite,
    writeReason: writeReason(),
    artifactURL,
  });

  const tabs = bindTabs($("ev-tabs"), {
    onChange: (id, { user }) => {
      if (user) showTab(id as EvidenceTab);
    },
  });

  function filterQuery(tab: EvidenceTab): string {
    switch (tab) {
      case "cases": {
        const p = new URLSearchParams(caseFiltersToQuery(state.caseFilters));
        if (state.view === "list") p.set("view", "list");
        const s = p.toString();
        return s ? `?${s}` : "";
      }
      case "runs": {
        const p = new URLSearchParams();
        if (state.pickerOn && state.project) p.set("project_id", state.project);
        if (state.runsEnv) p.set("environment_id", state.runsEnv);
        const s = p.toString();
        return s ? `?${s}` : "";
      }
      case "environments": {
        const p = new URLSearchParams();
        if (state.pickerOn && state.project) p.set("project_id", state.project);
        if (state.envTarget) p.set("env", state.envTarget);
        const s = p.toString();
        return s ? `?${s}` : "";
      }
      case "tasks":
        return state.tasksQuery;
    }
  }

  function writeURL(hash = location.hash): void {
    const url = `${location.pathname}${pageQuery(state.tab, filterQuery(state.tab))}${hash}`;
    replaceURL(url);
  }

  function showTab(next: EvidenceTab): void {
    state.tab = next;
    tabs.select(next);
    $(`panel-${next}`).hidden = false;
    scope.classList.toggle("ev-scope-off", next === "tasks");
    writeURL();
    if (next === "cases") renderCases();
    if (next === "runs") {
      renderBatches();
      batchesPoller.poke();
    }
    if (next === "environments") {
      renderEnvs();
      void loadEnvs();
    }
    if (next === "tasks") tasks.start();
  }

  function setOff(btn: HTMLButtonElement, p: Permission): void {
    btn.disabled = !p.allowed;
    if (p.allowed) btn.removeAttribute("title");
    else btn.title = reasonLabel(p.reason);
  }

  function applyRole(): void {
    const write: Permission = { allowed: state.canWrite, reason: "viewer" };
    document.querySelectorAll<HTMLElement>("[data-needs='write']").forEach((el) => {
      const btns =
        el instanceof HTMLButtonElement ? [el] : Array.from(el.querySelectorAll("button"));
      for (const b of btns) setOff(b, write);
    });
    const create = wsCan("create_environment");
    setOff($("e-new") as HTMLButtonElement, create);
    setOff($("e-empty-new") as HTMLButtonElement, create);
    $("c-readonly").hidden = state.canWrite;
    $("r-readonly").hidden = state.canWrite;
    const note = $("e-readonly");
    const prodLimited =
      state.canWrite && !state.canAdmin && state.envs.some((e) => e.is_production);
    note.hidden = state.canWrite && !prodLimited;
    setText(
      $("e-readonly-text"),
      state.canWrite ? t("evidence.envs.production_note") : t("evidence.can.viewer"),
    );
  }

  function applySession(role: string | undefined): void {
    state.canWrite = roleWithin("developer", role);
    state.canAdmin = roleWithin("admin", role);
    applyRole();
    renderCases();
    renderEnvs();
    renderBatches();
  }

  void sessionInfo().then((me) => {
    if (!state.ctx) applySession(me?.role);
  });
  void Promise.all([sessionInfo(), authDisabled()]).then(([me, devSession]) => {
    state.viewer = viewerFor(me?.user, devSession);
    tasks.redraw();
  });

  const fProject = $("f-project") as HTMLSelectElement;
  const fTag = $("f-tag") as HTMLSelectElement;
  const fTags = $("f-tags");
  const fPriority = $("f-priority") as HTMLSelectElement;
  const fEnv = $("f-env") as HTMLSelectElement;
  const fQ = $("f-q") as HTMLInputElement;
  const fClear = $("f-clear") as HTMLButtonElement;
  const rEnv = $("r-env") as HTMLSelectElement;

  function fillSelect(sel: HTMLSelectElement, opts: Array<[string, string]>, value: string): void {
    while (sel.options.length > 1) sel.remove(1);
    for (const [id, label] of opts) sel.add(new Option(label, id));
    if (value && !opts.some(([id]) => id === value)) sel.add(new Option(value, value));
    sel.value = value;
  }

  async function loadProjects(): Promise<void> {
    const list = await api.get<Project[]>("/projects").catch(() => [] as Project[]);
    state.projects = new Map(listOf<Project>(list, "projects").map((p) => [p.id, p.name || p.id]));
    const opts = [...state.projects].map(([id, name]): [string, string] => [id, name]);
    if (!state.pickerOn)
      fillSelect(
        fProject,
        [...opts, [PROJECT_NONE, t("evidence.picker.no_project")]],
        state.caseFilters.projectId,
      );
    $("f-project-field").hidden = state.pickerOn || state.projects.size <= 1;
    fillSelect($("cf-project") as HTMLSelectElement, opts, "");
    if (!state.ctx && state.ctxSettled) applyPicker(state.projects.size > 1);
    renderPicker();
    renderCases();
    if (drawerModal.isOpen()) renderDrawer();
  }

  const scope = $("ev-scope");
  const picker = $("ev-project") as HTMLSelectElement;

  async function loadContext(): Promise<void> {
    try {
      const c = await api.get<EvContext>("/evidence/context");
      state.ctx = c;
      applySession(c.role);
      applyPicker(showProjectPicker(state.ctx));
      recheckContext();
      if (drawerModal.isOpen()) renderDrawer();
    } catch {
      // Older daemons have no /evidence/context: fall back to the session role and project list.
      applyPicker(state.projects.size > 1);
    } finally {
      state.ctxSettled = true;
      casesPoller.poke();
    }
  }

  function applyPicker(on: boolean): void {
    state.pickerOn = on;
    const projects = projectList();
    const before = state.caseFilters.projectId;
    if (on) {
      state.project = pickProject(linkedProject() || state.project, rememberedProject(), projects);
      state.caseFilters = {
        ...state.caseFilters,
        projectId: state.project,
        includeUnassigned: withUnassigned(state.project),
      };
    } else {
      state.project = "";
      state.caseFilters = { ...state.caseFilters, includeUnassigned: false };
    }
    $("f-project-field").hidden = on || state.projects.size <= 1;
    renderPicker();
    writeURL();
    if (state.caseFilters.projectId !== before && state.ctxSettled) casesPoller.poke();
    renderCases();
    renderEnvs();
    renderBatches();
    fillRunsEnv();
  }

  function renderPicker(): void {
    scope.hidden = !state.pickerOn;
    if (!state.pickerOn) return;
    const counts = new Map((state.ctx?.projects ?? []).map((p) => [p.id, p.cases]));
    const opts = projectList().map(({ id, name }): [string, string] => [
      id,
      counts.has(id)
        ? t("evidence.picker.option", { project: name, count: counts.get(id) ?? 0 })
        : name,
    ]);
    const unassigned = state.ctx?.unassigned_cases ?? 0;
    if (unassigned || state.project === PROJECT_NONE)
      opts.push([
        PROJECT_NONE,
        t("evidence.picker.option", {
          project: t("evidence.picker.no_project"),
          count: unassigned,
        }),
      ]);
    fillSelect(picker, opts, state.project);
    setText(
      $("ev-scope-note"),
      withUnassigned(state.project) && unassigned
        ? t("evidence.picker.with_unassigned", { count: unassigned })
        : "",
    );
  }

  picker.addEventListener("change", () => {
    rememberProject(picker.value);
    showProject(picker.value);
  });

  function showProject(id: string): void {
    state.project = id;
    state.runsEnv = "";
    const env = state.caseFilters.envId ? envById(state.caseFilters.envId) : undefined;
    const keepEnv = env !== undefined && projectSees(id, env);
    setCaseFilters({
      ...state.caseFilters,
      projectId: state.project,
      includeUnassigned: withUnassigned(state.project),
      ...(keepEnv ? {} : { envId: "", status: statusWithoutEnv(state.caseFilters.status) }),
    });
    renderPicker();
    renderEnvs();
    fillRunsEnv();
    renderBatches();
  }

  function projectSees(project: string, env: EvEnvironment): boolean {
    if (!project) return true;
    if (project === PROJECT_NONE) return caseRunsOn("", env);
    return envKind(env) === "standalone" || env.project_id === project;
  }

  const statusWithoutEnv = (st: CaseFilters["status"]): CaseFilters["status"] =>
    st === NOT_RUN ? "" : st;

  function linkedProject(): string {
    const id = state.envTarget || flash?.id;
    return (id ? envById(id)?.project_id : undefined) ?? "";
  }

  function fillRunsEnv(): void {
    if (!state.envsLoaded) return;
    const keep = state.runsEnv;
    while (rEnv.options.length > 1) rEnv.remove(1);
    rEnv.querySelectorAll("optgroup").forEach((g) => g.remove());
    const groups = groupEnvs(envsInView(), state.pickerOn ? projectList() : []);
    for (const g of groups) {
      const all = g.sandbox ? [...g.envs, g.sandbox] : g.envs;
      if (!all.length) continue;
      const host: HTMLSelectElement | HTMLOptGroupElement =
        groups.length > 1 ? document.createElement("optgroup") : rEnv;
      if (host instanceof HTMLOptGroupElement) {
        host.label = g.projectId ? g.projectName : t("evidence.envs.other");
        rEnv.appendChild(host);
      }
      for (const env of all) host.appendChild(new Option(envTitle(env), env.id));
    }
    rEnv.value = state.envs.some((e) => e.id === keep) ? keep : "";
    state.runsEnv = rEnv.value;
  }

  let ctxRefreshed = false;

  // The context can answer before the environment list shows rows of new projects; refetch it once
  // so the picker knows them.
  function recheckContext(): void {
    if (!state.ctx || !state.envsLoaded || ctxRefreshed) return;
    const known = new Set(state.ctx.projects.map((p) => p.id));
    if (!state.envs.some((e) => e.project_id && !known.has(e.project_id))) return;
    ctxRefreshed = true;
    void loadContext();
  }

  async function loadEnvs(): Promise<void> {
    try {
      const r = await api.get<unknown>("/evidence/environments");
      state.envs = listOf<EvEnvironment>(r, "environments").map((e) => ({
        ...e,
        allowed_hosts: e.allowed_hosts ?? [],
        secret_names: e.secret_names ?? [],
      }));
      state.envsLoaded = true;
      fillRunsEnv();
      applyRole();
      recheckContext();
    } catch (e) {
      if (!state.envsLoaded)
        setHTML(
          $("e-list"),
          emptyRowHTML(errMsg(e, t("evidence.errors.load_envs")), { as: "div", tone: "danger" }),
        );
      return;
    }
    renderEnvs();
    renderCases();
    renderBatches();
  }

  async function loadCases(): Promise<void> {
    try {
      const r = await api.get<CasesResponse>(casesURL(state.caseFilters));
      state.cases = listOf<EvCase>(r, "cases");
      state.counts = r.counts ?? {};
      // Older daemons send no tag list.
      state.tagOptions = r.tags ?? tagsOf(state.cases);
      state.casesLoaded = true;
      $("ev-cases-note").hidden = true;
    } catch (e) {
      const note = $("ev-cases-note");
      note.hidden = false;
      note.classList.add("ev-note-err");
      setText(
        note,
        state.casesLoaded
          ? t("evidence.errors.refresh", { error: errMsg(e, t("evidence.errors.load_cases")) })
          : errMsg(e, t("evidence.errors.load_cases")),
      );
      $("ev-cases-loading").hidden = true;
      throw e;
    }
    renderCases();
    tasks.redraw();
  }

  async function loadBatches(): Promise<void> {
    state.lastBatchFetch = Date.now();
    let list: EvBatch[];
    try {
      list = listOf<EvBatch>(await api.get<unknown>(batchesURL("", 50)), "batches");
    } catch (e) {
      if (!state.batchesLoaded) {
        setHTML(
          $("r-batches"),
          emptyRowHTML(errMsg(e, t("evidence.errors.load_runs")), { as: "li", tone: "danger" }),
        );
      } else {
        const note = $("r-note");
        note.hidden = false;
        setText(
          note,
          t("evidence.errors.refresh", { error: errMsg(e, t("evidence.errors.load_runs")) }),
        );
      }
      throw e;
    }
    $("r-note").hidden = true;
    const sig = batchesSignature(list);
    const changed = state.batchesLoaded && sig !== state.batchSig;
    state.batches = list;
    state.batchSig = sig;
    state.batchesLoaded = true;
    await Promise.all(
      list
        .filter(
          (b) =>
            (state.openBatches.has(b.id) && (isActiveBatch(b) || changed)) ||
            (isSandboxFailed(b) && !state.batchLogs.has(b.id) && !state.batchRuns.has(b.id)),
        )
        .map((b) => loadBatchRuns(b.id)),
    );
    renderBatches();
    renderEnvs();
    if (changed) {
      casesPoller.poke();
      if (drawerModal.isOpen()) void loadDrawer();
    }
  }

  async function loadBatchRuns(id: string): Promise<void> {
    try {
      const r = unwrap<EvBatch>(
        await api.get<unknown>(`/evidence/batches/${encodeURIComponent(id)}`),
        "batch",
      );
      state.batchRuns.set(id, r.runs ?? []);
      if (r.sandbox_log) state.batchLogs.set(id, r.sandbox_log);
    } catch {
      state.batchRuns.set(id, state.batchRuns.get(id) ?? []);
    }
  }

  const anyActive = (): boolean => state.batches.some(isActiveBatch);

  const menu = $("ev-menu");
  // Moved to <body>: a transformed ancestor (phone layout) would make position:fixed relative to
  // it.
  document.body.appendChild(menu);
  let menuCase: EvCase | null = null;
  let menuTrigger: HTMLButtonElement | null = null;
  let menuHandle: MenuHandle | null = null;
  let menuBinding: AbortController | null = null;

  function openMenu(btn: HTMLButtonElement, c: EvCase): void {
    menuCase = c;
    if (menuTrigger === btn) return;
    menuHandle?.close(false);
    menuBinding?.abort();
    menuBinding = new AbortController();
    menuTrigger = btn;
    menuHandle = bindMenu(menu, btn, {
      signal: menuBinding.signal,
      onOpen: () => {
        const cur = menuCase;
        if (!cur) return;
        menu.setAttribute("aria-label", t("evidence.actions.more_aria", { key: cur.key }));
        setText(
          $("ev-menu-record"),
          isRecorded(cur) ? t("evidence.actions.record_again") : t("evidence.actions.record"),
        );
        const run = menu.querySelector<HTMLElement>("[data-m-act='run']");
        if (isRecorded(cur)) run?.removeAttribute("aria-disabled");
        else run?.setAttribute("aria-disabled", "true");
      },
      onSelect: (item) => {
        const cur = menuCase;
        if (!cur) return;
        const act = item.dataset.mAct;
        if (act === "record") openRecordModal(cur);
        else if (act === "run") openRunModal({ kind: "case", c: cur });
        else if (act === "edit") openCaseModal(cur);
        else if (act === "delete") void deleteCase(cur);
      },
    });
    menuHandle.open();
  }
  signal.addEventListener("abort", () => menuBinding?.abort());

  const grid = $("ev-cases-grid");
  const listPanel = $("ev-cases-list");
  const rows = $("ev-cases-rows");
  const chips = $("ev-status-chips");

  function renderChips(): void {
    const counts = state.counts;
    const picked = state.caseFilters.status;
    const env = state.caseFilters.envId ? envById(state.caseFilters.envId) : undefined;
    const all = {
      value: "",
      label: env ? t("evidence.filters.all_on", { env: envTitle(env) }) : t("evidence.cases.all"),
      count: totalOf(counts),
    };
    const items = visibleStatuses(counts, picked).map((s) => ({
      value: s,
      label: statusLabel(s),
      count: counts[s] ?? 0,
    }));
    setHTML(
      chips,
      filterChipsHTML([all, ...items], picked, {
        attr: "status",
        size: "md",
        label: t("evidence.cases.filter_status"),
      }),
    );
  }

  function renderSummary(): void {
    const pass = state.counts.pass ?? 0;
    const failing = state.counts.fail ?? 0;
    const blocked = state.counts.blocked ?? 0;
    const p = $("ev-sum-pass");
    const f = $("ev-sum-fail");
    const b = $("ev-sum-blocked");
    setCount(p, pass, { pill: true, label: t("evidence.summary.passing") });
    setCount(f, failing, { pill: true, tone: "danger", label: t("evidence.summary.failing") });
    setCount(b, blocked, { pill: true, tone: "warn", label: t("evidence.summary.blocked") });
    p.hidden = !state.casesLoaded || totalOf(state.counts) === 0;
    f.hidden = p.hidden || failing === 0;
    b.hidden = p.hidden || blocked === 0;
  }

  function fillEnvFilter(): void {
    const keep = state.caseFilters.envId;
    while (fEnv.options.length > 1) fEnv.remove(1);
    fEnv.querySelectorAll("optgroup").forEach((g) => g.remove());
    const groups = groupEnvs(envsInView(), state.pickerOn ? projectList() : []);
    for (const g of groups) {
      const all = g.sandbox ? [...g.envs, g.sandbox] : g.envs;
      if (!all.length) continue;
      const host: HTMLSelectElement | HTMLOptGroupElement =
        groups.length > 1 ? document.createElement("optgroup") : fEnv;
      if (host instanceof HTMLOptGroupElement) {
        host.label = g.projectId ? g.projectName : t("evidence.envs.other");
        fEnv.appendChild(host);
      }
      for (const env of all) host.appendChild(new Option(envTitle(env), env.id));
    }
    if (keep && !state.envs.some((e) => e.id === keep)) fEnv.add(new Option(keep, keep));
    fEnv.value = keep;
  }

  function syncCaseControls(): void {
    const f = state.caseFilters;
    fProject.value = f.projectId;
    fillSelect(
      fTag,
      state.tagOptions
        .filter((tag) => !f.tags.includes(tag))
        .map((tag): [string, string] => [tag, tag]),
      "",
    );
    fTag.options[0].text = f.tags.length
      ? t("evidence.filters.add_tag")
      : t("evidence.filters.all_tags");
    setHTML(
      fTags,
      f.tags
        .map((tag) =>
          labelTagHTML(tag, {
            removable: {
              label: t("evidence.filters.remove_tag", { tag }),
              attrs: { "data-tag": tag },
            },
          }),
        )
        .join(""),
    );
    fTags.hidden = f.tags.length === 0;
    $("f-tags-note").hidden = f.tags.length < 2;
    fPriority.value = f.priority;
    if (state.envsLoaded) fillEnvFilter();
    if (document.activeElement !== fQ) fQ.value = f.q;
    fClear.hidden = !hasOwnCaseFilters(f, state.pickerOn);
    setSegmentedValue($("ev-view"), state.view);
  }

  function renderCases(): void {
    syncCaseControls();
    renderChips();
    renderSummary();
    if (!state.casesLoaded) return;
    $("ev-cases-loading").hidden = true;
    const none = state.cases.length === 0;
    const filtered = hasOwnCaseFilters(state.caseFilters, state.pickerOn);
    $("ev-cases-empty").hidden = !none || filtered;
    $("ev-cases-nomatch").hidden = !none || !filtered;
    $("panel-cases").classList.toggle("ev-library-empty", none && !filtered);
    const runAll = $("ev-run-all") as HTMLButtonElement;
    setOff(runAll, { allowed: state.canWrite, reason: "viewer" });
    if (none) runAll.disabled = true;
    grid.hidden = none || state.view !== "grid";
    listPanel.hidden = none || state.view !== "list";
    if (none) return;
    const ctx = listCtx();
    const cases = sortCases(
      state.cases,
      state.sort,
      (c) => cardStatus(c, state.envs, state.caseFilters.envId).status,
    );
    const groups = state.pickerOn && !state.project ? groupCases(cases, state.projects) : null;
    listPanel.classList.toggle("ev-grouped", groups !== null);
    if (state.view === "grid")
      setHTML(
        grid,
        groups ? caseGroupsGridHTML(groups, ctx) : cases.map((c) => caseCardHTML(c, ctx)).join(""),
      );
    else
      setHTML(
        rows,
        groups ? caseGroupsRowsHTML(groups, ctx) : cases.map((c) => caseRowHTML(c, ctx)).join(""),
      );
    if (menuTrigger && !menuTrigger.isConnected) menuHandle?.close(false);
  }

  function setCaseFilters(next: CaseFilters): void {
    state.caseFilters = next;
    writeURL();
    syncCaseControls();
    renderChips();
    casesPoller.poke();
  }

  chips.addEventListener("click", (e) => {
    const b = (e.target as HTMLElement).closest<HTMLButtonElement>("[data-status]");
    if (!b) return;
    const s = (b.dataset.status ?? "") as LibraryStatus | "";
    setCaseFilters({ ...state.caseFilters, status: state.caseFilters.status === s ? "" : s });
  });
  fProject.addEventListener("change", () =>
    setCaseFilters({ ...state.caseFilters, projectId: fProject.value }),
  );
  fTag.addEventListener("change", () => {
    const tag = fTag.value;
    if (!tag) return;
    setCaseFilters({ ...state.caseFilters, tags: [...state.caseFilters.tags, tag] });
    fTag.focus();
  });
  fTags.addEventListener("click", (e) => {
    const b = (e.target as HTMLElement).closest<HTMLButtonElement>("[data-tag]");
    if (!b) return;
    const tags = state.caseFilters.tags.filter((tag) => tag !== b.dataset.tag);
    setCaseFilters({ ...state.caseFilters, tags });
    (fTags.querySelector<HTMLButtonElement>("[data-tag]") ?? fTag).focus();
  });
  fPriority.addEventListener("change", () =>
    setCaseFilters({
      ...state.caseFilters,
      priority: fPriority.value as CaseFilters["priority"],
    }),
  );
  fEnv.addEventListener("change", () =>
    setCaseFilters({ ...state.caseFilters, envId: fEnv.value, status: "" }),
  );
  let searchTimer = 0;
  fQ.addEventListener("input", () => {
    window.clearTimeout(searchTimer);
    searchTimer = window.setTimeout(
      () => setCaseFilters({ ...state.caseFilters, q: fQ.value }),
      250,
    );
  });
  const clearedFilters = (): CaseFilters =>
    state.pickerOn
      ? {
          ...EMPTY_CASE_FILTERS,
          projectId: state.project,
          includeUnassigned: withUnassigned(state.project),
        }
      : EMPTY_CASE_FILTERS;
  fClear.addEventListener("click", () => setCaseFilters(clearedFilters()));
  $("ev-nomatch-clear").addEventListener("click", () => setCaseFilters(clearedFilters()));
  $("ev-view").addEventListener("change", () => {
    state.view = segmentedValue($("ev-view")) === "list" ? "list" : "grid";
    try {
      localStorage.setItem(VIEW_KEY, state.view);
    } catch {
      // private window: the choice lasts for this visit
    }
    writeURL();
    renderCases();
  });
  const fSort = $("f-sort") as HTMLSelectElement;
  fSort.value = state.sort;
  fSort.addEventListener("change", () => {
    state.sort = (CASE_SORTS as string[]).includes(fSort.value)
      ? (fSort.value as CaseSort)
      : "newest";
    try {
      localStorage.setItem(SORT_KEY, state.sort);
    } catch {
      // private window: the order lasts for this visit
    }
    renderCases();
  });
  $("ev-new-case").addEventListener("click", () => openCaseModal(null));
  $("ev-empty-new").addEventListener("click", () => openCaseModal(null));
  $("ev-run-all").addEventListener("click", () => openRunModal({ kind: "all" }));

  function onCaseListClick(e: MouseEvent): void {
    const target = e.target as HTMLElement;
    const host = target.closest<HTMLElement>("[data-case]");
    if (!host) return;
    const c = findCase(host.dataset.case ?? "");
    if (!c) return;
    const btn = target.closest<HTMLButtonElement>("button[data-act]");
    if (btn) {
      if (btn.dataset.act === "run") openRunModal({ kind: "case", c });
      else if (btn.dataset.act === "record") openRecordModal(c);
      else if (btn.dataset.act === "menu") openMenu(btn, c);
      return;
    }
  }
  grid.addEventListener("click", onCaseListClick);
  rows.addEventListener("click", onCaseListClick);

  const batchList = $("r-batches");

  function caseNames(): Map<string, { key: string; title: string }> {
    return new Map(state.cases.map((c) => [c.id, { key: c.key, title: c.title }]));
  }

  function renderBatches(): void {
    $("r-live").hidden = !anyActive();
    if (!state.batchesLoaded) return;
    const inView = new Set(envsInView().map((e) => e.id));
    const shown = state.batches.filter(
      (b) =>
        (!state.runsEnv || b.environment_id === state.runsEnv) &&
        (!state.pickerOn || !state.project || !state.envsLoaded || inView.has(b.environment_id)),
    );
    $("r-empty").hidden = shown.length > 0;
    $("r-batches-box").hidden = shown.length === 0;
    const runAll = $("r-run-all") as HTMLButtonElement;
    setOff(runAll, { allowed: state.canWrite, reason: "viewer" });
    if (state.cases.length === 0 && state.casesLoaded) runAll.disabled = true;
    const names = caseNames();
    setHTML(
      batchList,
      shown
        .map((b) => {
          const env = envById(b.environment_id);
          return batchHTML(b, {
            envName: env ? envTitle(env) : "",
            open: state.openBatches.has(b.id),
            canWrite: state.canWrite,
            canRetry: env
              ? permission(env, "run")
              : { allowed: false, reason: "sandbox_unavailable" },
            initScriptURL: env?.sandbox?.init_script_url ?? "",
            canAdmin: state.canAdmin,
            runs: state.batchRuns.get(b.id),
            log: state.batchLogs.get(b.id),
            caseNames: names,
          });
        })
        .join(""),
    );
  }

  batchList.addEventListener("click", (e) => {
    const target = e.target as HTMLElement;
    const btn = target.closest<HTMLButtonElement>("button[data-act]");
    const li = target.closest<HTMLElement>("[data-batch]");
    if (!btn || !li) return;
    const id = li.dataset.batch ?? "";
    if (btn.dataset.act === "toggle-batch") {
      if (state.openBatches.has(id)) state.openBatches.delete(id);
      else {
        state.openBatches.add(id);
        void loadBatchRuns(id).then(renderBatches);
      }
      renderBatches();
      batchList
        .querySelector<HTMLElement>(`[data-batch="${CSS.escape(id)}"] [data-act="toggle-batch"]`)
        ?.focus();
    } else if (btn.dataset.act === "cancel-batch") {
      void cancelBatch(id, btn);
    } else if (btn.dataset.act === "retry-batch") {
      void retryBatch(id, btn);
    }
  });

  async function retryBatch(id: string, btn: HTMLButtonElement): Promise<void> {
    const b = state.batches.find((x) => x.id === id);
    if (!b) return;
    const done = busy(btn, t("evidence.run.starting"));
    try {
      if (!state.batchRuns.has(id)) await loadBatchRuns(id);
      const caseIds = [...new Set((state.batchRuns.get(id) ?? []).map((r) => r.case_id))];
      const body: Record<string, unknown> = { environment_id: b.environment_id };
      if (caseIds.length) body.case_ids = caseIds;
      if (b.source_branch) body.branch = b.source_branch;
      await api.post<unknown>("/evidence/runs", body);
      toast(t("evidence.sandbox.retried", { env: envName(b.environment_id) }), "ok");
      batchesPoller.poke();
    } catch (e) {
      toast(errMsg(e, t("evidence.errors.run")), "err");
    } finally {
      done();
    }
  }

  async function cancelBatch(id: string, btn: HTMLButtonElement): Promise<void> {
    const ok = await confirmDialog({
      title: t("evidence.runs.cancel_title"),
      message: t("evidence.runs.cancel_message"),
      confirmLabel: t("evidence.runs.cancel_confirm"),
      danger: true,
    });
    if (!ok) return;
    const done = busy(btn, t("evidence.runs.canceling"));
    try {
      await api.post(`/evidence/batches/${encodeURIComponent(id)}/cancel`);
      toast(t("evidence.runs.canceled"), "ok");
      batchesPoller.poke();
    } catch (e) {
      toast(errMsg(e, t("evidence.errors.cancel")), "err");
    } finally {
      done();
    }
  }

  rEnv.addEventListener("change", () => {
    state.runsEnv = rEnv.value;
    writeURL();
    renderBatches();
  });
  $("r-run-all").addEventListener("click", () =>
    openRunModal(state.runsEnv ? { kind: "env", envId: state.runsEnv } : { kind: "all" }),
  );

  const envList = $("e-list");

  function renderEnvs(): void {
    if (!state.envsLoaded) return;
    const envs = envsInView();
    const projects =
      state.pickerOn && state.project
        ? projectList().filter((p) => p.id === state.project)
        : projectList();
    const groups = groupEnvs(envs, state.ctx ? projects : []);
    const empty = groups.length === 0;
    $("e-empty").hidden = !empty;
    envList.hidden = empty;
    const last = lastBatchByEnv(state.batches);
    const headings = groups.some((g) => g.projectId !== "");
    setHTML(
      envList,
      envGroupsHTML(groups, {
        lastBatch: (id) => last.get(id),
        canCreate: wsCan("create_environment"),
        canAdmin: state.canAdmin,
        headings,
      }),
    );
    if (flash && Date.now() < flash.until)
      document.getElementById(`ev-env-${flash.id}`)?.classList.add(CARD_TARGET);
    showEnvTarget();
  }

  function showEnvTarget(): void {
    if (!state.envTarget || state.tab !== "environments") return;
    const id = state.envTarget;
    const card = document.getElementById(`ev-env-${id}`);
    if (!card) {
      const project = linkedProject();
      if (state.pickerOn && project && project !== state.project) showProject(project);
      return;
    }
    state.envTarget = "";
    writeURL();
    flash = { id, until: Date.now() + 4000 };
    card.classList.add(CARD_TARGET);
    card.scrollIntoView({ block: "center" });
    card.focus({ preventScroll: true });
    window.setTimeout(() => {
      flash = null;
      document.getElementById(`ev-env-${id}`)?.classList.remove(CARD_TARGET);
    }, 4000);
  }

  envList.addEventListener("click", (e) => {
    const target = e.target as HTMLElement;
    const btn = target.closest<HTMLButtonElement>("button[data-act]");
    if (!btn) return;
    if (btn.dataset.act === "new-env") {
      openEnvModal(null, btn.dataset.project ?? "");
      return;
    }
    const card = target.closest<HTMLElement>("[data-env]");
    if (!card) return;
    const env = state.envs.find((x) => x.id === card.dataset.env);
    if (!env) return;
    if (btn.dataset.act === "run-env") openRunModal({ kind: "env", envId: env.id });
    else if (btn.dataset.act === "edit-env") openEnvModal(env);
    else if (btn.dataset.act === "delete-env") void deleteEnv(env);
    else if (btn.dataset.act === "link-env") openLinkModal(env);
  });
  $("e-new").addEventListener("click", () => openEnvModal(null));
  $("e-empty-new").addEventListener("click", () => openEnvModal(null));

  async function deleteEnv(env: EvEnvironment): Promise<void> {
    const name = envLabel(env);
    const ok = await confirmDialog({
      title: t("evidence.envs.delete_title", { name }),
      message: t("evidence.envs.delete_message"),
      confirmLabel: t("evidence.envs.delete_confirm"),
      danger: true,
    });
    if (!ok) return;
    try {
      await api.del(`/evidence/environments/${encodeURIComponent(env.id)}`);
      toast(t("evidence.envs.deleted", { name }), "ok");
      await loadEnvs();
    } catch (e) {
      toast(errMsg(e, t("evidence.errors.delete_env")), "err");
    }
  }

  function envOptionsHTML(
    name: string,
    envs: EvEnvironment[],
    selected: string,
    action: "run" | "record",
  ): string {
    if (!envs.length) {
      const create = wsCan("create_environment");
      const add = buttonHTML({
        label: t("evidence.envs.new"),
        size: "sm",
        icon: "plus",
        attrs: { "data-act": "add-env" },
        ...offProps(create),
      });
      const msg = state.envs.length
        ? t("evidence.pickers.none_for_case")
        : t("evidence.pickers.no_envs");
      return emptyStateHTML({ size: "sm", dashed: true, body: msg, actionHTML: add });
    }
    const usable = envs.filter((env) => permission(env, action).allowed);
    const first = usable.find((e) => !e.is_production) ?? usable.at(0);
    const pick = usable.some((e) => e.id === selected) ? selected : (first?.id ?? "");
    const groups = groupEnvs(envs);
    const option = (env: EvEnvironment): string => {
      const p = permission(env, action);
      const isSandbox = envKind(env) === "sandbox";
      const sub = isSandbox
        ? t("evidence.sandbox.option_sub", {
            branch: sandboxBranch(env),
          })
        : env.base_url || t("evidence.envs.no_url");
      // The fix link goes outside the card: a link inside a <label> selects the radio instead of
      // navigating.
      const sandboxWhy =
        !p.allowed && isSandbox && env.sandbox
          ? sandboxReasonHTML(p.reason, env.sandbox.init_script_url, state.canAdmin)
          : "";
      const card = checkHTML({
        type: "radio",
        card: true,
        name,
        value: env.id,
        checked: env.id === pick,
        disabled: !p.allowed,
        reason: p.allowed || sandboxWhy ? undefined : reasonLabel(p.reason),
        labelHTML: esc(envLabel(env)),
        hintHTML: `<span class="ev-env-url">${esc(sub)}</span>`,
        trailingHTML: prodBadgeHTML(env) || undefined,
      });
      return sandboxWhy ? `<div class="ev-env-opt-wrap">${card}${sandboxWhy}</div>` : card;
    };
    return groups
      .map((g) => {
        const all = g.sandbox ? [...g.envs, g.sandbox] : g.envs;
        const head =
          groups.length > 1
            ? groupHeadHTML({ title: g.projectId ? g.projectName : t("evidence.envs.other") })
            : "";
        return head + all.map(option).join("");
      })
      .join("");
  }

  const pickedEnv = (name: string): string =>
    document.querySelector<HTMLInputElement>(`input[name="${name}"]:checked`)?.value ?? "";

  function goAddEnv(close: () => void): void {
    close();
    showTab("environments");
    openEnvModal(null);
  }

  const recModal = bindModal($("record-modal"));
  const recStart = $("rec-start") as HTMLButtonElement;
  const recAgent = $("rec-agent") as HTMLSelectElement;
  let recCase: EvCase | null = null;
  let recAgents: RecordingAgents | null = null;
  let recEnvsOK = false;

  async function loadAgents(): Promise<void> {
    recAgents = null;
    syncRecord();
    try {
      const r = await api.get<RecordingAgents>("/evidence/recording-agents");
      recAgents = { agents: listOf(r, "agents"), auto: r.auto };
    } catch {
      // Older daemons lack /evidence/recording-agents; the automatic pick still works.
      recAgents = null;
      $("rec-agent-note").hidden = true;
      recStart.disabled = !recEnvsOK;
      return;
    }
    const keep = recAgent.value;
    while (recAgent.options.length > 1) recAgent.remove(1);
    for (const a of recAgents.agents) recAgent.add(new Option(recordingAgentLabel(a), a.type));
    recAgent.value = recAgents.agents.some((a) => a.type === keep) ? keep : "";
    syncRecord();
  }

  function syncRecord(): void {
    const note = $("rec-agent-note");
    if (!recAgents) {
      note.hidden = true;
      recStart.disabled = true;
      return;
    }
    const { agent, block } = recordingPick(recAgents, recAgent.value);
    const name = agent ? agent.name || agent.type : "";
    const link = (href: string, label: string) =>
      ` <a class="ev-link" href="${href}">${esc(label)}</a>`;
    let html = "";
    if (block === "no_agent")
      html = esc(t("evidence.record.no_agent")) + link("/agents/", t("evidence.record.go_agents"));
    else if (block === "no_credential")
      html =
        esc(t("evidence.record.no_credential", { name })) +
        link("/settings/#llm", t("evidence.record.go_credentials"));
    else if (block === "no_prompt" && agent)
      html =
        esc(t("evidence.record.no_prompt", { name })) +
        link(recordingPromptHref(agent), t("evidence.record.go_prompts"));
    else if (!recAgent.value) html = esc(t("evidence.record.auto_picks", { name }));
    note.hidden = html === "";
    note.classList.toggle("ev-rec-blocked", block !== "");
    setHTML(note, html);
    recStart.disabled = !recEnvsOK || block !== "";
  }

  recAgent.addEventListener("change", syncRecord);

  function openRecordModal(c: EvCase): void {
    recCase = c;
    setText(
      $("rec-lead"),
      isRecorded(c)
        ? t("evidence.record.lead_again", { key: c.key, title: c.title })
        : t("evidence.record.lead", { key: c.key, title: c.title }),
    );
    // A Sandbox lives for one batch only, so it cannot be recorded on.
    const envs = state.envs.filter(
      (env) => envKind(env) !== "sandbox" && caseRunsOn(c.project_id, env),
    );
    $("rec-envs").innerHTML = envOptionsHTML("rec-env", envs, "", "record");
    setFormError($("rec-error"), "");
    recEnvsOK = envs.some((env) => permission(env, "record").allowed);
    recModal.open(document.querySelector<HTMLElement>('input[name="rec-env"]:checked') ?? recStart);
    void loadAgents();
  }

  $("rec-envs").addEventListener("click", (e) => {
    if ((e.target as HTMLElement).closest("[data-act='add-env']")) goAddEnv(recModal.close);
  });

  recStart.addEventListener("click", () => void startRecording());

  async function startRecording(): Promise<void> {
    const c = recCase;
    const envId = pickedEnv("rec-env");
    if (!c || !envId) return;
    const done = busy(recStart, t("evidence.record.starting"));
    setFormError($("rec-error"), "");
    try {
      const body: Record<string, string> = { environment_id: envId };
      if (recAgent.value) body.agent_type = recAgent.value;
      const r = await api.post<{ chat_id?: string }>(
        `/evidence/cases/${encodeURIComponent(c.key)}/record`,
        body,
      );
      recModal.close();
      toast(t("evidence.record.started", { key: c.key, env: envName(envId) }), "ok", 6000);
      if (r.chat_id)
        document.dispatchEvent(new CustomEvent("octopus:open-chat", { detail: { id: r.chat_id } }));
    } catch (e) {
      setFormError($("rec-error"), errMsg(e, t("evidence.errors.record")));
    } finally {
      done();
    }
  }

  const runModal = bindModal($("run-modal"));
  const runStart = $("run-start") as HTMLButtonElement;
  const runBranch = $("run-branch") as HTMLInputElement;
  let runScope: RunScope = { kind: "all" };

  function runEnvs(scope: RunScope): EvEnvironment[] {
    switch (scope.kind) {
      case "case":
        return state.envs.filter((env) => caseRunsOn(scope.c.project_id, env));
      case "env":
        return state.envs;
      case "all": {
        const ids = state.cases.filter(isRecorded).map((c) => c.project_id);
        return envsForProjects(state.envs, ids.length ? ids : [""]);
      }
    }
  }

  function syncRunBranch(): void {
    const env = envById(pickedEnv("run-env"));
    const sandbox = env && envKind(env) === "sandbox";
    $("run-branch-row").hidden = !sandbox;
    setFieldError(runBranch, "");
    if (sandbox && runBranch.dataset.env !== env.id) {
      runBranch.value = sandboxBranch(env);
      runBranch.dataset.env = env.id;
    }
  }

  function listCasesOn(envId: string): EvCase[] {
    const env = envById(envId);
    return state.cases.filter((c) => isRecorded(c) && (!env || caseRunsOn(c.project_id, env)));
  }

  function runLead(scope: RunScope): string {
    switch (scope.kind) {
      case "case":
        return t("evidence.run.lead_case", { key: scope.c.key, title: scope.c.title });
      case "env":
        return t("evidence.run.lead_env");
      case "all": {
        const recorded = state.cases.filter(isRecorded).length;
        const runs = listCasesOn(pickedEnv("run-env")).length;
        const drafts = state.cases.length - recorded;
        return [
          t("evidence.run.lead_list", { count: runs }),
          recorded > runs ? t("evidence.run.lead_list_skipped", { count: recorded - runs }) : "",
          drafts ? t("evidence.run.lead_list_drafts", { count: drafts }) : "",
        ]
          .filter(Boolean)
          .join(" ");
      }
    }
  }

  function openRunModal(scope: RunScope): void {
    runScope = scope;
    const envs = runEnvs(scope);
    $("run-envs").innerHTML = envOptionsHTML(
      "run-env",
      envs,
      scope.kind === "env" ? scope.envId : "",
      "run",
    );
    setText($("run-lead"), runLead(scope));
    setFormError($("run-error"), "");
    runBranch.dataset.env = "";
    syncRunBranch();
    runStart.disabled = !envs.some((env) => permission(env, "run").allowed);
    runModal.open(document.querySelector<HTMLElement>('input[name="run-env"]:checked') ?? runStart);
  }

  $("run-envs").addEventListener("click", (e) => {
    if ((e.target as HTMLElement).closest("[data-act='add-env']")) goAddEnv(runModal.close);
  });
  $("run-envs").addEventListener("change", () => {
    syncRunBranch();
    setText($("run-lead"), runLead(runScope));
  });
  runStart.addEventListener("click", () => void startRun());

  async function startRun(): Promise<void> {
    const envId = pickedEnv("run-env");
    if (!envId) return;
    const scope = runScope;
    const body: Record<string, unknown> = { environment_id: envId };
    if (scope.kind === "case") body.case_ids = [scope.c.id];
    if (scope.kind === "all") {
      const ids = listCasesOn(envId).map((c) => c.id);
      if (!ids.length) {
        runModal.close();
        toast(t("evidence.run.nothing", { env: envName(envId) }), "info", 6000);
        return;
      }
      body.case_ids = ids;
    }
    const env = envById(envId);
    if (env && envKind(env) === "sandbox") {
      const branch = runBranch.value.trim();
      if (!branch) {
        setFieldError(runBranch, t("evidence.run.branch_required"), { focus: true });
        return;
      }
      body.branch = branch;
    }
    const done = busy(runStart, t("evidence.run.starting"));
    setFormError($("run-error"), "");
    try {
      const batch = unwrap<EvBatch>(await api.post<unknown>("/evidence/runs", body), "batch");
      const n = batch.runs?.length ?? batchCounts(batch).total;
      runModal.close();
      if (n === 0) toast(t("evidence.run.nothing", { env: envName(envId) }), "info", 6000);
      else if (typeof body.branch === "string")
        toast(t("evidence.sandbox.queued", { count: n, branch: body.branch }), "ok", 6000);
      else toast(t("evidence.run.started", { count: n, env: envName(envId) }), "ok");
      batchesPoller.poke();
      casesPoller.poke();
    } catch (e) {
      setFormError($("run-error"), errMsg(e, t("evidence.errors.run")));
    } finally {
      done();
    }
  }

  const caseModal = bindModal($("case-modal"));
  const caseForm = $("case-form") as HTMLFormElement;
  const cf = {
    title: $("cf-title") as HTMLInputElement,
    description: $("cf-description") as HTMLTextAreaElement,
    steps: $("cf-steps") as HTMLTextAreaElement,
    expected: $("cf-expected") as HTMLTextAreaElement,
    priority: $("cf-priority") as HTMLSelectElement,
    project: $("cf-project") as HTMLSelectElement,
    tags: $("cf-tags") as HTMLInputElement,
    save: $("cf-save") as HTMLButtonElement,
  };
  let editingCase: EvCase | null = null;

  function openCaseModal(c: EvCase | null): void {
    editingCase = c;
    setText(
      $("case-modal-title"),
      c ? t("evidence.case_form.edit_title", { key: c.key }) : t("evidence.case_form.new_title"),
    );
    cf.title.value = c?.title ?? "";
    cf.description.value = c?.description ?? "";
    cf.steps.value = c?.steps ?? "";
    cf.expected.value = c?.expected ?? "";
    cf.priority.value = c?.priority ?? "medium";
    cf.project.value =
      c?.project_id ??
      (state.caseFilters.projectId === PROJECT_NONE ? "" : state.caseFilters.projectId);
    cf.tags.value = (c?.tags ?? state.caseFilters.tags).join(", ");
    clearFormErrors(caseForm);
    caseModal.open(cf.title);
  }

  caseForm.addEventListener("submit", (e) => {
    e.preventDefault();
    void saveCase();
  });

  async function saveCase(): Promise<void> {
    const title = cf.title.value.trim();
    clearFormErrors(caseForm);
    if (!title) {
      setFieldError(cf.title, t("evidence.case_form.title_required"), { focus: true });
      return;
    }
    const body = {
      title,
      description: cf.description.value,
      steps: cf.steps.value,
      expected: cf.expected.value,
      priority: cf.priority.value,
      tags: parseTags(cf.tags.value),
      project_id: cf.project.value,
    };
    const was = editingCase;
    const done = busy(cf.save, t("evidence.saving"));
    try {
      const r = was
        ? await api.patch<unknown>(`/evidence/cases/${encodeURIComponent(was.key)}`, body)
        : await api.post<unknown>("/evidence/cases", body);
      const saved = unwrap<EvCase>(r, "case");
      caseModal.close();
      casesPoller.poke();
      if (was) {
        toast(t("evidence.case_form.saved", { key: was.key }), "ok");
        if (drawerModal.isOpen() && drawer.key === was.key) void loadDrawer();
      } else {
        toast(t("evidence.case_form.created", { key: saved.key }), "ok");
        location.hash = caseHash(saved.key);
      }
    } catch (e) {
      setFormError($("cf-error"), errMsg(e, t("evidence.errors.save_case")));
    } finally {
      done();
    }
  }

  async function deleteCase(c: EvCase): Promise<void> {
    const ok = await confirmDialog({
      title: t("evidence.delete.title", { key: c.key }),
      message: t("evidence.delete.message", { title: c.title }),
      confirmLabel: t("evidence.delete.confirm"),
      danger: true,
    });
    if (!ok) return;
    try {
      await api.del(`/evidence/cases/${encodeURIComponent(c.key)}`);
      toast(t("evidence.delete.done", { key: c.key }), "ok");
      if (drawerModal.isOpen() && drawer.key === c.key) drawerModal.close();
      state.cases = state.cases.filter((x) => x.id !== c.id);
      renderCases();
      casesPoller.poke();
    } catch (e) {
      toast(errMsg(e, t("evidence.errors.delete_case")), "err");
    }
  }

  const drawerEl = $("ev-drawer");
  const drawer = {
    key: "",
    runId: "",
    envFilter: "",
    detail: null as EvCaseDetail | null,
    run: null as EvRun | null,
    runs: new Map<string, EvRun>(),
    chapters: new Map<string, string>(),
    opener: null as HTMLElement | null,
    seq: 0,
  };
  const drawerModal = bindDrawer(drawerEl, { onClose: () => afterDrawerClosed(true) });
  const drawerBox = drawerEl.querySelector<HTMLElement>(".drawer");
  const dRun = $("ev-d-runbox");

  function afterDrawerClosed(dropHash: boolean): void {
    drawerEl.querySelector("video")?.pause();
    drawer.key = "";
    drawer.detail = null;
    drawer.runId = "";
    drawer.envFilter = "";
    if (dropHash && parseCaseHash(location.hash)) writeURL("");
    drawer.opener?.focus();
    drawer.opener = null;
  }

  function closeDrawerFromHash(): void {
    if (!drawerModal.isOpen()) return;
    drawerEl.classList.remove("open");
    afterDrawerClosed(false);
  }

  function openDrawer(key: string, runId: string): void {
    const same = drawerModal.isOpen() && drawer.key === key;
    drawer.runId = runId;
    if (!same) {
      drawer.key = key;
      drawer.detail = null;
      drawer.envFilter = "";
      drawer.opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      $("ev-d-loading").hidden = false;
      setHTML($("ev-d-loading"), emptyRowHTML(undefined, { as: "div", loading: true }));
      for (const id of ["ev-d-draft", "ev-d-run-section"]) $(id).hidden = true;
      setText($("ev-d-key"), key);
      setText($("ev-drawer-title"), "");
      $("ev-d-status").innerHTML = "";
      $("ev-d-meta").innerHTML = "";
      setHTML(dRun, "");
      drawerModal.open(drawerBox);
    }
    void loadDrawer();
  }

  async function loadDrawer(): Promise<void> {
    const seq = ++drawer.seq;
    const key = drawer.key;
    if (!key) return;
    try {
      const r = await api.get<unknown>(`/evidence/cases/${encodeURIComponent(key)}`);
      if (seq !== drawer.seq) return;
      drawer.detail = unwrap<EvCaseDetail>(r, "case");
    } catch (e) {
      if (seq !== drawer.seq) return;
      if (e instanceof ApiError && e.status === 404) {
        toast(t("evidence.errors.case_gone", { key }), "err");
        drawerModal.close();
        return;
      }
      if (!drawer.detail)
        setHTML(
          $("ev-d-loading"),
          emptyRowHTML(errMsg(e, t("evidence.errors.load_case")), { as: "div", tone: "danger" }),
        );
      return;
    }
    $("ev-d-loading").hidden = true;
    renderDrawer();
  }

  function takeStatus(c: EvCaseDetail): string {
    if (!isRecorded(c)) return t("evidence.detail.take_none");
    const when = whenLabel(c.take_updated_at);
    if (c.take_source === "agent") return t("evidence.detail.take_agent", { when });
    if (c.take_source === "task") return t("evidence.detail.take_task", { when });
    if (c.take_source === "upload") return t("evidence.detail.take_upload", { when });
    return t("evidence.detail.take_ready", { when });
  }

  function renderDrawer(): void {
    const c = drawer.detail;
    if (!c) return;
    const recorded = isRecorded(c);
    setText($("ev-d-key"), c.key);
    setText($("ev-drawer-title"), c.title);
    setHTML($("ev-d-status"), statusPillHTML(c.status));
    const project = c.project_id ? projectName(c.project_id) : "";
    setHTML(
      $("ev-d-meta"),
      [
        casePriorityTagHTML(c.priority, {
          label: t("evidence.detail.priority", { priority: priorityLabel(c.priority) }),
        }),
        project ? `<span class="ev-project">${icoSvg("i-folder")}${esc(project)}</span>` : "",
        (c.tags ?? []).map((tag) => labelTagHTML(tag, { size: "sm" })).join(""),
      ].join(""),
    );
    setText(
      $("ev-d-record-label"),
      recorded ? t("evidence.actions.record_again") : t("evidence.actions.record"),
    );
    const recBtn = $("ev-d-record");
    recBtn.classList.toggle("btn-primary", !recorded);
    const runBtn = $("ev-d-run") as HTMLButtonElement;
    setOff(runBtn, { allowed: state.canWrite, reason: "viewer" });
    if (!recorded) runBtn.disabled = true;
    runBtn.classList.toggle("btn-primary", recorded);

    $("ev-d-draft").hidden = recorded;
    $("ev-d-draft-noenv").hidden = state.envs.some(
      (env) => envKind(env) !== "sandbox" && caseRunsOn(c.project_id, env),
    );

    setHTML($("ev-d-description"), mdText(c.description));
    setHTML($("ev-d-steps"), mdText(c.steps));
    setHTML($("ev-d-expected"), mdText(c.expected));

    setText($("ev-d-take-status"), takeStatus(c));
    $("ev-d-take-download").hidden = c.take === undefined || c.take === null;

    const runs = c.runs ?? [];
    const envsWithRuns = state.envs.filter((env) => runs.some((r) => r.environment_id === env.id));
    if (drawer.envFilter && !envsWithRuns.some((e) => e.id === drawer.envFilter))
      drawer.envFilter = "";
    const envChips = [
      { value: "", label: t("evidence.detail.all_envs") },
      ...envsWithRuns.map((e) => ({ value: e.id, label: envLabel(e) })),
    ];
    setHTML(
      $("ev-d-envs"),
      envsWithRuns.length > 1
        ? filterChipsHTML(envChips, drawer.envFilter, {
            attr: "env",
            label: t("evidence.detail.env_filter"),
          })
        : "",
    );
    $("ev-d-run-section").hidden = !recorded && runs.length === 0;
    void selectRun();
  }

  async function selectRun(): Promise<void> {
    const c = drawer.detail;
    if (!c) return;
    const runs = c.runs ?? [];
    const pool = drawer.envFilter
      ? runs.filter((r) => r.environment_id === drawer.envFilter)
      : runs;
    const run = pickRun(pool, drawer.runId);
    renderHistory(pool, run?.id ?? "");
    if (!run) {
      drawer.run = null;
      setHTML(
        dRun,
        noVideoHTML(isRecorded(c) ? t("evidence.detail.never_ran") : t("evidence.detail.no_runs")),
      );
      return;
    }
    let full = drawer.runs.get(run.id);
    if (!full || isActiveRun(full.status) || full.status !== run.status) {
      try {
        full = unwrap<EvRun>(
          await api.get<unknown>(`/evidence/runs/${encodeURIComponent(run.id)}`),
          "run",
        );
        drawer.runs.set(run.id, full);
      } catch {
        full = run;
      }
    }
    if (drawer.detail !== c) return;
    drawer.run = full;
    renderRun(full);
  }

  function noVideoHTML(text: string): string {
    return emptyStateHTML({ size: "sm", dashed: true, body: text });
  }

  function renderHistory(runs: EvRun[], selected: string): void {
    setHTML($("ev-d-history"), historyHTML(runs, envNames(), selected));
  }

  function runSummaryHTML(r: EvRun): string {
    const parts: string[] = [];
    const env = envName(r.environment_id);
    if (env) parts.push(`<span>${esc(t("evidence.detail.on_env", { env }))}</span>`);
    const when = isActiveRun(r.status) ? "" : whenLabel(r.finished_at ?? r.started_at);
    if (when) parts.push(`<span>${esc(when)}</span>`);
    const dur = durationLabel(r.duration_ms);
    if (dur) parts.push(`<span>${esc(t("evidence.detail.took", { duration: dur }))}</span>`);
    if (r.trigger) parts.push(`<span>${esc(triggerLabel(r.trigger, r.trigger_ref ?? ""))}</span>`);
    if (r.checks?.length) {
      const s = checksSummary(r.checks);
      parts.push(
        `<span>${esc(t("evidence.detail.checks_passed", { passed: s.passed, total: s.total }))}</span>`,
      );
    }
    return `<div class="ev-run-summary">${statusPillHTML(r.status)}${parts.join(" ")}</div>`;
  }

  function runHTML(r: EvRun): string {
    const blocks: string[] = [runSummaryHTML(r)];
    if (isActiveRun(r.status)) {
      blocks.push(noVideoHTML(t("evidence.detail.in_progress")));
    } else if (r.video_id) {
      const poster = r.poster_id ? ` poster="${esc(artifactURL(r.poster_id))}"` : "";
      const track = r.chapters_id
        ? `<track kind="chapters" src="${esc(artifactURL(r.chapters_id))}" default />`
        : "";
      blocks.push(
        `<video class="ev-video" controls preload="metadata" playsinline${poster} src="${esc(artifactURL(r.video_id))}" aria-label="${esc(t("evidence.detail.video_aria", { key: drawer.key }))}">${track}</video>`,
      );
      if (r.chapters_id)
        blocks.push(`<div id="ev-d-chapters">${drawer.chapters.get(r.chapters_id) ?? ""}</div>`);
    } else if (r.poster_id) {
      blocks.push(`<img class="ev-poster" src="${esc(artifactURL(r.poster_id))}" alt="" />`);
    } else {
      blocks.push(noVideoHTML(t("evidence.detail.no_video")));
    }
    if (r.error) blocks.push(runnerErrorHTML(r.error, r.error_cause, "ev-run-error", "special"));
    if (r.failures?.length)
      blocks.push(
        `${sectionHeadHTML({ title: t("evidence.detail.failures"), level: 4 })}${failuresHTML(r.failures)}`,
      );
    if (r.checks)
      blocks.push(
        `${sectionHeadHTML({ title: t("evidence.detail.checks"), level: 4 })}${checksTableHTML(r.checks)}`,
      );
    const shots = r.shot_ids ?? [];
    if (shots.length)
      blocks.push(
        `${sectionHeadHTML({ title: t("evidence.detail.captures_count", { count: shots.length }), level: 4 })}${capturesHTML(shots, artifactURL)}`,
      );
    if (r.report_md?.trim())
      blocks.push(
        disclosureHTML({
          variant: "code",
          summary: t("evidence.detail.report"),
          bodyHTML: `<div class="ev-report-body">${mdHTML(md(r.report_md, { mediaBase: API }))}</div>`,
        }),
      );
    return `<div class="ev-d-section">${blocks.join("")}</div>`;
  }

  function renderRun(r: EvRun): void {
    // setHTML skips identical markup, so a refresh does not restart a playing video.
    setHTML(dRun, runHTML(r));
    if (r.chapters_id && !drawer.chapters.has(r.chapters_id)) void loadChapters(r.chapters_id);
  }

  async function loadChapters(id: string): Promise<void> {
    try {
      const vtt = await api.getText(`/evidence/artifacts/${encodeURIComponent(id)}`);
      const html = chaptersHTML(parseChapters(vtt));
      drawer.chapters.set(id, html);
      const box = document.getElementById("ev-d-chapters");
      const run = drawer.run;
      if (box && run?.chapters_id === id) {
        // Fill in place and update the signature so setHTML does not rebuild a playing video.
        box.innerHTML = html;
        dRun.dataset.sig = runHTML(run);
      }
    } catch {
      drawer.chapters.set(id, "");
    }
  }

  drawerEl.addEventListener("click", (e) => {
    const target = e.target as HTMLElement;
    const act = target.closest<HTMLElement>("[data-d-act]")?.dataset.dAct;
    const c = drawer.detail;
    if (act && c) {
      if (act === "record") openRecordModal(c);
      else if (act === "run") openRunModal({ kind: "case", c });
      else if (act === "edit") openCaseModal(c);
      else if (act === "delete") void deleteCase(c);
      return;
    }
    const envChip = target.closest<HTMLElement>("#ev-d-envs [data-env]");
    if (envChip) {
      drawer.envFilter = envChip.dataset.env ?? "";
      drawer.runId = "";
      renderDrawer();
      return;
    }
    const row = target.closest<HTMLElement>("[data-run]");
    if (row && c) {
      drawer.runId = row.dataset.run ?? "";
      writeURL(caseHash(c.key, drawer.runId));
      void selectRun();
      return;
    }
    const seek = target.closest<HTMLElement>("[data-act='seek']");
    if (seek) {
      const video = drawerEl.querySelector("video");
      if (video) {
        video.currentTime = Number(seek.dataset.at ?? 0);
        void video.play().catch(() => undefined);
      }
      return;
    }
    const shot = target.closest<HTMLElement>("[data-act='shot']");
    if (shot && drawer.run) openShot(drawer.run.shot_ids ?? [], Number(shot.dataset.index ?? 0));
  });

  $("ev-d-take-download").addEventListener("click", () => {
    const c = drawer.detail;
    if (!c?.take) return;
    downloadBlob(
      new Blob([JSON.stringify(c.take, null, 2)], { type: "application/json" }),
      `${c.key}.json`,
    );
  });

  function onHash(): void {
    const h = parseCaseHash(location.hash);
    if (h) openDrawer(h.key, h.runId);
    else closeDrawerFromHash();
  }
  window.addEventListener("hashchange", onHash, { signal });

  const shotModal = bindModal($("shot-modal"));
  const shotImg = $("shot-img") as HTMLImageElement;
  let shots: string[] = [];
  let shotAt = 0;

  function showShot(i: number): void {
    if (!shots.length) return;
    shotAt = (i + shots.length) % shots.length;
    const url = artifactURL(shots[shotAt]);
    shotImg.src = url;
    shotImg.alt = t("evidence.detail.capture_aria", { n: shotAt + 1, total: shots.length });
    ($("shot-open") as HTMLAnchorElement).href = url;
    setText(
      $("shot-count"),
      t("evidence.detail.capture_count", { n: shotAt + 1, total: shots.length }),
    );
    $("shot-prev").hidden = shots.length < 2;
    $("shot-next").hidden = shots.length < 2;
  }

  function openShot(ids: string[], i: number): void {
    shots = ids;
    showShot(i);
    shotModal.open($("shot-next"));
  }
  $("shot-prev").addEventListener("click", () => showShot(shotAt - 1));
  $("shot-next").addEventListener("click", () => showShot(shotAt + 1));

  const envModal = bindModal($("env-modal"));
  const ef = {
    form: $("env-form") as HTMLFormElement,
    name: $("ef-name") as HTMLInputElement,
    slug: $("ef-slug") as HTMLInputElement,
    project: $("ef-project") as HTMLSelectElement,
    url: $("ef-url") as HTMLInputElement,
    prod: $("ef-prod") as HTMLInputElement,
    hosts: $("ef-hosts") as HTMLTextAreaElement,
    logins: $("ef-logins"),
    preset: $("ef-preset") as HTMLSelectElement,
    time: $("ef-time") as HTMLInputElement,
    minute: $("ef-minute") as HTMLInputElement,
    cron: $("ef-cron") as HTMLInputElement,
    tz: $("ef-tz") as HTMLInputElement,
    deployOn: $("ef-deploy-on") as HTMLInputElement,
    schedProject: $("ef-sched-project") as HTMLSelectElement,
    schedTag: $("ef-sched-tag") as HTMLInputElement,
    save: $("ef-save") as HTMLButtonElement,
  };
  let editingEnv: EvEnvironment | null = null;
  let formKind: "project" | "standalone" | "sandbox" = "standalone";
  let slugTouched = false;
  let formCanEdit = true;
  let formCanSecrets = true;
  let logins: LoginRow[] = [];
  let loginSeq = 0;
  let tzFilled = false;

  function fillTimeZones(): void {
    if (tzFilled) return;
    tzFilled = true;
    const list = $("ef-tz-list");
    for (const z of timeZones()) {
      const o = document.createElement("option");
      o.value = z;
      list.appendChild(o);
    }
  }

  const fieldOf = (el: HTMLElement): HTMLElement => el.closest<HTMLElement>(".field") ?? el;

  function scheduleForm(): { preset: SchedulePreset; time: string; cron: string } {
    const preset = ef.preset.value as SchedulePreset;
    const minute = Math.min(59, Math.max(0, Number(ef.minute.value) || 0));
    const time =
      preset === "hourly" ? `00:${String(minute).padStart(2, "0")}` : readDateInput(ef.time);
    return { preset, time, cron: ef.cron.value };
  }

  let scopeSeq = 0;
  let scopeAsked = "";
  function syncScheduleScope(): void {
    const on = formKind === "standalone" && scheduleForm().preset !== "off";
    $("ef-sched-scope").hidden = !on;
    const line = $("ef-sched-count");
    const projectId = ef.schedProject.value;
    const tag = ef.schedTag.value.trim();
    const asked = on ? `${projectId}\n${tag}` : "";
    if (asked === scopeAsked) return;
    scopeAsked = asked;
    if (!on) {
      line.hidden = true;
      return;
    }
    const seq = ++scopeSeq;
    void api
      .get<CasesResponse>(casesURL({ ...EMPTY_CASE_FILTERS, projectId, tags: tag ? [tag] : [] }))
      .then((r) => {
        if (seq !== scopeSeq) return;
        const counts = r.counts ?? {};
        const recorded = totalOf(counts) - (counts.draft ?? 0);
        const projectName = projectId ? (state.projects.get(projectId) ?? projectId) : "";
        setText(line, scheduleScopeLine(recorded, projectName, tag));
        line.classList.toggle("ev-sched-empty", recorded === 0);
        line.hidden = false;
      })
      .catch(() => {
        if (seq === scopeSeq) line.hidden = true;
      });
  }
  ef.schedProject.addEventListener("change", syncScheduleScope);
  ef.schedTag.addEventListener("change", syncScheduleScope);

  function syncSchedule(): void {
    const f = scheduleForm();
    fieldOf(ef.time).hidden = f.preset !== "daily" && f.preset !== "weekdays";
    fieldOf(ef.minute).hidden = f.preset !== "hourly";
    fieldOf(ef.cron).hidden = f.preset !== "custom";
    fieldOf(ef.tz).hidden = f.preset === "off";
    const cron = cronFromSchedule(f);
    const badCron = f.preset === "custom" && !isValidCron(cron);
    setFieldError(ef.cron, badCron && cron ? t("evidence.env_form.cron_invalid") : "");
    const bad = badCron || !scheduleTimeOK(f);
    setText(
      $("ef-sched-preview"),
      f.preset === "off"
        ? t("evidence.env_form.preview_off")
        : bad
          ? ""
          : t("evidence.env_form.preview", { label: scheduleLabel(cron, ef.tz.value.trim()) }),
    );
    syncScheduleScope();
  }

  function loginRowHTML(row: LoginRow, readOnly: boolean): string {
    const uid = String(row.uid);
    const btn = (act: string, label: string, variant: "ghost" | "danger" = "ghost") =>
      readOnly ? "" : buttonHTML({ label, variant, size: "sm", attrs: { "data-l-act": act } });
    if (!row.existing)
      return `<div class="ev-login" data-uid="${uid}">
        <input class="mono" data-f="name" value="${esc(row.name)}" placeholder="${esc(t("evidence.env_form.login_name_placeholder"))}" aria-label="${esc(t("evidence.env_form.login_name"))}" autocomplete="off" spellcheck="false" />
        <input type="password" data-f="value" value="${esc(row.value)}" placeholder="${esc(t("evidence.env_form.login_value_placeholder"))}" aria-label="${esc(t("evidence.env_form.login_value"))}" autocomplete="new-password" />
        <span class="ev-login-btns">${btn("drop", t("evidence.env_form.login_drop"))}</span>
      </div>`;
    const name = `<span class="ev-login-name">${esc(row.name)}</span>`;
    if (row.op === "replace")
      return `<div class="ev-login" data-uid="${uid}">${name}
        <input type="password" data-f="value" value="${esc(row.value)}" placeholder="${esc(t("evidence.env_form.login_new_value"))}" aria-label="${esc(t("evidence.env_form.login_new_value_aria", { name: row.name }))}" autocomplete="new-password" />
        <span class="ev-login-btns">${btn("keep", t("common.buttons.cancel"))}</span>
      </div>`;
    if (row.op === "remove")
      return `<div class="ev-login ev-login-gone" data-uid="${uid}">${name}
        <span class="ev-login-set">${esc(t("evidence.env_form.login_removing"))}</span>
        <span class="ev-login-btns">${btn("keep", t("evidence.env_form.login_undo"))}</span>
      </div>`;
    return `<div class="ev-login" data-uid="${uid}">${name}
      <span class="ev-login-set">${esc(t("evidence.env_form.login_set"))}</span>
      <span class="ev-login-btns">${btn("replace", t("evidence.env_form.login_replace"))}${btn("remove", t("evidence.env_form.login_remove"), "danger")}</span>
    </div>`;
  }

  function renderLogins(): void {
    ef.logins.innerHTML = logins.map((r) => loginRowHTML(r, !formCanSecrets)).join("");
  }

  ef.logins.addEventListener("input", (e) => {
    const input = e.target as HTMLInputElement;
    const row = logins.find(
      (r) => String(r.uid) === input.closest<HTMLElement>("[data-uid]")?.dataset.uid,
    );
    if (!row) return;
    if (input.dataset.f === "name") row.name = input.value;
    if (input.dataset.f === "value") row.value = input.value;
  });
  ef.logins.addEventListener("focusout", (e) => {
    const input = e.target as HTMLInputElement;
    if (input.dataset.f !== "name") return;
    const row = logins.find(
      (r) => String(r.uid) === input.closest<HTMLElement>("[data-uid]")?.dataset.uid,
    );
    if (!row) return;
    row.name = normalizeSecretName(input.value);
    input.value = row.name;
  });
  ef.logins.addEventListener("click", (e) => {
    const btn = (e.target as HTMLElement).closest<HTMLButtonElement>("[data-l-act]");
    const uid = btn?.closest<HTMLElement>("[data-uid]")?.dataset.uid;
    const row = logins.find((r) => String(r.uid) === uid);
    if (!btn || !row) return;
    const act = btn.dataset.lAct;
    if (act === "drop") logins = logins.filter((r) => r !== row);
    else if (act === "replace" || act === "remove" || act === "keep") {
      row.op = act;
      row.value = "";
    }
    renderLogins();
    if (act === "replace")
      ef.logins.querySelector<HTMLInputElement>(`[data-uid="${row.uid}"] input`)?.focus();
  });
  $("ef-login-add").addEventListener("click", () => {
    logins.push({ uid: ++loginSeq, name: "", existing: false, op: "keep", value: "" });
    renderLogins();
    ef.logins.querySelector<HTMLInputElement>(`[data-uid="${loginSeq}"] input`)?.focus();
  });

  const kindRadios = (): HTMLInputElement[] =>
    Array.from(document.querySelectorAll<HTMLInputElement>('input[name="ef-kind"]'));

  function syncKind(): void {
    const kind = formKind;
    const creating = !editingEnv;
    $("ef-project-row").hidden = kind !== "project" || !creating || projectList().length < 2;
    $("ef-sandbox-lead").hidden = kind !== "sandbox";
    $("ef-name-row").hidden = kind === "sandbox";
    fieldOf(ef.slug).hidden = kind !== "project";
    $("ef-url-row").hidden = kind === "sandbox";
    $("ef-schedule-section").hidden = kind === "sandbox";
    $("ef-deploy-section").hidden = kind !== "project";
    $("ef-hook-section").hidden = kind === "sandbox";
    syncScheduleScope();
    $("ef-delete-note").hidden = !(editingEnv && kind === "project");
    if (editingEnv?.project_id)
      ($("ef-delete-link") as HTMLAnchorElement).href =
        `/project/?id=${encodeURIComponent(editingEnv.project_id)}#environments`;
    syncProd();
    syncDeploy();
  }

  function syncProd(): void {
    const env = editingEnv;
    let p: Permission;
    let hint = t("evidence.env_form.production_hint");
    const prodHint = $("ef-prod-hint");
    prodHint.hidden = false;
    if (env?.is_production && !formCanEdit) {
      ef.prod.disabled = true;
      prodHint.hidden = true;
      return;
    }
    if (!env) p = wsCan("create_production_environment");
    else if (envKind(env) === "project") {
      ef.prod.disabled = true;
      const href = `/project/?id=${encodeURIComponent(env.project_id ?? "")}#environments`;
      setHTML(
        $("ef-prod-hint"),
        state.canAdmin
          ? `${esc(t("evidence.env_form.production_on_project"))} <a class="ev-link" href="${esc(href)}">${esc(t("evidence.envs.open_project"))}</a>`
          : esc(t("evidence.env_form.production_on_project_ask")),
      );
      return;
    } else p = { allowed: state.canAdmin, reason: "production" };
    ef.prod.disabled = !formCanEdit || !p.allowed;
    if (!p.allowed && p.reason) hint = reasonLabel(p.reason);
    setText($("ef-prod-hint"), hint);
  }

  function syncDeploy(): void {
    const d = editingEnv?.deploy;
    const info = $("ef-deploy-on-hint");
    if (!editingEnv) {
      ef.deployOn.disabled = !formCanEdit;
      setText(info, t("evidence.env_form.deploy_new"));
      return;
    }
    if (d && !d.available) {
      ef.deployOn.disabled = true;
      ef.deployOn.checked = false;
      setText(info, reasonLabel(d.reason));
      return;
    }
    ef.deployOn.disabled = !formCanEdit;
    setText(
      info,
      d?.repo
        ? t("evidence.env_form.deploy_info", { repo: d.repo, environment: d.environment })
        : t("evidence.env_form.deploy_new"),
    );
  }

  function openEnvModal(env: EvEnvironment | null, projectId = ""): void {
    editingEnv = env;
    const projects = projectList();
    formKind = env ? envKind(env) : projects.length ? "project" : "standalone";
    const editPerm = env ? permission(env, "edit") : wsCan("create_environment");
    formCanEdit = editPerm.allowed;
    formCanSecrets = env ? permission(env, "secrets").allowed : formCanEdit;
    fillTimeZones();
    const label = env ? envLabel(env) : "";
    setText(
      $("env-modal-title"),
      !env
        ? t("evidence.env_form.new_title")
        : envKind(env) === "sandbox"
          ? t("evidence.env_form.sandbox_title", { project: env.project_name ?? "" })
          : formCanEdit
            ? t("evidence.env_form.edit_title", { name: label })
            : label,
    );
    const limit = $("ef-limit");
    limit.hidden = formCanEdit;
    setText(limit, formCanEdit ? "" : reasonLabel(editPerm.reason));
    $("ef-kind-row").hidden = Boolean(env) || projects.length === 0;
    for (const r of kindRadios()) r.checked = r.value === formKind;
    ef.project.replaceChildren(...projects.map((p) => new Option(p.name, p.id)));
    ef.project.value =
      projectId || state.project || (env?.project_id ?? "") || (projects.at(0)?.id ?? "");
    ef.name.value = env ? (envKind(env) === "project" ? label : env.name) : "";
    ef.slug.value = env?.name ?? "";
    slugTouched = Boolean(env);
    ef.url.value = env?.base_url ?? "";
    ef.prod.checked = Boolean(env?.is_production);
    ef.hosts.value = (env?.allowed_hosts ?? []).join("\n");
    logins = (env?.secret_names ?? []).map((name) => ({
      uid: ++loginSeq,
      name,
      existing: true,
      op: "keep" as const,
      value: "",
    }));
    renderLogins();
    const sched = scheduleFromCron(env?.schedule ?? "");
    ef.preset.value = sched.preset;
    writeDateInput(ef.time, sched.preset === "hourly" ? "09:00" : sched.time);
    ef.minute.value = String(Number(sched.time.slice(3)) || 0);
    ef.cron.value = sched.cron;
    ef.tz.value = (env?.schedule_tz ?? "") || browserTimeZone();
    ef.deployOn.checked = Boolean(env?.on_deploy);
    ef.schedProject.replaceChildren(
      new Option(t("evidence.env_form.schedule_project_all"), ""),
      ...projects.map((p) => new Option(p.name, p.id)),
    );
    ef.schedProject.value = env?.schedule_project_id ?? "";
    ef.schedTag.value = env?.schedule_tag ?? "";
    scopeAsked = "-";
    $("ef-tag-list").replaceChildren(...state.tagOptions.map((tag) => new Option(tag)));
    $("ef-hook-new").hidden = Boolean(env);
    setText(
      $("ef-hook-cmd"),
      ciHookCurl(API || location.origin, env ? env.id : t("evidence.env_form.hook_id_placeholder")),
    );
    clearFormErrors(ef.form);
    ef.form
      .querySelectorAll<
        HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement
      >("input, textarea, select")
      .forEach((x) => {
        x.disabled = !formCanEdit;
      });
    for (const r of kindRadios()) r.disabled = false;
    // A project environment's name is its id in prompts and links: it cannot change after creation.
    ef.slug.disabled = !formCanEdit || Boolean(env);
    setOff($("ef-login-add") as HTMLButtonElement, {
      allowed: formCanSecrets,
      reason: env ? permission(env, "secrets").reason : editPerm.reason,
    });
    setOff(ef.save, editPerm);
    setText($("ef-cancel"), formCanEdit ? t("common.buttons.cancel") : t("common.buttons.close"));
    syncKind();
    syncSchedule();
    envModal.open(!formCanEdit ? $("ef-cancel") : formKind === "sandbox" ? ef.hosts : ef.name);
  }

  for (const r of kindRadios())
    r.addEventListener("change", () => {
      if (!r.checked) return;
      formKind = r.value === "project" ? "project" : "standalone";
      syncKind();
    });
  ef.name.addEventListener("input", () => {
    if (!slugTouched && !editingEnv) ef.slug.value = slugify(ef.name.value);
  });
  ef.slug.addEventListener("input", () => {
    slugTouched = ef.slug.value.trim() !== "";
  });
  ef.slug.addEventListener("blur", () => {
    ef.slug.value = slugify(ef.slug.value);
  });

  ef.preset.addEventListener("change", () => {
    if (ef.preset.value === "custom" && !ef.cron.value.trim()) {
      const f = scheduleForm();
      ef.cron.value = cronFromSchedule({ ...f, preset: "daily" });
    }
    syncSchedule();
  });
  for (const x of [ef.time, ef.minute, ef.cron, ef.tz]) x.addEventListener("input", syncSchedule);
  ef.form.addEventListener("submit", (e) => {
    e.preventDefault();
    void saveEnv();
  });

  function fieldError(control: HTMLElement, msg: string, focus: boolean): boolean {
    setFieldError(control, msg, { focus });
    return Boolean(msg);
  }

  async function saveEnv(): Promise<void> {
    if (!formCanEdit) return;
    const kind = formKind;
    const was = editingEnv;
    const name = ef.name.value.trim();
    const slug = slugify(ef.slug.value);
    const url = ef.url.value.trim();
    const sched = scheduleForm();
    const cron = cronFromSchedule(sched);
    let bad = false;
    if (kind !== "sandbox") {
      bad = fieldError(ef.name, name ? "" : t("evidence.env_form.name_required"), true);
      if (kind === "project" && !was)
        bad =
          fieldError(
            ef.slug,
            ENV_SLUG.test(slug) ? "" : t("evidence.env_form.slug_invalid"),
            !bad,
          ) || bad;
      const urlOK = isHttpURL(url) || (kind === "project" && url === "");
      bad = fieldError(ef.url, urlOK ? "" : t("evidence.env_form.url_invalid"), !bad) || bad;
      if (sched.preset === "custom" && !isValidCron(cron))
        bad = fieldError(ef.cron, t("evidence.env_form.cron_invalid"), !bad) || bad;
      // cronFromSchedule() falls back to 09:00 for an invalid time; never save that.
      if (!scheduleTimeOK(sched))
        bad = fieldError(ef.time, dateInvalidMessage("time"), !bad) || bad;
    }
    const secrets: Record<string, string | null> = {};
    for (const row of logins) {
      if (row.existing && row.op === "remove") secrets[row.name] = null;
      else if (row.existing && row.op === "replace" && row.value) secrets[row.name] = row.value;
      else if (!row.existing) {
        const n = normalizeSecretName(row.name);
        if (!n && !row.value) continue;
        if (!SECRET_NAME.test(n) || !row.value) {
          showEnvError(t("evidence.env_form.login_invalid"));
          return;
        }
        secrets[n] = row.value;
      }
    }
    if (bad) return;
    const hosts = parseHosts(ef.hosts.value);
    const tz = ef.tz.value.trim() || "UTC";
    let body: Record<string, unknown>;
    if (kind === "sandbox") body = { allowed_hosts: hosts };
    else if (kind === "project")
      body = was
        ? {
            display_name: name,
            base_url: url,
            allowed_hosts: hosts,
            schedule: cron,
            schedule_tz: tz,
            on_deploy: ef.deployOn.checked && !ef.deployOn.disabled,
          }
        : {
            project_id: ef.project.value,
            name: slug,
            display_name: name,
            base_url: url,
            is_production: ef.prod.checked,
            allowed_hosts: hosts,
            schedule: cron,
            schedule_tz: tz,
            on_deploy: ef.deployOn.checked,
          };
    else {
      body = {
        name,
        base_url: url,
        allowed_hosts: hosts,
        schedule: cron,
        schedule_tz: tz,
        schedule_project_id: cron ? ef.schedProject.value : "",
        schedule_tag: cron ? ef.schedTag.value.trim() : "",
      };
      if (!was ? ef.prod.checked : ef.prod.checked !== Boolean(was.is_production))
        body.is_production = ef.prod.checked;
    }
    const shown = kind === "sandbox" ? t("evidence.sandbox.name") : name;
    const done = busy(ef.save, t("evidence.saving"));
    setFormError($("ef-error"), "");
    try {
      const r = was
        ? await api.patch<unknown>(`/evidence/environments/${encodeURIComponent(was.id)}`, body)
        : await api.post<unknown>("/evidence/environments", body);
      const saved = unwrap<EvEnvironment>(r, "environment");
      const id = saved.id || (was?.id ?? "");
      if (Object.keys(secrets).length && id && formCanSecrets) {
        try {
          await api.put(`/evidence/environments/${encodeURIComponent(id)}/secrets`, secrets);
        } catch (e) {
          editingEnv = { ...(was ?? saved), ...saved, id };
          showEnvError(t("evidence.errors.save_logins", { error: errMsg(e, "") }));
          void loadEnvs();
          return;
        }
      }
      envModal.close();
      toast(
        was
          ? t("evidence.env_form.saved", { name: shown })
          : t("evidence.env_form.created", { name: shown }),
        "ok",
      );
      await loadEnvs();
      if (!was) void loadContext();
    } catch (e) {
      showEnvError(errMsg(e, t("evidence.errors.save_env")));
    } finally {
      done();
    }
  }

  function showEnvError(msg: string): void {
    const err = $("ef-error");
    setFormError(err, msg);
    err.scrollIntoView({ block: "nearest" });
  }

  const linkModal = bindModal($("link-modal"));
  const linkTarget = $("link-target") as HTMLSelectElement;
  const linkSave = $("link-save") as HTMLButtonElement;
  let linking: EvEnvironment | null = null;

  function openLinkModal(env: EvEnvironment): void {
    linking = env;
    setText($("link-lead"), t("evidence.link.lead", { name: envLabel(env) }));
    linkTarget.replaceChildren();
    // The daemon refuses to link to a target that already has runs.
    for (const g of groupEnvs(state.envs.filter((e) => envKind(e) === "project"))) {
      const og = document.createElement("optgroup");
      og.label = g.projectName;
      for (const e of g.envs) {
        const ran = Boolean(e.last_batch);
        const o = new Option(
          ran ? t("evidence.link.option_ran", { env: envLabel(e) }) : envLabel(e),
          e.project_environment_id ?? "",
        );
        o.disabled = ran;
        og.appendChild(o);
      }
      if (og.children.length) linkTarget.appendChild(og);
    }
    const first = Array.from(linkTarget.options).find((o) => !o.disabled);
    linkTarget.value = first?.value ?? "";
    const none = !first;
    $("link-none").hidden = !none;
    fieldOf(linkTarget).hidden = none;
    linkSave.disabled = none;
    setFormError($("link-error"), "");
    linkModal.open(none ? linkSave : linkTarget);
  }

  linkSave.addEventListener("click", () => void saveLink());

  async function saveLink(): Promise<void> {
    const env = linking;
    if (!env || !linkTarget.value) return;
    const done = busy(linkSave, t("evidence.saving"));
    setFormError($("link-error"), "");
    try {
      await api.post(`/evidence/environments/${encodeURIComponent(env.id)}/link`, {
        project_environment_id: linkTarget.value,
      });
      linkModal.close();
      toast(t("evidence.link.done", { name: envLabel(env) }), "ok");
      await loadEnvs();
    } catch (e) {
      setFormError($("link-error"), errMsg(e, t("evidence.errors.save_env")));
    } finally {
      done();
    }
  }

  const tasks = (() => {
    let started = false;
    let filters: QAFilters = filtersFromQuery(state.tasksQuery);
    let stageLabels = new Map<string, string>();
    let loaded = false;
    let last: QAResponse | null = null;
    let poller: { poke: () => void } | null = null;

    const list = $("t-list");
    const note = $("t-note");
    const boardSel = $("t-board") as HTMLSelectElement;
    const projectSel = $("t-project") as HTMLSelectElement;
    const searchInput = $("t-q") as HTMLInputElement;
    const uncoveredBox = $("t-uncovered") as HTMLInputElement;
    const clearBtn = $("t-clear") as HTMLButtonElement;
    const statusChips = $("t-status-chips");
    const chipEls = statusChips.querySelectorAll<HTMLButtonElement>(".chip[data-status]");
    const hasFilters = (f: QAFilters) => filtersToQuery(f) !== "";
    const emptyRow = (text: string) => emptyRowHTML(text, { cols: QA_COLUMNS });

    function syncControls(): void {
      boardSel.value = filters.boardId;
      projectSel.value = filters.projectId;
      if (document.activeElement !== searchInput) searchInput.value = filters.q;
      uncoveredBox.checked = filters.uncovered;
      setChipsPressed(statusChips, filters.status, "status");
      clearBtn.hidden = !hasFilters(filters);
      state.tasksQuery = filtersToQuery(filters);
      if (state.tab === "tasks") writeURL();
    }

    function render(resp: QAResponse): void {
      last = resp;
      const rowsData = resp.test_cases;
      const summary = countsSummary(resp.counts, resp.covered_counts);
      chipEls.forEach((c) => {
        const s = c.dataset.status as TestCaseStatus;
        const count = c.querySelector<HTMLElement>(".chip-count");
        if (count) setCount(count, summary.byStatus[s], { cls: "chip-count" });
      });
      const total = matchedTotal(summary, filters.status);
      const bar = caseProgressHTML(summary, { size: "md" });
      setHTML($("t-progress"), bar);
      note.classList.remove("qa-note-err");
      setText(
        note,
        rowsData.length < total
          ? t("evidence.tasks.showing", {
              shown: rowsData.length,
              total,
              covered: matchedCovered(resp.covered_counts ?? {}, filters.status),
            })
          : t("evidence.tasks.covered_note", {
              covered: matchedCovered(resp.covered_counts ?? {}, filters.status),
              total,
            }),
      );
      if (rowsData.length === 0) {
        setHTML(
          list,
          emptyRow(
            hasFilters(filters) ? t("evidence.tasks.no_matches") : t("evidence.tasks.none_yet"),
          ),
        );
        return;
      }
      setHTML(
        list,
        tableBodyHTML(
          groupByTask(rowsData),
          stageLabels,
          {
            keys: libraryIndex(state.cases),
            canAdd: state.canWrite,
          },
          state.viewer,
        ),
      );
    }

    function showError(err: unknown): void {
      const msg = errMsg(err, t("evidence.tasks.load_failed"));
      if (!loaded) {
        setHTML(list, emptyRow(msg));
        return;
      }
      note.classList.add("qa-note-err");
      setText(note, t("evidence.errors.refresh", { error: msg }));
    }

    async function refresh(): Promise<void> {
      try {
        // The daemon omits empty lists from the response.
        const r = await api.get<Partial<QAResponse>>(testCasesURL(filters));
        render({ ...r, test_cases: r.test_cases ?? [], counts: r.counts ?? {} });
        loaded = true;
      } catch (err) {
        showError(err);
        throw err; // rethrow so the poller backs off
      }
    }

    function setFilters(next: QAFilters): void {
      filters = next;
      syncControls();
      poller?.poke();
    }

    async function loadPickers(): Promise<void> {
      const [boards, projects, stages] = await Promise.all([
        api.get<BoardsResponse>("/boards").catch(() => null),
        api.get<Project[]>("/projects").catch(() => [] as Project[]),
        api.get<Stage[]>("/stages").catch(() => [] as Stage[]),
      ]);
      for (const b of boards?.boards ?? []) boardSel.add(new Option(b.label || b.id, b.id));
      for (const p of listOf<Project>(projects, "projects"))
        projectSel.add(new Option(p.name || p.id, p.id));
      stageLabels = new Map(stages.map((s) => [s.id, s.label || s.id]));
      syncControls();
      if (loaded) poller?.poke();
    }

    async function addToLibrary(btn: HTMLButtonElement): Promise<void> {
      const taskId = btn.dataset.task ?? "";
      const key = btn.dataset.key ?? "";
      const done = busy(btn, t("evidence.tasks.adding"));
      try {
        const r = await api.post<unknown>("/evidence/cases/from-task", {
          task_id: taskId,
          case: key,
        });
        const c = unwrap<EvCase>(r, "case");
        toast(
          isRecorded(c)
            ? t("evidence.tasks.added_recorded", { key: c.key })
            : t("evidence.tasks.added", { key: c.key }),
          "ok",
        );
        state.cases = [...state.cases.filter((x) => x.id !== c.id), c];
        redraw();
        casesPoller.poke();
      } catch (e) {
        done();
        toast(errMsg(e, t("evidence.errors.add_to_library")), "err");
      }
    }

    function redraw(): void {
      if (last) render(last);
    }

    function start(): void {
      syncControls();
      if (started) return;
      started = true;
      chipEls.forEach((c) =>
        c.addEventListener("click", () =>
          setFilters(toggleStatus(filters, c.dataset.status as TestCaseStatus)),
        ),
      );
      boardSel.addEventListener("change", () =>
        setFilters({ ...filters, boardId: boardSel.value }),
      );
      projectSel.addEventListener("change", () =>
        setFilters({ ...filters, projectId: projectSel.value }),
      );
      uncoveredBox.addEventListener("change", () =>
        setFilters({ ...filters, uncovered: uncoveredBox.checked }),
      );
      clearBtn.addEventListener("click", () => setFilters(EMPTY_FILTERS));
      let debounce = 0;
      searchInput.addEventListener("input", () => {
        window.clearTimeout(debounce);
        debounce = window.setTimeout(() => setFilters({ ...filters, q: searchInput.value }), 250);
      });
      list.addEventListener("click", (e) => {
        const btn = (e.target as HTMLElement).closest<HTMLButtonElement>("[data-act='to-library']");
        if (btn) void addToLibrary(btn);
      });
      // Every test case write notifies the `tasks` topic; the poller only covers dropped frames.
      const p = startPoller({ interval: 30000, fetch: refresh });
      poller = p;
      const offTasks = events.subscribe(TASKS_TOPIC, () => p.poke(), { replay: false });
      const offReconnect = events.onReconnect(() => p.poke());
      signal.addEventListener("abort", () => {
        offTasks();
        offReconnect();
      });
      void loadPickers();
    }

    function clearSearch(): boolean {
      if (!searchInput.value) return false;
      searchInput.value = "";
      setFilters({ ...filters, q: "" });
      searchInput.blur();
      return true;
    }

    return { start, redraw, clearSearch, focusSearch: () => searchInput.focus() };
  })();

  const closeIfOpen = (m: { isOpen: () => boolean; close: () => void }) => (): boolean => {
    if (!m.isOpen()) return false;
    m.close();
    return true;
  };

  initKeyboard({
    rawKey: (e) => {
      if (!shotModal.isOpen() || shots.length < 2) return false;
      if (e.key === "ArrowLeft") showShot(shotAt - 1);
      else if (e.key === "ArrowRight") showShot(shotAt + 1);
      else return false;
      e.preventDefault();
      return true;
    },
    onEscape: [
      closeIfOpen(shotModal),
      closeIfOpen(linkModal),
      closeIfOpen(envModal),
      closeIfOpen(runModal),
      closeIfOpen(recModal),
      closeIfOpen(caseModal),
      closeIfOpen(drawerModal),
      () => {
        if (state.tab === "tasks") return tasks.clearSearch();
        if (state.tab !== "cases" || !fQ.value) return false;
        fQ.value = "";
        setCaseFilters({ ...state.caseFilters, q: "" });
        fQ.blur();
        return true;
      },
    ],
    onSearch: () => {
      if (state.tab === "tasks") tasks.focusSearch();
      else {
        if (state.tab !== "cases") showTab("cases");
        fQ.focus();
        fQ.select();
      }
    },
    onNew: () => {
      if (state.canWrite) openCaseModal(null);
    },
  });

  // Cases wait for the context: it settles the project they are filtered by.
  const casesPoller = startPoller({
    interval: 30000,
    fetch: loadCases,
    skipWhen: () => !state.ctxSettled,
  });
  const batchesPoller = startPoller({
    interval: 5000,
    fetch: loadBatches,
    skipWhen: () => !anyActive() && Date.now() - state.lastBatchFetch < 30000,
  });
  applyRole();
  showTab(state.tab);
  void loadProjects().then(loadContext);
  void loadEnvs();
  onHash();
}

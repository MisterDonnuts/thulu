import { t } from "../i18n";
import { api } from "./api";
import { roleWithin } from "./api-keys";
import { sessionInfo } from "./auth";
import {
  applyStarter,
  CHECKED_FIELDS,
  checkFields,
  editorHTML,
  fileRowEl,
  filePaths,
  formToInput,
  historyRows,
  paramRowEl,
  prewarmHTML,
  revisionHTML,
  starterFor,
  starterHTML,
  type InitForm,
  type InitRevision,
  type InitScript,
  type PrewarmStatus,
} from "./init-script";
import { confirmDiscard } from "./mdeditor";
import {
  confirmDialog,
  emptyStateHTML,
  fillHistoryList,
  historyListError,
  openHistoryView,
  setFieldError,
  setFieldWarning,
} from "./ui";

export interface InitPanelOpts {
  host: HTMLElement;
  saveBtn: HTMLElement;
  historyBtn: HTMLElement;
  projectID: string;
  toast: (msg: string, kind?: "ok" | "err" | "info", ms?: number) => void;
  codeReady?: boolean;
}

export interface InitPanel {
  codeArrived(): void;
}

function field<T extends HTMLElement>(root: HTMLElement, sel: string): T | null {
  return root.querySelector<T>(sel);
}

function readForm(root: HTMLElement): InitForm {
  const val = (sel: string) => field<HTMLInputElement>(root, sel)?.value ?? "";
  return {
    script: field<HTMLTextAreaElement>(root, "#is-script")?.value ?? "",
    params: [...root.querySelectorAll<HTMLElement>("[data-param-row]")].map((row) => ({
      name: field<HTMLInputElement>(row, '[data-p="name"]')?.value ?? "",
      default: field<HTMLInputElement>(row, '[data-p="default"]')?.value ?? "",
      description: field<HTMLInputElement>(row, '[data-p="description"]')?.value ?? "",
      required: field<HTMLInputElement>(row, '[data-p="required"]')?.checked ?? false,
    })),
    ports: val("#is-ports"),
    ready: val("#is-ready"),
    timeout: val("#is-timeout"),
    workdir: val("#is-workdir"),
    setup: field<HTMLTextAreaElement>(root, "#is-setup")?.value ?? "",
    tools: val("#is-tools"),
    cacheKey: val("#is-cache-key"),
    cachePaths: val("#is-cache-paths"),
    files: [...root.querySelectorAll<HTMLElement>("[data-file-row]")].map((row) => ({
      path: field<HTMLInputElement>(row, '[data-f="path"]')?.value ?? "",
      content: field<HTMLTextAreaElement>(row, '[data-f="content"]')?.value ?? "",
    })),
    note: val("#is-note"),
  };
}

function showFieldChecks(root: HTMLElement, rootFiles: string[] | null): void {
  const { errors, warnings } = checkFields(readForm(root), rootFiles);
  for (const id of CHECKED_FIELDS) {
    const input = field(root, `#${id}`);
    if (!input) continue;
    setFieldError(input, errors[id] ?? "");
    setFieldWarning(input, errors[id] ? "" : (warnings[id] ?? ""));
  }
}

const PREWARM_POLL_MS = 5000;

export function mountInitScript(o: InitPanelOpts): InitPanel {
  const base = `/projects/${encodeURIComponent(o.projectID)}/init-script`;
  let current: InitScript | null = null;
  let view: "editor" | "history" = "editor";
  let dirty = false;
  let nextRow = 0;
  let nextFile = 0;
  // UI-only gate; the server enforces Admin on writes. roleWithin allows editing until /auth/me
  // answers, and always on no-login installs.
  let readOnly = false;
  let prewarmTimer: ReturnType<typeof setTimeout> | undefined;
  let rootFiles: string[] | null = null;
  const loadRootFiles = async () => {
    try {
      rootFiles = (await api.get<{ files: string[] }>(`${base}/repo-files`)).files;
    } catch {
      rootFiles = null;
    }
  };

  const refreshPrewarm = async () => {
    clearTimeout(prewarmTimer);
    const box = field(o.host, "[data-prewarm]");
    if (!box) return;
    let st: PrewarmStatus;
    try {
      st = await api.get<PrewarmStatus>(`${base}/prewarm`);
    } catch {
      box.innerHTML = "";
      return;
    }
    if (!box.isConnected) return;
    box.innerHTML = prewarmHTML(st, !readOnly);
    field<HTMLButtonElement>(box, "[data-prewarm-run]")?.addEventListener(
      "click",
      () => void runPrewarm(),
    );
    if (st.running) prewarmTimer = setTimeout(() => void refreshPrewarm(), PREWARM_POLL_MS);
  };

  const runPrewarm = async () => {
    if (dirty) {
      o.toast(t("lib.init_script.prewarm.save_first"), "info", 3500);
      return;
    }
    try {
      await api.post(`${base}/prewarm`, {});
    } catch (e) {
      o.toast((e as Error).message, "err", 4500);
      return;
    }
    o.toast(t("lib.init_script.prewarm.started"), "ok", 2500);
    // The prewarm row appears only after it fetches the default branch.
    setTimeout(() => void refreshPrewarm(), 1500);
  };

  const showEditor = () => {
    view = "editor";
    dirty = false;
    o.historyBtn.textContent = t("lib.init_script.buttons.history");
    o.saveBtn.hidden = readOnly;
    if (!current) return;
    o.host.innerHTML = editorHTML(current, readOnly);
    void refreshPrewarm();
    if (readOnly) return;
    nextRow = current.manifest.params.length;
    o.host.addEventListener("input", () => (dirty = true), { once: true });
    showFieldChecks(o.host, rootFiles);
    offerStarter();
    field(o.host, "[data-param-add]")?.addEventListener("click", () => {
      const list = field(o.host, "[data-params]");
      if (!list) return;
      field(list, "[data-params-empty]")?.remove();
      list.append(paramRowEl({ name: "" }, nextRow++, false));
      dirty = true;
      list.querySelector<HTMLInputElement>('[data-param-row]:last-child [data-p="name"]')?.focus();
    });
    field(o.host, "[data-params]")?.addEventListener("click", (e) => {
      const del = (e.target as HTMLElement).closest<HTMLElement>("[data-param-del]");
      if (!del) return;
      del.closest("[data-param-row]")?.remove();
      dirty = true;
    });
    nextFile = filePaths(current.manifest.files).length;
    field(o.host, "[data-file-add]")?.addEventListener("click", () => {
      const list = field(o.host, "[data-files]");
      if (!list) return;
      field(list, "[data-files-empty]")?.remove();
      list.append(fileRowEl({ path: "", content: "" }, nextFile++, false));
      dirty = true;
      list.querySelector<HTMLInputElement>('[data-file-row]:last-child [data-f="path"]')?.focus();
    });
    field(o.host, "[data-files]")?.addEventListener("click", (e) => {
      const del = (e.target as HTMLElement).closest<HTMLElement>("[data-file-del]");
      if (!del) return;
      del.closest("[data-file-row]")?.remove();
      dirty = true;
    });
  };

  let starterUsed = false;
  const offerStarter = () => {
    const slot = field(o.host, "[data-starter-slot]");
    if (!slot || !current || current.revision || !rootFiles || starterUsed) return;
    const st = starterFor(rootFiles);
    if (!st) return;
    slot.innerHTML = starterHTML(st);
    field(slot, "[data-starter-use]")?.addEventListener("click", () => {
      if (!current) return;
      const saved = current;
      starterUsed = true;
      current = applyStarter(saved, st);
      showEditor();
      current = saved;
      dirty = true;
    });
  };

  // Registered once: the host survives editor redraws, so adding it in showEditor would stack
  // listeners.
  o.host.addEventListener("input", () => {
    if (view === "editor" && !readOnly) showFieldChecks(o.host, rootFiles);
  });

  const readRootFiles = () =>
    void loadRootFiles().then(() => {
      if (view === "editor" && !readOnly && current) {
        showFieldChecks(o.host, rootFiles);
        offerStarter();
      }
    });

  const load = async () => {
    if (o.codeReady !== false) readRootFiles();
    try {
      const [sc, me] = await Promise.all([api.get<InitScript>(base), sessionInfo()]);
      current = sc;
      readOnly = !roleWithin("admin", me?.role);
    } catch (e) {
      o.host.innerHTML = emptyStateHTML({ size: "sm", tone: "danger", body: (e as Error).message });
      return;
    }
    showEditor();
  };

  const save = async () => {
    if (view !== "editor" || readOnly) return;
    const { input, error } = formToInput(readForm(o.host));
    if (!input) {
      o.toast(error ?? "", "err", 4500);
      return;
    }
    try {
      current = await api.put<InitScript>(base, input);
    } catch (e) {
      o.toast((e as Error).message, "err", 4500);
      return;
    }
    o.toast(t("lib.init_script.saved", { revision: current.revision }), "ok", 2200);
    showEditor();
  };

  const showRevision = async (rev: InitRevision, pane: HTMLElement) => {
    let sc: InitScript;
    try {
      sc = await api.get<InitScript>(`${base}/revisions/${rev.revision}`);
    } catch (e) {
      pane.innerHTML = emptyStateHTML({ size: "sm", tone: "danger", body: (e as Error).message });
      return;
    }
    pane.innerHTML = revisionHTML(sc, current?.revision ?? 0, !readOnly);
    field(pane, "[data-rev-restore]")?.addEventListener("click", () => {
      void (async () => {
        const ok = await confirmDialog({
          title: t("lib.init_script.history.confirm_title", { revision: sc.revision }),
          message: t("lib.init_script.history.confirm_message"),
          confirmLabel: t("lib.init_script.history.restore"),
        });
        if (!ok) return;
        try {
          current = await api.post<InitScript>(`${base}/revisions/${sc.revision}/restore`, {});
        } catch (e) {
          o.toast((e as Error).message, "err", 4500);
          return;
        }
        o.toast(
          t("lib.init_script.history.restored", { revision: sc.revision, now: current.revision }),
          "ok",
          2200,
        );
        showEditor();
      })();
    });
  };

  const showHistory = async () => {
    if (dirty && !(await confirmDiscard(t("lib.init_script.discard")))) return;
    view = "history";
    o.historyBtn.textContent = t("lib.init_script.buttons.back");
    o.saveBtn.hidden = true;
    const { list, preview } = openHistoryView(o.host, {
      title: t("lib.init_script.history.title"),
      pick: t("lib.init_script.history.pick"),
      bar: false,
      boxed: true,
    });
    let revs: InitRevision[];
    try {
      revs = await api.get<InitRevision[]>(`${base}/revisions?limit=100`);
    } catch (e) {
      historyListError(list, (e as Error).message);
      return;
    }
    fillHistoryList(list, historyRows(revs), {
      none: t("lib.init_script.history.none"),
      onPick: (i) => void showRevision(revs[i], preview),
    });
  };

  o.saveBtn.addEventListener("click", () => void save());
  o.historyBtn.addEventListener("click", () => {
    if (view === "history") showEditor();
    else void showHistory();
  });
  void load();
  return { codeArrived: readRootFiles };
}

import { api, ApiError } from "./api";
import { activeRunners, loadRunnerRepos, loadRunners, runnerLabel, type Runner } from "./runners";
import { calloutHTML, clearFormErrors, esc, makeToast, setFieldError } from "./ui";
import { t } from "../i18n";

// "Where agents run" on a project's settings: in Thulu, or on a registered machine from a folder on
// it. A project created from a folder on a machine has no code in Thulu, so it can only move to
// another machine.

export interface PlacedProject {
  id: string;
  name: string;
  source?: string;
  runner_id?: string;
  runner_path?: string;
}

export interface RunnerPlaceEls {
  where: HTMLSelectElement;
  path: HTMLInputElement;
  pathField: HTMLElement;
  list: HTMLDataListElement;
  note: HTMLElement;
  save: HTMLButtonElement;
  panel: HTMLElement;
}

const THULU = "";
const toast = makeToast(3500);

export function codeOnlyOnRunner(p: PlacedProject): boolean {
  return p.source === "runner";
}

// The body a save sends: an empty runner moves the project back to Thulu and drops the folder.
export function placeBody(
  runnerID: string,
  path: string,
): { runner_id: string; runner_path: string } {
  return { runner_id: runnerID, runner_path: runnerID ? path.trim() : "" };
}

export interface RunnerPlace {
  render: (p: PlacedProject) => Promise<void>;
  runners: () => Runner[];
}

export function runnerPlace(els: RunnerPlaceEls, onSaved: (p: PlacedProject) => void): RunnerPlace {
  let project: PlacedProject | null = null;
  let runners: Runner[] = [];

  function noteHTML(): string {
    const id = els.where.value;
    if (!id) return "";
    const r = runners.find((x) => x.id === id);
    return calloutHTML({
      tone: "info",
      icon: "monitor",
      body: t("lib.runner_place.login_note", { name: r?.name ?? t("lib.runners.badge.unknown") }),
    });
  }

  function syncFields() {
    const onMachine = els.where.value !== THULU;
    els.pathField.hidden = !onMachine;
    els.note.innerHTML = noteHTML();
  }

  async function fillFolders(runnerID: string) {
    els.list.innerHTML = "";
    if (!runnerID) return;
    try {
      const repos = await loadRunnerRepos(runnerID);
      if (els.where.value !== runnerID) return;
      els.list.innerHTML = repos.repos
        .filter((r) => !r.project_id || r.project_id === project?.id)
        .map(
          (r) =>
            `<option value="${esc(r.path)}">${esc([r.name, r.branch].filter(Boolean).join(" · "))}</option>`,
        )
        .join("");
    } catch {
      // Typing the path still works; the daemon checks it on save.
    }
  }

  function paint() {
    if (!project) return;
    const active = activeRunners(runners);
    const current = project.runner_id ?? "";
    // A revoked runner still shows, so the select does not pretend the project moved.
    const shown = runners.filter((r) => !r.revoked_at || r.id === current);
    const thulu = `<option value="${THULU}"${current ? "" : " selected"}${codeOnlyOnRunner(project) ? " disabled" : ""}>${esc(t("lib.runner_place.in_thulu"))}</option>`;
    els.where.innerHTML =
      thulu +
      shown
        .map(
          (r) =>
            `<option value="${esc(r.id)}"${r.id === current ? " selected" : ""}>${esc(t("lib.runner_place.on_machine", { label: runnerLabel(r) }))}</option>`,
        )
        .join("");
    els.path.value = project.runner_path ?? "";
    els.panel.hidden = !active.length && !current;
    syncFields();
    void fillFolders(els.where.value);
  }

  els.where.addEventListener("change", () => {
    if (els.where.value !== (project?.runner_id ?? "")) els.path.value = "";
    else els.path.value = project?.runner_path ?? "";
    syncFields();
    void fillFolders(els.where.value);
  });

  els.save.addEventListener("click", () => void save());

  async function save() {
    if (!project) return;
    clearFormErrors(els.panel);
    const body = placeBody(els.where.value, els.path.value);
    if (body.runner_id && !body.runner_path.startsWith("/")) {
      setFieldError(els.path, t("lib.runner_place.errors.path_required"), { focus: true });
      return;
    }
    els.save.disabled = true;
    try {
      const saved = await api.patch<PlacedProject>(
        `/projects/${encodeURIComponent(project.id)}`,
        body,
      );
      project = saved;
      onSaved(saved);
      toast(t("lib.runner_place.saved"), "ok");
      paint();
    } catch (e) {
      setFieldError(
        body.runner_id ? els.path : els.where,
        t("lib.runner_place.errors.save", { error: e instanceof ApiError ? e.message : String(e) }),
      );
    } finally {
      els.save.disabled = false;
    }
  }

  return {
    async render(p: PlacedProject): Promise<void> {
      project = p;
      try {
        runners = await loadRunners();
      } catch {
        runners = [];
      }
      paint();
    },
    runners: (): Runner[] => runners,
  };
}

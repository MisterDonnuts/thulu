import { ApiError } from "./api";
import { relTime } from "./activity";
import {
  activeRunners,
  createRunnerToken,
  loadRunnerRepos,
  loadRunners,
  runnerLabel,
  setupStepsHTML,
  waitForNewRunner,
  type Runner,
  type RunnerRepo,
  type RunnerRepos,
} from "./runners";
import {
  bindCodeCopy,
  buttonHTML,
  calloutHTML,
  checkGroupHTML,
  checkHTML,
  codeBlockHTML,
  disclosureHTML,
  esc,
} from "./ui";
import { t } from "../i18n";

// The "On a machine" origin of New project: pick a registered runner, then one of the repositories
// it reported inside the folders its owner allowed. A repository the list doesn't show is added from
// the machine with `thulu add`; the form notices that project and offers to open it. The daemon checks
// the folder on create, and the runner checks it again before every run.

export interface RunnerChoice {
  runnerID: string;
  path: string;
  repo?: RunnerRepo;
}

export interface RunnerSourceOptions {
  slot: HTMLElement;
  nameInput: HTMLInputElement;
  workspace: () => string;
  // A project someone created on the machine with `thulu add` while the form was open.
  onCreated?: (projectID: string) => void;
}

const POLL_MS = 5000;
const FILTER_FROM = 8;

export function repoMatches(repo: RunnerRepo, q: string): boolean {
  const needle = q.trim().toLowerCase();
  if (!needle) return true;
  return [repo.name, repo.path, repo.origin ?? "", repo.branch ?? ""].some((s) =>
    s.toLowerCase().includes(needle),
  );
}

// The machine's home folder reads as ~: the daemon doesn't know it, but macOS and Linux homes sit
// in the same two places.
export function shortPath(path: string): string {
  return path.replace(/^\/(?:Users|home)\/[^/]+(?=\/|$)/, "~");
}

export function repoHint(repo: RunnerRepo): string {
  return [shortPath(repo.path), repo.branch].filter(Boolean).join(" · ");
}

export function splitRepos(repos: RunnerRepo[]): { free: RunnerRepo[]; taken: RunnerRepo[] } {
  return { free: repos.filter((r) => !r.project_id), taken: repos.filter((r) => !!r.project_id) };
}

// The projects on the machine that weren't there before: what `thulu add` made meanwhile.
export function newProjects(repos: RunnerRepo[], seen: Set<string>): RunnerRepo[] {
  return repos.filter((r) => r.project_id && !seen.has(r.project_id));
}

export interface RunnerSource {
  open: () => Promise<void>;
  close: () => void;
  read: () => RunnerChoice | { field: string; message: string };
}

export function runnerSource(o: RunnerSourceOptions): RunnerSource {
  let runners: Runner[] = [];
  let runnerID = "";
  let repos: RunnerRepos | null = null;
  let picked = "";
  let filter = "";
  let autoName = "";
  let error = "";
  let setup = "";
  let timer: ReturnType<typeof setInterval> | undefined;
  let wait: AbortController | null = null;
  let live = false;
  // Project ids already on the machine when its list first loaded; null until then.
  let seen: Set<string> | null = null;
  let created: RunnerRepo | null = null;

  const ws = () => o.workspace();
  // Read through a function: after an await the flag may have changed under a narrowed copy.
  const isLive = () => live;

  function machineHTML(): string {
    const active = activeRunners(runners);
    if (!active.length) {
      return (
        calloutHTML({
          icon: "monitor",
          title: t("lib.runner_source.none_title"),
          body: t("lib.runner_source.none_body"),
        }) +
        (setup
          ? `<div class="runner-setup">${setup}<p class="muted">${esc(t("lib.runners.setup.waiting"))}</p></div>`
          : `<div class="form-actions">${buttonHTML({ label: t("lib.runners.add"), icon: "plus", variant: "primary", attrs: { "data-rs-add": "" } })}</div>`)
      );
    }
    const options = active
      .map(
        (r) =>
          `<option value="${esc(r.id)}"${r.id === runnerID ? " selected" : ""}>${esc(runnerLabel(r))}</option>`,
      )
      .join("");
    return `<label class="field"><span class="field-label">${esc(t("lib.runner_source.machine"))}</span><select id="m-runner" data-rs-runner>${options}</select></label>`;
  }

  function addCommandHTML(lead: boolean): string {
    return calloutHTML({
      tone: lead ? "accent" : "info",
      icon: "terminal",
      title: lead ? t("lib.runner_source.add_title_lead") : t("lib.runner_source.add_title"),
      bodyHTML:
        codeBlockHTML({ text: "thulu add", copy: true, label: t("lib.runner_source.add_copy") }) + // i18n-ignore a shell command
        `<p>${esc(t("lib.runner_source.add_body"))}</p>`,
    });
  }

  function createdHTML(): string {
    if (!created?.project_id) return "";
    return calloutHTML({
      tone: "ok",
      icon: "check",
      body: t("lib.runner_source.created", { project: created.project_name ?? created.name }),
      actionsHTML: buttonHTML({
        label: t("lib.runner_source.open_project"),
        size: "sm",
        href: `/project/?id=${encodeURIComponent(created.project_id)}`,
      }),
    });
  }

  function takenHTML(taken: RunnerRepo[]): string {
    if (!taken.length) return "";
    const items = taken
      .map(
        (r) =>
          `<p class="field-hint"><strong>${esc(r.project_name ?? r.name)}</strong> · ${esc(shortPath(r.path))}</p>`,
      )
      .join("");
    return disclosureHTML({
      summary: t("lib.runner_source.taken_summary", {
        count: taken.length,
        names: taken.map((r) => r.project_name ?? r.name).join(", "),
      }),
      bodyHTML: items,
    });
  }

  function footerHTML(r: RunnerRepos): string {
    const parts = [
      r.scanned_at ? t("lib.runner_source.scanned", { when: relTime(r.scanned_at) }) : "",
      r.roots.length
        ? t("lib.runner_source.roots_count", { count: r.roots.length })
        : t("lib.runner_source.no_roots"),
    ].filter(Boolean);
    const truncated = r.truncated ? `<p>${esc(t("lib.runner_source.truncated"))}</p>` : "";
    if (!r.roots.length) return `<p class="field-hint">${esc(parts.join(" · "))}</p>`;
    return disclosureHTML({
      summary: parts.join(" · "),
      bodyHTML:
        r.roots.map((root) => `<p class="field-hint">${esc(shortPath(root))}</p>`).join("") +
        truncated,
    });
  }

  function reposHTML(): string {
    if (!runnerID) return "";
    if (!repos) return `<p class="muted">${esc(t("lib.runner_source.loading"))}</p>`;
    if (!repos.reported) {
      return (
        calloutHTML({
          tone: "warn",
          icon: "warn",
          title: t("lib.runner_source.not_reported_title"),
          body: t("lib.runner_source.not_reported_body"),
        }) + addCommandHTML(true)
      );
    }
    const { free, taken } = splitRepos(repos.repos);
    if (!free.length) {
      return createdHTML() + addCommandHTML(true) + takenHTML(taken) + footerHTML(repos);
    }
    const visible = free.filter((r) => repoMatches(r, filter));
    const items = visible.map((r) =>
      checkHTML({
        type: "radio",
        name: "m-runner-repo",
        value: r.path,
        label: r.name,
        hint: repoHint(r),
        checked: picked === r.path,
      }),
    );
    return (
      createdHTML() +
      `<div class="field"><span class="field-label" id="m-runner-repo-label">${esc(t("lib.runner_source.folder"))}</span>` +
      (free.length >= FILTER_FROM
        ? `<input type="search" id="m-runner-filter" data-rs-filter value="${esc(filter)}" placeholder="${esc(t("lib.runner_source.filter"))}" aria-label="${esc(t("lib.runner_source.filter"))}" />`
        : "") +
      (items.length
        ? checkGroupHTML({
            type: "radio",
            labelledBy: "m-runner-repo-label",
            itemsHTML: items.join(""),
          })
        : `<p class="muted">${esc(t("lib.runner_source.no_match"))}</p>`) +
      takenHTML(taken) +
      `</div>` +
      addCommandHTML(false) +
      footerHTML(repos)
    );
  }

  function paint() {
    const focusedID = (document.activeElement as HTMLElement | null)?.id ?? "";
    // The 5-second refresh repaints: a list someone opened must stay open.
    const opened = [...o.slot.querySelectorAll("details")].map((d) => d.open);
    o.slot.innerHTML =
      `<p class="muted">${esc(t("lib.runner_source.lede"))}</p>` +
      machineHTML() +
      reposHTML() +
      (error ? calloutHTML({ tone: "danger", icon: "alert", body: error }) : "");
    bindCodeCopy(o.slot);
    o.slot.querySelectorAll("details").forEach((d, i) => {
      if (opened[i]) d.open = true;
    });
    if (focusedID) {
      const el = document.getElementById(focusedID) as HTMLInputElement | null;
      if (el && o.slot.contains(el)) {
        el.focus();
        if (el.type === "search" || el.tagName === "INPUT")
          el.setSelectionRange(el.value.length, el.value.length);
      }
    }
  }

  async function refreshRunners() {
    try {
      runners = await loadRunners(ws());
      error = "";
    } catch (e) {
      error = t("lib.runner_source.errors.load", {
        error: e instanceof ApiError ? e.message : String(e),
      });
    }
    const active = activeRunners(runners);
    if (!active.some((r) => r.id === runnerID)) {
      runnerID = (active.find((r) => r.online) ?? active.at(0))?.id ?? "";
      repos = null;
      picked = "";
      seen = null;
    }
  }

  async function refreshRepos() {
    if (!runnerID) return;
    const id = runnerID;
    try {
      const fresh = await loadRunnerRepos(id, ws());
      if (id !== runnerID) return;
      repos = fresh;
      noticeCreated(fresh);
    } catch (e) {
      error = t("lib.runner_source.errors.repos", {
        error: e instanceof ApiError ? e.message : String(e),
      });
    }
  }

  function noticeCreated(fresh: RunnerRepos) {
    if (!fresh.reported) return;
    const ids = fresh.repos.flatMap((r) => (r.project_id ? [r.project_id] : []));
    if (!seen) {
      seen = new Set(ids);
      return;
    }
    const added = newProjects(fresh.repos, seen).at(0);
    ids.forEach((id) => seen?.add(id));
    if (!added?.project_id) return;
    created = added;
    if (picked === added.path) picked = "";
    o.onCreated?.(added.project_id);
  }

  async function tick() {
    if (!live) return;
    await refreshRunners();
    await refreshRepos();
    // Repainting while the person types in the filter would steal their caret and selection.
    if (isLive() && (document.activeElement as HTMLElement | null)?.id !== "m-runner-filter")
      paint();
  }

  function pickRepo(path: string) {
    picked = path;
    const repo = repos?.repos.find((r) => r.path === path);
    const current = o.nameInput.value.trim();
    if (repo && (current === "" || current === autoName)) {
      o.nameInput.value = repo.name;
      autoName = repo.name;
    }
    paint();
  }

  async function addMachine() {
    try {
      const tok = await createRunnerToken(ws());
      setup = setupStepsHTML(tok);
      error = "";
    } catch (e) {
      error = t("lib.runners.errors.token", {
        error: e instanceof ApiError ? e.message : String(e),
      });
    }
    paint();
    if (!setup) return;
    wait?.abort();
    wait = new AbortController();
    const fresh = await waitForNewRunner(new Set(runners.map((r) => r.id)), ws(), wait.signal);
    if (!fresh || !isLive()) return;
    setup = "";
    runnerID = fresh.id;
    repos = null;
    await tick();
  }

  o.slot.addEventListener("change", (e) => {
    const el = e.target as HTMLInputElement | HTMLSelectElement;
    if (el.matches("[data-rs-runner]")) {
      runnerID = el.value;
      repos = null;
      picked = "";
      seen = null;
      created = null;
      paint();
      void refreshRepos().then(paint);
    } else if (el.matches('input[name="m-runner-repo"]')) {
      pickRepo(el.value);
    }
  });
  o.slot.addEventListener("input", (e) => {
    const el = e.target as HTMLInputElement;
    if (el.matches("[data-rs-filter]")) {
      filter = el.value;
      paint();
    }
  });
  o.slot.addEventListener("click", (e) => {
    if ((e.target as HTMLElement).closest("[data-rs-add]")) void addMachine();
  });

  return {
    async open() {
      live = true;
      picked = "";
      filter = "";
      autoName = "";
      setup = "";
      repos = null;
      seen = null;
      created = null;
      paint();
      await tick();
      clearInterval(timer);
      timer = setInterval(() => void tick(), POLL_MS);
    },
    close() {
      live = false;
      clearInterval(timer);
      wait?.abort();
    },
    // The field to flag and its message when the choice is not complete.
    read(): RunnerChoice | { field: string; message: string } {
      if (!runnerID)
        return { field: "m-runner-slot", message: t("lib.runner_source.errors.no_machine") };
      if (!picked)
        return { field: "m-runner-slot", message: t("lib.runner_source.errors.no_folder") };
      return { runnerID, path: picked, repo: repos?.repos.find((r) => r.path === picked) };
    },
  };
}

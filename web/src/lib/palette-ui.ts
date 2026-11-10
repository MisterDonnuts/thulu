import { navigate } from "astro:transitions/client";
import { t } from "../i18n";
import { api, apiWriteCount } from "./api";
import { authDisabled, sessionInfo } from "./auth";
import {
  agentItems,
  boardItems,
  pageItems,
  paletteStale,
  projectItems,
  searchPalette,
  taskChangeStalesPalette,
  taskItems,
  type PaletteGroup,
  type PaletteItem,
  type PaletteKind,
  type PaletteRead,
  type TaskLike,
} from "./command-palette";
import { FRAME_RESET, TASKS_TOPIC, events } from "./events";
import { emptyRowHTML, esc, groupHeadHTML, listRowHTML } from "./ui";

const GROUP_LABEL: Record<PaletteKind, () => string> = {
  page: () => t("palette.groups.pages"),
  task: () => t("palette.groups.tasks"),
  project: () => t("palette.groups.projects"),
  agent: () => t("palette.groups.agents"),
  board: () => t("palette.groups.boards"),
};

let items: PaletteItem[] = [];
let shown: PaletteItem[] = [];
let active = 0;
let loading = false;
let lastRead: PaletteRead | null = null;
let everLoaded = false;
let lastFocus: HTMLElement | null = null;
let bound = false;

const OPTION = '#cmdk-list [role="option"]';

const el = {
  overlay: () => document.getElementById("cmdk"),
  input: () => document.getElementById("cmdk-input") as HTMLInputElement | null,
  list: () => document.getElementById("cmdk-list"),
};

export function isPaletteOpen(): boolean {
  return el.overlay()?.hidden === false;
}

export function togglePalette(): void {
  if (isPaletteOpen()) closePalette();
  else openPalette();
}

export function openPalette(): void {
  const overlay = el.overlay();
  const input = el.input();
  if (!overlay || !input) return;
  bind(overlay, input);
  lastFocus = document.activeElement as HTMLElement | null;
  overlay.hidden = false;
  input.value = "";
  active = 0;
  render();
  input.focus();
  if (!loading && paletteStale(lastRead, Date.now(), apiWriteCount())) void load();
}

export function invalidatePalette(): void {
  lastRead = null;
}

export function closePalette(restoreFocus = true): void {
  const overlay = el.overlay();
  if (!overlay || overlay.hidden) return;
  overlay.hidden = true;
  if (restoreFocus && lastFocus?.isConnected) lastFocus.focus();
  lastFocus = null;
}

async function load(): Promise<void> {
  loading = true;
  // Taken before the requests go out: a write landing while they are in flight may be missing, so
  // the next open re-reads.
  const writes = apiWriteCount();
  const [me, noLogin] = await Promise.all([
    sessionInfo().catch(() => null),
    authDisabled().catch(() => false),
  ]);
  const pages = pageItems({ login: !noLogin, operator: noLogin || me?.operator === true });
  if (!everLoaded) {
    items = pages;
    render();
  }

  const [tasks, projects, agents, boards] = await Promise.allSettled([
    api.get<TaskLike[] | null>("/tasks"),
    api.get<Array<{ id: string; name: string; repo_url?: string }> | null>("/projects"),
    api.get<Array<{ name: string; description?: string }> | null>("/types"),
    api.get<{ boards: Array<{ id: string; label?: string }> | null } | null>("/boards"),
  ]);
  const boardList = boards.status === "fulfilled" ? (boards.value?.boards ?? []) : [];
  const boardNames = new Map(boardList.map((b) => [b.id, b.label ?? b.id]));
  items = [
    ...pages,
    ...(tasks.status === "fulfilled" ? taskItems(tasks.value ?? [], boardNames) : []),
    ...(projects.status === "fulfilled" ? projectItems(projects.value ?? []) : []),
    ...(agents.status === "fulfilled" ? agentItems(agents.value ?? []) : []),
    ...boardItems(boardList),
  ];
  loading = false;
  everLoaded = true;
  lastRead = { at: Date.now(), writes };
  if (isPaletteOpen()) render();
}

function render(): void {
  const list = el.list();
  const input = el.input();
  if (!list || !input) return;
  const groups: PaletteGroup[] = searchPalette(input.value, items);
  shown = groups.flatMap((g) => g.items);
  if (active >= shown.length) active = Math.max(0, shown.length - 1);
  if (shown.length === 0) {
    list.innerHTML = loading
      ? emptyRowHTML(undefined, { as: "div", loading: true })
      : emptyRowHTML(t("palette.empty", { query: input.value.trim() }), { as: "div" });
    input.removeAttribute("aria-activedescendant");
    return;
  }
  let n = 0;
  list.innerHTML = groups
    .map(
      (g) =>
        `<div class="cmdk-group" role="group" aria-label="${esc(GROUP_LABEL[g.kind]())}">
          ${groupHeadHTML({ title: GROUP_LABEL[g.kind](), attrs: { "aria-hidden": "true" } })}
          ${g.items
            .map((item) => {
              const i = n++;
              return listRowHTML({
                href: item.href,
                size: "sm",
                inline: true,
                title: item.label,
                meta: item.detail,
                id: `cmdk-opt-${i}`,
                attrs: {
                  role: "option",
                  "aria-selected": String(i === active),
                  "data-index": String(i),
                },
              });
            })
            .join("")}
        </div>`,
    )
    .join("");
  input.setAttribute("aria-activedescendant", `cmdk-opt-${active}`);
}

function move(to: number): void {
  if (shown.length === 0) return;
  active = (to + shown.length) % shown.length;
  const list = el.list();
  list?.querySelectorAll<HTMLElement>(OPTION).forEach((a) => {
    a.setAttribute("aria-selected", String(Number(a.dataset.index) === active));
  });
  el.input()?.setAttribute("aria-activedescendant", `cmdk-opt-${active}`);
  document.getElementById(`cmdk-opt-${active}`)?.scrollIntoView({ block: "nearest" });
}

function go(item: PaletteItem | undefined): void {
  if (!item) return;
  closePalette(false);
  void navigate(item.href);
}

function bind(overlay: HTMLElement, input: HTMLInputElement): void {
  if (bound) return;
  bound = true;
  // A reconnect (FRAME_RESET) also invalidates: frames sent while the channel was down are lost.
  events.subscribe(
    TASKS_TOPIC,
    (f) => {
      if (f.type === FRAME_RESET || taskChangeStalesPalette(f.type)) invalidatePalette();
    },
    { replay: false },
  );
  input.addEventListener("input", () => {
    active = 0;
    render();
  });
  // Stop propagation: lib/keyboard.ts would read Esc as closing the drawer behind and arrows as
  // board moves.
  overlay.addEventListener("keydown", (e) => {
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      closePalette();
    } else if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      e.stopPropagation();
      move(active + (e.key === "ArrowDown" ? 1 : -1));
    } else if (e.key === "Enter") {
      e.preventDefault();
      e.stopPropagation();
      go(shown[active]);
    } else if (e.key === "Tab") {
      e.preventDefault();
      move(active + (e.shiftKey ? -1 : 1));
    }
  });
  overlay.addEventListener("click", (e) => {
    const target = e.target as HTMLElement;
    if (target === overlay) {
      closePalette();
      return;
    }
    const link = target.closest<HTMLAnchorElement>(OPTION);
    if (!link) return;
    closePalette(false);
  });
  overlay.addEventListener("mousemove", (e) => {
    const link = (e.target as HTMLElement).closest<HTMLElement>(OPTION);
    const at = Number(link?.dataset.index);
    if (link && at !== active) move(at);
  });
  document.addEventListener("astro:before-swap", () => closePalette(false));
}

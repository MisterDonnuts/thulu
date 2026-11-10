import { navigate } from "astro:transitions/client";
import { t } from "../i18n";
import { api, ApiError } from "./api";
import { nextColor, normalizeHex, sessionMayEditBoards, slugify, validId } from "./board-editor";
import { TYPE_TEMPLATES, isLockedStage, newType, toggleSkip } from "./board-types";
import { columnHeadHTML } from "./board";
import {
  columnAgentText,
  projectAgentPicker,
  waitsForPerson,
  type TemplateProject,
} from "./column-agent";
import { mountColumnEditor, stageColorSwatches, type ColumnEditor } from "./column-editor";
import { bindModal } from "./modal";
import { destinationOptions, pendingTaskCount, type MoveDestination } from "./move-picker";
import { reconcile, setText } from "./reactive";
import {
  stagePillHTML,
  type Board,
  type BoardsResponse,
  type Stage,
  type TaskType,
} from "./stages";
import {
  badgeHTML,
  bindMenu,
  buttonHTML,
  cardClasses,
  cardHTML,
  checkHTML,
  confirmDialog,
  dotHTML,
  emptyRowHTML,
  emptyStateHTML,
  esc,
  fieldHTML,
  icoSvg,
  makeToast,
  menuItemsHTML,
  moveSwatchFocus,
  moveTasksBodyHTML,
  setCount,
  setFieldError,
  setFormError,
  skeletonHTML,
  swatchPickerHTML,
  tagHTML,
  type MenuEntry,
  type MenuHandle,
} from "./ui";

const byId = (id: string) => document.getElementById(id);

// Keys must match the glyph names in internal/stage/icons.go.
const TYPE_ICON_TEXT: Record<string, string> = {
  bug: t("settings.boards.types.icons.bug"),
  feature: t("settings.boards.types.icons.feature"),
  chore: t("settings.boards.types.icons.chore"),
  spike: t("settings.boards.types.icons.spike"),
  rocket: t("settings.boards.types.icons.rocket"),
  shield: t("settings.boards.types.icons.shield"),
  book: t("settings.boards.types.icons.book"),
  bolt: t("settings.boards.types.icons.bolt"),
  refresh: t("settings.boards.types.icons.refresh"),
  question: t("settings.boards.types.icons.question"),
  alert: t("settings.boards.types.icons.alert"),
  palette: t("settings.boards.types.icons.palette"),
  plug: t("settings.boards.types.icons.plug"),
  database: t("settings.boards.types.icons.database"),
  checklist: t("settings.boards.types.icons.checklist"),
  clock: t("settings.boards.types.icons.clock"),
  star: t("settings.boards.types.icons.star"),
  users: t("settings.boards.types.icons.users"),
};

export function boardSettingsURL(id?: string): string {
  return id ? `/settings/?board=${encodeURIComponent(id)}#boards` : "/settings/#boards";
}

export function boardMeta(b: Pick<Board, "stage_count" | "task_count">): string {
  return t("settings.boards.card.meta", {
    columns: t("settings.boards.card.columns", { count: b.stage_count ?? 0 }),
    tasks: t("settings.boards.card.tasks", { count: b.task_count ?? 0 }),
  });
}

export function boardMenuEntries(
  b: Pick<Board, "default">,
  o: { canEdit: boolean; inDetail: boolean },
): MenuEntry[] {
  const act = (a: string) => ({ "data-act": a });
  const open: MenuEntry = {
    title: t("settings.boards.menu.open"),
    icon: "i-board",
    attrs: act("open"),
  };
  if (!o.canEdit) return [open];
  const isDefault = Boolean(b.default);
  return [
    open,
    ...(o.inDetail
      ? []
      : [{ title: t("settings.boards.menu.edit"), icon: "i-columns", attrs: act("edit") }]),
    {
      title: t("settings.boards.menu.make_default"),
      icon: "i-check",
      disabled: isDefault,
      attrs: act("default"),
    },
    { title: t("settings.boards.menu.duplicate"), icon: "i-copy", attrs: act("duplicate") },
    { sep: true },
    {
      title: t("settings.boards.menu.delete"),
      icon: "i-trash",
      danger: true,
      disabled: isDefault,
      sub: isDefault ? t("settings.boards.menu.default_undeletable") : undefined,
      attrs: act("delete"),
    },
  ];
}

export function behaviorChips(st: Stage): string[] {
  const out: string[] = [];
  if (st.initial) out.push(t("settings.boards.chips.initial"));
  if (st.terminal) out.push(t("settings.boards.chips.terminal"));
  if (st.auto_advance) out.push(t("settings.boards.chips.auto_advance"));
  if (st.push_branch) out.push(t("settings.boards.chips.push_branch"));
  if (st.open_pr) out.push(t("settings.boards.chips.open_pr"));
  const approvals = (st.required_approvals ?? []).length;
  if (approvals) out.push(t("settings.boards.chips.approvals", { count: approvals }));
  if (st.wip_group === "work") out.push(t("settings.boards.chips.wip_work"));
  if (st.wip_group === "review") out.push(t("settings.boards.chips.wip_review"));
  return out;
}

export function mountBoardsSettings(signal: AbortSignal): void {
  const root = byId("bs");
  if (!root) return;
  const toast = makeToast(3500);
  const params = new URLSearchParams(window.location.search);
  const detailID = params.get("board") ?? "";
  const listView = byId("bs-list-view");
  const detailView = byId("bs-detail-view");
  if (listView) listView.hidden = Boolean(detailID);
  if (detailView) detailView.hidden = !detailID;

  let boards: Board[] = [];
  let canEdit = true;
  let loading: Promise<void> | null = null;

  const showsBoards = () => window.location.hash === "#boards";
  // The detail loads only once: reloading would mount the column editor and its listeners a second
  // time.
  let detailLoaded = false;
  const load = () => {
    if (detailID && detailLoaded) return Promise.resolve();
    detailLoaded = Boolean(detailID);
    loading ??= (detailID ? loadDetail(detailID) : loadList()).finally(() => {
      loading = null;
    });
    return loading;
  };
  if (showsBoards()) void load();
  document.addEventListener(
    "octopus:settings-section",
    (e) => {
      if ((e as CustomEvent<{ key: string }>).detail.key === "boards") void load();
    },
    { signal },
  );

  async function fetchBoards(): Promise<void> {
    const [resp, may] = await Promise.all([
      api.get<BoardsResponse>("/boards"),
      sessionMayEditBoards(),
    ]);
    boards = resp.boards;
    canEdit = may;
    root?.toggleAttribute("data-can-edit", canEdit);
  }

  const cards = byId("bs-cards");
  const listErr = byId("bs-list-error");
  const stagesOf = new Map<string, Stage[]>();

  async function loadList() {
    try {
      await fetchBoards();
      if (listErr) listErr.hidden = true;
      renderCards();
      await Promise.all(
        boards.map(async (b) => {
          try {
            stagesOf.set(
              b.id,
              await api.get<Stage[]>(`/boards/${encodeURIComponent(b.id)}/stages`),
            );
          } catch {
            stagesOf.delete(b.id);
          }
        }),
      );
      renderCards();
    } catch (e) {
      if (listErr) {
        listErr.textContent = t("settings.boards.errors.list", { error: errText(e) });
        listErr.hidden = false;
      }
      if (cards && boards.length === 0) cards.innerHTML = "";
    } finally {
      cards?.removeAttribute("aria-busy");
    }
  }

  function chipsHTML(id: string): string {
    const stages = stagesOf.get(id);
    if (!stages) return skeletonHTML({ shape: "chip", tag: "li", cls: "bs-chip-wait" });
    return stages.map((s) => `<li>${stagePillHTML(s.id, s, { size: "sm" })}</li>`).join("");
  }

  function boardCardHTML(b: Board): string {
    return cardHTML({
      title: b.label,
      href: boardSettingsURL(b.id),
      headHTML:
        (b.default ? badgeHTML(t("settings.boards.card.default"), "accent") : "") +
        buttonHTML({
          label: t("settings.boards.card.actions", { board: b.label }),
          icon: "more",
          iconOnly: true,
          size: "lg",
          cls: "bs-card-menu",
          attrs: { "data-board-menu": true, "aria-haspopup": "menu", "aria-expanded": "false" },
        }),
      bodyHTML:
        (b.description ? `<p class="bs-card-desc">${esc(b.description)}</p>` : "") +
        `<ol class="bs-chips" aria-label="${esc(t("settings.boards.card.columns_label", { board: b.label }))}">${chipsHTML(b.id)}</ol>` +
        `<p class="bs-card-meta">${esc(boardMeta(b))}</p>`,
      attrs: { "data-board": b.id },
    });
  }

  function boardCardEl(html: string): HTMLElement {
    const tpl = document.createElement("template");
    tpl.innerHTML = html;
    return tpl.content.firstElementChild as HTMLElement;
  }

  function renderCards() {
    if (!cards) return;
    if (boards.length === 0) {
      cards.innerHTML = emptyStateHTML({
        icon: "board",
        title: t("settings.boards.empty.title"),
        body: t("settings.boards.empty.body"),
        cls: "bs-empty",
      });
      return;
    }
    reconcile<Board>({
      container: cards,
      items: boards,
      key: (b) => b.id,
      create: (b) => {
        const html = boardCardHTML(b);
        const el = boardCardEl(html);
        el.dataset.sig = html;
        return el;
      },
      update: (el, b) => {
        const html = boardCardHTML(b);
        if (el.dataset.sig !== html) {
          const next = boardCardEl(html);
          el.className = next.className;
          el.innerHTML = next.innerHTML;
          el.dataset.sig = html;
        }
      },
    });
    cards.querySelector(".empty-state")?.remove();
    cards.querySelectorAll(".bs-card-skeleton").forEach((el) => el.remove());
  }

  const menu = byId("bs-menu");
  let bound: { trigger: HTMLElement; handle: MenuHandle; ctl: AbortController } | null = null;

  function openMenu(id: string, trigger: HTMLElement, focus: "first" | "last" = "first") {
    if (!menu || !boards.some((x) => x.id === id)) return;
    if (bound?.trigger !== trigger) {
      bound?.handle.close(false);
      bound?.ctl.abort();
      const ctl = new AbortController();
      signal.addEventListener("abort", () => ctl.abort(), { once: true });
      const handle = bindMenu(menu, trigger, {
        signal: ctl.signal,
        onOpen: () => {
          const b = boards.find((x) => x.id === id);
          if (!b) return;
          menu.innerHTML = menuItemsHTML(boardMenuEntries(b, { canEdit, inDetail: !!detailID }));
          menu.setAttribute("aria-label", t("settings.boards.card.actions", { board: b.label }));
        },
        onClose: () => menu.replaceChildren(),
        onSelect: (item) => runMenuItem(id, item, trigger),
      });
      bound = { trigger, handle, ctl };
    }
    bound.handle.open(undefined, focus);
  }

  function runMenuItem(id: string, item: HTMLElement, trigger: HTMLElement) {
    const act = item.dataset.act;
    if (!act) return;
    switch (act) {
      case "open":
        void navigate(`/tasks/?board=${encodeURIComponent(id)}`);
        break;
      case "edit":
        void navigate(boardSettingsURL(id));
        break;
      case "default":
        void makeDefault(id, trigger);
        break;
      case "duplicate":
        openNewBoard(trigger, id);
        break;
      case "delete":
        void deleteBoard(id, trigger);
        break;
    }
  }

  const menuTriggerOf = (e: Event) => {
    const btn = (e.target as HTMLElement).closest<HTMLElement>("[data-board-menu]");
    // The bound trigger toggles the menu through its own bindMenu listener; handling it here too
    // would reopen it.
    if (!btn || bound?.trigger === btn) return null;
    const id = btn.closest<HTMLElement>("[data-board]")?.dataset.board ?? detailID;
    return id ? { btn, id } : null;
  };
  root.addEventListener("click", (e) => {
    const hit = menuTriggerOf(e);
    if (hit) openMenu(hit.id, hit.btn);
  });
  root.addEventListener("keydown", (e) => {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
    const hit = menuTriggerOf(e);
    if (!hit) return;
    e.preventDefault();
    openMenu(hit.id, hit.btn, e.key === "ArrowUp" ? "last" : "first");
  });

  async function makeDefault(id: string, trigger: HTMLElement | null) {
    try {
      await api.patch(`/boards/${encodeURIComponent(id)}`, { default: true });
      boards = boards.map((b) => ({ ...b, default: b.id === id }));
      renderCards();
      renderDetailHead();
      const b = boards.find((x) => x.id === id);
      toast(t("settings.boards.toasts.default", { board: b?.label ?? id }), "ok");
      document.dispatchEvent(new CustomEvent("octopus:boards-changed"));
    } catch (e) {
      toast(t("settings.boards.errors.default", { error: errText(e) }), "err", 6000);
    }
    trigger?.focus();
  }

  const newModalEl = byId("bs-new-modal");
  const newForm = byId("bs-new-form") as HTMLFormElement | null;
  const newName = byId("bs-new-name") as HTMLInputElement | null;
  const newDesc = byId("bs-new-desc") as HTMLTextAreaElement | null;
  const newFrom = byId("bs-new-from") as HTMLSelectElement | null;
  const newSlugHint = byId("bs-new-slug");
  const newIdField = byId("bs-new-id") as HTMLInputElement | null;
  const newErr = byId("bs-new-error");
  const newSubmit = byId("bs-new-submit") as HTMLButtonElement | null;
  let newTrigger: HTMLElement | null = null;
  let idTouched = false;
  const newModal = bindModal(newModalEl, {
    onClose: () => newTrigger?.focus(),
  });

  function openNewBoard(trigger: HTMLElement | null, cloneFrom = "") {
    if (!newForm || !newName || !newFrom) return;
    newTrigger = trigger;
    newForm.reset();
    idTouched = false;
    if (newErr) setFormError(newErr, "");
    setFieldError(newName, "");
    newFrom.innerHTML =
      `<option value="">${esc(t("settings.boards.new.from_standard"))}</option>` +
      boards
        .map(
          (b) =>
            `<option value="${esc(b.id)}">${esc(t("settings.boards.new.from_copy", { board: b.label }))}</option>`,
        )
        .join("");
    newFrom.value = cloneFrom;
    if (cloneFrom) {
      const src = boards.find((b) => b.id === cloneFrom);
      if (src) newName.value = t("settings.boards.new.copy_name", { board: src.label });
    }
    syncSlug();
    newModal.open(newName);
  }

  function newSlug(): string {
    const typed = newIdField?.value.trim() ?? "";
    if (idTouched && typed) return typed;
    return slugify(
      newName?.value ?? "",
      boards.map((b) => b.id),
      "board",
    );
  }

  function syncSlug() {
    const label = newName?.value.trim() ?? "";
    const slug = newSlug();
    if (newIdField && !idTouched) newIdField.value = label ? slug : "";
    if (newSlugHint) {
      newSlugHint.textContent = label
        ? t("settings.boards.new.slug", { id: slug })
        : t("settings.boards.new.slug_empty");
    }
  }

  newName?.addEventListener("input", () => {
    syncSlug();
    if (newName.value.trim()) setFieldError(newName, "");
  });
  newIdField?.addEventListener("input", () => {
    idTouched = newIdField.value.trim() !== "";
    syncSlug();
  });
  byId("bs-new")?.addEventListener("click", (e) => openNewBoard(e.currentTarget as HTMLElement));

  newForm?.addEventListener("submit", (e) => {
    e.preventDefault();
    void createBoard();
  });

  async function createBoard() {
    if (!newName || !newSubmit || !newErr) return;
    const label = newName.value.trim();
    if (!label) {
      setFieldError(newName, t("settings.boards.new.name_required"), { focus: true });
      return;
    }
    const id = newSlug();
    if (!validId(id)) {
      setFormError(newErr, t("settings.boards.new.id_invalid", { id }));
      newIdField?.focus();
      return;
    }
    setFormError(newErr, "");
    newSubmit.disabled = true;
    const idle = newSubmit.textContent;
    newSubmit.textContent = t("settings.boards.new.creating");
    try {
      await api.post("/boards", {
        id,
        label,
        description: orUndefined(newDesc?.value.trim()),
        clone_from: orUndefined(newFrom?.value),
      });
      newModal.close();
      document.dispatchEvent(new CustomEvent("octopus:boards-changed"));
      toast(t("settings.boards.toasts.created", { board: label }), "ok");
      void navigate(boardSettingsURL(id));
    } catch (err) {
      setFormError(newErr, errText(err));
    } finally {
      newSubmit.disabled = false;
      newSubmit.textContent = idle;
    }
  }

  async function deleteBoard(id: string, trigger: HTMLElement | null, knownCount?: number) {
    const b = boards.find((x) => x.id === id);
    if (!b) return;
    const count = knownCount ?? b.task_count ?? 0;
    const bodyHTML = moveTasksBodyHTML({
      count,
      destinations: boards.filter((x) => x.id !== id),
      label: t("settings.boards.delete.move_to", { count }),
      id: "bs-delete-to",
    });
    let regrown = 0;
    const { ok } = await confirmDialog({
      title: t("settings.boards.delete.title", { board: b.label }),
      message: count
        ? t("settings.boards.delete.with_tasks", { count })
        : t("settings.boards.delete.empty"),
      bodyHTML,
      notes: bodyHTML
        ? [t("settings.boards.delete.move_note"), t("settings.boards.delete.lost")]
        : [t("settings.boards.delete.lost")],
      confirmLabel: t("settings.boards.delete.confirm"),
      danger: true,
      action: async (values) => {
        const moveTo = typeof values.move_to === "string" ? values.move_to : "";
        try {
          await api.del(
            `/boards/${encodeURIComponent(id)}${moveTo ? `?move_to=${encodeURIComponent(moveTo)}` : ""}`,
          );
        } catch (e) {
          regrown = pendingTaskCount(e);
          if (!regrown)
            throw new Error(t("settings.boards.errors.delete", { error: errText(e) }), {
              cause: e,
            });
        }
      },
    });
    if (!ok) return;
    if (regrown) {
      void deleteBoard(id, trigger, regrown);
      return;
    }
    byId("bs-new")?.focus();
    document.dispatchEvent(new CustomEvent("octopus:boards-changed"));
    toast(t("settings.boards.toasts.deleted", { board: b.label }), "ok");
    if (detailID) void navigate(boardSettingsURL());
    else {
      boards = boards.filter((x) => x.id !== id);
      renderCards();
    }
  }

  let board: Board | undefined;
  let colEd: ColumnEditor | null = null;
  let taskTypes: TaskType[] = [];
  let typeIcons: string[] = [];
  let agents: Array<{ name: string; type?: string; valid_stages?: string[] }> = [];
  let projects: TemplateProject[] = [];
  const agentPicker = projectAgentPicker({
    board: () => board?.id ?? "",
    defaultBoard: () => boards.find((b) => b.default)?.id ?? "",
    projects: () => projects,
    setProjects: (list) => (projects = list),
    agents: () => agents,
    onChange: () => {
      if (colEd) renderColumns(colEd.editor.stages);
      colEd?.sync();
    },
  });
  const taskCounts = new Map<string, number>();
  const detailErr = byId("bs-detail-error");
  const columnsEl = byId("bs-columns");

  async function loadDetail(id: string) {
    try {
      await fetchBoards();
      board = boards.find((b) => b.id === id);
      if (!board) {
        if (detailErr) {
          detailErr.textContent = t("settings.boards.errors.missing", { board: id });
          detailErr.hidden = false;
        }
        return;
      }
      const [stages, typesResp, tasks, agentList, icons, projectList] = await Promise.all([
        api.get<Stage[]>(`/boards/${encodeURIComponent(id)}/stages`),
        api.get<{ task_types: TaskType[] | null }>(`/boards/${encodeURIComponent(id)}/types`),
        api.get<Array<{ stage: string }>>(`/tasks?board=${encodeURIComponent(id)}`).catch(() => []),
        api.get<Array<{ name: string; valid_stages?: string[] }>>("/types").catch(() => []),
        api.get<{ icons: string[] }>("/task-types/icons").catch(() => ({ icons: [] })),
        api.get<TemplateProject[]>("/projects").catch(() => [] as TemplateProject[]),
      ]);
      projects = projectList;
      taskTypes = (typesResp.task_types ?? []).map(withSkips);
      typeIcons = icons.icons;
      agents = agentList;
      taskCounts.clear();
      for (const task of tasks) taskCounts.set(task.stage, (taskCounts.get(task.stage) ?? 0) + 1);
      if (detailErr) detailErr.hidden = true;
      renderDetailHead();
      mountColumns(stages);
      renderTypes();
    } catch (e) {
      if (detailErr) {
        detailErr.textContent = t("settings.boards.errors.detail", { error: errText(e) });
        detailErr.hidden = false;
      }
    }
  }

  const nameInput = byId("bs-name") as HTMLInputElement | null;
  const descInput = byId("bs-desc") as HTMLTextAreaElement | null;
  const headStatus = byId("bs-head-status");

  function renderDetailHead() {
    if (!board) return;
    const b = boards.find((x) => x.id === board?.id) ?? board;
    board = b;
    document.title = t("settings.boards.detail.document_title", { board: b.label });
    setText(byId("bs-detail-title"), b.label);
    const badge = byId("bs-default-badge");
    if (badge) badge.hidden = !b.default;
    const makeDef = byId("bs-make-default") as HTMLButtonElement | null;
    if (makeDef) makeDef.hidden = Boolean(b.default) || !canEdit;
    const open = byId("bs-open-board") as HTMLAnchorElement | null;
    if (open) open.href = `/tasks/?board=${encodeURIComponent(b.id)}`;
    setText(byId("bs-detail-meta"), boardMeta(b));
    if (nameInput && document.activeElement !== nameInput) nameInput.value = b.label;
    if (descInput && document.activeElement !== descInput) descInput.value = b.description ?? "";
    if (nameInput) nameInput.readOnly = !canEdit;
    if (descInput) descInput.readOnly = !canEdit;
    const more = byId("bs-detail-more");
    if (more) more.hidden = !canEdit;
  }

  byId("bs-make-default")?.addEventListener("click", (e) => {
    if (board) void makeDefault(board.id, e.currentTarget as HTMLElement);
  });

  async function saveField(
    field: "label" | "description",
    input: HTMLInputElement | HTMLTextAreaElement,
  ) {
    if (!board || !canEdit) return;
    const value = input.value.trim();
    const current = field === "label" ? board.label : (board.description ?? "");
    if (value === current) return;
    if (field === "label" && !value) {
      setFieldError(input, t("settings.boards.detail.name_required"));
      return;
    }
    setFieldError(input, "");
    setText(headStatus, t("settings.boards.detail.saving"));
    try {
      await api.patch(`/boards/${encodeURIComponent(board.id)}`, { [field]: value });
      const id = board.id;
      boards = boards.map((b) => (b.id === id ? { ...b, [field]: value } : b));
      renderDetailHead();
      setText(headStatus, t("settings.boards.detail.saved"));
      if (field === "label") document.dispatchEvent(new CustomEvent("octopus:boards-changed"));
    } catch (e) {
      setText(headStatus, "");
      setFieldError(input, errText(e));
    }
  }

  nameInput?.addEventListener("change", () => void saveField("label", nameInput));
  descInput?.addEventListener("change", () => void saveField("description", descInput));
  nameInput?.addEventListener("keydown", (e) => {
    if (e.key === "Enter") {
      e.preventDefault();
      nameInput.blur();
    }
  });

  function agentLineHTML(st: Stage): string {
    const state = agentPicker.state(st.id);
    const text = columnAgentText(st, state);
    if (!text) return "";
    const warn = state.with === 0;
    return `<p class="bs-col-agent${warn ? " is-warn" : ""}">${icoSvg(warn ? "i-alert" : "i-bot")}<span>${esc(text)}</span></p>`;
  }

  function columnBodyHTML(st: Stage): string {
    const chips = behaviorChips(st);
    const count = taskCounts.get(st.id) ?? 0;
    const waits = waitsForPerson(agentPicker.state(st.id));
    return `<div class="bs-col-body">
        ${agentLineHTML(st)}
        ${
          chips.length
            ? `<ul class="bs-col-chips">${chips.map((c) => `<li>${tagHTML(c, { size: "sm" })}</li>`).join("")}</ul>`
            : waits
              ? emptyStateHTML({ size: "inline", title: t("settings.boards.chips.none") })
              : ""
        }
        <div class="bs-col-foot">
          <span class="bs-col-count">${esc(t("settings.boards.card.tasks", { count }))}</span>
          ${buttonHTML({ label: t("settings.boards.detail.edit_behavior"), variant: "ghost", size: "sm", cls: "bs-col-edit", attrs: { "data-bs-behavior": true } })}
        </div>
      </div>`;
  }

  const colSig = (s: Stage) =>
    JSON.stringify(s) + (taskCounts.get(s.id) ?? 0) + JSON.stringify(agentPicker.state(s.id));

  function renderColumns(stages: Stage[]) {
    if (!columnsEl || !board) return;
    reconcile<Stage>({
      container: columnsEl,
      items: stages,
      key: (s) => s.id,
      create: (s) => {
        const col = document.createElement("div");
        col.className = cardClasses({ cls: "col bs-col" });
        col.dataset.stage = s.id;
        col.innerHTML = columnHeadHTML(s) + columnBodyHTML(s);
        col.dataset.sig = colSig(s);
        return col;
      },
      update: (col, s) => {
        const sig = colSig(s);
        if (col.dataset.sig === sig) return;
        col.dataset.sig = sig;
        setText(col.querySelector(".col-title"), s.label);
        const body = col.querySelector(".bs-col-body");
        if (body) body.outerHTML = columnBodyHTML(s);
      },
      skip: (el) => el.classList.contains("ce-renaming"),
    });
    const count = byId("bs-cols-count");
    if (count) setCount(count, stages.length, { pill: true });
  }

  function mountColumns(stages: Stage[]) {
    if (!columnsEl || !board) return;
    renderColumns(stages);
    colEd = mountColumnEditor({
      root: columnsEl,
      board: board.id,
      stages,
      signal,
      canEdit,
      editing: true,
      columnClass: cardClasses(),
      render: renderColumns,
      taskCount: (id) => taskCounts.get(id) ?? 0,
      agentPicker,
      taskTypes: () => taskTypes.map((tt) => ({ id: tt.id, label: tt.label })),
      toast,
      warnings: byId("bs-warnings"),
      onSaved: (res) => {
        if (res.kind === "create" || res.kind === "remove") void refreshCounts();
        if (res.kind !== "patch") renderTypes();
      },
    });
    columnsEl.addEventListener("click", (e) => {
      const btn = (e.target as HTMLElement).closest<HTMLElement>("[data-bs-behavior]");
      const id = btn?.closest<HTMLElement>(".col")?.dataset.stage;
      if (btn && id) colEd?.openBehavior(id, btn);
    });
  }

  async function refreshCounts() {
    if (!board) return;
    try {
      const id = board.id;
      const [resp, tasks] = await Promise.all([
        api.get<BoardsResponse>("/boards"),
        api.get<Array<{ stage: string }>>(`/tasks?board=${encodeURIComponent(id)}`),
      ]);
      boards = resp.boards;
      taskCounts.clear();
      for (const task of tasks) taskCounts.set(task.stage, (taskCounts.get(task.stage) ?? 0) + 1);
      renderDetailHead();
      if (colEd) renderColumns(colEd.editor.stages);
      colEd?.sync();
    } catch {
      // Counts are a nicety; the columns themselves are already right.
    }
  }

  const typesEl = byId("bs-types");
  const typesStatus = byId("bs-types-status");
  const typesErr = byId("bs-types-error");
  const typesFix = byId("bs-types-fix");
  let saveTimer = 0;
  let saving: Promise<void> | null = null;
  let openPop: { type: string; kind: "icon" | "color" } | null = null;

  const stageList = () => colEd?.editor.stages ?? [];

  function typeIconName(icon: string): string {
    return TYPE_ICON_TEXT[icon] ?? icon;
  }

  function ttIcon(icon: string | undefined): string {
    return icon ? icoSvg(`tt-${icon}`) : `<span class="tt-dot" aria-hidden="true"></span>`;
  }

  function typeRowHTML(tt: TaskType): string {
    const count = Number(tt.task_count) || 0;
    const walk = stageList()
      .map((st) => {
        const locked = isLockedStage(st);
        const on = locked || !tt.skips.includes(st.id);
        const why = locked
          ? st.initial
            ? t("settings.boards.types.locked_initial", { stage: st.label })
            : t("settings.boards.types.locked_terminal", { stage: st.label })
          : "";
        const dot = dotHTML("neutral", {
          attrs: st.color ? { style: `--dot-color:${st.color}` } : undefined,
        });
        return checkHTML({
          cls: "bs-walk",
          labelHTML: `${dot} ${esc(st.label)}`,
          checked: on,
          disabled: locked,
          inputAttrs: { "data-walk": st.id },
          attrs: why ? { title: why } : undefined,
        });
      })
      .join("");
    const pop =
      openPop?.type === tt.id
        ? openPop.kind === "icon"
          ? swatchPickerHTML({
              kind: "glyph",
              boxed: true,
              label: t("settings.boards.types.icon_label", { type: tt.label }),
              items: typeIcons.map((icon) => ({
                value: icon,
                label: typeIconName(icon),
                icon: `tt-${icon}`,
                attrs: { "data-icon": icon },
              })),
              selected: tt.icon,
            })
          : swatchPickerHTML({
              boxed: true,
              label: t("settings.boards.types.color_label", { type: tt.label }),
              items: stageColorSwatches(),
              selected: normalizeHex(tt.color ?? ""),
            })
        : "";
    return `<div class="bs-type" data-type="${esc(tt.id)}">
        <div class="bs-type-main">
          <button type="button" class="bs-type-icon" data-act="icon" aria-expanded="${openPop?.type === tt.id && openPop.kind === "icon"}"
            aria-label="${esc(t("settings.boards.types.icon_label", { type: tt.label }))}" style="--tt-color:${esc(tt.color ?? "")}">${ttIcon(tt.icon)}</button>
          <input class="bs-type-name" data-field="label" value="${esc(tt.label)}" maxlength="64"
            aria-label="${esc(t("settings.boards.types.name_label", { type: tt.label }))}" />
          <button type="button" class="bs-type-color" data-act="color" aria-expanded="${openPop?.type === tt.id && openPop.kind === "color"}"
            aria-label="${esc(t("settings.boards.types.color_label", { type: tt.label }))}" style="--sw:${esc(tt.color ?? "")}"></button>
          ${checkHTML({
            type: "radio",
            name: "bs-tt-default",
            label: t("settings.boards.types.default"),
            checked: !!tt.default,
            cls: "bs-type-default",
            inputAttrs: { "data-field": "default" },
          })}
          ${count ? `<span class="bs-type-count">${esc(t("settings.boards.card.tasks", { count }))}</span>` : ""}
          ${buttonHTML({ label: t("settings.boards.types.remove_label", { type: tt.label }), icon: "trash", iconOnly: true, size: "lg", variant: "danger", attrs: { "data-act": "remove" } })}
        </div>
        ${pop}
        <fieldset class="bs-walks">
          <legend>${esc(t("settings.boards.types.walks"))}</legend>
          ${walk}
        </fieldset>
      </div>`;
  }

  function renderTypes() {
    if (!typesEl) return;
    const focused = document.activeElement as HTMLElement | null;
    const focusKey = focused?.closest<HTMLElement>("[data-type]")?.dataset.type;
    const focusSel = focused?.dataset.field
      ? `[data-field="${focused.dataset.field}"]`
      : focused?.dataset.act
        ? `[data-act="${focused.dataset.act}"]`
        : focused?.dataset.walk
          ? `[data-walk="${focused.dataset.walk}"]`
          : "";
    typesEl.innerHTML = taskTypes.length
      ? taskTypes.map(typeRowHTML).join("")
      : emptyRowHTML(t("settings.boards.types.none", { count: stageList().length }), { as: "div" });
    if (focusKey && focusSel) {
      typesEl
        .querySelector<HTMLElement>(`[data-type="${CSS.escape(focusKey)}"] ${focusSel}`)
        ?.focus();
    }
    const tpl = byId("bs-type-templates");
    if (tpl) {
      const free = TYPE_TEMPLATES.filter((x) => !taskTypes.some((tt) => tt.id === x.id));
      tpl.hidden = free.length === 0 || !canEdit;
      const list = tpl.querySelector(".bs-templates-list");
      if (list) {
        list.innerHTML = free
          .map((x) =>
            buttonHTML({
              label: x.label,
              icon: `tt-${x.icon}`,
              variant: "ghost",
              size: "sm",
              attrs: { "data-template": x.id },
            }),
          )
          .join("");
      }
    }
  }

  const typeOf = (el: HTMLElement) => {
    const id = el.closest<HTMLElement>("[data-type]")?.dataset.type;
    return taskTypes.find((tt) => tt.id === id);
  };

  typesEl?.addEventListener("change", (e) => {
    const input = e.target as HTMLInputElement;
    const tt = typeOf(input);
    if (!tt || !canEdit) return;
    if (input.dataset.field === "label") {
      const v = input.value.trim();
      if (!v) {
        input.value = tt.label;
        return;
      }
      tt.label = v;
    } else if (input.dataset.field === "default") {
      taskTypes.forEach((x) => (x.default = x.id === tt.id));
    } else if (input.dataset.walk) {
      tt.skips = toggleSkip(tt.skips, input.dataset.walk, input.checked);
    } else return;
    queueSave();
  });

  typesEl?.addEventListener("click", (e) => {
    const target = e.target as HTMLElement;
    const tt = typeOf(target);
    if (!tt || !canEdit) return;
    const act = target.closest<HTMLElement>("[data-act]")?.dataset.act;
    const icon = target.closest<HTMLElement>("[data-icon]")?.dataset.icon;
    const swatch = target.closest<HTMLElement>("[data-swatch]")?.dataset.swatch;
    if (icon) {
      tt.icon = icon;
      openPop = null;
      renderTypes();
      typesEl
        .querySelector<HTMLElement>(`[data-type="${CSS.escape(tt.id)}"] [data-act="icon"]`)
        ?.focus();
      queueSave();
    } else if (swatch) {
      tt.color = swatch;
      openPop = null;
      renderTypes();
      typesEl
        .querySelector<HTMLElement>(`[data-type="${CSS.escape(tt.id)}"] [data-act="color"]`)
        ?.focus();
      queueSave();
    } else if (act === "icon" || act === "color") {
      openPop = openPop?.type === tt.id && openPop.kind === act ? null : { type: tt.id, kind: act };
      renderTypes();
      typesEl
        .querySelector<HTMLElement>(
          `[data-type="${CSS.escape(tt.id)}"] .swatch-picker .swatch[tabindex="0"]`,
        )
        ?.focus();
    } else if (act === "remove") {
      void removeType(tt);
    }
  });

  typesEl?.addEventListener("keydown", (e) => {
    const target = e.target as HTMLElement;
    const pop = target.closest<HTMLElement>(".swatch-picker");
    if (e.key === "Escape" && pop) {
      e.preventDefault();
      e.stopPropagation();
      const tt = typeOf(target);
      const kind = openPop?.kind;
      openPop = null;
      renderTypes();
      if (tt && kind)
        typesEl
          .querySelector<HTMLElement>(`[data-type="${CSS.escape(tt.id)}"] [data-act="${kind}"]`)
          ?.focus();
      return;
    }
    if (pop && moveSwatchFocus(e)) return;
    if (target.dataset.field === "label" && e.key === "Enter") {
      e.preventDefault();
      target.blur();
    }
  });

  async function removeType(tt: TaskType) {
    const ok = await confirmDialog({
      title: t("settings.boards.types.remove_title", { type: tt.label }),
      message: t("settings.boards.types.remove_message"),
      confirmLabel: t("settings.boards.types.remove_confirm"),
      danger: true,
    });
    if (!ok) return;
    taskTypes = taskTypes.filter((x) => x.id !== tt.id);
    renderTypes();
    queueSave(0);
  }

  function queueSave(delay = 450) {
    window.clearTimeout(saveTimer);
    setText(typesStatus, t("settings.boards.types.pending"));
    saveTimer = window.setTimeout(() => void saveTypes(""), delay);
  }

  async function saveTypes(query: string) {
    if (!board) return;
    if (saving) await saving;
    const id = board.id;
    const run = (async () => {
      setText(typesStatus, t("settings.boards.detail.saving"));
      try {
        const resp = await api.put<{ task_types: TaskType[] | null; moved_tasks: number }>(
          `/boards/${encodeURIComponent(id)}/types${query}`,
          { types: taskTypes },
        );
        taskTypes = (resp.task_types ?? []).map(withSkips);
        if (typesErr) typesErr.hidden = true;
        if (typesFix) typesFix.hidden = true;
        renderTypes();
        setText(
          typesStatus,
          resp.moved_tasks
            ? t("settings.boards.types.saved_moved", { count: resp.moved_tasks })
            : t("settings.boards.detail.saved"),
        );
      } catch (e) {
        setText(typesStatus, "");
        if (!offerTypesFix(e)) {
          if (typesErr) {
            typesErr.textContent = t("settings.boards.errors.types", { error: errText(e) });
            typesErr.hidden = false;
          }
        }
      }
    })();
    saving = run;
    await run;
    saving = null;
  }

  function offerTypesFix(e: unknown): boolean {
    if (!(e instanceof ApiError) || !typesFix) return false;
    const body = e.body as
      | { task_type?: string; task_count?: number; stage?: string; available_stages?: Stage[] }
      | undefined;
    const count = Number(body?.task_count) || 0;
    if (!count) return false;
    let message: string;
    let dests: MoveDestination[];
    let param: string;
    if (body?.stage) {
      const stage = stageList().find((s) => s.id === body.stage)?.label ?? body.stage;
      message = t("settings.boards.types.fix_skipped", { count, stage });
      dests = body.available_stages ?? [];
      param = "move_to_stage";
    } else {
      const type = body?.task_type ?? "";
      message = t("settings.boards.types.fix_removed", { count, type });
      dests = [{ id: "", label: t("settings.boards.types.untyped") }, ...taskTypes];
      param = "move_to";
    }
    const fixBody = byId("bs-types-fix-body") ?? typesFix;
    fixBody.innerHTML = `<p>${esc(message)}</p>${fieldHTML({
      label: t("settings.boards.types.fix_to"),
      for: "bs-types-fix-to",
      control: `<div class="bs-fix-row">
        <select id="bs-types-fix-to">${destinationOptions(dests)}</select>
        ${buttonHTML({ label: t("settings.boards.types.fix_go"), variant: "primary", size: "sm", attrs: { "data-fix": "go" } })}
        ${buttonHTML({ label: t("settings.boards.types.fix_undo"), variant: "ghost", size: "sm", attrs: { "data-fix": "undo" } })}
      </div>`,
    })}`;
    typesFix.hidden = false;
    typesFix.querySelector<HTMLButtonElement>('[data-fix="go"]')?.addEventListener("click", () => {
      const to = typesFix.querySelector<HTMLSelectElement>("select")?.value ?? "";
      void saveTypes(`?${param}=${encodeURIComponent(to)}`);
    });
    typesFix
      .querySelector<HTMLButtonElement>('[data-fix="undo"]')
      ?.addEventListener("click", () => {
        typesFix.hidden = true;
        void reloadTypes();
      });
    typesFix.querySelector<HTMLElement>("select")?.focus();
    return true;
  }

  async function reloadTypes() {
    if (!board) return;
    try {
      const resp = await api.get<{ task_types: TaskType[] | null }>(
        `/boards/${encodeURIComponent(board.id)}/types`,
      );
      taskTypes = (resp.task_types ?? []).map(withSkips);
      renderTypes();
      setText(typesStatus, "");
    } catch (e) {
      if (typesErr) {
        typesErr.textContent = t("settings.boards.errors.types", { error: errText(e) });
        typesErr.hidden = false;
      }
    }
  }

  const addForm = byId("bs-type-add") as HTMLFormElement | null;
  const addName = byId("bs-type-name") as HTMLInputElement | null;
  const addHint = byId("bs-type-slug");

  function typeSlug(label: string): string {
    return slugify(
      label,
      taskTypes.map((tt) => tt.id),
      "type",
    );
  }

  addName?.addEventListener("input", () => {
    const label = addName.value.trim();
    if (addHint) {
      addHint.textContent = label
        ? t("settings.boards.types.slug", { id: typeSlug(label) })
        : t("settings.boards.types.slug_empty");
    }
    if (label) setFieldError(addName, "");
  });

  function addType(tpl: { id: string; label: string; icon?: string; color?: string }) {
    taskTypes.push(newType(tpl, taskTypes.length));
    renderTypes();
    queueSave(0);
  }

  addForm?.addEventListener("submit", (e) => {
    e.preventDefault();
    if (!addName) return;
    const label = addName.value.trim();
    if (!label) {
      setFieldError(addName, t("settings.boards.types.name_required"), { focus: true });
      return;
    }
    addType({ id: typeSlug(label), label, color: nextColor(taskTypes) });
    addName.value = "";
    if (addHint) addHint.textContent = t("settings.boards.types.slug_empty");
    addName.focus();
  });

  byId("bs-type-templates")?.addEventListener("click", (e) => {
    const id = (e.target as HTMLElement).closest<HTMLElement>("[data-template]")?.dataset.template;
    const tpl = TYPE_TEMPLATES.find((x) => x.id === id);
    if (tpl && !taskTypes.some((tt) => tt.id === tpl.id)) addType({ ...tpl });
  });

  signal.addEventListener("abort", () => {
    if (saveTimer) {
      window.clearTimeout(saveTimer);
      if (typesStatus?.textContent === t("settings.boards.types.pending")) void saveTypes("");
    }
  });

  // Settings has no initKeyboard() cascade, so this section closes its own layers on Escape,
  // topmost first.
  document.addEventListener(
    "keydown",
    (e) => {
      if (e.key !== "Escape" || !showsBoards()) return;
      // bindMenu handles Escape for an open board menu before this listener runs.
      let closed = colEd?.escape() ?? false;
      if (!closed && newModal.isOpen()) {
        newModal.close();
        closed = true;
      }
      if (closed) e.preventDefault();
    },
    { signal },
  );
}

// The daemon sends `skips: null` for a type that skips nothing.
function withSkips(tt: TaskType): TaskType {
  return { ...tt, skips: Array.isArray(tt.skips) ? tt.skips : [] };
}

function orUndefined(v: string | undefined): string | undefined {
  return v === "" ? undefined : v;
}

function errText(e: unknown): string {
  return e instanceof Error && e.message ? e.message : t("settings.boards.errors.network");
}

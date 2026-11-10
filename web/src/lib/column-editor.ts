import { t } from "../i18n";
import { ALT_KEY, altKeyWord } from "./shortcuts";
import {
  BoardEditor,
  FLAG_KEYS,
  defaultInsertIndex,
  STAGE_COLORS,
  WIP_GROUPS,
  errorText,
  flagState,
  flowWarningText,
  flowWarnings,
  normalizeHex,
  slugify,
  RESERVED_STAGE_IDS,
  type EditResult,
  type FlagKey,
  type FlagState,
  type SwatchName,
  type WipGroup,
} from "./board-editor";
import { MIXED, pickerValue, takesAgent, type ColumnAgentPicker } from "./column-agent";
import { autoScrollDelta } from "./dnd";
import { bindModal } from "./modal";
import { pendingTaskCount } from "./move-picker";
import {
  approvalKeyFromLabel,
  approvalRowEl,
  approvalsEditorHTML,
  approvalsPatch,
  normalizeRequiredApprovals,
  onChangesOf,
  type ApprovalsPatch,
} from "./required-approvals";
import type { Stage } from "./stages";
import {
  bindMenu,
  buttonHTML,
  calloutHTML,
  checkGroupHTML,
  checkHTML,
  checkSwatch,
  confirmDialog,
  esc,
  fieldErrorHTML,
  fieldHTML,
  icoSvg,
  menuItemsHTML,
  moveTasksBodyHTML,
  segmentedHTML,
  setFieldError,
  swatchPickerHTML,
  type MenuEntry,
  type MenuHandle,
  type SwatchItem,
  type ToastKind,
} from "./ui";

export interface ColumnEditorOpts {
  root: HTMLElement;
  board: string;
  stages: Stage[];
  signal: AbortSignal;
  canEdit: boolean;
  editing?: boolean;
  columnClass?: string;
  render: (stages: Stage[]) => void;
  taskCount: (stage: string) => number;
  agentPicker?: ColumnAgentPicker;
  taskTypes?: () => Array<{ id: string; label: string }>;
  toast: (msg: string, kind?: ToastKind, ms?: number) => void;
  warnings?: HTMLElement | null;
  onSaved?: (result: EditResult) => void;
}

export interface ColumnEditor {
  editor: BoardEditor;
  sync(): void;
  setEditing(on: boolean): void;
  readonly editing: boolean;
  reset(board: string, stages: Stage[]): void;
  openBehavior(id: string, trigger: HTMLElement | null): void;
  escape(): boolean;
}

const FLAG_TEXT: Record<FlagKey, { name: string; desc: string }> = {
  initial: {
    name: t("lib.board_editor.flags.initial"),
    desc: t("lib.board_editor.flags.initial_desc"),
  },
  terminal: {
    name: t("lib.board_editor.flags.terminal"),
    desc: t("lib.board_editor.flags.terminal_desc"),
  },
  auto_advance: {
    name: t("lib.board_editor.flags.auto_advance"),
    desc: t("lib.board_editor.flags.auto_advance_desc"),
  },
  push_branch: {
    name: t("lib.board_editor.flags.push_branch"),
    desc: t("lib.board_editor.flags.push_branch_desc"),
  },
  open_pr: {
    name: t("lib.board_editor.flags.open_pr"),
    desc: t("lib.board_editor.flags.open_pr_desc"),
  },
};

export function flagCheckHTML(flag: FlagKey, s: FlagState): string {
  const id = `ce-flag-${flag}`;
  return checkHTML({
    id,
    label: FLAG_TEXT[flag].name,
    hint: FLAG_TEXT[flag].desc,
    checked: s.checked,
    disabled: s.disabled,
    reason: s.disabled && s.reason ? s.reason : undefined,
    errorId: `${id}-error`,
    inputAttrs: { "data-flag": flag },
    attrs: { "data-flag-row": flag },
  });
}

export function legacyFlagCheckHTML(): string {
  return checkHTML({
    id: "ce-flag-legacy",
    label: t("lib.board_editor.flags.legacy"),
    hint: t("lib.board_editor.flags.legacy_desc"),
    checked: true,
    errorId: "ce-flag-legacy-error",
    inputAttrs: { "data-legacy": "" },
    attrs: { "data-flag-row": "blocked_by_flags" },
  });
}

const WIP_TEXT: Record<WipGroup, string> = {
  "": t("lib.board_editor.wip.none"),
  work: t("lib.board_editor.wip.work"),
  review: t("lib.board_editor.wip.review"),
};

const SWATCH_TEXT: Record<SwatchName, string> = {
  slate: t("lib.board_editor.colors.slate"),
  orange: t("lib.board_editor.colors.orange"),
  green: t("lib.board_editor.colors.green"),
  blue: t("lib.board_editor.colors.blue"),
  cyan: t("lib.board_editor.colors.cyan"),
  purple: t("lib.board_editor.colors.purple"),
  pink: t("lib.board_editor.colors.pink"),
  red: t("lib.board_editor.colors.red"),
  yellow: t("lib.board_editor.colors.yellow"),
  gray: t("lib.board_editor.colors.gray"),
};

function failedText(res: EditResult): string {
  const error = errorText(res.error);
  switch (res.kind) {
    case "reorder":
      return t("lib.board_editor.errors.reorder", { error });
    case "patch":
      return t("lib.board_editor.errors.patch", { error });
    case "create":
      return t("lib.board_editor.errors.create", { error });
    case "remove":
      return t("lib.board_editor.errors.remove", { error });
  }
}

export function stageColorSwatches(): SwatchItem[] {
  return STAGE_COLORS.map((c) => ({
    value: c.hex,
    label: SWATCH_TEXT[c.name],
    attrs: { "data-swatch": c.hex },
  }));
}

function behaviorSummary(st: Stage): string {
  const on: string[] = [];
  if (st.initial) on.push(t("lib.board_editor.summary.initial"));
  if (st.terminal) on.push(t("lib.board_editor.summary.terminal"));
  if (st.auto_advance) on.push(t("lib.board_editor.summary.auto_advance"));
  if (st.open_pr) on.push(t("lib.board_editor.summary.open_pr"));
  return on.slice(0, 2).join(", ");
}

export function columnMenuEntries(st: Stage, stages: Stage[]): MenuEntry[] {
  const i = stages.findIndex((s) => s.id === st.id);
  const act = (a: string) => ({ "data-act": a });
  const summary = behaviorSummary(st);
  const color = t("lib.board_editor.menu.color");
  return [
    { title: t("lib.board_editor.menu.rename"), icon: "i-edit", attrs: act("rename") },
    { head: color },
    {
      slot: "colors",
      html: swatchPickerHTML({
        label: color,
        items: stageColorSwatches(),
        selected: normalizeHex(st.color || ""),
        role: "menuitemradio",
        keepOpen: true,
      }),
    },
    { sep: true },
    {
      title: t("lib.board_editor.menu.behavior"),
      icon: "i-settings",
      hint: summary || undefined,
      attrs: act("behavior"),
    },
    {
      title: t("lib.board_editor.menu.approvals"),
      icon: "i-check",
      hint: String((st.required_approvals ?? []).length),
      attrs: act("approvals"),
    },
    { sep: true },
    {
      title: t("lib.board_editor.menu.move_left"),
      indent: true,
      kbd: [ALT_KEY, "←"],
      disabled: i <= 0,
      attrs: act("left"),
    },
    {
      title: t("lib.board_editor.menu.move_right"),
      indent: true,
      kbd: [ALT_KEY, "→"],
      disabled: i >= stages.length - 1,
      attrs: act("right"),
    },
    { title: t("lib.board_editor.menu.add_before"), icon: "i-plus", attrs: act("add-before") },
    { title: t("lib.board_editor.menu.add_after"), icon: "i-plus", attrs: act("add-after") },
    { sep: true },
    {
      title: t("lib.board_editor.menu.delete"),
      icon: "i-trash",
      danger: true,
      disabled: st.initial,
      sub: st.initial ? t("lib.board_editor.menu.delete_initial") : undefined,
      attrs: act("delete"),
    },
  ];
}

const reducedMotion = () =>
  // i18n-ignore: a media query
  typeof window !== "undefined" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;

const byId = (id: string) => document.getElementById(id);

export function mountColumnEditor(opts: ColumnEditorOpts): ColumnEditor {
  const { root, signal } = opts;
  let editing = opts.editing ?? false;
  const live = byId("ce-live");
  const menu = byId("ce-menu");
  const gripHint = byId("ce-grip-hint");
  if (gripHint) gripHint.textContent = t("lib.board_editor.head.grip_hint", { alt: altKeyWord() });

  const announce = (msg: string) => {
    if (!live) return;
    live.textContent = "";
    // Clear, then set after 30ms: screen readers do not re-announce identical live-region text.
    window.setTimeout(() => (live.textContent = msg), 30);
  };

  const editor = new BoardEditor({
    board: opts.board,
    stages: opts.stages,
    onChange: (stages) => draw(stages),
    onError: (res) => {
      if (!showDialogError(res)) opts.toast(failedText(res), "err", 6000);
    },
  });

  const stageOf = (id: string) => editor.stages.find((s) => s.id === id);
  const colOf = (id: string) =>
    root.querySelector<HTMLElement>(`.col[data-stage="${CSS.escape(id)}"]`);
  const columns = () => Array.from(root.querySelectorAll<HTMLElement>(".col[data-stage]"));

  function draw(stages: Stage[]) {
    const active = document.activeElement as HTMLElement | null;
    opts.render(stages);
    sync();
    if (active && active !== document.activeElement && active.isConnected) {
      active.focus({ preventScroll: true });
    }
  }

  async function track(p: Promise<EditResult> | null, ok?: string) {
    if (!p) return;
    const res = await p;
    if (!res.ok) return;
    opts.onSaved?.(res);
    if (ok) announce(ok);
  }

  const colClass = (role: string) => [opts.columnClass, "col", role].filter(Boolean).join(" ");

  let ghost: HTMLElement | null = null;
  function ensureGhost() {
    if (!opts.canEdit) return;
    if (!ghost) {
      ghost = document.createElement("div");
      ghost.className = colClass("col-ghost");
      ghost.innerHTML = `<button type="button" class="col-ghost-btn" data-ce-add-end>
          ${icoSvg("i-plus")}<span>${esc(t("lib.board_editor.add.button"))}</span>
        </button>`;
    }
    ghost.hidden = !editing;
    if (root.lastElementChild !== ghost) root.appendChild(ghost);
  }

  function sync() {
    root.toggleAttribute("data-can-edit", opts.canEdit);
    root.toggleAttribute("data-editing", opts.canEdit && editing);
    const stages = editor.stages;
    const warns = flowWarnings(stages);
    const warned = new Set(warns.map((w) => w.stage));
    for (const col of columns()) {
      const id = col.dataset.stage ?? "";
      const st = stages.find((s) => s.id === id);
      if (!st) continue;
      col.style.setProperty("--col-color", st.color || "");
      col.toggleAttribute("data-flow-warning", warned.has(id));
      const grip = col.querySelector<HTMLElement>("[data-col-grip]");
      grip?.setAttribute("aria-label", t("lib.board_editor.head.move", { column: st.label }));
      grip?.setAttribute("aria-describedby", "ce-grip-hint");
      const title = col.querySelector<HTMLElement>(".col-title");
      if (title) {
        const renamable = opts.canEdit && editing;
        title.toggleAttribute("data-ce-rename", renamable);
        if (renamable) {
          title.tabIndex = 0;
          title.setAttribute("role", "button");
          title.setAttribute("aria-label", t("lib.board_editor.head.rename", { column: st.label }));
          title.title = t("lib.board_editor.head.rename_title");
        } else if (title.hasAttribute("role")) {
          title.removeAttribute("tabindex");
          title.removeAttribute("role");
          title.removeAttribute("aria-label");
          title.removeAttribute("title");
        }
      }
      const btn = col.querySelector<HTMLElement>("[data-col-menu]");
      btn?.setAttribute("aria-label", t("lib.board_editor.head.options", { column: st.label }));
      btn?.setAttribute("title", t("lib.board_editor.head.options_title"));
    }
    ensureGhost();
    if (opts.warnings) {
      const show = opts.canEdit && warns.length > 0;
      opts.warnings.hidden = !show;
      opts.warnings.innerHTML = show
        ? calloutHTML({
            tone: "warn",
            icon: "i-warn",
            bodyHTML: warns.map((w) => `<p>${esc(flowWarningText(w, stages))}</p>`).join(""),
          })
        : "";
    }
    if (behaviorFor) refreshBehavior();
    if (menuFor && !stageOf(menuFor)) closeMenu(false);
  }

  let menuFor: string | null = null;
  let bound: { trigger: HTMLElement; handle: MenuHandle; ctl: AbortController } | null = null;
  const menuOpen = () => bound?.handle.isOpen() ?? false;

  function closeMenu(restoreFocus = true) {
    bound?.handle.close(restoreFocus);
  }

  function openMenu(id: string, trigger: HTMLElement, focus: "first" | "last" = "first") {
    if (!menu || !stageOf(id)) return;
    if (bound?.trigger !== trigger) {
      bound?.handle.close(false);
      bound?.ctl.abort();
      const ctl = new AbortController();
      signal.addEventListener("abort", () => ctl.abort(), { once: true });
      const handle = bindMenu(menu, trigger, {
        signal: ctl.signal,
        onOpen: () => {
          const st = stageOf(id);
          if (!st) return;
          menuFor = id;
          menu.innerHTML = menuItemsHTML(columnMenuEntries(st, editor.stages));
          menu.setAttribute("aria-label", t("lib.board_editor.head.options", { column: st.label }));
          colOf(id)?.classList.add("ce-menu-open");
        },
        onClose: () => {
          if (menuFor) colOf(menuFor)?.classList.remove("ce-menu-open");
          menuFor = null;
          menu.replaceChildren();
        },
        onSelect: (item) => runMenuItem(id, item, trigger),
      });
      bound = { trigger, handle, ctl };
    }
    bound.handle.open(undefined, focus);
  }

  function runMenuItem(id: string, item: HTMLElement, trigger: HTMLElement) {
    const hex = item.dataset.swatch;
    if (hex !== undefined) {
      checkSwatch(item);
      void track(editor.recolor(id, hex));
      return;
    }
    const act = item.dataset.act;
    if (!act) return;
    switch (act) {
      case "rename":
        startRename(id);
        break;
      case "behavior":
        openBehavior(id, trigger, false);
        break;
      case "approvals":
        openBehavior(id, trigger, true);
        break;
      case "left":
      case "right":
        stepColumn(id, act === "left" ? -1 : 1);
        trigger.focus();
        break;
      case "add-before":
      case "add-after": {
        const i = editor.stages.findIndex((s) => s.id === id);
        openDraft(act === "add-before" ? i : i + 1);
        break;
      }
      case "delete":
        void openDelete(id, trigger);
        break;
    }
  }

  const followMenu = () => {
    if (!bound || !menuOpen()) return;
    const r = bound.trigger.getBoundingClientRect();
    const box = root.getBoundingClientRect();
    if (r.right < box.left || r.left > box.right) closeMenu(false);
  };
  root.addEventListener("scroll", followMenu, { signal, passive: true });

  function positionText(id: string): string {
    const stages = editor.stages;
    const st = stages.find((s) => s.id === id);
    return t("lib.board_editor.move.moved", {
      column: st?.label ?? id,
      position: stages.findIndex((s) => s.id === id) + 1,
      count: stages.length,
    });
  }

  function stepColumn(id: string, delta: -1 | 1) {
    const p = editor.step(id, delta);
    if (!p) {
      announce(delta < 0 ? t("lib.board_editor.move.at_start") : t("lib.board_editor.move.at_end"));
      return;
    }
    announce(positionText(id));
    void track(p);
  }

  function startRename(id: string) {
    const col = colOf(id);
    const st = stageOf(id);
    const title = col?.querySelector<HTMLElement>(".col-title");
    if (!col || !st || !title || col.querySelector(".ce-rename")) return;
    const input = document.createElement("input");
    input.className = "ce-rename";
    input.value = st.label;
    input.maxLength = 64;
    input.setAttribute("aria-label", t("lib.board_editor.rename.label"));
    title.hidden = true;
    title.after(input);
    col.classList.add("ce-renaming");
    input.focus();
    input.select();
    let done = false;
    const finish = (save: boolean) => {
      if (done) return;
      done = true;
      const value = input.value.trim();
      input.remove();
      title.hidden = false;
      col.classList.remove("ce-renaming");
      col.querySelector<HTMLElement>("[data-col-menu]")?.focus({ preventScroll: true });
      if (!save) return;
      if (!value) {
        opts.toast(t("lib.board_editor.rename.empty"), "err");
        return;
      }
      void track(editor.rename(id, value), t("lib.board_editor.rename.done", { column: value }));
    };
    input.addEventListener("keydown", (e) => {
      if (e.key === "Enter") {
        e.preventDefault();
        finish(true);
      } else if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        finish(false);
      }
    });
    input.addEventListener("blur", () => finish(true));
  }

  let draft: HTMLElement | null = null;

  function openDraft(index: number) {
    closeDraft();
    const stages = editor.stages;
    draft = document.createElement("div");
    draft.className = colClass("col-draft");
    const hintID = "ce-draft-id";
    draft.innerHTML = `
      <form class="ce-draft form-stack" novalidate>
        ${fieldHTML({
          label: t("lib.board_editor.add.name"),
          for: "ce-draft-name",
          help: t("lib.board_editor.add.hint"),
          helpId: hintID,
          errorId: "ce-draft-name-error",
          control: `<input id="ce-draft-name" maxlength="64" autocomplete="off"
            placeholder="${esc(t("lib.board_editor.add.placeholder"))}" aria-describedby="${hintID}" />`,
        })}
        <div class="form-actions">
          ${buttonHTML({ label: t("lib.board_editor.add.submit"), type: "submit", variant: "primary", size: "sm" })}
          ${buttonHTML({ label: t("common.buttons.cancel"), variant: "ghost", size: "sm", attrs: { "data-ce-cancel": true } })}
        </div>
      </form>`;
    const cols = columns();
    const before = index < cols.length ? cols[index] : ghost;
    root.insertBefore(draft, before);
    const form = draft.querySelector<HTMLFormElement>("form");
    const input = draft.querySelector<HTMLInputElement>("input");
    const hint = draft.querySelector<HTMLElement>(`#${hintID}`);
    if (!form || !input || !hint) return;
    const showHint = () => {
      const label = input.value.trim();
      hint.innerHTML = label
        ? esc(
            t("lib.board_editor.add.id_preview", {
              id: slugify(
                label,
                stages.map((s) => s.id),
                "column",
                RESERVED_STAGE_IDS,
              ),
            }),
          )
        : esc(t("lib.board_editor.add.hint"));
    };
    showHint();
    input.addEventListener("input", () => {
      showHint();
      if (input.value.trim()) setFieldError(input, "");
    });
    input.addEventListener("keydown", (e) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        closeDraft(true);
      }
    });
    draft.querySelector("[data-ce-cancel]")?.addEventListener("click", () => closeDraft(true));
    form.addEventListener("submit", (e) => {
      e.preventDefault();
      const label = input.value.trim();
      if (!label) {
        setFieldError(input, t("lib.board_editor.add.required"), { focus: true });
        return;
      }
      closeDraft();
      const made = editor.create(label, { index });
      if (!made) return;
      announce(t("lib.board_editor.add.done", { column: label }));
      void made.done.then((res) => {
        if (!res.ok) return;
        opts.onSaved?.(res);
        colOf(made.id)?.querySelector<HTMLElement>("[data-col-menu]")?.focus();
      });
    });
    draft.scrollIntoView({ block: "nearest", inline: "nearest" });
    input.focus();
  }

  function closeDraft(restoreFocus = false) {
    if (!draft) return;
    draft.remove();
    draft = null;
    if (restoreFocus) ghost?.querySelector<HTMLElement>("button")?.focus();
  }

  root.addEventListener("click", (e) => {
    const target = e.target as HTMLElement;
    if (target.closest("[data-ce-add-end]")) {
      // Matches where the API inserts a column given no position: before the first terminal column.
      openDraft(defaultInsertIndex(editor.stages));
      return;
    }
    const btn = target.closest<HTMLElement>("[data-col-menu]");
    if (btn && opts.canEdit) {
      e.stopPropagation();
      // The bound head's button toggles the menu through bindMenu's own listener; opening here too
      // would fight it.
      if (bound?.trigger === btn) return;
      const id = btn.closest<HTMLElement>(".col")?.dataset.stage;
      if (id) openMenu(id, btn);
    }
  });

  root.addEventListener("keydown", (e) => {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
    const btn = (e.target as HTMLElement).closest<HTMLElement>("[data-col-menu]");
    if (!btn || !opts.canEdit || bound?.trigger === btn) return;
    const id = btn.closest<HTMLElement>(".col")?.dataset.stage;
    if (!id) return;
    e.preventDefault();
    e.stopPropagation();
    openMenu(id, btn, e.key === "ArrowUp" ? "last" : "first");
  });

  root.addEventListener("dblclick", (e) => {
    if (!opts.canEdit || !editing) return;
    // The head holds pointer capture for the drag, so e.target is the head; hit-test the point to
    // find the title.
    const under = document.elementFromPoint(e.clientX, e.clientY) as HTMLElement | null;
    const title = under?.closest<HTMLElement>("[data-ce-rename]");
    const id = title?.closest<HTMLElement>(".col[data-stage]")?.dataset.stage;
    if (!title || !id || !root.contains(title)) return;
    e.preventDefault();
    startRename(id);
  });

  const behaviorEl = byId("ce-behavior");
  const behaviorBody = byId("ce-behavior-body");
  let behaviorFor: string | null = null;
  let behaviorTrigger: HTMLElement | null = null;
  const behaviorModal = bindModal(behaviorEl, {
    beforeClose: () => flushApprovals(),
    onClose: () => {
      behaviorFor = null;
      window.clearTimeout(raTimer);
      raDirty = false;
      behaviorTrigger?.focus();
    },
  });

  function flagRowHTML(st: Stage, flag: FlagKey): string {
    return flagCheckHTML(flag, flagState(st, flag, editor.stages));
  }

  function agentRowHTML(st: Stage): string {
    const picker = opts.agentPicker;
    if (!picker) return "";
    if (!takesAgent(st)) {
      return `<p class="ce-section-desc" data-ce-agent-row>${esc(t("lib.column_agent.start_end"))}</p>`;
    }
    const state = picker.state(st.id);
    const perProject = `<a href="${esc(picker.projectsHref)}">${esc(t("lib.column_agent.per_project"))}</a>`;
    if (state.projects === 0) {
      return `<p class="ce-section-desc" data-ce-agent-row>${esc(t("lib.column_agent.no_projects"))} ${perProject}</p>`;
    }
    const value = pickerValue(state);
    const names = [...new Set([...picker.choices(st.id), ...state.agents])];
    const options = [
      `<option value=""${value === "" ? " selected" : ""}>${esc(t("lib.column_agent.picker_none"))}</option>`,
      value === MIXED
        ? `<option value="${MIXED}" selected disabled>${esc(t("lib.column_agent.picker_mixed"))}</option>`
        : "",
      ...names.map(
        (n) => `<option value="${esc(n)}"${value === n ? " selected" : ""}>${esc(n)}</option>`,
      ),
    ].join("");
    const noAgents = picker.choices(st.id).length
      ? ""
      : ` ${esc(t("lib.board_editor.flags.no_agents"))} <a href="/agents/">${esc(t("lib.board_editor.flags.agents_link"))}</a>`;
    return fieldHTML({
      label: t("lib.column_agent.picker"),
      for: "ce-agent",
      helpHTML: `${esc(t("lib.column_agent.picker_desc"))} ${esc(t("lib.column_agent.projects", { count: state.projects }))} ${perProject}${noAgents}`,
      errorId: "ce-agent-error",
      control: `<select id="ce-agent" data-ce-agent aria-describedby="ce-agent-help" ${opts.canEdit ? "" : "disabled"}>${options}</select>`,
      cls: "ce-agent",
      attrs: { "data-ce-agent-row": "" },
    });
  }

  function refreshAgentRow() {
    const st = behaviorFor ? stageOf(behaviorFor) : undefined;
    const row = behaviorBody?.querySelector<HTMLElement>("[data-ce-agent-row]");
    if (!st || !row) return;
    const focused = document.activeElement?.hasAttribute("data-ce-agent");
    row.outerHTML = agentRowHTML(st);
    if (focused) behaviorBody?.querySelector<HTMLElement>("[data-ce-agent]")?.focus();
  }

  async function saveAgent(select: HTMLSelectElement) {
    const id = behaviorFor;
    const st = id ? stageOf(id) : undefined;
    const picker = opts.agentPicker;
    if (!id || !st || !picker || select.value === MIXED) return;
    const agent = select.value;
    setFieldError(select, "");
    select.disabled = true;
    const res = await picker.save(id, agent);
    if (behaviorFor !== id) return;
    refreshAgentRow();
    if (res.failed.length) {
      const msg = res.failed
        .map((f) => t("lib.column_agent.save_failed", { project: f.project, error: f.error }))
        .join(" ");
      const select = behaviorBody?.querySelector<HTMLElement>("[data-ce-agent]");
      if (select) setFieldError(select, msg);
      return;
    }
    if (!res.saved) return;
    const done = agent
      ? t("lib.column_agent.saved", { column: st.label, agent, count: res.saved })
      : t("lib.column_agent.cleared", { column: st.label });
    announce(done);
    opts.toast(done, "ok");
  }

  function behaviorHTML(st: Stage): string {
    const types = opts.taskTypes?.() ?? [];
    const idx = editor.stages.findIndex((s) => s.id === st.id);
    const earlier = editor.stages
      .slice(0, Math.max(idx, 0))
      .map((s) => ({ id: s.id, label: s.label }));
    const legacy = st.blocked_by_flags ? legacyFlagCheckHTML() : "";
    const wip = segmentedHTML(
      "ce-wip",
      WIP_GROUPS.map((g) => ({ value: g, label: WIP_TEXT[g] })),
      st.wip_group,
      { labelledBy: "ce-sec-wip", describedBy: "ce-wip-d" },
    );
    return `
      <p class="modal-lede">${esc(t("lib.board_editor.behavior.lead"))}</p>
      <section class="ce-section" aria-labelledby="ce-sec-flags">
        <h4 id="ce-sec-flags" class="ce-section-title">${esc(t("lib.board_editor.behavior.flow"))}</h4>
        ${checkGroupHTML({
          labelledBy: "ce-sec-flags",
          itemsHTML: flagRowHTML(st, "initial") + flagRowHTML(st, "terminal"),
        })}
      </section>
      <section class="ce-section" aria-labelledby="ce-sec-agent">
        <h4 id="ce-sec-agent" class="ce-section-title">${esc(t("lib.board_editor.behavior.agent"))}</h4>
        <div class="form-stack">
          ${agentRowHTML(st)}
          ${checkGroupHTML({
            labelledBy: "ce-sec-agent",
            itemsHTML:
              FLAG_KEYS.filter((f) => f !== "initial" && f !== "terminal")
                .map((f) => flagRowHTML(st, f))
                .join("") + legacy,
          })}
        </div>
      </section>
      <section class="ce-section" aria-labelledby="ce-sec-wip">
        <h4 id="ce-sec-wip" class="ce-section-title">${esc(t("lib.board_editor.behavior.wip"))}</h4>
        <p class="ce-section-desc" id="ce-wip-d">${esc(t("lib.board_editor.behavior.wip_desc"))}</p>
        ${wip}
        ${fieldErrorHTML({ tag: "span", cls: "ce-wip-error" })}
      </section>
      <section class="ce-section ce-approvals" id="ce-approvals" aria-labelledby="ce-sec-ra">
        <h4 id="ce-sec-ra" class="ce-section-title">${esc(t("lib.board_editor.menu.approvals"))}</h4>
        <div class="ra-body">${approvalsEditorHTML(st, types, earlier)}</div>
        <div class="ce-approvals-foot">
          ${fieldErrorHTML({ tag: "span", cls: "ce-ra-error" })}
          <span class="ce-ra-status" aria-hidden="true"></span>
        </div>
      </section>
      <p class="ce-elsewhere">
        <span>${esc(t("lib.board_editor.behavior.elsewhere"))}</span>
        ${
          opts.agentPicker
            ? ""
            : `<a href="/agents/">${esc(t("lib.board_editor.behavior.agents_link"))}</a>`
        }
        <a href="/sandboxes/">${esc(t("lib.board_editor.behavior.sandboxes_link"))}</a>
      </p>`;
  }

  function openBehavior(id: string, trigger: HTMLElement | null, approvals: boolean) {
    const st = stageOf(id);
    if (!behaviorEl || !behaviorBody || !st) return;
    behaviorFor = id;
    behaviorTrigger = trigger;
    const title = byId("ce-behavior-title");
    if (title) title.textContent = t("lib.board_editor.behavior.title", { column: st.label });
    behaviorBody.innerHTML = behaviorHTML(st);
    wireApprovals(behaviorBody);
    behaviorModal.open(
      approvals
        ? behaviorBody.querySelector<HTMLElement>("#ce-approvals [data-ra-add]")
        : behaviorBody.querySelector<HTMLElement>("input:not([disabled])"),
    );
    if (approvals) {
      requestAnimationFrame(() =>
        behaviorBody.querySelector("#ce-approvals")?.scrollIntoView({ block: "start" }),
      );
    }
  }

  function refreshBehavior() {
    const st = behaviorFor ? stageOf(behaviorFor) : undefined;
    if (!behaviorBody || !st) {
      if (behaviorFor) behaviorModal.close(true);
      return;
    }
    for (const flag of FLAG_KEYS) {
      const row = behaviorBody.querySelector<HTMLElement>(`[data-flag-row="${flag}"]`);
      const input = row?.querySelector<HTMLInputElement>("input");
      if (!row || !input) continue;
      const s = flagState(st, flag, editor.stages);
      const reason = s.disabled ? s.reason : "";
      const shown = row.querySelector(".check-reason")?.textContent ?? "";
      if (input.disabled !== s.disabled || shown !== reason) {
        row.outerHTML = flagCheckHTML(flag, s);
      } else {
        input.checked = s.checked;
      }
    }
    behaviorBody
      .querySelectorAll<HTMLInputElement>('input[name="ce-wip"]')
      .forEach((r) => (r.checked = r.value === st.wip_group));
  }

  behaviorBody?.addEventListener("change", (e) => {
    const input = e.target as HTMLInputElement;
    const id = behaviorFor;
    if (!id) return;
    if (input.hasAttribute("data-ce-agent")) {
      void saveAgent(input as unknown as HTMLSelectElement);
      return;
    }
    const flag = input.dataset.flag as FlagKey | undefined;
    if (flag) {
      clearError(input);
      void track(
        editor.setFlag(id, flag, input.checked),
        t("lib.board_editor.behavior.saved", { setting: FLAG_TEXT[flag].name }),
      );
      return;
    }
    if (input.hasAttribute("data-legacy")) {
      void track(editor.patch(id, { blocked_by_flags: input.checked }));
      return;
    }
    if (input.name === "ce-wip") {
      void track(
        editor.setWipGroup(id, input.value as WipGroup),
        t("lib.board_editor.behavior.saved", { setting: WIP_TEXT[input.value as WipGroup] }),
      );
      return;
    }
    if (input.closest(".ra-body")) scheduleApprovals();
  });
  behaviorBody?.addEventListener("input", (e) => {
    if ((e.target as HTMLElement).closest(".ra-body")) raDirty = true;
  });

  let raDirty = false;
  let raTimer = 0;
  let raSaving = false;

  function scheduleApprovals() {
    raDirty = true;
    window.clearTimeout(raTimer);
    raTimer = window.setTimeout(() => void saveApprovals(), 250);
  }

  function approvalsOf(st: Stage): ApprovalsPatch {
    const mode = onChangesOf(st);
    return {
      required_approvals: normalizeRequiredApprovals(st.required_approvals ?? []).rows,
      on_changes_requested: mode,
      changes_kick_back_to: mode === "kick_back" ? (st.changes_kick_back_to ?? "") : "",
    };
  }

  function readApprovals(): ApprovalsPatch | null | { problem: string } {
    const editorEl = behaviorBody?.querySelector<HTMLElement>(".ra-body");
    const st = behaviorFor ? stageOf(behaviorFor) : undefined;
    if (!editorEl || !st) return null;
    const patch = approvalsPatch(editorEl);
    if ("problem" in patch) return patch;
    return JSON.stringify(patch) === JSON.stringify(approvalsOf(st)) ? null : patch;
  }

  function approvalsStatus(text: string) {
    const el = behaviorBody?.querySelector<HTMLElement>(".ce-ra-status");
    if (el) el.textContent = text;
  }

  function approvalsProblem(problem: string | null) {
    const err = behaviorBody?.querySelector<HTMLElement>(".ce-ra-error");
    if (!err) return;
    err.textContent = problem ?? "";
    err.hidden = !problem;
  }

  function saveApprovals(): boolean {
    window.clearTimeout(raTimer);
    const id = behaviorFor;
    if (!raDirty || !id) return true;
    const read = readApprovals();
    if (read && "problem" in read) {
      approvalsProblem(read.problem);
      return false;
    }
    approvalsProblem(null);
    raDirty = false;
    if (!read) return true;
    pinApprovalKeys();
    approvalsStatus(t("lib.board_editor.behavior.saving"));
    raSaving = true;
    void editor.patch(id, read).then((res) => {
      raSaving = false;
      if (behaviorFor !== id) return;
      if (res.ok) {
        approvalsStatus(t("lib.board_editor.behavior.approvals_saved"));
        announce(t("lib.board_editor.behavior.approvals_saved"));
        opts.onSaved?.(res);
      } else {
        approvalsStatus("");
        raDirty = true;
      }
    });
    return true;
  }

  function flushApprovals(): boolean {
    if (saveApprovals()) return true;
    const rows = behaviorBody?.querySelectorAll<HTMLInputElement>('[data-ra="label"]') ?? [];
    const blank = Array.from(rows).find((r) => !r.value.trim());
    const target = blank ?? behaviorBody?.querySelector<HTMLElement>('[data-ra="key"]');
    target?.scrollIntoView({ block: "nearest" });
    target?.focus();
    return false;
  }

  // Write the derived key into the field: the daemon matches open approvals to their definition by
  // key, so a later label rename must not change it.
  function pinApprovalKeys() {
    behaviorBody?.querySelectorAll<HTMLElement>("[data-ra-row]").forEach((row) => {
      const key = row.querySelector<HTMLInputElement>('[data-ra="key"]');
      const label = row.querySelector<HTMLInputElement>('[data-ra="label"]');
      if (key && label && !key.value.trim() && label.value.trim()) {
        key.value = approvalKeyFromLabel(label.value);
      }
    });
  }

  function wireApprovals(body: HTMLElement) {
    const rows = body.querySelector<HTMLElement>("[data-ra-rows]");
    const types = opts.taskTypes?.() ?? [];
    body.querySelector("[data-ra-add]")?.addEventListener("click", () => {
      rows?.append(approvalRowEl({ key: "", label: "" }, types));
      rows?.querySelector<HTMLInputElement>('[data-ra-row]:last-child [data-ra="label"]')?.focus();
    });
    rows?.addEventListener("click", (e) => {
      const btn = (e.target as HTMLElement).closest("[data-ra-remove]");
      if (!btn) return;
      const row = btn.closest("[data-ra-row]");
      const next =
        row?.nextElementSibling?.querySelector<HTMLElement>('[data-ra="label"]') ??
        body.querySelector<HTMLElement>("[data-ra-add]");
      row?.remove();
      next?.focus();
      scheduleApprovals();
    });
    const mode = body.querySelector<HTMLSelectElement>('[data-ra="on_changes"]');
    const kick = body.querySelector<HTMLElement>("[data-ra-kick]");
    mode?.addEventListener("change", () => {
      if (kick) kick.hidden = mode.value !== "kick_back";
    });
  }

  function clearError(input: HTMLElement) {
    const el = input.closest("[data-flag-row]")?.querySelector<HTMLElement>(".check-error");
    if (el) el.hidden = true;
  }

  function showDialogError(res: EditResult): boolean {
    if (behaviorFor && res.stage === behaviorFor && behaviorBody && res.kind === "patch") {
      const focused = document.activeElement as HTMLElement | null;
      const slot =
        (raSaving ? behaviorBody.querySelector<HTMLElement>(".ce-ra-error") : null) ??
        focused?.closest("[data-flag-row]")?.querySelector<HTMLElement>(".check-error") ??
        (focused?.closest(".ce-approvals")
          ? behaviorBody.querySelector<HTMLElement>(".ce-ra-error")
          : null) ??
        behaviorBody.querySelector<HTMLElement>(".ce-wip-error");
      if (slot) {
        slot.textContent = errorText(res.error);
        slot.hidden = false;
        return true;
      }
    }
    if (deleteFor && res.stage === deleteFor && res.kind === "remove") {
      const count = pendingTaskCount(res.error);
      if (count) {
        void openDelete(deleteFor, deleteTrigger, count);
        return true;
      }
    }
    return false;
  }

  let deleteFor: string | null = null;
  let deleteTrigger: HTMLElement | null = null;

  async function openDelete(id: string, trigger: HTMLElement | null, knownCount?: number) {
    const st = stageOf(id);
    if (!st) return;
    const count = knownCount ?? opts.taskCount(id);
    const { ok, values } = await confirmDialog({
      title: t("lib.board_editor.delete.title", { column: st.label }),
      message: count
        ? t("lib.board_editor.delete.with_tasks", { count })
        : t("lib.board_editor.delete.empty"),
      bodyHTML: moveTasksBodyHTML({
        count,
        destinations: editor.stages.filter((s) => s.id !== id),
        label: t("lib.board_editor.delete.move_to", { count }),
        id: "ce-delete-to",
      }),
      notes: [t("lib.board_editor.delete.lost")],
      confirmLabel: t("lib.board_editor.delete.confirm"),
      danger: true,
    });
    if (!ok) {
      trigger?.focus();
      return;
    }
    const neighbour = editor.stages[editor.stages.findIndex((s) => s.id === id) - 1]?.id;
    deleteTrigger = neighbour
      ? (colOf(neighbour)?.querySelector<HTMLElement>("[data-col-menu]") ?? null)
      : null;
    deleteFor = id;
    try {
      await track(
        editor.remove(id, typeof values.move_to === "string" ? values.move_to : ""),
        t("lib.board_editor.delete.done", { column: st.label }),
      );
    } finally {
      if (deleteFor === id) deleteFor = null;
    }
    deleteTrigger?.focus();
  }

  interface Drag {
    id: string;
    col: HTMLElement;
    head: HTMLElement;
    pointerId: number;
    startX: number;
    startY: number;
    grabX: number;
    grabY: number;
    tx: number;
    ty: number;
    x: number;
    y: number;
    axis: Axis;
    started: boolean;
    order: string[];
    raf: number;
  }
  let drag: Drag | null = null;
  let suppressClick = false;

  type Axis = "x" | "y" | "grid";

  function layoutAxis(): Axis {
    if (getComputedStyle(root).display === "grid") return "grid";
    const cols = columns();
    if (cols.length < 2) return "x";
    const a = cols[0].getBoundingClientRect();
    const b = cols[1].getBoundingClientRect();
    return Math.abs(b.top - a.top) > Math.abs(b.left - a.left) ? "y" : "x";
  }

  root.addEventListener("pointerdown", (e) => {
    if (!opts.canEdit || !editing || e.button !== 0 || drag || kbd) return;
    const target = e.target as HTMLElement;
    const head = target.closest<HTMLElement>(".col-head");
    const col = head?.closest<HTMLElement>(".col[data-stage]");
    if (!head || !col || !root.contains(col)) return;
    if (target.closest("[data-col-menu], input, a") || col.classList.contains("ce-renaming"))
      return;
    const rect = col.getBoundingClientRect();
    drag = {
      id: col.dataset.stage ?? "",
      col,
      head,
      pointerId: e.pointerId,
      startX: e.clientX,
      startY: e.clientY,
      grabX: e.clientX - rect.left,
      grabY: e.clientY - rect.top,
      tx: 0,
      ty: 0,
      x: e.clientX,
      y: e.clientY,
      axis: "x",
      started: false,
      order: columns().map((c) => c.dataset.stage ?? ""),
      raf: 0,
    };
    head.setPointerCapture(e.pointerId);
  });

  // Listen on window: moving the column in the DOM drops the head's pointer capture, and a release
  // root never hears would leave the drag open and commit on the next click.
  window.addEventListener(
    "pointermove",
    (e) => {
      if (drag?.pointerId !== e.pointerId) return;
      drag.x = e.clientX;
      drag.y = e.clientY;
      if (!drag.started) {
        if (Math.hypot(e.clientX - drag.startX, e.clientY - drag.startY) < 6) return;
        startDrag(drag);
      }
      e.preventDefault();
      follow(drag);
      reorderUnder(drag);
    },
    { signal },
  );

  const endPointer = (e: PointerEvent) => {
    if (drag?.pointerId !== e.pointerId) return;
    const d = drag;
    if (d.head.hasPointerCapture(e.pointerId)) d.head.releasePointerCapture(e.pointerId);
    if (!d.started) {
      drag = null;
      return;
    }
    suppressClick = true;
    window.setTimeout(() => (suppressClick = false), 0);
    finishDrag(d, e.type !== "pointercancel" && !outside(e.clientX, e.clientY));
  };
  window.addEventListener("pointerup", endPointer, { signal });
  window.addEventListener("pointercancel", endPointer, { signal });
  root.addEventListener(
    "click",
    (e) => {
      if (suppressClick) {
        e.stopPropagation();
        e.preventDefault();
      }
    },
    { capture: true },
  );

  function startDrag(d: Drag) {
    d.started = true;
    d.axis = layoutAxis();
    closeMenu(false);
    closeDraft();
    d.col.classList.add("dragging", "ce-dragging");
    root.classList.add("ce-drag-active");
    document.body.classList.add("ce-grabbing");
    const st = stageOf(d.id);
    announce(
      t("lib.board_editor.move.picked", {
        column: st?.label ?? d.id,
        position: d.order.indexOf(d.id) + 1,
        count: d.order.length,
      }),
    );
    const tick = () => {
      if (!drag?.started) return;
      const bounds = root.getBoundingClientRect();
      if (root.scrollWidth > root.clientWidth) {
        const dx = autoScrollDelta(drag.x, bounds, 80, 12);
        if (dx) {
          root.scrollLeft += dx;
          follow(drag);
          reorderUnder(drag);
        }
      }
      drag.raf = requestAnimationFrame(tick);
    };
    d.raf = requestAnimationFrame(tick);
  }

  function outside(x: number, y: number): boolean {
    const r = root.getBoundingClientRect();
    const slack = 32;
    return x < r.left - slack || x > r.right + slack || y < r.top - slack || y > r.bottom + slack;
  }

  function follow(d: Drag) {
    const r = d.col.getBoundingClientRect();
    const naturalLeft = r.left - d.tx;
    const naturalTop = r.top - d.ty;
    d.tx = d.axis !== "y" ? d.x - d.grabX - naturalLeft : 0;
    d.ty = d.axis !== "x" ? d.y - d.grabY - naturalTop : 0;
    d.col.style.transform = `translate(${Math.round(d.tx)}px, ${Math.round(d.ty)}px)`;
  }

  function slotUnder(d: Drag, others: HTMLElement[]): number {
    let n = 0;
    for (const c of others) {
      const r = c.getBoundingClientRect();
      const passed =
        d.axis === "x"
          ? d.x > r.left + r.width / 2
          : d.axis === "y"
            ? d.y > r.top + r.height / 2
            : d.y > r.bottom || (d.y >= r.top && d.x > r.left + r.width / 2);
      if (passed) n++;
    }
    return n;
  }

  function reorderUnder(d: Drag) {
    if (outside(d.x, d.y)) return;
    const all = columns();
    const others = all.filter((c) => c !== d.col);
    const cur = all.indexOf(d.col);
    const want = slotUnder(d, others);
    if (want === cur) return;
    const next = want > cur ? cur + 1 : cur - 1;
    const anchor =
      next < others.length ? others[next] : (others[others.length - 1]?.nextElementSibling ?? null);
    flip(others, () => root.insertBefore(d.col, anchor));
    follow(d);
  }

  function flip(els: HTMLElement[], move: () => void) {
    const first = new Map(els.map((el) => [el, el.getBoundingClientRect()]));
    move();
    if (reducedMotion()) return;
    for (const el of els) {
      const a = first.get(el);
      if (!a) continue;
      const b = el.getBoundingClientRect();
      const dx = a.left - b.left;
      const dy = a.top - b.top;
      if (!dx && !dy) continue;
      // i18n-ignore: a CSS transform
      el.animate([{ transform: `translate(${dx}px, ${dy}px)` }, { transform: "translate(0, 0)" }], {
        duration: 160,
        easing: "cubic-bezier(0.2, 0.8, 0.2, 1)",
      });
    }
  }

  function finishDrag(d: Drag, commit: boolean) {
    cancelAnimationFrame(d.raf);
    drag = null;
    d.col.style.transform = "";
    d.col.classList.remove("dragging", "ce-dragging");
    root.classList.remove("ce-drag-active");
    document.body.classList.remove("ce-grabbing");
    const ids = columns().map((c) => c.dataset.stage ?? "");
    const changed = ids.join() !== d.order.join();
    if (!commit) {
      restoreOrder(d.order);
      announce(t("lib.board_editor.move.cancelled"));
      return;
    }
    if (!changed) {
      announce(t("lib.board_editor.move.unchanged"));
      return;
    }
    announce(positionTextFor(d.id, ids));
    void track(editor.reorder(ids));
  }

  function positionTextFor(id: string, ids: string[]): string {
    return t("lib.board_editor.move.moved", {
      column: stageOf(id)?.label ?? id,
      position: ids.indexOf(id) + 1,
      count: ids.length,
    });
  }

  function restoreOrder(order: string[]) {
    const anchor = draft ?? ghost ?? null;
    for (const id of order) {
      const c = colOf(id);
      if (c) root.insertBefore(c, anchor?.parentElement === root ? anchor : null);
    }
  }

  let kbd: { id: string; order: string[] } | null = null;

  root.addEventListener("keydown", (e) => {
    // Edit mode only: outside it Alt+←/→ must stay the browser's Back and Forward.
    if (!opts.canEdit || !editing) return;
    const target = e.target as HTMLElement;
    const head = target.closest<HTMLElement>(".col-head");
    const col = head?.closest<HTMLElement>(".col[data-stage]");
    if (!head || !col || target.tagName === "INPUT") return;
    const id = col.dataset.stage ?? "";
    if (target.hasAttribute("data-ce-rename") && (e.key === "Enter" || e.key === "F2")) {
      e.preventDefault();
      startRename(id);
      return;
    }
    const horizontal = layoutAxis() === "x";
    const back = horizontal ? "ArrowLeft" : "ArrowUp";
    const fwd = horizontal ? "ArrowRight" : "ArrowDown";

    if (e.altKey && (e.key === "ArrowLeft" || e.key === "ArrowRight")) {
      e.preventDefault();
      stepColumn(id, e.key === "ArrowLeft" ? -1 : 1);
      return;
    }
    if (!target.hasAttribute("data-col-grip")) return;

    if (!kbd && (e.key === " " || e.key === "Enter")) {
      e.preventDefault();
      kbd = { id, order: columns().map((c) => c.dataset.stage ?? "") };
      col.classList.add("dragging", "ce-picked");
      announce(
        t("lib.board_editor.move.picked_kbd", {
          column: stageOf(id)?.label ?? id,
          position: kbd.order.indexOf(id) + 1,
          count: kbd.order.length,
        }),
      );
      return;
    }
    if (kbd?.id !== id) return;
    if (e.key === back || e.key === fwd) {
      e.preventDefault();
      const cols = columns();
      const i = cols.indexOf(col);
      const j = i + (e.key === fwd ? 1 : -1);
      if (j < 0 || j >= cols.length) {
        announce(j < 0 ? t("lib.board_editor.move.at_start") : t("lib.board_editor.move.at_end"));
        return;
      }
      const others = cols.filter((c) => c !== col);
      flip(others, () =>
        root.insertBefore(col, e.key === fwd ? cols[j].nextElementSibling : cols[j]),
      );
      target.focus();
      announce(
        positionTextFor(
          id,
          columns().map((c) => c.dataset.stage ?? ""),
        ),
      );
    } else if (e.key === " " || e.key === "Enter") {
      e.preventDefault();
      dropKbd(true);
    } else if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      dropKbd(false);
    }
  });

  root.addEventListener("focusout", () => {
    if (!kbd) return;
    window.setTimeout(() => {
      const active = document.activeElement as HTMLElement | null;
      if (kbd && !active?.closest(`.col[data-stage="${CSS.escape(kbd.id)}"] [data-col-grip]`)) {
        dropKbd(false);
      }
    }, 0);
  });

  function dropKbd(commit: boolean) {
    if (!kbd) return;
    const { id, order } = kbd;
    kbd = null;
    const col = colOf(id);
    col?.classList.remove("dragging", "ce-picked");
    const ids = columns().map((c) => c.dataset.stage ?? "");
    if (!commit) {
      restoreOrder(order);
      col?.querySelector<HTMLElement>("[data-col-grip]")?.focus();
      announce(t("lib.board_editor.move.cancelled"));
      return;
    }
    if (ids.join() === order.join()) {
      announce(t("lib.board_editor.move.unchanged"));
      return;
    }
    announce(positionTextFor(id, ids));
    void track(editor.reorder(ids));
  }

  document.addEventListener(
    "keydown",
    (e) => {
      if (e.key === "Escape" && drag?.started) {
        e.preventDefault();
        e.stopPropagation();
        const d = drag;
        if (d.head.hasPointerCapture(d.pointerId)) d.head.releasePointerCapture(d.pointerId);
        finishDrag(d, false);
      }
    },
    { signal, capture: true },
  );

  sync();

  return {
    editor,
    sync,
    get editing() {
      return editing;
    },
    setEditing(on: boolean) {
      editing = on;
      if (!on) {
        closeDraft();
        closeMenu(false);
      }
      sync();
    },
    reset(board: string, stages: Stage[]) {
      editor.reset(board, stages);
    },
    openBehavior(id: string, trigger: HTMLElement | null) {
      openBehavior(id, trigger, false);
    },
    escape() {
      if (menuOpen()) {
        closeMenu();
        return true;
      }
      if (behaviorModal.isOpen()) {
        behaviorModal.close();
        return true;
      }
      if (draft) {
        closeDraft(true);
        return true;
      }
      return false;
    },
  };
}

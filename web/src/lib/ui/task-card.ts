import { t } from "../../i18n";
import { attrsHTML, esc, type Attrs } from "./dom";
import { buttonHTML, type IconButtonOptions } from "./button";
import { icoSvg } from "./icon";

export type TaskCardFlag = "dragging" | "ghost" | "lifted" | "hl" | "dim";

const FLAGS = new Set<string>(["dragging", "ghost", "lifted", "hl", "dim"]);

export function taskCardFlagClass(flag: TaskCardFlag): string {
  if (!FLAGS.has(flag)) throw new Error(`task-card: no flag "${flag}"`); // i18n-ignore a programming error
  return `task-card-${flag}`;
}

export function setTaskCardFlag(el: Element, flag: TaskCardFlag, on: boolean): void {
  el.classList.toggle(taskCardFlagClass(flag), on);
}

export function hasTaskCardFlag(el: Element, flag: TaskCardFlag): boolean {
  return el.classList.contains(taskCardFlagClass(flag));
}

export function taskCardClasses(flags: TaskCardFlag[] = [], cls?: string): string {
  return ["task-card", ...flags.map(taskCardFlagClass), cls].filter(Boolean).join(" ");
}

export function taskCardKidsId(taskID: string): string {
  return `task-card-kids-${taskID}`;
}

export function taskCardMenuButton(): IconButtonOptions {
  return {
    iconOnly: true,
    icon: "more",
    label: t("lib.task_card.labels.actions_key"),
    size: "sm",
    variant: "ghost",
    cls: "task-card-menu-btn",
    attrs: { "data-card-menu": "", "aria-haspopup": "menu", "aria-expanded": "false" },
  };
}

export function taskCardHandleTitle(): string {
  return t("lib.task_card.labels.drag_to_move");
}

export function taskCardShellHTML(taskID: string): string {
  const kids = esc(taskCardKidsId(taskID));
  return (
    `<div class="task-card-tools">` +
    `<span class="task-card-handle" title="${esc(taskCardHandleTitle())}" aria-hidden="true">${icoSvg("grip")}</span>` +
    buttonHTML(taskCardMenuButton()) +
    `</div>` +
    `<div class="task-card-part" hidden></div>` +
    `<div class="task-card-title-row"><span class="task-card-type"></span><span class="task-card-title"></span><span class="task-card-prio"></span></div>` +
    `<div class="task-card-state" hidden></div>` +
    `<div class="task-card-meta" hidden></div>` +
    `<div class="task-card-foot">` +
    `<span class="task-card-progress"></span>` +
    `<button type="button" class="task-card-expand" data-card-expand aria-expanded="false" aria-controls="${kids}" hidden>${icoSvg("chevron-down")}</button>` +
    `<span class="task-card-foot-rest"></span>` +
    `</div>` +
    `<ul class="task-card-kids" id="${kids}" hidden></ul>`
  );
}

export interface TaskCardOptions {
  id: string;
  flags?: TaskCardFlag[];
  cls?: string;
  attrs?: Attrs;
}

export function taskCardHTML(o: TaskCardOptions): string {
  const attrs: Attrs = {
    class: taskCardClasses(o.flags, o.cls),
    "data-id": o.id,
    tabindex: "0",
  };
  return `<div${attrsHTML(attrs)}${attrsHTML(o.attrs)}>${taskCardShellHTML(o.id)}</div>`;
}

export function taskCardEl(o: TaskCardOptions): HTMLElement {
  const tpl = document.createElement("template");
  tpl.innerHTML = taskCardHTML(o);
  return tpl.content.firstElementChild as HTMLElement;
}

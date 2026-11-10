import { t } from "../i18n";
import { priorityLook } from "./task-detail";
import { buttonHTML, esc, icoSvg, isHexColor, menuItemsHTML, type MenuEntry } from "./ui";

export type CardAction =
  | "open"
  | "move"
  | "priority"
  | "add-subtask"
  | "copy-link"
  | "clone"
  | "fork"
  | "more-turns"
  | "archive"
  | "delete";

export interface CardMenuItem {
  act: CardAction;
  label: string;
  sub?: string;
  icon: string;
  kbd: string;
  key: string;
  danger?: boolean;
  disabled?: boolean;
}

export interface CardMenuTask {
  parent_id?: string;
  childCount: number;
  archived?: boolean;
  canDelete?: boolean;
  canWrite?: boolean;
  status?: string;
}

export function ranOutOfTurns(status: string | undefined): boolean {
  return status === "out_of_turns" || status === "needs_more_turns";
}

const WRITE_ACTIONS: ReadonlySet<CardAction> = new Set([
  "move",
  "priority",
  "add-subtask",
  "clone",
  "fork",
  "more-turns",
  "archive",
  "delete",
]);

export function cardMenuItems(task: CardMenuTask): CardMenuItem[] {
  const items = menuItems(task);
  if (task.canWrite !== false) return items;
  let said = false;
  return items.map((it) => {
    if (!WRITE_ACTIONS.has(it.act)) return it;
    const sub = said ? undefined : t("lib.card_menu.read_only");
    said = true;
    return { ...it, disabled: true, sub };
  });
}

function menuItems(task: CardMenuTask): CardMenuItem[] {
  const items: CardMenuItem[] = [
    {
      act: "open",
      label: t("lib.card_menu.open"),
      icon: "i-arrow-up-right",
      kbd: t("lib.card_menu.kbd_enter"),
      key: "enter",
    },
    { act: "priority", label: t("lib.card_menu.priority"), icon: "i-flag", kbd: "P", key: "p" },
  ];
  if (!task.archived)
    items.splice(1, 0, {
      act: "move",
      label: t("lib.card_menu.move"),
      icon: "i-arrow-right",
      kbd: "M",
      key: "m",
    });
  if (!task.parent_id) {
    items.push({
      act: "add-subtask",
      label: t("lib.card_menu.add_subtask"),
      icon: "i-plus",
      kbd: "N",
      key: "n",
    });
  }
  items.push(
    { act: "copy-link", label: t("lib.card_menu.copy_link"), icon: "i-link", kbd: "L", key: "l" },
    { act: "clone", label: t("lib.card_menu.clone"), icon: "i-copy", kbd: "C", key: "c" },
    { act: "fork", label: t("lib.card_menu.fork"), icon: "i-branch", kbd: "F", key: "f" },
  );
  if (ranOutOfTurns(task.status)) {
    items.push({
      act: "more-turns",
      label: t("lib.card_menu.more_turns"),
      icon: "i-refresh",
      kbd: "T",
      key: "t",
    });
  }
  if (!task.parent_id) {
    items.push({
      act: "archive",
      label: task.archived ? t("lib.card_menu.unarchive") : t("lib.card_menu.archive"),
      sub:
        !task.archived && task.childCount > 0
          ? t("lib.card_menu.archive_with_subtasks", { count: task.childCount })
          : undefined,
      icon: "i-box",
      kbd: "A",
      key: "a",
    });
  }
  const kids = task.childCount;
  const allowed = task.canDelete !== false;
  items.push({
    act: "delete",
    label: t("lib.card_menu.delete"),
    sub: !allowed
      ? t("lib.card_menu.delete_admin_only")
      : kids > 0
        ? t("lib.card_menu.delete_with_subtasks", { count: kids })
        : undefined,
    icon: "i-trash",
    kbd: t("lib.card_menu.kbd_delete"),
    key: "delete",
    danger: true,
    disabled: !allowed,
  });
  return items;
}

export function cardMenuEntries(items: CardMenuItem[]): MenuEntry[] {
  const out: MenuEntry[] = [];
  items.forEach((it, i) => {
    const closing =
      it.act === "archive" || (it.act === "delete" && items[i - 1]?.act !== "archive");
    if (it.act === "copy-link" || closing) out.push({ sep: true });
    out.push({
      title: it.label,
      sub: it.sub,
      icon: it.icon,
      kbd: it.kbd,
      key: it.key,
      danger: it.danger,
      disabled: it.disabled,
      cls: "cm-item",
      attrs: { "data-cm": it.act },
    });
  });
  return out;
}

export function cardMenuHTML(items: CardMenuItem[]): string {
  return menuItemsHTML(cardMenuEntries(items));
}

// back is false when the choices open on their own (the list's stage cell): there is no menu to go
// back to.
function subHead(title: string, back = true): MenuEntry {
  if (!back) return { head: title };
  const btn = buttonHTML({
    iconOnly: true,
    icon: "chevron-left",
    label: t("lib.card_menu.back"),
    size: "lg",
    variant: "ghost",
    cls: "cm-back",
    attrs: { "data-cm-back": true, role: "menuitem", tabindex: "-1" },
  });
  return { head: title, leadHTML: btn };
}

export interface StageChoice {
  id: string;
  label: string;
  color: string;
}

export function stageChoicesHTML(
  stages: StageChoice[],
  current: string,
  opts: { back?: boolean } = {},
): string {
  return menuItemsHTML([
    subHead(t("lib.card_menu.move_to"), opts.back ?? true),
    ...stages.map(
      (s): MenuEntry => ({
        title: s.label,
        dot: isHexColor(s.color) ? s.color : undefined,
        indent: !isHexColor(s.color),
        checked: s.id === current,
        cls: "cm-item",
        attrs: { "data-cm-stage": s.id },
      }),
    ),
  ]);
}

export const PRIORITIES = ["urgent", "high", "medium", "low", ""] as const;

export function priorityChoicesHTML(current: string): string {
  return menuItemsHTML([
    subHead(t("lib.card_menu.priority")),
    ...PRIORITIES.map((p): MenuEntry => {
      const look = priorityLook(p);
      return {
        title: look ? look.label : t("lib.card_menu.priority_none"),
        leadHTML: look
          ? `<span class="cm-prio prio-${esc(look.value)}">${icoSvg(look.icon)}</span>`
          : undefined,
        indent: !look,
        checked: p === current,
        cls: "cm-item",
        attrs: { "data-cm-priority": p },
      };
    }),
  ]);
}

export { menuPosition } from "./ui/menu";

// Each *HTML() here must write the same markup as its .astro component.

import { t } from "../../i18n";
import { dotHTML } from "./badge";
import { buttonHTML, type ButtonOptions } from "./button";
import { attrsHTML, esc, type Attrs } from "./dom";
import { listRowHTML, type ListRowClamp, type ListRowOptions } from "./list-row";

export type CatalogSide = "md" | "sm";

const SIDES = new Set<string>(["md", "sm"]);

export function catalogLayoutClasses(side: CatalogSide = "md", cls?: string): string {
  if (!SIDES.has(side)) throw new Error(`catalog: no side "${side}"`); // i18n-ignore a programming error
  return ["catalog", side === "sm" && "catalog-side-sm", cls].filter(Boolean).join(" ");
}

export interface CatalogLayoutOptions {
  side?: CatalogSide;
  label?: string;
  toolbarHTML?: string;
  listHTML?: string;
  listId?: string;
  footHTML?: string;
  mainHTML?: string;
  mainId?: string;
  id?: string;
  cls?: string;
  attrs?: Attrs;
}

// The page's <main> needs `.catalog-page` for the full-height column.
export function catalogLayoutHTML(o: CatalogLayoutOptions = {}): string {
  const own: Attrs = { id: o.id, class: catalogLayoutClasses(o.side, o.cls) };
  return (
    `<div${attrsHTML(own)}${attrsHTML(o.attrs)}>` +
    `<aside${attrsHTML({ class: "catalog-side", "aria-label": o.label })}>` +
    (o.toolbarHTML ? `<div class="catalog-side-toolbar">${o.toolbarHTML}</div>` : "") +
    `<div${attrsHTML({ id: o.listId, class: "catalog-side-list" })}>${o.listHTML ?? ""}</div>` +
    (o.footHTML ? `<div class="catalog-side-foot">${o.footHTML}</div>` : "") +
    `</aside>` +
    `<section${attrsHTML({ id: o.mainId, class: "catalog-main" })}>${o.mainHTML ?? ""}</section>` +
    `</div>`
  );
}

export interface CatalogItemOptions {
  name: string;
  description?: string;
  descriptionHTML?: string;
  tagsHTML?: string;
  mono?: boolean;
  clamp?: ListRowClamp;
  current?: boolean;
  dim?: boolean;
  badgesHTML?: string;
  id?: string;
  cls?: string;
  attrs?: Attrs;
}

export function catalogItemRow(o: CatalogItemOptions): ListRowOptions {
  return {
    as: "button",
    title: o.name,
    mono: o.mono,
    meta: o.description,
    metaHTML: o.descriptionHTML,
    tagsHTML: o.tagsHTML,
    clamp: o.clamp ?? 2,
    current: o.current ? true : undefined,
    dim: o.dim,
    badgesHTML: o.badgesHTML,
    id: o.id,
    cls: o.cls,
    attrs: o.attrs,
  };
}

export function catalogItemHTML(o: CatalogItemOptions): string {
  return listRowHTML(catalogItemRow(o));
}

const ITEM = "button.row:not(:disabled)";

export function setCurrentItem(list: HTMLElement, item: HTMLElement | null): void {
  list.querySelectorAll<HTMLElement>("[aria-current]").forEach((el) => {
    if (el !== item) el.removeAttribute("aria-current");
  });
  item?.setAttribute("aria-current", "true");
}

export interface ItemListOptions {
  onPick?: (item: HTMLElement) => void;
}

// Handlers sit on the list so it can be redrawn; arrow keys stop propagating so
// page-wide shortcuts on the same keys do not move twice.
export function bindItemList(list: HTMLElement, o: ItemListOptions = {}): () => void {
  const items = () => [...list.querySelectorAll<HTMLElement>(ITEM)];
  const onClick = (e: Event) => {
    const item = (e.target as Element | null)?.closest<HTMLElement>(ITEM);
    if (!item || !list.contains(item)) return;
    setCurrentItem(list, item);
    o.onPick?.(item);
  };
  const onKey = (e: KeyboardEvent) => {
    const all = items();
    const at = all.indexOf(document.activeElement as HTMLElement);
    const last = all.length - 1;
    let next: number;
    if (e.key === "Home") next = 0;
    else if (e.key === "End") next = last;
    else if (e.key === "ArrowDown") next = at < 0 ? 0 : Math.min(at + 1, last);
    else if (e.key === "ArrowUp") next = at < 0 ? last : Math.max(at - 1, 0);
    else return;
    if (!all.length) return;
    e.preventDefault();
    e.stopPropagation();
    all[next].focus();
  };
  list.addEventListener("click", onClick);
  list.addEventListener("keydown", onKey);
  return () => {
    list.removeEventListener("click", onClick);
    list.removeEventListener("keydown", onKey);
  };
}

export type EntityHeadLevel = 1 | 2 | 3;

export function entityHeadTitleClasses(mono = false): string {
  return mono ? "entity-head-title entity-head-title-mono" : "entity-head-title";
}

export interface EntityHeadOptions {
  title?: string;
  titleHTML?: string;
  titleId?: string;
  level?: EntityHeadLevel;
  mono?: boolean;
  meta?: string;
  metaHTML?: string;
  metaId?: string;
  badgesHTML?: string;
  actions?: ButtonOptions[];
  actionsHTML?: string;
  id?: string;
  cls?: string;
  attrs?: Attrs;
}

export function entityHeadHTML(o: EntityHeadOptions): string {
  if (o.title === undefined && o.titleHTML === undefined) {
    throw new Error("catalog: an entity head needs a title"); // i18n-ignore a programming error
  }
  const h = `h${o.level ?? 2}`;
  const meta = o.metaHTML ?? (o.meta ? esc(o.meta) : "");
  const actions =
    (o.actions ?? []).map((a) => buttonHTML({ ...a, size: "sm" })).join("") + (o.actionsHTML ?? "");
  const own: Attrs = { id: o.id, class: ["entity-head", o.cls].filter(Boolean).join(" ") };
  return (
    `<div${attrsHTML(own)}${attrsHTML(o.attrs)}>` +
    `<div class="entity-head-text">` +
    `<${h}${attrsHTML({ id: o.titleId, class: entityHeadTitleClasses(o.mono) })}>${o.titleHTML ?? esc(o.title)}</${h}>` +
    (meta || o.metaId
      ? `<p${attrsHTML({ id: o.metaId, class: "entity-head-meta" })}>${meta}</p>`
      : "") +
    (o.badgesHTML ? `<div class="entity-head-badges">${o.badgesHTML}</div>` : "") +
    `</div>` +
    (actions ? `<div class="entity-head-actions">${actions}</div>` : "") +
    `</div>`
  );
}

export function saveStateText(dirty: boolean): string {
  return dirty ? t("ui.save_state.dirty") : t("ui.save_state.clean");
}

export function saveStateInnerHTML(dirty: boolean): string {
  return (
    dotHTML(dirty ? "warn" : "neutral") +
    `<span class="save-state-text">${esc(saveStateText(dirty))}</span>`
  );
}

export interface SaveStateOptions {
  id?: string;
  quiet?: boolean;
}

export function saveStateAttrs(dirty: boolean, quiet = false): Attrs {
  return {
    class: "save-state",
    role: "status",
    "data-dirty": String(dirty),
    "data-quiet": quiet ? "" : undefined,
    hidden: (quiet && !dirty) || undefined,
  };
}

export function saveStateHTML(dirty = false, o: SaveStateOptions = {}): string {
  const own: Attrs = { id: o.id, ...saveStateAttrs(dirty, o.quiet) };
  return `<span${attrsHTML(own)}>${saveStateInnerHTML(dirty)}</span>`;
}

export function setSaveState(el: HTMLElement, dirty: boolean): void {
  if (el.dataset.dirty === String(dirty)) return;
  el.dataset.dirty = String(dirty);
  el.innerHTML = saveStateInnerHTML(dirty);
  if (el.dataset.quiet !== undefined) el.hidden = !dirty;
}

export interface SaveFootOptions {
  id?: string;
  stateId?: string;
  dirty?: boolean;
  actions?: ButtonOptions[];
  actionsHTML?: string;
  cls?: string;
  attrs?: Attrs;
}

export function saveFootHTML(o: SaveFootOptions = {}): string {
  const own: Attrs = { id: o.id, class: ["save-foot", o.cls].filter(Boolean).join(" ") };
  const actions = (o.actions ?? []).map((a) => buttonHTML(a)).join("") + (o.actionsHTML ?? "");
  return (
    `<div${attrsHTML(own)}${attrsHTML(o.attrs)}>` +
    saveStateHTML(o.dirty ?? false, { id: o.stateId }) +
    `<div class="save-foot-actions">${actions}</div>` +
    `</div>`
  );
}

export interface MdEditorSave {
  label?: string;
  id: string;
  kbd?: string;
  disabled?: boolean;
  hidden?: boolean;
}

export interface MdEditorOptions {
  path: string;
  pathId: string;
  note?: string;
  textId: string;
  value?: string;
  readOnly?: boolean;
  back?: false | { label?: string; id?: string; attrs?: Attrs };
  stateId?: string;
  buttons?: ButtonOptions[];
  save?: MdEditorSave;
  box?: boolean;
  id?: string;
  cls?: string;
  attrs?: Attrs;
}

export function mdEditorClasses(box = false, cls?: string): string {
  return ["md-editor", box && "md-editor-box", cls].filter(Boolean).join(" ");
}

export function backButtonOptions(label?: string, id?: string, attrs?: Attrs): ButtonOptions {
  return { label: label ?? t("common.buttons.back"), icon: "chevron-left", size: "sm", id, attrs };
}

export function mdEditorHTML(o: MdEditorOptions): string {
  const back =
    o.back === false ? "" : buttonHTML(backButtonOptions(o.back?.label, o.back?.id, o.back?.attrs));
  const note = o.note ? `<span class="md-editor-note">${esc(o.note)}</span>` : "";
  const state = o.stateId ? saveStateHTML(false, { id: o.stateId, quiet: true }) : "";
  const buttons = (o.buttons ?? []).map((b) => buttonHTML({ ...b, size: "sm" })).join("");
  const save =
    o.save && !o.save.hidden
      ? buttonHTML({
          label: o.save.label ?? t("common.buttons.save"),
          variant: "primary",
          size: "sm",
          kbd: o.save.kbd,
          id: o.save.id,
          disabled: o.save.disabled,
        })
      : "";
  const own: Attrs = { id: o.id, class: mdEditorClasses(o.box, o.cls) };
  const text: Attrs = {
    id: o.textId,
    class: "md-editor-text",
    spellcheck: "false",
    "aria-labelledby": o.pathId,
    readonly: o.readOnly ? true : undefined,
  };
  return (
    `<div${attrsHTML(own)}${attrsHTML(o.attrs)}>` +
    `<div class="md-editor-bar">${back}` +
    `<span${attrsHTML({ id: o.pathId, class: "md-editor-path" })}>${esc(o.path)}${note}</span>` +
    `${state}${buttons}${save}</div>` +
    `<textarea${attrsHTML(text)}>${esc(o.value ?? "")}</textarea>` +
    `</div>`
  );
}

export type FileTone = "ok" | "warn";

export type FileRowVariant = "dashed" | "deleted";

const FILE_VARIANTS = new Set<string>(["dashed", "deleted"]);
const FILE_TONES = new Set<string>(["ok", "warn"]);

export function fileRowClasses(variant?: FileRowVariant, cls?: string): string {
  if (variant !== undefined && !FILE_VARIANTS.has(variant)) {
    throw new Error(`catalog: no file row "${variant}"`); // i18n-ignore a programming error
  }
  return ["file-row", variant && `file-row-${variant}`, cls].filter(Boolean).join(" ");
}

export function fileStatusClasses(mono?: boolean): string {
  return mono === false ? "file-status" : "file-status file-status-mono";
}

export function fileStatusHTML(
  o: { text?: string; html?: string; tone?: FileTone; mono?: boolean } = {},
): string {
  if (o.tone !== undefined && !FILE_TONES.has(o.tone)) {
    throw new Error(`catalog: no file tone "${o.tone}"`); // i18n-ignore a programming error
  }
  return (
    `<span${attrsHTML({ class: fileStatusClasses(o.mono), "data-tone": o.tone })}>` +
    (o.tone ? dotHTML(o.tone) : "") +
    `<span class="file-status-text">${o.html ?? esc(o.text ?? "")}</span></span>`
  );
}

export interface FileRowOptions {
  label: string;
  labelMono?: boolean;
  status?: string;
  statusHTML?: string;
  tone?: FileTone;
  statusMono?: boolean;
  variant?: FileRowVariant;
  actions?: ButtonOptions[];
  actionsHTML?: string;
  as?: "div" | "li";
  id?: string;
  cls?: string;
  attrs?: Attrs;
}

export function fileRowRow(o: FileRowOptions): ListRowOptions {
  const hasStatus = o.status !== undefined || o.statusHTML !== undefined;
  return {
    as: o.as,
    boxed: true,
    title: o.label,
    mono: o.labelMono,
    metaHTML: hasStatus
      ? fileStatusHTML({ text: o.status, html: o.statusHTML, tone: o.tone, mono: o.statusMono })
      : undefined,
    actions: o.actions,
    actionsHTML: o.actionsHTML,
    id: o.id,
    cls: fileRowClasses(o.variant, o.cls),
    attrs: o.attrs,
  };
}

export function fileRowHTML(o: FileRowOptions): string {
  return listRowHTML(fileRowRow(o));
}

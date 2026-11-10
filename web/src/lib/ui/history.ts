// Keep historyViewHTML() in sync with HistoryView.astro and styles/primitives/history.css.

import { buttonHTML, type ButtonOptions } from "./button";
import { backButtonOptions, bindItemList } from "./catalog";
import { codeBlockHTML } from "./code";
import { disclosureHTML } from "./disclosure";
import { attrsHTML, esc, type Attrs } from "./dom";
import { emptyRowHTML, emptyStateHTML } from "./empty";
import { listRowHTML } from "./list-row";

export interface HistoryViewOptions {
  title: string;
  pick: string;
  bar?: boolean;
  backLabel?: string;
  boxed?: boolean;
  id?: string;
  cls?: string;
  attrs?: Attrs;
}

export function historyViewClasses(boxed = false, cls?: string): string {
  return ["rev-view", boxed && "rev-view-boxed", cls].filter(Boolean).join(" ");
}

export function historyViewHTML(o: HistoryViewOptions): string {
  const own: Attrs = { id: o.id, class: historyViewClasses(o.boxed, o.cls) };
  const bar =
    o.bar === false
      ? ""
      : `<div class="md-editor-bar">` +
        buttonHTML(backButtonOptions(o.backLabel, undefined, { "data-rev-back": true })) +
        `<span class="md-editor-path">${esc(o.title)}</span></div>`;
  return (
    `<div${attrsHTML(own)}${attrsHTML(o.attrs)}>${bar}` +
    `<div class="rev-body">` +
    `<ul${attrsHTML({ class: "rev-list", "data-rev-list": true, "aria-label": o.title })}>` +
    emptyRowHTML(undefined, { as: "li", loading: true }) +
    `</ul>` +
    `<div class="rev-preview" data-rev-preview>${emptyStateHTML({ size: "sm", body: o.pick })}</div>` +
    `</div></div>`
  );
}

export interface HistoryParts {
  view: HTMLElement;
  list: HTMLElement;
  preview: HTMLElement;
}

function part(root: HTMLElement, sel: string): HTMLElement {
  const el = root.querySelector<HTMLElement>(sel);
  if (!el) throw new Error(`history: ${sel} missing`); // i18n-ignore a programming error
  return el;
}

export function openHistoryView(
  host: HTMLElement,
  o: HistoryViewOptions & { onBack?: () => void | Promise<void> },
): HistoryParts {
  host.innerHTML = historyViewHTML(o);
  const view = part(host, ".rev-view");
  if (o.bar !== false && o.onBack) {
    const back = o.onBack;
    part(view, "[data-rev-back]").addEventListener("click", () => void back());
  }
  return { view, list: part(view, "[data-rev-list]"), preview: part(view, "[data-rev-preview]") };
}

export interface HistoryRowOptions {
  action: string;
  meta?: string;
  metaHTML?: string;
  badgesHTML?: string;
  tone?: "danger";
  current?: boolean;
}

export function historyRowHTML(o: HistoryRowOptions, index: number): string {
  return `<li>${listRowHTML({
    as: "button",
    size: "sm",
    title: o.action,
    meta: o.meta,
    metaHTML: o.metaHTML,
    badgesHTML: o.badgesHTML,
    current: o.current ? true : undefined,
    cls: o.tone === "danger" ? "rev-row-danger" : undefined,
    attrs: { "data-rev": index },
  })}</li>`;
}

// Relies on the daemon sending revisions newest first; no sorting here.
export function historyRowsHTML(rows: HistoryRowOptions[]): string {
  return rows.map((r, i) => historyRowHTML(r, i)).join("");
}

export function historyListError(list: HTMLElement, message: string): void {
  list.innerHTML = emptyRowHTML(message, { as: "li", tone: "danger" });
}

export function fillHistoryList(
  list: HTMLElement,
  rows: HistoryRowOptions[],
  o: { none: string; onPick: (index: number) => void },
): () => void {
  if (!rows.length) {
    list.innerHTML = emptyRowHTML(o.none, { as: "li" });
    return () => undefined;
  }
  list.innerHTML = historyRowsHTML(rows);
  return bindItemList(list, { onPick: (item) => o.onPick(Number(item.dataset.rev)) });
}

export type HistoryBody = { text: string } | { html: string } | { empty: string };

export interface HistoryFile {
  path: string;
  text: string;
}

export interface HistoryPreviewOptions {
  when: string;
  whenHTML?: string;
  body: HistoryBody;
  facts?: string[];
  files?: HistoryFile[];
  action?: ButtonOptions;
}

export function historyPreviewHTML(o: HistoryPreviewOptions): string {
  const action = o.action
    ? buttonHTML({
        ...o.action,
        variant: o.action.variant ?? "primary",
        size: "sm",
        attrs: o.action.attrs ?? { "data-rev-restore": true },
      })
    : "";
  let body: string;
  if ("text" in o.body) body = `<pre class="rev-text">${esc(o.body.text)}</pre>`;
  else if ("html" in o.body) body = `<div class="rev-text rev-facts">${o.body.html}</div>`;
  else body = emptyStateHTML({ size: "sm", body: o.body.empty });
  const when = o.whenHTML ?? esc(o.when);
  const facts = o.facts?.length
    ? `<ul class="rev-lead">${o.facts.map((f) => `<li>${esc(f)}</li>`).join("")}</ul>`
    : "";
  return (
    `<div class="rev-preview-head"><span class="rev-when">${when}</span>${action}</div>` +
    facts +
    body +
    historyFilesHTML(o.files ?? [])
  );
}

export function historyFilesHTML(files: HistoryFile[]): string {
  if (!files.length) return "";
  const each = files.map((f) =>
    disclosureHTML({
      variant: "code",
      summary: f.path,
      cls: "rev-file",
      bodyHTML: codeBlockHTML({ text: f.text, multiline: true, maxHeight: "sm" }),
    }),
  );
  return `<div class="rev-files">${each.join("")}</div>`;
}

export function historyHeadingHTML(text: string): string {
  return `<h5 class="rev-heading">${esc(text)}</h5>`;
}

export function historyNoteHTML(text: string): string {
  return `<p class="rev-note">${esc(text)}</p>`;
}

export function historyChangeHTML(
  before: string,
  after: string,
  labels: { before: string; after: string; empty: string },
): string {
  const value = (v: string) => (v ? esc(v) : `<em>${esc(labels.empty)}</em>`);
  return (
    `<span class="rev-before">${esc(labels.before)} ${value(before)}</span>` +
    `<span class="rev-after">${esc(labels.after)} ${value(after)}</span>`
  );
}

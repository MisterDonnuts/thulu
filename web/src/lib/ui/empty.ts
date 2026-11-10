// Keep in sync with EmptyState.astro, EmptyRow.astro and styles/primitives/empty.css.
// Pollers find placeholders by .row-empty; do not rename it.

import { t } from "../../i18n";
import { buttonHTML, type ButtonOptions } from "./button";
import { attrsHTML, esc, type Attrs } from "./dom";
import { icoSvg, type IconSize } from "./icon";

export type EmptySize = "block" | "sm" | "inline";

export type EmptyTone = "neutral" | "danger";

export type EmptyTitleTag = "p" | "h2" | "h3";

const SIZES = new Set<string>(["block", "sm", "inline"]);
const TITLE_TAGS = new Set<string>(["p", "h2", "h3"]);

export function emptyTitleTag(tag: EmptyTitleTag = "p"): EmptyTitleTag {
  if (!TITLE_TAGS.has(tag)) throw new Error(`empty: no title tag "${tag}"`); // i18n-ignore a programming error
  return tag;
}
const TONES = new Set<string>(["neutral", "danger"]);

export interface EmptyStateLook {
  size?: EmptySize;
  tone?: EmptyTone;
  dashed?: boolean;
  cls?: string;
}

export function emptyStateClasses(look: EmptyStateLook = {}): string {
  const { size = "block", tone = "neutral", dashed = false, cls } = look;
  if (!SIZES.has(size)) throw new Error(`empty: no size "${size}"`); // i18n-ignore a programming error
  if (!TONES.has(tone)) throw new Error(`empty: no tone "${tone}"`); // i18n-ignore a programming error
  return [
    "empty-state",
    size !== "block" && `empty-state-${size}`,
    dashed && "empty-state-dashed",
    tone === "danger" && "empty-state-danger",
    cls,
  ]
    .filter(Boolean)
    .join(" ");
}

export function emptySteps(size: EmptySize = "block", steps?: EmptyStep[]): void {
  if (steps?.length && size !== "block") throw new Error("empty: steps need the block size"); // i18n-ignore a programming error
}

export function emptyIconSize(size: EmptySize = "block"): IconSize {
  return size === "block" ? "xl" : size === "sm" ? "lg" : "md";
}

export interface EmptyA11yInput {
  tone?: EmptyTone;
  loading?: boolean;
  as?: string;
}

// A <tr> cannot take a live-region role, so it only gets aria-busy.
export function emptyA11y(input: EmptyA11yInput): Attrs {
  const { tone = "neutral", loading = false, as = "div" } = input;
  const role =
    as === "tr" ? undefined : tone === "danger" ? "alert" : loading ? "status" : undefined;
  return { role, "aria-busy": loading ? "true" : undefined };
}

export function loadingText(): string {
  return t("common.status.loading");
}

export interface EmptyStep {
  title: string;
  body?: string;
}

function emptyStepHTML(n: number, st: EmptyStep): string {
  const body = st.body ? `<span class="empty-state-step-body">${esc(st.body)}</span>` : "";
  return (
    `<li class="empty-state-step"><span class="empty-state-step-n" aria-hidden="true">${n}</span>` +
    `<strong class="empty-state-step-title">${esc(st.title)}</strong>${body}</li>`
  );
}

export function emptyStepsHTML(steps: EmptyStep[] | undefined): string {
  if (!steps?.length) return "";
  const items = Array.from(steps.entries(), ([i, st]) => emptyStepHTML(i + 1, st)).join("");
  return `<ol class="empty-state-steps">${items}</ol>`;
}

export interface EmptyStateOptions extends EmptyStateLook {
  title?: string;
  titleTag?: EmptyTitleTag;
  body?: string;
  bodyHTML?: string;
  icon?: string;
  steps?: EmptyStep[];
  action?: ButtonOptions;
  actionHTML?: string;
  loading?: boolean;
  as?: "div" | "li";
  id?: string;
  titleId?: string;
  bodyId?: string;
  attrs?: Attrs;
}

export function emptyStateHTML(o: EmptyStateOptions = {}): string {
  const tag = o.as ?? "div";
  const size = o.size ?? "block";
  emptySteps(size, o.steps);
  const titleTag = emptyTitleTag(o.titleTag);
  const own: Attrs = {
    id: o.id,
    class: emptyStateClasses({ size, tone: o.tone, dashed: o.dashed, cls: o.cls }),
    ...emptyA11y({ tone: o.tone, loading: o.loading, as: tag }),
  };
  const title = o.title ?? (o.loading ? loadingText() : undefined);
  const body = o.bodyHTML ?? (o.body !== undefined ? esc(o.body) : undefined);
  const hasTitle = title !== undefined || o.titleId !== undefined;
  const hasBody = body !== undefined || o.bodyId !== undefined;
  const icon = o.icon ? icoSvg(o.icon, { size: emptyIconSize(size), cls: "empty-state-icon" }) : "";
  const text =
    hasTitle || hasBody
      ? `<div class="empty-state-text">` +
        (hasTitle
          ? `<${titleTag}${attrsHTML({ id: o.titleId, class: "empty-state-title" })}>${esc(title)}</${titleTag}>`
          : "") +
        (hasBody
          ? `<p${attrsHTML({ id: o.bodyId, class: "empty-state-body" })}>${body ?? ""}</p>`
          : "") +
        `</div>`
      : "";
  const actionInner = o.actionHTML ?? (o.action ? buttonHTML(o.action) : "");
  const action = actionInner ? `<div class="empty-state-action">${actionInner}</div>` : "";
  const steps = emptyStepsHTML(o.steps);
  return `<${tag}${attrsHTML({ ...own, ...o.attrs })}>${icon}${text}${steps}${action}</${tag}>`;
}

export interface EmptyRowOptions {
  as?: "tr" | "li" | "div";
  cols?: number;
  loading?: boolean;
  tone?: EmptyTone;
  html?: string;
  id?: string;
  cls?: string;
  attrs?: Attrs;
}

export function emptyRowClasses(tone: EmptyTone = "neutral", cls?: string): string {
  if (!TONES.has(tone)) throw new Error(`empty: no tone "${tone}"`); // i18n-ignore a programming error
  return ["row-empty", tone === "danger" && "row-empty-danger", cls].filter(Boolean).join(" ");
}

export function emptyRowHTML(text?: string, o: EmptyRowOptions = {}): string {
  const tag = o.as ?? "tr";
  const shown = o.html ?? esc(text ?? (o.loading ? loadingText() : ""));
  const own: Attrs = {
    id: o.id,
    class: emptyRowClasses(o.tone, o.cls),
    ...emptyA11y({ tone: o.tone, loading: o.loading, as: tag }),
  };
  const open = `<${tag}${attrsHTML({ ...own, ...o.attrs })}>`;
  if (tag === "tr") return `${open}<td${attrsHTML({ colspan: o.cols })}>${shown}</td></tr>`;
  return `${open}${shown}</${tag}>`;
}

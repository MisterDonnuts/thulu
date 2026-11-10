import { attrsHTML, type Attrs } from "./dom";

export type SkeletonShape = "line" | "block" | "chip";

export type SkeletonTag = "span" | "div" | "li";

const SHAPES = new Set<string>(["line", "block", "chip"]);
const TAGS = new Set<string>(["span", "div", "li"]);

export function skeletonClasses(shape: SkeletonShape = "line", cls?: string): string {
  if (!SHAPES.has(shape)) throw new Error(`skeleton: no shape "${shape}"`); // i18n-ignore a programming error
  return ["skeleton", `skeleton-${shape}`, cls].filter(Boolean).join(" ");
}

export function skeletonTag(tag: SkeletonTag = "span"): SkeletonTag {
  if (!TAGS.has(tag)) throw new Error(`skeleton: no tag "${tag}"`); // i18n-ignore a programming error
  return tag;
}

export function skeletonLineCount(shape: SkeletonShape, lines: number | undefined): number {
  if (lines === undefined || lines <= 1) return 1;
  if (shape !== "line") throw new Error("skeleton: only a line skeleton takes lines"); // i18n-ignore a programming error
  return Math.floor(lines);
}

export interface SkeletonOptions {
  shape?: SkeletonShape;
  lines?: number;
  tag?: SkeletonTag;
  cls?: string;
  attrs?: Attrs;
}

export function skeletonHTML(o: SkeletonOptions = {}): string {
  const { shape = "line", cls } = o;
  const tag = skeletonTag(o.tag);
  const n = skeletonLineCount(shape, o.lines);
  if (n === 1) {
    const own: Attrs = { class: skeletonClasses(shape, cls), "aria-hidden": "true" };
    return `<${tag}${attrsHTML(own)}${attrsHTML(o.attrs)}></${tag}>`;
  }
  const own: Attrs = {
    class: ["skeleton-lines", cls].filter(Boolean).join(" "),
    "aria-hidden": "true",
  };
  const bar = `<span class="${skeletonClasses("line")}"></span>`;
  return `<${tag}${attrsHTML(own)}${attrsHTML(o.attrs)}>${bar.repeat(n)}</${tag}>`;
}

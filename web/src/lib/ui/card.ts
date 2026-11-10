// cardHTML() must write the same markup as Card.astro.

import { attrsHTML, esc, type Attrs } from "./dom";

export type CardReveal = "always" | "hover";

const REVEALS = new Set<string>(["always", "hover"]);
const TAGS = new Set<string>(["article", "div", "li", "section"]);
const HEADINGS = new Set<string>(["h2", "h3", "h4"]);

export interface CardLook {
  link?: boolean;
  reveal?: CardReveal;
  dashed?: boolean;
  off?: boolean;
  target?: boolean;
  cls?: string;
}

export const CARD_TARGET = "card-target";

function check(set: Set<string>, value: string, what: string): void {
  if (!set.has(value)) throw new Error(`card: no ${what} "${value}"`); // i18n-ignore a programming error
}

export function cardClasses(look: CardLook = {}): string {
  const {
    link = false,
    reveal = "always",
    dashed = false,
    off = false,
    target = false,
    cls,
  } = look;
  check(REVEALS, reveal, "reveal");
  return [
    "card",
    link && "card-link",
    reveal === "hover" && "card-reveal",
    dashed && "card-dashed",
    off && "card-off",
    target && CARD_TARGET,
    cls,
  ]
    .filter(Boolean)
    .join(" ");
}

export type CardTag = "article" | "div" | "li" | "section";
export type CardHeading = "h2" | "h3" | "h4";

export function cardTags(
  tag: CardTag = "article",
  heading: CardHeading = "h3",
): { tag: CardTag; heading: CardHeading } {
  check(TAGS, tag, "tag");
  check(HEADINGS, heading, "heading");
  return { tag, heading };
}

export interface CardOptions extends Omit<CardLook, "link"> {
  title?: string;
  heading?: CardHeading;
  href?: string;
  headHTML?: string;
  mediaHTML?: string;
  bodyHTML?: string;
  footHTML?: string;
  tag?: CardTag;
  id?: string;
  attrs?: Attrs;
}

export function cardHTML(o: CardOptions): string {
  const { tag, heading } = cardTags(o.tag, o.heading);
  const link = o.href !== undefined;
  const own: Attrs = {
    id: o.id,
    class: cardClasses({
      link,
      reveal: o.reveal,
      dashed: o.dashed,
      off: o.off,
      target: o.target,
      cls: o.cls,
    }),
  };
  const media = o.mediaHTML ? `<div class="card-media">${o.mediaHTML}</div>` : "";
  let title = "";
  if (o.title !== undefined) {
    const name = link
      ? `<a class="card-cover" href="${esc(o.href)}">${esc(o.title)}</a>`
      : esc(o.title);
    title = `<${heading}>${name}</${heading}>`;
  }
  const head =
    title || o.headHTML ? `<div class="card-head">${title}${o.headHTML ?? ""}</div>` : "";
  const body = o.bodyHTML ? `<div class="card-body">${o.bodyHTML}</div>` : "";
  const foot = o.footHTML ? `<div class="card-foot">${o.footHTML}</div>` : "";
  return `<${tag}${attrsHTML(own)}${attrsHTML(o.attrs)}>${media}${head}${body}${foot}</${tag}>`;
}

import { t } from "../../i18n";
import { attrsHTML, esc, type Attrs } from "./dom";
import { icoSvg } from "./icon";

// Each bucket needs its --tag-<bucket>-bg/-fg pair in tokens.css, for both themes.
export const CHIP_COLORS = [
  "gray",
  "brown",
  "orange",
  "yellow",
  "green",
  "blue",
  "purple",
  "pink",
  "red",
] as const;

export type TagBucket = (typeof CHIP_COLORS)[number];

export function chipColor(name: string): TagBucket {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = ((h << 5) - h + name.charCodeAt(i)) | 0;
  return CHIP_COLORS[Math.abs(h) % CHIP_COLORS.length];
}

const HEX = /^#[0-9a-f]{3,8}$/i;
const BUCKETS = new Set<string>(CHIP_COLORS);

// Only #hex values from data may be written into a style attribute; anything else could inject CSS.
export function isHexColor(value: string | null | undefined): value is string {
  return !!value && HEX.test(value);
}

function isBucket(value: string): value is TagBucket {
  return BUCKETS.has(value);
}

export type TagSize = "sm" | "md" | "lg";

export type TagTone = "neutral" | "muted" | "special" | "warn" | "danger";

export interface TagLook {
  color?: string;
  tone?: TagTone;
  size?: TagSize;
  inline?: boolean;
  dashed?: boolean;
  mono?: boolean;
  removable?: boolean;
  cls?: string;
}

export function tagClasses(look: TagLook = {}): string {
  const { color, tone = "neutral", size = "md", inline, dashed, mono, removable, cls } = look;
  if (color && tone !== "neutral") {
    throw new Error("tag: a colour and a tone cannot both paint a tag"); // i18n-ignore a programming error
  }
  let paint: string | false = tone !== "neutral" && `tag-${tone}`;
  if (color && isBucket(color)) paint = `tag-${color}`;
  else if (isHexColor(color)) paint = "tag-tint";
  return [
    "tag",
    size !== "md" && `tag-${size}`,
    paint,
    inline && "tag-inline",
    dashed && "tag-dashed",
    mono && "tag-mono",
    removable && "tag-removable",
    cls,
  ]
    .filter(Boolean)
    .join(" ");
}

export function tagColorStyle(color: string | undefined): string | undefined {
  return isHexColor(color) ? `--tag-color:${color}` : undefined;
}

export function tagDot(dot: boolean | string | undefined): {
  cls: string;
  style?: string;
} | null {
  if (!dot) return null;
  if (dot === true) return { cls: "tag-dot" };
  if (isBucket(dot)) return { cls: `tag-dot tag-dot-${dot}` };
  if (isHexColor(dot)) return { cls: "tag-dot", style: `--tag-mark:${dot}` };
  return { cls: "tag-dot" };
}

export function tagRemoveLabel(label: string): string {
  return t("ui.tag.remove", { name: label });
}

export interface TagRemove {
  label?: string;
  attrs?: Attrs;
}

export interface TagOptions extends Omit<TagLook, "removable"> {
  dot?: boolean | string;
  icon?: string;
  href?: string;
  button?: boolean;
  pressed?: boolean;
  disabled?: boolean;
  removable?: boolean | TagRemove;
  title?: string;
  id?: string;
  attrs?: Attrs;
}

export type TagElement = "a" | "button" | "span";

export function tagElement(
  opts: Pick<TagOptions, "href" | "button" | "pressed" | "removable">,
): TagElement {
  const interactive = !!opts.href || !!opts.button || opts.pressed !== undefined;
  if (interactive && opts.removable) {
    throw new Error("tag: a removable tag cannot be a link or a button"); // i18n-ignore a programming error
  }
  if (opts.href) return "a";
  return interactive ? "button" : "span";
}

export function tagHTML(label: string, opts: TagOptions = {}): string {
  const el = tagElement(opts);
  const cls = tagClasses({ ...opts, removable: !!opts.removable });
  const pressed = opts.pressed === undefined ? undefined : String(opts.pressed);
  const own: Attrs = {
    id: opts.id,
    class: cls,
    style: tagColorStyle(opts.color),
    href: el === "a" ? opts.href : undefined,
    type: el === "button" ? "button" : undefined,
    "aria-pressed": pressed,
    title: opts.title,
    disabled: el === "button" && opts.disabled ? true : undefined,
  };
  const check = pressed !== undefined ? icoSvg("check", { cls: "tag-check" }) : "";
  const d = tagDot(opts.dot);
  const dot = d
    ? `<span${attrsHTML({ class: d.cls, style: d.style, "aria-hidden": "true" })}></span>`
    : "";
  const icon = opts.icon ? icoSvg(opts.icon) : "";
  let x = "";
  if (opts.removable) {
    const r = opts.removable === true ? {} : opts.removable;
    const xAttrs: Attrs = {
      type: "button",
      class: "tag-x",
      "aria-label": r.label ?? tagRemoveLabel(label),
    };
    x = `<button${attrsHTML(xAttrs)}${attrsHTML(r.attrs)}>${icoSvg("x")}</button>`;
  }
  return `<${el}${attrsHTML(own)}${attrsHTML(opts.attrs)}>${check}${dot}${icon}<span class="tag-label">${esc(label)}</span>${x}</${el}>`;
}

export type PriorityLevel = "urgent" | "high" | "medium" | "low";

const PRIORITY_LOOK: Record<PriorityLevel, { tone: TagTone; icon: string }> = {
  urgent: { tone: "danger", icon: "i-alert" },
  high: { tone: "warn", icon: "i-arrow-up" },
  medium: { tone: "neutral", icon: "i-minus" },
  low: { tone: "muted", icon: "i-arrow-down" },
};

export type PriorityTagOptions = Pick<TagOptions, "size" | "title" | "cls" | "attrs"> & {
  pill?: boolean;
};

export function priorityLevelLook(level: string): { tone: TagTone; icon: string } | null {
  return Object.hasOwn(PRIORITY_LOOK, level) ? PRIORITY_LOOK[level as PriorityLevel] : null;
}

export function priorityTagHTML(
  label: string,
  level: string,
  opts: PriorityTagOptions = {},
): string {
  const look = priorityLevelLook(level);
  if (!look) return "";
  const { pill, ...rest } = opts;
  return tagHTML(label, { ...rest, tone: look.tone, icon: look.icon, inline: !pill });
}

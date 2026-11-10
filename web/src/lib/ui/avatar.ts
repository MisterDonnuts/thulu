// avatarHTML() must write the same markup as Avatar.astro.

import { t } from "../../i18n";
import { relTime } from "../activity";
import { fmtDate } from "../time";
import { attrsHTML, esc, type Attrs } from "./dom";
import { icoSvg } from "./icon";

export type AvatarKind = "person" | "agent" | "system";

export type AvatarSize = "sm" | "md";

export type AvatarShape = "round" | "square";

export type AvatarStatus = "ok" | "warn" | "danger" | "muted";

const SIZES = new Set<string>(["sm", "md"]);
const SHAPES = new Set<string>(["round", "square"]);

export function initialsOf(name: string | null | undefined): string {
  const src = (name ?? "").trim();
  const base = src.includes("@") ? src.split("@")[0] : src;
  const words = base.split(/[\s._-]+/).filter(Boolean);
  if (words.length === 0) return "?";
  const letters =
    words.length === 1
      ? Array.from(words[0]).slice(0, 2).join("")
      : Array.from(words[0])[0] + Array.from(words[1])[0];
  return letters.toLocaleUpperCase();
}

export interface AvatarLook {
  kind?: AvatarKind;
  size?: AvatarSize;
  shape?: AvatarShape;
  pending?: boolean;
  cls?: string;
}

export function avatarClasses(look: AvatarLook = {}): string {
  const { kind = "person", size = "sm", shape = "round", pending = false, cls } = look;
  if (!SIZES.has(size)) throw new Error(`avatar: no size "${size}"`); // i18n-ignore a programming error
  if (!SHAPES.has(shape)) throw new Error(`avatar: no shape "${shape}"`); // i18n-ignore a programming error
  return [
    "avatar",
    size !== "sm" && `avatar-${size}`,
    shape === "square" && "avatar-square",
    kind !== "person" && `avatar-${kind}`,
    pending && "avatar-pending",
    cls,
  ]
    .filter(Boolean)
    .join(" ");
}

export interface AvatarContent {
  name?: string;
  kind?: AvatarKind;
  pending?: boolean;
  icon?: string;
  letters?: boolean;
}

export function avatarMark(c: AvatarContent): { icon: string } | { text: string } {
  if (c.icon) return { icon: c.icon };
  if (c.pending) return { icon: "i-send" };
  const kind = c.kind ?? "person";
  if (kind === "person" || c.letters) return { text: initialsOf(c.name) };
  return { icon: kind === "agent" ? "i-bot" : "i-octopus" };
}

export function avatarA11y(label?: string): Attrs {
  return label ? { role: "img", "aria-label": label } : { "aria-hidden": "true" };
}

export interface AvatarOptions extends AvatarLook, AvatarContent {
  status?: AvatarStatus;
  pulse?: boolean;
  label?: string;
  attrs?: Attrs;
}

export function avatarStatusClasses(pulse?: boolean): string {
  return pulse ? "avatar-status avatar-status-pulse" : "avatar-status";
}

export function avatarHTML(opts: AvatarOptions = {}): string {
  const attrs = { class: avatarClasses(opts), ...avatarA11y(opts.label), ...opts.attrs };
  const mark = avatarMark(opts);
  const inner = "icon" in mark ? icoSvg(mark.icon, { size: "sm" }) : esc(mark.text);
  const dot = opts.status
    ? `<span class="${avatarStatusClasses(opts.pulse)}" data-tone="${esc(opts.status)}"></span>`
    : "";
  return `<span${attrsHTML(attrs)}>${inner}${dot}</span>`;
}

export interface AuthorLine {
  name: string;
  kind: AvatarKind;
  at?: string;
  stage?: string;
  avatar?: boolean | AvatarOptions;
}

export function authorTimeHTML(iso: string, now: number): string {
  if (!iso) return "";
  return `<time class="author-line-time" datetime="${esc(iso)}" data-ts="${esc(iso)}" title="${esc(fmtDate(iso))}">${esc(relTime(iso, now))}</time>`;
}

export function authorLineHTML(a: AuthorLine, now = Date.now()): string {
  const av =
    a.avatar === false || a.avatar === undefined
      ? ""
      : avatarHTML({ name: a.name, kind: a.kind, ...(a.avatar === true ? {} : a.avatar) });
  const kind =
    a.kind === "agent" ? `<span class="author-line-kind">${esc(t("ui.author.agent"))}</span>` : "";
  const stage = a.stage
    ? `<span class="author-line-in">${esc(t("ui.author.in_stage", { stage: a.stage }))}</span>`
    : "";
  return `<div class="author-line">${av}<span class="author-line-name">${esc(a.name)}</span>${kind}${a.at ? authorTimeHTML(a.at, now) : ""}${stage}</div>`;
}

// buttonHTML() must write the same markup as Button.astro.

import { attrsHTML, esc, type Attrs } from "./dom";
import { icoSvg } from "./icon";
import { kbdHTML, keyShortcuts } from "./kbd";

export type ButtonVariant = "default" | "primary" | "ghost" | "danger" | "link";

export type ButtonSize = "sm" | "default" | "md";

export type IconButtonSize = "xs" | "sm" | "default" | "lg";

export interface ButtonLook {
  variant?: ButtonVariant;
  size?: ButtonSize | IconButtonSize;
  iconOnly?: boolean;
  cls?: string;
}

const TEXT_SIZES = new Set<string>(["sm", "default", "md"]);
const ICON_SIZES = new Set<string>(["xs", "sm", "default", "lg"]);

export function buttonClasses(look: ButtonLook = {}): string {
  const { variant = "default", size = "default", iconOnly = false, cls } = look;
  if (variant === "link" && (iconOnly || size !== "default"))
    throw new Error("button: a link is a text button at the default size"); // i18n-ignore a programming error
  const sizes = iconOnly ? ICON_SIZES : TEXT_SIZES;
  if (!sizes.has(size)) {
    const kind = iconOnly ? "icon-only" : "text"; // i18n-ignore a programming error
    throw new Error(`button: no size "${size}" for ${kind} buttons`); // i18n-ignore a programming error
  }
  const parts = iconOnly
    ? [
        "icon-btn",
        size !== "default" && `icon-btn-${size}`,
        variant === "primary" ? "btn-primary" : variant !== "default" && `icon-btn-${variant}`,
      ]
    : ["btn", variant !== "default" && `btn-${variant}`, size !== "default" && `btn-${size}`];
  return [...parts, cls].filter(Boolean).join(" ");
}

export interface ButtonA11yInput {
  iconOnly?: boolean;
  label?: string;
  icon?: string;
  title?: string;
  kbd?: string;
  href?: string;
  disabled?: boolean;
}

export function buttonA11y(input: ButtonA11yInput): Attrs {
  const { iconOnly, label, icon, title, kbd, href, disabled } = input;
  if (iconOnly && !label) throw new Error("button: an icon-only button needs a label"); // i18n-ignore a programming error
  if (iconOnly && !icon) throw new Error("button: an icon-only button needs an icon"); // i18n-ignore a programming error
  return {
    href: href && !disabled ? href : undefined,
    disabled: !href && disabled ? true : undefined,
    "aria-disabled": href && disabled ? "true" : undefined,
    title: title ?? (iconOnly ? label : undefined),
    "aria-label": iconOnly ? label : undefined,
    "aria-keyshortcuts": keyShortcuts(kbd),
  };
}

interface ButtonCommon {
  variant?: ButtonVariant;
  icon?: string;
  href?: string;
  type?: "button" | "submit" | "reset";
  disabled?: boolean;
  id?: string;
  cls?: string;
  title?: string;
  attrs?: Attrs;
}

export interface TextButtonOptions extends ButtonCommon {
  label: string;
  iconOnly?: false;
  size?: ButtonSize;
  kbd?: string;
  trailingHTML?: string;
}

export interface IconButtonOptions extends ButtonCommon {
  label: string;
  iconOnly: true;
  icon: string;
  size?: IconButtonSize;
}

export type ButtonOptions = TextButtonOptions | IconButtonOptions;

export function buttonHTML(o: ButtonOptions): string {
  const kbd = o.iconOnly ? undefined : o.kbd;
  const trailing = o.iconOnly ? "" : (o.trailingHTML ?? "");
  const tag = o.href ? "a" : "button";
  const own: Attrs = {
    id: o.id,
    class: buttonClasses({ variant: o.variant, size: o.size, iconOnly: o.iconOnly, cls: o.cls }),
    type: o.href ? undefined : (o.type ?? "button"),
    ...buttonA11y({
      iconOnly: o.iconOnly,
      label: o.label,
      icon: o.icon,
      title: o.title,
      kbd,
      href: o.href,
      disabled: o.disabled,
    }),
  };
  const glyph = o.icon ? icoSvg(o.icon) : "";
  const text = o.iconOnly ? "" : esc(o.label);
  const hint = kbd ? kbdHTML(kbd, { decorative: true }) : "";
  return `<${tag}${attrsHTML(own)}${attrsHTML(o.attrs)}>${glyph}${text}${trailing}${hint}</${tag}>`;
}

export function buttonEl(o: ButtonOptions & { href?: undefined }): HTMLButtonElement;
export function buttonEl(o: ButtonOptions & { href: string }): HTMLAnchorElement;
export function buttonEl(o: ButtonOptions): HTMLButtonElement | HTMLAnchorElement {
  const tpl = document.createElement("template");
  tpl.innerHTML = buttonHTML(o);
  return tpl.content.firstElementChild as HTMLButtonElement | HTMLAnchorElement;
}

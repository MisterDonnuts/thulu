// Keep in sync with Icon.astro; the sprite comes from IconSprite.astro.

import { esc } from "./dom";

export type IconSize = "sm" | "md" | "lg" | "xl";

export interface IconOptions {
  size?: IconSize;
  cls?: string;
  label?: string;
}

export function iconHref(name: string): string {
  return name.startsWith("i-") ? `#${name}` : `#i-${name}`;
}

export function iconClasses(size: IconSize = "md", cls?: string): string {
  return ["ico", size !== "md" && `ico-${size}`, cls].filter(Boolean).join(" ");
}

export function icoSvg(name: string, opts: IconOptions | string = {}): string {
  const o: IconOptions = typeof opts === "string" ? {} : opts;
  const cls = typeof opts === "string" ? opts : iconClasses(o.size, o.cls);
  const a11y = o.label ? ` role="img" aria-label="${esc(o.label)}"` : ` aria-hidden="true"`;
  return `<svg class="${esc(cls)}"${a11y}><use href="${esc(iconHref(name))}"/></svg>`;
}

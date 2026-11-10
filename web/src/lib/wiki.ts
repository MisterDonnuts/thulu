// Where the docs live. Empty builds no link: wikiUrl() returns "" and callers omit it.
export const WIKI_BASE = "https://thulu.dev";

export function wikiUrl(path: string, anchor?: string, base: string = WIKI_BASE): string {
  if (!base) return "";
  const p = path.startsWith("/") ? path : `/${path}`;
  return `${base.replace(/\/+$/, "")}${p}${anchor ? `#${anchor}` : ""}`;
}

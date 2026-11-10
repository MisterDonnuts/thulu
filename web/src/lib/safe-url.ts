// "media" refuses same-origin paths other than attachments: an <img> must not GET an arbitrary API
// route on the reader's behalf.
export type UrlKind = "link" | "media";

const schemes: Record<UrlKind, readonly string[]> = {
  link: ["http:", "https:", "mailto:"],
  media: ["http:", "https:"],
};

const attachmentPath = /^\/task-attachments\/[A-Za-z0-9_-]+(?:[/?#]|$)/;

export function safeUrl(raw: unknown, kind: UrlKind = "link"): string {
  if (typeof raw !== "string") return "";
  const u = raw.trim();
  if (!u) return "";
  // The URL parser treats \ as / and drops tabs/newlines, so "/\evil.com" and "/\t/evil.com" are
  // protocol-relative; normalize before checking.
  const flat = u.replace(/[\t\n\r]/g, "").replace(/\\/g, "/");
  if (flat.startsWith("#")) return kind === "link" ? u : "";
  if (flat.startsWith("/")) {
    if (flat.startsWith("//")) return "";
    if (kind === "media") return attachmentPath.test(flat) ? u : "";
    return u;
  }
  let parsed: URL;
  try {
    // No base on purpose: relative input not caught above fails to parse and is refused.
    parsed = new URL(u);
  } catch {
    return "";
  }
  return schemes[kind].includes(parsed.protocol) ? u : "";
}

export function sameOriginPath(
  raw: string | null | undefined,
  origin: string,
  fallback = "/",
): string {
  if (!raw) return fallback;
  let parsed: URL;
  try {
    parsed = new URL(raw, origin);
  } catch {
    return fallback;
  }
  if (parsed.origin !== origin) return fallback;
  return parsed.pathname + parsed.search + parsed.hash;
}

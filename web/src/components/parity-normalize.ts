const ENTITIES: Record<string, string> = {
  "&amp;": "&",
  "&lt;": "<",
  "&gt;": ">",
  "&quot;": '"',
  "&#39;": "'",
  "&#x27;": "'",
};
const decode = (s: string) => s.replace(/&(?:amp|lt|gt|quot|#39|#x27);/g, (e) => ENTITIES[e]);

export function normalize(html: string): string {
  let out = "";
  for (const m of html.matchAll(
    /<(\/?)([a-z][\w-]*)((?:\s+[^\s=>/]+(?:="[^"]*")?)*)\s*(\/?)>|([^<]+)/gi,
  )) {
    const [, closing, tag, rawAttrs, selfClosing, text] = m;
    if (!tag) {
      out += decode(text).replace(/\s+/g, " ").trim();
      continue;
    }
    if (closing) {
      out += `</${tag}>`;
      continue;
    }
    const attrs = [...rawAttrs.matchAll(/([^\s=]+)(?:="([^"]*)")?/g)]
      .filter(([, name]) => !name.startsWith("data-astro-"))
      .map(([, name, value = ""]) => `${name}="${decode(value)}"`)
      .sort()
      .join(" ");
    out += `<${tag}${attrs ? " " + attrs : ""}>${selfClosing ? `</${tag}>` : ""}`;
  }
  return out;
}

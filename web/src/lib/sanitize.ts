// Backstop for regex mistakes in markdown.ts: bodies and comments are agent-written and agents can
// be prompt-injected.

import DOMPurify, { type Config, type DOMPurify as Purifier } from "dompurify";
import { safeUrl } from "./safe-url";
import { esc } from "./ui";

// Exactly the tags md() emits; update together with markdown.ts.
const allowedTags = [
  "a",
  "audio",
  "blockquote",
  "code",
  "details",
  "div",
  "em",
  "h1",
  "h2",
  "h3",
  "h4",
  "hr",
  "img",
  "input",
  "li",
  "ol",
  "p",
  "pre",
  "span",
  "strong",
  "summary",
  "table",
  "tbody",
  "td",
  "th",
  "thead",
  "tr",
  "ul",
  "video",
];

const allowedAttrs = [
  "alt",
  "checked",
  "class",
  "controls",
  "data-idx",
  "data-sec",
  "disabled",
  "href",
  "playsinline",
  "preload",
  "rel",
  "src",
  "style",
  "target",
  "type",
];

const config: Config = {
  ALLOWED_TAGS: allowedTags,
  ALLOWED_ATTR: allowedAttrs,
  ALLOW_DATA_ATTR: false,
  // DOMPurify tests every non-URI-safe attribute value against this, so it must accept plain words
  // like "_blank".
  // Schemes cut to safeUrl()'s; the hook applies safeUrl() to href/src, which also refuses
  // "//host".
  ALLOWED_URI_REGEXP: /^(?:(?:https?|mailto):|[^a-z]|[a-z+.-]+(?:[^a-z+.\-:]|$))/i,
};

const alignStyle = /^text-align:(?:left|right|center)$/;

let purifier: Purifier | undefined;

function instance(): Purifier | undefined {
  if (purifier) return purifier;
  if (typeof window === "undefined") return undefined;
  const p = DOMPurify(window);
  if (!p.isSupported) return undefined;
  p.addHook("uponSanitizeAttribute", (_node, data) => {
    const v = data.attrValue;
    switch (data.attrName) {
      case "href":
        if (!safeUrl(v, "link")) data.keepAttr = false;
        break;
      case "src":
        if (!safeUrl(v, "media")) data.keepAttr = false;
        break;
      case "style":
        if (!alignStyle.test(v.replace(/\s+/g, ""))) data.keepAttr = false;
        break;
      case "type":
        if (v !== "checkbox") data.keepAttr = false;
        break;
    }
  });
  p.addHook("afterSanitizeAttributes", (node) => {
    if (node.tagName === "A" && node.hasAttribute("target")) {
      node.setAttribute("target", "_blank");
      node.setAttribute("rel", "noopener noreferrer");
    }
  });
  purifier = p;
  return p;
}

export function sanitizeMarkdownHTML(html: string): string {
  const p = instance();
  if (!p) return esc(html);
  return p.sanitize(html, config);
}

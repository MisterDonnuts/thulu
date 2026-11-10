import { md, type MdOptions } from "./markdown";

// Private-use character that md() passes through untouched.
const BR = "\uE000";

function isParagraphLine(line: string): boolean {
  const t = line.trim();
  if (!t) return false;
  if (/^#{1,6}\s/.test(t)) return false;
  if (/^---+$/.test(t)) return false;
  if (t.startsWith("> ")) return false;
  if (/^([-*]|\d+\.)\s/.test(t)) return false;
  if (/^\|.+\|$/.test(t)) return false;
  return true;
}

export function chatMd(text: string, opts?: MdOptions): string {
  const src = text.split(BR).join("");
  if (!src.trim()) return "";
  const lines = src.split("\n");
  const para: boolean[] = [];
  let inFence = false;
  for (const line of lines) {
    const fences = line.split("```").length - 1;
    para.push(!inFence && fences === 0 && isParagraphLine(line));
    if (fences % 2) inFence = !inFence;
  }
  for (let i = 0; i < lines.length - 1; i++) {
    // md() runs inline rules line by line, so a mark placed after a trailing space lands in text,
    // never inside a URL or attribute.
    if (para[i] && para[i + 1]) lines[i] += " " + BR;
  }
  const html = md(lines.join("\n"), opts);
  if (!html.includes(BR)) return html;

  const tpl = document.createElement("template");
  tpl.innerHTML = html;
  const walker = document.createTreeWalker(tpl.content, NodeFilter.SHOW_TEXT);
  const marked: Text[] = [];
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    if (n.nodeValue?.includes(BR)) marked.push(n as Text);
  }
  for (const node of marked) {
    const parts = (node.nodeValue ?? "").split(new RegExp(` ?${BR} ?`));
    const frag = document.createDocumentFragment();
    parts.forEach((part, i) => {
      if (i > 0) frag.append(document.createElement("br"));
      if (part) frag.append(part);
    });
    node.replaceWith(frag);
  }
  return tpl.innerHTML;
}

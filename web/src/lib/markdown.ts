// All HTML is escaped first and only a small trusted subset is rebuilt; DOMPurify on md() output is
// a last line of defence, not the primary one.

import { t } from "../i18n";
import { attachmentIDFromURL, extOf, mediaKind } from "./media";
import { safeUrl, type UrlKind } from "./safe-url";
import { sanitizeMarkdownHTML } from "./sanitize";
import { esc } from "./ui";

// Not safe for attribute values (quotes are left alone); use attr() there.
export function escHtml(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

function unescHtml(s: string): string {
  return s.replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&");
}

// escHtml leaves quotes alone, so `![x" onerror="...](u)` would close the alt attribute and open an
// event handler.
function attr(escaped: string): string {
  return esc(unescHtml(escaped));
}

export interface MdOptions {
  // Attachment URLs are stored root-relative so a body written through a tunnel still renders on
  // localhost and vice versa.
  mediaBase?: string;
  checklist?: boolean;
  attachment?: (id: string) => { mime?: string; filename?: string } | undefined;
}

const attachmentURL = /^(?:[a-z][\w+.-]*:\/\/[^/]*)?(\/task-attachments\/[A-Za-z0-9_-]+.*)$/i;

// Also replaces a hardcoded origin: an http://localhost:8585 embed opened through the https tunnel
// is blocked as mixed content.
function withMediaBase(url: string, base?: string): string {
  const m = attachmentURL.exec(url.trim());
  if (!base || !m) return url;
  return base.replace(/\/$/, "") + m[1];
}

function urlAttr(escaped: string, kind: UrlKind, base?: string): string {
  const u = safeUrl(unescHtml(escaped), kind);
  return u ? esc(withMediaBase(u, base)) : "";
}

export function mdInline(s: string, opts?: MdOptions): string {
  // Inline code first so later rules don't touch its content.
  s = s.replace(/`([^`]+)`/g, (_, c) => `<code>${c}</code>`);
  // Media before links: the syntax overlaps.
  s = s.replace(/!\[([^\]]*)\]\(([^)]+)\)/g, (_m, alt: string, url: string) => {
    const src = urlAttr(url, "media", opts?.mediaBase);
    if (!src) return alt;
    const id = attachmentIDFromURL(url);
    const att = id ? opts?.attachment?.(id) : undefined;
    const kind = mediaKind(att?.mime, alt, att?.filename, url);
    if (kind === "video")
      return `<video class="md-media" src="${src}" controls preload="metadata" playsinline></video>`;
    if (kind === "audio")
      return `<audio class="md-media" src="${src}" controls preload="metadata"></audio>`;
    // Link instead of <img> only when the mime or an extension says so; alt text without one is
    // most often a screenshot.
    if (kind === "file" && (att?.mime || extOf(alt) || extOf(att?.filename ?? "") || extOf(url)))
      return `<a class="md-file" href="${src}" target="_blank" rel="noopener">${alt.trim() ? alt : esc(att?.filename ?? "file")}</a>`;
    // No loading="lazy": the drawer's transform breaks Chrome's lazy-load intersection check and
    // the image never loads.
    return `<img alt="${attr(alt)}" src="${src}">`;
  });
  s = s.replace(/\[([^\]]+)\]\(([^)]+)\)/g, (_m, txt: string, url: string) => {
    const href = urlAttr(url, "link", opts?.mediaBase);
    return href ? `<a href="${href}" target="_blank" rel="noopener">${txt}</a>` : txt;
  });
  {
    const parts = s.split(
      /(<a [^>]*>[\s\S]*?<\/a>|<img [^>]*>|<video [^>]*><\/video>|<audio [^>]*><\/audio>|<code>[\s\S]*?<\/code>)/g,
    );
    for (let i = 0; i < parts.length; i += 2) {
      parts[i] = parts[i].replace(/\bhttps?:\/\/[^\s<>"]+/g, (url) => {
        const tail = /[.,;:!?)\]'"]*$/.exec(url)?.[0] ?? "";
        const u = url.slice(0, url.length - tail.length);
        const href = urlAttr(u, "link");
        return href ? `<a href="${href}" target="_blank" rel="noopener">${u}</a>${tail}` : url;
      });
    }
    s = parts.join("");
  }
  // Bold must run before italic.
  s = s.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
  s = s.replace(/(^|[^\w*])\*([^*\n]+)\*(?!\w)/g, "$1<em>$2</em>");
  s = s.replace(/(^|[^\w_])_([^_\n]+)_(?!\w)/g, "$1<em>$2</em>");
  return s;
}

// Must match task.SlugifySection in internal/task/sections.go: the section API addresses `##
// Heading` blocks by this slug.
export function slugifySection(title: string): string {
  return title
    .trim()
    .toLowerCase()
    .replace(/[^\p{L}\p{N}]+/gu, "-")
    .replace(/^-+|-+$/g, "");
}

const QUOTE = "&gt; ";

export function md(src: string, opts?: MdOptions): string {
  const inline = (text: string) => mdInline(text, opts);
  if (!src.trim()) return `<div class="md-empty">${esc(t("lib.markdown.empty_preview"))}</div>`;
  let curSec = "";
  let itemIdx = 0;
  let s = escHtml(src);
  const codeBlocks: string[] = [];
  s = s.replace(/```(\w*)\n?([\s\S]*?)```/g, (_m, lang: string, code: string) => {
    codeBlocks.push(
      `<pre><code${lang ? ` class="lang-${lang}"` : ""}>${code.replace(/\n$/, "")}</code></pre>`,
    );
    return `�CB${codeBlocks.length - 1}�`;
  });
  const tables: string[] = [];
  {
    const ls = s.split("\n");
    const acc: string[] = [];
    const stripBars = (r: string) => {
      let row = r.trim();
      if (row.startsWith("|")) row = row.slice(1);
      if (row.endsWith("|")) row = row.slice(0, -1);
      return row.split("|").map((c) => c.trim());
    };
    let i = 0;
    while (i < ls.length) {
      const cur = ls[i],
        nxt = ls[i + 1] || "";
      if (/^\s*\|.+\|\s*$/.test(cur) && /^\s*\|[\s\-:|]+\|\s*$/.test(nxt)) {
        const sepCells = stripBars(nxt);
        if (sepCells.length > 0 && sepCells.every((c) => /^:?-+:?$/.test(c))) {
          const headers = stripBars(cur);
          const aligns = sepCells.map((c) => {
            const l = c.startsWith(":"),
              r = c.endsWith(":");
            if (l && r) return "center";
            if (r) return "right";
            if (l) return "left";
            return "";
          });
          const bodyRows: string[][] = [];
          let j = i + 2;
          while (j < ls.length && /^\s*\|.+\|\s*$/.test(ls[j])) {
            bodyRows.push(stripBars(ls[j]));
            j++;
          }
          const algAttr = (k: number) => (aligns[k] ? ` style="text-align:${aligns[k]}"` : "");
          const thead =
            "<thead><tr>" +
            headers.map((h, k) => `<th${algAttr(k)}>${inline(h)}</th>`).join("") +
            "</tr></thead>";
          const tbody =
            "<tbody>" +
            bodyRows
              .map(
                (row) =>
                  "<tr>" +
                  headers.map((_h, k) => `<td${algAttr(k)}>${inline(row[k] ?? "")}</td>`).join("") +
                  "</tr>",
              )
              .join("") +
            "</tbody>";
          tables.push(`<table class="md-table">${thead}${tbody}</table>`);
          acc.push(`�TB${tables.length - 1}�`);
          i = j;
          continue;
        }
      }
      acc.push(cur);
      i++;
    }
    s = acc.join("\n");
  }
  const lines = s.split("\n");
  const out: string[] = [];
  let inUL = false,
    inOL = false,
    inBQ = false;
  let openDetails = 0;
  let bareDetailsAt = -1;
  let para: string[] = [];
  const flushPara = () => {
    if (para.length) {
      out.push(`<p>${para.map(inline).join(" ")}</p>`);
      para = [];
    }
  };
  const closeLists = () => {
    if (inUL) {
      out.push("</ul>");
      inUL = false;
    }
    if (inOL) {
      out.push("</ol>");
      inOL = false;
    }
  };
  for (const raw of lines) {
    const line = raw.replace(/\s+$/, "");
    if (!line.trim()) {
      flushPara();
      closeLists();
      if (inBQ) {
        out.push("</blockquote>");
        inBQ = false;
      }
      continue;
    }
    if (/^�CB\d+�$/.test(line.trim())) {
      flushPara();
      closeLists();
      if (inBQ) {
        out.push("</blockquote>");
        inBQ = false;
      }
      out.push(line.trim());
      continue;
    }
    if (/^�TB\d+�$/.test(line.trim())) {
      flushPara();
      closeLists();
      if (inBQ) {
        out.push("</blockquote>");
        inBQ = false;
      }
      out.push(line.trim());
      continue;
    }
    // <details>/<summary> is the only raw HTML rebuilt; anything else that looks like HTML stays
    // escaped.
    const det = line
      .trim()
      .match(/^&lt;details&gt;\s*(?:&lt;summary&gt;(.*?)&lt;\/summary&gt;)?$/i);
    const sum = line.trim().match(/^&lt;summary&gt;(.*?)&lt;\/summary&gt;$/i);
    if (sum && bareDetailsAt >= 0 && bareDetailsAt === out.length - 1) {
      const label = sum[1].trim();
      if (label)
        out[bareDetailsAt] = `<details class="md-details"><summary>${inline(label)}</summary>`;
      bareDetailsAt = -1;
      continue;
    }
    if (det || (openDetails > 0 && /^&lt;\/details&gt;$/i.test(line.trim()))) {
      flushPara();
      closeLists();
      if (inBQ) {
        out.push("</blockquote>");
        inBQ = false;
      }
      if (det) {
        const summary = det.at(1)?.trim() ?? "";
        const label = summary ? inline(summary) : esc(t("lib.markdown.details"));
        bareDetailsAt = summary ? -1 : out.length;
        out.push(`<details class="md-details"><summary>${label}</summary>`);
        openDetails++;
      } else {
        out.push("</details>");
        openDetails--;
      }
      continue;
    }
    const h = line.match(/^(#{1,6})\s+(.+)$/);
    if (h) {
      flushPara();
      closeLists();
      if (inBQ) {
        out.push("</blockquote>");
        inBQ = false;
      }
      const lvl = Math.min(h[1].length, 4);
      if (h[1].length === 2) {
        curSec = slugifySection(h[2]);
        itemIdx = 0;
      }
      out.push(`<h${lvl}>${inline(h[2])}</h${lvl}>`);
      continue;
    }
    if (/^---+$/.test(line.trim())) {
      flushPara();
      closeLists();
      if (inBQ) {
        out.push("</blockquote>");
        inBQ = false;
      }
      out.push("<hr>");
      continue;
    }
    // The source is already escaped here, so the quote mark reads "&gt; ", not "> ".
    if (line.trim().startsWith(QUOTE)) {
      flushPara();
      closeLists();
      if (!inBQ) {
        out.push("<blockquote>");
        inBQ = true;
      }
      out.push(`<p>${inline(line.trim().slice(QUOTE.length))}</p>`);
      continue;
    } else if (inBQ) {
      out.push("</blockquote>");
      inBQ = false;
    }
    const ul = line.match(/^\s*[-*]\s+(.+)$/);
    if (ul) {
      flushPara();
      if (inOL) {
        out.push("</ol>");
        inOL = false;
      }
      if (!inUL) {
        out.push("<ul>");
        inUL = true;
      }
      const task = ul[1].match(/^\[([ xX])\]\s*(.*)$/);
      if (task) {
        const checked = task[1] !== " ";
        itemIdx++;
        const live = opts?.checklist && curSec;
        out.push(
          `<li class="md-task${checked ? " done" : ""}">` +
            `<input type="checkbox" class="md-task-check" data-sec="${curSec}" data-idx="${itemIdx}"${checked ? " checked" : ""}${live ? "" : " disabled"}> ` +
            `<span class="md-task-text">${inline(task[2])}</span></li>`,
        );
        continue;
      }
      out.push(`<li>${inline(ul[1])}</li>`);
      continue;
    } else if (inUL) {
      out.push("</ul>");
      inUL = false;
    }
    const ol = line.match(/^\s*\d+\.\s+(.+)$/);
    if (ol) {
      flushPara();
      if (!inOL) {
        out.push("<ol>");
        inOL = true;
      }
      out.push(`<li>${inline(ol[1])}</li>`);
      continue;
    } else if (inOL) {
      out.push("</ol>");
      inOL = false;
    }
    para.push(line);
  }
  flushPara();
  closeLists();
  if (inBQ) out.push("</blockquote>");
  for (; openDetails > 0; openDetails--) out.push("</details>");
  let html = out.join("\n");
  html = html.replace(/�CB(\d+)�/g, (_m, i: string) => codeBlocks[parseInt(i)]);
  html = html.replace(/�TB(\d+)�/g, (_m, i: string) => tables[parseInt(i)]);
  return sanitizeMarkdownHTML(html);
}

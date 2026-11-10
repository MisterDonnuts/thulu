export interface BriefSection {
  title: string;
  slug: string;
  source: string;
  lines: number;
}

export interface Brief {
  preamble: string;
  sections: BriefSection[];
}

// Must match slugifySection in lib/markdown.ts and task.SlugifySection.
function slugify(title: string): string {
  return title
    .trim()
    .toLowerCase()
    .replace(/[^\p{L}\p{N}]+/gu, "-")
    .replace(/^-+|-+$/g, "");
}

const FENCE = /^\s*```/;

export function splitBrief(body: string): Brief {
  const lines = (body || "").split(/\r?\n/);
  const pre: string[] = [];
  const sections: BriefSection[] = [];
  let cur: { title: string; lines: string[] } | null = null;
  let inFence = false;
  const flush = () => {
    if (!cur) return;
    while (cur.lines.length && !cur.lines[cur.lines.length - 1].trim()) cur.lines.pop();
    while (cur.lines.length && !cur.lines[0].trim()) cur.lines.shift();
    sections.push({
      title: cur.title,
      slug: slugify(cur.title),
      source: cur.lines.join("\n"),
      lines: cur.lines.length,
    });
  };
  for (const line of lines) {
    if (FENCE.test(line)) inFence = !inFence;
    const h = !inFence && line.match(/^##\s+(.+?)\s*#*\s*$/);
    if (h) {
      flush();
      cur = { title: h[1], lines: [] };
      continue;
    }
    (cur ? cur.lines : pre).push(line);
  }
  flush();
  return { preamble: pre.join("\n").trim(), sections };
}

export function checklistCount(source: string): { done: number; total: number } {
  let done = 0;
  let total = 0;
  let inFence = false;
  for (const line of source.split(/\r?\n/)) {
    if (FENCE.test(line)) {
      inFence = !inFence;
      continue;
    }
    if (inFence) continue;
    const m = line.match(/^\s*[-*]\s+\[([ xX])\]/);
    if (!m) continue;
    total++;
    if (m[1] !== " ") done++;
  }
  return { done, total };
}

export const FOLD_OVER_LINES = 30;
export const FOLD_TO_LINES = 12;

export function shouldFold(s: Pick<BriefSection, "lines">): boolean {
  return s.lines > FOLD_OVER_LINES;
}

// i18n-ignore stop words for matching, not text
const STOP_EN = "a an the to of in on for with and or at by is be from as into it its this that";
// i18n-ignore stop words for matching, not text
const STOP_ES = "de del la el los las en y o con para por un una al lo su sus que";
const STOP = new Set(`${STOP_EN} ${STOP_ES}`.split(" "));

function norm(s: string): string {
  return s.trim().toLowerCase().replace(/\s+/g, " ");
}

function words(s: string): string[] {
  return norm(s)
    .split(/[^\p{L}\p{N}]+/u)
    .filter((w) => w && !STOP.has(w));
}

export function matchSubtask<T extends { id: string; title: string }>(
  item: string,
  subtasks: T[],
): T | null {
  const key = norm(item);
  if (!key) return null;
  const exact = subtasks.filter((s) => norm(s.title) === key);
  if (exact.length === 1) return exact[0];
  if (exact.length > 1) return null;
  const want = words(item);
  if (want.length === 0) return null;
  const hits = subtasks.filter((s) => {
    const have = new Set(words(s.title));
    return want.every((w) => have.has(w));
  });
  return hits.length === 1 ? hits[0] : null;
}

export interface SectionEvent {
  kind: string;
  actor?: string;
  ref_id?: string;
  created_at?: string;
}

export interface SectionAuthor {
  agent: string;
  at: string;
}

export function sectionAuthors(
  events: SectionEvent[],
  isAgent: (name: string) => boolean,
): Map<string, SectionAuthor> {
  const latest = new Map<string, SectionEvent>();
  for (const e of events) {
    if (e.kind !== "section_updated" || !e.ref_id) continue;
    const prev = latest.get(e.ref_id);
    if (!prev || (e.created_at ?? "") >= (prev.created_at ?? "")) latest.set(e.ref_id, e);
  }
  const out = new Map<string, SectionAuthor>();
  for (const [slug, e] of latest) {
    const who = e.actor ?? "";
    if (who && isAgent(who)) out.set(slug, { agent: who, at: e.created_at ?? "" });
  }
  return out;
}

const OPEN_MD_FENCE = /^ {0,3}(`{3,}|~{3,})[ \t]*(?:md|markdown)[ \t]*$/i;
const CLOSE_FENCE = /^ {0,3}(`{3,}|~{3,})[ \t]*$/;

// Mirrors task.UnwrapMarkdownFence (applied on daemon writes) for tasks written before it existed.
// wrapped=true means the shown text differs from the stored text, so checklist clicks must stay
// off.
export function unwrapMarkdownFence(body: string): { text: string; wrapped: boolean } {
  const lines = body.replace(/\r\n/g, "\n").split("\n");
  let first = 0;
  let last = lines.length - 1;
  while (first <= last && !lines[first].trim()) first++;
  while (last >= first && !lines[last].trim()) last--;
  const open = last > first ? OPEN_MD_FENCE.exec(lines[first]) : null;
  if (!open) return { text: body, wrapped: false };
  const marker = open[1];
  for (let i = first + 1; i <= last; i++) {
    const m = CLOSE_FENCE.exec(lines[i]);
    if (!m?.[1].startsWith(marker[0]) || m[1].length < marker.length) continue;
    if (i !== last) break;
    const inner = lines.slice(first + 1, last).join("\n");
    return { text: `${inner.replace(/[ \t\n]+$/, "")}\n`, wrapped: true };
  }
  return { text: body, wrapped: false };
}

import { t } from "../i18n";
import { slugifySection } from "./markdown";
import { mediaKind } from "./media";

export interface AttachmentRef {
  id: string;
  filename: string;
  mime_type: string;
}

// Stored root-relative so the body works from any host that reaches the daemon; mdBody() adds the
// reader's origin at render time.
export function attachmentPath(id: string): string {
  return `/task-attachments/${id}`;
}

export function attachmentMarkdown(a: AttachmentRef): string {
  const url = attachmentPath(a.id);
  const embed = mediaKind(a.mime_type, a.filename) !== "file";
  return embed ? `![${a.filename}](${url})` : `[${a.filename}](${url})`;
}

export function embeddedIn(body: string, id: string): boolean {
  return (body || "").includes(attachmentPath(id));
}

export function pastedName(blob: File | Blob, now = Date.now()): string {
  const named = (blob as File).name;
  if (named) return named;
  const ext = (blob.type.split("/")[1] || "png").split(";")[0];
  return `paste-${now}.${ext}`;
}

// The timestamp keeps placeholders unique: replacePlaceholder() swaps the first match, so two files
// pasted together must not share one.
export function uploadPlaceholder(filename: string, now = Date.now()): string {
  return `![${t("lib.body_editor.uploading", { filename })}](#uploading-${now})`;
}

export const UPLOAD_FAILED = `![${t("lib.body_editor.upload_failed")}](#)`;

export interface TextEdit {
  value: string;
  caret: number;
}

export function spliceText(value: string, start: number, end: number, text: string): TextEdit {
  return { value: value.slice(0, start) + text + value.slice(end), caret: start + text.length };
}

export function replacePlaceholder(
  value: string,
  caret: number,
  placeholder: string,
  replacement: string,
): TextEdit {
  return {
    value: value.replace(placeholder, replacement),
    caret: caret - placeholder.length + replacement.length,
  };
}

export function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} kB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

// Order matches canonicalSections in internal/task/sections.go. Headings stay English in every
// locale: agents and the section API address sections by them.
const BRIEF_SECTIONS: Array<{ slug: string; heading: string; starter: string }> = [
  { slug: "goal", heading: "Goal", starter: "" }, // i18n-ignore: a section address
  { slug: "context", heading: "Context", starter: "" }, // i18n-ignore
  // i18n-ignore
  { slug: "definition-of-done", heading: "Definition of Done", starter: "- [ ] " },
  { slug: "out-of-scope", heading: "Out of Scope", starter: "" }, // i18n-ignore
];

export function missingBriefSections(body: string): string[] {
  const have = new Set<string>();
  for (const m of body.matchAll(/^##\s+(.+?)\s*#*\s*$/gm)) have.add(slugifySection(m[1]));
  return BRIEF_SECTIONS.filter((s) => !have.has(s.slug)).map((s) => s.heading);
}

export function withBriefSections(body: string): string {
  const missing = new Set(missingBriefSections(body));
  if (missing.size === 0) return body;
  const blocks = BRIEF_SECTIONS.filter((s) => missing.has(s.heading)).map((s) =>
    `## ${s.heading}\n\n${s.starter}`.trimEnd(),
  );
  const head = body.trimEnd();
  return `${head ? `${head}\n\n` : ""}${blocks.join("\n\n")}\n`;
}

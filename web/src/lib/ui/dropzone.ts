// Keep in sync with DropZone.astro and styles/primitives/dropzone.css.

import { t } from "../../i18n";
import { formatBytes, type UploadPlan, type UploadReport } from "../project-upload";
import { badgeHTML } from "./badge";
import { buttonHTML } from "./button";
import { disclosureHTML } from "./disclosure";
import { attrsHTML, esc, type Attrs } from "./dom";
import { icoSvg } from "./icon";

export type DropAccept = "both" | "folder" | "zip" | "json";

const ACCEPTS = new Set<string>(["both", "folder", "zip", "json"]);

export const ZIP_ACCEPT = ".zip,application/zip";

export const JSON_ACCEPT = ".json,application/json";

export function dropAccepts(accept: DropAccept = "both"): {
  folder: boolean;
  zip: boolean;
  json: boolean;
} {
  if (!ACCEPTS.has(accept)) throw new Error(`dropzone: no accept "${accept}"`); // i18n-ignore a programming error
  return {
    folder: accept === "both" || accept === "folder",
    zip: accept === "both" || accept === "zip",
    json: accept === "json",
  };
}

export function dropZoneClasses(o: { overlay?: boolean; cls?: string } = {}): string {
  return ["drop-zone", o.overlay && "drop-zone-overlay", o.cls].filter(Boolean).join(" ");
}

export function dropTitleId(id: string): string {
  return `${id}-title`;
}

export interface DropZoneOptions {
  id: string;
  title: string;
  sub?: string;
  icon?: string;
  accept?: DropAccept;
  // The container needs a CSS position for the overlay to cover it.
  overlay?: boolean;
  cls?: string;
  attrs?: Attrs;
}

export function dropZoneAttrs(id: string, accept: DropAccept = "both"): Attrs {
  dropAccepts(accept);
  return {
    id,
    role: "group",
    "aria-labelledby": dropTitleId(id),
    "data-accept": accept,
  };
}

export function dropZoneHTML(o: DropZoneOptions): string {
  const takes = dropAccepts(o.accept);
  const own: Attrs = {
    ...dropZoneAttrs(o.id, o.accept),
    class: dropZoneClasses(o),
  };
  const glyph = o.icon ? icoSvg(o.icon, { size: "xl", cls: "drop-ico" }) : "";
  const title = `<p class="drop-title" id="${esc(dropTitleId(o.id))}">${esc(o.title)}</p>`;
  const sub = o.sub ? `<p class="drop-sub">${esc(o.sub)}</p>` : "";
  if (o.overlay) return `<div${attrsHTML(own)}${attrsHTML(o.attrs)}>${glyph}${title}${sub}</div>`;
  const pick = (kind: "folder" | "zip" | "json", label: string) =>
    buttonHTML({ label, size: "sm", attrs: { "data-drop-pick": kind } });
  const actions =
    (takes.folder ? pick("folder", t("projects.upload.pick_folder")) : "") +
    (takes.zip ? pick("zip", t("projects.upload.pick_zip")) : "") +
    (takes.json ? pick("json", t("ui.dropzone.pick_json")) : "");
  const inputs =
    (takes.folder
      ? `<input type="file" webkitdirectory multiple hidden data-drop-input="folder">`
      : "") +
    (takes.zip ? `<input type="file" accept="${ZIP_ACCEPT}" hidden data-drop-input="zip">` : "") +
    (takes.json ? `<input type="file" accept="${JSON_ACCEPT}" hidden data-drop-input="json">` : "");
  return `<div${attrsHTML(own)}${attrsHTML(o.attrs)}>${glyph}${title}${sub}<div class="drop-actions">${actions}</div>${inputs}</div>`;
}

export interface DropData {
  items: ArrayLike<{ webkitGetAsEntry?: () => FileSystemEntry | null }>;
}

export function dropKind(
  dt: DropData,
  accept: DropAccept = "both",
): "zip" | "folder" | "json" | null {
  const takes = dropAccepts(accept);
  const entries = Array.from(dt.items, (i) => i.webkitGetAsEntry?.() ?? null).filter(
    (e): e is FileSystemEntry => e !== null,
  );
  const one = entries.length === 1 && entries[0].isFile ? entries[0] : null;
  if (one && takes.json) return "json";
  if (one && /\.zip$/i.test(one.name)) return takes.zip ? "zip" : null;
  if (entries.length === 0) return null;
  return takes.folder ? "folder" : null;
}

export interface DropZoneHandlers {
  onFolderPick?: (files: FileList) => void;
  onZip?: (file: File) => void;
  onFolderDrop?: (items: DataTransferItemList) => void;
  onJson?: (file: File) => void;
  signal?: AbortSignal;
}

export function bindDropZone(zone: HTMLElement, h: DropZoneHandlers): void {
  const opts = { signal: h.signal };
  const accept = (zone.dataset.accept as DropAccept | undefined) ?? "both";
  const input = (kind: string) =>
    zone.querySelector<HTMLInputElement>(`input[data-drop-input="${kind}"]`);
  zone.addEventListener(
    "click",
    (e) => {
      const pick = (e.target as Element).closest<HTMLElement>("[data-drop-pick]");
      if (pick) input(pick.dataset.dropPick ?? "")?.click();
    },
    opts,
  );
  input("folder")?.addEventListener(
    "change",
    (e) => {
      const files = (e.target as HTMLInputElement).files;
      if (files?.length) h.onFolderPick?.(files);
    },
    opts,
  );
  input("zip")?.addEventListener(
    "change",
    (e) => {
      const f = (e.target as HTMLInputElement).files?.[0];
      if (f) h.onZip?.(f);
    },
    opts,
  );
  input("json")?.addEventListener(
    "change",
    (e) => {
      const f = (e.target as HTMLInputElement).files?.[0];
      if (f) h.onJson?.(f);
    },
    opts,
  );
  zone.addEventListener(
    "dragover",
    (e) => {
      e.preventDefault();
      zone.classList.add("over");
    },
    opts,
  );
  zone.addEventListener(
    "dragleave",
    (e) => {
      if (!zone.contains(e.relatedTarget as Node | null)) zone.classList.remove("over");
    },
    opts,
  );
  zone.addEventListener(
    "drop",
    (e) => {
      e.preventDefault();
      zone.classList.remove("over");
      if (e.dataTransfer) handOver(e.dataTransfer, accept, h);
    },
    opts,
  );
}

function handOver(dt: DataTransfer, accept: DropAccept, h: DropZoneHandlers): void {
  const kind = dropKind(dt, accept);
  const file = dt.files.item(0);
  if (kind === "zip" && file) h.onZip?.(file);
  else if (kind === "json" && file) h.onJson?.(file);
  else if (kind === "folder") h.onFolderDrop?.(dt.items);
}

export interface DropOverlayHandlers extends DropZoneHandlers {
  enabled?: () => boolean;
}

function carriesFiles(e: DragEvent): boolean {
  return Array.from(e.dataTransfer?.types ?? []).includes("Files");
}

// Claim every file drag, welcome or not: otherwise the browser opens the dropped file in place of
// the page.
// dragenter/dragleave fire for every child, hence the depth count.
export function bindDropOverlay(
  container: HTMLElement,
  zone: HTMLElement,
  h: DropOverlayHandlers,
): void {
  const opts = { signal: h.signal };
  const accept = (zone.dataset.accept as DropAccept | undefined) ?? "both";
  const welcome = () => h.enabled?.() ?? true;
  let depth = 0;
  const hide = () => {
    depth = 0;
    zone.hidden = true;
  };
  const claim = (e: DragEvent): boolean => {
    e.preventDefault();
    const ok = welcome();
    if (!ok && e.dataTransfer) e.dataTransfer.dropEffect = "none";
    return ok;
  };
  container.addEventListener(
    "dragenter",
    (e) => {
      if (!carriesFiles(e) || !claim(e)) return;
      depth++;
      zone.hidden = false;
    },
    opts,
  );
  container.addEventListener(
    "dragover",
    (e) => {
      if (!carriesFiles(e)) return;
      if (!claim(e)) hide();
    },
    opts,
  );
  container.addEventListener(
    "dragleave",
    () => {
      depth = Math.max(0, depth - 1);
      if (depth === 0) hide();
    },
    opts,
  );
  container.addEventListener(
    "drop",
    (e) => {
      if (!carriesFiles(e)) return;
      const ok = claim(e);
      hide();
      if (ok && e.dataTransfer) handOver(e.dataTransfer, accept, h);
    },
    opts,
  );
}

export function resetDropZone(zone: HTMLElement): void {
  zone.querySelectorAll<HTMLInputElement>("input[data-drop-input]").forEach((i) => (i.value = ""));
}

function pathsHTML(paths: string[], total: number): string {
  const more =
    total > paths.length
      ? `<li>${esc(t("projects.upload.and_more", { count: total - paths.length }))}</li>`
      : "";
  return `<ul class="upload-paths">${paths.map((p) => `<li>${esc(p)}</li>`).join("")}${more}</ul>`;
}

export function uploadLeftOutHTML(
  excluded: string[],
  excludedCount: number,
  skipped: string[],
  skippedCount: number,
): string {
  const list = (summary: string, paths: string[], total: number) =>
    disclosureHTML({ variant: "code", summary, bodyHTML: pathsHTML(paths, total) });
  return (
    (excludedCount > 0
      ? list(t("projects.upload.excluded", { count: excludedCount }), excluded, excludedCount)
      : "") +
    (skippedCount > 0
      ? list(t("projects.upload.skipped", { count: skippedCount }), skipped, skippedCount)
      : "")
  );
}

function filesBadge(count: number): string {
  return badgeHTML(t("projects.upload.files", { count }), "neutral", {
    icon: "i-file",
    mono: true,
  });
}

function sizeBadge(bytes: number, glyph: boolean): string {
  return badgeHTML(formatBytes(bytes), "neutral", {
    icon: glyph ? "i-file" : undefined,
    mono: true,
  });
}

export type UploadHistory = "keep" | "ignore";

export interface UploadPlanOptions {
  zip?: { size: number } | null;
  history?: UploadHistory;
}

export function uploadPlanHTML(plan: UploadPlan | null, o: UploadPlanOptions = {}): string {
  if (o.zip) {
    return `<div class="plan-line">${sizeBadge(o.zip.size, true)}<span class="plan-note">${esc(t("projects.upload.zip_note"))}</span></div>`;
  }
  if (!plan) return "";
  const history = o.history ?? "keep";
  const kept =
    plan.history > 0 && history === "keep"
      ? badgeHTML(t("projects.upload.with_history"), "accent", { icon: "i-branch" })
      : "";
  const ignored =
    plan.history > 0 && history === "ignore"
      ? `<p class="plan-note">${esc(t("project.code.history_ignored"))}</p>`
      : "";
  const empty =
    plan.count === 0 ? `<p class="field-error">${esc(t("projects.upload.nothing_left"))}</p>` : "";
  return (
    `<div class="plan-line">${filesBadge(plan.count)}${sizeBadge(plan.bytes, false)}${kept}</div>` +
    ignored +
    empty +
    uploadLeftOutHTML(plan.excluded, plan.excluded.length, plan.skipped, plan.skipped.length)
  );
}

export function uploadReportHTML(r: UploadReport, o: { lead?: string } = {}): string {
  const head = o.lead
    ? `<p>${esc(o.lead)}</p>`
    : `<div class="plan-line">${filesBadge(r.files)}${sizeBadge(r.bytes, false)}</div>`;
  const ignored = r.history_ignored
    ? `<p class="plan-note">${esc(t("projects.upload.history_ignored"))}</p>`
    : "";
  return (
    head +
    ignored +
    uploadLeftOutHTML(r.excluded ?? [], r.excluded_count, r.skipped ?? [], r.skipped_count)
  );
}

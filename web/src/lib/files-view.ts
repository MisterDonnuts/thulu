// The Files tab: every file on the task, newest first, one row each (kind icon, name, size, who
// added it and when), as the Task View Lab draws it.
import { t } from "../i18n";
import { mediaKind } from "./media";
import { fmtDayClock } from "./time";
import { esc } from "./ui/dom";
import { icoSvg } from "./ui/icon";

export interface FileRowInput {
  id: string;
  filename: string;
  mime_type?: string;
  size: number;
  created_at?: string;
}

// The row's icon: a video plays, an image is a picture, anything else a page.
export function fileIcon(a: Pick<FileRowInput, "mime_type" | "filename">): string {
  const kind = mediaKind(a.mime_type, a.filename);
  if (kind === "video") return "i-play";
  if (kind === "image") return "i-camera";
  return "i-file";
}

// "3 KB", "705 KB", "1.2 MB": whole kilobytes, never under 1, and megabytes past a million bytes.
export function fileSize(bytes: number): string {
  if (bytes > 1e6) return t("lib.files_view.size.mb", { n: (bytes / 1e6).toFixed(1) });
  return t("lib.files_view.size.kb", { n: Math.max(1, Math.round(bytes / 1e3)) });
}

export function filesNewestFirst<T extends Pick<FileRowInput, "created_at">>(
  files: readonly T[],
): T[] {
  return files
    .map((f, i) => ({ f, i }))
    .sort((a, b) => {
      const x = a.f.created_at ?? "";
      const y = b.f.created_at ?? "";
      if (x !== y) return x < y ? 1 : -1;
      return b.i - a.i;
    })
    .map(({ f }) => f);
}

// The inside of one row. The name is the button that opens the file; `origin` says who added it.
export function fileRowInnerHTML(a: FileRowInput, origin: string): string {
  const when = a.created_at ? fmtDayClock(a.created_at) : "";
  const by = [origin, when].filter(Boolean).join(" · ");
  return `<span class="tv-file-ic">${icoSvg(fileIcon(a))}</span><button type="button" class="tv-file-nm" data-file-open="${esc(a.id)}" title="${esc(a.filename)}">${esc(a.filename)}</button><span class="tv-file-sz">${esc(fileSize(a.size))}</span><span class="tv-file-by">${esc(by)}</span>`;
}

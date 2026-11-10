// Attachment URLs carry no extension; the paste handler writes the filename as alt text so the type
// can be guessed from it.

export type MediaKind = "image" | "video" | "audio" | "file";

const videoExt = ["mp4", "webm", "ogv", "mov", "m4v"];
const audioExt = ["mp3", "wav", "ogg", "oga", "m4a", "aac", "flac"];
const imageExt = ["png", "jpg", "jpeg", "gif", "webp", "svg", "avif", "bmp", "ico"];

export function extOf(nameOrURL: string): string {
  const path = nameOrURL.split(/[?#]/)[0];
  const base = path.slice(path.lastIndexOf("/") + 1);
  const dot = base.lastIndexOf(".");
  if (dot <= 0 || dot === base.length - 1) return "";
  return base.slice(dot + 1).toLowerCase();
}

export function mediaKind(mime?: string, ...names: Array<string | undefined>): MediaKind {
  const m = (mime ?? "").toLowerCase();
  if (m.startsWith("image/")) return "image";
  if (m.startsWith("video/")) return "video";
  if (m.startsWith("audio/")) return "audio";
  for (const n of names) {
    const ext = extOf(n ?? "");
    if (!ext) continue;
    if (imageExt.includes(ext)) return "image";
    if (videoExt.includes(ext)) return "video";
    if (audioExt.includes(ext)) return "audio";
  }
  return "file";
}

export function attachmentIDFromURL(url: string): string {
  const m = /\/task-attachments\/([A-Za-z0-9_-]+)/.exec(url);
  return m ? m[1] : "";
}

export function countKinds(files: ReadonlyArray<{ mime_type?: string; filename?: string }>): {
  video: number;
  image: number;
  other: number;
} {
  const n = { video: 0, image: 0, other: 0 };
  for (const f of files) {
    const kind = mediaKind(f.mime_type, f.filename);
    if (kind === "video") n.video++;
    else if (kind === "image") n.image++;
    else n.other++;
  }
  return n;
}

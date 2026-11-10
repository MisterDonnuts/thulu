// The daemon re-applies these rules (internal/project/upload.go): keep these lists in sync with
// UploadSkipDirs, junkFiles, secretNames, secretExts and envTemplates there.

import { apiBase, csrfHeaders } from "./auth";

export const UPLOAD_SKIP_DIRS = [
  "node_modules",
  ".venv",
  "venv",
  "__pycache__",
  ".pytest_cache",
  ".mypy_cache",
  ".ruff_cache",
  ".tox",
  ".gradle",
  ".next",
  ".nuxt",
  ".turbo",
  ".parcel-cache",
  ".cache",
  "__MACOSX",
];

const JUNK_FILES = [".DS_Store", "Thumbs.db", "desktop.ini"];

const SECRET_NAMES = [
  ".env",
  ".netrc",
  ".pgpass",
  ".pypirc",
  ".git-credentials",
  ".htpasswd",
  "id_rsa",
  "id_dsa",
  "id_ecdsa",
  "id_ed25519",
];
const SECRET_EXTS = [".pem", ".key", ".p12", ".pfx", ".jks", ".keystore", ".ppk"];
const ENV_TEMPLATES = ["example", "sample", "template", "dist", "defaults"];

// Must match the daemon's MaxUploadArchiveBytes.
export const MAX_UPLOAD_BYTES = 200 * 1024 * 1024;

export function isSecretPath(p: string): boolean {
  const parts = p.split("/");
  const lower = parts[parts.length - 1].toLowerCase();
  if (SECRET_NAMES.includes(lower)) return true;
  if (lower.startsWith(".env.")) return !ENV_TEMPLATES.includes(lower.slice(5));
  const dot = lower.lastIndexOf(".");
  if (dot > 0 && SECRET_EXTS.includes(lower.slice(dot))) return true;
  return lower === "credentials" && parts[parts.length - 2] === ".aws";
}

function isHistoryPath(rest: string[]): boolean {
  if (rest.length === 0) return false;
  switch (rest[0]) {
    case "HEAD":
    case "packed-refs":
    case "shallow":
    case "config":
      return rest.length === 1;
    case "refs":
      return rest.length > 1;
    case "objects":
      return rest.length > 1 && rest[1] !== "info";
  }
  return false;
}

export type EntryVerdict =
  | { kind: "file" }
  | { kind: "history" }
  | { kind: "secret" }
  | { kind: "skip"; folder: string }
  | { kind: "drop" };

export function classifyPath(path: string): EntryVerdict {
  const parts = path.split("/").filter((p) => p !== "" && p !== ".");
  if (parts.length === 0) return { kind: "drop" };
  if (parts[0] === ".git")
    return isHistoryPath(parts.slice(1)) ? { kind: "history" } : { kind: "drop" };
  for (let i = 0; i < parts.length - 1; i++) {
    if (parts[i] === ".git" || UPLOAD_SKIP_DIRS.includes(parts[i])) {
      return { kind: "skip", folder: parts.slice(0, i + 1).join("/") + "/" };
    }
  }
  if (JUNK_FILES.includes(parts[parts.length - 1])) return { kind: "drop" };
  if (isSecretPath(parts.join("/"))) return { kind: "secret" };
  return { kind: "file" };
}

export function skipFolder(relPath: string): boolean {
  const parts = relPath.split("/").filter(Boolean);
  return UPLOAD_SKIP_DIRS.includes(parts[parts.length - 1]);
}

export interface PickedFile {
  path: string;
  file: Blob;
}

export interface UploadPlan {
  files: PickedFile[];
  count: number;
  bytes: number;
  history: number;
  skipped: string[];
  excluded: string[];
}

export function stripTopFolder(paths: string[]): string {
  if (paths.length === 0) return "";
  let top: string | null = null;
  for (const p of paths) {
    const slash = p.indexOf("/");
    if (slash < 0) return "";
    const first = p.slice(0, slash);
    if (top === null) top = first;
    else if (first !== top) return "";
  }
  return top && top !== ".git" ? top + "/" : "";
}

export function planUpload(picked: PickedFile[]): UploadPlan {
  const prefix = stripTopFolder(picked.map((f) => f.path));
  const plan: UploadPlan = { files: [], count: 0, bytes: 0, history: 0, skipped: [], excluded: [] };
  for (const f of picked) {
    const path = f.path.slice(prefix.length);
    const v = classifyPath(path);
    switch (v.kind) {
      case "file":
        plan.files.push({ path, file: f.file });
        plan.count++;
        plan.bytes += f.file.size;
        break;
      case "history":
        plan.files.push({ path, file: f.file });
        plan.history++;
        break;
      case "secret":
        plan.excluded.push(path);
        break;
      case "skip":
        if (!plan.skipped.includes(v.folder)) plan.skipped.push(v.folder);
        break;
      case "drop":
        break;
    }
  }
  return plan;
}

export function pickedFromInput(list: FileList): PickedFile[] {
  return Array.from(list).map((f) => ({ path: f.webkitRelativePath || f.name, file: f }));
}

export async function pickedFromDrop(items: DataTransferItemList): Promise<PickedFile[]> {
  const roots: FileSystemEntry[] = [];
  for (const item of Array.from(items)) {
    const entry = item.webkitGetAsEntry();
    if (entry) roots.push(entry);
  }
  const out: PickedFile[] = [];
  async function walk(entry: FileSystemEntry, rel: string): Promise<void> {
    if (entry.isFile) {
      const file = await new Promise<File>((res, rej) =>
        (entry as FileSystemFileEntry).file(res, rej),
      );
      out.push({ path: rel, file });
      return;
    }
    if (!entry.isDirectory) return;
    if (skipFolder(rel)) {
      // Stand-in file so classifyPath/planUpload report the skipped folder.
      out.push({ path: rel + "/.skipped", file: new Blob() });
      return;
    }
    const reader = (entry as FileSystemDirectoryEntry).createReader();
    // readEntries returns batches (100 in Chrome); keep reading until it returns none.
    for (;;) {
      const batch = await new Promise<FileSystemEntry[]>((res, rej) =>
        reader.readEntries(res, rej),
      );
      if (batch.length === 0) break;
      for (const child of batch) await walk(child, rel ? `${rel}/${child.name}` : child.name);
    }
  }
  for (const root of roots) await walk(root, root.name);
  return out;
}

const enc = new TextEncoder();

function octal(n: number, width: number): string {
  return n.toString(8).padStart(width - 1, "0") + "\0";
}

function header(name: string, size: number, type: string): Uint8Array<ArrayBuffer> {
  const h = new Uint8Array(512);
  const put = (s: string, off: number, len: number) => h.set(enc.encode(s).subarray(0, len), off);
  put(name, 0, 100);
  put(octal(0o644, 8), 100, 8);
  put(octal(0, 8), 108, 8);
  put(octal(0, 8), 116, 8);
  put(octal(size, 12), 124, 12);
  put(octal(Math.floor(Date.now() / 1000), 12), 136, 12);
  put("        ", 148, 8);
  put(type, 156, 1);
  put("ustar\0", 257, 6);
  put("00", 263, 2);
  let sum = 0;
  for (const b of h) sum += b;
  put(octal(sum, 7) + " ", 148, 8);
  return h;
}

function padding(size: number): Uint8Array<ArrayBuffer> {
  return new Uint8Array((512 - (size % 512)) % 512);
}

// The PAX length prefix counts its own digits, hence the fixed-point loop.
function paxRecord(key: string, value: string): Uint8Array<ArrayBuffer> {
  const body = ` ${key}=${value}\n`;
  const bodyLen = enc.encode(body).length;
  let len = bodyLen + 1;
  while (String(len).length + bodyLen !== len) len = String(len).length + bodyLen;
  return enc.encode(`${len}${body}`);
}

export function buildTar(files: PickedFile[]): Blob {
  const parts: BlobPart[] = [];
  for (const f of files) {
    const nameBytes = enc.encode(f.path);
    if (nameBytes.length > 100 || !/^[\x20-\x7e]*$/.test(f.path)) {
      const pax = paxRecord("path", f.path);
      parts.push(header("PaxHeader", pax.length, "x"), pax, padding(pax.length));
    }
    parts.push(header(f.path, f.file.size, "0"), f.file, padding(f.file.size));
  }
  parts.push(new Uint8Array(1024));
  return new Blob(parts);
}

export async function buildTarGz(files: PickedFile[]): Promise<Blob> {
  const stream = buildTar(files).stream().pipeThrough(new CompressionStream("gzip"));
  return new Response(stream).blob();
}

export interface UploadReport {
  files: number;
  bytes: number;
  excluded?: string[];
  excluded_count: number;
  skipped?: string[];
  skipped_count: number;
  history: boolean;
  history_ignored?: boolean;
  branch: string;
  commit: string;
  version?: string;
  changed: boolean;
}

export class UploadError extends Error {
  constructor(
    public status: number,
    message: string,
    public body: Record<string, unknown> = {},
  ) {
    super(message);
    this.name = "UploadError";
  }
}

// XHR because fetch reports no upload progress. meta must be appended before the archive: the
// daemon reads it first.
export function sendUpload<T>(
  path: string,
  archive: Blob,
  archiveName: string,
  meta: Record<string, unknown> | null,
  onProgress: (sent: number, total: number) => void,
): Promise<T> {
  const form = new FormData();
  if (meta) form.append("meta", JSON.stringify(meta));
  form.append("archive", archive, archiveName);
  return new Promise<T>((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", `${apiBase()}${path}`);
    xhr.withCredentials = true;
    for (const [k, v] of Object.entries(csrfHeaders())) xhr.setRequestHeader(k, v);
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) onProgress(e.loaded, e.total);
    };
    xhr.onerror = () => reject(new UploadError(0, ""));
    xhr.onload = () => {
      let body: { error?: string } & Record<string, unknown> = {};
      try {
        body = JSON.parse(xhr.responseText) as typeof body;
      } catch {
        // not JSON: the status speaks for itself
      }
      if (xhr.status >= 200 && xhr.status < 300) resolve(body as T);
      else reject(new UploadError(xhr.status, body.error ?? `HTTP ${xhr.status}`, body));
    };
    xhr.send(form);
  });
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}

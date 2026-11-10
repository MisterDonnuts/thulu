// AUDIT_MAX is the server's ceiling on `limit`.
export const AUDIT_PAGE = 100;
export const AUDIT_MAX = 1000;

export function auditWindowLimit(loaded: number): number {
  return Math.min(AUDIT_MAX, Math.max(AUDIT_PAGE, loaded));
}

// Offset paging overlaps when an entry is written between requests; dedupe by id.
export function mergeAuditPage<T extends { id: string }>(current: T[], more: T[]): T[] {
  const seen = new Set(current.map((e) => e.id));
  return current.concat(more.filter((e) => !seen.has(e.id)));
}

export function auditExportParams(format: "csv" | "json", kind: string): URLSearchParams {
  const params = new URLSearchParams({ format });
  if (kind) params.set("kind", kind);
  return params;
}

export function filenameFromDisposition(header: string | null, fallback: string): string {
  if (!header) return fallback;
  const star = /filename\*=(?:UTF-8'')?["']?([^"';]+)/i.exec(header);
  if (star?.[1]) {
    try {
      return decodeURIComponent(star[1]);
    } catch {
      return star[1];
    }
  }
  const plain = /filename=["']?([^"';]+)/i.exec(header);
  return plain?.[1] ?? fallback;
}

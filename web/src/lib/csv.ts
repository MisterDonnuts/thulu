export interface CSVSection {
  title: string;
  header: string[];
  rows: unknown[][];
}

export function csvCell(v: unknown): string {
  const s = String(v ?? "");
  return /[",\n\r]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
}

// CRLF and a trailing newline per RFC 4180; Excel opens it without complaint.
export function buildCSV(sections: CSVSection[]): string {
  const lines: string[] = [];
  for (const s of sections) {
    if (lines.length) lines.push("");
    lines.push(csvCell(s.title));
    lines.push(s.header.map(csvCell).join(","));
    for (const r of s.rows) lines.push(r.map(csvCell).join(","));
  }
  return lines.join("\r\n") + "\r\n";
}

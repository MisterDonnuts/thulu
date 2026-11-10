export function esc(s: string | null | undefined): string {
  if (s === null || s === undefined) return "";
  return String(s).replace(
    /[&<>"']/g,
    (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c] ?? c,
  );
}

export type AttrValue = string | number | boolean | null | undefined;
export type Attrs = Record<string, AttrValue>;

// Attribute names are written unescaped; never pass user input as a name.
export function attrsHTML(attrs: Attrs | undefined): string {
  if (!attrs) return "";
  let out = "";
  for (const [name, value] of Object.entries(attrs)) {
    if (value === false || value === null || value === undefined) continue;
    out += value === true ? ` ${name}` : ` ${name}="${esc(String(value))}"`;
  }
  return out;
}

export function $(id: string): HTMLElement {
  return document.getElementById(id) as HTMLElement;
}

export function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

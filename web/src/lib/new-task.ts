export function parseTags(text: string): string[] {
  const out: string[] = [];
  for (const raw of text.split(",")) {
    const tag = raw.trim();
    if (tag && !out.includes(tag)) out.push(tag);
  }
  return out;
}

export function preferredProject(remembered: string | null, projectIDs: string[]): string {
  return remembered && projectIDs.includes(remembered) ? remembered : "";
}

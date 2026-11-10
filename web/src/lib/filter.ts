// Whitespace is deliberately not trimmed: "fix " is a word-boundary search.
export function matchesQuery(query: string, fields: Array<string | undefined | null>): boolean {
  if (!query) return true;
  const q = query.toLowerCase();
  return fields.some((f) => (f ?? "").toLowerCase().includes(q));
}

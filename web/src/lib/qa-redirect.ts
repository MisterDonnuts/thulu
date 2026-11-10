// Kept separate so the redirect page does not ship the i18n catalog; do not import it here.

export function qaRedirectTarget(search: string, hash = ""): string {
  const p = new URLSearchParams(search);
  p.delete("tab");
  const rest = p.toString();
  return `/evidence/?tab=tasks${rest ? `&${rest}` : ""}${hash}`;
}

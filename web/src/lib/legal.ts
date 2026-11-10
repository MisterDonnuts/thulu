export interface AuthConfigLegal {
  legal?: { entity?: string; contact?: string; hosting?: string; hosting_region?: string };
  sso_providers?: Array<{ key: string; name: string }>;
}

export function contactHref(contact: string): string | null {
  const c = contact.trim();
  if (/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(c)) return `mailto:${c}`;
  if (/^https:\/\/\S+$/i.test(c)) return c;
  return null;
}

export function providerNames(cfg: AuthConfigLegal): string {
  return (cfg.sso_providers ?? [])
    .map((p) => p.name.trim())
    .filter(Boolean)
    .join(", ");
}

export function fillLegalIdentity(root: ParentNode, cfg: AuthConfigLegal): void {
  const contact = cfg.legal?.contact?.trim() ?? "";
  const idp = providerNames(cfg);
  for (const field of ["entity", "hosting", "hosting_region"] as const) {
    const value = cfg.legal?.[field]?.trim() ?? "";
    if (!value) continue;
    for (const el of root.querySelectorAll<HTMLElement>(`[data-legal="${field}"]`)) {
      el.textContent = value;
      el.classList.remove("legal-unset");
    }
  }
  for (const el of root.querySelectorAll<HTMLElement>('[data-legal="contact"]')) {
    if (!contact) continue;
    el.textContent = contact;
    el.classList.remove("legal-unset");
    const href = contactHref(contact);
    if (href && el instanceof HTMLAnchorElement) el.href = href;
  }
  for (const el of root.querySelectorAll<HTMLElement>('[data-legal="idp"]')) {
    if (idp) el.textContent = idp;
  }
}

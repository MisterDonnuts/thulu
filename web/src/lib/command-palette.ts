import { t } from "../i18n";
import { agentHref, boardHref, projectHref, taskHref } from "./links";

export type PaletteKind = "page" | "task" | "project" | "agent" | "board";

export const KINDS: readonly PaletteKind[] = ["page", "task", "project", "agent", "board"];

export interface PaletteItem {
  kind: PaletteKind;
  id: string;
  label: string;
  detail?: string;
  href: string;
  terms?: string[];
}

export interface PaletteGroup {
  kind: PaletteKind;
  items: PaletteItem[];
}

export interface PageAccess {
  login: boolean;
  operator: boolean;
}

export function pageItems(access: PageAccess): PaletteItem[] {
  const settings = t("nav.links.settings");
  const app: Array<[string, string]> = [
    ["/", t("common.nav.dashboard")],
    ["/tasks/", t("common.nav.tasks")],
    ["/projects/", t("common.nav.projects")],
    ["/inbox/", t("nav.links.inbox")],
    ["/evidence/", t("nav.links.evidence")],
    ["/agents/", t("common.nav.agents")],
    ["/skills/", t("nav.links.skills")],
    ["/automations/", t("nav.links.automations")],
    ["/onboarding/", t("layout.header.setup")],
  ];
  const admin: Array<[string, string, boolean]> = [
    ["/settings/#team", t("nav.links.members"), access.login],
    ["/settings/#boards", t("nav.links.boards"), true],
    ["/settings/#llm", t("nav.links.ai_engines"), true],
    ["/mcps/", t("nav.links.mcps"), true],
    ["/credentials/", t("nav.links.credentials"), true],
    ["/settings/#webhooks", t("nav.links.webhooks"), true],
    ["/settings/#api", t("nav.links.api"), true],
    ["/sandboxes/", t("nav.links.sandboxes"), true],
    ["/usage/", t("nav.links.usage"), true],
    ["/infra/", t("nav.links.infra"), access.operator],
    ["/settings/#health", t("nav.links.health"), true],
    ["/settings/#updates", t("nav.links.updates"), true],
    ["/settings/#audit", t("nav.links.audit_log"), true],
    ["/settings/#language", t("nav.links.language"), true],
    ["/settings/#account", t("nav.links.account"), access.login],
  ];
  return [
    ...app.map(([href, label]) => ({ kind: "page" as const, id: href, label, href })),
    ...admin
      .filter(([, , shown]) => shown)
      .map(([href, label]) => ({
        kind: "page" as const,
        id: href,
        label,
        detail: settings,
        href,
        terms: [settings],
      })),
  ];
}

export interface TaskLike {
  id: string;
  title: string;
  board_id?: string;
  updated_at?: string;
  created_at?: string;
}

export function taskItems(tasks: TaskLike[], boards: Map<string, string>): PaletteItem[] {
  const when = (x: TaskLike) => new Date(x.updated_at ?? x.created_at ?? 0).getTime() || 0;
  return [...tasks]
    .sort((a, b) => when(b) - when(a))
    .map((x) => ({
      kind: "task" as const,
      id: x.id,
      label: x.title !== "" ? x.title : x.id,
      detail: boards.size > 1 ? boards.get(x.board_id ?? "") : undefined,
      href: taskHref(x.id, x.board_id),
      terms: [x.id],
    }));
}

export function projectItems(
  projects: Array<{ id: string; name: string; repo_url?: string }>,
): PaletteItem[] {
  return projects.map((p) => ({
    kind: "project" as const,
    id: p.id,
    label: p.name !== "" ? p.name : p.id,
    detail: p.repo_url !== "" ? p.repo_url : undefined,
    href: projectHref(p.id),
  }));
}

export function agentItems(agents: Array<{ name: string; description?: string }>): PaletteItem[] {
  return agents.map((a) => ({
    kind: "agent" as const,
    id: a.name,
    label: a.name,
    detail: a.description !== "" ? a.description : undefined,
    href: agentHref(a.name),
  }));
}

export function boardItems(boards: Array<{ id: string; label?: string }>): PaletteItem[] {
  return boards.map((b) => ({
    kind: "board" as const,
    id: b.id,
    label: b.label ?? b.id,
    href: boardHref(b.id),
  }));
}

export function fold(s: string): string {
  return s.normalize("NFD").replace(/[̀-ͯ]/g, "").toLowerCase();
}

export function rank(query: string, item: PaletteItem): number {
  const words = fold(query).split(/\s+/).filter(Boolean);
  if (words.length === 0) return 0;
  const label = fold(item.label);
  const hay = [label, fold(item.detail ?? ""), ...(item.terms ?? []).map(fold)].join(" ");
  if (!words.every((w) => hay.includes(w))) return -1;
  const q = words.join(" ");
  if (label.startsWith(q)) return 0;
  if (label.split(/[\s\-_/·.]+/).some((w) => w.startsWith(words[0]))) return 1;
  if (label.includes(words[0])) return 2;
  return 3;
}

export const PALETTE_TTL_MS = 30_000;

export interface PaletteRead {
  at: number;
  writes: number;
}

export function paletteStale(
  read: PaletteRead | null,
  now: number,
  writes: number,
  ttl = PALETTE_TTL_MS,
): boolean {
  return read === null || now - read.at >= ttl || writes !== read.writes;
}

export function taskChangeStalesPalette(type: string): boolean {
  return type === "task_created" || type === "task_deleted";
}

export const LIMITS: Readonly<Record<PaletteKind, number>> = {
  page: 6,
  task: 8,
  project: 5,
  agent: 5,
  board: 5,
};

export function searchPalette(
  query: string,
  items: PaletteItem[],
  limits: Readonly<Record<PaletteKind, number>> = LIMITS,
): PaletteGroup[] {
  const q = query.trim();
  if (!q) {
    const groups: PaletteGroup[] = [];
    for (const kind of ["task", "page"] as const) {
      const ofKind = items.filter((i) => i.kind === kind);
      const picked = kind === "task" ? ofKind.slice(0, 5) : ofKind.filter((i) => !i.detail);
      if (picked.length) groups.push({ kind, items: picked });
    }
    return groups;
  }
  const scored: Array<PaletteGroup & { best: number }> = [];
  for (const kind of KINDS) {
    const matches = items
      .filter((i) => i.kind === kind)
      .map((item, at) => ({ item, at, score: rank(q, item) }))
      .filter((x) => x.score >= 0)
      .sort((a, b) => a.score - b.score || a.at - b.at)
      .slice(0, limits[kind]);
    if (matches.length) {
      scored.push({ kind, items: matches.map((x) => x.item), best: matches[0].score });
    }
  }
  return scored.sort((a, b) => a.best - b.best).map(({ kind, items }) => ({ kind, items }));
}

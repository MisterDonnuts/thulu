import { t } from "../i18n";
import { api } from "./api";
import { badgeHTML, downloadBlob, setCount, type CountOptions } from "./ui";
import type { BadgeTone } from "./badges";

export type WorkshopKind = "agent" | "skill";

export type WorkshopStatus = "available" | "installed" | "update" | "changed" | "reserved";

export interface WorkshopEntry {
  kind: WorkshopKind;
  name: string;
  description?: string;
  tags: string[];
  revision: number;
  status: WorkshopStatus;
  installed_revision?: number;
  edited?: boolean;
  disabled?: boolean;
  skills?: string[];
  requires?: string[];
  used_by?: string[];
  mcp?: string[];
  stages?: string[];
  image?: string;
  engine?: string;
  model?: string;
}

export interface WorkshopFile {
  path: string;
  body: string;
  encoding?: "base64";
}

export interface WorkshopDetail extends WorkshopEntry {
  files: WorkshopFile[];
}

export type Action = "install" | "same" | "update" | "conflict" | "reserved" | "rejected";
export type Resolution = "install" | "keep" | "update" | "replace" | "copy" | "skip";

export interface PlanItem {
  kind: WorkshopKind;
  name: string;
  action: Action;
  resolution: Resolution;
  options: Resolution[];
  target: string;
  revision?: number;
  installed_revision?: number;
  edited?: boolean;
  disabled?: boolean;
  used_by?: string[];
  as_copy?: boolean;
  skill_renames?: Record<string, string>;
}

export interface PlanNotice {
  code: string;
  severity: "warning" | "error";
  kind?: WorkshopKind;
  name?: string;
  refs?: string[];
}

export interface Plan {
  source: "workshop" | "import";
  items: PlanItem[];
  notices: PlanNotice[];
  blocked: boolean;
  writes: number;
}

export interface ItemRef {
  kind: WorkshopKind;
  name: string;
}

export interface CopiedRef {
  kind: WorkshopKind;
  from: string;
  to: string;
}

export interface ApplyResult {
  plan: Plan;
  result: {
    installed: ItemRef[];
    updated: ItemRef[];
    replaced: ItemRef[];
    copied: CopiedRef[];
    kept: ItemRef[];
    skipped: ItemRef[];
  };
}

export interface Choices {
  resolutions: Record<string, Resolution>;
  names: Record<string, string>;
}

export type PlanInput = { items: ItemRef[] } | { bundle: unknown };

export function listWorkshop(): Promise<WorkshopEntry[]> {
  return api.get<WorkshopEntry[]>("/workshop");
}

export function getWorkshopItem(kind: WorkshopKind, name: string): Promise<WorkshopDetail> {
  return api.get<WorkshopDetail>(`/workshop/${kind}/${encodeURIComponent(name)}`);
}

export function planWorkshop(
  input: PlanInput,
  choices: Choices = { resolutions: {}, names: {} },
): Promise<Plan> {
  return api.post<Plan>("/workshop/plan", { ...input, ...choices });
}

export function applyWorkshop(input: PlanInput, choices: Choices): Promise<ApplyResult> {
  return api.post<ApplyResult>("/workshop/apply", { ...input, ...choices });
}

export async function exportItem(kind: WorkshopKind, name: string): Promise<void> {
  const base = kind === "agent" ? "/types" : "/skills";
  const body = await api.getText(`${base}/${encodeURIComponent(name)}/export`);
  downloadBlob(new Blob([body], { type: "application/json" }), exportFilename(kind, name));
}

export function exportFilename(kind: WorkshopKind, name: string): string {
  return `octopus-${kind}-${name}.json`;
}

export async function exportItems(items: ItemRef[], filename: string): Promise<void> {
  const bundle = await api.post<unknown>("/workshop/export", { items });
  downloadBlob(new Blob([JSON.stringify(bundle, null, 2)], { type: "application/json" }), filename);
}

export function exportAllFilename(kind: WorkshopKind): string {
  return kind === "agent" ? "octopus-agents.json" : "octopus-skills.json";
}

export async function workshopUpdates(kind: WorkshopKind): Promise<Set<string>> {
  try {
    return updatesOf(await listWorkshop(), kind);
  } catch {
    return new Set();
  }
}

export function updatesOf(entries: WorkshopEntry[], kind: WorkshopKind): Set<string> {
  return new Set(
    entries.filter((e) => e.kind === kind && e.status === "update").map((e) => e.name),
  );
}

export function updateBadgeHTML(): string {
  return badgeHTML(t("workshop.update_badge"), "accent", {
    title: t("workshop.update_badge_hint"),
  });
}

// Count.astro draws the count with these props and paintUpdates() repaints it; keep both in sync.
export const UPDATES_COUNT: Omit<CountOptions, "id" | "attrs" | "title"> = {
  pill: true,
  tone: "accent",
  hideZero: true,
};

export function workshopButtonLabel(updates: number): string {
  return updates
    ? `${t("workshop.open")}, ${t("workshop.updates_count", { count: updates })}`
    : t("workshop.open");
}

export function paintUpdates(button: HTMLElement, count: HTMLElement, updates: number): void {
  setCount(count, updates, {
    ...UPDATES_COUNT,
    title: t("workshop.updates_count", { count: updates }),
  });
  button.setAttribute("aria-label", workshopButtonLabel(updates));
}

export function itemKey(it: { kind: string; name: string }): string {
  return `${it.kind}/${it.name}`;
}

export function statusTone(s: WorkshopStatus): BadgeTone {
  switch (s) {
    case "installed":
      return "ok";
    case "update":
      return "warn";
    case "changed":
      return "muted";
    case "reserved":
      return "danger";
    case "available":
      return "neutral";
  }
}

export function filterEntries(
  entries: WorkshopEntry[],
  kind: WorkshopKind,
  query: string,
  tags: string[] = [],
): WorkshopEntry[] {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  return entries.filter((e) => {
    if (e.kind !== kind) return false;
    if (tags.some((tag) => !e.tags.includes(tag))) return false;
    const hay =
      `${e.name} ${e.description ?? ""} ${e.tags.join(" ")} ${e.tags.map(tagLabel).join(" ")}`.toLowerCase();
    return words.every((w) => hay.includes(w));
  });
}

const TAG_KEYS: Partial<Record<string, string>> = {
  code: "workshop.tags.code",
  docs: "workshop.tags.docs",
  example: "workshop.tags.example",
  release: "workshop.tags.release",
};

export function tagLabel(tag: string): string {
  const key = TAG_KEYS[tag];
  return key ? t(key) : tag;
}

export function tagsOf(entries: WorkshopEntry[], kind: WorkshopKind): string[] {
  const set = new Set<string>();
  for (const e of entries) if (e.kind === kind) e.tags.forEach((t) => set.add(t));
  return [...set].sort();
}

export function choosable(plan: Plan): PlanItem[] {
  return plan.items.filter((it) => it.options.length > 1);
}

export function resolutionsOf(plan: Plan): Record<string, Resolution> {
  const out: Record<string, Resolution> = {};
  for (const it of choosable(plan)) out[itemKey(it)] = it.resolution;
  return out;
}

export function namesOf(plan: Plan): Record<string, string> {
  const out: Record<string, string> = {};
  for (const it of plan.items) if (it.resolution === "copy") out[itemKey(it)] = it.target;
  return out;
}

export function choicesOf(plan: Plan): Choices {
  return { resolutions: resolutionsOf(plan), names: namesOf(plan) };
}

// Names commit on change; clicking Install blurred the input and applied before the
// new plan returned, so the copy took the daemon's name. Returns null when nothing changed.
export function withTypedNames(plan: Plan, typed: Record<string, string>): Choices | null {
  const choices = choicesOf(plan);
  let changed = false;
  for (const [key, raw] of Object.entries(typed)) {
    const name = raw.trim();
    if (name && key in choices.names && choices.names[key] !== name) {
      choices.names[key] = name;
      changed = true;
    }
  }
  return changed ? choices : null;
}

export function badgeOf(it: PlanItem): Resolution | "keep_yours" {
  return it.resolution === "keep" && (it.action === "conflict" || it.action === "update")
    ? "keep_yours"
    : it.resolution;
}

export function copyActionText(it: PlanItem): string | null {
  if (!it.as_copy) return null;
  return it.action === "same"
    ? t("workshop.plan.action.copy_same", { target: it.target })
    : t("workshop.plan.action.copy_conflict", { target: it.target });
}

// Must match how the daemon counts writes.
export function writes(r: Resolution): boolean {
  return r !== "keep" && r !== "skip";
}

export function writesOf(plan: Plan): number {
  return plan.items.filter((it) => writes(it.resolution)).length;
}

export function applyVerb(plan: Plan): "install" | "update" | "apply" {
  const kinds = new Set(
    plan.items.filter((it) => writes(it.resolution)).map((it) => it.resolution),
  );
  if (kinds.size === 1 && kinds.has("update")) return "update";
  if ([...kinds].every((r) => r === "install" || r === "copy")) return "install";
  return "apply";
}

export function resultIsEmpty(r: ApplyResult["result"]): boolean {
  return (
    r.installed.length +
      r.updated.length +
      r.replaced.length +
      r.copied.length +
      r.skipped.length ===
    0
  );
}

export async function readBundleFile(file: File, maxBytes = 3 * 1024 * 1024): Promise<unknown> {
  if (file.size > maxBytes) throw new BundleError("too_large");
  const text = await file.text();
  try {
    return JSON.parse(text) as unknown;
  } catch {
    throw new BundleError("not_json");
  }
}

export class BundleError extends Error {
  constructor(public code: "too_large" | "not_json") {
    super(code);
    this.name = "BundleError";
  }
}

export function previewBody(f: WorkshopFile): string | null {
  return f.encoding === "base64" ? null : f.body;
}

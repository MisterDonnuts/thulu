import type { StepState } from "./ui/steps";

// Must match the daemon's agentstore.nameRe.
export const AGENT_NAME_RE = /^[a-z0-9_][a-z0-9_-]{0,63}$/;

// Must match agentstore reservedOwners and reservedAgentNames.
export const RESERVED_AGENT_NAMES = ["octopus", "runs", "user", "system", "auditor", "automation"];

export type NameProblem = "" | "required" | "invalid" | "reserved" | "taken";

export function agentNameProblem(name: string, taken: string[]): NameProblem {
  if (!name) return "required";
  if (!AGENT_NAME_RE.test(name)) return "invalid";
  if (RESERVED_AGENT_NAMES.includes(name)) return "reserved";
  if (taken.includes(name)) return "taken";
  return "";
}

export function suggestAgentName(name: string): string {
  const out = name
    .trim()
    .toLowerCase()
    .normalize("NFD")
    .replace(/[̀-ͯ]/g, "")
    .replace(/[^a-z0-9_-]+/g, "-")
    .replace(/-{2,}/g, "-")
    .replace(/^-+/, "")
    .slice(0, 64)
    .replace(/-+$/, "");
  if (!out || out === name || !AGENT_NAME_RE.test(out)) return "";
  return out;
}

// The daemon stores "auto" for a new agent; the select spells auto as "". Without this mapping
// every first edit reads as a change.
export function formSkillsMode(v: string | undefined): string {
  return v === "native" || v === "inline" ? v : "";
}

// The form reads stages in screen order, which differs from the stored order when a stage is no
// longer a column; keeping stored order stops an untouched list reading as a change.
export function keepOrder(prev: string[], next: string[]): string[] {
  return [...prev.filter((x) => next.includes(x)), ...next.filter((x) => !prev.includes(x))];
}

export function copyStageConfig(
  map: Record<string, string[]>,
  from: string,
  to: string[],
): Record<string, string[]> {
  const src = map[from] ?? [];
  const targets = to.filter((st) => st !== from);
  const out: Record<string, string[]> = {};
  for (const [st, list] of Object.entries(map)) {
    if (!targets.includes(st)) out[st] = list;
  }
  if (src.length) for (const st of targets) out[st] = [...src];
  return out;
}

export type CheckState = "ok" | "todo" | "unknown";

export const CHECK_STEP: Record<CheckState, StepState> = {
  ok: "done",
  todo: "pending",
  unknown: "unknown",
};

export interface CheckItem {
  id: "stages" | "prompt" | "credential" | "assigned";
  state: CheckState;
  missing?: string[];
  viaModes?: boolean;
  anyColumn?: boolean;
  modes?: string[];
  rejected?: boolean;
}

export const MODE_STAGES = ["mention", "chat"];

export interface ChecklistInput {
  validStages: string[];
  knownStages: string[];
  prompts: { fallback: boolean; own: string[] } | null;
  hasCredential: boolean | null;
  keyRejected?: boolean;
  assigned: number | null;
}

export function savedStagesLine(
  saved: string[] | undefined,
  labelOf: (id: string) => string,
): string | null {
  return saved?.length ? saved.map(labelOf).join(" · ") : null;
}

export function agentChecklist(i: ChecklistInput): CheckItem[] {
  const live = i.validStages.filter((s) => i.knownStages.includes(s));
  // The daemon runs an agent with no ticked stage in any column ("Use in column" relies on this),
  // so that is not a gap.
  const items: CheckItem[] = [
    i.validStages.length === 0
      ? { id: "stages", state: "ok", anyColumn: true }
      : { id: "stages", state: live.length ? "ok" : "todo" },
  ];

  if (!i.prompts) items.push({ id: "prompt", state: "unknown" });
  else {
    const p = i.prompts;
    const missing = p.fallback ? [] : live.filter((s) => !p.own.includes(s));
    const ok = live.length ? missing.length === 0 : p.fallback;
    items.push({ id: "prompt", state: ok ? "ok" : "todo", missing });
  }

  if (i.hasCredential && i.keyRejected)
    items.push({ id: "credential", state: "todo", rejected: true });
  else
    items.push({
      id: "credential",
      state: i.hasCredential === null ? "unknown" : i.hasCredential ? "ok" : "todo",
    });

  if (i.assigned === null) items.push({ id: "assigned", state: "unknown" });
  else if (i.assigned > 0) items.push({ id: "assigned", state: "ok" });
  else if (live.some((s) => MODE_STAGES.includes(s)))
    items.push({
      id: "assigned",
      state: "ok",
      viaModes: true,
      modes: MODE_STAGES.filter((m) => live.includes(m)),
    });
  else items.push({ id: "assigned", state: "todo" });
  return items;
}

export interface EffectiveCredential {
  source: string;
  has_credential: boolean;
  host_fallback?: boolean;
}

export function runsWithCredential(eff: EffectiveCredential): boolean {
  return eff.has_credential || (eff.source === "host" && eff.host_fallback === true);
}

export function hostWithoutKey(eff: EffectiveCredential): boolean {
  return eff.source === "host" && !runsWithCredential(eff);
}

export interface EffectiveNote {
  keyTone?: "danger" | "warn" | null;
  hasCredential: boolean;
  warnings: number;
}

export function effectiveNoteTone(n: EffectiveNote): "info" | "warn" | "danger" {
  if (n.keyTone === "danger") return "danger";
  if (n.keyTone === "warn" || !n.hasCredential || n.warnings > 0) return "warn";
  return "info";
}

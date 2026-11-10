// Must match the server's launch-time decision in types.StageSkills.

import { parseMCPEntry } from "./mcp-entry";

export interface SystemSkillInfo {
  name: string;
  description?: string;
  mcp: string;
  enforcement?: string;
}

export interface SystemSkillRow extends SystemSkillInfo {
  wired: boolean;
  enabled: boolean;
}

export const RUN_SKILL = "octopus-run";

export function stageWiresMCP(entries: string[], server: string): boolean {
  if (!server) return true;
  return entries.some((e) => parseMCPEntry(e.trim()).server === server);
}

export function systemSkillRows(
  catalog: SystemSkillInfo[],
  mcpEntries: string[],
  off: string[],
): SystemSkillRow[] {
  const offSet = new Set(off.map((n) => n.trim()));
  return catalog.map((sk) => ({
    ...sk,
    wired: stageWiresMCP(mcpEntries, sk.mcp),
    enabled: !offSet.has(sk.name),
  }));
}

export interface ShownToggle {
  stage: string;
  name: string;
  on: boolean;
}

// Opt-outs the form did not render are kept: dropping them would silently re-enable the skill.
// Lists are sorted and deduped, as the server stores them.
export function harvestSystemSkillsOff(
  previous: Record<string, string[]>,
  shown: ShownToggle[],
  activeStages: string[],
): Record<string, string[]> {
  const active = new Set(activeStages);
  const rendered = new Set(shown.map((s) => `${s.stage}/${s.name}`));
  const byStage = new Map<string, Set<string>>();
  const add = (stage: string, name: string): void => {
    const names = byStage.get(stage) ?? new Set<string>();
    names.add(name);
    byStage.set(stage, names);
  };
  for (const [stage, names] of Object.entries(previous)) {
    if (!active.has(stage)) continue;
    for (const name of names) {
      if (!rendered.has(`${stage}/${name}`)) add(stage, name);
    }
  }
  for (const s of shown) {
    if (!s.on && active.has(s.stage)) add(s.stage, s.name);
  }
  // Sorted keys: the server returns them sorted and the dirty check compares JSON.
  const out: Record<string, string[]> = {};
  for (const stage of [...byStage.keys()].sort()) {
    out[stage] = [...(byStage.get(stage) ?? [])].sort();
  }
  return out;
}

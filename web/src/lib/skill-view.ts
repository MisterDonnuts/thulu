export interface SkillGrant {
  agent: string;
  stages?: string[];
  auditor_stages?: string[];
}

export interface SkillFile {
  path: string;
  size?: number;
  deleted?: boolean;
}

export type Enforcement = "on_demand" | "always";

// Must match the daemon's api.skillDescriptionMax; engines also cut at this length.
export const DESCRIPTION_MAX = 1024;

export function enforcementOf(value?: string): Enforcement {
  return value === "always" ? "always" : "on_demand";
}

export function descriptionCount(text: string): { text: string; count: number; over: boolean } {
  const folded = text.split(/\s+/).filter(Boolean).join(" ");
  const count = [...folded].length;
  return { text: folded, count, over: count > DESCRIPTION_MAX };
}

export function grantStages(stages: string[] | undefined, label: (id: string) => string): string {
  return (stages ?? []).map(label).join(", ");
}

export function orderFiles(files: SkillFile[]): SkillFile[] {
  const live = files.filter((f) => !f.deleted);
  const gone = files.filter((f) => f.deleted).sort((a, b) => a.path.localeCompare(b.path));
  return [...live, ...gone];
}

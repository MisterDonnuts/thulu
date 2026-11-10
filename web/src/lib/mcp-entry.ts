export interface ExposedTool {
  name: string;
  description?: string;
  scope?: string;
}

export interface ToolDef {
  name: string;
  description?: string;
  type?: string;
  tools?: ExposedTool[];
  default_scope?: string;
  source?: string;
}

export interface MCPEntry {
  server: string;
  scope: string | null;
  tools: string[] | null;
}

const SCOPE_WORDS = ["read", "write", "admin"];

// Tools are "+"-separated because the stage is persisted as a comma-delimited
// `thulu.mcp.default.<stage>` label; commas are still parsed for hand-edited compose files.
// The scope keyword must be preserved: octopus-api grants read when none is present.
export function parseMCPEntry(entry: string): MCPEntry {
  const atIdx = entry.indexOf("@");
  if (atIdx === -1) return { server: entry, scope: null, tools: null };
  const server = entry.slice(0, atIdx);
  const suffix = entry.slice(atIdx + 1).trim();
  const parts = suffix
    .split(/[+,]/)
    .map((t) => t.trim())
    .filter(Boolean);
  const scope = parts.find((p) => SCOPE_WORDS.includes(p)) ?? null;
  const named = parts.filter((p) => !SCOPE_WORDS.includes(p));
  return { server, scope, tools: named.length > 0 ? named : null };
}

export function formatMCPEntry(e: MCPEntry): string {
  const named = e.tools && e.tools.length > 0 ? e.tools : null;
  if (e.scope && named) return `${e.server}@${[e.scope, ...named].join("+")}`;
  if (named) return `${e.server}@${named.join("+")}`;
  return e.scope ? `${e.server}@${e.scope}` : e.server;
}

export function scopeGrants(scope: string | null, toolScope?: string): boolean {
  if (!scope) return false;
  if (!toolScope) return true;
  const ladder = ["read", "write", "admin"];
  const have = ladder.indexOf(scope);
  const need = ladder.indexOf(toolScope);
  if (have === -1 || need === -1) return false;
  return need <= have;
}

const DESCRIPTOR_SOURCES = ["store", "disk", "builtin"];

// A server known only from a probe, or a scoped descriptor without default_scope, applies a default
// nobody wrote down.
// A registered entry here is an outside server (shipped descriptors win by name), and its bare name
// grants every tool.
export function bareGrantKnown(def: ToolDef | undefined): boolean {
  if (!def) return false;
  if (def.default_scope) return true;
  const scoped = (def.tools ?? []).some((t) => t.scope);
  if (scoped) return false;
  return DESCRIPTOR_SOURCES.includes(def.source ?? "") || def.source === "registered";
}

export function isBareEntry(e: MCPEntry | undefined): boolean {
  return Boolean(e && !e.scope && !(e.tools && e.tools.length > 0));
}

// Prefer a scope keyword over listing tools: a listed stage is frozen and never gets tools added to
// the server later.
export function entryForSelection(
  def: ToolDef | undefined,
  server: string,
  loadedScope: string | null,
  selected: string[],
): MCPEntry {
  const picked = new Set(selected);
  if (!def || !(def.tools ?? []).some((t) => t.scope)) {
    const all = (def?.tools ?? []).length;
    const everything = all > 0 && selected.length === all;
    if (everything && (loadedScope || bareGrantKnown(def))) {
      return { server, scope: loadedScope, tools: null };
    }
    return { server, scope: loadedScope, tools: selected };
  }
  // A candidate wins only if it covers strictly more: with no "admin" tools, admin equals write,
  // and emitting @admin would grant a wider word than ticked.
  let best: { scope: string | null; extras: string[]; size: number } = {
    scope: null,
    extras: selected,
    size: 0,
  };
  for (const candidate of ["read", "write", "admin"]) {
    const covered = (def.tools ?? [])
      .filter((t) => scopeGrants(candidate, t.scope))
      .map((t) => t.name);
    if (covered.length <= best.size) continue;
    if (!covered.every((name) => picked.has(name))) continue;
    best = {
      scope: candidate,
      extras: selected.filter((name) => !covered.includes(name)),
      size: covered.length,
    };
  }
  const isDefault = best.scope !== null && best.scope === (def.default_scope ?? null);
  return {
    server,
    scope: isDefault && best.extras.length === 0 ? null : best.scope,
    tools: best.extras.length > 0 ? best.extras : null,
  };
}

export function entryForSave(
  def: ToolDef | undefined,
  server: string,
  loaded: MCPEntry | undefined,
  selected: string[],
): MCPEntry | null {
  if (selected.length === 0) {
    return isBareEntry(loaded) && !bareGrantKnown(def)
      ? { server, scope: null, tools: null }
      : null;
  }
  return entryForSelection(def, server, loaded?.scope ?? null, selected);
}

export function grantedTools(def: ToolDef, entry: MCPEntry | undefined): Set<string> {
  const out = new Set<string>();
  if (!entry) return out;
  if (isBareEntry(entry) && !bareGrantKnown(def)) return out;
  const namesOnly = Boolean(entry.tools && !entry.scope);
  if (!namesOnly) {
    const scope = entry.scope ?? def.default_scope ?? null;
    for (const tool of def.tools ?? []) {
      // A descriptor with no scopes is one whole-server grant; scopeGrants(null, …) would tick
      // nothing and Save would drop the server.
      const granted = scope === null ? !tool.scope : scopeGrants(scope, tool.scope);
      if (granted) out.add(tool.name);
    }
  }
  for (const name of entry.tools ?? []) out.add(name);
  return out;
}

export function onLadder(toolScope?: string): boolean {
  return !toolScope || SCOPE_WORDS.includes(toolScope);
}

function ladderTools(def: ToolDef | undefined): string[] {
  return (def?.tools ?? []).filter((t) => onLadder(t.scope)).map((t) => t.name);
}

export function serverTickSelection(def: ToolDef | undefined): string[] {
  const scope = def?.default_scope;
  if (!scope) return ladderTools(def);
  return (def.tools ?? []).filter((t) => scopeGrants(scope, t.scope)).map((t) => t.name);
}

export function allToolsToggle(def: ToolDef | undefined, selected: string[]): string[] {
  return selected.length > 0 ? [] : ladderTools(def);
}

export function allToolsState(
  def: ToolDef | undefined,
  selected: string[],
): "all" | "some" | "none" {
  if (selected.length === 0) return "none";
  const ladder = ladderTools(def);
  const picked = new Set(selected);
  return ladder.length > 0 && ladder.every((name) => picked.has(name)) ? "all" : "some";
}

export interface ToolGroup {
  scope: string;
  byName: boolean;
  tools: ExposedTool[];
}

export function toolGroups(def: ToolDef | undefined): ToolGroup[] {
  const byScope = new Map<string, ExposedTool[]>();
  for (const tool of def?.tools ?? []) {
    const key = tool.scope ?? "";
    byScope.set(key, [...(byScope.get(key) ?? []), tool]);
  }
  const rank = (scope: string): number => {
    const i = SCOPE_WORDS.indexOf(scope);
    if (i !== -1) return i;
    return scope ? SCOPE_WORDS.length : SCOPE_WORDS.length + 1;
  };
  return [...byScope.entries()]
    .sort(([a], [b]) => rank(a) - rank(b) || a.localeCompare(b))
    .map(([scope, tools]) => ({ scope, byName: !onLadder(scope), tools }));
}

export type StageGrant =
  | { kind: "bare" }
  | { kind: "scope"; scope: string; extras: string[] }
  | { kind: "list"; tools: string[] };

export function stageGrant(entry: MCPEntry | null | undefined): StageGrant | null {
  if (!entry) return null;
  if (isBareEntry(entry)) return { kind: "bare" };
  if (entry.scope) return { kind: "scope", scope: entry.scope, extras: entry.tools ?? [] };
  return { kind: "list", tools: entry.tools ?? [] };
}

export function bareGrantSummary(def: ToolDef | undefined): string {
  if (!def || !bareGrantKnown(def)) return "unknown";
  return def.default_scope ?? "all";
}

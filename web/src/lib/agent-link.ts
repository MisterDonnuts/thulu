export type AgentTab = "settings" | "skills" | "mcp" | "prompts";

const TABS: readonly AgentTab[] = ["settings", "skills", "mcp", "prompts"];

export function agentLink(agent?: string, tab?: AgentTab): string {
  const q = agent ? `?agent=${encodeURIComponent(agent)}` : "";
  return `/agents/${q}${tab ? `#${tab}` : ""}`;
}

export function parseAgentLink(search: string, hash: string): { agent?: string; tab?: AgentTab } {
  const raw = new URLSearchParams(search).get("agent")?.trim();
  const agent = raw === "" ? undefined : raw;
  const h = hash.replace(/^#/, "");
  const tab = (TABS as readonly string[]).includes(h) ? (h as AgentTab) : undefined;
  return { agent, tab };
}

export function deepLinkAgent(
  link: { agent?: string; tab?: AgentTab },
  listed: readonly string[],
): string | undefined {
  if (link.agent && listed.includes(link.agent)) return link.agent;
  return link.tab ? listed[0] : undefined;
}

import { tagHTML, chipColor, type TagOptions } from "./ui/tag";

export { CHIP_COLORS, chipColor } from "./ui/tag";

type LabelOptions = Omit<TagOptions, "color" | "tone" | "dot" | "icon" | "inline">;

export function labelTagHTML(label: string, opts: LabelOptions = {}): string {
  return tagHTML(label, { ...opts, color: chipColor(label) });
}

export interface RepoChipOptions {
  href?: string;
  removable?: TagOptions["removable"];
  size?: TagOptions["size"];
  cls?: string;
  attrs?: TagOptions["attrs"];
}

export function repoChipHTML(name: string, opts: RepoChipOptions = {}): string {
  return tagHTML(name, { ...opts, dot: chipColor(name), title: name });
}

export interface AgentChipOptions {
  inline?: boolean;
  size?: TagOptions["size"];
  title?: string;
  cls?: string;
  attrs?: TagOptions["attrs"];
  button?: boolean;
}

export function agentChipHTML(name: string, opts: AgentChipOptions = {}): string {
  return tagHTML(name, { ...opts, tone: "special", icon: "i-bot" });
}

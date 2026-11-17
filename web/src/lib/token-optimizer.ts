import { t } from "../i18n";
import { attrsHTML, esc } from "./ui";

export type OptimizerLevel = "off" | "suggest" | "enforce";

export const OPTIMIZER_LEVELS: OptimizerLevel[] = ["off", "suggest", "enforce"];

export interface AgentTokenOptimizer {
  level: OptimizerLevel;
  engine: string;
  model: string;
}

export type TokenOptimizerPayload =
  { level: "off" } | { level: Exclude<OptimizerLevel, "off">; engine: string; model: string };

export type WorkerReason = "" | "no_default_profile" | "unsupported_credential";

export interface OptimizerWorker {
  engine: string;
  available: boolean;
  profile_name?: string;
  reason?: string;
}

export const ENFORCE_ENGINES = ["claude-code"];

export function isOptimizerLevel(v: unknown): v is OptimizerLevel {
  return typeof v === "string" && (OPTIMIZER_LEVELS as string[]).includes(v);
}

// The editor detects dirty state by comparing JSON, so load and harvest must both go through here
// (same keys, same order).
// An unknown level (older "" that followed the workspace) reads as off.
export function normalizeAgentOptimizer(
  v: Partial<Record<keyof AgentTokenOptimizer, unknown>> | null | undefined,
): AgentTokenOptimizer {
  const level = isOptimizerLevel(v?.level) ? v.level : "off";
  if (level === "off") return { level, engine: "", model: "" };
  const text = (x: unknown) => (typeof x === "string" ? x.trim() : "");
  return { level, engine: text(v?.engine), model: text(v?.model) };
}

export function optimizerPayload(v: AgentTokenOptimizer): TokenOptimizerPayload {
  const n = normalizeAgentOptimizer(v);
  if (n.level === "off") return { level: "off" };
  return { level: n.level, engine: n.engine, model: n.model };
}

export function optimizerProblem(v: AgentTokenOptimizer): "" | "engine" | "model" {
  if (v.level === "off") return "";
  if (!v.engine.trim()) return "engine";
  if (!v.model.trim()) return "model";
  return "";
}

export function canEnforce(agentEngine: string): boolean {
  return ENFORCE_ENGINES.includes(agentEngine);
}

export function effectiveLevel(level: OptimizerLevel, agentEngine: string): OptimizerLevel {
  return level === "enforce" && !canEnforce(agentEngine) ? "suggest" : level;
}

export function parseWorkers(raw: unknown): OptimizerWorker[] {
  const list = Array.isArray(raw)
    ? raw
    : raw && typeof raw === "object" && Array.isArray((raw as { workers?: unknown }).workers)
      ? (raw as { workers: unknown[] }).workers
      : [];
  const out: OptimizerWorker[] = [];
  for (const w of list) {
    if (!w || typeof w !== "object") continue;
    const o = w as Record<string, unknown>;
    if (typeof o.engine !== "string" || !o.engine) continue;
    out.push({
      engine: o.engine,
      available: o.available === true,
      profile_name: typeof o.profile_name === "string" ? o.profile_name : undefined,
      reason: typeof o.reason === "string" ? o.reason : undefined,
    });
  }
  return out;
}

const LEVEL_KEYS: Record<OptimizerLevel, string> = {
  off: "lib.token_optimizer.levels.off",
  suggest: "lib.token_optimizer.levels.suggest",
  enforce: "lib.token_optimizer.levels.enforce",
};

export function levelLabel(level: OptimizerLevel): string {
  return t(LEVEL_KEYS[level]);
}

const REASON_KEYS: Record<Exclude<WorkerReason, "">, string> = {
  no_default_profile: "lib.token_optimizer.reasons.no_default_profile",
  unsupported_credential: "lib.token_optimizer.reasons.unsupported_credential",
};

export function workerReasonText(reason: string | undefined): string {
  return Object.hasOwn(REASON_KEYS, reason ?? "")
    ? t(REASON_KEYS[reason as Exclude<WorkerReason, "">])
    : t("lib.token_optimizer.reasons.unknown");
}

// A saved enforce stays selectable even where it can't apply, so the form shows what is stored.
export function modeOptionsHTML(selected: OptimizerLevel, agentEngine: string): string {
  return OPTIMIZER_LEVELS.map(
    (l) =>
      `<option${attrsHTML({
        value: l,
        selected: l === selected,
        disabled: l === "enforce" && l !== selected && !canEnforce(agentEngine),
      })}>${esc(levelLabel(l))}</option>`,
  ).join("");
}

export function modeNote(
  level: OptimizerLevel,
  agentEngine: string,
): { tone: "help" | "warn"; text: string } {
  if (canEnforce(agentEngine)) return { tone: "help", text: "" };
  if (level === "enforce")
    return { tone: "warn", text: t("lib.token_optimizer.mode.runs_as_suggest") };
  return { tone: "help", text: t("lib.token_optimizer.mode.enforce_unavailable") };
}

// A saved engine the list no longer names stays listed, so a save does not drop it unseen.
export function engineOptionsHTML(
  workers: OptimizerWorker[],
  selected: string,
  engineLabel: (engine: string) => string,
): string {
  const opt = (value: string, label: string, disabled = false, title?: string) =>
    `<option${attrsHTML({
      value,
      selected: value === selected,
      disabled: disabled && value !== selected,
      title,
    })}>${esc(label)}</option>`;
  const rows = workers.map((w) => {
    const name = engineLabel(w.engine);
    if (w.available) return opt(w.engine, name);
    const why = workerReasonText(w.reason);
    return opt(
      w.engine,
      t("lib.token_optimizer.engine_unavailable", { engine: name, reason: why }),
      true,
      why,
    );
  });
  if (selected && !workers.some((w) => w.engine === selected))
    rows.push(opt(selected, engineLabel(selected)));
  return [opt("", t("lib.token_optimizer.pick_engine")), ...rows].join("");
}

export function firstAvailableEngine(workers: OptimizerWorker[]): string {
  return workers.find((w) => w.available)?.engine ?? "";
}

export function engineWarning(workers: OptimizerWorker[], engine: string): string {
  const w = workers.find((x) => x.engine === engine);
  if (!engine || !w || w.available) return "";
  return workerReasonText(w.reason);
}

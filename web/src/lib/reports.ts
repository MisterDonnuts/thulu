import { t } from "../i18n";
import { plainReportTitle } from "./test-cases-drawer";

export interface ReportInput {
  source: string;
  kind: string;
  title?: string;
  body?: string;
}

export interface ReportView {
  title: string;
  source: string;
  body: string;
  score: string;
}

export function splitFrontmatter(body: string): {
  meta: Partial<Record<string, string>>;
  body: string;
} {
  const m = /^\s*---\r?\n([\s\S]*?)\r?\n---[ \t]*(?:\r?\n|$)/.exec(body);
  if (!m) return { meta: {}, body };
  const meta: Partial<Record<string, string>> = {};
  for (const line of m[1].split(/\r?\n/)) {
    const kv = /^([A-Za-z_][\w-]*)\s*:\s*(.*?)\s*(?:#.*)?$/.exec(line);
    if (kv) meta[kv[1]] = kv[2].replace(/^["']|["']$/g, "");
  }
  return { meta, body: body.slice(m[0].length).replace(/^\s+/, "") };
}

const SOURCE: Record<string, string> = {
  agent: "lib.reports.source.agent",
  auditor: "lib.reports.source.auditor",
  user: "lib.reports.source.user",
  system: "lib.reports.source.system",
};

const KIND: Record<string, string> = {
  agent_index: "lib.reports.kind.agent_index",
  evidence: "lib.reports.kind.evidence",
  auditor_feedback: "lib.reports.kind.auditor_feedback",
};

const VERDICT: Record<string, string> = {
  continue: "lib.reports.verdict.continue",
  block: "lib.reports.verdict.block",
  needs_more_turns: "lib.reports.verdict.needs_more_turns",
};

function verdictTitle(
  meta: Partial<Record<string, string>>,
  stageLabel: (id: string) => string,
): string {
  const v = meta.verdict ?? "";
  if (v === "kick_back")
    return meta.kick_back_to
      ? t("lib.reports.verdict.kick_back_to", { stage: stageLabel(meta.kick_back_to) })
      : t("lib.reports.verdict.kick_back");
  return VERDICT[v] ? t(VERDICT[v]) : "";
}

export function reportView(r: ReportInput, stageLabel: (id: string) => string): ReportView {
  const { meta, body } = splitFrontmatter(r.body ?? "");
  const own = plainReportTitle(r.title ?? "");
  const title =
    own ||
    (r.kind === "auditor_feedback" ? verdictTitle(meta, stageLabel) : "") ||
    (KIND[r.kind] ? t(KIND[r.kind]) : "") ||
    t("lib.reports.fallback_title");
  return {
    title,
    source: SOURCE[r.source] ? t(SOURCE[r.source]) : r.source,
    body,
    score: meta.score ?? "",
  };
}

const VIRTUAL_STAGE: Record<string, string> = {
  mention: "lib.reports.stage.mention",
  chat: "lib.reports.stage.chat",
};

export function reportStageLabel(id: string, label: (id: string) => string | undefined): string {
  if (VIRTUAL_STAGE[id]) return t(VIRTUAL_STAGE[id]);
  return label(id) ?? id;
}

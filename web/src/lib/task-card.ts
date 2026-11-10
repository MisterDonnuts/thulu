import { t } from "../i18n";
import { agentChipHTML, labelTagHTML, repoChipHTML } from "./chips";
import { prNumberOf } from "./pr-section";
import { safeUrl } from "./safe-url";
import type { NextAgent } from "./stage-agents";
import { subtaskProgressHTML, subtaskRowName, type SubtaskProgress } from "./subtasks";
import { reasonLine, runBadgeTone, runPillHTML, runWord, type RunWord } from "./task-detail";
import { unitShort } from "./time";
import { badgeHTML, dotHTML, esc, icoSvg, isHexColor, listRowHTML, tagHTML } from "./ui";

export function agoLabel(iso?: string): string {
  if (!iso) return "";
  const when = new Date(iso).getTime();
  if (!when || isNaN(when)) return "";
  const sec = Math.max(0, Math.round((Date.now() - when) / 1000));
  if (sec < 45) return t("lib.task_card.ago.just_now");
  if (sec < 60) return t("lib.task_card.ago.under", { span: unitShort(1, "minute") });
  const m = Math.round(sec / 60);
  if (m < 60) return unitShort(m, "minute");
  const h = Math.round(m / 60);
  if (h < 24) return unitShort(h, "hour");
  return unitShort(Math.round(h / 24), "day");
}

export function shortId(id: string): string {
  return id.slice(0, 6);
}

export function priorityIconHTML(priority: string | undefined): string {
  if (priority === "urgent")
    return `<span class="prio prio-urgent" role="img" title="${esc(t("lib.task_card.priority.urgent"))}" aria-label="${esc(t("lib.task_card.priority.urgent"))}">${icoSvg("i-alert")}</span>`;
  if (priority === "high")
    return `<span class="prio prio-high" role="img" title="${esc(t("lib.task_card.priority.high"))}" aria-label="${esc(t("lib.task_card.priority.high"))}">${icoSvg("i-arrow-up")}</span>`;
  return "";
}

export function partOfHTML(parentTitle: string): string {
  const text = t("lib.task_card.part_of", { title: parentTitle });
  return `${icoSvg("i-corner-up")}<span class="task-card-part-text" title="${esc(text)}">${esc(text)}</span>`;
}

export interface RunWordInput {
  status?: string;
  pending_approvals?: number;
  status_reason?: string;
  open_questions?: number;
}

export function askedWhileWorking(t_: RunWordInput): boolean {
  return t_.status !== "awaiting_input" && (Number(t_.open_questions) || 0) > 0;
}

export interface KidsAttention {
  question: number;
  failed: number;
  blocked: number;
}

export interface KidsWord {
  id: "question" | "failed" | "blocked";
  label: string;
  title: string;
  tone: "warning" | "danger";
  icon: string;
}

export function kidsWord(k: KidsAttention | undefined): KidsWord | null {
  if (!k) return null;
  if (k.question > 0)
    return {
      id: "question",
      label: t("lib.task_card.kids.question", { count: k.question }),
      title: t("lib.task_card.kids.question_title", { count: k.question }),
      tone: "warning",
      icon: "i-help",
    };
  if (k.failed > 0)
    return {
      id: "failed",
      label: t("lib.task_card.kids.failed", { count: k.failed }),
      title: t("lib.task_card.kids.failed", { count: k.failed }),
      tone: "danger",
      icon: "i-x-circle",
    };
  if (k.blocked > 0)
    return {
      id: "blocked",
      label: t("lib.task_card.kids.blocked", { count: k.blocked }),
      title: t("lib.task_card.kids.blocked", { count: k.blocked }),
      tone: "warning",
      icon: "i-lock",
    };
  return null;
}

export interface CardStateRow {
  word: RunWord | null;
  reason?: string;
  asked: boolean;
  agent: string;
  kids: KidsWord | null;
}

export function cardStateRow(
  t_: RunWordInput,
  opts: { terminal: boolean; agent?: string; kids?: KidsAttention },
): CardStateRow | null {
  let word: RunWord | null = null;
  if (!opts.terminal) {
    word = runWord(t_.status, false);
    if (!word && (Number(t_.pending_approvals) || 0) > 0)
      word = runWord("awaiting_approval", false);
  }
  const kids = kidsWord(opts.kids);
  const asked = !opts.terminal && askedWhileWorking(t_);
  const agent = opts.terminal ? "" : (opts.agent ?? "");
  if (!word && !kids && !asked && !agent) return null;
  const reason = word ? reasonLine(t_.status_reason) : "";
  return { word, reason, asked, agent, kids };
}

export function showsNextAgent(row: CardStateRow | null): boolean {
  return !row?.word;
}

export function cardStateHTML(row: CardStateRow | null): string {
  if (!row) return "";
  const out: string[] = [];
  const w = row.word;
  if (w) out.push(runPillHTML(w));
  if (row.asked)
    out.push(
      badgeHTML(t("lib.task_card.asked"), "warn", {
        shape: "pill",
        icon: "i-help",
        title: t("lib.task_card.asked_title"),
        attrs: { "data-run": "asked" },
      }),
    );
  if (row.agent) out.push(agentChipHTML(row.agent, { inline: true, title: row.agent }));
  const k = row.kids;
  if (k)
    out.push(
      badgeHTML(k.label, runBadgeTone(k.tone), {
        shape: "pill",
        icon: k.icon,
        title: k.title,
        attrs: { "data-kids": k.id },
      }),
    );
  if (w && row.reason)
    out.push(`<span class="task-card-reason" title="${esc(row.reason)}">${esc(row.reason)}</span>`);
  return out.join("");
}

export const CARD_TAGS = 2;

export interface CardMeta {
  repos: string[];
  tags: string[];
  blocked: string;
  blocks: number;
  next?: NextAgent | null;
}

export function cardMetaHTML(m: CardMeta): string {
  const repos = m.repos.map((name) => repoChipHTML(name, { size: "sm" })).join("");
  const shown = m.tags.slice(0, CARD_TAGS);
  const rest = m.tags.slice(CARD_TAGS);
  const tags =
    shown.map((tag) => labelTagHTML(tag, { size: "sm" })).join("") +
    (rest.length
      ? tagHTML(t("lib.task_card.more_tags", { count: rest.length }), {
          size: "sm",
          title: rest.join(", "),
        })
      : "");
  const blocked = m.blocked
    ? tagHTML(m.blocked, {
        size: "sm",
        tone: "warn",
        icon: "i-lock",
        title: m.blocked,
      })
    : "";
  const blocks =
    m.blocks > 0
      ? tagHTML(t("lib.task_card.blocks", { count: m.blocks }), {
          size: "sm",
          icon: "i-arrow-right",
        })
      : "";
  return repos + tags + blocked + blocks + nextAgentHTML(m.next ?? null);
}

export function nextAgentHTML(n: NextAgent | null): string {
  if (!n) return "";
  if (n.agent) {
    return tagHTML(t("lib.stage_agents.next_runs", { stage: n.label, agent: n.agent }), {
      size: "sm",
      icon: "i-bot",
      title: t("lib.stage_agents.next_runs_title", { stage: n.label, agent: n.agent }),
    });
  }
  return tagHTML(t("lib.stage_agents.next_none", { stage: n.label }), {
    size: "sm",
    tone: "warn",
    inline: true,
    icon: "i-alert",
    title: t("lib.stage_agents.next_none_title", { stage: n.label }),
  });
}

export function blockedLabel(count: number, blockerTitles: string[]): string {
  if (count <= 0) return "";
  if (count === 1 && blockerTitles.length > 0)
    return t("lib.task_card.blocked_by", { title: blockerTitles[0] });
  return t("lib.task_card.blocked_by_count", { count });
}

export interface CardPR {
  number: string;
  url: string;
}

export function cardPRs(prURLs: Record<string, string> | undefined): CardPR[] {
  return Object.keys(prURLs ?? {})
    .sort()
    .map((k) => ({ number: prNumberOf(prURLs?.[k] ?? ""), url: safeUrl(prURLs?.[k] ?? "") }))
    .filter((p) => p.url !== "");
}

export function cardProgressHTML(p: SubtaskProgress | null): string {
  if (!p || p.total === 0) return "";
  return subtaskProgressHTML(p, { text: "short", size: "xs" });
}

export function cardFootHTML(f: { id: string; prs: CardPR[]; age: string }): string {
  const prs = f.prs
    .map((p) => {
      const label = p.number
        ? t("lib.task_card.pr", { number: p.number })
        : t("lib.task_card.pr_plain");
      return `<a class="task-card-pr" href="${esc(p.url)}" target="_blank" rel="noopener noreferrer" title="${esc(label)}">${icoSvg("i-github")}<span>${esc(p.number ? `#${p.number}` : label)}</span></a>`;
    })
    .join("");
  const age = f.age ? `<span class="task-card-age">${esc(f.age)}</span>` : "";
  return `${prs}<span class="task-card-id">#${esc(shortId(f.id))}</span>${age}`;
}

export interface NestedRow {
  id: string;
  title: string;
  state: string;
  iconHTML: string;
  stateLabel: string;
  stageLabel: string;
  stageColor: string;
  match?: boolean;
}

export function nestedRowsHTML(rows: NestedRow[]): string {
  return rows
    .map((r) => {
      const dot = dotHTML("neutral", {
        attrs: { style: isHexColor(r.stageColor) ? `--dot-color:${r.stageColor}` : undefined },
      });
      const name = subtaskRowName(r.title, r.stateLabel, r.stageLabel);
      const row = listRowHTML({
        as: "button",
        size: "sm",
        wrap: true,
        cls: ["task-card-kid", `st-${r.state}`, r.match && "is-match"].filter(Boolean).join(" "),
        attrs: { "data-open-sub": r.id, "aria-label": name, title: name },
        leadHTML: r.iconHTML,
        title: r.title,
        asideHTML: dot,
      });
      return `<li>${row}</li>`;
    })
    .join("");
}

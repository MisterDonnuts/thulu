import { t } from "../i18n";
import { api } from "./api";
import { fmtDate } from "./time";
import {
  confirmDialog,
  esc,
  factsHTML,
  fillHistoryList,
  historyChangeHTML,
  historyHeadingHTML,
  historyListError,
  historyNoteHTML,
  historyPreviewHTML,
  openHistoryView,
} from "./ui";

export interface AgentDef {
  name: string;
  description?: string;
  valid_stages?: string[];
  engine?: string;
  model?: string;
  effort?: string;
  skills_by_stage?: Record<string, string[]>;
  mcp_by_stage?: Record<string, string[]>;
  auditor?: { stages?: string[] } | null;
}

export interface DefRevision {
  id: number;
  name: string;
  reason: string;
  actor?: string;
  created_at: string;
  definition: AgentDef;
}

export function defReason(reason: string): string {
  switch (reason) {
    case "workshop":
      return t("lib.agent_def_history.reason.workshop");
    case "restore":
      return t("lib.agent_def_history.reason.restore");
    default:
      return t("lib.agent_def_history.reason.settings");
  }
}

type DefField = [key: string, label: string, value: string];

function defFields(def: AgentDef, stageName: (id: string) => string): DefField[] {
  const byStage = (m: Record<string, string[]> | undefined) =>
    Object.keys(m ?? {})
      .sort()
      .filter((k) => (m?.[k] ?? []).length > 0)
      .map((k) => `${stageName(k)}: ${(m?.[k] ?? []).join(", ")}`)
      .join(" · ");
  return [
    ["description", t("lib.agent_def_history.field.description"), def.description ?? ""],
    [
      "stages",
      t("lib.agent_def_history.field.stages"),
      (def.valid_stages ?? []).map(stageName).join(", "),
    ],
    ["engine", t("lib.agent_def_history.field.engine"), def.engine ?? ""],
    ["model", t("lib.agent_def_history.field.model"), def.model ?? ""],
    ["effort", t("lib.agent_def_history.field.effort"), def.effort ?? ""],
    ["skills", t("lib.agent_def_history.field.skills"), byStage(def.skills_by_stage)],
    ["mcp", t("lib.agent_def_history.field.mcp"), byStage(def.mcp_by_stage)],
    [
      "auditor",
      t("lib.agent_def_history.field.auditor"),
      (def.auditor?.stages ?? []).map(stageName).join(", "),
    ],
  ];
}

export function defRows(
  def: AgentDef,
  stageName: (id: string) => string = (id) => id,
): Array<[string, string]> {
  return defFields(def, stageName)
    .filter(([, , value]) => value !== "")
    .map(([, label, value]) => [label, value]);
}

export interface DefChange {
  label: string;
  before: string;
  after: string;
}

export function defChanges(
  before: AgentDef,
  after: AgentDef,
  stageName: (id: string) => string = (id) => id,
): DefChange[] {
  const next = new Map(defFields(after, stageName).map(([key, , value]) => [key, value]));
  return defFields(before, stageName)
    .filter(([key, , value]) => next.get(key) !== value)
    .map(([key, label, value]) => ({ label, before: value, after: next.get(key) ?? "" }));
}

export interface DefHistoryOpts {
  host: HTMLElement;
  agent: string;
  current?: AgentDef;
  readOnly?: boolean;
  stageName?: (id: string) => string;
  onBack: () => void | Promise<void>;
  onRestored: () => void | Promise<void>;
  toast: (msg: string, kind?: "ok" | "err" | "info", ms?: number) => void;
}

export async function renderDefHistory(o: DefHistoryOpts): Promise<void> {
  const base = `/types/${encodeURIComponent(o.agent)}`;
  const { list, preview } = openHistoryView(o.host, {
    title: t("lib.agent_def_history.title", { agent: o.agent }),
    pick: t("lib.agent_def_history.pick"),
    backLabel: t("lib.revisions.back"),
    onBack: o.onBack,
  });
  let revs: DefRevision[];
  try {
    revs = await api.get<DefRevision[]>(`${base}/definition-revisions`);
  } catch (e) {
    historyListError(list, (e as Error).message);
    return;
  }
  fillHistoryList(
    list,
    revs.map((r) => ({
      action: defReason(r.reason),
      meta: `${r.actor ?? t("lib.revisions.unknown_actor")} · ${fmtDate(r.created_at)}`,
    })),
    {
      none: t("lib.agent_def_history.none"),
      onPick: (i) => showDef(o, preview, base, revs[i], i > 0 ? revs[i - 1].definition : o.current),
    },
  );
}

function changesHTML(changes: DefChange[]): string {
  if (!changes.length) return historyNoteHTML(t("lib.agent_def_history.no_changes"));
  const labels = {
    before: t("lib.agent_def_history.before"),
    after: t("lib.agent_def_history.after"),
    empty: t("lib.agent_def_history.empty_value"),
  };
  const rows = changes.map((c) => ({
    label: c.label,
    valueHTML: historyChangeHTML(c.before, c.after, labels),
  }));
  return factsHTML(rows, { cls: "def-changes" });
}

function showDef(
  o: DefHistoryOpts,
  pane: HTMLElement,
  base: string,
  rev: DefRevision,
  next?: AgentDef,
): void {
  const rows = defRows(rev.definition, o.stageName);
  const changes = next
    ? historyHeadingHTML(t("lib.agent_def_history.changes")) +
      changesHTML(defChanges(rev.definition, next, o.stageName)) +
      historyHeadingHTML(t("lib.agent_def_history.full"))
    : "";
  pane.innerHTML = historyPreviewHTML({
    when: t("lib.agent_def_history.kept", {
      reason: defReason(rev.reason),
      when: fmtDate(rev.created_at),
    }),
    body: {
      html:
        changes +
        (rows.length
          ? factsHTML(rows.map(([label, value]) => ({ label, value })))
          : esc(t("lib.agent_def_history.empty_def"))),
    },
    action: o.readOnly ? undefined : { label: t("lib.agent_def_history.restore") },
  });
  pane.querySelector<HTMLButtonElement>("[data-rev-restore]")?.addEventListener("click", () => {
    void (async () => {
      const ok = await confirmDialog({
        title: t("lib.agent_def_history.confirm_title", { agent: o.agent }),
        message: t("lib.agent_def_history.confirm_message"),
        confirmLabel: t("lib.agent_def_history.restore"),
      });
      if (!ok) return;
      try {
        await api.post(`${base}/definition-revisions/${rev.id}/restore`);
      } catch (e) {
        o.toast((e as Error).message, "err", 4500);
        return;
      }
      o.toast(t("lib.agent_def_history.restored", { agent: o.agent }), "ok", 1800);
      await o.onRestored();
    })();
  });
}

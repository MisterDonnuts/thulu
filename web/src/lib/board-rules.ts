// The daemon's board rules (internal/stage/validate.go and approvals.go) that a column edit can
// break, so the column editor locks the control instead of letting the save fail. The rule ids are
// the daemon's error keys; board-rules.fixtures.json holds boards both sides must judge alike (the
// Go test in internal/stage reads the same file), and lists the keys this file leaves to other
// checks.

import { t } from "../i18n";
import type { Stage } from "./stages";

export const MIRRORED_RULES = [
  "initial_and_terminal",
  "terminal_auto_advance",
  "flag_on_edge_stage",
  "skip_unless_combined",
  "changes_kick_back_unknown",
  "changes_kick_back_self",
  "changes_kick_back_terminal",
  "changes_kick_back_forward",
  "initial_count",
  "terminal_missing",
  "merge_target_multiple",
  "merge_target_without_pr",
  "required_checks_without_pr",
  "release_resources_worktree_after",
  "release_resources_kick_back",
  "skip_unless_kick_back_target",
] as const;
export type BoardRule = (typeof MIRRORED_RULES)[number];

export interface BoardProblem {
  rule: BoardRule;
  // The column the daemon names; "" for a rule about the whole board.
  stage: string;
  // The other column the rule involves: the earlier merge column, the column that freed the
  // worktree, the kick-back target. The flag for flag_on_edge_stage and skip_unless_combined.
  other?: string;
}

type Flag = keyof Stage;

// validateEdgeFlags: no agent runs in the start or the finishing column.
const EDGE_FLAGS: readonly Flag[] = [
  "agent_verdict",
  "lock_tests",
  "release_resources",
  "pr_draft",
];
const START_ONLY: readonly Flag[] = ["pr_ready", "merge_target"];
// validateSkipUnless: a column that may be skipped never opens a gate nor publishes a branch.
const SKIP_COMBINED: readonly Flag[] = [
  "open_pr",
  "pr_draft",
  "pr_ready",
  "push_branch",
  "lock_tests",
  "release_resources",
  "merge_target",
  "no_skip",
];
// validateFlow: what needs the worktree after a column freed it.
const NEEDS_WORKTREE: readonly Flag[] = ["push_branch", "open_pr", "pr_draft", "lock_tests"];

const on = (st: Stage, flag: Flag): boolean => {
  const v = st[flag];
  return Array.isArray(v) ? v.length > 0 : Boolean(v);
};

function stageProblems(st: Stage, byId: Map<string, Stage>): BoardProblem[] {
  const out: BoardProblem[] = [];
  const add = (rule: BoardRule, other?: string) => out.push({ rule, stage: st.id, other });
  if (st.initial && st.terminal) add("initial_and_terminal");
  if (st.terminal && st.auto_advance) add("terminal_auto_advance");
  if (st.initial || st.terminal) {
    const edge = [...EDGE_FLAGS, "skip_unless" as Flag, ...(st.initial ? START_ONLY : [])];
    for (const f of edge) if (on(st, f)) add("flag_on_edge_stage", f);
  }
  if (st.skip_unless?.length) {
    if (st.required_approvals?.length) add("skip_unless_combined", "required_approvals");
    for (const f of SKIP_COMBINED) if (on(st, f)) add("skip_unless_combined", f);
  }
  const to = st.changes_kick_back_to ?? "";
  if (to) {
    const target = byId.get(to);
    if (!target) add("changes_kick_back_unknown", to);
    else if (target.id === st.id) add("changes_kick_back_self");
    else if (target.terminal) add("changes_kick_back_terminal", to);
    else if (target.position >= st.position) add("changes_kick_back_forward", to);
  }
  return out;
}

// validateFlow, in board order.
function flowProblems(ordered: readonly Stage[], byId: Map<string, Stage>): BoardProblem[] {
  const out: BoardProblem[] = [];
  let opensPR = false;
  let released = "";
  let mergeTarget = "";
  for (const st of ordered) {
    const add = (rule: BoardRule, other?: string) => out.push({ rule, stage: st.id, other });
    if (st.merge_target) {
      if (mergeTarget) add("merge_target_multiple", mergeTarget);
      else mergeTarget = st.id;
      if (!opensPR) add("merge_target_without_pr");
    }
    if (st.open_pr || st.pr_draft) opensPR = true;
    if (!opensPR && st.required_approvals?.some((a) => a.required_checks)) {
      add("required_checks_without_pr");
    }
    if (released && !st.terminal && NEEDS_WORKTREE.some((f) => on(st, f))) {
      add("release_resources_worktree_after", released);
    }
    if (st.release_resources) {
      if (st.changes_kick_back_to || st.on_changes_requested === "kick_back") {
        add("release_resources_kick_back");
      }
      if (!released) released = st.id;
    }
    const target = byId.get(st.changes_kick_back_to ?? "");
    if (target?.skip_unless?.length) add("skip_unless_kick_back_target", target.id);
  }
  return out;
}

// Every rule the board breaks. The daemon stops at the first; this lists them all.
export function boardProblems(stages: readonly Stage[]): BoardProblem[] {
  const byId = new Map(stages.map((s) => [s.id, s]));
  const out = stages.flatMap((s) => stageProblems(s, byId));
  if (stages.filter((s) => s.initial).length !== 1) out.push({ rule: "initial_count", stage: "" });
  if (!stages.some((s) => s.terminal)) out.push({ rule: "terminal_missing", stage: "" });
  const ordered = [...stages].sort((a, b) => a.position - b.position);
  return [...out, ...flowProblems(ordered, byId)];
}

const problemKey = (p: BoardProblem) => `${p.rule}/${p.stage}/${p.other ?? ""}`;

// The problems a change adds to the board, most telling first; [] when the daemon would take it.
export function newProblems(before: readonly Stage[], after: readonly Stage[]): BoardProblem[] {
  const had = new Set(boardProblems(before).map(problemKey));
  return boardProblems(after)
    .filter((p) => !had.has(problemKey(p)))
    .sort((a, b) => MIRRORED_RULES.indexOf(a.rule) - MIRRORED_RULES.indexOf(b.rule));
}

// Why a change to column `editing` is locked, in the editor's words.
export function problemText(p: BoardProblem, stages: readonly Stage[], editing: string): string {
  const label = (id: string | undefined) => stages.find((s) => s.id === id)?.label ?? id ?? "";
  const otherColumn = p.stage === editing ? p.other : p.stage;
  switch (p.rule) {
    case "initial_and_terminal":
      return stages.find((s) => s.id === editing)?.initial
        ? t("lib.board_editor.locks.terminal_initial")
        : t("lib.board_editor.locks.initial_terminal");
    case "terminal_auto_advance":
      return t("lib.board_editor.locks.terminal_no_advance");
    case "initial_count":
      return t("lib.board_editor.locks.initial_move");
    case "terminal_missing":
      return t("lib.board_editor.locks.last_terminal");
    case "changes_kick_back_unknown":
    case "changes_kick_back_self":
    case "changes_kick_back_terminal":
    case "changes_kick_back_forward":
      return t("lib.board_editor.locks.kick_back_earlier");
    case "flag_on_edge_stage":
      return START_ONLY.includes(p.other as Flag) && stages.find((s) => s.id === p.stage)?.initial
        ? t("lib.board_editor.locks.start_no_pr")
        : t("lib.board_editor.locks.edge_no_agent");
    case "skip_unless_combined":
      return t("lib.board_editor.locks.skip_unless");
    case "merge_target_multiple":
      return t("lib.board_editor.locks.merge_target_taken", { stage: label(otherColumn) });
    case "merge_target_without_pr":
      return p.stage === editing
        ? t("lib.board_editor.locks.merge_target_without_pr")
        : t("lib.board_editor.locks.pr_needed_by_merge", { stage: label(p.stage) });
    case "required_checks_without_pr":
      return p.stage === editing
        ? t("lib.board_editor.locks.required_checks_without_pr")
        : t("lib.board_editor.locks.pr_needed_by_checks", { stage: label(p.stage) });
    case "release_resources_worktree_after":
      return p.stage === editing
        ? t("lib.board_editor.locks.worktree_after_release", { stage: label(p.other) })
        : t("lib.board_editor.locks.release_before_worktree", { stage: label(p.stage) });
    case "release_resources_kick_back":
      return t("lib.board_editor.locks.release_kick_back");
    case "skip_unless_kick_back_target":
      return p.stage === editing
        ? t("lib.board_editor.locks.kick_back_to_skippable", { stage: label(p.other) })
        : t("lib.board_editor.locks.skippable_is_kick_back_target", { stage: label(p.stage) });
  }
}

// Why patching column `id` would break the board; "" when the daemon would take it.
export function patchLock(stages: readonly Stage[], id: string, after: readonly Stage[]): string {
  const found = newProblems(stages, after);
  return found.length ? problemText(found[0], after, id) : "";
}

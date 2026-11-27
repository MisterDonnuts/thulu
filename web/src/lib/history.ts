import { t } from "../i18n";
import { localized } from "./activity";
import { personName } from "./person";
import type { AvatarKind } from "./ui/avatar";

export interface HistoryCtx {
  stageLabel(id: string): string;
}

const str = (v: unknown): string => (typeof v === "string" ? v : "");

export function historyActor(actor: string | undefined): string {
  const a = (actor ?? "").trim();
  if (a.startsWith("agent:")) return a.slice("agent:".length) || t("lib.history.actor.agent");
  if (a === "" || a === "octopus" || a === "system") return t("lib.history.actor.octopus");
  if (a === "auditor") return t("lib.history.actor.auditor");
  return personName(a);
}

export function historyActorKind(actor: string | undefined): AvatarKind {
  const a = (actor ?? "").trim();
  if (a.startsWith("agent:") || a === "auditor") return "agent";
  if (a === "" || a === "octopus" || a === "system") return "system";
  return "person";
}

const VIA: Record<string, string> = {
  kick_back: "lib.history.via.kick_back",
  changes_requested: "lib.history.via.changes_requested",
  stage_deleted: "lib.history.via.stage_deleted",
  skip_removed: "lib.history.via.skip_removed",
};

const VERDICT: Record<string, string> = {
  block: "lib.history.verdict.block",
  needs_more_turns: "lib.history.verdict.needs_more_turns",
  continue: "lib.history.verdict.continue",
};

const DELIVERY: Record<string, string> = {
  steered: "lib.history.delivery.steered",
  queued: "lib.history.delivery.queued",
  continuation: "lib.history.delivery.continuation",
  unreachable: "lib.history.delivery.unreachable",
  mention: "lib.history.delivery.mention",
  chat: "lib.history.delivery.chat",
  thread: "lib.history.delivery.thread",
};

function credentialBlockedText(faults: unknown): string {
  if (!Array.isArray(faults) || faults.length === 0) return "";
  if (faults.length > 1) return t("lib.history.credentials_unusable", { count: faults.length });
  const f = (faults[0] ?? {}) as Record<string, unknown>;
  const name = str(f.name);
  switch (str(f.state)) {
    case "expired":
      return t("lib.history.credential.expired", { name });
    case "invalid":
      return t("lib.history.credential.rejected", { name });
    default:
      return t("lib.history.credential.unusable", { name });
  }
}

// A stage_skip_removed event in words, by its cause (internal/task/stage_skips.go SkipCause*). The
// daemon writes the reason of a system cause in its own language, so those lines come from the
// cause alone.
function skipRemovedText(p: Record<string, unknown>, stage: string): string {
  const who = historyActor(str(p.by));
  const reason = str(p.reason);
  switch (str(p.cause)) {
    case "entered":
      return t("lib.history.skip.entered", { stage, who });
    case "ui_diff":
      return t("lib.history.skip.ui_diff", { stage });
    case "diff_unreadable":
      return t("lib.history.skip.diff_unreadable", { stage });
    case "refinement_dropped":
      return t("lib.history.skip.refinement_dropped", { stage });
    case "board_changed":
      return t("lib.history.skip.board_changed", { stage });
  }
  if (p.deferred === true) return t("lib.history.skip.removed_later", { who, stage, reason });
  if (p.passed === true) return t("lib.history.skip.removed_back", { who, stage, reason });
  return t("lib.history.skip.removed", { who, stage, reason });
}

// The checks the daemon ran before it skipped an audit (internal/task/audit_signals.go); an id this
// build does not know shows as itself.
const AUDIT_CHECKS: Record<string, string> = {
  exit: "lib.history.audit_checks.exit",
  out_of_turns: "lib.history.audit_checks.out_of_turns",
  verdict: "lib.history.audit_checks.verdict",
  locked_tests: "lib.history.audit_checks.locked_tests",
  scope: "lib.history.audit_checks.scope",
  diff_size: "lib.history.audit_checks.diff_size",
  skips: "lib.history.audit_checks.skips",
  flags: "lib.history.audit_checks.flags",
  no_output: "lib.history.audit_checks.no_output",
  person_relaunch: "lib.history.audit_checks.person_relaunch",
  risk_high: "lib.history.audit_checks.risk_high",
};

export function auditChecksText(checks: unknown): string {
  if (!Array.isArray(checks)) return "";
  return checks
    .map((c) => {
      const id = str((c as { check?: unknown } | null)?.check);
      if (!id) return "";
      const label = AUDIT_CHECKS[id] ? t(AUDIT_CHECKS[id]) : id;
      return str((c as { state?: unknown }).state) === "unread"
        ? t("lib.history.audit_check_unread", { check: label })
        : label;
    })
    .filter(Boolean)
    .join(", ");
}

export function historyText(
  e: { kind: string; payload?: Record<string, unknown> | null },
  ctx: HistoryCtx,
): string {
  const p = e.payload ?? {};
  const stage = (id: unknown) => ctx.stageLabel(str(id));
  switch (e.kind) {
    case "stage_entered": {
      if (!str(p.from) || !str(p.to)) return "";
      const moved = t("lib.history.moved", { from: stage(p.from), to: stage(p.to) });
      const via = VIA[str(p.via) || str(p.reason)];
      return via ? `${moved} ${t(via)}` : moved;
    }
    case "agent_run_started":
    case "auditor_run_started":
    case "mention_started":
      if (!str(p.agent)) return "";
      return str(p.stage)
        ? t("lib.history.run_in", { agent: str(p.agent), stage: stage(p.stage) })
        : str(p.agent);
    case "agent_run_finished":
    case "auditor_run_finished": {
      if (!str(p.stage)) return "";
      const where = stage(p.stage);
      if ("stopped_by" in p)
        return str(p.stopped_by)
          ? t("lib.history.finished.stopped_by", {
              stage: where,
              who: personName(str(p.stopped_by)),
            })
          : t("lib.history.finished.stopped", { stage: where });
      if (p.outcome === "max_turns")
        return t("lib.history.finished.out_of_turns", { stage: where });
      const exit = typeof p.exit === "number" ? p.exit : null;
      if (exit !== null && exit !== 0)
        return t("lib.history.finished.failed", { stage: where, code: exit });
      return t("lib.history.finished.done", { stage: where });
    }
    case "auditor_verdict": {
      const v = str(p.verdict);
      if (v === "kick_back")
        return str(p.kick_back_to)
          ? t("lib.history.verdict.kick_back_to", { stage: stage(p.kick_back_to) })
          : t("lib.history.verdict.kick_back");
      return VERDICT[v] ? t(VERDICT[v]) : "";
    }
    case "auditor_skipped":
      if (!str(p.agent) || !str(p.stage)) return "";
      return t("lib.history.auditor_skipped", {
        agent: str(p.agent),
        stage: stage(p.stage),
        checks: auditChecksText(p.checks),
      });
    case "task_created":
      return str(p.cloned_from)
        ? t("lib.history.cloned_from", { task: str(p.cloned_from).slice(0, 8) })
        : "";
    case "run_queued":
    case "run_not_started":
      return localized(p);
    case "run_outlived":
      if (!str(p.agent) || !str(p.stage) || !str(p.task_stage)) return "";
      return t("lib.history.outlived", {
        agent: str(p.agent),
        stage: stage(p.stage),
        now: stage(p.task_stage),
      });
    case "stage_launch_deferred":
      if (!str(p.agent) || !str(p.stage)) return "";
      return t("lib.history.deferred", {
        agent: str(p.agent),
        stage: stage(p.stage),
        busy: stage(p.busy_stage),
      });
    case "credential_blocked":
      return credentialBlockedText(p.faults);
    case "stage_skip_added":
      if (!str(p.stage)) return "";
      return t("lib.history.skip.added", {
        who: historyActor(str(p.by)),
        stage: stage(p.stage),
        reason: str(p.reason),
      });
    case "stage_skip_removed":
      return str(p.stage) ? skipRemovedText(p, stage(p.stage)) : "";
    case "stage_skipped":
      if (!str(p.stage)) return "";
      if (p.task_skip === true)
        return t("lib.history.skip.passed_over", { stage: stage(p.stage), reason: str(p.reason) });
      return Array.isArray(p.required) && p.required.length
        ? t("lib.history.skip.passed_over_surface", {
            stage: stage(p.stage),
            required: p.required.map(String).join(", "),
          })
        : "";
    case "conversation_message": {
      const key = DELIVERY[str(p.delivery)];
      if (!key || !("line" in p)) return "";
      return t(key, { line: str(p.line) });
    }
    default:
      return "";
  }
}

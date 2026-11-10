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
    case "task_created":
      return str(p.cloned_from)
        ? t("lib.history.cloned_from", { task: str(p.cloned_from).slice(0, 8) })
        : "";
    case "run_queued":
    case "run_not_started":
      return localized(p);
    case "credential_blocked":
      return credentialBlockedText(p.faults);
    case "conversation_message": {
      const key = DELIVERY[str(p.delivery)];
      if (!key || !("line" in p)) return "";
      return t(key, { line: str(p.line) });
    }
    default:
      return "";
  }
}

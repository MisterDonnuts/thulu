// Older daemons send `flags` (the /tasks/{id}/flags alias) instead of `approvals`; read both until
// the alias is removed.

import { t } from "../i18n";
import { historyActor, historyActorKind } from "./history";
import { personName } from "./person";
import { fmtDate } from "./time";
import {
  authorLineHTML,
  badgeHTML,
  buttonHTML,
  emptyStateHTML,
  esc,
  fieldHTML,
  menuHTML,
  type Attrs,
  type AuthorLine,
  type BadgeTone,
  type ButtonVariant,
  type MenuEntry,
} from "./ui";

export interface Approval {
  id: string;
  task_id?: string;
  key?: string;
  label: string;
  stage?: string;
  source?: string;
  status: string;
  reason?: string;
  created_by?: string;
  decided_by?: string;
  decided_note?: string;
  created_at?: string;
  decided_at?: string;
}

export interface LegacyFlag {
  id: string;
  kind?: string;
  label: string;
  status: string;
}

export interface WithApprovals {
  approvals?: Approval[] | null;
  flags?: LegacyFlag[] | null;
  pending_approvals?: number;
}

export type Decision = "approved" | "changes_requested" | "waived";

export function approvalsOf(task: WithApprovals): Approval[] {
  if (Array.isArray(task.approvals)) return task.approvals;
  return (task.flags ?? []).map((f) => ({
    id: f.id,
    key: f.kind,
    label: f.label,
    source: "agent",
    status: f.status === "rejected" ? "changes_requested" : f.status,
  }));
}

export function isSettled(a: Approval): boolean {
  return a.status === "approved" || a.status === "waived";
}

export function pendingApprovalCount(task: WithApprovals): number {
  if (typeof task.pending_approvals === "number") return task.pending_approvals;
  return approvalsOf(task).filter((a) => !isSettled(a)).length;
}

export function decisionNeedsNote(d: Decision): boolean {
  return d !== "approved";
}

export function decisionBody(
  d: Decision,
  note: string,
): { decision: Decision; note?: string } | null {
  const n = note.trim();
  if (decisionNeedsNote(d) && !n) return null;
  return n ? { decision: d, note: n } : { decision: d };
}

const STATUS_KEY: Record<string, string> = {
  pending: "lib.approvals.status.pending",
  approved: "lib.approvals.status.approved",
  changes_requested: "lib.approvals.status.changes_requested",
  waived: "lib.approvals.status.waived",
};

const SOURCE_KEY: Record<string, string> = {
  stage: "lib.approvals.source.stage",
  agent: "lib.approvals.source.agent",
  system: "lib.approvals.source.system",
  person: "lib.approvals.source.person",
};

export function approvalStatusLabel(status: string): string {
  return status in STATUS_KEY ? t(STATUS_KEY[status]) : status;
}

const STATUS_TONE: Record<string, BadgeTone> = {
  pending: "warn",
  approved: "ok",
  changes_requested: "danger",
  waived: "muted",
};

export function approvalStatusBadgeHTML(status: string): string {
  return badgeHTML(approvalStatusLabel(status), STATUS_TONE[status] ?? "neutral", {
    size: "sm",
    attrs: { "data-status": status },
  });
}

export function approvalSourceLabel(source: string): string {
  return source in SOURCE_KEY ? t(SOURCE_KEY[source]) : source;
}

export interface NoteForm {
  id: string;
  decision: Decision;
}

export interface PaneView {
  askOpen?: boolean;
  menuFor?: string | null;
  stageLabel?: (id: string) => string;
  effectOf?: (a: Approval) => string;
  readOnly?: boolean;
}

export interface ChangesStage {
  id: string;
  label: string;
  position: number;
  initial?: boolean;
  terminal?: boolean;
  virtual?: boolean;
  on_changes_requested?: string;
  changes_kick_back_to?: string;
}

// Mirrors the daemon's task.changesKickBackTarget and stage.KickbackDefault.
export function kickBackTarget(stages: ChangesStage[], from: ChangesStage): ChangesStage | null {
  const board = stages.filter((s) => !s.virtual).sort((a, b) => a.position - b.position);
  const behind = (id: string | undefined) => {
    const to = board.find((s) => s.id === id);
    return to && to.position < from.position ? to : null;
  };
  const fallback =
    board.find((s) => !s.initial && !s.terminal) ?? board.find((s) => s.initial) ?? board.at(0);
  return behind(from.changes_kick_back_to) ?? behind(fallback?.id) ?? null;
}

// Mirrors the daemon's task.changesRequested, including deferring resume/kick_back while a run is
// active.
export function changesEffect(
  a: Pick<Approval, "stage">,
  taskStage: string,
  stages: ChangesStage[],
  agent: string,
  running = false,
): string {
  const st = stages.find((s) => s.id === taskStage);
  if (!a.stage || a.stage !== taskStage || !st) return t("lib.approvals.effect.record");
  const mode = st.on_changes_requested ?? "resume";
  if (mode === "kick_back") {
    const to = kickBackTarget(stages, st);
    if (to)
      return running
        ? t("lib.approvals.effect.kick_back_deferred", { stage: to.label })
        : t("lib.approvals.effect.kick_back", { stage: to.label });
  }
  if (mode === "stay" || mode === "kick_back")
    return t("lib.approvals.effect.stay", { stage: st.label });
  if (running)
    return agent
      ? t("lib.approvals.effect.resume_deferred", { agent })
      : t("lib.approvals.effect.resume_unnamed_deferred");
  return agent
    ? t("lib.approvals.effect.resume", { agent })
    : t("lib.approvals.effect.resume_unnamed");
}

export function approvalsSig(
  list: Approval[],
  noting: NoteForm | null,
  view: PaneView = {},
): string {
  return (
    list
      .map(
        (a) =>
          `${a.id}:${a.status}:${a.label}:${a.decided_at ?? ""}:${a.decided_note ?? ""}:${view.effectOf?.(a) ?? ""}`,
      )
      .join("|") +
    `#${noting ? `${noting.id}:${noting.decision}` : ""}#${view.askOpen ? 1 : 0}#${view.menuFor ?? ""}#${view.readOnly ? 1 : 0}`
  );
}

export function sortApprovals(list: Approval[]): Approval[] {
  return [
    ...list.filter((a) => a.status === "pending"),
    ...list.filter((a) => a.status !== "pending"),
  ];
}

function decidedLine(a: Approval): string {
  if (a.status === "pending" || !a.decided_by) return "";
  const when = a.decided_at ? fmtDate(a.decided_at) : "";
  const key =
    a.status === "approved"
      ? "lib.approvals.decided.approved"
      : a.status === "waived"
        ? "lib.approvals.decided.waived"
        : "lib.approvals.decided.changes_requested";
  return t(key, { who: personName(a.decided_by), when });
}

function apButton(o: {
  label: string;
  variant?: ButtonVariant;
  icon?: string;
  id?: string;
  attrs?: Attrs;
}): string {
  return buttonHTML({ ...o, size: "sm" });
}

export function approvalsPaneHTML(
  list: Approval[],
  noting: NoteForm | null,
  view: PaneView = {},
): string {
  const rows =
    list.length === 0
      ? emptyStateHTML({ size: "sm", body: t("lib.approvals.empty") })
      : sortApprovals(list)
          .map((a) => approvalHTML(a, noting?.id === a.id ? noting : null, view))
          .join("");
  if (view.readOnly) return `<div class="ap-list">${rows}</div>`;
  const open = !!view.askOpen;
  return `<div class="ap-list">${rows}</div>
    <div class="ap-ask">
      ${apButton({
        label: t("lib.approvals.add_title"),
        icon: "plus",
        id: "ap-ask-open",
        attrs: { "aria-expanded": String(open), "aria-controls": "ap-form", hidden: open },
      })}
      <div class="ap-form" id="ap-form"${open ? "" : " hidden"}>
        <h3 class="ap-form-title">${esc(t("lib.approvals.add_title"))}</h3>
        ${fieldHTML({
          label: t("lib.approvals.label_label"),
          for: "ap-label",
          control: `<input id="ap-label" placeholder="${esc(t("lib.approvals.label_placeholder"))}" />`,
        })}
        ${fieldHTML({
          label: t("lib.approvals.reason_label"),
          for: "ap-reason",
          control: `<input id="ap-reason" placeholder="${esc(t("lib.approvals.reason_placeholder"))}" />`,
        })}
        <div class="ap-form-foot">
          ${apButton({ label: t("common.buttons.cancel"), variant: "ghost", id: "ap-ask-cancel" })}
          ${apButton({ label: t("lib.approvals.add"), id: "ap-add" })}
        </div>
      </div>
    </div>`;
}

function sourceText(a: Approval, stageLabel?: (id: string) => string): string {
  const source = a.source ?? "agent";
  if (source === "stage" && a.stage)
    return t("lib.approvals.source.stage_named", {
      stage: stageLabel ? stageLabel(a.stage) : a.stage,
    });
  return approvalSourceLabel(source);
}

export function approvalAuthor(a: Approval, stageLabel?: (id: string) => string): AuthorLine {
  if (!a.created_by) return { name: sourceText(a, stageLabel), kind: "system" };
  const stage =
    a.source === "stage" && a.stage ? (stageLabel ? stageLabel(a.stage) : a.stage) : undefined;
  return { name: historyActor(a.created_by), kind: historyActorKind(a.created_by), stage };
}

function approvalHTML(a: Approval, noting: NoteForm | null, view: PaneView): string {
  const id = esc(a.id);
  const pending = a.status === "pending";
  const menuOpen = view.menuFor === a.id;
  // A stage's required approval is recreated whenever a task enters the stage, so deleting it would
  // not stick.
  const deletable = a.source !== "stage";
  const decide = (d: Decision) =>
    d === "approved"
      ? t("lib.approvals.approve")
      : d === "changes_requested"
        ? t("lib.approvals.request_changes")
        : t("lib.approvals.waive");
  const item = (action: string, title: string, icon: string, danger = false): MenuEntry => ({
    title,
    icon,
    danger,
    attrs: { "data-ap": action, "data-id": a.id },
  });
  const others = (["approved", "changes_requested", "waived"] as Decision[]).filter(
    (d) => d !== a.status,
  );
  const entries: MenuEntry[] = pending
    ? []
    : others.map((d) =>
        item(d, decide(d), d === "approved" ? "i-check" : d === "waived" ? "i-minus" : "i-edit"),
      );
  if (deletable) {
    if (entries.length) entries.push({ sep: true });
    entries.push(item("delete", t("common.buttons.delete"), "i-trash", true));
  }
  const menu = menuOpen
    ? menuHTML(entries, {
        label: t("lib.approvals.more_actions", { label: a.label }),
        open: true,
        cls: "ap-pop",
      })
    : "";
  const menuBtn =
    view.readOnly || (pending && !deletable)
      ? ""
      : buttonHTML({
          iconOnly: true,
          icon: "more",
          label: t("lib.approvals.more_actions", { label: a.label }),
          size: "sm",
          variant: "ghost",
          cls: "ap-menu-btn",
          attrs: {
            "data-ap-menu": a.id,
            "aria-haspopup": "menu",
            "aria-expanded": String(menuOpen),
          },
        });
  const effect = view.effectOf?.(a) ?? "";
  const effectLine = effect ? `<p class="ap-effect">${esc(effect)}</p>` : "";
  const decided = decidedLine(a);
  let body = "";
  if (view.readOnly) {
    // A reader sees the decision, not the controls that make one.
  } else if (pending) {
    const note = fieldHTML({
      label: t("lib.approvals.note_label"),
      for: `ap-note-${a.id}`,
      help: t("lib.approvals.note_hint"),
      errorId: `ap-note-${a.id}-error`,
      cls: "ap-note",
      control: `<textarea id="ap-note-${id}" data-ap-note-for="${id}" rows="2" aria-describedby="ap-note-${id}-help" placeholder="${esc(t("lib.approvals.note_placeholder"))}"></textarea>`,
    });
    body = `${note}
      <div class="approval-actions">
        ${apButton({ label: t("lib.approvals.approve"), variant: "primary", attrs: { "data-ap": "approved", "data-id": a.id } })}
        ${apButton({ label: t("lib.approvals.request_changes"), attrs: { "data-ap": "changes_requested", "data-id": a.id } })}
        ${apButton({ label: t("lib.approvals.waive"), variant: "ghost", attrs: { "data-ap": "waived", "data-id": a.id } })}
      </div>
      ${effectLine}`;
  } else if (noting) {
    body = `<div class="approval-note-form">
        <textarea data-ap-note rows="2" aria-label="${esc(t("lib.approvals.note_label"))}" placeholder="${esc(
          noting.decision === "waived"
            ? t("lib.approvals.waive_placeholder")
            : t("lib.approvals.changes_placeholder"),
        )}"></textarea>
        <div class="approval-actions">
          ${apButton({
            label:
              noting.decision === "waived"
                ? t("lib.approvals.confirm_waive")
                : t("lib.approvals.confirm_changes"),
            attrs: { "data-ap": "confirm", "data-id": a.id },
          })}
          ${apButton({ label: t("common.buttons.cancel"), variant: "ghost", attrs: { "data-ap": "dismiss", "data-id": a.id } })}
        </div>
        ${noting.decision === "changes_requested" ? effectLine : ""}
      </div>`;
  }
  const keyTitle = a.key ? ` title="${esc(t("lib.approvals.key_title", { key: a.key }))}"` : "";
  return `<div class="approval${pending ? " approval-pending" : ""}" data-approval="${id}">
      <div class="approval-head">
        <strong class="approval-label"${keyTitle}>${esc(a.label)}</strong>
        ${approvalStatusBadgeHTML(a.status)}
        ${authorLineHTML(approvalAuthor(a, view.stageLabel))}
        ${menuBtn ? `<span class="ap-menu-wrap">${menuBtn}${menu}</span>` : ""}
      </div>
      ${a.reason ? `<div class="approval-reason">${esc(a.reason)}</div>` : ""}
      ${decided ? `<div class="approval-decided">${esc(decided)}</div>` : ""}
      ${a.decided_note ? `<div class="approval-note">${esc(a.decided_note)}</div>` : ""}
      ${body}
    </div>`;
}

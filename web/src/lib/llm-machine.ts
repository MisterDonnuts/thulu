import { t } from "../i18n";
import type { KeyBadge, LLMProfile } from "./llm-profiles";
import { runnerLabel, runnerStatus, type EngineState, type Runner } from "./runners";
import { esc } from "./ui";

// A machine profile runs its agents on a registered machine, paid by the login of the engine's CLI
// there. These helpers back that profile kind in Settings → AI engines.

export const MACHINE_KIND = "machine";

export interface CredentialKind {
  id: string;
  label: string;
  prefix: string;
  federated?: boolean;
  machine?: boolean;
}

// What the credential select of the profile modal offers. A profile never changes kind, so editing
// one offers only its own side. runners is null when this daemon runs nothing on machines.
export function credentialKinds(
  keyKinds: CredentialKind[],
  machineLogin: boolean | undefined,
  editing: LLMProfile | null,
  runners: Runner[] | null,
): CredentialKind[] {
  const machine = { id: MACHINE_KIND, label: t("lib.llm_machine.kind"), prefix: "", machine: true };
  if (editing) return editing.runner_id ? [machine] : keyKinds;
  return machineLogin && runners ? [...keyKinds, machine] : keyKinds;
}

export function engineStateText(st: EngineState, engineLabel: string): string {
  if (!st.installed) return t("lib.llm_machine.state.not_installed", { engine: engineLabel });
  if (!st.logged_in) return t("lib.llm_machine.state.not_logged_in", { engine: engineLabel });
  const who = [st.email, st.org].filter(Boolean).join(" · ");
  return who
    ? t("lib.llm_machine.state.logged_in_as", { who })
    : t("lib.llm_machine.state.logged_in");
}

// The machine only reports engine state to the daemon; the page sees it through the profiles
// already on that machine.
function engineStateOn(runnerID: string, engine: string, profiles: LLMProfile[]) {
  return profiles.find((p) => p.runner_id === runnerID && p.engine === engine && p.engine_state)
    ?.engine_state;
}

export interface MachineChoice {
  id: string;
  label: string;
}

// The machines a profile can run on, each with its connection and, when known, its login. A
// profile keeps its current machine in the list even when it was revoked, so the select shows it.
export function machineChoices(
  runners: Runner[] | null,
  engine: string,
  engineLabel: string,
  profiles: LLMProfile[],
  editing: LLMProfile | null,
  now = Date.now(),
): MachineChoice[] {
  const current = editing?.runner_id ?? "";
  const out = (runners ?? [])
    .filter((r) => !r.revoked_at || r.id === current)
    .map((r) => {
      const st = engineStateOn(r.id, engine, profiles);
      const parts = [runnerLabel(r, now), st ? engineStateText(st, engineLabel) : ""];
      return { id: r.id, label: parts.filter(Boolean).join(" · ") };
    });
  if (current && !out.some((c) => c.id === current))
    out.unshift({ id: current, label: editing?.runner_name ?? current });
  return out;
}

export function validateMachineForm(name: string, runnerID: string): string | null {
  if (!name) return t("lib.llm_profiles.errors.name_required");
  if (!runnerID) return t("lib.llm_machine.errors.runner_required");
  return null;
}

interface Note {
  tone: "danger" | "warn";
  html: string;
}

export interface MachineRow {
  badge: KeyBadge;
  // HTML, escaped like profileMeta().
  meta: string[];
  trouble: Note | null;
}

// What the row knows besides the profile.
export interface MachineContext {
  // undefined when the runner list could not be read.
  runner?: Runner;
  // This daemon runs nothing on machines: every run with the profile is refused.
  runnersOff?: boolean;
  engineLabel: string;
  // GET /engines' machine_login_command; empty when the engine names none.
  loginCommand?: string;
}

function loginNote(machine: string, ctx: MachineContext): string {
  const engine = ctx.engineLabel;
  return ctx.loginCommand
    ? t("lib.llm_machine.note.not_logged_in_html", {
        engine: esc(engine),
        runner: esc(machine),
        command: esc(ctx.loginCommand),
      })
    : esc(t("lib.llm_machine.note.not_logged_in", { engine, runner: machine }));
}

// The first runner protocol that reports its engines' CLI.
const ENGINE_REPORT_PROTOCOL = 2;

// A runner older than the engine report never sends one, so only an update helps; a current one
// reports on start, so it has not started yet or is about to report.
function notReportedText(machine: string, ctx: MachineContext): string {
  const r = ctx.runner;
  const outdated = !!r?.last_seen_at && (r.protocol ?? 0) < ENGINE_REPORT_PROTOCOL;
  const key = outdated
    ? "lib.llm_machine.note.not_reported"
    : "lib.llm_machine.note.not_reported_start";
  return t(key, { engine: ctx.engineLabel, runner: machine });
}

// The badge and, when it cannot run, the note for the engine's CLI on the machine.
function engineHealth(
  p: LLMProfile,
  machine: string,
  ctx: MachineContext,
): { badge: KeyBadge; note: Note | null } {
  const st = p.engine_state;
  if (!st)
    return {
      badge: { label: t("lib.llm_machine.badge.not_reported"), tone: "warn", title: "" },
      note: { tone: "warn", html: esc(notReportedText(machine, ctx)) },
    };
  const title = st.error ?? "";
  if (!st.installed)
    return {
      badge: { label: t("lib.llm_machine.badge.missing"), tone: "danger", title },
      note: {
        tone: "danger",
        html: esc(
          t("lib.llm_machine.note.not_installed", { engine: ctx.engineLabel, runner: machine }),
        ),
      },
    };
  if (!st.logged_in)
    return {
      badge: { label: t("lib.llm_machine.badge.not_logged_in"), tone: "danger", title },
      note: { tone: "danger", html: loginNote(machine, ctx) },
    };
  return {
    badge: { label: t("lib.llm_machine.badge.logged_in"), tone: "ok", title: "" },
    note: null,
  };
}

function blocked(label: string, text: string, meta: string[]): MachineRow {
  return {
    badge: { label, tone: "danger", title: "" },
    meta,
    trouble: { tone: "danger", html: esc(text) },
  };
}

// What a machine profile's row says in place of the key: the machine, its connection, and the
// engine's login there.
export function machineRow(p: LLMProfile, ctx: MachineContext, now = Date.now()): MachineRow {
  const { runner } = ctx;
  const machine = runner?.name ?? p.runner_name ?? p.runner_id ?? "";
  const st = p.engine_state;
  const meta = [t("lib.llm_machine.meta.machine", { runner: esc(machine) })];
  if (ctx.runnersOff)
    return blocked(
      t("lib.llm_machine.badge.runners_off"),
      t("lib.llm_machine.note.runners_off"),
      meta,
    );
  if (runner) meta.push(esc(runnerStatus(runner, now).label));
  if (st?.logged_in) meta.push(esc(engineStateText(st, ctx.engineLabel)));

  if (runner?.revoked_at)
    return blocked(
      t("lib.llm_machine.badge.revoked"),
      t("lib.llm_machine.note.revoked", { runner: machine }),
      meta,
    );
  const { badge, note } = engineHealth(p, machine, ctx);
  const notes: Note[] = [];
  if (runner && !runner.online)
    notes.push({ tone: "warn", html: esc(t("lib.llm_machine.note.offline", { runner: machine })) });
  if (note) notes.push(note);
  const trouble = notes.length
    ? {
        tone: notes.some((n) => n.tone === "danger") ? ("danger" as const) : ("warn" as const),
        html: notes.map((n) => n.html).join(" "),
      }
    : null;
  return { badge, meta, trouble };
}

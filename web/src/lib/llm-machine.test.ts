import { describe, expect, it } from "vitest";

import {
  MACHINE_KIND,
  credentialKinds,
  engineStateText,
  machineChoices,
  machineRow,
  validateMachineForm,
  type CredentialKind,
} from "./llm-machine";
import type { LLMProfile } from "./llm-profiles";
import type { EngineState, Runner } from "./runners";

const NOW = Date.parse("2026-11-16T12:00:00Z");

const keyKinds: CredentialKind[] = [
  { id: "api", label: "API key", prefix: "sk-ant-api" },
  { id: "federation", label: "Federation", prefix: "", federated: true },
];

function rn(over: Partial<Runner> = {}): Runner {
  return { id: "r1", name: "mac", created_at: "2026-11-01T00:00:00Z", online: true, ...over };
}

function state(over: Partial<EngineState> = {}): EngineState {
  return { engine: "claude-code", installed: true, logged_in: true, ...over };
}

const CC = { engineLabel: "Claude Code", loginCommand: "claude auth login" };

function machineProfile(over: Partial<LLMProfile> = {}): LLMProfile {
  return {
    id: "lp_1",
    name: "on-mac",
    engine: "claude-code",
    runner_id: "r1",
    runner_name: "mac",
    engine_state: state({ email: "ana@example.com" }),
    ...over,
  };
}

describe("credentialKinds", () => {
  // Design case 47: the kind appears only when the engine can run on a machine.
  it("offers the machine kind only with machine_login and machines on this daemon", () => {
    const offered = (machineLogin: boolean | undefined, runners: Runner[] | null) =>
      credentialKinds(keyKinds, machineLogin, null, runners).map((k) => k.id);
    expect(offered(true, [rn()])).toEqual(["api", "federation", MACHINE_KIND]);
    expect(offered(true, [])).toEqual(["api", "federation", MACHINE_KIND]);
    expect(offered(false, [rn()])).toEqual(["api", "federation"]);
    expect(offered(undefined, [rn()])).toEqual(["api", "federation"]);
    expect(offered(true, null)).toEqual(["api", "federation"]);
  });

  it("asks for no key on the machine kind", () => {
    const machine = credentialKinds(keyKinds, true, null, [rn()]).find((k) => k.machine);
    expect(machine).toMatchObject({ id: MACHINE_KIND, prefix: "", machine: true });
    expect(machine?.federated).toBeUndefined();
  });

  it("never lets an edit change the kind", () => {
    expect(credentialKinds(keyKinds, true, machineProfile(), [rn()]).map((k) => k.id)).toEqual([
      MACHINE_KIND,
    ]);
    expect(credentialKinds(keyKinds, true, machineProfile(), null).map((k) => k.id)).toEqual([
      MACHINE_KIND,
    ]);
    const keyed = machineProfile({ runner_id: undefined, has_key: true });
    expect(credentialKinds(keyKinds, true, keyed, [rn()]).map((k) => k.id)).toEqual([
      "api",
      "federation",
    ]);
  });
});

describe("engineStateText", () => {
  it.each([
    [state({ installed: false, logged_in: false }), "Claude Code not installed"],
    [state({ logged_in: false }), "Claude Code not signed in"],
    [state({ email: "ana@example.com", org: "Acme" }), "signed in as ana@example.com · Acme"],
    [state(), "signed in"],
  ])("%o says %s", (st, want) => {
    expect(engineStateText(st, "Claude Code")).toBe(want);
  });
});

describe("machineChoices", () => {
  const runners = [
    rn(),
    rn({ id: "r2", name: "linux", online: false }),
    rn({ id: "r3", name: "old", revoked_at: "2026-11-10T00:00:00Z" }),
  ];

  it("lists live machines with their connection and the login a profile already saw", () => {
    const got = machineChoices(
      runners,
      "claude-code",
      "Claude Code",
      [machineProfile()],
      null,
      NOW,
    );
    expect(got.map((c) => c.id)).toEqual(["r1", "r2"]);
    expect(got[0]?.label).toContain("mac");
    expect(got[0]?.label).toContain("signed in as ana@example.com");
    expect(got[1]?.label).not.toContain("signed in");
  });

  it("ignores the login another engine reported on the same machine", () => {
    const codex = machineProfile({ engine: "codex" });
    const got = machineChoices(runners, "claude-code", "Claude Code", [codex], null, NOW);
    expect(got[0]?.label).not.toContain("signed in");
  });

  it("keeps the edited profile's machine even when revoked or not listed", () => {
    const onOld = machineProfile({ runner_id: "r3", runner_name: "old" });
    expect(
      machineChoices(runners, "claude-code", "Claude Code", [], onOld, NOW).map((c) => c.id),
    ).toEqual(["r1", "r2", "r3"]);
    const gone = machineProfile({ runner_id: "r9", runner_name: "gone" });
    expect(machineChoices(null, "claude-code", "Claude Code", [], gone, NOW)).toEqual([
      { id: "r9", label: "gone" },
    ]);
  });

  it("is empty when no machine is registered", () => {
    expect(machineChoices([], "claude-code", "Claude Code", [], null, NOW)).toEqual([]);
    expect(machineChoices(null, "claude-code", "Claude Code", [], null, NOW)).toEqual([]);
  });
});

describe("validateMachineForm", () => {
  it("asks for a name and a machine", () => {
    expect(validateMachineForm("", "r1")).not.toBeNull();
    expect(validateMachineForm("on-mac", "")).toBe("Pick the machine this profile runs on.");
    expect(validateMachineForm("on-mac", "r1")).toBeNull();
  });
});

// Design case 48: the row shows the machine and the login there instead of the key hint.
describe("machineRow", () => {
  it("shows the machine, its connection and the account", () => {
    const row = machineRow(machineProfile(), { ...CC, runner: rn() }, NOW);
    expect(row.badge).toMatchObject({ label: "Signed in", tone: "ok" });
    expect(row.meta.join(" · ")).toContain("machine mac");
    expect(row.meta.join(" · ")).toContain("signed in as ana@example.com");
    expect(row.trouble).toBeNull();
  });

  it("falls back to the profile's runner name when the runner list is unavailable", () => {
    const row = machineRow(machineProfile(), CC, NOW);
    expect(row.meta[0]).toBe("machine mac");
    expect(row.meta).toHaveLength(2);
  });

  it("escapes the machine name", () => {
    const row = machineRow(machineProfile(), { ...CC, runner: rn({ name: "<b>mac</b>" }) }, NOW);
    expect(row.meta[0]).toBe("machine &lt;b&gt;mac&lt;/b&gt;");
  });

  it("proposes the login command when the engine is not signed in there", () => {
    const p = machineProfile({ engine_state: state({ logged_in: false }) });
    const row = machineRow(p, { ...CC, runner: rn() }, NOW);
    expect(row.badge).toMatchObject({ label: "Not signed in", tone: "danger" });
    expect(row.trouble?.tone).toBe("danger");
    expect(row.trouble?.html).toContain('<code class="code-inline">claude auth login</code>');
    expect(row.trouble?.html).toContain("mac");
  });

  it("names no command when the engine declares none", () => {
    const p = machineProfile({
      engine: "codex",
      engine_state: state({ engine: "codex", logged_in: false }),
    });
    const row = machineRow(p, { engineLabel: "Codex", runner: rn() }, NOW);
    expect(row.trouble?.html).not.toContain("<code");
    expect(row.trouble?.html).toContain("Codex is not signed in on mac");
  });

  // The command comes from GET /engines, not from a list kept in the web app.
  it("proposes whatever login command the engine declares", () => {
    const p = machineProfile({
      engine: "codex",
      engine_state: state({ engine: "codex", logged_in: false }),
    });
    const row = machineRow(
      p,
      { engineLabel: "Codex", loginCommand: "codex login", runner: rn() },
      NOW,
    );
    expect(row.trouble?.html).toContain('<code class="code-inline">codex login</code>');
  });

  it("says when the CLI is missing", () => {
    const missing = machineRow(
      machineProfile({
        engine_state: state({ installed: false, logged_in: false, error: "exit 127" }),
      }),
      { ...CC, runner: rn() },
      NOW,
    );
    expect(missing.badge).toMatchObject({
      label: "CLI missing",
      tone: "danger",
      title: "exit 127",
    });
    expect(missing.trouble?.html).toContain("PATH");
  });

  // An unreported CLI is unknown: a current runner that has not started yet reports nothing.
  it("does not call an unreported CLI missing, and asks to start a current runner", () => {
    const unreported = machineProfile({ engine_state: undefined });
    for (const runner of [
      rn({ protocol: 2, last_seen_at: "2026-11-16T11:59:00Z" }),
      rn(),
      undefined,
    ]) {
      const row = machineRow(unreported, { ...CC, runner }, NOW);
      expect(row.badge).toMatchObject({ label: "Not reported yet", tone: "warn" });
      expect(row.trouble?.html).toContain("has not reported Claude Code yet");
      expect(row.trouble?.html).toContain("thulu runner run");
      expect(row.trouble?.html).not.toContain("Update the runner");
    }
  });

  it("asks to update a runner too old to report its engines", () => {
    const old = rn({ protocol: 1, last_seen_at: "2026-11-16T11:59:00Z" });
    const row = machineRow(
      machineProfile({ engine_state: undefined }),
      { ...CC, runner: old },
      NOW,
    );
    expect(row.badge.tone).toBe("warn");
    expect(row.trouble?.html).toContain("Update the runner");
  });

  // With runners off, nothing about the machine or its CLI matters.
  it("says runners are off instead of blaming the runner", () => {
    const row = machineRow(
      machineProfile({ engine_state: undefined }),
      { ...CC, runnersOff: true },
      NOW,
    );
    expect(row.badge).toMatchObject({ label: "Runners off", tone: "danger" });
    expect(row.trouble).toMatchObject({ tone: "danger" });
    expect(row.trouble?.html).toContain("Runners are off on this instance");
    expect(row.trouble?.html).not.toContain("Update the runner");
    expect(row.meta).toEqual(["machine mac"]);
  });

  it("warns when the machine is offline and adds the login problem", () => {
    const offline = machineRow(machineProfile(), { ...CC, runner: rn({ online: false }) }, NOW);
    expect(offline.badge.tone).toBe("ok");
    expect(offline.trouble).toMatchObject({ tone: "warn" });
    expect(offline.trouble?.html).toContain("not connected");
    const both = machineRow(
      machineProfile({ engine_state: state({ logged_in: false }) }),
      { ...CC, runner: rn({ online: false }) },
      NOW,
    );
    expect(both.trouble?.tone).toBe("danger");
    expect(both.trouble?.html).toContain("not connected");
    expect(both.trouble?.html).toContain("claude auth login");
  });

  it("puts a revoked machine ahead of everything else", () => {
    const row = machineRow(
      machineProfile({ engine_state: state({ logged_in: false }) }),
      { ...CC, runner: rn({ revoked_at: "2026-11-10T00:00:00Z", online: false }) },
      NOW,
    );
    expect(row.badge).toMatchObject({ label: "Machine revoked", tone: "danger" });
    expect(row.trouble?.html).toContain("was revoked");
    expect(row.trouble?.html).not.toContain("claude auth login");
  });
});

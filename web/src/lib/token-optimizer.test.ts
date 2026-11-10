import { describe, expect, it } from "vitest";

import {
  canEnforce,
  effectiveLevel,
  engineOptionsHTML,
  engineWarning,
  firstAvailableEngine,
  levelLabel,
  modeNote,
  modeOptionsHTML,
  normalizeAgentOptimizer,
  optimizerPayload,
  optimizerProblem,
  parseWorkers,
  workerReasonText,
  type OptimizerWorker,
} from "./token-optimizer";

const workers: OptimizerWorker[] = [
  { engine: "claude-code", available: true, profile_name: "Claude (subscription)" },
  { engine: "codex", available: false, reason: "no_default_profile" },
  { engine: "opencode", available: false, reason: "unsupported_credential" },
];

const label = (name: string) => ({ "claude-code": "Claude Code", codex: "Codex" })[name] ?? name;

describe("normalizeAgentOptimizer", () => {
  it("fills every key in one order, so the editor's dirty check is stable", () => {
    expect(JSON.stringify(normalizeAgentOptimizer(undefined))).toBe(
      '{"level":"off","engine":"","model":""}',
    );
    expect(
      JSON.stringify(
        normalizeAgentOptimizer({ model: " haiku ", engine: "claude-code", level: "suggest" }),
      ),
    ).toBe('{"level":"suggest","engine":"claude-code","model":"haiku"}');
  });

  it("reads an unknown or inheriting level as off, and off keeps no worker", () => {
    expect(normalizeAgentOptimizer({ level: "", engine: "claude-code", model: "haiku" })).toEqual({
      level: "off",
      engine: "",
      model: "",
    });
    expect(normalizeAgentOptimizer({ level: "max" }).level).toBe("off");
  });

  it("ignores fields the daemon does not take", () => {
    const old = { level: "enforce", firm: true, by_stage: { dev: "off" }, threshold_bytes: 2048 };
    expect(normalizeAgentOptimizer(old)).toEqual({ level: "enforce", engine: "", model: "" });
  });
});

describe("optimizerPayload", () => {
  it("sends only the level when off", () => {
    expect(optimizerPayload({ level: "off", engine: "claude-code", model: "haiku" })).toEqual({
      level: "off",
    });
  });

  it("sends the level, engine and model when on, and nothing else", () => {
    const sent = optimizerPayload({
      level: "enforce",
      engine: "claude-code",
      model: "haiku",
      ...({ firm: true, by_stage: {}, threshold_bytes: 1 } as object),
    });
    expect(sent).toEqual({ level: "enforce", engine: "claude-code", model: "haiku" });
    expect(Object.keys(sent)).toEqual(["level", "engine", "model"]);
  });
});

describe("optimizerProblem", () => {
  it("asks for an engine, then a model, only while the mode is on", () => {
    expect(optimizerProblem({ level: "off", engine: "", model: "" })).toBe("");
    expect(optimizerProblem({ level: "suggest", engine: "", model: "haiku" })).toBe("engine");
    expect(optimizerProblem({ level: "suggest", engine: "claude-code", model: " " })).toBe("model");
    expect(optimizerProblem({ level: "enforce", engine: "claude-code", model: "haiku" })).toBe("");
  });
});

describe("enforce and the agent's engine", () => {
  it("takes effect only on Claude Code; elsewhere it runs as suggest", () => {
    expect(canEnforce("claude-code")).toBe(true);
    expect(canEnforce("codex")).toBe(false);
    expect(effectiveLevel("enforce", "codex")).toBe("suggest");
    expect(effectiveLevel("enforce", "claude-code")).toBe("enforce");
    expect(effectiveLevel("suggest", "codex")).toBe("suggest");
    expect(effectiveLevel("off", "codex")).toBe("off");
  });

  it("disables enforce for another engine unless it is the saved choice", () => {
    expect(modeOptionsHTML("off", "claude-code")).not.toContain("disabled");
    expect(modeOptionsHTML("suggest", "codex")).toContain(
      '<option value="enforce" disabled>Enforce</option>',
    );
    expect(modeOptionsHTML("enforce", "codex")).toContain(
      '<option value="enforce" selected>Enforce</option>',
    );
  });

  it("says why enforce is unavailable, and warns when it would run as suggest", () => {
    expect(modeNote("enforce", "claude-code")).toEqual({ tone: "help", text: "" });
    expect(modeNote("suggest", "codex")).toEqual({
      tone: "help",
      text: "Enforce works only for agents that run on Claude Code.",
    });
    expect(modeNote("enforce", "codex")).toEqual({
      tone: "warn",
      text: "This agent doesn't run on Claude Code, so Enforce runs as Suggest.",
    });
  });
});

describe("parseWorkers", () => {
  const entry = {
    engine: "claude-code",
    available: true,
    via: "cli",
    provider: "anthropic",
    profile_name: "Claude (suscripción)",
    reason: "",
  };

  it("takes a bare array", () => {
    expect(parseWorkers([entry])).toEqual([
      { engine: "claude-code", available: true, profile_name: "Claude (suscripción)", reason: "" },
    ]);
  });

  it("takes the array wrapped in {workers}", () => {
    expect(parseWorkers({ workers: [entry] }).map((w) => w.engine)).toEqual(["claude-code"]);
  });

  it("drops entries without an engine and reads anything else as empty", () => {
    expect(parseWorkers([null, { available: true }, { engine: "codex" }])).toEqual([
      { engine: "codex", available: false, profile_name: undefined, reason: undefined },
    ]);
    expect(parseWorkers(null)).toEqual([]);
    expect(parseWorkers({ engines: [] })).toEqual([]);
    expect(parseWorkers("nope")).toEqual([]);
  });
});

describe("engine select", () => {
  it("lists every engine, the unavailable ones disabled with their reason", () => {
    const html = engineOptionsHTML(workers, "", label);
    expect(html.startsWith('<option value="" selected>Pick an engine</option>')).toBe(true);
    expect(html).toContain('<option value="claude-code">Claude Code</option>');
    expect(html).toContain(
      '<option value="codex" disabled title="no default credential for this engine">Codex (no default credential for this engine)</option>',
    );
    expect(html).toContain("opencode (its default credential can&#39;t run it)");
  });

  it("keeps a saved engine selectable even when it lost its worker or left the list", () => {
    expect(engineOptionsHTML(workers, "codex", label)).toContain(
      '<option value="codex" selected title=',
    );
    expect(engineOptionsHTML(workers, "gemini", label)).toContain(
      '<option value="gemini" selected>gemini</option>',
    );
  });

  it("starts a mode turned on with the first engine that has a worker", () => {
    expect(firstAvailableEngine(workers)).toBe("claude-code");
    expect(firstAvailableEngine(workers.slice(1))).toBe("");
  });

  it("warns when the chosen engine has no worker now", () => {
    expect(engineWarning(workers, "claude-code")).toBe("");
    expect(engineWarning(workers, "codex")).toBe("no default credential for this engine");
    expect(engineWarning(workers, "")).toBe("");
    expect(engineWarning(workers, "gemini")).toBe("");
    expect(workerReasonText("something_new")).toBe("not available");
  });
});

describe("levelLabel", () => {
  it("names the levels the usage pages show", () => {
    expect(levelLabel("off")).toBe("Off");
    expect(levelLabel("suggest")).toBe("Suggest");
    expect(levelLabel("enforce")).toBe("Enforce");
  });
});

import { describe, expect, it } from "vitest";

import { stepItemClasses } from "./ui/steps";
import {
  CHECK_STEP,
  agentChecklist,
  agentNameProblem,
  copyStageConfig,
  effectiveNoteTone,
  formSkillsMode,
  hostWithoutKey,
  keepOrder,
  runsWithCredential,
  savedStagesLine,
  suggestAgentName,
} from "./agent-editor";

describe("agentNameProblem", () => {
  it("accepts the daemon's names", () => {
    expect(agentNameProblem("backend", [])).toBe("");
    expect(agentNameProblem("qa_2-bis", [])).toBe("");
    expect(agentNameProblem("a".repeat(64), [])).toBe("");
  });

  it("names what is wrong", () => {
    expect(agentNameProblem("", [])).toBe("required");
    expect(agentNameProblem("Backend", [])).toBe("invalid");
    expect(agentNameProblem("my agent", [])).toBe("invalid");
    expect(agentNameProblem("-lead", [])).toBe("invalid");
    expect(agentNameProblem("a".repeat(65), [])).toBe("invalid");
    expect(agentNameProblem("auditor", [])).toBe("reserved");
    expect(agentNameProblem("backend", ["backend"])).toBe("taken");
  });
});

describe("suggestAgentName", () => {
  it("lowercases and joins words with a dash", () => {
    expect(suggestAgentName("Backend Dev")).toBe("backend-dev");
    expect(suggestAgentName("  QA / Release  ")).toBe("qa-release");
  });

  it("drops accents and leading dashes", () => {
    expect(suggestAgentName("Revisión")).toBe("revision");
    expect(suggestAgentName("--lead")).toBe("lead");
  });

  it("offers nothing when the name is already valid or nothing is left", () => {
    expect(suggestAgentName("backend")).toBe("");
    expect(suggestAgentName("!!!")).toBe("");
  });

  it("cuts at 64 characters", () => {
    expect(suggestAgentName("A".repeat(70))).toBe("a".repeat(64));
  });
});

describe("formSkillsMode", () => {
  it("reads auto, empty and unknown modes as the select's auto", () => {
    expect(formSkillsMode("auto")).toBe("");
    expect(formSkillsMode(undefined)).toBe("");
    expect(formSkillsMode("")).toBe("");
    expect(formSkillsMode("bogus")).toBe("");
  });
  it("keeps native and inline", () => {
    expect(formSkillsMode("native")).toBe("native");
    expect(formSkillsMode("inline")).toBe("inline");
  });
});

describe("keepOrder", () => {
  it("keeps the stored order of what is still there", () => {
    expect(
      keepOrder(["in_progress", "review", "mention"], ["in_progress", "mention", "review"]),
    ).toEqual(["in_progress", "review", "mention"]);
  });
  it("drops what was unticked and appends what was ticked", () => {
    expect(keepOrder(["review", "in_progress"], ["in_progress", "chat"])).toEqual([
      "in_progress",
      "chat",
    ]);
  });
});

describe("copyStageConfig", () => {
  it("copies one stage's list to the others", () => {
    const out = copyStageConfig({ review: ["a", "b"], done: ["c"] }, "review", [
      "review",
      "done",
      "qa",
    ]);
    expect(out).toEqual({ review: ["a", "b"], done: ["a", "b"], qa: ["a", "b"] });
  });

  it("an empty source clears the targets and leaves the input alone", () => {
    const map = { done: ["c"] };
    expect(copyStageConfig(map, "review", ["done"])).toEqual({});
    expect(map).toEqual({ done: ["c"] });
  });

  it("copies are not shared arrays", () => {
    const out = copyStageConfig({ review: ["a"] }, "review", ["done"]);
    out.done.push("x");
    expect(out.review).toEqual(["a"]);
  });
});

describe("CHECK_STEP", () => {
  it("draws each checklist state as a step state the step list knows", () => {
    expect(CHECK_STEP).toEqual({ ok: "done", todo: "pending", unknown: "unknown" });
    for (const state of Object.values(CHECK_STEP))
      expect(() => stepItemClasses(state)).not.toThrow();
  });
});

describe("agentChecklist", () => {
  const base = {
    validStages: ["in_progress"],
    knownStages: ["in_progress", "review", "mention", "chat"],
    prompts: { fallback: true, own: [] },
    hasCredential: true,
    assigned: 2,
  };
  const state = (items: ReturnType<typeof agentChecklist>, id: string) =>
    items.find((x) => x.id === id);

  it("is all done for an agent ready to run", () => {
    expect(agentChecklist(base).every((x) => x.state === "ok")).toBe(true);
  });

  it("does not count a stage that no longer exists", () => {
    expect(state(agentChecklist({ ...base, validStages: ["gone"] }), "stages")?.state).toBe("todo");
  });

  it("reads no ticked stage as every column", () => {
    expect(state(agentChecklist({ ...base, validStages: [] }), "stages")).toEqual({
      id: "stages",
      state: "ok",
      anyColumn: true,
    });
  });

  it("lists the stages with no prompt when there is no main.md", () => {
    const items = agentChecklist({
      ...base,
      validStages: ["in_progress", "review"],
      prompts: { fallback: false, own: ["review"] },
    });
    expect(state(items, "prompt")).toEqual({
      id: "prompt",
      state: "todo",
      missing: ["in_progress"],
    });
  });

  it("asks for a credential and an assignment", () => {
    const items = agentChecklist({ ...base, hasCredential: false, assigned: 0 });
    expect(state(items, "credential")?.state).toBe("todo");
    expect(state(items, "assigned")?.state).toBe("todo");
  });

  it("does not count a credential the provider refused", () => {
    const items = agentChecklist({ ...base, keyRejected: true });
    expect(state(items, "credential")).toEqual({ id: "credential", state: "todo", rejected: true });
    expect(
      state(agentChecklist({ ...base, hasCredential: false, keyRejected: true }), "credential"),
    ).toEqual({ id: "credential", state: "todo" });
  });

  it("counts a mode as a way to run", () => {
    const items = agentChecklist({ ...base, validStages: ["chat"], assigned: 0 });
    expect(state(items, "assigned")).toEqual({
      id: "assigned",
      state: "ok",
      viaModes: true,
      modes: ["chat"],
    });
  });

  it("names only the modes the agent runs in", () => {
    const items = agentChecklist({ ...base, validStages: ["chat", "mention"], assigned: 0 });
    expect(state(items, "assigned")?.modes).toEqual(["mention", "chat"]);
  });

  it("says unknown when a read failed", () => {
    const items = agentChecklist({ ...base, prompts: null, hasCredential: null, assigned: null });
    expect(items.filter((x) => x.state === "unknown").map((x) => x.id)).toEqual([
      "prompt",
      "credential",
      "assigned",
    ]);
  });
});

describe("savedStagesLine", () => {
  const label = (id: string) => ({ backlog: "Backlog", review: "Revisión" })[id] ?? id;

  it("lists the saved stages by label", () => {
    expect(savedStagesLine(["backlog", "review", "qa"], label)).toBe("Backlog · Revisión · qa");
  });

  it("gives null for no stage, which the checklist reads as any column", () => {
    expect(savedStagesLine([], label)).toBeNull();
    expect(savedStagesLine(undefined, label)).toBeNull();
    expect(
      agentChecklist({
        validStages: [],
        knownStages: [],
        prompts: null,
        hasCredential: null,
        assigned: null,
      })[0],
    ).toMatchObject({ anyColumn: true });
  });
});

describe("hostWithoutKey", () => {
  it("is a host source the daemon does not let runs use", () => {
    expect(hostWithoutKey({ source: "host", has_credential: false, host_fallback: false })).toBe(
      true,
    );
    expect(hostWithoutKey({ source: "host", has_credential: false })).toBe(true);
  });

  it("is not a host the daemon falls back to, nor a profile", () => {
    expect(hostWithoutKey({ source: "host", has_credential: false, host_fallback: true })).toBe(
      false,
    );
    expect(hostWithoutKey({ source: "binding", has_credential: false })).toBe(false);
    expect(hostWithoutKey({ source: "workspace_default", has_credential: true })).toBe(false);
  });
});

describe("runsWithCredential", () => {
  it("counts a profile's key", () => {
    expect(runsWithCredential({ source: "workspace_default", has_credential: true })).toBe(true);
  });

  it("counts the host only when the daemon lets runs use it", () => {
    expect(runsWithCredential({ source: "host", has_credential: false, host_fallback: true })).toBe(
      true,
    );
    expect(
      runsWithCredential({ source: "host", has_credential: false, host_fallback: false }),
    ).toBe(false);
    expect(runsWithCredential({ source: "host", has_credential: false })).toBe(false);
  });

  it("does not count a profile with no key", () => {
    expect(runsWithCredential({ source: "binding", has_credential: false })).toBe(false);
  });
});

describe("effectiveNoteTone", () => {
  it("is info when the run has a working key and nothing to warn about", () => {
    expect(effectiveNoteTone({ hasCredential: true, warnings: 0 })).toBe("info");
  });
  it("warns without a credential, with daemon warnings or an unchecked key", () => {
    expect(effectiveNoteTone({ hasCredential: false, warnings: 0 })).toBe("warn");
    expect(effectiveNoteTone({ hasCredential: true, warnings: 2 })).toBe("warn");
    expect(effectiveNoteTone({ keyTone: "warn", hasCredential: true, warnings: 0 })).toBe("warn");
  });
  it("is danger when the provider refused the key, whatever else holds", () => {
    expect(effectiveNoteTone({ keyTone: "danger", hasCredential: false, warnings: 1 })).toBe(
      "danger",
    );
  });
});

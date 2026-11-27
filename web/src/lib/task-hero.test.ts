// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import { joinTitleKey, pathSteps, splitTitleKey, stagePathHTML } from "./task-hero";

describe("splitTitleKey", () => {
  it("takes a leading ticket key off the title", () => {
    expect(splitTitleKey("ABC-123: Remove the account-check screen")).toEqual({
      key: "ABC-123",
      rest: "Remove the account-check screen",
    });
    expect(splitTitleKey("OPS2-7 - Rotate keys")).toEqual({ key: "OPS2-7", rest: "Rotate keys" });
  });

  it("leaves a title without a key whole", () => {
    expect(splitTitleKey("Fix the login: again")).toEqual({
      key: "",
      rest: "Fix the login: again",
    });
    expect(splitTitleKey("rb-1: lower case is not a key")).toEqual({
      key: "",
      rest: "rb-1: lower case is not a key",
    });
    expect(splitTitleKey("RB-1:")).toEqual({ key: "", rest: "RB-1:" });
    expect(splitTitleKey("")).toEqual({ key: "", rest: "" });
  });

  it("puts the key back for a save", () => {
    expect(joinTitleKey("ABC-123", "New title")).toBe("ABC-123: New title");
    expect(joinTitleKey("", "New title")).toBe("New title");
  });
});

const STAGES = [
  { id: "backlog", label: "Backlog", initial: true },
  { id: "spec", label: "Spec" },
  { id: "build", label: "Build" },
  { id: "review", label: "Review" },
  { id: "done", label: "Done", terminal: true },
];

describe("pathSteps", () => {
  it("marks the columns before the current one done and skips the first and last", () => {
    const steps = pathSteps({ stages: STAGES, current: "build" });
    expect(steps.map((s) => [s.id, s.state])).toEqual([
      ["spec", "done"],
      ["build", "now"],
      ["review", "todo"],
    ]);
  });

  it("keeps the first or last column while the task sits in it", () => {
    expect(pathSteps({ stages: STAGES, current: "backlog" }).map((s) => s.state)).toEqual([
      "now",
      "todo",
      "todo",
      "todo",
    ]);
    expect(pathSteps({ stages: STAGES, current: "done" }).at(-1)?.state).toBe("now");
  });

  it("locks the steps ahead where an approval holds the task, not the ones behind", () => {
    const locks = [
      { stage: "review", label: "Push and PR" },
      { stage: "spec", label: "Spec approved" },
    ];
    const steps = pathSteps({ stages: STAGES, current: "build", locks });
    expect(steps.map((s) => s.lock)).toEqual(["", "", "Push and PR"]);
  });

  it("marks nothing done when the current column is not in the flow", () => {
    expect(pathSteps({ stages: STAGES, current: "gone" }).every((s) => s.state === "todo")).toBe(
      true,
    );
  });
});

describe("stagePathHTML", () => {
  it("draws the current step as the stage picker button, blue while it runs", () => {
    const html = stagePathHTML({ stages: STAGES, current: "build", running: true });
    const box = document.createElement("div");
    box.innerHTML = html;
    const now = box.querySelector<HTMLButtonElement>("#dr-stage");
    if (!now) throw new Error("no current step");
    expect(now.tagName).toBe("BUTTON");
    expect(now.className).toBe("tv-step now tone-run");
    expect(now.getAttribute("aria-current")).toBe("step");
    expect(box.querySelectorAll(".tv-step.done")).toHaveLength(1);
    expect(box.querySelector(".tv-path.compact")).toBeNull();
  });

  it("numbers the steps for the compact path and escapes labels", () => {
    const html = stagePathHTML({
      stages: [{ id: "a", label: "<b>A</b>" }],
      current: "a",
      compact: true,
    });
    expect(html).toContain("tv-path compact");
    expect(html).toContain("&lt;b&gt;A&lt;/b&gt;");
    expect(html).toContain('<span class="tv-step-n" aria-hidden="true">1</span>');
  });

  it("names the agent of each column in its tooltip", () => {
    const html = stagePathHTML({ stages: STAGES, current: "spec", agents: { build: "dev-agent" } });
    expect(html).toContain('title="Build, dev-agent"');
  });

  it("strikes a skipped column, says why in its tooltip and names it for screen readers", () => {
    const stages = STAGES.map((s) =>
      s.id === "spec" || s.id === "review"
        ? { ...s, skip: { reason: "docs only", by: "agent:thulu-refiner" } }
        : s,
    );
    const steps = pathSteps({ stages, current: "build" });
    expect(steps.map((s) => s.skipped)).toEqual([
      "Spec is skipped (by thulu-refiner): docs only",
      "",
      "Review is skipped (by thulu-refiner): docs only",
    ]);
    const box = document.createElement("div");
    box.innerHTML = stagePathHTML({ stages, current: "build" });
    const skipped = box.querySelectorAll<HTMLElement>(".tv-step.skipped");
    expect([...skipped].map((el) => el.className)).toEqual([
      "tv-step done skipped",
      "tv-step skipped",
    ]);
    expect(skipped[1].title).toBe("Review is skipped (by thulu-refiner): docs only");
    expect(skipped[1].querySelector(".visually-hidden")?.textContent).toBe("(skipped)");
  });

  it("never marks the current step skipped", () => {
    const stages = STAGES.map((s) =>
      s.id === "build" ? { ...s, skip: { reason: "x", by: "ana@example.com" } } : s,
    );
    expect(pathSteps({ stages, current: "build" })[1]).toMatchObject({ state: "now", skipped: "" });
    expect(stagePathHTML({ stages, current: "build" })).not.toContain("skipped");
  });
});

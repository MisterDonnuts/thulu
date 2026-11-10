// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import { setupFinish, setupStepItem } from "./onboarding-steps";
import { stepItemHTML, stepFinishHTML, type StepFinish } from "./ui";

const finishHTML = (f: StepFinish | undefined): string => (f ? stepFinishHTML(f) : "");

describe("setupFinish", () => {
  it("links the sentence and keeps the command outside the link, as code", () => {
    const finish = setupFinish({
      finish: "import your own",
      href: "/agents/",
      command: "thulu agents import <dir>",
    });
    expect(finishHTML(finish)).toBe(
      '<a href="/agents/">import your own</a> <code class="code-inline">thulu agents import &lt;dir&gt;</code>',
    );
  });

  it("shows the sentence as text when the step has no page", () => {
    const finish = setupFinish({
      finish: "create one from the CLI",
      command: "octopus workspace create",
    });
    expect(finishHTML(finish)).toBe(
      'create one from the CLI <code class="code-inline">octopus workspace create</code>',
    );
  });

  it("drops an unsafe href, omits a missing command and reads Finish for a bare page", () => {
    expect(finishHTML(setupFinish({ finish: "go", href: "javascript:alert(1)" }))).toBe("go");
    expect(finishHTML(setupFinish({ href: "/settings/#llm" }))).toBe(
      '<a href="/settings/#llm">Finish</a>',
    );
  });

  it("hands the steps family the sentence, the page and the command", () => {
    expect(setupFinish({ finish: "import", href: "/agents/", command: "octopus x" })).toEqual({
      text: "import",
      href: "/agents/",
      command: "octopus x",
    });
  });

  it("is nothing when the step names no way to finish it", () => {
    expect(setupFinish({})).toBeUndefined();
    expect(setupFinish({ href: "javascript:alert(1)" })).toBeUndefined();
  });
});

describe("setupStepItem", () => {
  it("says what a finished step found and nothing else", () => {
    const item = setupStepItem({
      id: "llm",
      title: "AI engine",
      done: true,
      detail: "Claude — default profile",
      effect: "agents cannot run",
      href: "/settings/#llm",
    });
    expect(item).toEqual({
      title: "AI engine",
      state: "done",
      detail: "Claude — default profile",
      attrs: { "data-step": "llm" },
    });
  });

  it("says what an unfinished step costs and how to finish it", () => {
    const el = document.createElement("ol");
    el.innerHTML = stepItemHTML(
      setupStepItem({
        id: "project",
        title: "A project",
        done: false,
        detail: "0 projects",
        effect: "agents have no code to work on",
        finish: "add one",
        href: "/projects/",
      }),
    );
    const li = el.querySelector("li");
    if (!li) throw new Error("no step");
    expect(li.dataset.state).toBe("pending");
    expect(li.dataset.step).toBe("project");
    expect(li.querySelector(".step-title")?.textContent).toBe("A project");
    expect(li.querySelector(".step-title a")).toBeNull();
    expect(li.querySelector(".step-detail")?.textContent).toBe("0 projects");
    expect(li.querySelector(".step-note")?.textContent).toBe(
      "Until then: agents have no code to work on",
    );
    expect(li.querySelector<HTMLAnchorElement>(".step-finish a")?.getAttribute("href")).toBe(
      "/projects/",
    );
  });
});

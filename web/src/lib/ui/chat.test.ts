// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { t } from "../../i18n";
import {
  chatDuration,
  chatEffectsHTML,
  chatFootEl,
  chatSayStepEl,
  chatStepsHTML,
  chatTokens,
  chatToolInfo,
  chatToolSummary,
  parseChatEffects,
} from "./chat";

describe("chatToolInfo", () => {
  it("names a Thulu tool in words, whatever engine spelled it", () => {
    for (const name of [
      "mcp__octopus-api__task_update",
      "octopus-api.task_update",
      "octopus-api_task_update",
    ]) {
      const info = chatToolInfo(name);
      expect(info.kind).toBe("thulu");
      expect(info.text).toBe(t("chat.tools.thulu.task_update"));
    }
  });

  it("keeps another server's tool as server and tool", () => {
    expect(chatToolInfo("mcp__argus-browser__screenshot")).toMatchObject({
      kind: "mcp",
      label: "argus-browser",
      text: "screenshot",
    });
  });

  it("drops the cd prefix of a shell command", () => {
    expect(chatToolInfo("Bash", "cd /repo && go test ./...")).toMatchObject({
      kind: "cmd",
      text: "go test ./...",
      mono: true,
    });
  });

  it("sorts reads, edits and subagents", () => {
    expect(chatToolInfo("Read", "a.go").kind).toBe("read");
    expect(chatToolInfo("apply_patch").kind).toBe("edit");
    expect(chatToolInfo("Task").kind).toBe("sub");
  });
});

describe("chatToolSummary", () => {
  it("counts by kind in a fixed order", () => {
    const s = chatToolSummary([
      { name: "Read", hint: "" },
      { name: "Bash", hint: "ls" },
      { name: "Bash", hint: "pwd" },
    ]);
    expect(s).toBe(
      [t("chat.steps.kinds.cmd", { count: 2 }), t("chat.steps.kinds.read", { count: 1 })].join(
        ", ",
      ),
    );
  });
});

describe("chatDuration and chatTokens", () => {
  it("writes seconds under a minute and minutes above", () => {
    expect(chatDuration(42_000)).toBe(t("chat.duration.seconds", { s: 42 }));
    expect(chatDuration(138_000)).toBe(t("chat.duration.minutes", { m: 2, s: "18" }));
  });

  it("rounds tokens to thousands", () => {
    expect(chatTokens(950)).toBe("950");
    expect(chatTokens(48_200)).toBe("48k");
  });
});

describe("chat effects", () => {
  it("reads the list the daemon stores and skips what is not an effect", () => {
    const raw = JSON.stringify([{ action: "created", task_id: "a" }, { nope: 1 }, null]);
    expect(parseChatEffects(raw)).toEqual([{ action: "created", task_id: "a" }]);
    expect(parseChatEffects("not json")).toEqual([]);
    expect(parseChatEffects(undefined)).toEqual([]);
  });

  it("draws one row per effect with the task chip", () => {
    const html = chatEffectsHTML(
      [
        { action: "moved", task_id: "a", detail: "Review" },
        { action: "commented", task_id: "b", error: true },
      ],
      (id) => `<i data-chip="${id}"></i>`,
    );
    const root = document.createElement("div");
    root.innerHTML = html;
    expect(root.querySelectorAll(".cx-fx-row")).toHaveLength(2);
    expect(root.textContent).toContain(t("chat.effects.moved_to", { stage: "Review" }));
    expect(root.querySelector("[data-error]")).not.toBeNull();
    expect(root.querySelector('[data-chip="a"]')).not.toBeNull();
  });

  it("draws nothing for no effects", () => {
    expect(chatEffectsHTML([], () => "")).toBe("");
  });
});

describe("chatStepsHTML", () => {
  it("names the turn whose transcript it loads", () => {
    const root = document.createElement("div");
    root.innerHTML = chatStepsHTML({ summaryHTML: "s", bodyHTML: "b", turn: "m1" });
    expect(root.querySelector("details")?.dataset.turn).toBe("m1");
  });
});

describe("live turn nodes", () => {
  it("puts what the agent said in as text, never as markup", () => {
    const el = chatSayStepEl('<img src=x onerror="alert(1)">');
    expect(el.querySelector("img")).toBeNull();
    expect(el.querySelector("span")?.textContent).toBe('<img src=x onerror="alert(1)">');
    expect(el.dataset.state).toBe("say");
  });

  it("cuts a long note", () => {
    expect(chatSayStepEl("a".repeat(300)).textContent).toHaveLength(240);
  });

  it("writes one span per foot part and skips the empty ones", () => {
    const el = chatFootEl(["ok", "", "<b>1 s</b>"]);
    expect(el.className).toBe("cx-foot");
    expect([...el.querySelectorAll("span")].map((s) => s.textContent)).toEqual([
      "ok",
      "<b>1 s</b>",
    ]);
    expect(el.querySelector("b")).toBeNull();
  });
});

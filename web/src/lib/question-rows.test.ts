// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import {
  listedQuestions,
  questionCardHTML,
  questionHeadline,
  questionRowBodyId,
  questionTally,
  type QuestionCardOptions,
} from "./activity-html";
import type { TaskQuestion } from "./questions";

const q = (over: Partial<TaskQuestion> = {}): TaskQuestion => ({
  id: "q1",
  comment_id: "c1",
  agent: "backend",
  stage: "dev",
  kind: "question",
  blocking: true,
  status: "open",
  created_at: "2026-11-20T10:00:00Z",
  payload: {
    questions: [
      { header: "Cache", question: "Which cache layer?\nWe need it for the sessions." },
      { question: "Which endpoints?" },
    ],
  },
  ...over,
});

const stageLabel = (id: string) => (id === "dev" ? "Development" : id);

function one<T extends Element = HTMLElement>(root: ParentNode, sel: string): T {
  const el = root.querySelector<T>(sel);
  if (!el) throw new Error(`no ${sel}`);
  return el;
}

function dom(html: string): HTMLElement {
  const el = document.createElement("div");
  el.innerHTML = html;
  return el;
}

describe("listedQuestions and questionTally", () => {
  const open1 = q({ id: "a", created_at: "2026-11-20T09:00:00Z" });
  const open2 = q({ id: "b", created_at: "2026-11-20T08:00:00Z" });
  const done1 = q({ id: "c", status: "answered", created_at: "2026-11-19T08:00:00Z" });
  const done2 = q({ id: "d", status: "canceled", created_at: "2026-11-20T07:00:00Z" });
  const empty = q({ id: "e", payload: { questions: [] } });

  it("puts pending questions first, oldest first, then the rest newest first", () => {
    expect(listedQuestions([done1, open1, empty, done2, open2]).map((x) => x.id)).toEqual([
      "b",
      "a",
      "d",
      "c",
    ]);
  });

  it("counts every question with items and the pending ones", () => {
    expect(questionTally([done1, open1, empty, done2, open2])).toEqual({ total: 4, pending: 2 });
    expect(questionTally(undefined)).toEqual({ total: 0, pending: 0 });
  });
});

describe("questionHeadline", () => {
  it("takes the header and the first line of the first question", () => {
    expect(questionHeadline(q())).toEqual({ header: "Cache", text: "Which cache layer?", more: 1 });
  });

  it("works without a header", () => {
    const h = questionHeadline(q({ payload: { questions: [{ question: "  Go on?  " }] } }));
    expect(h).toEqual({ header: "", text: "Go on?", more: 0 });
  });
});

describe("questionCardHTML", () => {
  const base: QuestionCardOptions = {
    expanded: false,
    stageLabel,
    agentAvatarHTML: '<span class="av-agent"></span>',
    personAvatarHTML: '<span class="av-person"></span>',
    answeredBy: "You",
    answeredByViewer: true,
  };
  const answered = q({
    status: "answered",
    blocking: false,
    answered_at: "2026-11-20T10:30:00Z",
    answers: {
      answers: [
        { question_index: 0, selected: ["Redis"], other: "" },
        { question_index: 1, selected: [], other: "Only <login>" },
      ],
      note: "Thanks",
    },
    payload: {
      questions: [
        {
          header: "Cache",
          question: "Which cache layer?",
          options: [{ label: "Redis", description: "In memory" }],
        },
        { header: "Scope", question: "Which endpoints?" },
      ],
    },
  });

  it("is a row that opens the body it controls", () => {
    const box = dom(questionCardHTML(answered, base));
    const btn = one<HTMLButtonElement>(box, "button.q-row");
    const body = one(box, ".q-body");
    expect(btn.dataset.qToggle).toBe("q1");
    expect(btn.getAttribute("aria-expanded")).toBe("false");
    expect(btn.getAttribute("aria-controls")).toBe(questionRowBodyId("q1"));
    expect(body.id).toBe(questionRowBodyId("q1"));
    expect(body.hidden).toBe(true);
    expect(box.firstElementChild?.getAttribute("data-question-row")).toBe("q1");
    expect(
      dom(questionCardHTML(answered, { ...base, expanded: true }))
        .querySelector(".q-body")
        ?.hasAttribute("hidden"),
    ).toBe(false);
  });

  it("names the agent, counts the questions, lists their headers and who answered", () => {
    const box = dom(questionCardHTML(answered, base));
    expect(one(box, ".nm").textContent).toBe("backend");
    expect(one(box, ".cm-tag").textContent).toBe("2 questions");
    expect(one(box, ".q-heads").textContent).toBe("Cache · Scope");
    expect(one(box, ".q-state").textContent).toMatch(/^You answered /);
    expect(one(box, ".q-state").classList.contains("wait")).toBe(false);
    const other = dom(
      questionCardHTML(answered, { ...base, answeredBy: "ana", answeredByViewer: false }),
    );
    expect(one(other, ".q-state").textContent).toMatch(/^ana answered /);
  });

  it("shows each answer in place, the options it offered and the note, escaped", () => {
    const box = dom(questionCardHTML(answered, { ...base, expanded: true }));
    const answers = [...box.querySelectorAll(".q > .a")].map((a) => a.textContent);
    expect(answers).toEqual(["Redis", "Only <login>"]);
    expect(box.innerHTML).toContain("Only &lt;login&gt;");
    expect(box.querySelectorAll(".q-opts")).toHaveLength(1);
    expect(one(box, ".a.note").textContent).toBe("Thanks");
    expect(one(box, ".q-info").textContent).toContain("The agent kept working while it waited");
    expect(one(box, ".q-info").textContent).toContain("in Development");
    expect(one(box, "[data-see-activity]").getAttribute("data-see-activity")).toBe("c1");
  });

  it("waits for the person with the answer form when the question is open", () => {
    const box = dom(questionCardHTML(q(), { ...base, expanded: true, formHTML: "<form>f</form>" }));
    expect(one(box, ".q-state").classList.contains("wait")).toBe(true);
    expect(one(box, ".q-state").textContent).toBe("Waiting for you");
    expect(one(box, ".q-info").textContent).toContain(
      "The agent stopped and waited for the answer",
    );
    expect(box.querySelector("form")?.textContent).toBe("f");
    expect(box.querySelector(".q > .a")).toBeNull();
  });
});

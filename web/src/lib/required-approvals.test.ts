// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import {
  approvalKeyFromLabel,
  approvalRowEl,
  approvalsEditorHTML,
  approvalsPatch,
  normalizeRequiredApprovals,
  onChangesOf,
} from "./required-approvals";

const types = [
  { id: "feature", label: "Feature" },
  { id: "bugfix", label: "Bugfix" },
];
const earlier = [
  { id: "backlog", label: "Backlog" },
  { id: "in_progress", label: "In progress" },
];

function find<T extends Element>(root: ParentNode, sel: string): T {
  const el = root.querySelector<T>(sel);
  if (!el) throw new Error(`no ${sel}`);
  return el;
}

function editor(html: string): HTMLElement {
  const root = document.createElement("div");
  root.innerHTML = html;
  return root;
}

describe("approvalKeyFromLabel", () => {
  it("slugs a label, accents and all", () => {
    expect(approvalKeyFromLabel("Diseño UX")).toBe("diseno_ux");
    expect(approvalKeyFromLabel("  QA / Release!  ")).toBe("qa_release");
  });
});

describe("onChangesOf", () => {
  it("defaults to resume, including for a value it does not know", () => {
    expect(onChangesOf({})).toBe("resume");
    expect(onChangesOf({ on_changes_requested: "explode" })).toBe("resume");
    expect(onChangesOf({ on_changes_requested: "stay" })).toBe("stay");
  });
});

describe("normalizeRequiredApprovals", () => {
  it("drops empty rows, fills the key and sorts the types", () => {
    const { rows, problem } = normalizeRequiredApprovals([
      { key: "", label: "" },
      { key: "", label: "Diseño", task_types: ["feature", "bugfix", "feature"] },
      { key: "qa", label: "QA", reset_on_change: false },
    ]);
    expect(problem).toBeNull();
    expect(rows).toEqual([
      { key: "diseno", label: "Diseño", task_types: ["bugfix", "feature"], reset_on_change: true },
      { key: "qa", label: "QA", task_types: [], reset_on_change: false },
    ]);
  });

  it("refuses a row without a label, a bad key, and a repeated key", () => {
    expect(normalizeRequiredApprovals([{ key: "qa", label: " " }]).problem).toContain("qa");
    expect(normalizeRequiredApprovals([{ key: "Q-A", label: "QA" }]).problem).toContain("Q-A");
    expect(
      normalizeRequiredApprovals([
        { key: "qa", label: "QA" },
        { key: "", label: "qa" },
      ]).problem,
    ).toContain('"qa"');
  });
});

describe("approvals editor", () => {
  const stage = {
    required_approvals: [
      { key: "design", label: "Design", task_types: ["feature", "spike"], reset_on_change: true },
    ],
    on_changes_requested: "kick_back",
    changes_kick_back_to: "in_progress",
  };

  it("round-trips what it was given", () => {
    const root = editor(approvalsEditorHTML(stage, types, earlier));
    expect(approvalsPatch(root)).toEqual({
      required_approvals: [
        { key: "design", label: "Design", task_types: ["feature", "spike"], reset_on_change: true },
      ],
      on_changes_requested: "kick_back",
      changes_kick_back_to: "in_progress",
    });
  });

  it("forgets the kick-back target unless the mode uses it", () => {
    const root = editor(approvalsEditorHTML(stage, types, earlier));
    find<HTMLSelectElement>(root, '[data-ra="on_changes"]').value = "stay";
    expect(approvalsPatch(root)).toMatchObject({
      on_changes_requested: "stay",
      changes_kick_back_to: "",
    });
  });

  it("hides the target while the mode does not kick back", () => {
    const root = editor(approvalsEditorHTML({}, types, earlier));
    expect(find<HTMLElement>(root, "[data-ra-kick]").hidden).toBe(true);
    expect(approvalsPatch(root)).toEqual({
      required_approvals: [],
      on_changes_requested: "resume",
      changes_kick_back_to: "",
    });
  });

  it("reports a problem instead of a patch", () => {
    const root = editor(
      approvalsEditorHTML({ required_approvals: [{ key: "x y", label: "X" }] }, types, earlier),
    );
    expect(approvalsPatch(root)).toHaveProperty("problem");
  });

  it("escapes labels", () => {
    const html = approvalsEditorHTML(
      { required_approvals: [{ key: "a", label: '"><script>' }] },
      types,
      earlier,
    );
    expect(html).not.toContain("<script>");
  });
});

describe("approvalRowEl", () => {
  it("is the row approvalRowHTML writes, ready to append", () => {
    const row = approvalRowEl({ key: "", label: "" }, types);
    expect(row.matches("[data-ra-row]")).toBe(true);
    expect(row.querySelectorAll("[data-ra-type]")).toHaveLength(types.length);
  });

  it("is read back with the rows the editor drew", () => {
    const root = editor(
      approvalsEditorHTML(
        { required_approvals: [{ key: "design", label: "Design" }] },
        types,
        earlier,
      ),
    );
    const row = approvalRowEl({ key: "", label: "" }, types);
    find<HTMLElement>(root, "[data-ra-rows]").append(row);
    find<HTMLInputElement>(row, '[data-ra="label"]').value = "Legal";
    find<HTMLInputElement>(row, '[data-ra-type="bugfix"]').checked = true;
    expect(approvalsPatch(root)).toMatchObject({
      required_approvals: [
        { key: "design", label: "Design" },
        { key: "legal", label: "Legal", task_types: ["bugfix"] },
      ],
    });
  });

  it("keeps a label as text", () => {
    const row = approvalRowEl({ key: "a", label: '"><img src=x onerror=alert(1)>' }, types);
    expect(row.querySelector("img")).toBeNull();
    expect(find<HTMLInputElement>(row, '[data-ra="label"]').value).toBe(
      '"><img src=x onerror=alert(1)>',
    );
  });
});

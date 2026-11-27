import { describe, expect, it } from "vitest";
import {
  briefOutline,
  checklistCount,
  filledLines,
  matchSubtask,
  sectionAuthors,
  splitBrief,
  unwrapMarkdownFence,
} from "./brief";

describe("splitBrief", () => {
  it("splits the preamble from the level-two sections", () => {
    const b = splitBrief("Intro line\n\n## Plan\n\n- [ ] one\n\n## Acceptance criteria\nok\n");
    expect(b.preamble).toBe("Intro line");
    expect(b.sections.map((s) => [s.title, s.slug, s.source, s.lines])).toEqual([
      ["Plan", "plan", "- [ ] one", 1],
      ["Acceptance criteria", "acceptance-criteria", "ok", 1],
    ]);
  });

  it("keeps a heading inside a code block as code", () => {
    const b = splitBrief("## Notes\n```\n## not a heading\n```");
    expect(b.sections).toHaveLength(1);
    expect(b.sections[0].source).toContain("## not a heading");
  });

  it("returns an empty brief for an empty body", () => {
    expect(splitBrief("")).toEqual({ preamble: "", sections: [] });
  });

  it("strips closing hashes from a heading", () => {
    expect(splitBrief("## Plan ##").sections[0].title).toBe("Plan");
  });
});

describe("checklistCount", () => {
  it("counts done and total, skipping code blocks", () => {
    const src = "- [x] a\n- [ ] b\n* [X] c\n```\n- [ ] code\n```\n- plain";
    expect(checklistCount(src)).toEqual({ done: 2, total: 3 });
  });
});

describe("matchSubtask", () => {
  const subs = [
    { id: "1", title: "Gift card field in the cart drawer" },
    { id: "2", title: "Schema for gift cards" },
    { id: "3", title: "Docs" },
  ];

  it("matches an exact title, ignoring case and spaces", () => {
    expect(matchSubtask("  docs ", subs)?.id).toBe("3");
  });

  it("matches when every significant word is in exactly one title", () => {
    expect(matchSubtask("Cart field", subs)?.id).toBe("1");
    expect(matchSubtask("the schema", subs)?.id).toBe("2");
  });

  it("is null for no match or more than one", () => {
    expect(matchSubtask("gift", subs)).toBeNull();
    expect(matchSubtask("payments", subs)).toBeNull();
    expect(matchSubtask("", subs)).toBeNull();
    expect(matchSubtask("the", subs)).toBeNull();
  });
});

describe("sectionAuthors", () => {
  const isAgent = (n: string) => n === "planner" || n === "qa";

  it("keeps the latest section_updated per section, and only agents", () => {
    const m = sectionAuthors(
      [
        {
          kind: "section_updated",
          actor: "planner",
          ref_id: "plan",
          created_at: "2026-10-01T10:00:00Z",
        },
        {
          kind: "section_updated",
          actor: "qa",
          ref_id: "plan",
          created_at: "2026-10-01T11:00:00Z",
        },
        {
          kind: "section_updated",
          actor: "ana",
          ref_id: "notes",
          created_at: "2026-10-01T11:00:00Z",
        },
        { kind: "comment", actor: "planner", ref_id: "notes", created_at: "2026-10-01T12:00:00Z" },
        { kind: "section_updated", ref_id: "risks", created_at: "2026-10-01T12:00:00Z" },
      ],
      isAgent,
    );
    expect([...m.entries()]).toEqual([["plan", { agent: "qa", at: "2026-10-01T11:00:00Z" }]]);
  });

  it("drops a section a person edited after the agent", () => {
    const m = sectionAuthors(
      [
        {
          kind: "section_updated",
          actor: "planner",
          ref_id: "plan",
          created_at: "2026-10-01T10:00:00Z",
        },
        {
          kind: "section_updated",
          actor: "ana",
          ref_id: "plan",
          created_at: "2026-10-01T11:00:00Z",
        },
      ],
      isAgent,
    );
    expect(m.size).toBe(0);
  });
});

describe("unwrapMarkdownFence", () => {
  it("unwraps a body that is one md or markdown fence", () => {
    expect(unwrapMarkdownFence("```md\n## Goal\n\n- one\n```\n")).toEqual({
      text: "## Goal\n\n- one\n",
      wrapped: true,
    });
    expect(unwrapMarkdownFence("\n~~~~ Markdown\n# T\n~~~~\n\n").text).toBe("# T\n");
    expect(unwrapMarkdownFence("```md\r\n## Goal\r\n```\r\n").text).toBe("## Goal\n");
    expect(unwrapMarkdownFence("````md\na\n```\nb\n````").text).toBe("a\n```\nb\n");
  });

  it("leaves code, text around the fence and plain bodies alone", () => {
    for (const body of [
      "```\n## Goal\n```\n",
      "```go\nfunc main() {}\n```\n",
      "```md\n## Goal\n```\nmore\n",
      "intro\n```md\n## Goal\n```\n",
      "```md\na\n```\n\n```md\nb\n```\n",
      "```md\n## Goal\n",
      "## Goal\n- one\n",
      "",
    ]) {
      expect(unwrapMarkdownFence(body)).toEqual({ text: body, wrapped: false });
    }
  });
});

describe("briefOutline", () => {
  it("opens the text before the first heading and folds every section", () => {
    const o = briefOutline(splitBrief("Intro line\n\n## Context\nA\n## Risks\nB"));
    expect(o.lead).toBe("Intro line");
    expect(o.leadSection).toBeNull();
    expect(o.sections.map((s) => s.slug)).toEqual(["context", "risks"]);
  });

  it("opens a leading Goal section without its heading", () => {
    const o = briefOutline(splitBrief("## Goal\nShip it\n\n## Context\nA"));
    expect(o.lead).toBe("Ship it");
    expect(o.leadSection?.slug).toBe("goal");
    expect(o.sections.map((s) => s.slug)).toEqual(["context"]);
  });

  it("folds a first section that is not a goal", () => {
    const o = briefOutline(splitBrief("## Context\nA\n## Goal\nB"));
    expect(o.lead).toBe("");
    expect(o.sections).toHaveLength(2);
  });
});

describe("filledLines", () => {
  it("counts the lines with text", () => {
    expect(filledLines("a\n\n  \nb\r\nc")).toBe(3);
    expect(filledLines("")).toBe(0);
  });
});

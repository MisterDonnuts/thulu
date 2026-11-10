import { describe, expect, it } from "vitest";
import { reportStageLabel, reportView, splitFrontmatter } from "./reports";

const label = (id: string) => ({ in_progress: "Development" })[id] ?? id;

describe("splitFrontmatter", () => {
  it("takes the YAML block off the body", () => {
    const { meta, body } = splitFrontmatter(
      "---\nverdict: kick_back\nkick_back_to: in_progress   # only for kick_back\nscore: 3/10\n---\n\n# Notes\nno.\n",
    );
    expect(meta).toEqual({ verdict: "kick_back", kick_back_to: "in_progress", score: "3/10" });
    expect(body).toBe("# Notes\nno.\n");
  });

  it("leaves a body without one alone", () => {
    expect(splitFrontmatter("# Notes").body).toBe("# Notes");
  });
});

describe("reportView", () => {
  it("titles the auditor's feedback by its verdict, in words", () => {
    const v = reportView(
      {
        source: "auditor",
        kind: "auditor_feedback",
        body: "---\nverdict: kick_back\nkick_back_to: in_progress\nscore: 3/10\n---\nRedo it.",
      },
      label,
    );
    expect(v.title).toBe("Verdict: send back to Development");
    expect(v.source).toBe("Auditor");
    expect(v.body).toBe("Redo it.");
    expect(v.score).toBe("3/10");
  });

  it("falls back to what the report is, and translates who filed it", () => {
    expect(
      reportView({ source: "auditor", kind: "auditor_feedback", body: "x" }, label).title,
    ).toBe("Auditor feedback");
    const agent = reportView({ source: "agent", kind: "agent_index", body: "x" }, label);
    expect(agent.title).toBe("Run summary");
    expect(agent.source).toBe("Agent");
    expect(
      reportView({ source: "agent", kind: "evidence", title: "✅ SHOP-142", body: "" }, label)
        .title,
    ).toBe("SHOP-142");
  });
});

describe("reportStageLabel", () => {
  it("names the board's column, the mention a run answered, or the bare id", () => {
    const board = (id: string) => ({ in_progress: "Development" })[id];
    expect(reportStageLabel("in_progress", board)).toBe("Development");
    expect(reportStageLabel("mention", board)).toBe("Mention");
    expect(reportStageLabel("gone", board)).toBe("gone");
  });
});

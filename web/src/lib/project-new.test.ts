import { describe, expect, it } from "vitest";

import {
  createWorkspace,
  decodeDraft,
  draftHasContent,
  DRAFT_KEY,
  newProjectContext,
  newProjectFromTaskURL,
  otherDefaultBoard,
  stashDraft,
  takeDraft,
  withWorkspace,
  type NewProjectDraft,
} from "./project-new";

describe("createWorkspace", () => {
  it("leaves it to the daemon when the session has a workspace", () => {
    expect(createWorkspace({ workspace_id: "ws1" }, [{ default: true, workspace_id: "ws2" }])).toBe(
      "",
    );
  });
  it("takes the default board's workspace for a session without one", () => {
    expect(
      createWorkspace(null, [{ workspace_id: "a" }, { default: true, workspace_id: "b" }]),
    ).toBe("b");
  });
  it("takes the workspace every board shares", () => {
    expect(createWorkspace({}, [{ workspace_id: "a" }, { workspace_id: "a" }])).toBe("a");
  });
  it("names none when the boards disagree", () => {
    expect(createWorkspace({}, [{ workspace_id: "a" }, { workspace_id: "b" }])).toBe("");
    expect(createWorkspace({}, [])).toBe("");
  });
});

describe("withWorkspace", () => {
  it("adds the param only when there is a workspace", () => {
    expect(withWorkspace("/projects", "")).toBe("/projects");
    expect(withWorkspace("/projects", "w s")).toBe("/projects?workspace=w%20s");
    expect(withWorkspace("/projects/upload?x=1", "w")).toBe("/projects/upload?x=1&workspace=w");
  });
});

function memStorage(): Storage & { data: Map<string, string> } {
  const data = new Map<string, string>();
  return {
    data,
    get length() {
      return data.size;
    },
    clear: () => data.clear(),
    getItem: (k) => data.get(k) ?? null,
    key: (i) => [...data.keys()][i] ?? null,
    removeItem: (k) => void data.delete(k),
    setItem: (k, v) => void data.set(k, v),
  };
}

const draft: NewProjectDraft = {
  name: "api",
  repo: "https://github.com/o/api.git",
  branch: "main",
  description: "the backend",
  board: "default",
  template: { dev: "developer" },
  task: "578dfc",
};

describe("draft", () => {
  it("round-trips once", () => {
    const s = memStorage();
    stashDraft(s, draft);
    expect(takeDraft(s)).toEqual(draft);
    expect(s.data.has(DRAFT_KEY)).toBe(false);
    expect(takeDraft(s)).toBeNull();
  });
  it("survives storage that throws", () => {
    const broken = {
      setItem: () => {
        throw new Error("quota");
      },
      getItem: () => {
        throw new Error("blocked");
      },
      removeItem: () => undefined,
    };
    expect(() => stashDraft(broken, draft)).not.toThrow();
    expect(takeDraft(broken)).toBeNull();
    expect(takeDraft(null)).toBeNull();
  });
  it("drops what is not a draft field", () => {
    const d = decodeDraft(
      JSON.stringify({ name: 3, repo: "r", template: { a: "x", b: 1 }, task: "../x" }),
    );
    expect(d).toEqual({
      name: "",
      repo: "r",
      branch: "",
      description: "",
      board: "",
      template: { a: "x" },
      task: "",
    });
    expect(decodeDraft("not json")).toBeNull();
    expect(decodeDraft("null")).toBeNull();
  });
  it("tells an empty draft", () => {
    expect(draftHasContent(draft)).toBe(true);
    expect(
      draftHasContent({ ...draft, name: "", repo: "", description: "", board: "", template: {} }),
    ).toBe(false);
  });
});

describe("new project from a task", () => {
  it("carries the name and the task", () => {
    const url = newProjectFromTaskURL("my api", "578dfc");
    expect(url).toBe("/projects/?new=1&name=my+api&task=578dfc");
    expect(newProjectContext(url.slice(url.indexOf("?")))).toEqual({
      name: "my api",
      task: "578dfc",
    });
  });
  it("refuses a task id that is no id", () => {
    expect(newProjectFromTaskURL("x", "../../x")).toBe("/projects/?new=1&name=x");
    expect(newProjectContext("?name=x&task=a/b")).toEqual({ name: "x", task: "" });
  });
});

describe("otherDefaultBoard", () => {
  it("names the board the picked projects agree on", () => {
    expect(otherDefaultBoard([{ default_board_id: "ops" }, {}], "dev")).toBe("ops");
  });
  it("says nothing for the open board, no board, or a disagreement", () => {
    expect(otherDefaultBoard([{ default_board_id: "dev" }], "dev")).toBeNull();
    expect(otherDefaultBoard([{}, { default_board_id: "" }], "dev")).toBeNull();
    expect(
      otherDefaultBoard([{ default_board_id: "a" }, { default_board_id: "b" }], "dev"),
    ).toBeNull();
    expect(otherDefaultBoard([], "dev")).toBeNull();
  });
});

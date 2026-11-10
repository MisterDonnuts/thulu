// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import {
  blockedByHTML,
  blocksHTML,
  branchHTML,
  emptyValueHTML,
  projectChipsHTML,
  type Dep,
} from "./task-drawer";

const terminal = (s: string) => s === "done";

function q(root: ParentNode, sel: string): Element {
  const el = root.querySelector(sel);
  if (!el) throw new Error(`no ${sel}`);
  return el;
}

describe("blockedByHTML", () => {
  it("says None without dependencies", () => {
    expect(blockedByHTML([], terminal)).toContain("None");
    expect(blockedByHTML(undefined, terminal)).toContain("None");
  });

  it('puts hard blockers first, with a lock and "blocking" until they finish', () => {
    const deps: Dep[] = [
      { task_id: "s1", title: "Soft one", kind: "soft", stage: "dev" },
      { task_id: "h1", title: "Schema", kind: "hard", stage: "dev" },
      { task_id: "h2", title: "Docs", kind: "hard", stage: "done" },
    ];
    const box = document.createElement("div");
    box.innerHTML = blockedByHTML(deps, terminal);
    const ids = [...box.querySelectorAll<HTMLElement>("[data-dep-id]")].map((e) => e.dataset.depId);
    expect(ids).toEqual(["h1", "h2", "s1"]);
    const unmet = q(box, ".dep-unmet");
    expect(q(unmet, ".row-title").textContent).toBe("Schema");
    expect(q(unmet, ".badge.badge-warn").textContent).toBe("blocking");
    expect(q(unmet, "use").getAttribute("href")).toBe("#i-lock");
    const met = q(box, ".dep-met");
    expect(met.textContent).not.toContain("blocking");
    expect(q(met, "use").getAttribute("href")).toBe("#i-check");
    expect(q(box, ".dep-soft").textContent).toContain("heads-up");
  });

  it("escapes titles and falls back to a short id", () => {
    const html = blockedByHTML([{ task_id: "abcdef123456", kind: "hard", stage: "dev" }], terminal);
    expect(html).not.toContain("undefined");
    expect(
      blockedByHTML([{ task_id: "x", title: "<i>", kind: "hard", stage: "dev" }], terminal),
    ).toContain("&lt;i&gt;");
  });
});

describe("blocksHTML", () => {
  it("lists the dependents, soft ones with their badge", () => {
    const html = blocksHTML([
      { task_id: "a", title: "Front", kind: "hard", stage: "dev" },
      { task_id: "b", title: "Later", kind: "soft", stage: "dev" },
    ]);
    const box = document.createElement("div");
    box.innerHTML = html;
    expect(box.querySelectorAll(".row-list > .row.row-sm.dep")).toHaveLength(2);
    expect(q(box, '[data-dep-id="a"]').classList.contains("dep-plain")).toBe(true);
    expect(q(box, '[data-dep-id="a"]').querySelector(".badge")).toBeNull();
    const soft = q(box, '[data-dep-id="b"]');
    expect(soft.classList.contains("dep-soft")).toBe(true);
    expect(q(soft, ".badge.badge-muted.badge-dashed").textContent).toBe("heads-up");
    expect(blocksHTML([])).toContain("None");
  });

  it("gives hard and soft dependents a lead, so their titles line up", () => {
    const box = document.createElement("div");
    box.innerHTML = blocksHTML([
      { task_id: "a", title: "Front", kind: "hard", stage: "dev" },
      { task_id: "b", title: "Later", kind: "soft", stage: "dev" },
    ]);
    const rows = [...box.querySelectorAll<HTMLElement>(".dep")];
    expect(rows).toHaveLength(2);
    for (const row of rows) expect(row.querySelector(".row-lead .dep-ico svg")).not.toBeNull();
    expect(q(box, ".dep-plain").querySelector("use")?.getAttribute("href")).toContain(
      "i-arrow-right",
    );
  });
});

describe("branchHTML", () => {
  it("shows the branch with a copy button, and Force PR without a pull request", () => {
    const html = branchHTML({
      branch: "octopus/t1",
      prCount: 0,
      shared: false,
      pr: true,
      merge: false,
    });
    expect(html).toContain('data-copy="octopus/t1"');
    expect(html).toContain("data-force-pr");
    expect(
      branchHTML({ branch: "octopus/t1", prCount: 1, shared: false, pr: true, merge: false }),
    ).not.toContain("data-force-pr");
  });

  it("says a subtask shares the parent's branch and offers no Force PR", () => {
    const html = branchHTML({
      branch: "octopus/t1",
      prCount: 0,
      shared: true,
      pr: true,
      merge: true,
    });
    expect(html).toContain("Shared with the parent");
    expect(html).not.toContain("data-force-pr");
    expect(html).not.toContain("data-merge");
  });

  it("offers Merge into for an uploaded project and Force PR only for a GitHub one", () => {
    const html = branchHTML({
      branch: "octopus/t1",
      prCount: 0,
      shared: false,
      pr: false,
      merge: true,
    });
    expect(html).toContain("data-merge");
    expect(html).not.toContain("data-force-pr");
  });

  it("says so when there is no branch yet", () => {
    const html = branchHTML({ branch: "", prCount: 0, shared: false, pr: true, merge: false });
    expect(html).toContain("No branch yet");
    expect(html).not.toContain("data-force-pr");
  });
});

describe("projectChipsHTML", () => {
  const name = (id: string) => ({ p1: "octopus", p2: "atlas" })[id] ?? id;

  it("links each project to its page", () => {
    const html = projectChipsHTML(["p1", "p2"], name);
    expect(html).toContain('href="/project/?id=p1"');
    expect(html).toContain('<span class="tag-label">octopus</span></a>');
    expect(html).toContain('<span class="tag-label">atlas</span></a>');
    expect(html).toMatch(
      /^<a class="tag" href="\/project\/\?id=p1" title="octopus"><span class="tag-dot tag-dot-\w+"/,
    );
  });

  it("says so when none is linked", () => {
    expect(projectChipsHTML([], name)).toBe(
      '<span class="nprop-value-empty">No linked projects</span>',
    );
  });
});

describe("emptyValueHTML", () => {
  it("writes the quiet empty word, escaped", () => {
    expect(emptyValueHTML("<none>")).toBe('<span class="nprop-value-empty">&lt;none&gt;</span>');
  });
});

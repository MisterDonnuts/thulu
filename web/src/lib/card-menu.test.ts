import { describe, expect, it } from "vitest";
import {
  cardMenuHTML,
  cardMenuItems,
  menuPosition,
  priorityChoicesHTML,
  stageChoicesHTML,
} from "./card-menu";

describe("cardMenuItems", () => {
  it("lists every action in order with its key hint", () => {
    const items = cardMenuItems({ childCount: 0, status: "out_of_turns" });
    expect(items.map((i) => i.act)).toEqual([
      "open",
      "move",
      "priority",
      "add-subtask",
      "copy-link",
      "clone",
      "fork",
      "more-turns",
      "archive",
      "delete",
    ]);
    expect(items.map((i) => i.kbd)).toEqual([
      "Enter",
      "M",
      "P",
      "N",
      "L",
      "C",
      "F",
      "T",
      "A",
      "Del",
    ]);
  });

  it("offers More turns only after a run ran out of them", () => {
    const acts = (status?: string) => cardMenuItems({ childCount: 0, status }).map((i) => i.act);
    expect(acts("needs_more_turns")).toContain("more-turns");
    expect(acts(undefined)).not.toContain("more-turns");
    expect(acts("idle")).not.toContain("more-turns");
    expect(acts("blocked")).not.toContain("more-turns");
  });

  it("leaves Add subtask out on a subtask", () => {
    expect(cardMenuItems({ parent_id: "p", childCount: 0 }).map((i) => i.act)).not.toContain(
      "add-subtask",
    );
  });

  it("leaves Archive out on a subtask, which goes with its parent", () => {
    expect(cardMenuItems({ parent_id: "p", childCount: 0 }).map((i) => i.act)).not.toContain(
      "archive",
    );
  });

  it("says a parent's subtasks go along with Delete and Archive", () => {
    const items = cardMenuItems({ childCount: 2 });
    const del = items.find((i) => i.act === "delete");
    expect(del?.disabled).toBe(false);
    expect(del?.sub).toBe("Also deletes its 2 subtasks");
    expect(items.find((i) => i.act === "archive")?.sub).toBe("Also archives its 2 subtasks");
  });

  it("offers Restore on an archived task", () => {
    const arch = cardMenuItems({ childCount: 0, archived: true }).find((i) => i.act === "archive");
    expect(arch?.label).toBe("Restore to the board");
    expect(arch?.sub).toBeUndefined();
  });

  it("offers no Move to on an archived task, which is off the board", () => {
    expect(cardMenuItems({ childCount: 0, archived: true }).map((i) => i.act)).not.toContain(
      "move",
    );
    expect(cardMenuItems({ childCount: 0 }).map((i) => i.act)).toContain("move");
  });

  it("disables Delete for someone who is not an admin and says why", () => {
    const del = cardMenuItems({ childCount: 0, canDelete: false }).find((i) => i.act === "delete");
    expect(del?.disabled).toBe(true);
    expect(del?.sub).toBe("Only an admin can delete tasks");
  });

  it("disables every item that changes the task for a viewer, saying why once", () => {
    const items = cardMenuItems({ childCount: 2, canWrite: false, canDelete: false });
    const enabled = items.filter((i) => !i.disabled).map((i) => i.act);
    expect(enabled).toEqual(["open", "copy-link"]);
    const subs = items.filter((i) => i.sub).map((i) => [i.act, i.sub]);
    expect(subs).toEqual([["move", "Your role can only read this workspace"]]);
  });
});

describe("markup", () => {
  it("marks danger and disabled items and separates groups", () => {
    const html = cardMenuHTML(cardMenuItems({ childCount: 1, canDelete: false }));
    expect(html.match(/class="menu-sep"/g)).toHaveLength(2);
    expect(
      cardMenuHTML(cardMenuItems({ parent_id: "p", childCount: 0 })).match(/class="menu-sep"/g),
    ).toHaveLength(2);
    expect(html).toMatch(/class="menu-item menu-item-danger cm-item"[^>]*data-cm="delete"/);
    expect(html).toMatch(/data-key="delete"[^>]*data-cm="delete"/);
    expect(html).toContain('aria-disabled="true"');
    expect(html).toMatch(/<kbd class="kbd-sm menu-kbd"[^>]*>M<\/kbd>/);
  });

  it("checks the current stage and drops a bad colour", () => {
    const html = stageChoicesHTML(
      [
        { id: "a", label: "Backlog", color: "#5d6477" },
        { id: "b", label: "Ready", color: "url(x)" },
      ],
      "a",
    );
    expect(html).toMatch(/role="menuitemradio" aria-checked="true"[^>]*data-cm-stage="a"/);
    expect(html).toMatch(/role="menuitemradio" aria-checked="false"[^>]*data-cm-stage="b"/);
    expect(html).toContain('class="menu-head"');
    expect(html).toContain("--dot-color:#5d6477");
    expect(html).not.toContain("url(x)");
    expect(html).toContain("data-cm-back");
  });

  it("leaves out Back when the choices open on their own", () => {
    const html = stageChoicesHTML([{ id: "a", label: "Backlog", color: "" }], "a", { back: false });
    expect(html).toContain('class="menu-head"');
    expect(html).not.toContain("data-cm-back");
  });

  it("lists the priorities with their icons and None", () => {
    const html = priorityChoicesHTML("high");
    expect(html).toMatch(/aria-checked="true"[^>]*data-cm-priority="high"/);
    expect(html).toContain("prio-urgent");
    expect(html).toContain(">None<");
  });
});

describe("menuPosition", () => {
  const vp = { width: 1000, height: 800 };
  const menu = { width: 200, height: 300 };

  it("opens under the anchor, aligned to its right edge", () => {
    expect(menuPosition({ left: 500, top: 100, right: 528, bottom: 128 }, menu, vp)).toEqual({
      left: 328,
      top: 132,
    });
  });

  it("flips up near the bottom and stays inside the viewport", () => {
    const at = menuPosition({ left: 10, top: 700, right: 38, bottom: 728 }, menu, vp);
    expect(at.top).toBe(396);
    expect(at.left).toBe(10);
  });
});

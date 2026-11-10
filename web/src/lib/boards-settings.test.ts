import { describe, expect, it, vi } from "vitest";

vi.mock("astro:transitions/client", () => ({ navigate: vi.fn() }));

import { boardMenuEntries } from "./boards-settings";
import { isMenuItem, isMenuSep, menuItemsHTML } from "./ui";

const acts = (entries: ReturnType<typeof boardMenuEntries>) =>
  entries.filter(isMenuItem).map((i) => i.attrs?.["data-act"]);

describe("boardMenuEntries", () => {
  it("offers every action on a board an editor may change, Edit only from the list", () => {
    const list = boardMenuEntries({ default: false }, { canEdit: true, inDetail: false });
    expect(acts(list)).toEqual(["open", "edit", "default", "duplicate", "delete"]);
    expect(list.some(isMenuSep)).toBe(true);
    const detail = boardMenuEntries({ default: false }, { canEdit: true, inDetail: true });
    expect(acts(detail)).toEqual(["open", "default", "duplicate", "delete"]);
    expect(detail.filter(isMenuItem).every((i) => !i.disabled)).toBe(true);
  });

  it("turns Make default and Delete off on the default board, saying why under Delete", () => {
    const items = boardMenuEntries({ default: true }, { canEdit: true, inDetail: false }).filter(
      isMenuItem,
    );
    const by = (act: string) => items.find((i) => i.attrs?.["data-act"] === act);
    expect(by("default")?.disabled).toBe(true);
    expect(by("delete")?.disabled).toBe(true);
    expect(by("delete")?.danger).toBe(true);
    expect(by("delete")?.sub).toBeTruthy();
    const html = menuItemsHTML(items);
    expect(html).toContain('data-act="delete"');
    expect(html.match(/aria-disabled="true"/g)).toHaveLength(2);
  });

  it("only opens the board for a session that may not edit boards", () => {
    expect(acts(boardMenuEntries({ default: true }, { canEdit: false, inDetail: false }))).toEqual([
      "open",
    ]);
  });
});

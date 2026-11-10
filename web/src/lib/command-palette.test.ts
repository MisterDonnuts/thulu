import { describe, expect, it } from "vitest";

import {
  agentItems,
  boardItems,
  fold,
  pageItems,
  PALETTE_TTL_MS,
  paletteStale,
  projectItems,
  rank,
  searchPalette,
  taskChangeStalesPalette,
  taskItems,
  type PaletteItem,
} from "./command-palette";

const all = { login: true, operator: true };

describe("pageItems", () => {
  it("offers every app page and each settings section on its own", () => {
    const hrefs = pageItems(all).map((p) => p.href);
    expect(hrefs).toContain("/inbox/");
    expect(hrefs).toContain("/settings/#webhooks");
    expect(hrefs).toContain("/infra/");
    for (const h of hrefs) expect(h.startsWith("/")).toBe(true);
  });

  it("leaves out what this caller cannot open", () => {
    const hrefs = pageItems({ login: false, operator: false }).map((p) => p.href);
    expect(hrefs).not.toContain("/infra/");
    expect(hrefs).not.toContain("/settings/#team");
    expect(hrefs).not.toContain("/settings/#account");
  });
});

describe("taskItems", () => {
  const boards = new Map([
    ["web", "Web shop"],
    ["ops", "Ops"],
  ]);

  it("sorts newest first and links each task on its board", () => {
    const items = taskItems(
      [
        { id: "t1", title: "Old", board_id: "web", updated_at: "2026-01-01T00:00:00Z" },
        { id: "t2", title: "New", board_id: "ops", updated_at: "2026-02-01T00:00:00Z" },
      ],
      boards,
    );
    expect(items.map((i) => i.id)).toEqual(["t2", "t1"]);
    expect(items[0]).toMatchObject({ detail: "Ops", href: "/tasks/?board=ops#task-t2" });
  });

  it("names the board only when there is more than one", () => {
    const [one] = taskItems([{ id: "t1", title: "x", board_id: "web" }], new Map([["web", "W"]]));
    expect(one.detail).toBeUndefined();
  });
});

describe("rank", () => {
  const item = (label: string, extra: Partial<PaletteItem> = {}): PaletteItem => ({
    kind: "page",
    id: label,
    label,
    href: "/",
    ...extra,
  });

  it("ignores case and accents", () => {
    expect(fold("Configuración")).toBe("configuracion");
    expect(rank("configuracion", item("Configuración"))).toBe(0);
  });

  it("needs every word, wherever it is", () => {
    expect(rank("fix login", item("Fix the login form"))).toBeGreaterThanOrEqual(0);
    expect(rank("fix signup", item("Fix the login form"))).toBe(-1);
    expect(rank("ops", item("Deploy", { detail: "Ops" }))).toBe(3);
  });

  it("finds a task by its id", () => {
    expect(rank("a1b2", item("Some task", { terms: ["a1b2c3d4"] }))).toBe(3);
  });

  it("prefers a label that starts with the query", () => {
    expect(rank("pro", item("Projects"))).toBeLessThan(rank("pro", item("My projects")));
    expect(rank("pro", item("My projects"))).toBeLessThan(rank("pro", item("Approvals")));
  });
});

describe("searchPalette", () => {
  const items: PaletteItem[] = [
    ...pageItems(all),
    ...taskItems(
      Array.from({ length: 12 }, (_, i) => ({
        id: `t${i}`,
        title: `Task ${i} checkout`,
        updated_at: `2026-01-${String(i + 1).padStart(2, "0")}T00:00:00Z`,
      })),
      new Map(),
    ),
    ...projectItems([{ id: "p1", name: "checkout-api", repo_url: "https://git/x" }]),
    ...agentItems([{ name: "frontend", description: "Builds the checkout UI" }]),
    ...boardItems([{ id: "b1", label: "Checkout" }]),
  ];

  it("starts from the latest tasks and the app's pages", () => {
    const groups = searchPalette("", items);
    expect(groups.map((g) => g.kind)).toEqual(["task", "page"]);
    expect(groups[0].items.map((i) => i.id)).toEqual(["t11", "t10", "t9", "t8", "t7"]);
    expect(groups[1].items.every((i) => !i.href.startsWith("/settings/#"))).toBe(true);
  });

  it("groups matches by kind, best first, within each group's limit", () => {
    const groups = searchPalette("checkout", items);
    expect(groups.map((g) => g.kind)).toEqual(["project", "board", "task", "agent"]);
    expect(groups[0].items[0].label).toBe("checkout-api");
    expect(groups[2].items).toHaveLength(8);
  });

  it("puts a board named by the query ahead of tasks that only mention it", () => {
    const boards = new Map([
      ["main", "Principal"],
      ["ops", "Operaciones"],
    ]);
    const found = searchPalette("operac", [
      ...pageItems(all),
      ...taskItems(
        [{ id: "t1", title: "Rotar certificados del balanceador", board_id: "ops" }],
        boards,
      ),
      ...boardItems([
        { id: "main", label: "Principal" },
        { id: "ops", label: "Operaciones" },
      ]),
    ]);
    expect(found.map((g) => g.kind)).toEqual(["board", "task"]);
    expect(found[0].items[0].id).toBe("ops");
  });

  it("keeps the kinds' order between groups that match equally well", () => {
    const found = searchPalette("front", [
      ...agentItems([{ name: "frontend" }]),
      ...boardItems([{ id: "fb", label: "Frontend" }]),
    ]);
    expect(found.map((g) => g.kind)).toEqual(["agent", "board"]);
  });

  it("returns nothing when nothing matches", () => {
    expect(searchPalette("zzzz", items)).toEqual([]);
  });
});

describe("paletteStale", () => {
  it("reads the lists on the first open", () => {
    expect(paletteStale(null, 1_000, 0)).toBe(true);
  });

  it("reuses them within the window and reads them again after it", () => {
    const read = { at: 10_000, writes: 2 };
    expect(paletteStale(read, 10_000 + PALETTE_TTL_MS - 1, 2)).toBe(false);
    expect(paletteStale(read, 10_000 + PALETTE_TTL_MS, 2)).toBe(true);
  });

  it("reads them again right after the page writes something", () => {
    expect(paletteStale({ at: 10_000, writes: 2 }, 10_001, 3)).toBe(true);
  });
});

describe("taskChangeStalesPalette", () => {
  it("throws the lists away when a task appears or goes, not on every update", () => {
    expect(taskChangeStalesPalette("task_created")).toBe(true);
    expect(taskChangeStalesPalette("task_deleted")).toBe(true);
    expect(taskChangeStalesPalette("task_updated")).toBe(false);
    expect(taskChangeStalesPalette("reconnect")).toBe(false);
  });
});

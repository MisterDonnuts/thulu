import { experimental_AstroContainer as AstroContainer } from "astro/container";
import { beforeAll, describe, expect, it } from "vitest";

import { menuHTML, menuItemHTML, type MenuEntry, type MenuOptions } from "../lib/ui/menu";
import { pickerHTML, type PickerOptions } from "../lib/ui/picker";
import Menu from "./Menu.astro";
import MenuItem from "./MenuItem.astro";
import { normalize } from "./parity-normalize";
import Picker from "./Picker.astro";

let container: AstroContainer;
beforeAll(async () => {
  container = await AstroContainer.create();
});

describe("Menu.astro and menuHTML()", () => {
  const actions: MenuEntry[] = [
    {
      title: "Open",
      icon: "arrow-up-right",
      kbd: "Enter",
      key: "enter",
      attrs: { "data-cm": "open" },
    },
    { title: "Move left", indent: true, kbd: ["Alt", "←"], hint: "2 of 4" },
    { sep: true },
    { title: "Settings", icon: "settings", href: "/settings/", id: "go" },
    {
      title: "Delete",
      icon: "trash",
      sub: "Only an admin deletes tasks",
      subId: "del-sub",
      titleId: "del-title",
      danger: true,
      disabled: true,
      key: "delete",
      kbd: "Del",
      cls: "cm-item",
    },
    { sep: true, hidden: true },
    { note: "You can read this task, not change it.", id: "ro", hidden: true },
  ];
  const cases: Array<{
    name: string;
    entries: MenuEntry[];
    opts: MenuOptions;
    slots?: Record<string, string>;
  }> = [
    { name: "a compact menu of actions", entries: actions, opts: { id: "m", labelledBy: "btn" } },
    {
      name: "a rich floating menu at the start edge, open",
      entries: actions,
      opts: {
        label: "Task actions",
        density: "rich",
        align: "start",
        floating: true,
        open: true,
        cls: "x",
        attrs: { "data-menu": "task" },
      },
    },
    {
      name: "choices with dots, a head and a slot",
      entries: [
        { head: "Move to", sub: "Board one", id: "h" },
        { title: "Backlog", dot: "#5d6477", checked: false },
        { title: "Review", dot: "#bb86fc", checked: true, keepOpen: true },
        { title: "Labels on", role: "menuitemcheckbox", checked: true },
        { slot: "theme", html: '<div class="seg"><button type="button">Dark</button></div>' },
        { title: "Hidden", hidden: true },
      ],
      opts: { id: "mv", label: "Move to" },
      slots: { theme: '<div class="seg"><button type="button">Dark</button></div>' },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, ...props } = c.opts;
      const items = c.entries.map((e) => ("slot" in e ? { slot: e.slot } : e));
      const astro = await container.renderToString(Menu, {
        props: { ...props, items, class: cls, ...attrs },
        slots: c.slots ?? {},
      });
      expect(normalize(astro)).toBe(normalize(menuHTML(c.entries, c.opts)));
    });
  }

  it("refuses a menu without a name, like menuHTML", async () => {
    await expect(container.renderToString(Menu, { props: { items: [] } })).rejects.toThrow(/label/);
  });

  it("refuses a markup lead in its items: that is MenuItem's lead slot", async () => {
    await expect(
      container.renderToString(Menu, {
        props: { label: "x", items: [{ title: "x", leadHTML: "<i></i>" }] },
      }),
    ).rejects.toThrow(/lead slot/);
  });
});

describe("MenuItem.astro and menuItemHTML()", () => {
  it("takes its lead slot where menuItemHTML takes leadHTML", async () => {
    const lead =
      '<svg class="ico prio-urgent" aria-hidden="true"><use href="#i-alert"></use></svg>';
    const astro = await container.renderToString(MenuItem, {
      props: { title: "Urgent", checked: true, "data-cm-priority": "urgent" },
      slots: { lead },
    });
    expect(normalize(astro)).toBe(
      normalize(
        menuItemHTML({
          title: "Urgent",
          checked: true,
          leadHTML: lead,
          attrs: { "data-cm-priority": "urgent" },
        }),
      ),
    );
  });
});

describe("Picker.astro and pickerHTML()", () => {
  const cases: Array<{ name: string; opts: PickerOptions }> = [
    {
      name: "a single list without a search",
      opts: {
        id: "st",
        label: "Stage",
        active: 1,
        items: [
          { value: "backlog", label: "Backlog", dot: "#5d6477" },
          { value: "review", label: "Review", dot: "#bb86fc", selected: true, hint: "now" },
          { value: "done", label: "Done", icon: "check", disabled: true, attrs: { "data-x": "1" } },
        ],
      },
    },
    {
      name: "a multi picker with chips, search, hint and create, inline and wide",
      opts: {
        id: "tg",
        label: "Labels",
        place: "inline",
        width: "lg",
        multi: true,
        chips: true,
        search: "Add a label",
        query: "do",
        hint: "Enter adds it",
        create: "Create do",
        active: 1,
        cls: "x",
        attrs: { "data-prop": "tags" },
        items: [
          { value: "docs", label: "docs", selected: true, chip: { color: "blue" } },
          { value: "dot", label: "dot", icon: "tag" },
        ],
      },
    },
    {
      name: "a floating list at the start, over its row, narrow",
      opts: {
        id: "rp",
        label: "Repositories",
        place: "floating",
        start: true,
        up: true,
        width: "sm",
        search: "Find",
        items: [{ value: "web", label: "web" }],
      },
    },
    {
      name: "nothing left, with its own empty line",
      opts: { id: "ag", label: "Agents", items: [], empty: "No agent fits this stage" },
    },
    {
      name: "current values with an X, a kind beside the search, grouped options and an error line",
      opts: {
        id: "dp",
        label: "Dependencies",
        multi: true,
        search: "Find a task",
        current: {
          heading: "Blocked by",
          empty: "None yet",
          items: [{ value: "t1", label: "Login <form>", hint: "blocking, Review" }],
        },
        kind: {
          label: "Kind",
          value: "soft",
          options: [
            { value: "hard", label: "blocking", disabled: true },
            { value: "soft", label: "heads-up" },
          ],
        },
        hint: "Only soft past the first column",
        errors: true,
        items: [
          { value: "a", label: "A", hint: "Backlog", group: "Same parent" },
          { value: "b", label: "B", group: "Same parent" },
          { value: "c", label: "C", group: "Other tasks" },
        ],
      },
    },
    {
      name: "no current values yet",
      opts: {
        id: "dq",
        label: "Dependencies",
        current: { heading: "Blocked by", empty: "None yet", items: [] },
        items: [{ value: "a", label: "A" }],
      },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, ...props } = c.opts;
      const astro = await container.renderToString(Picker, {
        props: { ...props, class: cls, ...attrs },
      });
      expect(normalize(astro)).toBe(normalize(pickerHTML(c.opts)));
    });
  }

  it("refuses markup labels, leads and create rows: those are for pickerHTML", async () => {
    const base = { id: "x", label: "X" };
    for (const props of [
      { ...base, items: [{ value: "a", label: "a", labelHTML: "<b>a</b>" }] },
      { ...base, items: [{ value: "a", label: "a", leadHTML: "<i></i>" }] },
      { ...base, items: [], createHTML: "<b>new</b>" },
      {
        ...base,
        items: [],
        current: {
          heading: "h",
          empty: "e",
          items: [{ value: "a", label: "a", labelHTML: "<b>a</b>" }],
        },
      },
    ]) {
      await expect(container.renderToString(Picker, { props })).rejects.toThrow(/markup/);
    }
  });
});

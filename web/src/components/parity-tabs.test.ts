import { experimental_AstroContainer as AstroContainer } from "astro/container";
import { beforeAll, describe, expect, it } from "vitest";

import { tabsHTML, type TabsOptions } from "../lib/ui/tabs";
import { normalize } from "./parity-normalize";
import Tabs from "./Tabs.astro";

let container: AstroContainer;
beforeAll(async () => {
  container = await AstroContainer.create();
});

describe("Tabs.astro and tabsHTML()", () => {
  const cases: Array<{ name: string; ts: TabsOptions }> = [
    {
      name: "an underlined strip with panels, the second tab selected",
      ts: {
        label: "Infra",
        active: "containers",
        panels: true,
        items: [
          { id: "overview", label: "Overview" },
          { id: "containers", label: "Pods", labelId: "tab-containers-label", count: 12 },
          { id: "cache", label: "Cache", flag: "Needs a look", flagHidden: true },
        ],
      },
    },
    {
      name: "icons, a zero count and a disabled tab, nothing named active",
      ts: {
        label: "Task",
        id: "task-tabs",
        idPrefix: "d-",
        items: [
          { id: "overview", label: "Task", icon: "list" },
          { id: "approvals", label: "Approvals", icon: "check", count: 0 },
          { id: "reports", label: "Reports", icon: "file", disabled: true },
        ],
      },
    },
    {
      name: "pills with dots, one coloured by data, and extra classes",
      ts: {
        variant: "pill",
        label: "Stages",
        active: "doing",
        cls: "stage-row",
        attrs: { "data-x": "1" },
        items: [
          { id: "todo", label: "Backlog", dot: true, dotColor: "#4c8bf5", count: 3 },
          { id: "doing", label: "In progress <b>", dot: "ok", dotColor: "red;x" },
          { id: "review", label: "Review", badge: { label: "Proposed", tone: "accent" } },
        ],
      },
    },
    {
      name: "pills with none selected",
      ts: {
        variant: "pill",
        label: "Dashboards",
        active: null,
        items: [
          { id: "a", label: "Checkout" },
          { id: "b", label: "Payments", badge: { label: "Proposed", tone: "accent" } },
        ],
      },
    },
    {
      name: "a vertical list whose tabs share one panel",
      ts: {
        variant: "vertical",
        label: "Workshop",
        active: "skill",
        items: [
          {
            id: "agent",
            label: "Agents",
            icon: "bot",
            count: 4,
            tabId: "ws-tab-agent",
            panelId: "ws-list",
          },
          {
            id: "skill",
            label: "Skills",
            icon: "skills",
            count: 0,
            tabId: "ws-tab-skill",
            panelId: "ws-list",
          },
        ],
      },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { cls, attrs, ...props } = c.ts;
      const astro = await container.renderToString(Tabs, {
        props: { ...props, class: cls, ...attrs },
      });
      expect(normalize(astro)).toBe(normalize(tabsHTML(c.ts)));
    });
  }

  it("refuses a variant that does not exist, like tabsHTML", async () => {
    await expect(
      container.renderToString(Tabs, {
        props: { label: "x", items: [], variant: "folder" },
      }),
    ).rejects.toThrow(/no variant "folder"/);
  });
});

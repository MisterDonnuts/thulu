import { experimental_AstroContainer as AstroContainer } from "astro/container";
import { beforeAll, describe, expect, it } from "vitest";

import { taskCardHTML, type TaskCardOptions } from "../lib/ui/task-card";
import { normalize } from "./parity-normalize";
import TaskCard from "./TaskCard.astro";

let container: AstroContainer;
beforeAll(async () => {
  container = await AstroContainer.create();
});

describe("TaskCard.astro and taskCardHTML()", () => {
  const cases: Array<{ name: string; ts: TaskCardOptions }> = [
    { name: "a card alone", ts: { id: "bbbb00000000000a" } },
    {
      name: "states, an extra class and attributes",
      ts: { id: "a<b>", flags: ["lifted", "hl"], cls: "x", attrs: { "data-group": "p1" } },
    },
  ];
  for (const c of cases) {
    it(c.name, async () => {
      const { id, flags, cls, attrs } = c.ts;
      const astro = await container.renderToString(TaskCard, {
        props: { id, flags, class: cls, ...attrs },
      });
      expect(normalize(astro)).toBe(normalize(taskCardHTML(c.ts)));
    });
  }
});

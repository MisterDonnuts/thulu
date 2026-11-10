import { describe, expect, it } from "vitest";

import {
  batchCount,
  effectiveTz,
  emptyTrigger,
  insertAt,
  parsePayload,
  readTrigger,
  renderTrigger,
  runColumns,
  samplePayload,
  shouldLockGuide,
  stepSummary,
  syncBurst,
  triggerBlock,
  triggerRange,
  withTimezone,
  withTrigger,
} from "./automation-editor";
import { tableRowHTML } from "./ui/table";

function must<T>(v: T | null | undefined): T {
  if (v === null || v === undefined) throw new Error("expected a value");
  return v;
}

const RECIPE = `name: nightly
description: what it does
trigger:
  schedule: "0 3 * * *"
  tz: UTC

# the work
policy:
  timeout: 5m
steps:
  - id: t
    task: {title: x}
`;

describe("triggerRange", () => {
  it("covers the block and leaves the comment above the next key alone", () => {
    const r = must(triggerRange(RECIPE));
    expect(RECIPE.slice(r.start, r.end)).toBe('trigger:\n  schedule: "0 3 * * *"\n  tz: UTC\n');
  });

  it("reaches the end of a recipe whose last key is the trigger", () => {
    const spec = "name: n\ntrigger:\n  manual: true";
    const r = must(triggerRange(spec));
    expect(spec.slice(r.start, r.end)).toBe("trigger:\n  manual: true");
  });

  it("ignores a trigger key nested under another one", () => {
    expect(triggerRange("name: n\nvars:\n  trigger: x\n")).toBeNull();
  });
});

describe("withTrigger", () => {
  it("replaces only the trigger block", () => {
    const out = withTrigger(RECIPE, { ...emptyTrigger("event"), event: "task.created" });
    expect(out).toContain("trigger:\n  event: task.created\n\n# the work\npolicy:");
    expect(out).toContain("name: nightly\ndescription: what it does\n");
    expect(out).not.toContain("schedule");
  });

  it("adds a trigger after the header keys when there is none", () => {
    const out = withTrigger("name: n\nsteps: []\n", emptyTrigger("manual"));
    expect(out).toBe("name: n\ntrigger:\n  manual: true\nsteps: []\n");
  });

  it("round-trips through the parsed form", () => {
    const form = readTrigger({ schedule: "*/15 * * * *", tz: "America/Argentina/Buenos_Aires" });
    expect(renderTrigger(form)).toBe(
      'trigger:\n  schedule: "*/15 * * * *"\n  tz: America/Argentina/Buenos_Aires\n',
    );
  });
});

describe("triggerBlock", () => {
  it("reads the block without the blank line after it", () => {
    expect(triggerBlock(RECIPE)).toBe('trigger:\n  schedule: "0 3 * * *"\n  tz: UTC');
  });

  it("is null for a recipe with no trigger", () => {
    expect(triggerBlock("name: n\nsteps: []\n")).toBeNull();
  });
});

describe("shouldLockGuide", () => {
  const eventForm = emptyTrigger("event");
  const written = renderTrigger(eventForm);
  const halfDone = withTrigger(RECIPE, eventForm);

  it("leaves the form open when the trigger parsed", () => {
    expect(shouldLockGuide(true, RECIPE, "")).toBe(false);
  });

  it("leaves it open while the recipe holds the block the form wrote", () => {
    expect(written).toBe("trigger:\n");
    expect(shouldLockGuide(false, halfDone, written)).toBe(false);
  });

  it("leaves it open when that block is the last thing in the recipe", () => {
    const spec = withTrigger("name: n\nsteps: []\ntrigger:\n  manual: true", eventForm);
    expect(spec.endsWith("trigger:")).toBe(true);
    expect(shouldLockGuide(false, spec, written)).toBe(false);
  });

  it("locks when nothing parsed and the form has written nothing", () => {
    expect(shouldLockGuide(false, "name: n\ntrigger: [\n", "")).toBe(true);
  });

  it("locks once the trigger was changed by hand", () => {
    const edited = halfDone.replace("trigger:\n", "trigger:\n  evnt: task.created\n");
    expect(shouldLockGuide(false, edited, written)).toBe(true);
  });

  it("locks when the recipe has no trigger block left", () => {
    expect(shouldLockGuide(false, "name: n\nsteps: [\n", written)).toBe(true);
  });
});

describe("renderTrigger", () => {
  it("quotes what YAML would misread and keeps the threshold a number", () => {
    const out = renderTrigger({
      ...emptyTrigger("metric"),
      environment: "e1a2",
      metric: "error_rate",
      window: "15m",
      op: ">",
      threshold: "0.05",
    });
    expect(out).toBe(
      'trigger:\n  metric:\n    environment: e1a2\n    metric: error_rate\n    window: 15m\n    op: ">"\n    threshold: 0.05\n',
    );
  });

  it("quotes a filter with operators in it", () => {
    const out = renderTrigger({
      ...emptyTrigger("event"),
      event: "github.push",
      filter: 'trigger.payload.ref == "refs/heads/main"',
    });
    expect(out).toContain(`filter: 'trigger.payload.ref == "refs/heads/main"'`);
  });

  it("doubles a single quote inside single quotes, and double-quotes the rest", () => {
    const out = renderTrigger({
      ...emptyTrigger("event"),
      event: "task.created",
      filter: `trigger.payload.title == "it's done"`,
    });
    expect(out).toContain(`filter: 'trigger.payload.title == "it''s done"'`);
    const plain = renderTrigger({
      ...emptyTrigger("event"),
      event: "task.created",
      filter: "!trigger.payload.sender_is_bot",
    });
    expect(plain).toContain('filter: "!trigger.payload.sender_is_bot"');
  });

  it("reads a metric trigger back field by field", () => {
    const f = readTrigger({ metric: { environment: "e1", metric: "m", threshold: 800, op: ">=" } });
    expect(f.kind).toBe("metric");
    expect(f.threshold).toBe("800");
    expect(f.op).toBe(">=");
  });
});

describe("insertAt", () => {
  it("replaces the selection and puts the cursor after the value", () => {
    expect(insertAt("board: X", 7, 8, "b123")).toEqual({ text: "board: b123", cursor: 11 });
  });

  it("clamps a stale cursor", () => {
    expect(insertAt("ab", 9, 12, "c")).toEqual({ text: "abc", cursor: 3 });
  });
});

describe("timezones", () => {
  it("says UTC when the recipe names no tz", () => {
    expect(effectiveTz()).toBe("UTC");
    expect(effectiveTz("Europe/Madrid")).toBe("Europe/Madrid");
  });

  it("puts the browser's zone into a template", () => {
    const spec = 'trigger:\n  schedule: "0 9 * * 1-5"\n  tz: UTC\n';
    expect(withTimezone(spec, "America/Argentina/Buenos_Aires")).toContain(
      "tz: America/Argentina/Buenos_Aires",
    );
    expect(withTimezone(spec, "UTC")).toBe(spec);
  });
});

describe("samplePayload", () => {
  it("has nothing for a schedule", () => {
    expect(samplePayload({ trigger: { schedule: "* * * * *" } })).toBeNull();
  });

  it("shapes a review comment like the webhook does", () => {
    const p = must(samplePayload({ trigger: { event: "github.pull_request_review_comment" } }));
    expect(p.action).toBe("created");
    expect(p.sender_is_bot).toBe(false);
    expect(p).toHaveProperty("task_id");
    expect(p).not.toHaveProperty("events");
  });

  it("carries the burst for a debounced recipe", () => {
    const p = must(samplePayload({ trigger: { event: "task.created" }, debounce: "5m" }));
    expect(p.event_count).toBe(1);
    expect(Array.isArray(p.events)).toBe(true);
  });

  it("crosses the metric's threshold", () => {
    const p = must(
      samplePayload({
        trigger: { metric: { environment: "e1", metric: "lat", threshold: 800, op: ">" } },
      }),
    );
    expect(p.environment).toBe("e1");
    expect(Number(p.value)).toBeGreaterThan(800);
  });

  it("names the stages of a stage change", () => {
    const p = must(samplePayload({ trigger: { event: "task.stage_changed" } }));
    expect(p.from).toBeTruthy();
    expect(p.to).toBe(p.stage);
  });

  it("uses the board's own stages and id when it has them", () => {
    const board = { id: "b-1", stages: ["backlog", "in_progress", "revision"] };
    const p = must(samplePayload({ trigger: { event: "task.stage_changed" }, board }));
    expect(p.board_id).toBe("b-1");
    expect(p.from).toBe("backlog");
    expect(p.to).toBe("in_progress");
    const created = must(samplePayload({ trigger: { event: "task.created" }, board }));
    expect(created.stage).toBe("backlog");
  });
});

describe("syncBurst", () => {
  it("makes a burst of one follow the edited event", () => {
    const sample = must(samplePayload({ trigger: { event: "task.created" }, debounce: "5m" }));
    const edited = { ...sample, task_id: "t-9" };
    const sent = syncBurst(edited);
    expect((sent.events as Array<Record<string, unknown>>)[0].task_id).toBe("t-9");
    expect((sent.events as Array<Record<string, unknown>>)[0]).not.toHaveProperty("events");
    expect(sent.event_count).toBe(1);
  });

  it("leaves a burst of several as written", () => {
    const p = { task_id: "a", events: [{ task_id: "a" }, { task_id: "b" }], event_count: 2 };
    expect(syncBurst(p)).toBe(p);
    expect(syncBurst({ task_id: "x" })).toEqual({ task_id: "x" });
  });
});

describe("batchCount", () => {
  it("reads how many events a debounced run gathered", () => {
    expect(batchCount({ event_count: 3, events: [{}, {}, {}] })).toBe(3);
    expect(batchCount({ events: [{}, {}] })).toBe(2);
    expect(batchCount({ task_id: "t" })).toBe(0);
    expect(batchCount(null)).toBe(0);
  });

  it("reads the payload the run stored as text", () => {
    expect(batchCount(parsePayload('{"event_count":2,"events":[{},{}]}'))).toBe(2);
    expect(parsePayload("")).toBeNull();
    expect(parsePayload("{broken")).toBeNull();
    expect(parsePayload("[1]")).toBeNull();
  });
});

describe("stepSummary", () => {
  it("says which task a task step created, with its id for the link", () => {
    const s = must(
      stepSummary({
        kind: "task",
        status: "ok",
        output: '{"agent":"","created":true,"fingerprint":"","task_id":"t-1","title":"Check deps"}',
      }),
    );
    expect(s.taskID).toBe("t-1");
    expect(s.text).toContain("Check deps");
  });

  it("links to the task's drawer, the #task-<id> the tasks page opens", () => {
    const created = must(
      stepSummary({
        kind: "task",
        status: "ok",
        output: '{"created":true,"task_id":"t-1","title":"Check deps"}',
      }),
    );
    expect(created.href).toBe("/tasks/#task-t-1");
    const mentioned = must(
      stepSummary({
        kind: "mention",
        status: "ok",
        output: '{"agent":"dev","mentioned":true,"task_id":"t-2"}',
      }),
    );
    expect(mentioned.href).toBe("/tasks/#task-t-2");
    expect(mentioned.text).toContain("t-2");
    const titled = must(
      stepSummary({
        kind: "mention",
        status: "ok",
        output: '{"agent":"dev","mentioned":true,"task_id":"t-2","title":"Fix login"}',
      }),
    );
    expect(titled.text).toContain("Fix login");
    expect(titled.text).not.toContain("t-2");
    expect(
      must(stepSummary({ kind: "query", status: "ok", output: '{"count":2}' })).href,
    ).toBeUndefined();
  });

  it("says a mention commented because its stage has nobody to wake", () => {
    const s = must(
      stepSummary({
        kind: "mention",
        status: "ok",
        output: '{"commented":true,"task_id":"t-3","title":"Fix login"}',
      }),
    );
    expect(s.text).toBe('Commented on "Fix login"; its stage has no agent to wake');
  });

  it("names the stage's agent a mention skipped because it does not answer mentions", () => {
    const s = must(
      stepSummary({
        kind: "mention",
        status: "ok",
        output: '{"commented":true,"skipped_agent":"builder","task_id":"t-3","title":"Fix login"}',
      }),
    );
    expect(s.text).toBe('Commented on "Fix login"; builder does not answer mentions');
    expect(s.text).not.toContain("no agent");
    expect(s.href).toBe("/tasks/#task-t-3");
  });

  it("names the agent a task woke", () => {
    const s = must(
      stepSummary({
        kind: "task",
        status: "ok",
        output: '{"agent":"dev","created":true,"task_id":"t-1","title":"Fix"}',
      }),
    );
    expect(s.text).toContain("dev");
  });

  it("reads the count from a query output cut short", () => {
    const s = must(
      stepSummary({
        kind: "query",
        status: "ok",
        output: '{"count":12,"json":{"tasks":[{"id":"a',
      }),
    );
    expect(s.text).toContain("12");
  });

  it("leaves a failed step and plain text outputs alone", () => {
    expect(stepSummary({ kind: "task", status: "failed", error: "boom" })).toBeNull();
    expect(stepSummary({ kind: "condition", status: "ok", output: "condition held" })).toBeNull();
  });
});

describe("runColumns", () => {
  it("lays out a run after its caret, with the cancel column unnamed on screen", () => {
    const cols = runColumns();
    expect(cols.map((c) => c.label)).toEqual([
      "Started",
      "Trigger",
      "Status",
      "Agent runs",
      "Duration",
      "Actions",
    ]);
    expect(cols[5].hideLabel).toBe(true);
    const row = tableRowHTML(
      cols,
      {
        id: "r1",
        expanded: true,
        detailHTML: "<ul></ul>",
        cells: ["2 m", "manual", "ok", 3, "1s", ""],
      },
      { expandable: true, stack: true },
    );
    expect(row).toContain('<td class="num mono cell-shrink" data-label="Agent runs">3</td>');
    expect(row).toContain('<td colspan="7"><ul></ul></td>');
  });
});

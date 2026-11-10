import { describe, expect, it } from "vitest";

import { t } from "../i18n";

import {
  CAPACITY_SETTINGS,
  capacityOf,
  checkCapacity,
  storedCapacity,
  chartWindow,
  dataDisk,
  fmtBytes,
  fmtPercent,
  fmtQuantity,
  groupOf,
  groupUsage,
  containerOwnerName,
  labelIsTask,
  nearestIndex,
  niceBytes,
  parseQuantity,
  reachableFrom,
  seriesLegend,
  severity,
  share,
  sortContainers,
  stageLabelOf,
  stopToasts,
  stateLabel,
  timeChart,
  timeTicks,
  workspaceLabel,
  type ChartSeries,
  type Container,
  type Disk,
  type Point,
  type Snapshot,
  containerColumns,
  groupColumns,
  quotaColumns,
  workspaceColumns,
} from "./infra";
import { tableHeadHTML, tableRowHTML } from "./ui/table";

const GiB = 1024 ** 3;

function snapshot(
  over: Partial<Snapshot["runtime"]> = {},
  threads = 8,
  memTotal = 16 * GiB,
): Snapshot {
  return {
    host: { threads, memory: { total: memTotal } },
    runtime: { mode: "none", ...over },
  } as unknown as Snapshot;
}

function container(over: Partial<Container>): Container {
  return {
    id: "",
    name: "",
    kind: "other",
    state: "running",
    status: "",
    image: "",
    workspace_id: "",
    workspace_name: "",
    task_id: "",
    agent: "",
    stage: "",
    service: "",
    cpu_cores: 0,
    has_usage: true,
    mem_bytes: 0,
    mem_limit: 0,
    net_rx: 0,
    net_tx: 0,
    blk_read: 0,
    blk_write: 0,
    pids: 0,
    ...over,
  };
}

function point(t: number, v: number): Point {
  return {
    t: new Date(t).toISOString(),
    cpu: v,
    mem: v,
    disk: 0,
    rx: v,
    tx: v / 2,
    running: 1,
    runs: 0,
  };
}

describe("fmtBytes", () => {
  it("uses binary units with three significant digits", () => {
    expect(fmtBytes(0)).toBe("0 B");
    expect(fmtBytes(-5)).toBe("0 B");
    expect(fmtBytes(NaN)).toBe("0 B");
    expect(fmtBytes(812)).toBe("812 B");
    expect(fmtBytes(1536)).toBe("1.5 KiB");
    expect(fmtBytes(1.4 * GiB)).toBe("1.4 GiB");
    expect(fmtBytes(150 * GiB)).toBe("150 GiB");
  });
});

describe("parseQuantity / fmtQuantity", () => {
  it("reads Kubernetes suffixes into base units", () => {
    expect(parseQuantity("5500m")).toBe(5.5);
    expect(parseQuantity("6")).toBe(6);
    expect(parseQuantity("16Gi")).toBe(16 * GiB);
    expect(parseQuantity("9114Mi")).toBe(9114 * 1024 ** 2);
    expect(parseQuantity("2k")).toBe(2000);
    expect(parseQuantity("1e3")).toBe(1000);
    expect(parseQuantity("lots")).toBeNull();
  });

  it("shows cores and bytes instead of the cluster's units", () => {
    expect(fmtQuantity("5500m", "cpu")).toBe("5.5");
    expect(fmtQuantity("16Gi", "memory")).toBe("16 GiB");
    expect(fmtQuantity("9114Mi", "memory")).toBe("8.9 GiB");
    expect(fmtQuantity("0", "memory")).toBe("0");
    expect(fmtQuantity("20", "count")).toBe("20");
    expect(fmtQuantity("weird", "cpu")).toBe("weird");
  });
});

describe("fmtPercent", () => {
  it("keeps a decimal under ten and none above", () => {
    expect(fmtPercent(7.44)).toBe("7.4%");
    expect(fmtPercent(63.4)).toBe("63%");
    expect(fmtPercent(Infinity)).toBe("—");
  });
});

describe("severity and share", () => {
  it("turns warn at 75 and danger at 90", () => {
    expect(severity(74.9)).toBe("ok");
    expect(severity(75)).toBe("warn");
    expect(severity(89.9)).toBe("warn");
    expect(severity(90)).toBe("danger");
  });
  it("clamps to 0..100 and gives 0 for an unknown whole", () => {
    expect(share(1, 4)).toBe(25);
    expect(share(5, 4)).toBe(100);
    expect(share(1, 0)).toBe(0);
  });
});

describe("groupOf / groupUsage", () => {
  it("folds the daemon, Postgres and the web proxy into the platform", () => {
    expect(groupOf("daemon")).toBe("platform");
    expect(groupOf("postgres")).toBe("platform");
    expect(groupOf("web")).toBe("platform");
    expect(groupOf("agent")).toBe("agents");
    expect(groupOf("other")).toBe("other");
    expect(groupOf("other_install")).toBe("other_install");
  });
  it("adds kinds of one group and lists groups in a fixed order", () => {
    const rows = groupUsage([
      { kind: "postgres", count: 1, running: 1, cpu_cores: 0.1, mem_bytes: 100 },
      { kind: "agent", count: 3, running: 2, cpu_cores: 1.5, mem_bytes: 300 },
      { kind: "daemon", count: 1, running: 1, cpu_cores: 0.2, mem_bytes: 50 },
    ]);
    expect(rows.map((r) => r.group)).toEqual(["agents", "platform"]);
    expect(rows[1]).toMatchObject({ count: 2, running: 2, memBytes: 150 });
    expect(rows[1].cpuCores).toBeCloseTo(0.3);
  });
});

describe("capacityOf", () => {
  it("adds Kubernetes nodes first", () => {
    const s = snapshot({
      kube: {
        nodes: [
          { cpu_capacity_milli: 4000, mem_capacity: 8 * GiB },
          { cpu_capacity_milli: 2000, mem_capacity: 4 * GiB },
        ],
      } as unknown as NonNullable<Snapshot["runtime"]["kube"]>,
    });
    expect(capacityOf(s)).toEqual({ cpus: 6, mem: 12 * GiB });
  });
  it("then Docker's own view, then the host", () => {
    const docker = { ncpu: 4, mem_total: 6 * GiB } as NonNullable<Snapshot["runtime"]["docker"]>;
    expect(capacityOf(snapshot({ docker }))).toEqual({ cpus: 4, mem: 6 * GiB });
    expect(capacityOf(snapshot())).toEqual({ cpus: 8, mem: 16 * GiB });
    expect(capacityOf(snapshot({}, 0, 0))).toEqual({ cpus: 1, mem: 0 });
  });
});

describe("dataDisk", () => {
  const d = (path: string, roles: string[]) => ({ path, roles }) as Disk;
  it("prefers the data directory's filesystem", () => {
    expect(dataDisk([d("/", ["root"]), d("/data", ["data"])])?.path).toBe("/data");
    expect(dataDisk([d("/", ["root"])])?.path).toBe("/");
    expect(dataDisk(null)).toBeUndefined();
  });
});

describe("sortContainers", () => {
  const list = [
    container({ name: "b", cpu_cores: 0.5, kind: "agent" }),
    container({ name: "a", cpu_cores: 2, kind: "daemon" }),
    container({ name: "c", cpu_cores: 0.5, kind: "agent", has_usage: false }),
    container({ name: "d", cpu_cores: 0.5, kind: "agent" }),
  ];
  it("sorts numbers biggest first and keeps the server order on ties", () => {
    expect(sortContainers(list, "cpu", true).map((c) => c.name)).toEqual(["a", "b", "d", "c"]);
  });
  it("sorts names and kinds ascending, kinds by group order", () => {
    expect(sortContainers(list, "name", false).map((c) => c.name)).toEqual(["a", "b", "c", "d"]);
    expect(sortContainers(list, "kind", false).map((c) => c.name)).toEqual(["b", "c", "d", "a"]);
  });
  it("does not mutate the list it was given", () => {
    sortContainers(list, "name", true);
    expect(list.map((c) => c.name)).toEqual(["b", "a", "c", "d"]);
  });
});

describe("timeChart", () => {
  const end = Date.parse("2026-10-03T12:00:00Z");
  const step = 15_000;
  const opts = {
    width: 600,
    height: 180,
    rangeMs: 15 * 60_000,
    end,
    intervalMs: step,
    fmtY: String,
  };
  const one: ChartSeries[] = [{ label: "CPU", color: "var(--viz-1)", value: (p) => p.cpu }];
  const two: ChartSeries[] = [
    { label: "In", color: "var(--viz-1)", value: (p) => p.rx },
    { label: "Out", color: "var(--viz-3)", value: (p) => p.tx },
  ];

  it("drops points older than the window", () => {
    const pts = [
      point(end - 20 * 60_000, 10),
      point(end - 2 * step, 20),
      point(end - step, 30),
      point(end, 40),
    ];
    const m = timeChart(pts, one, opts);
    expect(m.xs).toHaveLength(3);
    expect(m.xs.at(-1)).toBeCloseTo(600 - 12);
  });

  it("washes the area only under a single series", () => {
    const pts = [point(end - 2 * step, 20), point(end - step, 30), point(end, 40)];
    expect(timeChart(pts, one, opts).svg).toContain("chart-wash");
    const svg = timeChart(pts, two, opts).svg;
    expect(svg).not.toContain("chart-wash");
    expect(svg.match(/class="chart-line"/g)).toHaveLength(2);
  });

  it("breaks the line across a hole longer than three samples", () => {
    const pts = [
      point(end - 10 * 60_000, 10),
      point(end - 10 * 60_000 + step, 10),
      point(end - step, 30),
      point(end, 40),
    ];
    expect(timeChart(pts, one, opts).svg.match(/class="chart-line"/g)).toHaveLength(2);
  });

  it("caps values at a fixed maximum", () => {
    const pts = [point(end - step, 50), point(end, 250)];
    const m = timeChart(pts, one, { ...opts, yMax: 100 });
    expect(m.ys[0][1]).toBeCloseTo(10);
  });

  it("escapes the axis labels it is given", () => {
    const pts = [point(end - step, 1), point(end, 2)];
    const svg = timeChart(pts, one, { ...opts, fmtY: () => "<b>" }).svg;
    expect(svg).not.toContain("<b>");
    expect(svg).toContain("&lt;b&gt;");
  });
});

describe("timeTicks", () => {
  it("cuts a narrow axis into fewer intervals", () => {
    expect(timeTicks(260)).toBe(2);
    expect(timeTicks(400)).toBe(3);
    expect(timeTicks(900)).toBe(4);
  });
});

describe("chartWindow", () => {
  const end = Date.parse("2026-10-03T12:00:00Z");
  const hour = 60 * 60_000;
  it("keeps the range when the history covers it", () => {
    expect(chartWindow([point(end - 2 * hour, 1), point(end, 1)], hour)).toBe(hour);
  });
  it("shrinks to a young history, but not under the floor", () => {
    expect(chartWindow([point(end - 12 * 60_000, 1), point(end, 1)], hour)).toBe(12 * 60_000);
    expect(chartWindow([point(end - 30_000, 1), point(end, 1)], hour)).toBe(2 * 60_000);
  });
  it("keeps the range with fewer than two samples", () => {
    expect(chartWindow([point(end, 1)], hour)).toBe(hour);
  });
});

describe("nearestIndex", () => {
  it("picks the closest x, or -1 for no points", () => {
    expect(nearestIndex([10, 20, 30], 24)).toBe(1);
    expect(nearestIndex([10, 20, 30], 99)).toBe(2);
    expect(nearestIndex([], 5)).toBe(-1);
  });
});

describe("niceBytes", () => {
  it("rounds up in binary units", () => {
    expect(niceBytes(3.6 * 1024 ** 2)).toBe(5 * 1024 ** 2);
    expect(niceBytes(700)).toBe(1000);
    expect(niceBytes(0)).toBe(1024);
  });
});

describe("capacity settings", () => {
  const by = (k: string) => {
    const spec = CAPACITY_SETTINGS.find((s) => s.key === k);
    if (!spec) throw new Error(`no capacity setting ${k}`);
    return spec;
  };

  it("lets 0 lift the agent cap but not the sandbox one", () => {
    expect(checkCapacity(by("agent.max_containers"), "0")).toEqual({ ok: true, value: 0 });
    expect(checkCapacity(by("sandbox.max"), "0")).toEqual({ ok: false, problem: "range" });
  });

  it("keeps the queue wait between 1 and 720 minutes", () => {
    expect(checkCapacity(by("agent.max_queue_minutes"), "721")).toEqual({
      ok: false,
      problem: "range",
    });
    expect(checkCapacity(by("agent.max_queue_minutes"), " 60 ")).toEqual({ ok: true, value: 60 });
  });

  it("wants a whole number, typed", () => {
    expect(checkCapacity(by("chat.max_containers"), "")).toEqual({
      ok: false,
      problem: "required",
    });
    expect(checkCapacity(by("chat.max_containers"), "2.5")).toEqual({
      ok: false,
      problem: "integer",
    });
  });

  it("reads a stored 0 as 0 and an unset key as the default", () => {
    expect(storedCapacity(by("agent.max_containers"), { "agent.max_containers": 0 })).toBe(0);
    expect(storedCapacity(by("chat.max_containers"), {})).toBe(5);
    expect(storedCapacity(by("sandbox.max"), { "sandbox.max": null })).toBe(3);
  });
});

describe("reachableFrom", () => {
  it("offers nothing until the session answers", () => {
    expect(reachableFrom("ws1", null)).toBe(false);
  });
  it("offers every row when no session names a workspace", () => {
    expect(reachableFrom("ws1", "")).toBe(true);
    expect(reachableFrom(undefined, "")).toBe(true);
  });
  it("offers only the session's own workspace", () => {
    expect(reachableFrom("ws1", "ws1")).toBe(true);
    expect(reachableFrom("ws2", "ws1")).toBe(false);
    expect(reachableFrom(undefined, "ws1")).toBe(false);
  });
});

describe("labelIsTask", () => {
  it("tells a task from a prewarm or Evidence owner, before and after Kubernetes", () => {
    expect(labelIsTask("a1b2c3d4")).toBe(true);
    expect(labelIsTask("prewarm:p1")).toBe(false);
    expect(labelIsTask("prewarm-p1")).toBe(false);
    expect(labelIsTask("evidence-b1")).toBe(false);
  });
});

describe("containerOwnerName", () => {
  it("says what a container works for the way /sandboxes does", () => {
    expect(containerOwnerName("a1b2", "Fix the login page")).toBe("Fix the login page");
    expect(containerOwnerName("a1b2")).toBe("a1b2");
    expect(containerOwnerName("prewarm:p-tienda", "", "tienda-api")).toBe("Prewarm of tienda-api");
    expect(containerOwnerName("prewarm-p-tienda", "", "tienda-api")).toBe("Prewarm of tienda-api");
    expect(containerOwnerName("prewarm:p-tienda")).toBe("Prewarm of p-tienda");
    expect(containerOwnerName("evidence-b1")).toBe("Evidence batch");
  });
});

describe("row labels", () => {
  it("names the fallback workspace in words and keeps a real one's name", () => {
    expect(workspaceLabel("", "default")).toBe("Default workspace");
    expect(workspaceLabel("Acme", "bbbb000000000008")).toBe("Acme");
    expect(workspaceLabel("", "bbbb000000000008")).toBe("bbbb000000000008");
    expect(workspaceLabel(undefined, undefined)).toBe("—");
  });

  it("reads a stage by its board label, the mention gate by name, an unknown one as sent", () => {
    const labels = new Map([["in_progress", "In progress"]]);
    expect(stageLabelOf("in_progress", labels)).toBe("In progress");
    expect(stageLabelOf("mention", labels)).toBe("Mention");
    expect(stageLabelOf("gone", labels)).toBe("gone");
  });

  it("translates Docker states and pod phases, and keeps an unknown one", () => {
    expect(stateLabel("running")).toBe("Running");
    expect(stateLabel("exited")).toBe("Exited");
    expect(stateLabel("Pending")).toBe("Pending");
    expect(stateLabel("hibernating")).toBe("hibernating");
  });
});

describe("infra table columns", () => {
  it("make every container column but the actions sortable, from none", () => {
    const cols = containerColumns();
    expect(cols).toHaveLength(9);
    expect(cols.slice(0, 8).map((c) => c.key)).toEqual([
      "name",
      "kind",
      "workspace",
      "task",
      "cpu",
      "mem",
      "uptime",
      "state",
    ]);
    expect(cols.slice(0, 8).every((c) => c.sort === "none")).toBe(true);
    const head = tableHeadHTML(cols);
    expect(head).toContain('data-sort="cpu" aria-sort="none"><button type="button">CPU</button>');
    expect(head).toContain('<span class="visually-hidden">Actions</span>');
  });

  it("align figures right and name each cell on a phone", () => {
    const row = tableRowHTML(
      groupColumns(),
      { cells: ["Agents", "2 of 3", "4%", "1%"] },
      { stack: true },
    );
    expect(row).toBe(
      '<tr><td data-label="What">Agents</td>' +
        '<td class="num cell-shrink" data-label="Running">2 of 3</td>' +
        '<td class="num cell-shrink" data-label="CPU">4%</td>' +
        '<td class="num cell-shrink" data-label="Memory">1%</td></tr>',
    );
    expect(workspaceColumns()).toHaveLength(10);
    expect(workspaceColumns()[1].num).toBeUndefined();
    expect(quotaColumns()[1].mono).toBe(true);
  });
});

describe("seriesLegend", () => {
  it("gives each series a line key in its own colour", () => {
    const series: ChartSeries[] = [
      { label: "In", color: "var(--viz-1)", value: (p) => p.rx },
      { label: "Out", color: "var(--viz-3)", value: (p) => p.tx },
    ];
    expect(seriesLegend(series)).toEqual([
      { label: "In", color: "var(--viz-1)" },
      { label: "Out", color: "var(--viz-3)" },
    ]);
  });
});

describe("stopToasts", () => {
  it("says an orphan is being removed, from the first toast to the last", () => {
    const w = stopToasts({ name: "dev-1", orphan: true });
    expect(w.pending).toBe(t("infra.containers.removing_orphan", { name: "dev-1" }));
    expect(w.done).toBe(t("infra.containers.removed_orphan", { name: "dev-1" }));
    expect(w.failed("boom")).toBe(t("infra.containers.remove_orphan_failed", { error: "boom" }));
    expect(w.pending).not.toBe(t("infra.containers.stopping", { name: "dev-1" }));
  });

  it("says any other container is being stopped", () => {
    const w = stopToasts({ name: "chat-1" });
    expect(w.pending).toBe(t("infra.containers.stopping", { name: "chat-1" }));
    expect(w.done).toBe(t("infra.containers.stopped", { name: "chat-1" }));
    expect(w.failed("boom")).toBe(t("infra.containers.stop_failed", { error: "boom" }));
  });
});

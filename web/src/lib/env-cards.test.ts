import { describe, expect, it } from "vitest";
import {
  customMetricsHTML,
  dashboardsHTML,
  envCardHTML,
  envListHTML,
  envProposalsHTML,
  envSignalsHTML,
  envTestingHTML,
  groupBySystem,
  type CustomMetric,
  type Dashboard,
  type EnvCardDeps,
  type EnvDetail,
  type Environment,
  type Mapping,
} from "./env-cards";
import type { SeriesPoint } from "./signals";

function env(over: Partial<Environment> = {}): Environment {
  return {
    id: "e1",
    project_id: "p1",
    name: "prod",
    provider: "aws",
    is_production: false,
    has_credential: false,
    ...over,
  };
}

function mapping(over: Partial<Mapping> = {}): Mapping {
  return {
    id: "m1",
    environment_id: "e1",
    service: "api",
    signal: "latency_p95",
    source: {},
    status: "active",
    created_by: "claude",
    ...over,
  };
}

function metric(over: Partial<CustomMetric> = {}): CustomMetric {
  return {
    id: "cm1",
    service: "api",
    name: "orders_total",
    kind: "counter",
    labels: [],
    label_limit: 10,
    status: "discovered",
    first_seen: "2026-07-31T10:00:00Z",
    last_seen: "2026-07-31T11:00:00Z",
    ...over,
  };
}

function dash(over: Partial<Dashboard> = {}): Dashboard {
  return {
    id: "d1",
    environment_id: "e1",
    name: "Checkout health",
    status: "active",
    panels: [],
    created_by: "claude",
    updated_at: "2026-07-31T11:00:00Z",
    ...over,
  };
}

function detail(over: Partial<EnvDetail> = {}): EnvDetail {
  return {
    mappings: [],
    points: [],
    custom: { metrics: [], discovered: 0, registered: 0, capacity: 0 },
    dashboards: [],
    ...over,
  };
}

function deps(over: Partial<EnvCardDeps> = {}): EnvCardDeps {
  return {
    detail: () => detail(),
    openDashID: () => undefined,
    dashPanels: () => undefined,
    fmtSignal: (_s, v) => String(v),
    sigAge: () => "2m ago",
    sparkline: () => "<svg/>",
    ...over,
  };
}

describe("groupBySystem", () => {
  it("puts the ungrouped environments last", () => {
    const out = groupBySystem([
      env({ id: "a" }),
      env({ id: "b", system: "platform" }),
      env({ id: "c", system: "billing" }),
    ]);
    expect(out.map(([k]) => k)).toEqual(["billing", "platform", ""]);
  });
  it("keeps the order the environments arrived in inside a group", () => {
    const out = groupBySystem([env({ id: "a", system: "s" }), env({ id: "b", system: "s" })]);
    expect(out[0][1].map((e) => e.id)).toEqual(["a", "b"]);
  });
});

describe("envListHTML", () => {
  it("skips the system heading when there is only one group", () => {
    const html = envListHTML([env({ system: "platform" })], deps());
    expect(html).not.toContain("group-head");
  });
  it("heads each named group once there are two", () => {
    const html = envListHTML([env({ id: "a", system: "platform" }), env({ id: "b" })], deps());
    expect(html.match(/class="group-head"/g)?.length).toBe(1);
    expect(html).toContain(
      '<span class="group-head-title"><span class="group-head-kicker">System</span>platform</span>',
    );
  });
  it("draws each group's environments as a run of rows", () => {
    const html = envListHTML([env({ id: "a", system: "platform" }), env({ id: "b" })], deps());
    expect(html.match(/class="row-list"/g)?.length).toBe(2);
  });
});

describe("envCardHTML", () => {
  it("offers to attach a credential when there is none", () => {
    const html = envCardHTML(env(), deps());
    expect(html).toMatch(
      /<button class="tag [^"]*tag-dashed[^"]*" type="button"[^>]*data-env-cred="e1"/,
    );
    expect(html).toContain('<use href="#i-plus"/>');
    expect(html).toContain(">attach credential<");
    expect(html).not.toContain("+ attach");
  });
  it("names the gap when a credential exists but nothing is mapped", () => {
    const html = envCardHTML(env({ has_credential: true }), deps());
    expect(html).toContain("credential set");
    expect(html).toContain("not mapped");
  });
  it("says nothing about mapping when there is no credential either", () => {
    expect(envCardHTML(env(), deps())).not.toContain("not mapped");
  });
  it("counts active signals", () => {
    const d = deps({
      detail: () => detail({ mappings: [mapping(), mapping({ id: "m2", signal: "cpu" })] }),
    });
    expect(envCardHTML(env({ has_credential: true }), d)).toContain("2 signals");
  });
  it("escapes a name and a URL", () => {
    const html = envCardHTML(env({ name: "<script>", url: 'x" onerror="y' }), deps());
    expect(html).not.toContain("<script>");
    expect(html).not.toContain('onerror="y"');
  });
  it("lines the row's icon buttons up with its small buttons, each with a name", () => {
    const html = envCardHTML(env(), deps());
    expect(html).toMatch(
      /<button class="btn btn-sm" type="button" title="[^"]+" data-env-collect="e1">/,
    );
    expect(html).toMatch(
      /<button class="icon-btn icon-btn-sm" type="button" title="([^"]+)" aria-label="\1" data-env-edit="e1">/,
    );
    expect(html).toMatch(
      /<button class="icon-btn icon-btn-sm icon-btn-danger" type="button" title="([^"]+)" aria-label="\1" data-env-del="e1">/,
    );
  });
  it("is a list row with its blocks in a detail under it", () => {
    const html = envCardHTML(env(), deps({ detail: () => detail({ mappings: [mapping()] }) }));
    expect(html).toMatch(/^<div class="env-item"><div class="row">/);
    expect(html).toContain('</div><div class="env-detail"><div class="stat-grid sig-strip"');
    expect(html).toContain('<h4 class="sec-title">Dashboards</h4>');
  });
  it("links an http(s) URL and shows any other scheme as text", () => {
    expect(envCardHTML(env({ url: "https://app.example.com" }), deps())).toContain(
      'href="https://app.example.com"',
    );
    const html = envCardHTML(env({ url: "javascript:alert(1)" }), deps());
    expect(html).not.toContain('href="javascript');
    expect(html).toContain("javascript:alert(1)");
  });
});

describe("envProposalsHTML", () => {
  it("is empty with nothing proposed", () => {
    expect(envProposalsHTML(env(), deps())).toBe("");
  });
  it("says proposals are inert and offers the approve button", () => {
    const d = deps({
      detail: () => detail({ mappings: [mapping({ status: "proposed" })] }),
    });
    const html = envProposalsHTML(env(), d);
    expect(html).toContain("1 mapping proposed");
    expect(html).toContain("inert by design");
    expect(html).toContain('data-env-approve="e1"');
  });
  it("pluralises", () => {
    const d = deps({
      detail: () =>
        detail({
          mappings: [
            mapping({ status: "proposed" }),
            mapping({ id: "m2", status: "proposed", signal: "cpu" }),
          ],
        }),
    });
    expect(envProposalsHTML(env(), d)).toContain("2 mappings proposed");
  });
});

describe("envSignalsHTML", () => {
  const pts: SeriesPoint[] = [
    { service: "api", signal: "latency_p95", bucket: "2026-07-31T10:00:00Z", value: 10 },
    { service: "api", signal: "latency_p95", bucket: "2026-07-31T11:00:00Z", value: 20 },
  ];

  it("draws nothing when there is nothing to say", () => {
    expect(envSignalsHTML(env(), deps())).toBe("");
  });
  it("says 'no data yet' for a mapped signal with no points", () => {
    const d = deps({ detail: () => detail({ mappings: [mapping()] }) });
    expect(envSignalsHTML(env(), d)).toContain("no data yet");
  });
  it("shows the last value and its age", () => {
    const d = deps({ detail: () => detail({ mappings: [mapping()], points: pts }) });
    const html = envSignalsHTML(env(), d);
    expect(html).toContain(">20<");
    expect(html).toContain("2m ago");
  });
  it("spells out a signal the architecture cannot produce", () => {
    const d = deps({
      detail: () =>
        detail({
          mappings: [
            mapping({ status: "not_applicable", signal: "cpu", note: "Lambda has no CPU metric" }),
          ],
        }),
    });
    const html = envSignalsHTML(env(), d);
    expect(html).toContain('class="stat stat-sm stat-na"');
    expect(html).toContain("Lambda has no CPU metric");
  });
  it("renders pushed telemetry that no mapping claims", () => {
    const d = deps({ detail: () => detail({ mappings: [], points: pts }) });
    const html = envSignalsHTML(env(), d);
    expect(html).toContain("reported by the app");
    expect(html).toContain("sig-pushed");
  });
  it("does not render a pushed tile for a series a mapping already covers", () => {
    const d = deps({ detail: () => detail({ mappings: [mapping()], points: pts }) });
    expect(envSignalsHTML(env(), d)).not.toContain("reported by the app");
  });
  it("takes the unit off the mapping's source", () => {
    const seen: string[] = [];
    const d = deps({
      detail: () => detail({ mappings: [mapping({ source: { unit: "s" } })], points: pts }),
      fmtSignal: (_s, v, u) => {
        seen.push(String(u));
        return String(v);
      },
    });
    envSignalsHTML(env(), d);
    expect(seen).toContain("s");
  });
});

describe("customMetricsHTML", () => {
  it("stays out of the way when nothing was discovered", () => {
    expect(customMetricsHTML(env(), deps())).toBe("");
  });
  it("gives the buttons only to a metric waiting on a decision", () => {
    const d = deps({
      detail: () =>
        detail({
          custom: {
            metrics: [metric(), metric({ id: "cm2", status: "rejected" })],
            discovered: 1,
            registered: 2,
            capacity: 50,
          },
        }),
    });
    const html = customMetricsHTML(env(), d);
    expect(html.match(/data-cm-approve/g)?.length).toBe(1);
    expect(html).toContain("1 waiting");
    expect(html).toContain('class="row row-wrap row-wrap-all row-dim"');
  });
  it("lets a decided metric go back to waiting or be deleted", () => {
    const d = deps({
      detail: () =>
        detail({
          custom: {
            metrics: [
              metric(),
              metric({ id: "cm2", status: "approved" }),
              metric({ id: "cm3", status: "rejected" }),
            ],
            discovered: 1,
            registered: 3,
            capacity: 50,
          },
        }),
    });
    const html = customMetricsHTML(env(), d);
    expect(html.match(/data-cm-reset/g)?.length).toBe(2);
    expect(html.match(/data-cm-delete/g)?.length).toBe(2);
    expect(html).toContain("Back to waiting");
    expect(html).not.toContain('data-cm-reset="e1" data-cm-id="cm1"');
  });
  it("mentions the ceiling only once it is close", () => {
    const at = (registered: number) =>
      customMetricsHTML(
        env(),
        deps({
          detail: () =>
            detail({ custom: { metrics: [metric()], discovered: 0, registered, capacity: 50 } }),
        }),
      );
    expect(at(10)).not.toContain("registered</span>");
    expect(at(45)).toContain("45/50 registered");
  });
  it("lets a long name take every line it needs", () => {
    const d = deps({
      detail: () =>
        detail({
          custom: {
            metrics: [metric({ name: "checkout_payment_retry_<total>" })],
            discovered: 1,
            registered: 1,
            capacity: 50,
          },
        }),
    });
    const html = customMetricsHTML(env(), d);
    expect(html).toContain('class="row row-wrap row-wrap-all"');
    expect(html).toContain("api · <strong>checkout_payment_retry_&lt;total&gt;</strong>");
  });
  it("says so when a metric carries no labels", () => {
    const d = deps({
      detail: () =>
        detail({ custom: { metrics: [metric()], discovered: 1, registered: 1, capacity: 50 } }),
    });
    expect(customMetricsHTML(env(), d)).toContain("no labels");
  });
});

describe("dashboardsHTML", () => {
  it("shows the block with what unlocks it when nothing can be charted yet", () => {
    const html = dashboardsHTML(env(), deps());
    expect(html).toContain("Dashboards");
    expect(html).toContain("a minute or two after the app first sends it");
    expect(html).not.toContain("data-dash-new");
  });
  it("asks to approve a metric when some are waiting", () => {
    const d = deps({
      detail: () =>
        detail({ custom: { metrics: [metric()], discovered: 1, registered: 1, capacity: 50 } }),
    });
    expect(dashboardsHTML(env(), d)).toContain("Approve a custom metric above");
  });
  it("offers a new dashboard once a metric is approved", () => {
    const d = deps({
      detail: () =>
        detail({
          custom: {
            metrics: [metric({ status: "approved" })],
            discovered: 0,
            registered: 1,
            capacity: 50,
          },
        }),
    });
    const html = dashboardsHTML(env(), d);
    expect(html).toContain("data-dash-new");
    expect(html).toContain("No dashboards yet");
  });
  it("drops the empty line once a dashboard exists", () => {
    const d = deps({
      detail: () =>
        detail({
          dashboards: [dash()],
          custom: {
            metrics: [metric({ status: "approved" })],
            discovered: 0,
            registered: 1,
            capacity: 50,
          },
        }),
    });
    expect(dashboardsHTML(env(), d)).not.toContain("No dashboards yet");
  });
  it("does not offer an editor with no approved metric to name", () => {
    const d = deps({ detail: () => detail({ dashboards: [dash()] }) });
    const html = dashboardsHTML(env(), d);
    expect(html).toContain("Checkout health");
    expect(html).not.toContain("data-dash-new");
    expect(html).toContain("Dashboards chart custom metrics");
  });
  it("offers delete on a published dashboard and discard on a proposal", () => {
    const at = (status: "active" | "proposed") =>
      dashboardsHTML(
        env(),
        deps({
          detail: () => detail({ dashboards: [dash({ status })] }),
          openDashID: () => "d1",
          dashPanels: () => [],
        }),
      );
    expect(at("active")).toContain("data-dash-remove");
    expect(at("active")).not.toContain("data-dash-del=");
    expect(at("proposed")).not.toContain("data-dash-remove");
    expect(at("proposed")).toContain("data-dash-del=");
  });
  it("leaves archived dashboards out of the tab strip", () => {
    const d = deps({
      detail: () => detail({ dashboards: [dash(), dash({ id: "d2", status: "archived" })] }),
    });
    expect(dashboardsHTML(env(), d).match(/role="tab"/g)?.length).toBe(1);
  });
  it("lists the dashboards as pill tabs over one panel, the open one selected", () => {
    const d = deps({
      detail: () =>
        detail({ dashboards: [dash(), dash({ id: "d2", name: "Pagos", status: "proposed" })] }),
      openDashID: () => "d1",
      dashPanels: () => [],
    });
    const html = dashboardsHTML(env(), d);
    expect(html).toContain('class="tabs tabs-pill" role="tablist" aria-label="Dashboards"');
    expect(html).toContain('data-dash-env="e1"');
    expect(html).toContain(
      'id="dash-e1-tab-d1" aria-selected="true" aria-controls="dash-e1-panel"',
    );
    expect(html).toContain('id="dash-e1-tab-d2" aria-selected="false"');
    expect(html).toContain('<span class="badge badge-accent">proposed</span>');
    expect(html).toContain(
      'id="dash-e1-panel" role="tabpanel" aria-labelledby="dash-e1-tab-d1" class="dash-panel"',
    );
  });
  it("selects no tab and draws no panel while only a proposal exists", () => {
    const d = deps({
      detail: () => detail({ dashboards: [dash({ status: "proposed" })] }),
    });
    const html = dashboardsHTML(env(), d);
    expect(html).toContain('role="tab"');
    expect(html).not.toContain('aria-selected="true"');
    expect(html).not.toContain('role="tabpanel"');
  });
  it("shows the review banner over a proposal", () => {
    const d = deps({
      detail: () => detail({ dashboards: [dash({ status: "proposed" })] }),
      openDashID: () => "d1",
      dashPanels: () => [],
    });
    const html = dashboardsHTML(env(), d);
    expect(html).toContain("inert until approved");
    expect(html).toContain("data-dash-approve");
  });
  it("says it is loading until the panels resolve, then empty if they are", () => {
    const base = { detail: () => detail({ dashboards: [dash()] }), openDashID: () => "d1" };
    expect(dashboardsHTML(env(), deps({ ...base }))).toContain("Loading…");
    expect(dashboardsHTML(env(), deps({ ...base, dashPanels: () => [] }))).toContain("no panels");
    expect(dashboardsHTML(env(), deps({ ...base, dashPanels: () => ["<div>x</div>"] }))).toContain(
      '<div class="chart-panels"><div>x</div></div>',
    );
  });
});

describe("evidence summary", () => {
  const testing = {
    id: "ev1",
    base_url: "https://staging.example.com",
    secret_names: ["ADMIN_PASSWORD"],
    schedule: "0 9 * * 1-5",
    schedule_tz: "UTC",
  };
  it("stays out until Evidence answered for the environment", () => {
    expect(envTestingHTML(env(), deps())).toBe("");
    expect(envTestingHTML(env(), deps({ testing: () => undefined }))).toBe("");
  });
  it("sums up the address, logins and schedule and links to Evidence", () => {
    const html = envTestingHTML(env(), deps({ testing: () => testing }));
    expect(html).toContain("https://staging.example.com");
    expect(html).toContain("1 test login");
    expect(html).toContain("Weekdays at 09:00 (UTC)");
    expect(html).toContain('href="/evidence/?tab=environments&amp;env=ev1"');
  });
  it("says what is missing", () => {
    const html = envTestingHTML(
      env(),
      deps({ testing: () => ({ ...testing, base_url: "", secret_names: null, schedule: "" }) }),
    );
    expect(html).toContain("No address for tests");
    expect(html).toContain("No test logins");
    expect(html).toContain("Runs when someone starts it");
  });
  it("shows the display name with the name beside it", () => {
    const html = envCardHTML(env({ display_name: "Production EU" }), deps());
    expect(html).toContain('class="env-name">Production EU<');
    expect(html).toContain('class="env-slug">prod<');
  });
});

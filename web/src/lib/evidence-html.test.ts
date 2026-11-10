import { describe, expect, it } from "vitest";

import {
  batchBarHTML,
  batchHTML,
  batchStatusHTML,
  capturesHTML,
  caseCardHTML,
  caseEnvs,
  caseGroupsGridHTML,
  caseGroupsRowsHTML,
  caseRowHTML,
  chaptersHTML,
  checksTableHTML,
  countsLine,
  envCardHTML,
  envGroupsHTML,
  envResultsHTML,
  failuresHTML,
  historyHTML,
  offProps,
  sandboxCardHTML,
  sandboxFailureHTML,
  sandboxReasonHTML,
  sandboxStepsHTML,
  statusDotHTML,
  statusPillHTML,
  type ListCtx,
  runnerErrorHTML,
} from "./evidence-html";
import {
  groupEnvs,
  type EvBatch,
  type EvCase,
  type EvEnvironment,
  type EvRun,
} from "./evidence-view";

function evCase(over: Partial<EvCase> = {}): EvCase {
  return {
    id: "c1",
    key: "EV-1",
    title: "Checkout <applies> a code",
    description: "",
    steps: "",
    expected: "",
    priority: "high",
    tags: ["checkout"],
    project_id: "p1",
    status: "pass",
    take_source: "agent",
    take_updated_at: null,
    last_run_at: null,
    created_at: "2026-10-01T10:00:00Z",
    updated_at: "2026-10-01T10:00:00Z",
    ...over,
  };
}

function env(over: Partial<EvEnvironment> = {}): EvEnvironment {
  return {
    id: "e1",
    name: "Staging",
    base_url: "https://staging.example.com",
    allowed_hosts: [],
    secret_names: [],
    schedule: "",
    schedule_tz: "UTC",
    ...over,
  };
}

function batch(over: Partial<EvBatch> = {}): EvBatch {
  return {
    id: "b1",
    environment_id: "e1",
    trigger: "schedule",
    trigger_ref: "",
    status: "done",
    created_at: "2026-10-01T10:00:00Z",
    counts: { total: 4, passed: 3, failed: 1 },
    ...over,
  };
}

function tagWith(html: string, attr: string): string {
  const tag = html.match(new RegExp(`<(?:button|a)\\b[^>]*${attr}[^>]*>`));
  if (!tag) throw new Error(`no button with ${attr}`);
  return tag[0];
}

const ctx = (over: Partial<ListCtx> = {}): ListCtx => ({
  envs: [env()],
  projects: new Map([["p1", "Shop"]]),
  canWrite: true,
  artifactURL: (id) => `/evidence/artifacts/${id}`,
  ...over,
});

describe("offProps", () => {
  it("adds nothing for an allowed action", () => {
    expect(offProps({ allowed: true })).toEqual({});
  });

  it("turns the button off and says why in its tooltip", () => {
    expect(offProps({ allowed: false, reason: "viewer" })).toEqual({
      disabled: true,
      title: "Your role can look but not change anything here.",
    });
  });
});

describe("buttons", () => {
  it("draws the card's action and menu with the button component", () => {
    const html = caseCardHTML(evCase(), ctx());
    expect(tagWith(html, 'data-act="run"')).toContain('class="btn btn-sm"');
    const menu = tagWith(html, 'data-act="menu"');
    expect(menu).toContain('class="icon-btn icon-btn-lg"');
    expect(menu).toContain('aria-label="More actions for EV-1"');
    expect(menu).toContain('aria-haspopup="menu"');
    const draft = caseCardHTML(evCase({ status: "draft" }), ctx());
    expect(tagWith(draft, 'data-act="record"')).toContain('class="btn btn-sm"');
  });

  it("gives an environment's delete a danger icon button with a name", () => {
    const del = tagWith(envCardHTML(env()), 'data-act="delete-env"');
    expect(del).toContain('class="icon-btn icon-btn-lg icon-btn-danger ev-env-del"');
    expect(del).toContain('aria-label="Delete Staging"');
  });
});

describe("status pill", () => {
  it("carries the tone, the label and a hint", () => {
    const html = statusPillHTML("fail");
    expect(html).toContain('class="badge badge-danger badge-pill"');
    expect(html).toContain(">Failed<");
    expect(html).toContain('title="A check failed on the last run."');
  });

  it("paints a broken run purple, a running one as under way and a draft hollow", () => {
    expect(statusPillHTML("error")).toContain('class="badge badge-accent badge-pill"');
    expect(statusPillHTML("running")).toContain('class="badge badge-info badge-pill"');
    expect(statusPillHTML("")).toContain('class="badge badge-pill badge-hollow"');
  });

  it("raises the pill over a poster and places it", () => {
    expect(statusPillHTML("pass", { raised: true, cls: "ev-card-pill" })).toContain(
      'class="badge badge-ok badge-pill badge-raised ev-card-pill"',
    );
  });
});

describe("status dot", () => {
  it("is a decorative Dot in the status's tone; a live run breathes", () => {
    expect(statusDotHTML("pass")).toBe('<span class="dot dot-ok" aria-hidden="true"></span>');
    expect(statusDotHTML("error")).toContain('class="dot dot-special"');
    expect(statusDotHTML("running")).toContain('class="dot dot-accent dot-pulse"');
    expect(statusDotHTML("")).toContain('class="dot dot-empty"');
  });
});

describe("case card and row", () => {
  it("escapes the title and links to the drawer", () => {
    const html = caseCardHTML(evCase(), ctx());
    expect(html).toContain("Checkout &lt;applies&gt; a code");
    expect(html).toContain('href="#case-EV-1"');
    expect(html).toContain("Shop");
    expect(html).toContain('data-act="run"');
  });

  it("is a Card whose title opens the case, with the poster and pill on top", () => {
    const html = caseCardHTML(evCase(), ctx());
    expect(html).toMatch(
      /^<article class="card card-link ev-card" data-case="c1" data-key="EV-1">/,
    );
    expect(html).toContain('<div class="card-media">');
    expect(html).toContain('<h3><a class="card-cover" href="#case-EV-1">');
    expect(html).toContain('<div class="card-foot">');
  });

  it("heads each project's cases, over the cards and as a row across the list", () => {
    const groups = [{ projectId: "p1", projectName: "Shop", cases: [evCase()] }];
    const grid = caseGroupsGridHTML(groups, ctx());
    expect(grid).toMatch(/^<div class="group-head"><svg[^>]*><use href="#i-folder"\/><\/svg>/);
    expect(grid).toContain(
      '<h2 class="group-head-title">Shop</h2><span class="group-head-meta">1 case</span>',
    );
    const rows = caseGroupsRowsHTML(groups, ctx());
    expect(rows).toMatch(
      /^<tr class="group-head-row"><th colspan="6"><div class="group-head group-head-band">/,
    );
  });

  it("stacks the compact row on a phone, each cell named", () => {
    const html = caseRowHTML(evCase(), ctx());
    expect(html).toMatch(/^<tr data-case="c1" data-key="EV-1">/);
    expect(html).toContain('data-label="Latest result"');
    expect(html).toContain('class="ev-col-project" data-label="Project"');
    expect(html).toContain('<td class="cell-shrink actions-cell">');
  });

  it("offers recording for a case without one", () => {
    const html = caseCardHTML(evCase({ status: "draft" }), ctx());
    expect(html).toContain('data-act="record"');
    expect(html).not.toContain('data-act="run"');
    expect(html).toContain("Not recorded yet");
  });

  it("shows the newest poster", () => {
    const c = evCase({
      latest: {
        e1: { run_id: "r1", status: "pass", finished_at: "2026-10-01T10:00:00Z", poster_id: "p9" },
      },
    });
    expect(caseCardHTML(c, ctx())).toContain('src="/evidence/artifacts/p9"');
  });

  it("turns actions off for read-only viewers, saying why", () => {
    const off = ctx({ canWrite: false, writeReason: "viewer" });
    for (const html of [caseCardHTML(evCase(), off), caseRowHTML(evCase(), off)]) {
      const run = tagWith(html, 'data-act="run"');
      expect(run).toContain(" disabled ");
      expect(run).toMatch(/title="Your role can look/);
      expect(html).not.toContain('data-act="menu"');
    }
  });

  it("lists results per environment and counts the rest", () => {
    const envs = [env(), env({ id: "e2", name: "Prod" }), env({ id: "e3", name: "QA" })];
    const html = envResultsHTML(evCase(), envs, 2);
    expect(html).toContain("Not run here");
    expect(html).toContain("+1 more environment");
    expect(envResultsHTML(evCase(), [])).toBe("");
  });

  it("names the environments the way the screen asks", () => {
    const html = envResultsHTML(evCase(), [env()], 3, (e) => `Shop · ${e.name}`);
    expect(html).toContain(">Shop · Staging<");
  });

  it("leaves out a Sandbox that can't start until it has a result", () => {
    const sandbox = env({
      id: "sb",
      name: "sandbox",
      kind: "sandbox",
      project_id: "p1",
      sandbox: {
        environment_id: "sb",
        available: false,
        reason: "no_init_script",
        port: 0,
        default_branch: "main",
        init_script_url: "/project/?id=p1#repo",
      },
    });
    expect(caseEnvs(evCase(), [env(), sandbox]).map((e) => e.id)).toEqual(["e1"]);
    const ran = evCase({
      latest: { sb: { run_id: "r1", status: "error", finished_at: null, poster_id: "" } },
    });
    expect(caseEnvs(ran, [env(), sandbox]).map((e) => e.id)).toEqual(["sb", "e1"]);
    expect(caseEnvs(ran, [env(), sandbox], "e1").map((e) => e.id)).toEqual(["e1", "sb"]);
  });
});

describe("batches", () => {
  const counts = { total: 5, passed: 2, failed: 1, blocked: 0, errored: 1 };

  it("draws one segment per outcome and names them all", () => {
    const html = batchBarHTML(counts);
    expect(html).toMatch(/^<span class="seg-progress"/);
    expect(html).toContain('data-tone="ok" style="flex-basis:40%"');
    expect(html).toContain('data-tone="special"');
    expect(html).not.toContain('data-tone="warn"');
    expect(html).toContain('data-tone="track" style="flex-basis:20%"');
    expect(html).toContain('aria-label="2 passed, 1 failed, 1 couldn&#39;t run, 1 to go"');
    expect(countsLine(counts)).toBe("2 passed · 1 failed · 1 couldn't run · 1 to go");
  });

  it("counts canceled runs as canceled, not as waiting", () => {
    expect(countsLine({ ...counts, canceled: 1 })).toBe(
      "2 passed · 1 failed · 1 couldn't run · 1 canceled",
    );
  });

  it("gives a finished batch a verdict", () => {
    expect(batchStatusHTML(batch())).toContain("Something failed");
    expect(batchStatusHTML(batch({ counts: { total: 2, passed: 2 } }))).toContain("All passed");
    expect(batchStatusHTML(batch({ status: "running" }))).toContain("badge-info");
  });

  it("offers cancel only while it runs, and lists runs when open", () => {
    const live = batch({ status: "running" });
    const closed = batchHTML(live, {
      envName: "Staging",
      open: false,
      canWrite: true,
      caseNames: new Map(),
    });
    expect(closed).toContain('data-act="cancel-batch"');
    expect(closed).toContain('aria-expanded="false"');
    expect(closed).toContain("On schedule");
    const r: EvRun = {
      id: "r1",
      batch_id: "b1",
      case_id: "c1",
      environment_id: "e1",
      status: "fail",
      created_at: "",
      video_id: "v1",
      checks: [{ label: "", expected: "", observed: "", pass: true, measured: true }],
    };
    const open = batchHTML(batch(), {
      envName: "",
      open: true,
      canWrite: true,
      runs: [r],
      caseNames: new Map([["c1", { key: "EV-1", title: "Checkout" }]]),
    });
    expect(open).not.toContain("cancel-batch");
    expect(open).toContain("Deleted environment");
    expect(open).toContain('href="#case-EV-1/r1"');
    expect(open).toContain("1/1 checks");
    expect(open).toContain(">Watch<");
    expect(closed).toMatch(
      /<button class="row ev-batch-head" type="button" aria-expanded="false" data-act="toggle-batch" aria-controls="ev-batch-runs-b1">/,
    );
    expect(open).toContain(
      '<ul class="row-list ev-runs"><li><a class="row ev-run" href="#case-EV-1/r1">',
    );
  });
});

describe("environment card", () => {
  it("lists schedule, deploy and logins, with every action allowed", () => {
    const html = envCardHTML(
      env({
        schedule: "0 9 * * 1-5",
        on_deploy: true,
        deploy: { available: true, repo: "acme/web", environment: "staging" },
        secret_names: ["A", "B"],
      }),
      { lastBatch: batch() },
    );
    expect(html).toContain("Weekdays at 09:00 (UTC)");
    expect(html).toContain("After each deploy of acme/web to staging");
    expect(html).toContain("2 test logins");
    expect(html).toContain('data-act="delete-env"');
    expect(html).toContain('id="ev-env-e1"');
    expect(html).toContain("Something failed");
  });

  it("turns actions off with the reason instead of hiding them", () => {
    const viewer = { allowed: false, reason: "viewer" };
    const html = envCardHTML(
      env({ can: { run: viewer, record: viewer, edit: viewer, secrets: viewer, delete: viewer } }),
    );
    expect(html).toContain("Runs only when someone starts it");
    expect(html).toContain("Nothing has run here yet.");
    const run = tagWith(html, 'data-act="run-env"');
    expect(run).toContain(" disabled ");
    expect(run).toMatch(/title="Your role can look/);
    const del = tagWith(html, 'data-act="delete-env"');
    expect(del).toContain(" disabled ");
    expect(del).toMatch(/title="Your role can look/);
    expect(del).toContain('aria-label="');
    expect(html).toContain(">View<");
    expect(html).not.toContain("ev-env-limit");
  });

  it("names a project environment and sends its delete to the project page", () => {
    const html = envCardHTML(
      env({
        kind: "project",
        project_id: "p1",
        name: "staging",
        display_name: "Staging EU",
        is_production: true,
        base_url: "",
        can: {
          delete: { allowed: false, reason: "delete_on_project" },
          edit: { allowed: false, reason: "production" },
          run: { allowed: false, reason: "no_url" },
        },
      }),
    );
    expect(html).toContain(">Staging EU<");
    expect(html).toContain('class="ev-env-slug ev-mono">staging<');
    expect(html).toContain("Production");
    expect(html).toContain("No address yet");
    expect(html).toContain('href="/project/?id=p1#environments"');
    expect(html).not.toContain('data-act="delete-env"');
    expect(html).toContain("Open project</a>");
    expect(html).not.toContain("i-trash");
    expect(html).toContain("Only workspace admins can change production.");
    expect(html).toContain('title="It has no address yet."');
  });

  it("offers moving a standalone site to admins only", () => {
    expect(envCardHTML(env({ kind: "standalone" }))).toContain('data-act="link-env"');
    expect(
      envCardHTML(
        env({ kind: "standalone", can: { link: { allowed: false, reason: "admin_only" } } }),
      ),
    ).not.toContain("link-env");
  });
});

describe("sandbox", () => {
  const sandbox = (over: Partial<EvEnvironment> = {}): EvEnvironment =>
    env({
      id: "sb",
      kind: "sandbox",
      name: "sandbox",
      project_id: "p1",
      project_name: "Shop",
      base_url: "",
      sandbox: {
        environment_id: "sb",
        available: true,
        port: 3000,
        default_branch: "main",
        init_script_url: "/project/?id=p1#init-script",
      },
      ...over,
    });

  it("shows the branch and port and never offers delete", () => {
    const html = sandboxCardHTML(sandbox());
    expect(html).toContain("Starts from main unless you pick another branch");
    expect(html).toContain("App on port 3000");
    expect(html).toContain('data-act="run-env"');
    expect(html).not.toContain("delete-env");
    expect(html).toMatch(/^<article id="ev-env-sb" class="card card-dashed ev-env ev-sandbox"/);
  });

  it("links to the init script when it can't come up", () => {
    const html = sandboxCardHTML(
      sandbox({
        sandbox: {
          environment_id: "sb",
          available: false,
          reason: "no_init_script",
          port: 0,
          default_branch: "main",
          init_script_url: "/project/?id=p1#init-script",
        },
        can: { run: { allowed: false, reason: "no_init_script" } },
      }),
      { canAdmin: true },
    );
    expect(html).toMatch(
      /^<article id="ev-env-sb" class="card card-dashed card-off ev-env ev-sandbox"/,
    );
    expect(html).toContain("The project has no init script");
    expect(html).toContain('href="/project/?id=p1#init-script"');
    expect(html).toContain("Write the init script");
    expect(tagWith(html, 'data-act="run-env"')).toContain(" disabled ");
  });

  it("tells a caller who can't save the init script to ask an admin", () => {
    const html = sandboxReasonHTML("no_init_script", "/project/?id=p1#init-script");
    expect(html).toContain("Ask a workspace admin to add it.");
    expect(html).not.toContain("/project/?id=p1");
  });

  it("groups it after the project's environments, with an empty state", () => {
    const groups = groupEnvs(
      [sandbox(), env({ id: "s1", kind: "standalone" })],
      [{ id: "p1", name: "Shop" }],
    );
    const html = envGroupsHTML(groups, {
      lastBatch: () => undefined,
      canCreate: { allowed: true },
      headings: true,
    });
    expect(html).toContain("This project has no environment yet.");
    expect(html).toContain('data-act="new-env" data-project="p1"');
    expect(html.indexOf("ev-env-blank")).toBeLessThan(html.indexOf("ev-sandbox"));
    expect(html).toContain('class="empty-state empty-state-sm empty-state-dashed ev-env-blank"');
    expect(html).toContain("Other sites");
    expect(html).toContain('<h2 class="group-head-title">Shop</h2>');
  });

  it("walks the bring-up steps", () => {
    const html = sandboxStepsHTML(
      batch({ status: "running", sandbox_status: "starting", sandbox_phase: "tools" }),
    );
    expect(html).toMatch(/^<ol class="step-list step-list-inline" aria-label="[^"]+">/);
    const states = Array.from(html.matchAll(/data-state="(\w+)"/g), (m) => m[1]);
    expect(states).toEqual(["done", "done", "now", "pending"]);
    expect(html).toMatch(
      /<li class="step-item step-item-now" data-state="now" aria-current="step">.*Running the init script/,
    );
  });

  it("tells a failed bring-up apart from a failed case", () => {
    const b = batch({
      status: "error",
      sandbox_status: "failed",
      sandbox_error: "init script exited with 1",
      source_branch: "feat/x",
      canceled: 3,
      counts: { total: 3 },
    });
    expect(batchStatusHTML(b)).toContain('class="badge badge-warn badge-pill badge-edge"');
    expect(batchStatusHTML(b)).toContain("Didn&#39;t start");
    const fail = sandboxFailureHTML(b, { allowed: true });
    expect(fail).toContain("Init script exited with 1");
    expect(fail).toContain('data-act="retry-batch"');
    expect(fail).toMatch(/^<div class="callout callout-warn ev-bringup-fail" role="status">/);
    const html = batchHTML(b, {
      envName: "Sandbox",
      open: false,
      canWrite: true,
      canRetry: { allowed: true },
      log: "npm ERR! missing script",
      caseNames: new Map(),
    });
    expect(html).toContain("3 cases not run");
    expect(html).toContain("npm ERR! missing script");
    expect(html).toContain("feat/x");
  });

  it("sends a failed bring-up to the init script or to Thulu by its reason", () => {
    const failed = (reason: string) =>
      batch({
        status: "error",
        sandbox_status: "failed",
        sandbox_reason: reason,
        sandbox_error: "x",
        sandbox_log: "log",
      });
    const fix = { initScriptURL: "/project/?id=p1#init-script", canAdmin: true };
    const script = sandboxFailureHTML(failed("setup_failed"), { allowed: true }, fix);
    expect(script).toContain("Check the init script, then try again.");
    expect(script).toContain('href="/project/?id=p1#init-script"');
    expect(script).toContain("Show what the init script printed");
    const asked = sandboxFailureHTML(
      failed("start_failed"),
      { allowed: true },
      { ...fix, canAdmin: false },
    );
    expect(asked).toContain("Ask a workspace admin to check the init script");
    expect(asked).not.toContain("/project/?id=p1");
    const ours = sandboxFailureHTML(failed("launch_failed"), { allowed: true }, fix);
    expect(ours).toContain("on Thulu&#39;s side");
    expect(ours).toContain("Show details");
    expect(ours).not.toContain("init script");
  });
});

describe("drawer parts", () => {
  it("tells measured checks from what the agent saw", () => {
    const html = checksTableHTML([
      { label: "Total", expected: "$90", observed: "$100", pass: false, measured: true },
      { label: "Banner", expected: "shown", observed: "shown", pass: true, measured: false },
    ]);
    expect(html.match(/data-tone="danger"/g)?.length).toBe(1);
    expect(html).toContain(
      'class="list-table list-table-top list-table-compact list-table-stack ev-checks"',
    );
    expect(html).toContain("Measured");
    expect(html).toContain("Seen by the agent");
    expect(checksTableHTML([])).toContain("This run has no checks.");
  });

  it("lists failures by step", () => {
    const html = failuresHTML([{ step: 3, kind: "click", label: "", error: "not <found>" }]);
    expect(html).toContain("Step 3 · click");
    expect(html).toContain('class="callout callout-danger"');
    expect(html).toContain("not &lt;found&gt;");
    expect(failuresHTML([])).toBe("");
  });

  it("numbers captures and chapters", () => {
    expect(capturesHTML(["a", "b"], (id) => `/x/${id}`)).toContain('aria-label="Capture 2 of 2"');
    expect(chaptersHTML([{ start: 65, title: "Pay" }])).toContain('data-at="65"');
    expect(chaptersHTML([{ start: 65, title: "Pay" }])).toContain("1:05");
  });

  it("marks the run on show in the history", () => {
    const runs: EvRun[] = [
      {
        id: "r1",
        batch_id: "",
        case_id: "",
        environment_id: "e1",
        status: "running",
        created_at: "",
      },
      {
        id: "r2",
        batch_id: "",
        case_id: "",
        environment_id: "e1",
        status: "pass",
        created_at: "",
        duration_ms: 42_000,
        trigger: "deploy",
      },
    ];
    const html = historyHTML(runs, new Map([["e1", "Staging"]]), "r2");
    expect(html).toContain('data-run="r2"');
    expect(html).toContain('aria-current="true"');
    expect(html).toContain("42s");
    expect(html).toContain("After a deploy");
    expect(html).toContain(">Running<");
    expect(historyHTML([], new Map(), "")).toContain("No runs yet.");
  });
});

describe("runner errors", () => {
  it("says a known cause and folds the stored text", () => {
    const html = runnerErrorHTML(
      "per-run browser image x: denied",
      "browser_image",
      "ev-run-error",
    );
    expect(html).toContain("browser image");
    expect(html).toContain('class="callout callout-danger ev-run-error"');
    expect(html).toContain('<details class="disclosure disclosure-code"');
    expect(html).toContain("per-run browser image x: denied");
    expect(html).toContain(
      "code-block code-block-multi code-block-wrap code-block-h-sm code-block-danger",
    );
  });

  it("shows an unknown error as it is", () => {
    expect(runnerErrorHTML("boom", "", "ev-batch-error")).toBe(
      '<p class="callout callout-danger callout-bare ev-batch-error">boom</p>',
    );
    expect(runnerErrorHTML("boom", "", "ev-run-error", "special")).toBe(
      '<p class="callout callout-special callout-bare ev-run-error">boom</p>',
    );
  });
});

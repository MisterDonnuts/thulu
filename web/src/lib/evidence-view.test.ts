import { describe, expect, it } from "vitest";

import {
  sameName,
  scriptFailure,
  sentence,
  EMPTY_CASE_FILTERS,
  SECRET_NAME,
  batchCounts,
  batchDuration,
  batchesSignature,
  batchesURL,
  caseFiltersFromQuery,
  caseFiltersToQuery,
  caseHash,
  casesURL,
  checksSummary,
  ciHookCurl,
  clockLabel,
  cronFromSchedule,
  scheduleTimeOK,
  caseRunsOn,
  deployLabel,
  durationLabel,
  envLabel,
  envLongLabel,
  envsForProjects,
  groupCases,
  groupEnvs,
  hasCaseFilters,
  hasOwnCaseFilters,
  isHttpURL,
  isNotRun,
  isRecorded,
  isValidCron,
  permission,
  pickProject,
  reasonLabel,
  sandboxStep,
  showProjectPicker,
  slugify,
  lastBatchByEnv,
  latestPoster,
  libraryIndex,
  listOf,
  normalizeSecretName,
  pageQuery,
  parseCaseHash,
  parseChapters,
  parseHosts,
  parseTags,
  parseTime,
  pendingCount,
  pickRun,
  scheduleFromCron,
  scheduleLabel,
  statusLabel,
  statusTone,
  tabFromQuery,
  tagsOf,
  totalOf,
  triggerLabel,
  unwrap,
  visibleStatuses,
  cardEnv,
  cardStatus,
  causeLabel,
  recordingAgentLabel,
  recordingPromptHref,
  recordingPick,
  scheduleScopeLine,
  sortCases,
  type EvBatch,
  type EvCase,
  type EvEnvironment,
  type EvRun,
} from "./evidence-view";

function evCase(over: Partial<EvCase> = {}): EvCase {
  return {
    id: "c1",
    key: "EV-1",
    title: "Checkout applies a code",
    description: "",
    steps: "",
    expected: "",
    priority: "medium",
    tags: [],
    project_id: "",
    status: "ready",
    take_source: "agent",
    take_updated_at: null,
    last_run_at: null,
    created_at: "2026-10-01T10:00:00Z",
    updated_at: "2026-10-01T10:00:00Z",
    ...over,
  };
}

function run(over: Partial<EvRun> = {}): EvRun {
  return {
    id: "r1",
    batch_id: "b1",
    case_id: "c1",
    environment_id: "e1",
    status: "pass",
    created_at: "2026-10-01T10:00:00Z",
    ...over,
  };
}

function batch(over: Partial<EvBatch> = {}): EvBatch {
  return {
    id: "b1",
    environment_id: "e1",
    trigger: "manual",
    trigger_ref: "",
    status: "done",
    created_at: "2026-10-01T10:00:00Z",
    ...over,
  };
}

describe("responses", () => {
  it("reads a list bare or under its key", () => {
    expect(listOf<number>([1, 2], "cases")).toEqual([1, 2]);
    expect(listOf<number>({ cases: [3] }, "cases")).toEqual([3]);
    expect(listOf<number>({ other: [3] }, "cases")).toEqual([]);
    expect(listOf<number>(null, "cases")).toEqual([]);
  });

  it("unwraps an object and keeps its parts beside it", () => {
    expect(
      unwrap<{ id: string; runs: number[] }>({ batch: { id: "b1" }, runs: [1] }, "batch"),
    ).toEqual({ id: "b1", runs: [1] });
    expect(unwrap<{ id: string }>({ id: "b2" }, "batch")).toEqual({ id: "b2" });
  });
});

describe("statuses", () => {
  it("labels known statuses and passes unknown ones through", () => {
    expect(statusLabel("draft")).toBe("Needs recording");
    expect(statusLabel("pass")).toBe("Passed");
    expect(statusLabel("weird")).toBe("weird");
  });

  it("gives each status a tone", () => {
    expect(statusTone("pass")).toBe("ok");
    expect(statusTone("fail")).toBe("bad");
    expect(statusTone("error")).toBe("err");
    expect(statusTone("blocked")).toBe("warn");
    expect(statusTone("running")).toBe("live");
    expect(statusTone("ready")).toBe("idle");
    expect(statusTone("draft")).toBe("none");
  });

  it("shows chips for statuses with cases plus the picked one, problems first", () => {
    expect(visibleStatuses({ pass: 3, fail: 1, draft: 0 }, "")).toEqual(["fail", "pass"]);
    expect(visibleStatuses({ pass: 3 }, "blocked")).toEqual(["blocked", "pass"]);
    expect(totalOf({ pass: 3, fail: 1, draft: 2 })).toBe(6);
  });

  it("counts the cases that never ran on an environment last", () => {
    expect(visibleStatuses({ pass: 1, not_run: 2 }, "")).toEqual(["pass", "not_run"]);
    expect(totalOf({ fail: 1, not_run: 2 })).toBe(3);
    expect(statusLabel("not_run")).toBe("Not run here");
  });
});

describe("address bar", () => {
  it("picks the tab, Cases by default", () => {
    expect(tabFromQuery("")).toBe("cases");
    expect(tabFromQuery("?tab=runs")).toBe("runs");
    expect(tabFromQuery("?tab=nope")).toBe("cases");
  });

  it("leaves tab out for Cases and puts it first otherwise", () => {
    expect(pageQuery("cases", "?status=fail")).toBe("?status=fail");
    expect(pageQuery("cases", "")).toBe("");
    expect(pageQuery("runs", "?environment_id=e1")).toBe("?tab=runs&environment_id=e1");
    expect(pageQuery("environments", "?tab=cases")).toBe("?tab=environments");
  });

  it("round-trips case filters under the API's names", () => {
    const f = {
      ...EMPTY_CASE_FILTERS,
      status: "fail" as const,
      projectId: "p1",
      tags: ["checkout", "smoke"],
      priority: "high" as const,
      envId: "e1",
      q: " log in ",
    };
    const q = caseFiltersToQuery(f);
    expect(q).toBe(
      "?status=fail&project_id=p1&tag=checkout%2Csmoke&priority=high&environment_id=e1&q=log+in",
    );
    expect(caseFiltersFromQuery(q)).toEqual({ ...f, q: "log in" });
    expect(casesURL(f)).toBe(`/evidence/cases${q}`);
    expect(hasCaseFilters(EMPTY_CASE_FILTERS)).toBe(false);
    expect(caseFiltersFromQuery("?status=bogus").status).toBe("");
    expect(caseFiltersFromQuery("?priority=urgent").priority).toBe("");
    expect(caseFiltersFromQuery("?tag=checkout").tags).toEqual(["checkout"]);
  });

  it("takes 'not run here' only with an environment", () => {
    expect(caseFiltersFromQuery("?status=not_run").status).toBe("");
    expect(caseFiltersFromQuery("?status=not_run&environment_id=e1").status).toBe("not_run");
  });

  it("asks for the cases with no project next to a picked project", () => {
    const f = { ...EMPTY_CASE_FILTERS, projectId: "p1", includeUnassigned: true };
    expect(caseFiltersToQuery(f)).toBe("?project_id=p1&include_unassigned=1");
    expect(caseFiltersFromQuery("?project_id=p1&include_unassigned=1")).toEqual(f);
    expect(caseFiltersToQuery({ ...EMPTY_CASE_FILTERS, includeUnassigned: true })).toBe("");
    expect(hasOwnCaseFilters(f, true)).toBe(false);
    expect(hasOwnCaseFilters(f, false)).toBe(true);
    expect(hasOwnCaseFilters({ ...f, tags: ["x"] }, true)).toBe(true);
    expect(hasOwnCaseFilters({ ...f, priority: "low" }, true)).toBe(true);
  });

  it("builds the batch feed URL", () => {
    expect(batchesURL("", 50)).toBe("/evidence/batches?limit=50");
    expect(batchesURL("e 1")).toBe("/evidence/batches?environment_id=e+1&limit=30");
  });

  it("round-trips the case deep link", () => {
    expect(caseHash("EV-3")).toBe("#case-EV-3");
    expect(parseCaseHash("#case-EV-3")).toEqual({ key: "EV-3", runId: "" });
    expect(parseCaseHash(caseHash("EV-3", "r9"))).toEqual({ key: "EV-3", runId: "r9" });
    expect(parseCaseHash("#task-1")).toBeNull();
    expect(parseCaseHash("#case-")).toBeNull();
    expect(parseCaseHash("#case-%E0")).toEqual({ key: "%E0", runId: "" });
  });
});

describe("times", () => {
  it("formats durations", () => {
    expect(durationLabel(0)).toBe("");
    expect(durationLabel(null)).toBe("");
    expect(durationLabel(400)).toBe("1s");
    expect(durationLabel(192_000)).toBe("3m 12s");
  });

  it("measures a batch from start to finish, or to now", () => {
    const b = batch({ started_at: "2026-10-01T10:00:00Z", finished_at: "2026-10-01T10:01:30Z" });
    expect(batchDuration(b)).toBe("1m 30s");
    const live = batch({ started_at: "2026-10-01T10:00:00Z", finished_at: null });
    expect(batchDuration(live, Date.parse("2026-10-01T10:00:42Z"))).toBe("42s");
    expect(batchDuration(batch())).toBe("");
  });

  it("formats chapter times", () => {
    expect(clockLabel(7)).toBe("0:07");
    expect(clockLabel(3723)).toBe("1:02:03");
  });
});

describe("batches", () => {
  it("reads counts nested or flat", () => {
    expect(batchCounts(batch({ counts: { total: 4, passed: 3, failed: 1 } }))).toEqual({
      total: 4,
      passed: 3,
      failed: 1,
      blocked: 0,
      errored: 0,
      canceled: 0,
    });
    expect(batchCounts(batch({ total: 2, passed: 1 })).total).toBe(2);
    expect(pendingCount({ total: 5, passed: 2, failed: 1, blocked: 0, errored: 0 })).toBe(2);
    expect(
      pendingCount({ total: 5, passed: 2, failed: 1, blocked: 0, errored: 0, canceled: 2 }),
    ).toBe(0);
  });

  it("says who started a batch", () => {
    expect(triggerLabel("manual", "")).toBe("Run by hand");
    expect(triggerLabel("manual", "ana")).toBe("Run by ana");
    expect(triggerLabel("deploy", "v1.2")).toBe("After deploy v1.2");
    expect(triggerLabel("api", "")).toBe("From CI");
    expect(triggerLabel("other", "")).toBe("other");
  });

  it("keeps the newest batch of each environment", () => {
    const m = lastBatchByEnv([
      batch({ id: "new", environment_id: "e1" }),
      batch({ id: "old", environment_id: "e1" }),
      batch({ id: "x", environment_id: "e2" }),
    ]);
    expect(m.get("e1")?.id).toBe("new");
    expect(m.get("e2")?.id).toBe("x");
  });

  it("changes the signature when a count moves", () => {
    const a = batchesSignature([batch({ status: "running", counts: { total: 3, passed: 1 } })]);
    const b = batchesSignature([batch({ status: "running", counts: { total: 3, passed: 2 } })]);
    expect(a).not.toBe(b);
  });
});

describe("cases", () => {
  it("takes the poster of the newest finished run", () => {
    const c = evCase({
      latest: {
        e1: { run_id: "r1", status: "pass", finished_at: "2026-10-01T10:00:00Z", poster_id: "p1" },
        e2: { run_id: "r2", status: "fail", finished_at: "2026-10-02T10:00:00Z", poster_id: "p2" },
        e3: { run_id: "r3", status: "pass", finished_at: "2026-10-03T10:00:00Z", poster_id: "" },
      },
    });
    expect(latestPoster(c)).toBe("p2");
    expect(latestPoster(evCase())).toBe("");
  });

  it("knows whether a case is recorded", () => {
    expect(isRecorded(evCase({ status: "draft" }))).toBe(false);
    expect(isRecorded(evCase({ status: "pass" }))).toBe(true);
    expect(isRecorded(evCase({ status: "pass", has_take: false }))).toBe(false);
  });

  it("collects and parses tags", () => {
    expect(
      tagsOf([evCase({ tags: ["b", "a"] }), evCase({ tags: null }), evCase({ tags: ["a"] })]),
    ).toEqual(["a", "b"]);
    expect(parseTags("checkout, smoke ,, #login, smoke")).toEqual(["checkout", "smoke", "login"]);
  });

  it("picks the run the link names, else the newest finished one", () => {
    const runs = [
      run({ id: "live", status: "running" }),
      run({ id: "done", status: "fail" }),
      run({ id: "other", environment_id: "e2" }),
    ];
    expect(pickRun(runs, "")?.id).toBe("done");
    expect(pickRun(runs, "other")?.id).toBe("other");
    expect(pickRun(runs, "", "e2")?.id).toBe("other");
    expect(pickRun([run({ id: "q", status: "queued" })], "")?.id).toBe("q");
    expect(pickRun([], "")).toBeNull();
  });

  it("summarizes checks", () => {
    const check = (pass: boolean) => ({
      label: "",
      expected: "",
      observed: "",
      pass,
      measured: true,
    });
    expect(checksSummary([check(true), check(false), check(true)])).toEqual({
      passed: 2,
      total: 3,
    });
  });

  it("indexes the task cases already in the library", () => {
    const m = libraryIndex([
      evCase({ key: "EV-4", source_task_id: "t1", source_case_key: "TC-2" }),
      evCase({ key: "EV-5" }),
    ]);
    expect([...m]).toEqual([["t1/TC-2", "EV-4"]]);
  });
});

describe("schedules", () => {
  it("reads presets back from a cron", () => {
    expect(scheduleFromCron("")).toEqual({ preset: "off", time: "09:00", cron: "" });
    expect(scheduleFromCron("15 * * * *")).toEqual({
      preset: "hourly",
      time: "00:15",
      cron: "15 * * * *",
    });
    expect(scheduleFromCron("0 9 * * *").preset).toBe("daily");
    expect(scheduleFromCron("30 7  * * 1-5")).toEqual({
      preset: "weekdays",
      time: "07:30",
      cron: "30 7 * * 1-5",
    });
    expect(scheduleFromCron("0 9 1 * *").preset).toBe("custom");
  });

  it("writes a cron from a form", () => {
    expect(cronFromSchedule({ preset: "off", time: "09:00", cron: "" })).toBe("");
    expect(cronFromSchedule({ preset: "hourly", time: "00:05", cron: "" })).toBe("5 * * * *");
    expect(cronFromSchedule({ preset: "daily", time: "18:45", cron: "" })).toBe("45 18 * * *");
    expect(cronFromSchedule({ preset: "weekdays", time: "bad", cron: "" })).toBe("0 9 * * 1-5");
    expect(cronFromSchedule({ preset: "custom", time: "", cron: " 0  9 1 * * " })).toBe(
      "0 9 1 * *",
    );
  });

  it("asks for a time of day only where the preset uses one", () => {
    expect(scheduleTimeOK({ preset: "daily", time: "18:45", cron: "" })).toBe(true);
    expect(scheduleTimeOK({ preset: "daily", time: "", cron: "" })).toBe(false);
    expect(scheduleTimeOK({ preset: "weekdays", time: "25:00", cron: "" })).toBe(false);
    expect(scheduleTimeOK({ preset: "hourly", time: "", cron: "" })).toBe(true);
    expect(scheduleTimeOK({ preset: "custom", time: "", cron: "0 9 * * *" })).toBe(true);
    expect(scheduleTimeOK({ preset: "off", time: "", cron: "" })).toBe(true);
  });

  it("parses times and checks crons", () => {
    expect(parseTime("09:30")).toEqual([9, 30]);
    expect(parseTime("24:00")).toBeNull();
    expect(isValidCron("*/15 9-17 * * MON-FRI")).toBe(true);
    expect(isValidCron("0 9 * *")).toBe(false);
    expect(isValidCron("0 9 * * $(rm)")).toBe(false);
  });

  it("describes a schedule in words", () => {
    expect(scheduleLabel("", "UTC")).toBe("Off");
    expect(scheduleLabel("0 * * * *", "")).toBe("Every hour");
    expect(scheduleLabel("20 * * * *", "")).toBe("Every hour at :20");
    expect(scheduleLabel("0 9 * * 1-5", "America/Buenos_Aires")).toBe(
      "Weekdays at 09:00 (America/Buenos_Aires)",
    );
    expect(scheduleLabel("0 9 1 * *", "")).toBe("0 9 1 * * (UTC)");
  });

  it("describes the deploy trigger", () => {
    const deploy = { available: true, repo: "acme/web", environment: "staging" };
    expect(deployLabel({ on_deploy: true, deploy })).toBe(
      "After each deploy of acme/web to staging",
    );
    expect(deployLabel({ on_deploy: false, deploy })).toBe("");
    expect(deployLabel({ on_deploy: true, deploy: null })).toBe("");
    expect(
      deployLabel({
        on_deploy: true,
        deploy: { ...deploy, available: false, reason: "repo_not_github" },
      }),
    ).toBe("");
  });
});

describe("environments", () => {
  it("turns typed names into valid secret names", () => {
    expect(normalizeSecretName(" admin password ")).toBe("ADMIN_PASSWORD");
    expect(normalizeSecretName("user-email!")).toBe("USER_EMAIL");
    expect(SECRET_NAME.test("ADMIN_PASSWORD")).toBe(true);
    expect(SECRET_NAME.test("1PASS")).toBe(false);
  });

  it("parses hosts without scheme or path", () => {
    expect(parseHosts("https://auth.example.com/login\ncdn.example.com, auth.example.com")).toEqual(
      ["auth.example.com", "cdn.example.com"],
    );
  });

  it("accepts only plain http addresses", () => {
    expect(isHttpURL("https://staging.example.com")).toBe(true);
    expect(isHttpURL("ftp://x")).toBe(false);
    expect(isHttpURL("https://user:pw@example.net")).toBe(false);
    expect(isHttpURL("staging")).toBe(false);
  });

  it("writes the CI hook command with the key from the environment", () => {
    const cmd = ciHookCurl("https://thulu.example.com/", "env 1");
    expect(cmd).toContain('"https://thulu.example.com/evidence/environments/env%201/run"');
    expect(cmd).toContain("X-API-Key: $OCTOPUS_API_KEY");
    expect(cmd).toContain("-X POST");
  });
});

describe("chapters", () => {
  it("reads the cues of a WebVTT file", () => {
    const vtt =
      "WEBVTT\n\n1\n00:00:00.000 --> 00:00:04.000\nOpen the cart\n\n00:04.500 --> 00:09.000\nApply SAVE10\n\nNOTE skipped\n";
    expect(parseChapters(vtt)).toEqual([
      { start: 0, title: "Open the cart" },
      { start: 4.5, title: "Apply SAVE10" },
    ]);
  });
});

describe("environments by project", () => {
  const env = (over: Partial<EvEnvironment>): EvEnvironment => ({
    id: "e",
    name: "staging",
    base_url: "https://staging.example.com",
    allowed_hosts: [],
    secret_names: [],
    schedule: "",
    schedule_tz: "",
    on_deploy: false,
    is_production: false,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
    ...over,
  });
  const shopStaging = env({
    id: "e1",
    kind: "project",
    project_id: "p1",
    project_name: "Shop",
    display_name: "Staging",
  });
  const shopSandbox = env({ id: "e2", kind: "sandbox", project_id: "p1", project_name: "Shop" });
  const blogProd = env({ id: "e3", kind: "project", project_id: "p2", project_name: "Blog" });
  const site = env({ id: "e4", kind: "standalone", name: "Marketing" });

  it("names environments the way people read them", () => {
    expect(envLabel(shopStaging)).toBe("Staging");
    expect(envLabel(blogProd)).toBe("staging");
    expect(envLabel(shopSandbox)).toBe("Sandbox");
    expect(envLongLabel(shopStaging)).toBe("Staging · Shop");
    expect(envLongLabel(site)).toBe("Marketing");
  });

  it("runs cases where the daemon runs them", () => {
    expect(caseRunsOn("p1", shopStaging)).toBe(true);
    expect(caseRunsOn("", shopStaging)).toBe(true);
    expect(caseRunsOn("p2", shopStaging)).toBe(false);
    expect(caseRunsOn("p1", shopSandbox)).toBe(true);
    expect(caseRunsOn("", shopSandbox)).toBe(false);
    expect(caseRunsOn("p2", site)).toBe(true);
    const all = [shopStaging, shopSandbox, blogProd, site];
    expect(envsForProjects(all, ["p2"]).map((e) => e.id)).toEqual(["e3", "e4"]);
    expect(envsForProjects(all, [""]).map((e) => e.id)).toEqual(["e1", "e3", "e4"]);
  });

  it("groups by project with the Sandbox apart and the other sites last", () => {
    const groups = groupEnvs(
      [site, blogProd, shopSandbox, shopStaging],
      [
        { id: "p1", name: "Shop" },
        { id: "p2", name: "Blog" },
        { id: "p3", name: "Docs" },
      ],
    );
    expect(groups.map((g) => g.projectName)).toEqual(["Shop", "Blog", "Docs", ""]);
    expect(groups[0]?.envs.map((e) => e.id)).toEqual(["e1"]);
    expect(groups[0]?.sandbox?.id).toBe("e2");
    expect(groups[2]?.envs).toEqual([]);
    expect(groups[3]?.envs.map((e) => e.id)).toEqual(["e4"]);
    expect(groupEnvs([site, blogProd]).map((g) => g.projectId)).toEqual(["p2", ""]);
  });

  it("groups cases by project, the ones without a project last", () => {
    const c = (id: string, project_id: string) => ({ id, project_id }) as EvCase;
    const groups = groupCases(
      [c("a", ""), c("b", "p2"), c("d", "p1"), c("e", "p2")],
      new Map([
        ["p1", "Shop"],
        ["p2", "Blog"],
      ]),
    );
    expect(groups.map((g) => [g.projectName, g.cases.map((x) => x.id)])).toEqual([
      ["Shop", ["d"]],
      ["Blog", ["b", "e"]],
      ["", ["a"]],
    ]);
  });

  it("says why an action is off", () => {
    expect(permission(env({}), "run")).toEqual({ allowed: true });
    const off = env({ can: { run: { allowed: false, reason: "no_url" } } });
    expect(permission(off, "run").allowed).toBe(false);
    expect(reasonLabel("no_url")).toBe("It has no address yet.");
    expect(reasonLabel("from_the_future")).toBe("You can't do this here.");
    expect(reasonLabel(undefined)).toBe("You can't do this here.");
  });

  it("shows the project picker from two projects", () => {
    const p = (n: number) => Array.from({ length: n }, (_, i) => ({ id: `p${i}` })) as never[];
    expect(showProjectPicker(null)).toBe(false);
    expect(showProjectPicker({ projects: p(1) })).toBe(false);
    expect(showProjectPicker({ projects: p(2) })).toBe(true);
    expect(showProjectPicker({ projects: p(3), show_project_picker: false })).toBe(false);
  });

  it("picks the address bar's project, else the remembered one", () => {
    const projects = [{ id: "p1" }, { id: "p2" }];
    expect(pickProject("p2", "p1", projects)).toBe("p2");
    expect(pickProject("", "p1", projects)).toBe("p1");
    expect(pickProject("", "gone", projects)).toBe("");
    expect(pickProject("gone", "p1", projects)).toBe("");
    expect(pickProject("none", "p1", projects)).toBe("none");
    expect(pickProject("", "none", projects)).toBe("none");
  });

  it("turns a display name into a short name", () => {
    expect(slugify("Staging")).toBe("staging");
    expect(slugify("Producción EU")).toBe("produccion-eu");
    expect(slugify("  --QA v2.1!  ")).toBe("qa-v2.1");
    expect(slugify("x".repeat(60))).toHaveLength(40);
  });
});

describe("sandbox batches", () => {
  it("folds the daemon's phases into four steps", () => {
    expect(sandboxStep("")).toBe("room");
    expect(sandboxStep("room")).toBe("room");
    expect(sandboxStep("source")).toBe("source");
    expect(sandboxStep("tools")).toBe("setup");
    expect(sandboxStep("setup")).toBe("setup");
    expect(sandboxStep("start")).toBe("start");
  });

  it("reads canceled runs of a failed bring-up as not run", () => {
    expect(isNotRun({ sandbox_status: "failed" }, { status: "canceled" })).toBe(true);
    expect(isNotRun({ sandbox_status: "ready" }, { status: "canceled" })).toBe(false);
    expect(isNotRun({ sandbox_status: "failed" }, { status: "fail" })).toBe(false);
  });
});

describe("environment names and bring-up reasons", () => {
  it("hides a slug that says what the display name says", () => {
    expect(sameName("Production", "production")).toBe(true);
    expect(sameName("Staging EU", "staging-eu")).toBe(true);
    expect(sameName("Sitio de ejemplo", "staging")).toBe(false);
  });

  it("reads a reason on its own and tells script failures apart", () => {
    expect(sentence(" the app did not answer in time")).toBe("The app did not answer in time");
    expect(sentence("")).toBe("");
    expect(scriptFailure("ready_timeout")).toBe(true);
    expect(scriptFailure("launch_failed")).toBe(false);
    expect(scriptFailure(undefined)).toBe(false);
  });
});

describe("recording agents", () => {
  const list = {
    agents: [
      { type: "qa", name: "QA", browser: true, ready: true },
      { type: "dev", name: "Dev", browser: false, ready: false, reason: "no key" },
    ],
    auto: "qa",
  };

  it("says which agent the automatic pick takes", () => {
    expect(recordingPick(list, "")).toEqual({ agent: list.agents[0], block: "" });
  });

  it("stops a pick without a model credential", () => {
    expect(recordingPick(list, "dev")).toEqual({ agent: list.agents[1], block: "no_credential" });
  });

  it("stops when no agent holds chats", () => {
    expect(recordingPick({ agents: [], auto: "" }, "")).toEqual({ agent: null, block: "no_agent" });
  });

  it("stops a pick without a chat prompt, and marks it in the picker", () => {
    const bare = { type: "w", name: "Writer", browser: true, ready: true, prompt_ready: false };
    expect(recordingPick({ agents: [bare], auto: "w" }, "")).toEqual({
      agent: bare,
      block: "no_prompt",
    });
    expect(recordingAgentLabel(bare)).toBe("Writer (no chat prompt)");
    expect(recordingAgentLabel(list.agents[1])).toBe("Dev (no model credential)");
    expect(recordingAgentLabel(list.agents[0])).toBe("QA");
  });

  it("links the prompt by the agent's name, which /agents/ looks up", () => {
    const clone = { type: "writer", name: "writer-copy", browser: true, ready: true };
    expect(recordingPromptHref(clone)).toBe("/agents/?agent=writer-copy#prompts");
    expect(recordingPromptHref({ ...clone, name: "" })).toBe("/agents/?agent=writer#prompts");
  });
});

describe("runner causes", () => {
  it("names a known cause and leaves others to the stored text", () => {
    expect(causeLabel("browser_image")).toMatch(/browser image/);
    expect(causeLabel("something_new")).toBe("");
    expect(causeLabel(undefined)).toBe("");
  });
});

describe("the status a case card shows", () => {
  const envOf = (over: Partial<EvEnvironment>): EvEnvironment => ({
    id: "e",
    name: "e",
    base_url: "",
    schedule: "",
    schedule_tz: "",
    ...over,
  });
  const prod = envOf({ id: "prod", kind: "project", project_id: "p1", is_production: true });
  const staging = envOf({ id: "stg", kind: "project", project_id: "p1" });
  const other = envOf({ id: "site", kind: "standalone" });
  const envs = [other, prod, staging];
  const recorded = evCase({
    project_id: "p1",
    status: "fail",
    has_take: true,
    latest: {
      prod: { run_id: "r1", status: "fail", finished_at: null, poster_id: "" },
      stg: { run_id: "r2", status: "pass", finished_at: null, poster_id: "" },
    },
  });

  it("reads the project's first non-production environment", () => {
    expect(cardEnv(recorded, envs)?.id).toBe("stg");
    expect(cardStatus(recorded, envs)).toEqual({ status: "pass", env: staging });
  });

  it("reads the environment the library is filtered by", () => {
    expect(cardStatus(recorded, envs, "prod").status).toBe("fail");
    expect(cardStatus(recorded, envs, "site").status).toBe("not_run");
  });

  it("keeps the case's status when it is not recorded", () => {
    const draft = evCase({ status: "draft", has_take: false });
    expect(cardStatus(draft, envs)).toEqual({ status: "draft", env: null });
  });
});

describe("case order", () => {
  const cs = [
    evCase({ id: "a", priority: "low", status: "pass" }),
    evCase({ id: "b", priority: "critical", status: "blocked" }),
    evCase({ id: "c", priority: "high", status: "fail" }),
  ];
  const ids = (list: EvCase[]) => list.map((c) => c.id).join("");

  it("keeps the daemon's order for newest", () => {
    expect(ids(sortCases(cs, "newest"))).toBe("abc");
  });

  it("sorts by priority and by status, problems first", () => {
    expect(ids(sortCases(cs, "priority"))).toBe("bca");
    expect(ids(sortCases(cs, "status"))).toBe("cba");
  });
});

describe("a site's schedule scope", () => {
  it("says how many recorded cases the schedule runs", () => {
    expect(scheduleScopeLine(7, "", "")).toBe("Runs the 7 recorded cases of every project.");
    expect(scheduleScopeLine(2, "Shop", "checkout")).toBe(
      "Runs the 2 recorded cases of Shop tagged checkout.",
    );
  });

  it("warns when the schedule would run nothing", () => {
    expect(scheduleScopeLine(0, "Shop", "nope")).toBe(
      "No recorded case of Shop is tagged nope: the schedule will run nothing. Check the tag.",
    );
    expect(scheduleScopeLine(0, "", "")).toContain("no recorded cases yet");
  });
});

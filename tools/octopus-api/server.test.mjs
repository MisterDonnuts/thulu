import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import path from "node:path";

import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { SSEClientTransport } from "@modelcontextprotocol/sdk/client/sse.js";

const HERE = path.dirname(fileURLToPath(import.meta.url));

const BOARDS = [
  { id: "default", label: "Default", default: true, position: 10 },
  { id: "acme", label: "Acme", position: 20 },
];
const STAGES = {
  default: [{ id: "backlog", label: "Backlog" }, { id: "in_progress", label: "In progress" }],
  acme: [{ id: "acme_intake", label: "Intake" }, { id: "acme_doing", label: "Doing" }],
};
const TASKS = {
  t_acme: { id: "t_acme", title: "on acme", stage: "acme_doing", board_id: "acme" },
  t_none: { id: "t_none", title: "boardless", stage: "backlog" },
};

// What GET /tasks?stage= answers: the release flow's view of one stage.
const THREE_DAYS_AGO = new Date(Date.now() - 3 * 86400000 - 60000).toISOString();
const STAGE_TASKS = [
  {
    id: "t_81", title: "Checkout copy", stage: "acme_doing", status: "", board_id: "acme",
    project_ids: ["p1"], merge_shas: { p1: "9f2c1e7aa" }, pr_urls: { p1: "https://github.com/a/b/pull/81" },
    updated_at: THREE_DAYS_AGO, body: "long body the summary drops",
  },
  { id: "t_84", title: "Never merged", stage: "acme_doing", board_id: "acme", project_ids: ["p1"], updated_at: "junk" },
];

// Batch b_1 finishes on its second read; b_slow never does.
let batchReads = 0;
const runningBatch = (id) => ({
  id, environment_id: "env_prod", status: "running", total: 3, passed: 1, failed: 0, errored: 0, blocked: 0,
  safe_only: true, trigger: "agent", trigger_ref: "t_acme", task_id: "t_acme",
  runs: [{ case_key: "EV-1", case_title: "Home", status: "pass" }, { case_key: "EV-2", case_title: "Cart", status: "queued" }],
});
const doneBatch = (id) => ({
  ...runningBatch(id), status: "done", passed: 2, failed: 1,
  runs: [
    { case_key: "EV-1", case_title: "Home", status: "pass" },
    { case_key: "EV-2", case_title: "Cart", status: "fail", failures: [{ error: "expected 200, got 500" }] },
  ],
});

// What GET /test-cases/need-kinds answers: the daemon's catalog, hardest to get first.
const NEED_KINDS = [
  { kind: "device", title: "Physical device", help: "A real phone." },
  { kind: "account", title: "Account in the app", help: "A login to the app under test." },
  { kind: "drone", title: "Drone" },
];

const ASK_INSTRUCTION = "Question posted. End your turn now with a one-line summary; the run resumes with the answer.";

// Oldest first, as the daemon lists them; the long one stands for a 20 KB agent report.
const REPORTS = [
  { id: "rep_rev1", stage: "ai_review", author: "thulu-reviewer", kind: "agent_index", title: "",
    body: "# AI review round 1\n" + "x".repeat(5000), created_at: "2026-11-25T22:58:12Z" },
  { id: "rep_ver", stage: "verification", author: "thulu-verifier", kind: "stage_verdict",
    body: "---\nverdict: continue\n---\nok", verdict_meta: { verdict: "continue" }, created_at: "2026-11-25T23:57:09Z" },
  { id: "rep_rev2", stage: "ai_review", author: "thulu-reviewer", kind: "agent_index", title: "",
    body: "# AI review round 2\nR3 blocking", created_at: "2026-11-26T00:02:57Z" },
];

const EVENTS = [
  { id: "ev1", kind: "stage_entered", actor: "octopus", summary: "test_design → in_progress", round: 3,
    payload: null, created_at: "2026-11-25T19:50:42Z" },
  { id: "ev2", kind: "agent_tool_call", actor: "agent:dev", summary: "Bash", payload: { cmd: "ls" },
    created_at: "2026-11-25T19:51:00Z" },
  { id: "ev3", kind: "auditor_prompt", actor: "auditor", summary: "Auditor prompt", round: 3,
    payload: "p".repeat(30000), created_at: "2026-11-25T21:56:08Z" },
  { id: "ev4", kind: "auditor_verdict", actor: "auditor", summary: "in_progress · continue", round: 3,
    payload: { verdict: "continue" }, created_at: "2026-11-25T21:56:09Z" },
];
const PNG = Buffer.from("89504e470d0a1a0a0000000d49484452", "hex");
const ATTACHMENTS = [
  { id: "att_png", task_id: "t_acme", filename: "before.png", mime_type: "image/png", size: PNG.length },
  { id: "att_take", task_id: "t_acme", filename: "take.json", mime_type: "", size: 40 },
  { id: "att_vid", task_id: "t_acme", filename: "run.mp4", mime_type: "video/mp4", size: 900000 },
  { id: "att_pdf", task_id: "t_acme", filename: "report.pdf", mime_type: "application/pdf", size: 900 },
  { id: "att_big", task_id: "t_acme", filename: "huge.png", mime_type: "image/png", size: 9_000_000 },
];
const ATTACHMENT_BYTES = { att_png: PNG, att_take: Buffer.from('{"seconds":[{"t":1,"saw":"Checkout screen"}]}') };

let calls = [];

function fakeDaemon() {
  return http.createServer((req, res) => {
    const url = new URL(req.url, "http://x");
    let body = "";
    req.on("data", (c) => (body += c));
    req.on("end", () => {
      const rec = { method: req.method, path: url.pathname, query: url.searchParams, body: body ? JSON.parse(body) : null };
      calls.push(rec);
      const json = (code, v) => {
        res.writeHead(code, { "content-type": "application/json" });
        res.end(JSON.stringify(v));
      };

      if (req.method === "GET" && url.pathname === "/boards") {
        return json(200, { boards: BOARDS, default: "default" });
      }
      const stageMatch = url.pathname.match(/^\/boards\/([^/]+)\/stages$/);
      if (req.method === "GET" && stageMatch) {
        const s = STAGES[stageMatch[1]];
        return s ? json(200, s) : json(404, { error: "unknown board" });
      }
      if (req.method === "GET" && url.pathname === "/tasks" && url.searchParams.has("stage")) {
        return json(200, STAGE_TASKS);
      }
      if (req.method === "POST" && url.pathname === "/tasks/batch-move") {
        return json(200, { moved: [{ id: "t_81" }], gated: [], skipped: [{ id: "t_84", reason: "no_merge_sha" }] });
      }
      const runMatch = url.pathname.match(/^\/evidence\/environments\/([^/]+)\/run$/);
      if (req.method === "POST" && runMatch) {
        if (runMatch[1] === "env_slow") return json(201, { ...runningBatch("b_slow"), status: "queued" });
        batchReads = 0;
        return json(201, { ...runningBatch("b_1"), status: "queued", skipped_unsafe: ["EV-3"] });
      }
      if (req.method === "GET" && url.pathname === "/evidence/batches") {
        const found = url.searchParams.get("trigger") === "deploy" && url.searchParams.get("ref") === "@9f2c1e7";
        return json(200, { batches: found ? [runningBatch("b_dep")] : [] });
      }
      const batchMatch = url.pathname.match(/^\/evidence\/batches\/([^/]+)$/);
      if (req.method === "GET" && batchMatch) {
        const id = batchMatch[1];
        if (id === "b_slow") return json(200, runningBatch(id));
        batchReads++;
        return json(200, batchReads >= 2 || id === "b_dep" ? doneBatch(id) : runningBatch(id));
      }
      if (req.method === "GET" && url.pathname === "/tasks") {
        const board = url.searchParams.get("board");
        const all = Object.values(TASKS);
        return json(200, board ? all.filter((t) => t.board_id === board) : all);
      }
      if (req.method === "POST" && url.pathname === "/types") {
        return json(201, { name: rec.body.name });
      }
      if (req.method === "POST" && url.pathname === "/tasks") {
        return json(201, { id: "new1", ...rec.body });
      }
      if (req.method === "GET" && url.pathname === "/tasks/t_acme/reports") {
        return json(200, REPORTS);
      }
      if (req.method === "GET" && url.pathname === "/tasks/t_acme/events") {
        return json(200, EVENTS);
      }
      if (req.method === "GET" && url.pathname === "/tasks/t_acme/attachments") {
        return json(200, ATTACHMENTS);
      }
      const attMatch = url.pathname.match(/^\/task-attachments\/([^/]+)$/);
      if (req.method === "GET" && attMatch && ATTACHMENT_BYTES[attMatch[1]]) {
        res.writeHead(200, { "content-type": "application/octet-stream" });
        return res.end(ATTACHMENT_BYTES[attMatch[1]]);
      }
      const watchMatch = url.pathname.match(/^\/tasks\/([^/]+)\/deploy-watches(?:\/([^/]+))?$/);
      if (watchMatch) {
        if (req.method === "PUT") return json(201, { watch: { id: "dw_1", ...rec.body }, created: true });
        if (req.method === "GET") return json(200, { watches: [{ id: "dw_1", source: "http", last_state: "success" }] });
        if (req.method === "DELETE") return json(200, { status: "deleted" });
      }
      const taskMatch = url.pathname.match(/^\/tasks\/([^/]+)$/);
      if (taskMatch) {
        const t = TASKS[taskMatch[1]];
        if (req.method === "GET") return t ? json(200, t) : json(404, { error: "no such task" });
        if (req.method === "PATCH") return json(200, { ...t, ...rec.body });
      }
      if (req.method === "GET" && url.pathname === "/test-cases/need-kinds") {
        return json(200, NEED_KINDS);
      }
      if (/^\/tasks\/[^/]+\/(criteria|relaunch|comments)$/.test(url.pathname)) {
        return json(200, { ok: true, received: rec.body });
      }
      if (/^\/tasks\/[^/]+\/(test-cases(\/|$)|reports$)/.test(url.pathname)) {
        return json(200, { ok: true, received: rec.body });
      }
      const askMatch = url.pathname.match(/^\/tasks\/([^/]+)\/questions$/);
      if (req.method === "POST" && askMatch) {
        if (askMatch[1] === "t_none") return json(409, { error: "task is not running" });
        const question = { id: "q_1", comment_id: "cm_q", status: "open", ...rec.body };
        if (askMatch[1] === "t_quiet") return json(201, { question, comment_id: "cm_q" });
        return json(201, { question, comment_id: "cm_q", instruction: ASK_INSTRUCTION });
      }
      // t_dev plays a developer's run key: the daemon refuses the add and changes nothing.
      const skipAdd = url.pathname.match(/^\/tasks\/([^/]+)\/stage-skips$/);
      if (req.method === "POST" && skipAdd) {
        if (skipAdd[1] === "t_dev") {
          return json(403, { error: "only the refinement stage's agent (refinement) or a person may skip a column" });
        }
        const skip = { stage: rec.body.stage, reason: rec.body.reason, by: "agent:thulu-refiner", at: "2026-11-25T10:00:00Z" };
        return json(200, { task_id: skipAdd[1], skip, stage_skips: [skip] });
      }
      // The stage id picks the outcome: ahead, passed and moved back, or passed while the caller's run is on.
      const skipRemove = url.pathname.match(/^\/tasks\/([^/]+)\/stage-skips\/([^/]+)\/remove$/);
      if (req.method === "POST" && skipRemove) {
        const stage = decodeURIComponent(skipRemove[2]);
        if (stage === "review") return json(404, { error: "the task does not skip review" });
        const removed = { stage, reason: "old", by: "agent:thulu-refiner", at: "2026-11-25T10:00:00Z" };
        const passed = stage !== "testing";
        return json(200, {
          task_id: skipRemove[1], removed, passed,
          moved_back: stage === "test_design", deferred: stage === "qa/manual", stage_skips: [],
        });
      }
      if (req.method === "POST" && /^\/tasks\/[^/]+\/approvals$/.test(url.pathname)) {
        return json(201, { id: "ap_1", status: "pending", source: "agent", ...rec.body });
      }
      if (req.method === "POST" && /\/environments\/[^/]+\/(mappings|mapping-proposals)$/.test(url.pathname)) {
        return json(201, rec.body.mappings);
      }
      if (req.method === "POST" && /\/environments\/[^/]+\/(dashboards|dashboard-proposals)$/.test(url.pathname)) {
        return json(201, { id: "d_1", ...rec.body });
      }
      if (req.method === "PUT" && /^\/types\/[^/]+\/settings$/.test(url.pathname)) {
        return json(200, rec.body);
      }
      if (req.method === "GET" && /^\/projects\/[^/]+\/deploy-contents$/.test(url.pathname)) {
        return json(200, { deployed_sha: url.searchParams.get("sha"), tasks: [], reverts: [], source: "git" });
      }
      json(404, { error: `fake daemon has no route ${req.method} ${url.pathname}` });
    });
  });
}

let daemon;
let mcp;
let mcpPort;

const listen = (server) =>
  new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve(server.address().port)));

const RUN_KEY = "oct_test_run_key";

async function connect(query) {
  const client = new Client({ name: "test", version: "0" }, { capabilities: {} });
  const url = new URL(`http://127.0.0.1:${mcpPort}/sse?${query}`);
  await client.connect(new SSEClientTransport(url, { requestInit: { headers: { "X-API-Key": RUN_KEY } } }));
  return client;
}

async function call(client, name, args = {}) {
  const r = await client.callTool({ name, arguments: args });
  return { text: r.content?.[0]?.text ?? "", isError: !!r.isError };
}

const parse = (text) => JSON.parse(text);

before(async () => {
  daemon = fakeDaemon();
  const daemonPort = await listen(daemon);

  const probe = http.createServer();
  mcpPort = await listen(probe);
  await new Promise((r) => probe.close(r));

  mcp = spawn(process.execPath, [path.join(HERE, "server.js")], {
    env: {
      ...process.env,
      PORT: String(mcpPort),
      OCTOPUS_API: `http://127.0.0.1:${daemonPort}`,
      OCTOPUS_API_KEY: "",
      EVIDENCE_POLL_MS: "20",
      EVIDENCE_WAIT_SILENT_SECONDS: "1",
    },
    stdio: ["ignore", "pipe", "pipe"],
  });
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("MCP server did not start in time")), 10000);
    mcp.stdout.on("data", (d) => {
      if (String(d).includes("listening on")) {
        clearTimeout(timer);
        resolve();
      }
    });
    mcp.stderr.on("data", (d) => process.stderr.write(`[mcp] ${d}`));
    mcp.on("exit", (code) => reject(new Error(`MCP server exited early (${code})`)));
  });
});

after(async () => {
  mcp?.kill("SIGKILL");
  await new Promise((r) => daemon.close(r));
});

test("board_current reports the board of the session's task, with its columns", async () => {
  const c = await connect("task=t_acme&scope=write");
  const { text, isError } = await call(c, "board_current");
  assert.equal(isError, false, text);
  const got = parse(text);
  assert.equal(got.board.id, "acme");
  assert.equal(got.board.label, "Acme");
  assert.deepEqual(got.stages.map((s) => s.id), ["acme_intake", "acme_doing"]);
  assert.equal(got.task.stage, "acme_doing");
  await c.close();
});

test("board_current fails loudly when the session carries no task", async () => {
  const c = await connect("scope=write");
  const { text, isError } = await call(c, "board_current");
  assert.equal(isError, true);
  assert.match(text, /no task context/i);
  await c.close();
});

test("task_create pins the task to the session's board — the model never picks", async () => {
  const c = await connect("task=t_acme&scope=write");
  calls = [];
  const { text, isError } = await call(c, "task_create", { title: "imported" });
  assert.equal(isError, false, text);
  const post = calls.find((r) => r.method === "POST" && r.path === "/tasks");
  assert.equal(post.body.board_id, "acme");
  await c.close();
});

test("task_create refuses a board_id instead of quietly honouring it", async () => {
  const c = await connect("task=t_acme&scope=write");
  calls = [];
  const { text, isError } = await call(c, "task_create", { title: "sneaky", board_id: "default" });
  assert.equal(isError, true);
  assert.match(text, /task_create_on_board/);
  assert.equal(calls.filter((r) => r.method === "POST").length, 0);
  await c.close();
});

test("task_create rejects a stage that is not a column of this board", async () => {
  const c = await connect("task=t_acme&scope=write");
  calls = [];
  const { text, isError } = await call(c, "task_create", { title: "wrong column", stage: "backlog" });
  assert.equal(isError, true);
  assert.match(text, /does not exist on board 'acme'/);
  assert.match(text, /acme_intake → acme_doing/);
  assert.equal(calls.filter((r) => r.method === "POST").length, 0);
  await c.close();
});

test("task_list is scoped to the session's board", async () => {
  const c = await connect("task=t_acme&scope=write");
  const { text } = await call(c, "task_list");
  const got = parse(text);
  assert.equal(got.length, 1);
  assert.equal(got[0].id, "t_acme");
  await c.close();
});

test("a session with no task keeps the unscoped list rather than inventing a board", async () => {
  const c = await connect("scope=write");
  const { text } = await call(c, "task_list");
  assert.equal(parse(text).length, Object.keys(TASKS).length);
  await c.close();
});

test("task_create_on_board writes where it is told, validating board and column", async () => {
  const c = await connect("task=t_acme&tools=task_create_on_board");
  calls = [];
  const { text, isError } = await call(c, "task_create_on_board", {
    board_id: "default", title: "cross", stage: "backlog",
  });
  assert.equal(isError, false, text);
  const post = calls.find((r) => r.method === "POST" && r.path === "/tasks");
  assert.equal(post.body.board_id, "default");

  const unknown = await call(c, "task_create_on_board", { board_id: "nope", title: "x" });
  assert.equal(unknown.isError, true);
  assert.match(unknown.text, /unknown board 'nope'.*default.*acme/s);

  const badStage = await call(c, "task_create_on_board", {
    board_id: "default", title: "x", stage: "acme_doing",
  });
  assert.equal(badStage.isError, true);
  assert.match(badStage.text, /does not exist on board 'default'/);
  await c.close();
});

test("task_move_to_board moves board and column together, defaulting to the first", async () => {
  const c = await connect("task=t_acme&tools=task_move_to_board");
  calls = [];
  const { text, isError } = await call(c, "task_move_to_board", { id: "t_acme", board_id: "default" });
  assert.equal(isError, false, text);
  const patch = calls.find((r) => r.method === "PATCH");
  assert.deepEqual(patch.body, { board_id: "default", stage: "backlog" });

  const bad = await call(c, "task_move_to_board", { id: "t_acme", board_id: "default", stage: "acme_doing" });
  assert.equal(bad.isError, true);
  assert.match(bad.text, /does not exist on board 'default'/);
  await c.close();
});

test("task_update cannot smuggle a board change through the patch spread", async () => {
  const c = await connect("task=t_acme&scope=write");
  calls = [];
  const { text, isError } = await call(c, "task_update", { id: "t_acme", board_id: "default" });
  assert.equal(isError, true);
  assert.match(text, /task_move_to_board/);
  assert.equal(calls.filter((r) => r.method === "PATCH").length, 0);
  await c.close();
});

test("board_tasks_list distinguishes an empty board from one that does not exist", async () => {
  const c = await connect("task=t_acme&tools=board_tasks_list");
  const okCall = await call(c, "board_tasks_list", { board_id: "acme" });
  assert.equal(okCall.isError, false, okCall.text);
  assert.equal(parse(okCall.text).length, 1);

  const ghost = await call(c, "board_tasks_list", { board_id: "ghost" });
  assert.equal(ghost.isError, true);
  assert.match(ghost.text, /unknown board 'ghost'/);
  await c.close();
});

test("a scope keyword in the allowlist keeps the scope and adds the named tools", async () => {
  const c = await connect("task=t_acme&tools=write+boards_list");
  const names = (await c.listTools()).tools.map((t) => t.name);
  for (const want of ["task_create", "task_attach", "task_test_case_add", "task_get", "boards_list"]) {
    assert.ok(names.includes(want), `${want} missing from ${names.join(",")}`);
  }
  assert.ok(!names.includes("task_move_to_board"), "an unnamed cross-board tool must stay out");
  await c.close();
});

test("an allowlist without the cross-board tools hides AND blocks them", async () => {
  const c = await connect("task=t_acme&tools=board_current,task_create,task_list");
  const names = (await c.listTools()).tools.map((t) => t.name).sort();
  assert.deepEqual(names, ["board_current", "task_create", "task_list"]);

  for (const denied of ["task_create_on_board", "task_move_to_board", "boards_list", "board_tasks_list"]) {
    const r = await call(c, denied, { board_id: "default", title: "x", id: "t_acme" });
    assert.equal(r.isError, true, `${denied} should be denied`);
    assert.match(r.text, /not in this session's allowed tool list/);
  }
  const mine = await call(c, "task_create", { title: "still fine" });
  assert.equal(mine.isError, false, mine.text);
  await c.close();
});

test("read scope exposes board_current and nothing that leaves the board", async () => {
  const c = await connect("task=t_acme&scope=read");
  const names = (await c.listTools()).tools.map((t) => t.name);
  assert.ok(names.includes("board_current"), "own-board orientation is part of read");
  for (const cross of ["boards_list", "board_tasks_list", "task_create_on_board", "task_move_to_board"]) {
    assert.ok(!names.includes(cross), `${cross} must not ride on a scope`);
  }
  await c.close();
});

test("scope=write does not reach outside the board — no scope does", async () => {
  const c = await connect("task=t_acme&scope=write");
  const names = (await c.listTools()).tools.map((t) => t.name);
  assert.ok(names.includes("task_create"), "own-board writes are still there");
  for (const cross of ["boards_list", "board_tasks_list", "task_create_on_board", "task_move_to_board"]) {
    assert.ok(!names.includes(cross), `${cross} listed under scope=write`);
  }

  calls = [];
  const denied = await call(c, "task_create_on_board", { board_id: "default", title: "x" });
  assert.equal(denied.isError, true);
  assert.match(denied.text, /named in the agent's MCP tool list/);
  assert.equal(calls.length, 0, "a denied call must not touch the daemon");
  await c.close();
});

test("naming a cross-board tool is the grant — and grants only that one", async () => {
  const c = await connect("task=t_acme&tools=task_create+task_create_on_board");
  const names = (await c.listTools()).tools.map((t) => t.name).sort();
  assert.deepEqual(names, ["task_create", "task_create_on_board"]);

  const allowed = await call(c, "task_create_on_board", { board_id: "default", title: "ok", stage: "backlog" });
  assert.equal(allowed.isError, false, allowed.text);

  const stillDenied = await call(c, "task_move_to_board", { id: "t_acme", board_id: "default" });
  assert.equal(stillDenied.isError, true);
  assert.match(stillDenied.text, /not in this session's allowed tool list/);
  await c.close();
});

test("catalog=1 lists the ungranted tools without granting them", async () => {
  const c = await connect("task=t_acme&scope=write&catalog=1");
  const names = (await c.listTools()).tools.map((t) => t.name);
  for (const cross of ["boards_list", "board_tasks_list", "task_create_on_board", "task_move_to_board"]) {
    assert.ok(names.includes(cross), `${cross} missing from the picker catalog`);
  }

  calls = [];
  const denied = await call(c, "task_create_on_board", { board_id: "default", title: "x" });
  assert.equal(denied.isError, true, "enumeration must not authorize");
  assert.equal(calls.length, 0);
  await c.close();
});

test("agent_type_create without a template asks for the scaffold", async () => {
  const c = await connect("scope=write");
  calls = [];
  const { text, isError } = await call(c, "agent_type_create", { name: "helper" });
  assert.equal(isError, false, text);
  const post = calls.find((r) => r.method === "POST" && r.path === "/types");
  assert.deepEqual(post.body, { name: "helper" });
  await c.close();
});

test("agent_type_create forwards an explicit template", async () => {
  const c = await connect("scope=write");
  calls = [];
  const { text, isError } = await call(c, "agent_type_create", { name: "helper", template: "mine" });
  assert.equal(isError, false, text);
  const post = calls.find((r) => r.method === "POST" && r.path === "/types");
  assert.deepEqual(post.body, { name: "helper", template: "mine" });
  await c.close();
});

async function onlyRequest(name, args) {
  const c = await connect("task=t_acme&scope=write");
  calls = [];
  const r = await call(c, name, args);
  await c.close();
  assert.equal(r.isError, false, r.text);
  assert.equal(calls.length, 1, `${name} made ${calls.length} requests`);
  return calls[0];
}

test("task_test_cases reads the task's cases", async () => {
  const req = await onlyRequest("task_test_cases", { id: "t_acme" });
  assert.equal(req.method, "GET");
  assert.equal(req.path, "/tasks/t_acme/test-cases");
});

test("task_test_case_add posts the cases array as is, with no author", async () => {
  const cases = [
    { title: "login works", steps: "1. open /login", expected: "dashboard", priority: "high" },
    { title: "logout", key: "TC-9" },
  ];
  const req = await onlyRequest("task_test_case_add", { id: "t_acme", cases });
  assert.equal(req.method, "POST");
  assert.equal(req.path, "/tasks/t_acme/test-cases");
  assert.deepEqual(req.body, { cases });
});

test("task_test_case_add refuses an empty cases array before reaching the daemon", async () => {
  const c = await connect("task=t_acme&scope=write");
  calls = [];
  const r = await call(c, "task_test_case_add", { id: "t_acme", cases: [] });
  assert.equal(r.isError, true);
  assert.match(r.text, /non-empty array/);
  assert.equal(calls.length, 0);
  await c.close();
});

test("task_test_case_update patches only the given fields, addressing the case by key", async () => {
  const req = await onlyRequest("task_test_case_update", {
    id: "t_acme", case: "TC-3", status: "skipped", covered: false, position: 2,
  });
  assert.equal(req.method, "PATCH");
  assert.equal(req.path, "/tasks/t_acme/test-cases/TC-3");
  assert.deepEqual(req.body, { status: "skipped", covered: false, position: 2 });
});

test("task_test_case_delete deletes the case", async () => {
  const req = await onlyRequest("task_test_case_delete", { id: "t_acme", case: "tc_abc" });
  assert.equal(req.method, "DELETE");
  assert.equal(req.path, "/tasks/t_acme/test-cases/tc_abc");
});

test("task_test_case_run posts the run with its evidence ids", async () => {
  const run = {
    verdict: "fail",
    observed: "500 on submit",
    note: "see report",
    video_attachment_id: "att_v",
    take_attachment_id: "att_t",
    report_id: "rep_1",
    capture_attachment_ids: ["att_c1", "att_c2"],
  };
  const req = await onlyRequest("task_test_case_run", { id: "t_acme", case: "TC-1", ...run });
  assert.equal(req.method, "POST");
  assert.equal(req.path, "/tasks/t_acme/test-cases/TC-1/runs");
  assert.deepEqual(req.body, run);
});

test("task_add_report files evidence in the Reports tab", async () => {
  const req = await onlyRequest("task_add_report", { id: "t_acme", title: "❌ checkout", body: "| TC-1 | fail |" });
  assert.equal(req.method, "POST");
  assert.equal(req.path, "/tasks/t_acme/reports");
  assert.deepEqual(req.body, { kind: "evidence", title: "❌ checkout", body: "| TC-1 | fail |" });
});

test("task_reports lists the Reports tab newest first, cutting long bodies", async () => {
  const req = await onlyRequest("task_reports", { id: "t_acme" });
  assert.equal(req.method, "GET");
  assert.equal(req.path, "/tasks/t_acme/reports");
  const c = await connect("task=t_acme&scope=read");
  const r = await call(c, "task_reports", { id: "t_acme" });
  await c.close();
  const list = JSON.parse(r.text);
  assert.deepEqual(list.map((x) => x.id), ["rep_rev2", "rep_ver", "rep_rev1"]);
  const long = list.find((x) => x.id === "rep_rev1");
  assert.equal(long.truncated, true);
  assert.equal(long.body.length, 4000);
  assert.equal(long.chars, REPORTS[0].body.length);
  assert.equal(list.find((x) => x.id === "rep_ver").verdict, "continue");
});

test("task_reports filters by stage and kind, and report_id returns one report whole", async () => {
  const c = await connect("task=t_acme&scope=read");
  const byStage = JSON.parse((await call(c, "task_reports", { id: "t_acme", stage: "ai_review" })).text);
  assert.deepEqual(byStage.map((x) => x.id), ["rep_rev2", "rep_rev1"]);
  const byKind = JSON.parse((await call(c, "task_reports", { id: "t_acme", kind: "stage_verdict" })).text);
  assert.deepEqual(byKind.map((x) => x.id), ["rep_ver"]);
  const whole = JSON.parse((await call(c, "task_reports", { id: "t_acme", report_id: "rep_rev1" })).text);
  assert.equal(whole.body, REPORTS[0].body);
  const missing = await call(c, "task_reports", { id: "t_acme", report_id: "nope" });
  assert.equal(missing.isError, true);
  assert.match(missing.text, /No report nope on this task/);
  await c.close();
});

test("task_reports is a read: read scope lists it, and tool.yaml says so in every locale", async () => {
  const c = await connect("task=t_acme&scope=read");
  const tool = (await c.listTools()).tools.find((t) => t.name === "task_reports");
  await c.close();
  assert.ok(tool, "task_reports listed under scope=read");
  assert.deepEqual(tool.inputSchema.required, ["id"]);
  const yaml = (await import("node:fs")).readFileSync(path.join(HERE, "tool.yaml"), "utf8");
  assert.match(yaml, /- name: task_reports\n    description: .+\n    scope: read\n/);
  const { loadCatalogs } = await import("./i18n.js");
  const catalogs = loadCatalogs();
  for (const lang of ["en", "es"]) {
    for (const key of ["tools.task_reports.description", "tools.task_reports.params.report_id", "errors.report_not_found"]) {
      assert.ok(catalogs[lang][key], `${lang}: ${key}`);
    }
  }
});

test("task_events lists the activity without payloads or tool calls, and event_id opens one", async () => {
  const c = await connect("task=t_acme&scope=read");
  const all = JSON.parse((await call(c, "task_events", { id: "t_acme" })).text);
  assert.equal(all.total, 3);
  assert.deepEqual(all.events.map((e) => e.id), ["ev1", "ev3", "ev4"]);
  assert.ok(all.events.every((e) => !("payload" in e)), "listing carries no payloads");
  const last = JSON.parse((await call(c, "task_events", { id: "t_acme", limit: 1 })).text);
  assert.deepEqual(last.events.map((e) => e.id), ["ev4"]);
  const since = JSON.parse((await call(c, "task_events", { id: "t_acme", since: "2026-11-25T20:00:00Z" })).text);
  assert.deepEqual(since.events.map((e) => e.id), ["ev3", "ev4"]);
  const calls_ = JSON.parse((await call(c, "task_events", { id: "t_acme", kind: "agent_tool_call" })).text);
  assert.deepEqual(calls_.events.map((e) => e.id), ["ev2"]);
  const one = JSON.parse((await call(c, "task_events", { id: "t_acme", event_id: "ev3" })).text);
  assert.equal(one.payload.length, 20000);
  assert.equal(one.payload_truncated, true);
  const missing = await call(c, "task_events", { id: "t_acme", event_id: "nope" });
  assert.match(missing.text, /No event nope on this task/);
  await c.close();
});

test("task_attachment_read returns an image as an image and a take as text", async () => {
  const c = await connect("task=t_acme&scope=read");
  const img = await c.callTool({ name: "task_attachment_read", arguments: { id: "t_acme", attachment_id: "att_png" } });
  assert.equal(img.isError ?? false, false);
  const pic = img.content.find((x) => x.type === "image");
  assert.equal(pic.mimeType, "image/png");
  assert.equal(Buffer.from(pic.data, "base64").toString("hex"), PNG.toString("hex"));
  const take = await c.callTool({ name: "task_attachment_read", arguments: { id: "t_acme", attachment_id: "att_take" } });
  assert.match(take.content[1].text, /Checkout screen/);
  await c.close();
});

test("task_attachment_read says what to read instead of a video, and refuses PDFs, big images and other tasks' ids", async () => {
  const c = await connect("task=t_acme&scope=read");
  calls = [];
  const vid = await call(c, "task_attachment_read", { id: "t_acme", attachment_id: "att_vid" });
  assert.equal(vid.isError, true);
  assert.match(vid.text, /take_attachment_id/);
  const pdf = await call(c, "task_attachment_read", { id: "t_acme", attachment_id: "att_pdf" });
  assert.match(pdf.text, /only images, text and JSON/);
  const big = await call(c, "task_attachment_read", { id: "t_acme", attachment_id: "att_big" });
  assert.match(big.text, /images open up to/);
  const foreign = await call(c, "task_attachment_read", { id: "t_acme", attachment_id: "att_other_task" });
  assert.match(foreign.text, /No attachment att_other_task on this task/);
  assert.ok(!calls.some((r) => r.path.startsWith("/task-attachments/")), "nothing refused was downloaded");
  await c.close();
});

test("task_events and task_attachment_read are reads, in tool.yaml and in every locale", async () => {
  const c = await connect("task=t_acme&scope=read");
  const names = (await c.listTools()).tools.map((t) => t.name);
  await c.close();
  const yaml = (await import("node:fs")).readFileSync(path.join(HERE, "tool.yaml"), "utf8");
  const { loadCatalogs } = await import("./i18n.js");
  const catalogs = loadCatalogs();
  for (const name of ["task_events", "task_attachment_read"]) {
    assert.ok(names.includes(name), `${name} listed under scope=read`);
    assert.match(yaml, new RegExp(`- name: ${name}\\n    description: .+\\n    scope: read\\n`), name);
  }
  const keys = ["tools.task_events.description", "tools.task_events.params.event_id",
    "tools.task_attachment_read.description", "errors.event_not_found", "errors.attachment_not_found",
    "errors.attachment_video", "errors.attachment_unreadable", "errors.attachment_too_big", "text.attachment_truncated"];
  for (const lang of ["en", "es"]) for (const key of keys) assert.ok(catalogs[lang][key], `${lang}: ${key}`);
});

test("deploy_contents reads the project's deploy, renaming prev_sha to prev", async () => {
  const req = await onlyRequest("deploy_contents", { project_id: "p 1", sha: "9f2c1e7", stage: "release", prev_sha: "3a7c210" });
  assert.equal(req.method, "GET");
  assert.equal(req.path, "/projects/p%201/deploy-contents");
  assert.equal(req.query.get("sha"), "9f2c1e7");
  assert.equal(req.query.get("stage"), "release");
  assert.equal(req.query.get("prev"), "3a7c210");
});

test("deploy_contents sends only the parameters given, and read scope lists it", async () => {
  const req = await onlyRequest("deploy_contents", { project_id: "p1", sha: "9f2c1e7" });
  assert.deepEqual([...req.query.keys()], ["sha"]);
  const c = await connect("task=t_acme&scope=read");
  const names = (await c.listTools()).tools.map((t) => t.name);
  await c.close();
  assert.ok(names.includes("deploy_contents"));
});

test("task_test_case_add and update carry covers and preconditions as fields", async () => {
  const cases = [{
    title: "SAVE10 takes 10% off", covers: ["AC-1"], preconditions: "Cart: one mug at $100.00.",
    steps: "1. Apply SAVE10.", expected: "Order total $90.00.",
  }];
  const add = await onlyRequest("task_test_case_add", { id: "t_acme", cases });
  assert.deepEqual(add.body, { cases });

  const upd = await onlyRequest("task_test_case_update", {
    id: "t_acme", case: "TC-1", covers: ["AC-1", "AC-3"], preconditions: "Logged in as qa@shop.example.",
  });
  assert.deepEqual(upd.body, { covers: ["AC-1", "AC-3"], preconditions: "Logged in as qa@shop.example." });
});

test("task_test_case_run sends what a blocked case needs", async () => {
  const run = {
    verdict: "blocked",
    observed: "Sign-in with Google needs a real Google account.",
    note: "Tried the OAuth popup; it asks for a phone code.",
    needs: [{
      kind: "third_party_account", label: "A Google test account",
      secrets: [{ name: "GOOGLE_TEST_EMAIL", label: "Email of the account" }],
    }],
    tried: "simulated",
    tried_result: "With a faked login, /signup-email shows the email filled and disabled.",
    writes_data: true,
  };
  const req = await onlyRequest("task_test_case_run", { id: "t_acme", case: "TC-4", ...run });
  assert.equal(req.path, "/tasks/t_acme/test-cases/TC-4/runs");
  assert.deepEqual(req.body, run);
});

test("task_test_case_run refuses an observed over 280 characters and names the fix", async () => {
  const c = await connect("task=t_acme&scope=write");
  calls = [];
  const long = `${"é".repeat(281)}`;
  const r = await call(c, "task_test_case_run", { id: "t_acme", case: "TC-1", verdict: "fail", observed: long });
  assert.equal(r.isError, true);
  assert.match(r.text, /observed has 281 characters and the limit is 280/);
  assert.match(r.text, /detail in note/);
  assert.equal(calls.length, 0);

  // 280 code points pass, even when they take more UTF-16 units.
  const ok = await call(c, "task_test_case_run", { id: "t_acme", case: "TC-1", verdict: "fail", observed: "😀".repeat(280) });
  assert.equal(ok.isError, false, ok.text);
  await c.close();
});

test("task_test_case_run lists the daemon's need kinds instead of an enum of its own", async () => {
  const c = await connect("task=t_acme&scope=write");
  const tool = (await c.listTools()).tools.find((t) => t.name === "task_test_case_run");
  await c.close();
  const kind = tool.inputSchema.properties.needs.items.properties.kind;
  assert.equal(kind.enum, undefined, "the kinds are the daemon's, not an enum here");
  assert.match(kind.description, /device \(Physical device\): A real phone\.; account \(Account in the app\)/);
  assert.match(kind.description, /; drone \(Drone\)\. The daemon refuses any other kind/);
  assert.deepEqual(tool.inputSchema.properties.tried.enum, ["none", "simulated", "partial"]);
  assert.equal(tool.inputSchema.properties.observed.maxLength, 280);
});

test("the keyless catalog probe gets the need kinds description without a list", async () => {
  const client = new Client({ name: "probe", version: "0" }, { capabilities: {} });
  await client.connect(new SSEClientTransport(new URL(`http://127.0.0.1:${mcpPort}/sse?catalog=1&scope=write`)));
  const tool = (await client.listTools()).tools.find((t) => t.name === "task_test_case_run");
  await client.close();
  const desc = tool.inputSchema.properties.needs.items.properties.kind.description;
  // The server process may already hold the list from an earlier session; either text is fine, a
  // made-up list is not.
  assert.ok(/GET \/test-cases\/need-kinds/.test(desc) || /device \(Physical device\)/.test(desc), desc);
});

test("task_test_case_run still forwards a legacy comment_id", async () => {
  const run = { verdict: "pass", comment_id: "cm_1" };
  const req = await onlyRequest("task_test_case_run", { id: "t_acme", case: "TC-2", ...run });
  assert.deepEqual(req.body, run);
});

test("task_test_case_run declares report_id and comment_id", async () => {
  const c = await connect("task=t_acme&scope=write");
  const tool = (await c.listTools()).tools.find((t) => t.name === "task_test_case_run");
  assert.match(tool.inputSchema.properties.report_id.description, /Reports tab/);
  assert.match(tool.inputSchema.properties.comment_id.description, /^Comment id/);
  await c.close();
});

test("the test-case tools enforce their required arguments", async () => {
  const c = await connect("task=t_acme&scope=write");
  calls = [];
  const cases = [
    ["task_test_cases", {}, /id/],
    ["task_test_case_add", { id: "t_acme" }, /cases/],
    ["task_test_case_update", { id: "t_acme", title: "x" }, /case/],
    ["task_test_case_delete", { case: "TC-1" }, /id/],
    ["task_test_case_run", { id: "t_acme", case: "TC-1" }, /verdict/],
  ];
  for (const [name, args, missing] of cases) {
    const r = await call(c, name, args);
    assert.equal(r.isError, true, `${name} accepted ${JSON.stringify(args)}`);
    assert.match(r.text, /missing required argument/);
    assert.match(r.text, missing);
  }
  assert.equal(calls.length, 0, "a call missing arguments must not touch the daemon");
  await c.close();
});

test("read scope lists task_test_cases and none of the test-case writes", async () => {
  const c = await connect("task=t_acme&scope=read");
  const names = (await c.listTools()).tools.map((t) => t.name);
  assert.ok(names.includes("task_test_cases"));
  for (const w of ["task_test_case_add", "task_test_case_update", "task_test_case_delete", "task_test_case_run"]) {
    assert.ok(!names.includes(w), `${w} listed under scope=read`);
  }
  await c.close();
});

const ONE_QUESTION = {
  kind: "question",
  context: "Wiring the export; the request does not say which format.",
  questions: [
    {
      header: "Format",
      question: "CSV or JSON for the export?",
      options: [{ label: "CSV", description: "Opens in a spreadsheet" }, { label: "JSON" }],
    },
  ],
};

test("task_ask posts to the session's task with the defaults spelled out", async () => {
  const req = await onlyRequest("task_ask", ONE_QUESTION);
  assert.equal(req.method, "POST");
  assert.equal(req.path, "/tasks/t_acme/questions");
  assert.deepEqual(req.body, {
    kind: "question",
    blocking: true,
    context: ONE_QUESTION.context,
    questions: [
      {
        header: "Format",
        question: "CSV or JSON for the export?",
        options: [
          { label: "CSV", description: "Opens in a spreadsheet" },
          { label: "JSON", description: "" },
        ],
        multi_select: false,
        allow_other: true,
      },
    ],
    default: "",
    needs_credential: false,
  });
});

test("task_ask passes on a blocker that needs a credential, and refuses a flag that is not a boolean", async () => {
  const req = await onlyRequest("task_ask", { ...ONE_QUESTION, kind: "blocker", needs_credential: true });
  assert.equal(req.body.kind, "blocker");
  assert.equal(req.body.needs_credential, true);
  const c = await connect("task=t_acme&scope=write");
  const { text, isError } = await call(c, "task_ask", { ...ONE_QUESTION, needs_credential: "yes" });
  await c.close();
  assert.equal(isError, true);
  assert.match(text, /needs_credential/);
});

test("task_ask hands back the daemon's instruction first, then the ids", async () => {
  const c = await connect("task=t_acme&scope=write");
  const { text, isError } = await call(c, "task_ask", ONE_QUESTION);
  await c.close();
  assert.equal(isError, false, text);
  assert.ok(text.startsWith(ASK_INSTRUCTION), text);
  assert.match(text, /q_1/);
  assert.match(text, /cm_q/);
});

test("task_ask falls back to its own instruction when the daemon sends none", async () => {
  const c = await connect("task=t_quiet&scope=write");
  const blocking = await call(c, "task_ask", ONE_QUESTION);
  assert.equal(blocking.isError, false, blocking.text);
  assert.match(blocking.text, /^Question posted\. End your turn now/);

  const nonBlocking = await call(c, "task_ask", { ...ONE_QUESTION, blocking: false, default: "CSV" });
  assert.equal(nonBlocking.isError, false, nonBlocking.text);
  assert.match(nonBlocking.text, /^Question posted\. Go on with your default/);
  await c.close();
});

test("task_ask ignores a task id in the arguments", async () => {
  const req = await onlyRequest("task_ask", { ...ONE_QUESTION, id: "t_none", task: "t_none" });
  assert.equal(req.path, "/tasks/t_acme/questions");
  assert.equal(req.body.id, undefined);
});

test("task_ask needs a task on the session", async () => {
  const c = await connect("scope=write");
  calls = [];
  const r = await call(c, "task_ask", ONE_QUESTION);
  await c.close();
  assert.equal(r.isError, true);
  assert.match(r.text, /only inside a task run/);
  assert.equal(calls.length, 0);
});

test("task_ask surfaces the daemon's refusal", async () => {
  const c = await connect("task=t_none&scope=write");
  const r = await call(c, "task_ask", ONE_QUESTION);
  await c.close();
  assert.equal(r.isError, true);
  assert.match(r.text, /HTTP 409.*task is not running/);
});

test("task_ask validates its limits before reaching the daemon", async () => {
  const q = ONE_QUESTION.questions[0];
  const opts = (n) => Array.from({ length: n }, (_, i) => ({ label: `opt${i}` }));
  const cases = [
    [{ ...ONE_QUESTION, kind: "urgent" }, /kind must be 'question' or 'blocker'/],
    [{ ...ONE_QUESTION, questions: [] }, /questions takes 1 to 4 items, got 0/],
    [{ ...ONE_QUESTION, questions: [q, q, q, q, q] }, /questions takes 1 to 4 items, got 5/],
    [{ ...ONE_QUESTION, questions: [{ ...q, header: "Thirteen char" }] }, /questions\[0\]\.header.*longer than 12/],
    [{ ...ONE_QUESTION, questions: [{ ...q, header: "" }] }, /questions\[0\]\.header is required/],
    [{ ...ONE_QUESTION, questions: [{ ...q, question: "  " }] }, /questions\[0\]\.question is required/],
    [{ ...ONE_QUESTION, questions: [q, { ...q, options: opts(7) }] }, /questions\[1\]\.options takes at most 6 options, got 7/],
    [{ ...ONE_QUESTION, questions: [{ ...q, options: [{ label: "A" }, { label: "A " }] }] }, /repeats the label 'A'/],
    [{ ...ONE_QUESTION, questions: [{ ...q, options: [{ description: "no label" }] }] }, /options\[0\]\.label is required/],
    [{ ...ONE_QUESTION, questions: [{ ...q, multi_select: "yes" }] }, /multi_select must be true or false/],
    [{ ...ONE_QUESTION, blocking: false }, /default is required when blocking is false/],
    [{ ...ONE_QUESTION, blocking: false, default: " " }, /default is required when blocking is false/],
    [{ ...ONE_QUESTION, context: undefined }, /missing required argument.*context/],
    [{ kind: "question", context: "x" }, /missing required argument.*questions/],
  ];
  const c = await connect("task=t_acme&scope=write");
  calls = [];
  for (const [args, want] of cases) {
    const r = await call(c, "task_ask", args);
    assert.equal(r.isError, true, `accepted ${JSON.stringify(args)}`);
    assert.match(r.text, want);
  }
  assert.equal(calls.length, 0, "an invalid ask must not touch the daemon");
  await c.close();
});

test("task_ask counts header characters, not bytes", async () => {
  const header = "Diseño ñandú";
  const req = await onlyRequest("task_ask", {
    ...ONE_QUESTION,
    kind: "blocker",
    blocking: false,
    default: "Go with the current palette",
    questions: [{ header, question: "Which palette?", multi_select: true, allow_other: false }],
  });
  assert.deepEqual(req.body.questions[0], {
    header,
    question: "Which palette?",
    options: [],
    multi_select: true,
    allow_other: false,
  });
  assert.equal(req.body.kind, "blocker");
  assert.equal(req.body.blocking, false);
  assert.equal(req.body.default, "Go with the current palette");
});

test("task_ask's schema carries the limits the model has to respect", async () => {
  const c = await connect("task=t_acme&scope=write");
  const tool = (await c.listTools()).tools.find((t) => t.name === "task_ask");
  await c.close();
  const qs = tool.inputSchema.properties.questions;
  assert.equal(qs.minItems, 1);
  assert.equal(qs.maxItems, 4);
  assert.equal(qs.items.properties.header.maxLength, 12);
  assert.equal(qs.items.properties.options.maxItems, 6);
  assert.deepEqual(tool.inputSchema.properties.kind.enum, ["question", "blocker"]);
  assert.match(tool.description, /END YOUR TURN/);
  assert.match(tool.description, /blocking: false/);
});

test("task_request_approval posts label, reason and key to the session's task", async () => {
  const req = await onlyRequest("task_request_approval", {
    label: "Design", reason: "Checkout UI changed", key: "design",
  });
  assert.equal(req.method, "POST");
  assert.equal(req.path, "/tasks/t_acme/approvals");
  assert.deepEqual(req.body, { label: "Design", reason: "Checkout UI changed", key: "design" });
});

test("task_request_approval passes a later stage for the approval to hold", async () => {
  const req = await onlyRequest("task_request_approval", {
    label: "UX review", reason: "2 of 3 personas failed the checkout", key: "ux_review", stage: "review",
  });
  assert.equal(req.path, "/tasks/t_acme/approvals");
  assert.deepEqual(req.body, {
    label: "UX review", reason: "2 of 3 personas failed the checkout", key: "ux_review", stage: "review",
  });
});

test("task_request_approval leaves the key to the daemon when none is given", async () => {
  const c = await connect("task=t_acme&scope=write");
  calls = [];
  const { text, isError } = await call(c, "task_request_approval", { label: "QA", reason: "New flow" });
  await c.close();
  assert.equal(isError, false, text);
  assert.deepEqual(calls[0].body, { label: "QA", reason: "New flow" });
  const got = parse(text);
  assert.equal(got.approval.id, "ap_1");
  assert.match(got.note, /cannot approve it yourself/);
});

test("task_request_approval enforces label and reason, and needs a task", async () => {
  const c = await connect("task=t_acme&scope=write");
  calls = [];
  const noReason = await call(c, "task_request_approval", { label: "QA" });
  assert.equal(noReason.isError, true);
  assert.match(noReason.text, /missing required argument.*reason/);
  await c.close();

  const bare = await connect("scope=write");
  const noTask = await call(bare, "task_request_approval", { label: "QA", reason: "x" });
  assert.equal(noTask.isError, true);
  assert.match(noTask.text, /task_request_approval works only inside a task run/);
  await bare.close();
  assert.equal(calls.length, 0);
});

test("asking and requesting approval are writes: read scope neither lists nor calls them", async () => {
  const c = await connect("task=t_acme&scope=read");
  const names = (await c.listTools()).tools.map((t) => t.name);
  calls = [];
  for (const name of ["task_ask", "task_request_approval"]) {
    assert.ok(!names.includes(name), `${name} listed under scope=read`);
    const r = await call(c, name, { ...ONE_QUESTION, label: "QA", reason: "x" });
    assert.equal(r.isError, true);
    assert.match(r.text, new RegExp(`'${name}' requires scope 'write'`));
  }
  assert.equal(calls.length, 0);
  await c.close();

  const w = await connect("task=t_acme&scope=write");
  const writeNames = (await w.listTools()).tools.map((t) => t.name);
  assert.ok(writeNames.includes("task_ask"));
  assert.ok(writeNames.includes("task_request_approval"));
  await w.close();
});

test("naming task_ask grants it without task_request_approval", async () => {
  const c = await connect("task=t_acme&tools=task_get+task_ask");
  const names = (await c.listTools()).tools.map((t) => t.name).sort();
  assert.deepEqual(names, ["task_ask", "task_get"]);
  const asked = await call(c, "task_ask", ONE_QUESTION);
  assert.equal(asked.isError, false, asked.text);
  const denied = await call(c, "task_request_approval", { label: "QA", reason: "x" });
  assert.equal(denied.isError, true);
  assert.match(denied.text, /not in this session's allowed tool list/);
  await c.close();
});

test("task_skip_add posts stage and reason to the task's stage-skips route", async () => {
  const req = await onlyRequest("task_skip_add", {
    id: "t_acme", stage: "test_design", reason: "  copy change in one template  ",
  });
  assert.equal(req.method, "POST");
  assert.equal(req.path, "/tasks/t_acme/stage-skips");
  assert.deepEqual(req.body, { stage: "test_design", reason: "copy change in one template" });
});

test("task_skip_add returns the skip, the task's skips and what happens next", async () => {
  const c = await connect("task=t_acme&scope=write");
  const { text, isError } = await call(c, "task_skip_add", { id: "t_acme", stage: "test_design", reason: "copy only" });
  await c.close();
  assert.equal(isError, false, text);
  const got = parse(text);
  assert.equal(got.skip.stage, "test_design");
  assert.equal(got.skip.by, "agent:thulu-refiner");
  assert.deepEqual(got.stage_skips.map((s) => s.stage), ["test_design"]);
  assert.match(got.note, /task_skip_remove/);
});

test("task_skip_add surfaces the daemon's refusal to a non-refinement agent and calls nothing else", async () => {
  const c = await connect("task=t_dev&scope=write");
  calls = [];
  const { text, isError } = await call(c, "task_skip_add", { id: "t_dev", stage: "test_design", reason: "skip it" });
  await c.close();
  assert.equal(isError, true);
  assert.match(text, /HTTP 403/);
  assert.match(text, /only the refinement stage's agent/);
  assert.equal(calls.length, 1, "a refusal is not retried or followed by another call");
  assert.equal(calls[0].path, "/tasks/t_dev/stage-skips");
});

test("task_skip_remove encodes the stage in the path and says what happened to the task", async () => {
  const c = await connect("task=t_acme&scope=write");
  const cases = [
    { stage: "testing", path: "/tasks/t_acme/stage-skips/testing/remove", moved: false, deferred: false, note: /runs when the task reaches it/ },
    { stage: "test_design", path: "/tasks/t_acme/stage-skips/test_design/remove", moved: true, deferred: false, note: /went back there/ },
    { stage: "qa/manual", path: "/tasks/t_acme/stage-skips/qa%2Fmanual/remove", moved: false, deferred: true, note: /when your current run ends/ },
    { stage: "readiness", path: "/tasks/t_acme/stage-skips/readiness/remove", moved: false, deferred: false, note: /did not move back/ },
  ];
  for (const k of cases) {
    calls = [];
    const { text, isError } = await call(c, "task_skip_remove", { id: "t_acme", stage: k.stage, reason: "touched the UI" });
    assert.equal(isError, false, text);
    assert.equal(calls.length, 1);
    assert.equal(calls[0].method, "POST");
    assert.equal(calls[0].path, k.path);
    assert.deepEqual(calls[0].body, { reason: "touched the UI" });
    const got = parse(text);
    assert.equal(got.removed.stage, k.stage);
    assert.equal(got.moved_back, k.moved, k.stage);
    assert.equal(got.deferred, k.deferred, k.stage);
    assert.match(got.note, k.note, k.stage);
  }
  await c.close();
});

test("task_skip_remove surfaces a daemon 404 for a column the task does not skip", async () => {
  const c = await connect("task=t_acme&scope=write");
  const { text, isError } = await call(c, "task_skip_remove", { id: "t_acme", stage: "review", reason: "x" });
  await c.close();
  assert.equal(isError, true);
  assert.match(text, /HTTP 404.*does not skip review/);
});

test("the skip tools refuse a missing or blank reason before reaching the daemon", async () => {
  const c = await connect("task=t_acme&scope=write");
  calls = [];
  for (const name of ["task_skip_add", "task_skip_remove"]) {
    const missing = await call(c, name, { id: "t_acme", stage: "test_design" });
    assert.equal(missing.isError, true);
    assert.match(missing.text, /missing required argument.*reason/);
    const blank = await call(c, name, { id: "t_acme", stage: "test_design", reason: "   " });
    assert.equal(blank.isError, true);
    assert.match(blank.text, /reason is required and cannot be blank/);
    const noStage = await call(c, name, { id: "t_acme", reason: "x" });
    assert.equal(noStage.isError, true);
    assert.match(noStage.text, /missing required argument.*stage/);
  }
  await c.close();
  assert.equal(calls.length, 0);
});

test("the skip tools are writes: read scope neither lists nor calls them, naming one grants only it", async () => {
  const r = await connect("task=t_acme&scope=read");
  const readNames = (await r.listTools()).tools.map((t) => t.name);
  calls = [];
  for (const name of ["task_skip_add", "task_skip_remove"]) {
    assert.ok(!readNames.includes(name), `${name} listed under scope=read`);
    const res = await call(r, name, { id: "t_acme", stage: "test_design", reason: "x" });
    assert.equal(res.isError, true);
    assert.match(res.text, new RegExp(`'${name}' requires scope 'write'`));
  }
  assert.equal(calls.length, 0);
  await r.close();

  const named = await connect("task=t_acme&tools=read+task_skip_remove");
  const names = (await named.listTools()).tools.map((t) => t.name);
  assert.ok(names.includes("task_skip_remove"));
  assert.ok(!names.includes("task_skip_add"));
  const denied = await call(named, "task_skip_add", { id: "t_acme", stage: "test_design", reason: "x" });
  assert.equal(denied.isError, true);
  assert.match(denied.text, /not in this session's allowed tool list/);
  await named.close();
});

test("tool.yaml describes the skip tools with the scope the server gives them", async () => {
  const yaml = (await import("node:fs")).readFileSync(path.join(HERE, "tool.yaml"), "utf8");
  const c = await connect("task=t_acme&scope=write");
  const tools = (await c.listTools()).tools;
  await c.close();
  for (const name of ["task_skip_add", "task_skip_remove"]) {
    assert.match(yaml, new RegExp(`- name: ${name}\\n    description: .+\\n    scope: write\\n`), name);
    const tool = tools.find((t) => t.name === name);
    assert.ok(tool, `${name} listed under scope=write`);
    assert.deepEqual(tool.inputSchema.required, ["id", "stage", "reason"]);
  }
});

test("the skip tools' messages exist in every locale", async () => {
  const { loadCatalogs } = await import("./i18n.js");
  const catalogs = loadCatalogs();
  const keys = [
    "tools.task_skip_add.description", "tools.task_skip_add.params.id", "tools.task_skip_add.params.stage",
    "tools.task_skip_add.params.reason", "tools.task_skip_remove.description", "tools.task_skip_remove.params.id",
    "tools.task_skip_remove.params.stage", "tools.task_skip_remove.params.reason", "errors.skip_reason_required",
    "text.skip_added_note", "text.skip_removed_ahead", "text.skip_removed_back", "text.skip_removed_deferred",
    "text.skip_removed_not_moved",
  ];
  for (const lang of ["en", "es"]) {
    for (const key of keys) assert.ok(catalogs[lang][key], `${lang}: ${key}`);
  }
});

test("task_add_comment says how many agent turns a comment launches, and that it is conversation", async () => {
  const c = await connect("task=t_acme&scope=write");
  const tool = (await c.listTools()).tools.find((t) => t.name === "task_add_comment");
  await c.close();
  assert.doesNotMatch(tool.description, /never launches/);
  assert.match(tool.description, /cap of agent turns in a row per thread \(5 unless/);
  assert.match(tool.description, /never in a comment/);
  assert.ok(tool.inputSchema.properties.reply_to, "reply_to is declared");
});

test("task_add_comment files a reply_to under its thread, and still takes a legacy parent_id", async () => {
  const reply = await onlyRequest("task_add_comment", { id: "t_acme", body: "Done.", reply_to: "cm_7" });
  assert.equal(reply.method, "POST");
  assert.equal(reply.path, "/tasks/t_acme/comments");
  assert.deepEqual(reply.body, { attachment_ids: [], body: "Done.", parent_id: "cm_7" });

  const legacy = await onlyRequest("task_add_comment", { id: "t_acme", body: "x", parent_id: "cm_1" });
  assert.equal(legacy.body.parent_id, "cm_1");

  const both = await onlyRequest("task_add_comment", { id: "t_acme", body: "x", reply_to: "cm_2", parent_id: "cm_1" });
  assert.equal(both.body.parent_id, "cm_2");

  const top = await onlyRequest("task_add_comment", { id: "t_acme", body: "x" });
  assert.equal("parent_id" in top.body, false, "a new thread sends no parent_id");
});

test("task_relaunch forwards a reason only when there is one", async () => {
  const withReason = await onlyRequest("task_relaunch", { id: "t_acme", turns: 40, reason: "Staging is back up." });
  assert.equal(withReason.method, "POST");
  assert.equal(withReason.path, "/tasks/t_acme/relaunch");
  assert.deepEqual(withReason.body, { turns: 40, reason: "Staging is back up." });

  const bare = await onlyRequest("task_relaunch", { id: "t_acme", reason: "  " });
  assert.deepEqual(bare.body, {});
});

test("task_criteria_get reads the list and task_criteria_set replaces it", async () => {
  const read = await onlyRequest("task_criteria_get", { id: "t_acme" });
  assert.equal(read.method, "GET");
  assert.equal(read.path, "/tasks/t_acme/criteria");

  const criteria = [
    { key: "AC-1", text: "Given a cart of $100\nWhen SAVE10 is applied\nThen the total is $90" },
    { key: "AC-2", text: "An expired coupon is refused" },
  ];
  const write = await onlyRequest("task_criteria_set", { id: "t_acme", criteria });
  assert.equal(write.method, "PUT");
  assert.equal(write.path, "/tasks/t_acme/criteria");
  assert.deepEqual(write.body, { criteria });
});

test("task_criteria_set refuses a criteria value that is not a list before reaching the daemon", async () => {
  const c = await connect("task=t_acme&scope=write");
  calls = [];
  const r = await call(c, "task_criteria_set", { id: "t_acme", criteria: "AC-1 works" });
  await c.close();
  assert.equal(r.isError, true);
  assert.match(r.text, /criteria must be an array/);
  assert.equal(calls.length, 0);
});

test("read scope lists task_criteria_get and not task_criteria_set", async () => {
  const c = await connect("task=t_acme&scope=read");
  const names = (await c.listTools()).tools.map((t) => t.name);
  await c.close();
  assert.ok(names.includes("task_criteria_get"));
  assert.ok(!names.includes("task_criteria_set"));
});

async function rawEndpoint(key) {
  const ctrl = new AbortController();
  const res = await fetch(`http://127.0.0.1:${mcpPort}/sse?scope=write`, {
    headers: { "X-API-Key": key },
    signal: ctrl.signal,
  });
  const reader = res.body.getReader();
  let buf = "";
  while (!buf.includes("\n\n")) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += new TextDecoder().decode(value);
  }
  const data = buf.split("\n").find((l) => l.startsWith("data:"));
  return {
    endpoint: new URL(data.slice(5).trim(), `http://127.0.0.1:${mcpPort}`).toString(),
    close: () => ctrl.abort(),
  };
}

test("a handshake without a run key is refused", async () => {
  const r = await fetch(`http://127.0.0.1:${mcpPort}/sse?scope=write`);
  assert.equal(r.status, 401);
});

test("a POST on a leaked session id without that session's key is refused", async () => {
  const { endpoint, close } = await rawEndpoint(RUN_KEY);
  try {
    const body = JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/list" });
    const post = (headers) =>
      fetch(endpoint, { method: "POST", headers: { "Content-Type": "application/json", ...headers }, body });
    assert.equal((await post({ "X-API-Key": "oct_attacker" })).status, 401);
    assert.equal((await post({})).status, 401);
    assert.equal((await post({ Authorization: `Bearer ${RUN_KEY}` })).status, 202);
  } finally {
    close();
  }
});

test("the keyless catalog probe lists tools and cannot call one", async () => {
  const client = new Client({ name: "probe", version: "0" }, { capabilities: {} });
  await client.connect(new SSEClientTransport(new URL(`http://127.0.0.1:${mcpPort}/sse?scope=write&catalog=1`)));
  const { tools } = await client.listTools();
  assert.ok(tools.length > 0);
  calls = [];
  const { text, isError } = await call(client, "board_current");
  assert.equal(isError, true);
  assert.match(text, /without a credential/);
  assert.equal(calls.length, 0, "a list-only session must never reach the daemon");
  await client.close();
});

test("environment_mapping_propose goes through the run's task route", async () => {
  const c = await connect("task=t_acme&scope=write");
  calls = [];
  const r = await call(c, "environment_mapping_propose", {
    project_id: "p1",
    environment_id: "e1",
    mappings: [{ service: "api", signal: "latency_p95", source: { kind: "cloudwatch" } }],
  });
  assert.equal(r.isError, false, r.text);
  const post = calls.find((x) => x.method === "POST");
  assert.equal(post.path, "/tasks/t_acme/environments/e1/mapping-proposals");
  assert.equal(post.body.mappings.length, 1);
  assert.equal(post.body.task_id, undefined, "the task is the route's, not a body field");
  await c.close();
});

test("dashboard_propose goes through the named task's route", async () => {
  const c = await connect("scope=write");
  calls = [];
  const r = await call(c, "dashboard_propose", {
    project_id: "p1",
    environment_id: "e1",
    task_id: "t_none",
    name: "Pagos",
    panels: [{ type: "line", series: [] }],
  });
  assert.equal(r.isError, false, r.text);
  const post = calls.find((x) => x.method === "POST");
  assert.equal(post.path, "/tasks/t_none/environments/e1/dashboard-proposals");
  await c.close();
});

test("a proposal with no task at all stays on the project route", async () => {
  const c = await connect("scope=write");
  calls = [];
  const r = await call(c, "environment_mapping_propose", {
    project_id: "p1",
    environment_id: "e1",
    mappings: [{ service: "api", signal: "latency_p95", source: { kind: "cloudwatch" } }],
  });
  assert.equal(r.isError, false, r.text);
  assert.equal(calls.find((x) => x.method === "POST").path, "/projects/p1/environments/e1/mappings");
  await c.close();
});

test("stage_tasks_list reads one stage of the session's board and summarizes each task", async () => {
  const c = await connect("task=t_acme&scope=read");
  calls = [];
  const { text, isError } = await call(c, "stage_tasks_list", { stage: "acme_doing", project_id: "p1" });
  await c.close();
  assert.equal(isError, false, text);
  const list = calls.find((r) => r.method === "GET" && r.path === "/tasks");
  assert.equal(list.query.get("board"), "acme");
  assert.equal(list.query.get("stage"), "acme_doing");
  assert.equal(list.query.get("project"), "p1");
  const got = parse(text);
  assert.equal(got.board_id, "acme");
  const [merged, never] = got.tasks;
  assert.equal(merged.merge_sha, "9f2c1e7aa");
  assert.deepEqual(merged.merge_shas, { p1: "9f2c1e7aa" });
  assert.equal(merged.days_since_update, 3);
  assert.equal(merged.body, undefined, "the summary leaves the body out");
  assert.equal(never.merge_sha, null);
  assert.equal(never.days_since_update, null);
});

test("stage_tasks_list without a project sends no project filter and no merge_sha", async () => {
  const c = await connect("task=t_acme&scope=read");
  calls = [];
  const { text } = await call(c, "stage_tasks_list", { stage: "acme_doing" });
  await c.close();
  assert.equal(calls.find((r) => r.path === "/tasks").query.has("project"), false);
  assert.equal("merge_sha" in parse(text).tasks[0], false);
});

test("tasks_move_batch posts to the session's board and passes the daemon's verdicts through", async () => {
  const c = await connect("task=t_acme&tools=read+tasks_move_batch");
  calls = [];
  const args = {
    task_ids: ["t_81", "t_84"], from_stage: "acme_doing", to_stage: "acme_done", note: "Deployed 9f2c1e7, health pass",
    deployed_sha: "9f2c1e7", project_id: "p1", require_included: true,
  };
  const { text, isError } = await call(c, "tasks_move_batch", args);
  await c.close();
  assert.equal(isError, false, text);
  const post = calls.find((r) => r.method === "POST");
  assert.equal(post.path, "/tasks/batch-move");
  assert.deepEqual(post.body, { board_id: "acme", ...args });
  assert.deepEqual(parse(text).skipped, [{ id: "t_84", reason: "no_merge_sha" }]);
});

test("tasks_move_batch drops the optional fields it was not given", async () => {
  const c = await connect("task=t_acme&tools=tasks_move_batch");
  calls = [];
  await call(c, "tasks_move_batch", { task_ids: ["t_81"], from_stage: "a", to_stage: "b", note: "n" });
  await c.close();
  assert.deepEqual(Object.keys(calls.find((r) => r.method === "POST").body).sort(),
    ["board_id", "from_stage", "note", "task_ids", "to_stage"]);
});

test("deploy_watch_set, list and delete go through the session's own task", async () => {
  const c = await connect("task=t_acme&tools=read+deploy_watch_set+deploy_watch_list+deploy_watch_delete");
  calls = [];
  const set = await call(c, "deploy_watch_set", { source: "http", target: "https://shop.example.com/version", interval: "30m" });
  assert.equal(set.isError, false, set.text);
  const put = calls.find((r) => r.method === "PUT");
  assert.equal(put.path, "/tasks/t_acme/deploy-watches");
  assert.deepEqual(put.body, { source: "http", target: "https://shop.example.com/version", interval: "30m" });
  assert.equal(parse(set.text).watch.id, "dw_1");

  calls = [];
  await call(c, "deploy_watch_set", { source: "github_deployment", target: "production", project_id: "p1" });
  assert.deepEqual(calls.find((r) => r.method === "PUT").body, { source: "github_deployment", target: "production", project_id: "p1" });

  calls = [];
  const list = await call(c, "deploy_watch_list", {});
  assert.equal(calls[0].method, "GET");
  assert.equal(calls[0].path, "/tasks/t_acme/deploy-watches");
  assert.equal(parse(list.text).watches[0].last_state, "success");

  calls = [];
  const del = await call(c, "deploy_watch_delete", { watch_id: "dw 1" });
  assert.equal(del.isError, false, del.text);
  assert.equal(calls[0].method, "DELETE");
  assert.equal(calls[0].path, "/tasks/t_acme/deploy-watches/dw%201");
  await c.close();
});

test("deploy watch tools need a task session", async () => {
  const c = await connect("tools=deploy_watch_list");
  calls = [];
  const r = await call(c, "deploy_watch_list", {});
  await c.close();
  assert.equal(r.isError, true);
  assert.equal(calls.length, 0);
});

test("release tools are granted only by name: no scope lists or calls them", async () => {
  for (const scope of ["read", "write"]) {
    const c = await connect(`task=t_acme&scope=${scope}`);
    const names = (await c.listTools()).tools.map((t) => t.name);
    for (const tool of ["tasks_move_batch", "evidence_batch_run", "deploy_watch_set", "deploy_watch_list", "deploy_watch_delete"]) {
      assert.ok(!names.includes(tool), `${scope} lists ${tool}`);
      calls = [];
      const r = await call(c, tool, { environment_id: "env_prod", task_ids: ["x"], from_stage: "a", to_stage: "b", note: "n" });
      assert.equal(r.isError, true);
      assert.match(r.text, /no scope grants that/);
      assert.equal(calls.length, 0, "a denied call must not touch the daemon");
    }
    assert.ok(names.includes("stage_tasks_list") && names.includes("evidence_batch_get"), `${scope} lists the read tools`);
    await c.close();
  }
});

test("evidence_batch_run queues on the environment, waits, and reports failures and skipped cases", async () => {
  const c = await connect("task=t_acme&tools=read+evidence_batch_run");
  calls = [];
  const { text, isError } = await call(c, "evidence_batch_run", {
    environment_id: "env prod", tag: "smoke", project_id: "p1", safe_only: true, wait_seconds: 5,
  });
  await c.close();
  assert.equal(isError, false, text);
  const post = calls.find((r) => r.method === "POST");
  assert.equal(post.path, "/evidence/environments/env%20prod/run");
  assert.deepEqual(post.body, { tag: "smoke", project_id: "p1", safe_only: true });
  const got = parse(text);
  assert.equal(got.batch_id, "b_1");
  assert.equal(got.status, "done");
  assert.equal(got.finished, true);
  assert.equal(got.timed_out, false);
  assert.equal(got.failed, 1);
  assert.deepEqual(got.failures, [{ case: "EV-2", title: "Cart", status: "fail", error: "expected 200, got 500" }]);
  assert.equal(got.skipped_side_effect, 1);
  assert.deepEqual(got.skipped_side_effect_cases, ["EV-3"]);
});

test("evidence_batch_run without a wait returns as soon as the batch is queued", async () => {
  const c = await connect("task=t_acme&tools=evidence_batch_run");
  calls = [];
  const { text } = await call(c, "evidence_batch_run", { environment_id: "env_prod" });
  await c.close();
  assert.equal(calls.length, 1);
  assert.deepEqual(calls[0].body, {});
  const got = parse(text);
  assert.equal(got.status, "queued");
  assert.equal(got.timed_out, false);
});

test("evidence_batch_run says timed_out when the batch outlives the wait", async () => {
  const c = await connect("task=t_acme&tools=evidence_batch_run");
  const { text } = await call(c, "evidence_batch_run", { environment_id: "env_slow", wait_seconds: 1 });
  await c.close();
  const got = parse(text);
  assert.equal(got.timed_out, true);
  assert.equal(got.finished, false);
});

test("evidence_batch_run without progress cuts a long wait short and hands back the batch id", async () => {
  const c = await connect("task=t_acme&tools=evidence_batch_run");
  const started = Date.now();
  const { text } = await call(c, "evidence_batch_run", { environment_id: "env_slow", wait_seconds: 600 });
  await c.close();
  assert.ok(Date.now() - started < 10000, "the silent wait must stop before the client gives up");
  const got = parse(text);
  assert.equal(got.batch_id, "b_slow");
  assert.equal(got.timed_out, true);
  assert.equal(got.wait_capped_seconds, 1);
  assert.match(got.next, /evidence_batch_get/);
  assert.match(got.next, /b_slow/);
});

test("evidence_batch_run with a progress token keeps the whole wait and reports progress", async () => {
  const c = await connect("task=t_acme&tools=evidence_batch_run");
  const seen = [];
  const r = await c.callTool(
    { name: "evidence_batch_run", arguments: { environment_id: "env_slow", wait_seconds: 2 } },
    undefined,
    { onprogress: (p) => seen.push(p) },
  );
  await c.close();
  const got = parse(r.content[0].text);
  assert.equal(got.timed_out, true);
  assert.equal("wait_capped_seconds" in got, false, "a client that hears progress gets the wait it asked for");
  assert.ok(seen.length > 1, `got ${seen.length} progress notifications`);
  assert.ok(seen[1].progress > seen[0].progress, "progress only goes up");
  assert.match(seen[0].message, /b_slow/);
});

test("evidence_batch_get finds the batch a deploy queued by environment and sha", async () => {
  const c = await connect("task=t_acme&scope=read");
  calls = [];
  const { text, isError } = await call(c, "evidence_batch_get", { environment_id: "env_prod", sha: "9F2C1E7AA0" });
  assert.equal(isError, false, text);
  const find = calls.find((r) => r.path === "/evidence/batches");
  assert.equal(find.query.get("environment_id"), "env_prod");
  assert.equal(find.query.get("ref"), "@9f2c1e7");
  assert.equal(parse(text).batch_id, "b_dep");
  assert.equal("skipped_side_effect" in parse(text), false, "only the queuing call knows what was skipped");

  const none = await call(c, "evidence_batch_get", { environment_id: "env_prod", sha: "aaaaaaa" });
  assert.equal(none.isError, true);
  assert.match(none.text, /evidence_batch_run/);

  const bare = await call(c, "evidence_batch_get", { environment_id: "env_prod" });
  assert.equal(bare.isError, true);
  assert.match(bare.text, /batch_id, or environment_id and sha/);
  await c.close();
});

test("evidence_batch_get by id reads the batch", async () => {
  const c = await connect("task=t_acme&scope=read");
  calls = [];
  const { text } = await call(c, "evidence_batch_get", { batch_id: "b_slow" });
  await c.close();
  assert.equal(calls[0].path, "/evidence/batches/b_slow");
  assert.equal(parse(text).status, "running");
});

test("agent_type_update_settings declares the auditor mode and sends only what it is given", async () => {
  const c = await connect("task=t_acme&scope=write");
  const tool = (await c.listTools()).tools.find((t) => t.name === "agent_type_update_settings");
  await c.close();
  const props = tool.inputSchema.properties;
  assert.deepEqual(props.auditor_mode.enum, ["always", "signals"]);
  assert.match(props.auditor_mode.description, /^always: /);
  assert.equal(props.auditor_max_diff_lines.minimum, 0);
  assert.match(props.auditor_max_diff_files.description, /default, 20/);

  const req = await onlyRequest("agent_type_update_settings", { name: "dev", auditor_mode: "signals" });
  assert.equal(req.method, "PUT");
  assert.equal(req.path, "/types/dev/settings");
  assert.deepEqual(req.body, { auditor_mode: "signals" });
});

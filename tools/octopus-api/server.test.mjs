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

const ASK_INSTRUCTION = "Question posted. End your turn now with a one-line summary; the run resumes with the answer.";

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
      const taskMatch = url.pathname.match(/^\/tasks\/([^/]+)$/);
      if (taskMatch) {
        const t = TASKS[taskMatch[1]];
        if (req.method === "GET") return t ? json(200, t) : json(404, { error: "no such task" });
        if (req.method === "PATCH") return json(200, { ...t, ...rec.body });
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
      if (req.method === "POST" && /^\/tasks\/[^/]+\/approvals$/.test(url.pathname)) {
        return json(201, { id: "ap_1", status: "pending", source: "agent", ...rec.body });
      }
      if (req.method === "POST" && /\/environments\/[^/]+\/(mappings|mapping-proposals)$/.test(url.pathname)) {
        return json(201, rec.body.mappings);
      }
      if (req.method === "POST" && /\/environments\/[^/]+\/(dashboards|dashboard-proposals)$/.test(url.pathname)) {
        return json(201, { id: "d_1", ...rec.body });
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

test("task_add_comment says how many agent turns a comment launches", async () => {
  const c = await connect("task=t_acme&scope=write");
  const tool = (await c.listTools()).tools.find((t) => t.name === "task_add_comment");
  await c.close();
  assert.doesNotMatch(tool.description, /never launches/);
  assert.match(tool.description, /up to 3 agent turns/);
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

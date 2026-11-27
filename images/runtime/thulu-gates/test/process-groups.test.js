// A tool the gates run leaves a child behind (a test server, a jest worker). It must die with the
// tool, and with gates when something stops gates.

import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { run } from "../lib/exec.js";

const skip = process.platform === "win32" ? "process groups are POSIX" : false;
const execURL = new URL("../lib/exec.js", import.meta.url).href;

function alive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

async function gone(pid, ms = 3000) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    if (!alive(pid)) return true;
    await new Promise((r) => setTimeout(r, 50));
  }
  return !alive(pid);
}

// Kills by pid whatever a failing test left, so a red run never leaves a sleep behind.
function reaper(t) {
  const pids = [];
  t.after(() => {
    for (const pid of pids) {
      try {
        process.kill(pid, "SIGKILL");
      } catch {
        // already gone
      }
    }
  });
  return (pid) => pids.push(pid);
}

test("a child the tool left in its group dies when the tool closes", { skip }, async (t) => {
  const track = reaper(t);
  const res = await run("sh", ["-c", "sleep 600 >/dev/null 2>&1 & echo $!"]);
  const pid = Number(res.stdout.trim());
  assert.ok(pid > 0, `no pid in ${JSON.stringify(res.stdout)}`);
  track(pid);
  assert.ok(await gone(pid), `the tool's child ${pid} outlived the tool`);
});

test("a child holding the tool's stdout dies soon after the tool exits", { skip }, async (t) => {
  const track = reaper(t);
  const started = Date.now();
  const res = await run("sh", ["-c", "sleep 600 & echo $!"]);
  const pid = Number(res.stdout.trim().split("\n")[0]);
  track(pid);
  assert.ok(Date.now() - started < 10_000, "run waited on a child holding stdout");
  assert.ok(await gone(pid), `the child ${pid} holding stdout outlived the tool`);
});

for (const signal of ["SIGTERM", "SIGINT"]) {
  test(`gates killed with ${signal} takes the tool's children with it`, { skip }, async (t) => {
    const track = reaper(t);
    const dir = mkdtempSync(join(tmpdir(), "gates-groups-"));
    t.after(() => rmSync(dir, { recursive: true, force: true }));
    const pidFile = join(dir, "pids");
    // A stand-in for gates: it runs one tool that leaves a child and then waits forever. The tool
    // writes its group and its child's pid to a file, since run() keeps the tool's output.
    const tool = 'sleep 600 >/dev/null 2>&1 & echo $$ $! > "$0"; exec sleep 600';
    const script = `
      import { run } from ${JSON.stringify(execURL)};
      run("sh", ["-c", ${JSON.stringify(tool)}, ${JSON.stringify(pidFile)}]);
    `;
    const gates = spawn(process.execPath, ["--input-type=module", "-e", script], { stdio: "ignore" });
    track(gates.pid);
    const end = Date.now() + 10_000;
    let pids = [];
    while (Date.now() < end) {
      if (existsSync(pidFile)) {
        pids = readFileSync(pidFile, "utf8").trim().split(/\s+/).map(Number);
        if (pids.length === 2) break;
      }
      await new Promise((r) => setTimeout(r, 50));
    }
    assert.equal(pids.length, 2, "the tool never started");
    const [group, childPid] = pids;
    track(-group);
    track(childPid);
    const exited = new Promise((r) => gates.on("exit", (code, sig) => r({ code, sig })));
    gates.kill(signal);
    const { code } = await exited;
    assert.equal(code, 128 + (signal === "SIGTERM" ? 15 : 2));
    assert.ok(await gone(childPid), `the tool's child ${childPid} outlived gates`);
    assert.ok(await gone(group), `the tool ${group} outlived gates`);
  });
}

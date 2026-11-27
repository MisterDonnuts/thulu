// Vitest reporter that thulu-gates adds next to the project's own: one JSON line per test that
// starts or ends, appended to $THULU_GATES_PROGRESS, so a run cut short by a hang still says which
// test never finished. Vitest 3 and later call onTestCaseReady/onTestCaseResult; 1 and 2 only
// onTaskUpdate. The name is the suites and the test joined by spaces, which is what -t matches.

import { appendFileSync } from "node:fs";

const out = process.env.THULU_GATES_PROGRESS;

function write(line) {
  if (!out) return;
  try {
    appendFileSync(out, JSON.stringify(line) + "\n");
  } catch {
    // the gate reads what it can
  }
}

function caseName(tc) {
  const names = [tc.name];
  for (let p = tc.parent; p && p.type === "suite"; p = p.parent) names.unshift(p.name);
  return names.join(" ");
}

function taskName(task) {
  const names = [task.name];
  for (let s = task.suite; s && !s.filepath; s = s.suite) names.unshift(s.name);
  return names.join(" ");
}

const STATUS = { passed: "pass", pass: "pass", failed: "fail", fail: "fail", skipped: "skip", skip: "skip", todo: "skip", pending: "skip" };

export default class ThuluProgress {
  onInit(ctx) {
    this.ctx = ctx;
    this.started = new Set();
  }

  onTestRunStart() {
    this.modern = true;
  }

  onTestCaseReady(tc) {
    this.modern = true;
    write({ event: "start", file: tc.module?.moduleId || "", name: caseName(tc) });
  }

  onTestCaseResult(tc) {
    const r = tc.result();
    const err = r.errors?.[0];
    write({ event: "end", file: tc.module?.moduleId || "", name: caseName(tc), status: STATUS[r.state] || r.state, message: err ? String(err.message || err).split("\n")[0] : "" });
  }

  onTaskUpdate(packs) {
    if (this.modern) return;
    for (const [id, result] of packs || []) {
      const task = this.ctx?.state?.idMap?.get(id);
      if (!task || (task.type !== "test" && task.type !== "custom")) continue;
      const file = task.file?.filepath || "";
      const name = taskName(task);
      if (result?.state === "run" && !this.started.has(id)) {
        this.started.add(id);
        write({ event: "start", file, name });
      } else if (result && STATUS[result.state]) {
        const err = result.errors?.[0];
        write({ event: "end", file, name, status: STATUS[result.state], message: err ? String(err.message || err).split("\n")[0] : "" });
      }
    }
  }
}

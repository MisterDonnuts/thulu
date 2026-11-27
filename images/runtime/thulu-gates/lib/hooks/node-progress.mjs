// node --test reporter that thulu-gates adds next to the project's own (through NODE_OPTIONS): one
// JSON line per test that starts or ends, appended to $THULU_GATES_PROGRESS, so a run cut short
// by a hang still says which test never finished. test:dequeue is the moment a test starts to run;
// test:complete comes once per test whatever its outcome.

import { appendFileSync } from "node:fs";
import { relative } from "node:path";

const out = process.env.THULU_GATES_PROGRESS;

function write(line) {
  if (!out) return;
  try {
    appendFileSync(out, JSON.stringify(line) + "\n");
  } catch {
    // the gate reads what it can
  }
}

export default async function* progress(source) {
  // Per file, the names of the tests open at each nesting level, to build the full name.
  const open = new Map();
  for await (const ev of source) {
    const d = ev.data;
    if (!d || typeof d.name !== "string" || !["test:dequeue", "test:complete", "test:pass", "test:fail"].includes(ev.type)) continue;
    const file = d.file || "";
    // With process isolation the runner wraps each file in a test named after its path.
    if (file && relative(process.cwd(), file) === d.name) continue;
    let stack = open.get(file);
    if (!stack) open.set(file, (stack = []));
    if (ev.type === "test:dequeue") {
      stack[d.nesting] = d.name;
      stack.length = d.nesting + 1;
      write({ event: "start", file, name: stack.join(" ") });
    } else if (ev.type === "test:pass" || ev.type === "test:fail") {
      const name = [...stack.slice(0, d.nesting), d.name].join(" ");
      const skipped = d.skip !== undefined || d.todo !== undefined;
      const err = d.details?.error;
      const message = err ? String(err.cause?.message || err.message || err).split("\n")[0] : "";
      write({ event: "end", file, name, status: ev.type === "test:fail" ? "fail" : skipped ? "skip" : "pass", message, line: d.line || 0, suite: d.details?.type === "suite" });
    }
  }
}

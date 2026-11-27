// Jest reporter that thulu-gates adds next to the default one: one JSON line per test that starts
// or ends, appended to $THULU_GATES_PROGRESS, so a run cut short by a hang still says which test
// never finished. Jest 29.6 and later call onTestCaseStart; before that only the file is known
// to start. The name is the describe blocks and the test joined by spaces, which is what
// --testNamePattern matches.

const { appendFileSync } = require("node:fs");

const out = process.env.THULU_GATES_PROGRESS;

function write(line) {
  if (!out) return;
  try {
    appendFileSync(out, JSON.stringify(line) + "\n");
  } catch {
    // the gate reads what it can
  }
}

const name = (info) => [...(info.ancestorTitles || []), info.title].join(" ");
const STATUS = { passed: "pass", failed: "fail", skipped: "skip", pending: "skip", todo: "skip", disabled: "skip", focused: "pass" };

class ThuluProgress {
  onTestStart(test) {
    write({ event: "file-start", file: test.path });
  }

  onTestCaseStart(test, info) {
    write({ event: "start", file: test.path, name: name(info) });
  }

  onTestCaseResult(test, r) {
    write({ event: "end", file: test.path, name: name(r), status: STATUS[r.status] || r.status, message: String((r.failureMessages || [])[0] || "").split("\n")[0] });
  }

  onTestResult(test) {
    write({ event: "file-end", file: test.path });
  }

  getLastError() {
    return undefined;
  }
}

module.exports = ThuluProgress;

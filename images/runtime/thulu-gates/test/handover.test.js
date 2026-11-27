import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { cpSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";

import { HANDED_ENV, HOME_ENV, handOver } from "../lib/handover.js";
import "./helpers.js";

const PKG = dirname(dirname(fileURLToPath(import.meta.url)));
const SELF = pathToFileURL(join(PKG, "bin", "thulu-gates.js")).href;

function tmp(t) {
  const dir = mkdtempSync(join(tmpdir(), "thulu-gates-handover-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  return dir;
}

// A home whose CLI prints its arguments and exits 7, standing in for the runner's copy.
function fakeHome(t) {
  const home = tmp(t);
  mkdirSync(join(home, "bin"));
  writeFileSync(join(home, "bin", "thulu-gates.js"),
    `process.stdout.write("runner copy " + process.argv.slice(2).join(" ") + " " + process.env.${HANDED_ENV}); process.exitCode = 7;`);
  return home;
}

test("a copy that is not the job's hands the run to the job's copy, with its arguments and exit code", (t) => {
  const home = fakeHome(t);
  let out = "";
  try {
    execFileSync(process.execPath, [join(PKG, "bin", "thulu-gates.js"), "run", "--reuse"], {
      env: { ...process.env, [HOME_ENV]: home }, encoding: "utf8",
    });
  } catch (err) {
    assert.equal(err.status, 7);
    out = err.stdout;
  }
  assert.equal(out, "runner copy run --reuse 1");
});

test("the job's own copy, a run already handed over, or no home runs the copy at hand", (t) => {
  const never = () => assert.fail("handed over");
  // The home is this package, reached through a symlink: still this copy.
  const link = join(tmp(t), "gates");
  execFileSync("ln", ["-s", PKG, link]);
  assert.equal(handOver([], SELF, { env: { [HOME_ENV]: link }, spawn: never }), null);
  assert.equal(handOver([], SELF, { env: { [HOME_ENV]: fakeHome(t), [HANDED_ENV]: "1" }, spawn: never }), null);
  assert.equal(handOver([], SELF, { env: {}, spawn: never }), null);
});

test("a home without the CLI in it is ignored, and a copy of this package is still another copy", (t) => {
  const never = () => assert.fail("handed over");
  assert.equal(handOver([], SELF, { env: { [HOME_ENV]: tmp(t) }, spawn: never }), null);
  const other = join(tmp(t), "gates");
  cpSync(PKG, other, { recursive: true, filter: (p) => !p.includes(`${PKG}/test`) });
  let called;
  const code = handOver(["--version"], SELF, {
    env: { [HOME_ENV]: other }, node: "node",
    spawn: (cmd, args, opts) => { called = { cmd, args, handed: opts.env[HANDED_ENV] }; return { status: 0 }; },
  });
  assert.equal(code, 0);
  assert.deepEqual(called, { cmd: "node", args: [join(other, "bin", "thulu-gates.js"), "--version"], handed: "1" });
  assert.equal(handOver([], SELF, { env: { [HOME_ENV]: other }, spawn: () => ({ signal: "SIGTERM" }) }), 143);
});

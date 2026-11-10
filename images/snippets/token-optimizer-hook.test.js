"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("fs");
const os = require("os");
const path = require("path");
const { spawnSync } = require("child_process");

const HOOK = path.join(__dirname, "token-optimizer-hook.js");
const LIMIT = 24576;

const root = fs.mkdtempSync(path.join(os.tmpdir(), "tokopt-hook-"));
const F = (name) => path.join(root, name);
const state = path.join(root, "state");

function linesOf(n, width = 60) {
  let s = "";
  for (let i = 1; i <= n; i++) s += `line ${String(i).padStart(5, "0")} `.padEnd(width, "x") + "\n";
  return s;
}

function write(name, content) {
  fs.mkdirSync(path.dirname(F(name)), { recursive: true });
  fs.writeFileSync(F(name), content);
}

write("small.txt", linesOf(100));
write("boundary.txt", "a".repeat(LIMIT - 1) + "\n");
write("over.txt", "a".repeat(LIMIT) + "\n");
write("large.txt", linesOf(1200));
write("huge.txt", linesOf(5000));
write("empty.txt", "");
write("medium.txt", linesOf(250));
write("thirty.txt", linesOf(500));
write("minified.js", ("var a=1;".repeat(25000) + "\n").repeat(3));
write("short-lines.txt", "abcd\n".repeat(600));
write("picture.png", Buffer.alloc(200000, 1));
write("doc.pdf", Buffer.alloc(200000, 1));
write("nb.ipynb", JSON.stringify({ cells: [{ source: "x".repeat(100000) }] }));
write("part-a.txt", linesOf(160));
write("part-b.txt", linesOf(160));
write("part-c.txt", linesOf(160));
write("sub/deep.txt", linesOf(1000));
write("logs/one.log", linesOf(300));
write("logs/two.log", linesOf(300));
fs.mkdirSync(F("adir"), { recursive: true });

const hasGit = spawnSync("git", ["--version"]).status === 0;
if (hasGit) {
  const g = (...a) => spawnSync("git", ["-C", root, ...a], { encoding: "utf8" });
  g("init", "-q");
  g("-c", "user.email=t@t", "-c", "user.name=t", "add", "large.txt", "small.txt");
  g("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "fixtures");
}

function runHook(mode, input, env = {}) {
  const r = spawnSync("node", [HOOK, mode], {
    input: typeof input === "string" ? input : JSON.stringify(input),
    encoding: "utf8",
    env: {
      PATH: process.env.PATH,
      HOME: root,
      OCTOPUS_TOKEN_OPTIMIZER: "enforce",
      OCTOPUS_TOKEN_OPTIMIZER_STATE_DIR: state,
      ...env,
    },
  });
  return r;
}

function decision(mode, input, env) {
  const r = runHook(mode, input, env);
  assert.equal(r.status, 0, `hook exit ${r.status}: ${r.stderr}`);
  if (!r.stdout.trim()) return { decision: "allow" };
  const out = JSON.parse(r.stdout);
  return { decision: out.hookSpecificOutput.permissionDecision, out };
}

function setFailures(n) {
  fs.mkdirSync(state, { recursive: true });
  if (n === null) fs.rmSync(path.join(state, "failures"), { force: true });
  else fs.writeFileSync(path.join(state, "failures"), `${n}\n`);
}

const readCases = [
  { name: "small-file", input: { file_path: F("small.txt") }, want: "allow" },
  { name: "boundary-exact-limit", input: { file_path: F("boundary.txt") }, want: "allow", why: "at the limit (<=) does not deny" },
  { name: "just-over-limit", input: { file_path: F("over.txt") }, want: "deny" },
  { name: "large-file", input: { file_path: F("large.txt") }, want: "deny" },
  { name: "very-large-file", input: { file_path: F("huge.txt") }, want: "deny" },
  { name: "empty-file", input: { file_path: F("empty.txt") }, want: "allow" },
  { name: "targeted-read-offset", input: { file_path: F("large.txt"), offset: 100 }, want: "allow" },
  { name: "targeted-read-limit", input: { file_path: F("large.txt"), limit: 50 }, want: "allow" },
  { name: "targeted-read-both", input: { file_path: F("large.txt"), offset: 100, limit: 50 }, want: "allow" },
  { name: "nonexistent-file", input: { file_path: F("nope.txt") }, want: "allow", why: "Read reports the error" },
  { name: "empty-filepath", input: { file_path: "" }, want: "allow" },
  { name: "missing-filepath-field", input: {}, want: "allow" },
  { name: "offset-zero", input: { file_path: F("large.txt"), offset: 0 }, want: "allow", why: "an explicit range, even from 0" },
  { name: "limit-zero", input: { file_path: F("large.txt"), limit: 0 }, want: "allow" },
  { name: "env-override-lower", input: { file_path: F("medium.txt") }, env: { OCTOPUS_TOKEN_OPTIMIZER_THRESHOLD_BYTES: "10000" }, want: "deny" },
  { name: "env-override-higher", input: { file_path: F("thirty.txt") }, env: { OCTOPUS_TOKEN_OPTIMIZER_THRESHOLD_BYTES: "50000" }, want: "allow" },
  { name: "env-non-numeric-fallback", input: { file_path: F("over.txt") }, env: { OCTOPUS_TOKEN_OPTIMIZER_THRESHOLD_BYTES: "abc" }, want: "deny" },
  { name: "minified-3-lines", input: { file_path: F("minified.js") }, want: "deny", why: "3 lines but 600 KB: bytes, not lines" },
  { name: "many-short-lines", input: { file_path: F("short-lines.txt") }, want: "allow", why: "600 lines but 3 KB" },
  { name: "image", input: { file_path: F("picture.png") }, want: "allow", why: "Read renders images; nothing to delegate" },
  { name: "pdf", input: { file_path: F("doc.pdf") }, want: "allow", why: "Read pages PDFs itself" },
  { name: "notebook", input: { file_path: F("nb.ipynb") }, want: "allow", why: "Read renders cells; offset/limit do not apply" },
  { name: "directory", input: { file_path: F("adir") }, want: "allow" },
  { name: "relative-path-cwd", input: { file_path: "large.txt" }, cwd: root, want: "deny" },
  { name: "level-suggest", input: { file_path: F("large.txt") }, env: { OCTOPUS_TOKEN_OPTIMIZER: "suggest" }, want: "allow", why: "only enforce denies" },
  { name: "level-unset", input: { file_path: F("large.txt") }, env: { OCTOPUS_TOKEN_OPTIMIZER: "" }, want: "allow" },
];

const bashCases = [
  { name: "cat-large-file", cmd: `cat ${F("large.txt")}`, want: "deny" },
  { name: "cat-small-file", cmd: `cat ${F("small.txt")}`, want: "allow" },
  { name: "cat-with-flag", cmd: `cat -n ${F("large.txt")}`, want: "deny" },
  { name: "head-large-file", cmd: `head ${F("large.txt")}`, want: "allow", why: "shunt denies; head prints 10 lines (610 bytes)" },
  { name: "head-with-count", cmd: `head -100 ${F("large.txt")}`, want: "allow", why: "shunt denies; 100 lines are 6 KB, like Read with limit=100" },
  { name: "tail-large-file", cmd: `tail ${F("large.txt")}`, want: "allow", why: "shunt denies; 10 lines" },
  { name: "less-large-file", cmd: `less ${F("large.txt")}`, want: "deny" },
  { name: "cat-pipe", cmd: `cat ${F("large.txt")} | grep export`, want: "allow" },
  { name: "cat-redirect", cmd: `cat ${F("large.txt")} > ${F("out.txt")}`, want: "allow" },
  { name: "non-read-command", cmd: "git status", want: "allow" },
  { name: "grep-command", cmd: `grep -n 'export' ${F("large.txt")}`, want: "allow" },
  { name: "cat-quoted-path", cmd: `cat "${F("large.txt")}"`, want: "deny" },
  { name: "cat-nonexistent", cmd: `cat ${F("nope.txt")}`, want: "allow" },
  { name: "empty-command", cmd: "", want: "allow" },
  { name: "missing-command-field", input: {}, want: "allow" },
  { name: "more-large-file", cmd: `more ${F("large.txt")}`, want: "deny" },
  { name: "head-n-space-count", cmd: `head -n 5 ${F("large.txt")}`, want: "allow", why: "shunt took 5 for the file; here it is the count" },
  { name: "head-big-count", cmd: `head -n 1000 ${F("large.txt")}`, want: "deny" },
  { name: "tail-from-line-1", cmd: `tail -n +1 ${F("large.txt")}`, want: "deny" },
  { name: "head-minified", cmd: `head -n 3 ${F("minified.js")}`, want: "deny", why: "3 lines, 600 KB" },
  { name: "head-bytes", cmd: `head -c 100000 ${F("large.txt")}`, want: "deny" },
  { name: "tail-follow", cmd: `tail -f ${F("large.txt")}`, want: "allow" },
  { name: "sed-n-whole-range", cmd: `sed -n '1,$p' ${F("large.txt")}`, want: "deny" },
  { name: "sed-n-huge-range", cmd: `sed -n '1,5000p' ${F("large.txt")}`, want: "deny" },
  { name: "sed-n-small-range", cmd: `sed -n '100,140p' ${F("large.txt")}`, want: "allow" },
  { name: "sed-n-p", cmd: `sed -n p ${F("large.txt")}`, want: "deny" },
  { name: "sed-substitute-prints-all", cmd: `sed 's/line/LINE/g' ${F("large.txt")}`, want: "deny" },
  { name: "sed-in-place", cmd: `sed -i 's/line/LINE/' ${F("out-sed.txt")} ${F("large.txt")}`, want: "allow" },
  { name: "sed-pattern", cmd: `sed -n '/export/p' ${F("large.txt")}`, want: "allow", why: "a pattern: unsure, allow" },
  { name: "awk-print-all", cmd: `awk '{print}' ${F("large.txt")}`, want: "deny" },
  { name: "awk-1", cmd: `awk 1 ${F("large.txt")}`, want: "deny" },
  { name: "awk-print-0", cmd: `awk '{ print $0 }' ${F("large.txt")}`, want: "deny" },
  { name: "awk-nr-small", cmd: `awk 'NR>=10 && NR<=40' ${F("large.txt")}`, want: "allow" },
  { name: "awk-field", cmd: `awk '{print $1}' ${F("large.txt")}`, want: "allow" },
  { name: "cat-three-files", cmd: `cat ${F("part-a.txt")} ${F("part-b.txt")} ${F("part-c.txt")}`, want: "deny", why: "each is under the limit, together they are not" },
  { name: "cat-small-then-large", cmd: `cat ${F("small.txt")} ${F("large.txt")}`, want: "deny", why: "shunt only looked at the first file" },
  { name: "cat-glob", cmd: `cat ${root}/logs/*.log`, want: "deny" },
  { name: "cd-then-cat", cmd: `cd ${F("sub")} && cat deep.txt`, want: "deny" },
  { name: "relative-cwd", cmd: "cat large.txt", cwd: root, want: "deny" },
  { name: "input-redirect", cmd: `cat < ${F("large.txt")}`, want: "deny" },
  { name: "pipe-head-small", cmd: `cat ${F("large.txt")} | head -n 20`, want: "allow" },
  { name: "pipe-sort", cmd: `cat ${F("large.txt")} | sort`, want: "deny" },
  { name: "pipe-wc", cmd: `cat ${F("large.txt")} | wc -l`, want: "allow" },
  { name: "stderr-redirect-still-prints", cmd: `cat ${F("large.txt")} 2>/dev/null`, want: "deny" },
  { name: "devnull", cmd: `cat ${F("large.txt")} >/dev/null`, want: "allow" },
  { name: "nl-to-less", cmd: `nl ${F("large.txt")} | less`, want: "deny" },
  { name: "python-c-read", cmd: `python3 -c "print(open('${F("large.txt")}').read())"`, want: "deny" },
  { name: "python-c-no-print", cmd: `python3 -c "n=len(open('${F("large.txt")}').read())"`, want: "allow" },
  { name: "node-e-read", cmd: `node -e "console.log(require('fs').readFileSync('${F("large.txt")}','utf8'))"`, want: "deny" },
  { name: "bash-c", cmd: `bash -c "cat ${F("large.txt")}"`, want: "deny" },
  { name: "git-show-blob", cmd: `git -C ${root} show HEAD:large.txt`, want: "deny", git: true },
  { name: "git-show-small-blob", cmd: `git -C ${root} show HEAD:small.txt`, want: "allow", git: true },
  { name: "git-show-blob-cwd", cmd: "git show HEAD:large.txt", cwd: root, want: "deny", git: true },
  { name: "git-show-commit", cmd: `git -C ${root} show HEAD`, want: "allow", git: true },
  { name: "variable", cmd: "cat $FILE", env: { FILE: F("large.txt") }, want: "allow" },
  { name: "loop", cmd: `for f in ${F("large.txt")}; do echo $f; done`, want: "allow" },
  { name: "heredoc-body-is-not-a-command", cmd: `cat > ${F("script.sh")} <<'EOF'\ncat ${F("large.txt")}\nEOF`, want: "allow" },
  { name: "heredoc-then-command", cmd: `cat > ${F("script.sh")} <<'EOF'\nhello\nEOF\ncat ${F("large.txt")}`, want: "deny" },
  { name: "comment", cmd: `ls # cat ${F("large.txt")}`, want: "allow" },
  { name: "malformed-quotes", cmd: `cat "${F("large.txt")}`, want: "deny", why: "an unclosed quote still names the file" },
];

for (const c of readCases) {
  test(`read: ${c.name}`, () => {
    setFailures(null);
    const input = { hook_event_name: "PreToolUse", tool_name: "Read", tool_input: c.input };
    if (c.cwd) input.cwd = c.cwd;
    assert.equal(decision("read", input, c.env).decision, c.want, c.why);
  });
}

for (const c of bashCases) {
  test(`bash: ${c.name}`, { skip: c.git && !hasGit ? "git not installed" : false }, () => {
    setFailures(null);
    const input = { hook_event_name: "PreToolUse", tool_name: "Bash", tool_input: c.input || { command: c.cmd } };
    input.cwd = c.cwd || os.tmpdir();
    assert.equal(decision("bash", input, c.env).decision, c.want, c.why);
  });
}

test("deny output follows the current PreToolUse schema", () => {
  setFailures(null);
  const r = runHook("read", { tool_name: "Read", tool_input: { file_path: F("large.txt") } });
  assert.equal(r.status, 0);
  const out = JSON.parse(r.stdout);
  assert.deepEqual(Object.keys(out), ["hookSpecificOutput"], "no top-level decision/reason (deprecated for PreToolUse)");
  assert.deepEqual(Object.keys(out.hookSpecificOutput).sort(), ["hookEventName", "permissionDecision", "permissionDecisionReason"]);
  assert.equal(out.hookSpecificOutput.hookEventName, "PreToolUse");
  assert.equal(out.hookSpecificOutput.permissionDecision, "deny");
  const reason = out.hookSpecificOutput.permissionDecisionReason;
  assert.match(reason, /71 KB/, "names the size");
  assert.match(reason, /24 KB/, "names the limit");
  assert.ok(reason.includes(`octopus-delegate read --question "<what you need to know>" --paths ${F("large.txt")}`), reason);
  assert.match(reason, /offset and limit/, "offers reading a range");
});

test("bash deny names every file and suggests them all in one call", () => {
  setFailures(null);
  const cmd = `cat part-a.txt part-b.txt part-c.txt`;
  const { out } = decision("bash", { tool_name: "Bash", tool_input: { command: cmd }, cwd: root });
  const reason = out.hookSpecificOutput.permissionDecisionReason;
  assert.ok(reason.includes("--paths part-a.txt part-b.txt part-c.txt"), reason);
});

test("allow prints nothing and exits 0", () => {
  const r = runHook("read", { tool_input: { file_path: F("small.txt") } });
  assert.equal(r.status, 0);
  assert.equal(r.stdout, "");
});

test("bad input allows instead of failing the call", () => {
  for (const input of ["not json", "", "[]", "null"]) {
    const r = runHook("bash", input);
    assert.equal(r.status, 0, input);
    assert.equal(r.stdout, "", input);
  }
  const r = runHook("unknown-mode", { tool_input: { file_path: F("large.txt") } });
  assert.equal(r.status, 0);
  assert.equal(r.stdout, "");
});

test("degrades to allow after 3 failed delegations", () => {
  const input = { tool_input: { file_path: F("large.txt") } };
  setFailures(2);
  assert.equal(decision("read", input).decision, "deny", "2 failures still deny");
  setFailures(3);
  assert.equal(decision("read", input).decision, "allow", "3 failures lift the limit");
  assert.equal(decision("bash", { tool_input: { command: `cat ${F("large.txt")}` } }).decision, "allow");
  setFailures(null);
});

test.after(() => fs.rmSync(root, { recursive: true, force: true }));

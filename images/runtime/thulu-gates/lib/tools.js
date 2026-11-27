// Tool registry: how to find each external tool, ask its version and install it. Versions in the
// install hints match the pins in the Thulu Makefile where Thulu itself uses the tool; the rest
// carry a <version> placeholder because a gate must never install `latest`.

import { localBin } from "./exec.js";
import { firstLine } from "./util.js";

const V = "<version>";

// stacks: where doctor lists it ("*" = every repo). local: a project devDependency found in
// node_modules/.bin, not on PATH.
export const TOOLS = {
  git: { stacks: ["*"], version: ["--version"], install: "apt-get install git" },
  semgrep: { stacks: ["*"], version: ["--version"], install: "mise use -g pipx:semgrep@1.163.0", checks: ["sast"] },
  gitleaks: { stacks: ["*"], version: ["version"], install: "mise use -g github:gitleaks/gitleaks@8.30.1", checks: ["secrets"] },
  "osv-scanner": { stacks: ["*"], version: ["--version"], install: `mise use -g github:google/osv-scanner@${V}`, checks: ["deps"] },
  jscpd: { stacks: ["*"], version: ["--version"], install: `mise use -g npm:jscpd@${V}`, checks: ["duplication"] },
  lizard: { stacks: ["*"], version: ["--version"], install: `mise use -g pipx:lizard@${V}`, checks: ["lint"] },
  hyperfine: { stacks: ["*"], version: ["--version"], install: `mise use -g github:sharkdp/hyperfine@${V}`, checks: ["perf"] },

  go: { stacks: ["go"], version: ["version"], install: `mise use go@${V}`, checks: ["tests", "typecheck"] },
  gofmt: { stacks: ["go"], version: null, install: "ships with go", checks: ["lint"] },
  "golangci-lint": { stacks: ["go"], version: ["version"], install: "mise use -g github:golangci/golangci-lint@2.13.2", checks: ["lint"] },
  govulncheck: { stacks: ["go"], version: ["-version"], install: "mise use -g go:golang.org/x/vuln/cmd/govulncheck@v1.8.0", checks: ["deps"] },
  gremlins: { stacks: ["go"], version: ["--version"], install: `mise use -g github:go-gremlins/gremlins@${V}`, checks: ["mutation"] },
  "go-mutesting": { stacks: ["go"], version: null, install: `go install github.com/avito-tech/go-mutesting/cmd/go-mutesting@${V}`, checks: ["mutation"] },
  benchstat: { stacks: ["go"], version: null, install: `mise use -g go:golang.org/x/perf/cmd/benchstat@${V}`, checks: ["perf"] },

  node: { stacks: ["ts", "js"], version: ["--version"], install: `mise use node@${V}` },
  npm: { stacks: ["ts", "js"], version: ["--version"], install: "ships with node", checks: ["deps"] },
  eslint: { stacks: ["ts", "js"], local: true, version: ["--version"], install: "npm install --save-dev eslint", checks: ["lint"] },
  prettier: { stacks: ["ts", "js"], local: true, version: ["--version"], install: "npm install --save-dev prettier", checks: ["lint"] },
  tsc: { stacks: ["ts"], local: true, version: ["--version"], install: "npm install --save-dev typescript", checks: ["typecheck"] },
  vitest: { stacks: ["ts", "js"], local: true, version: ["--version"], install: "npm install --save-dev vitest @vitest/coverage-v8", checks: ["tests", "coverage_new"] },
  jest: { stacks: ["ts", "js"], local: true, version: ["--version"], install: "npm install --save-dev jest", checks: ["tests", "coverage_new"] },
  stryker: { stacks: ["ts", "js"], local: true, version: ["--version"], install: "npm install --save-dev @stryker-mutator/core", checks: ["mutation"] },

  python3: { stacks: ["python"], version: ["--version"], install: `mise use python@${V}` },
  pytest: { stacks: ["python"], version: ["--version"], install: "add pytest and pytest-cov to the project's dev dependencies", checks: ["tests", "coverage_new"] },
  ruff: { stacks: ["python"], version: ["--version"], install: `mise use -g pipx:ruff@${V}`, checks: ["lint"] },
  mypy: { stacks: ["python"], version: ["--version"], install: "add mypy to the project's dev dependencies", checks: ["typecheck"] },
  pyright: { stacks: ["python"], version: ["--version"], install: `mise use -g npm:pyright@${V}`, checks: ["typecheck"] },
  "pip-audit": { stacks: ["python"], version: ["--version"], install: `mise use -g pipx:pip-audit@${V}`, checks: ["deps"] },
  mutmut: { stacks: ["python"], version: ["--version"], install: "add mutmut to the project's dev dependencies", checks: ["mutation"] },

  cargo: { stacks: ["rust"], version: ["--version"], install: `mise use rust@${V}`, checks: ["tests", "typecheck", "lint"] },
  // Run as cargo-<name> rather than `cargo <name>`, llvm-cov and mutants still expect their own name
  // as the first argument and refuse a bare --version; cargo-audit takes it either way.
  "cargo-llvm-cov": { stacks: ["rust"], version: ["llvm-cov", "--version"], install: `mise use -g cargo:cargo-llvm-cov@${V}`, checks: ["coverage_new"] },
  "cargo-mutants": { stacks: ["rust"], version: ["mutants", "--version"], install: `mise use -g cargo:cargo-mutants@${V}`, checks: ["mutation"] },
  "cargo-audit": { stacks: ["rust"], version: ["--version"], install: `mise use -g cargo:cargo-audit@${V}`, checks: ["deps"] },

  mvn: { stacks: ["java"], version: ["--version"], install: `mise use java@${V} maven@${V}`, checks: ["tests", "typecheck"] },
  gradle: { stacks: ["java"], version: ["--version"], install: `mise use java@${V} gradle@${V}`, checks: ["tests", "typecheck"] },
};

// createToolbox wraps which/run with a version cache, so every check asks each tool once.
export function createToolbox({ run, which, root }) {
  const versions = new Map();
  return {
    // find returns the path of a tool on PATH, or for local tools under dir's node_modules/.bin.
    find(name, dir) {
      const spec = TOOLS[name];
      if (spec?.local) return localBin(dir || root, name, root);
      return which(name);
    },
    // A check names a tool by what it ran ("mvn checkstyle", "cargo clippy"): the version is the
    // binary's, asked the binary's way. Cached by path and arguments, so which check asks first
    // does not change the answer.
    async version(name, path) {
      const spec = TOOLS[name] || TOOLS[name.split(" ")[0]];
      const key = `${path || name}\0${(spec?.version || []).join(" ")}`;
      if (versions.has(key)) return versions.get(key);
      let v = "";
      if (spec?.version && path) {
        const r = await run(path, spec.version, { cwd: root, timeoutMs: 20_000 });
        if (r.code === 0) v = versionOf(r.stdout || r.stderr);
      }
      versions.set(key, v);
      return v;
    },
  };
}

// versionOf pulls the version number out of a --version banner ("golangci-lint has version
// 2.12.2 built with..." -> "2.12.2"); a banner without one is kept as its first line.
export function versionOf(text) {
  const line = firstLine(text, 120);
  const m = /(?:^|[^\w.]|go|v)(\d+\.\d+(?:\.\d+)?(?:[-+][\w.]+)?)\b/.exec(line);
  return m ? m[1] : line;
}

export function installHint(name) {
  return TOOLS[name]?.install || "";
}

// doctor lists the tools that matter for the detected stacks: found with version, or missing
// with the command that installs it.
export async function doctor({ stacks, toolbox, projects }) {
  const want = new Set(["*", ...stacks]);
  const rows = [];
  for (const [name, spec] of Object.entries(TOOLS)) {
    if (!spec.stacks.some((s) => want.has(s))) continue;
    let path = null;
    if (spec.local) {
      for (const p of projects.filter((p) => p.stack === "ts" || p.stack === "js")) {
        path = toolbox.find(name, p.absDir);
        if (path) break;
      }
    } else path = toolbox.find(name);
    const version = path ? await toolbox.version(name, path) : "";
    rows.push({ name, found: Boolean(path), path, version, install: spec.install, checks: spec.checks || [] });
  }
  return rows;
}

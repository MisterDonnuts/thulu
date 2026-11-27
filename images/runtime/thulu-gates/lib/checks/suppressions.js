// suppressions: a new comment that silences a linter, type checker, scanner or coverage has to
// name the rule and give a reason of at least five words on the same line
// (golangci nolintlint style). It also flags changes to gate configuration: loosening a
// linter or a threshold is the same move as editing a locked test, so it needs a person.

import { basename } from "node:path";

import { t } from "../i18n.js";
import { isSourceFile, lineText, matchesAny } from "../util.js";
import { newResult } from "./common.js";

export const id = "suppressions";

const MIN_REASON_WORDS = 5;

// Each marker: a regex on the line (group "rules" when the syntax carries rule ids, "rest" with
// what follows), whether a rule id is required, and how to cut the reason out of the rest.
// The markers are spelled with character classes so this file does not match itself.
const C = String.raw`(?:\/\/|\/\*|#|--)`;
const MARKERS = [
  { kind: "nolint", re: new RegExp(String.raw`\/\/\s*[n]olint\b(?::(?<rules>[\w,.-]+))?(?<rest>.*)$`), needsRule: true },
  { kind: "eslint-disable", re: new RegExp(String.raw`(?:\/\/|\/\*)\s*[e]slint-disable(?<variant>-next-line|-line)?\b(?<body>.*?)(?:\*\/\s*)?$`), needsRule: true, eslint: true },
  { kind: "ts-nocheck", re: new RegExp(String.raw`\/\/\s*@[t]s-nocheck\b(?<rest>.*)$`), always: true },
  { kind: "ts-ignore", re: new RegExp(String.raw`\/\/\s*@[t]s-(?:ignore|expect-error)\b(?<rest>.*)$`), needsRule: false },
  { kind: "noqa", re: new RegExp(String.raw`#\s*[n]oqa\b(?::\s*(?<rules>[A-Z]+\d+(?:\s*,\s*[A-Z]+\d+)*))?(?<rest>.*)$`, "i"), needsRule: true },
  { kind: "type-ignore", re: new RegExp(String.raw`#\s*[t]ype:\s*ignore(?:\[(?<rules>[\w,\s-]+)\])?(?<rest>.*)$`), needsRule: true },
  { kind: "pyright-ignore", re: new RegExp(String.raw`#\s*[p]yright:\s*ignore(?:\[(?<rules>[\w,\s-]+)\])?(?<rest>.*)$`), needsRule: true },
  { kind: "pylint-disable", re: new RegExp(String.raw`#\s*[p]ylint:\s*disable=(?<rules>[\w,-]+)(?<rest>.*)$`), needsRule: true },
  { kind: "nosec", re: new RegExp(String.raw`${C}\s*#?[n]osec\b(?:\s+(?<rules>[A-Z]+\d+(?:[,\s]+[A-Z]+\d+)*))?(?<rest>.*)$`), needsRule: true },
  { kind: "nosemgrep", re: new RegExp(String.raw`${C}\s*[n]osemgrep\b(?::\s*(?<rules>[\w.-]+(?:\s*,\s*[\w.-]+)*))?(?<rest>.*)$`), needsRule: true },
  { kind: "gitleaks-allow", re: new RegExp(String.raw`[g]itleaks:allow\b(?<rest>.*)$`), needsRule: false },
  { kind: "suppress-warnings", re: new RegExp(String.raw`@[S]uppress(?:Warnings)?\((?<rules>[^)]*)\)(?<rest>.*)$`), needsRule: true, quotedRules: true },
  { kind: "rust-allow", re: new RegExp(String.raw`#!?\[(?:allow|expect)\((?<rules>[^)]*)\)\](?<rest>.*)$`), needsRule: true, rust: true },
  { kind: "no-cover", re: new RegExp(String.raw`(?:#\s*[p]ragma:\s*no\s*cover|${C}\s*(?:[i]stanbul|[c]8|v8)\s+ignore\b[\w\s-]*?(?:\*\/)?|\/\/\s*[c]overage:ignore)(?<rest>.*)$`), needsRule: false },
  { kind: "nosonar", re: new RegExp(String.raw`${C}\s*[N]OSONAR\b(?<rest>.*)$`), needsRule: false },
];

function words(s) {
  return (s.match(/[\p{L}\p{N}][\p{L}\p{N}'_-]*/gu) || []).length;
}

function cleanReason(s) {
  return (s || "")
    .replace(/\*\/\s*$/, "")
    .replace(/^[\s:;,.)\]-]*/, "")
    .replace(/^(?:\/\/|#|--|\/\*)\s*/, "")
    .trim();
}

// inspect returns the suppression on a line, if any: {kind, rules: [], reason, ok, problem}.
export function inspect(text) {
  for (const m of MARKERS) {
    const hit = m.re.exec(text);
    if (!hit) continue;
    const g = hit.groups || {};
    let rules = [];
    let reason = "";
    if (m.eslint) {
      const [r, ...why] = (g.body || "").split(/\s--\s|^--\s/);
      rules = r.split(",").map((x) => x.trim()).filter(Boolean);
      reason = why.join(" -- ");
    } else if (m.rust) {
      const parts = (g.rules || "").split(",").map((x) => x.trim());
      rules = parts.filter((x) => x && !x.startsWith("reason"));
      const inline = /reason\s*=\s*"([^"]*)"/.exec(g.rules || "")?.[1];
      reason = inline || g.rest;
    } else if (m.quotedRules) {
      rules = [...(g.rules || "").matchAll(/"([^"]+)"/g)].map((x) => x[1]);
      reason = g.rest;
    } else {
      rules = (g.rules || "").split(/[,\s]+/).filter(Boolean);
      reason = g.rest;
    }
    reason = cleanReason(reason);
    let problem = null;
    if (m.always || (m.eslint && !g.variant && !rules.length)) problem = "file_level";
    else if (m.needsRule && !rules.length) problem = "no_rule";
    else if (words(reason) < MIN_REASON_WORDS) problem = "no_reason";
    return { kind: m.kind, rules, reason, ok: !problem, problem };
  }
  return null;
}

// Gate configuration. Files in ALWAYS count on any change; CONDITIONAL ones only when a changed
// line (added or removed) matches, since tsconfig.json or package.json change for other reasons.
const ALWAYS = [
  ".golangci.{yml,yaml,toml,json}",
  "eslint.config.*",
  ".eslintrc*",
  ".eslintignore",
  ".prettierignore",
  ".semgrepignore",
  ".semgrep.{yml,yaml}",
  "**/.semgrep/**",
  ".gitleaks.toml",
  ".gitleaksignore",
  "ruff.toml",
  ".ruff.toml",
  "mypy.ini",
  ".mypy.ini",
  ".coveragerc",
  "pyrightconfig.json",
  "stryker.conf.*",
  "stryker.config.*",
  ".thulu/gates.json",
  ".trivyignore*",
  "codecov.yml",
  ".codecov.yml",
  ".size-limit.{json,js,cjs,mjs}",
  "lighthouserc.*",
  ".lighthouserc.*",
  ".gremlins.{yaml,yml}",
  "deny.toml",
  "clippy.toml",
  ".clippy.toml",
  "checkstyle*.xml",
  "*govulncheck-allow*",
  ".jscpd.json",
  ".github/workflows/**",
  ".gitlab-ci.yml",
  ".pre-commit-config.yaml",
];
const CONDITIONAL = [
  { glob: "tsconfig*.json", re: /strict|noImplicit|skipLibCheck|noUnchecked|"exclude"|checkJs/ },
  { glob: "pyproject.toml", re: /\[tool\.(ruff|mypy|pyright|coverage|pytest|mutmut|bandit)|noqa|ignore|omit|exclude|fail_under|strict|select/ },
  { glob: "setup.cfg", re: /flake8|mypy|coverage|omit|ignore|fail_under/ },
  { glob: "package.json", re: /eslint|prettier|"jest"|coverageThreshold|stryker|"lint"/ },
  { glob: "{vitest,jest,vite}.config.*", re: /coverage|threshold|exclude/ },
];

export function gateConfigReason(path, d) {
  if (matchesAny(path, ALWAYS)) return "always";
  const c = CONDITIONAL.find((x) => matchesAny(path, [x.glob]));
  if (!c) return null;
  const changed = [...(d.removed || []), ...[...d.added].map((n) => d._text?.(n) ?? "")];
  return changed.some((l) => c.re.test(l)) ? "lines" : null;
}

export async function run(ctx) {
  const res = newResult(id);
  res.tool = "thulu-gates/suppressions";
  const valid = [];
  let scanned = 0;
  for (const [file, d] of ctx.diff) {
    const reason = gateConfigReason(file, { ...d, _text: (n) => lineText(ctx.root, file, n) });
    if (reason) {
      const first = [...d.added].sort((a, b) => a - b)[0] || 0;
      res.findings.push({
        rule: "gate-config-changed",
        severity: "high",
        file,
        line: first,
        message: t("suppressions.gate_config", { file: basename(file) }),
        blocking: true,
      });
    }
    if (!isSourceFile(file)) continue;
    for (const n of [...d.added].sort((a, b) => a - b)) {
      const text = lineText(ctx.root, file, n);
      if (!text) continue;
      scanned++;
      const s = inspect(text);
      if (!s) continue;
      if (s.ok) {
        valid.push({ file, line: n, kind: s.kind, rules: s.rules, reason: s.reason });
        continue;
      }
      res.findings.push({
        rule: `suppression-${s.problem.replace(/_/g, "-")}`,
        severity: "high",
        file,
        line: n,
        message: t(`suppressions.${s.problem}`, { kind: s.kind, min: MIN_REASON_WORDS }),
        blocking: true,
      });
    }
  }
  res.metrics.lines_scanned = scanned;
  res.metrics.valid = valid;
  const blocking = res.findings.length;
  if (blocking) {
    res.status = "fail";
    res.blocking = true;
    res.summary = t("suppressions.fail", { count: blocking });
  } else {
    res.status = "pass";
    res.summary = valid.length ? t("suppressions.valid_only", { count: valid.length }) : t("suppressions.none");
  }
  return res;
}

// sast: Semgrep with explicit rulesets (never --config auto, which sends the project URL to the
// registry) plus the repo's own rules, only results that base does not have. A new ERROR (high)
// blocks; WARNING is a warning.

import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

import { isDirty } from "../git.js";
import { t } from "../i18n.js";
import { fromSarif } from "../sarif.js";
import { firstLine, tryJSON } from "../util.js";
import { exec, fresh, isBlockingSeverity, missingTool, newResult, onlyAdded, settle, skip, timedOut, use } from "./common.js";

export const id = "sast";

const LANG_RULESETS = { go: "p/golang", ts: "p/typescript", js: "p/javascript", python: "p/python", java: "p/java", rust: "p/rust" };
const REPO_RULES = [".semgrep.yml", ".semgrep.yaml", ".semgrep"];

export async function run(ctx) {
  const res = newResult(id);
  if (![...ctx.diff.keys()].length) return skip(res, t("common.no_changes"));
  const bin = ctx.toolbox.find("semgrep");
  if (!bin) return missingTool(res, "semgrep");
  await use(ctx, res, "semgrep", bin);
  const configs = [...(ctx.cfg.semgrep?.configs || ["p/default"])];
  for (const p of ctx.projects) if (LANG_RULESETS[p.stack] && !configs.includes(LANG_RULESETS[p.stack])) configs.push(LANG_RULESETS[p.stack]);
  for (const r of REPO_RULES) if (existsSync(join(ctx.root, r))) configs.push(r);
  const sarifPath = join(ctx.out, "raw", "semgrep.sarif");
  fresh(sarifPath);
  const args = ["scan", "--metrics=off", "--quiet", "--sarif", `--sarif-output=${sarifPath}`];
  for (const c of configs) args.push("--config", c);
  for (const rule of ctx.cfg.semgrep?.exclude_rules || []) args.push("--exclude-rule", rule);
  // --baseline-commit refuses a dirty tree; then the scan covers everything and the diff filter
  // below does the narrowing.
  const dirty = await isDirty(ctx.run, ctx.root);
  if (!dirty) args.push(`--baseline-commit=${ctx.base}`);
  res.metrics.baseline = dirty ? "diff-filter" : "baseline-commit";
  const r = await exec(ctx, res, bin, args);
  if (r.timedOut) return timedOut(res, ctx.timeoutMs(id) / 1000, false);
  const doc = existsSync(sarifPath) ? tryJSON(readFileSync(sarifPath, "utf8")) : undefined;
  // 0 is a clean scan and 1 findings under --error; anything above is semgrep failing (bad
  // config, registry unreachable), even when it left a SARIF with what it managed to scan.
  if (!doc || r.code > 1 || r.code === null) {
    res.status = "error";
    res.summary = t("common.tool_failed", { tool: "semgrep", detail: firstLine(r.stderr || r.stdout) || `exit ${r.code}` });
    return res;
  }
  const findings = onlyAdded(ctx, fromSarif(doc, ctx.root)).map((f) => ({ ...f, blocking: isBlockingSeverity(f.severity) }));
  res.findings.push(...findings.filter((f) => f.severity !== "low" && f.severity !== "info"));
  res.metrics.low_findings = findings.length - res.findings.length;
  res.metrics.configs = configs;
  return settle(res);
}

// secrets: gitleaks over every commit of the branch (a secret committed and deleted later is
// still in history) and over the uncommitted files. Any finding blocks. Without gitleaks a short
// list of high-precision patterns runs over the added lines, and the summary says so.

import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";

import { t } from "../i18n.js";
import { fromSarif } from "../sarif.js";
import { firstLine, lineText, tryJSON } from "../util.js";
import { exec, fresh, newResult, settle, skip, timedOut, use } from "./common.js";

export const id = "secrets";

// Patterns with almost no false positives; a full rule set is gitleaks' job.
export const BUILTIN_PATTERNS = [
  ["private-key", /-----BEGIN (?:RSA |EC |DSA |OPENSSH |PGP )?PRIVATE KEY(?: BLOCK)?-----/],
  ["aws-access-key-id", /\b(?:AKIA|ASIA)[0-9A-Z]{16}\b/],
  ["github-token", /\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,}\b/],
  ["github-fine-grained-pat", /\bgithub_pat_[A-Za-z0-9_]{60,}\b/],
  ["gitlab-token", /\bglpat-[A-Za-z0-9_-]{20,}\b/],
  ["slack-token", /\bxox[abprs]-[A-Za-z0-9-]{10,}\b/],
  ["anthropic-api-key", /\bsk-ant-[A-Za-z0-9_-]{20,}\b/],
  ["openai-api-key", /\bsk-(?:proj-)?[A-Za-z0-9]{32,}\b/],
  ["google-api-key", /\bAIza[0-9A-Za-z_-]{35}\b/],
  ["stripe-secret-key", /\b(?:sk|rk)_live_[0-9A-Za-z]{20,}\b/],
];

export function scanAddedLines(root, diff) {
  const out = [];
  for (const [file, d] of diff) {
    for (const n of d.added) {
      const text = lineText(root, file, n);
      for (const [rule, re] of BUILTIN_PATTERNS) {
        if (re.test(text)) out.push({ rule, severity: "critical", file, line: n, message: t("secrets.builtin_match", { rule }), blocking: true });
      }
    }
  }
  return out;
}

function gitleaksFailed(res, r) {
  res.status = "error";
  res.blocking = false;
  const detail = (r.stderr || "").split("\n").find((l) => /FTL|ERR/.test(l)) || r.stderr || r.stdout;
  res.summary = t("common.tool_failed", { tool: "gitleaks", detail: firstLine(detail.replace(/\x1b\[[0-9;]*m/g, "")) || `exit ${r.code}` });
  return res;
}

export async function run(ctx) {
  const res = newResult(id);
  const bin = ctx.toolbox.find("gitleaks");
  // An empty net diff can still hide a secret in history (committed, then deleted), which only
  // gitleaks sees; the built-in patterns read the tree and have nothing to scan.
  if (!ctx.diff.size && (!bin || ctx.base === ctx.head)) return skip(res, t("common.no_changes"));
  if (!bin) {
    res._tools.push({ name: "thulu-gates/secrets", version: "" });
    res.findings.push(...scanAddedLines(ctx.root, ctx.diff));
    res.metrics.engine = "builtin";
    settle(res, { passSummary: t("secrets.builtin_clean"), failSummary: t("secrets.builtin_found", { count: res.findings.length }) });
    return res;
  }
  await use(ctx, res, "gitleaks", bin);
  const version = await ctx.toolbox.version("gitleaks", bin);
  const m = /(\d+)\.(\d+)/.exec(version);
  const modern = !m || Number(m[1]) > 8 || (Number(m[1]) === 8 && Number(m[2]) >= 19);
  const config = existsSync(join(ctx.root, ".gitleaks.toml")) ? ["--config", join(ctx.root, ".gitleaks.toml")] : [];
  const histSarif = join(ctx.out, "raw", "gitleaks-history.sarif");
  mkdirSync(dirname(histSarif), { recursive: true });
  fresh(histSarif);
  const common = ["--redact", "--no-banner", "--exit-code", "1", "--report-format", "sarif", ...config];
  const histArgs = modern
    ? ["git", `--log-opts=${ctx.base}..HEAD`, ...common, "--report-path", histSarif, "."]
    : ["detect", "--source", ".", `--log-opts=${ctx.base}..HEAD`, ...common, "--report-path", histSarif];
  const r = await exec(ctx, res, bin, histArgs);
  if (r.timedOut) return timedOut(res, ctx.timeoutMs(id) / 1000, false);
  // gitleaks exits 1 both for leaks and for its own fatal errors (zerolog's Fatal); only the
  // report tells them apart, so a missing or unreadable report is an error, never a clean scan.
  const histDoc = existsSync(histSarif) ? tryJSON(readFileSync(histSarif, "utf8")) : undefined;
  // A git error inside gitleaks (bad --log-opts, a shallow clone) is logged and the scan still
  // reports "no leaks found" with exit 0, over zero commits.
  if ((r.code !== 0 && r.code !== 1) || !histDoc || /\[git\] fatal:/.test(r.stderr || "")) return gitleaksFailed(res, r);
  const findings = fromSarif(histDoc, ctx.root);

  // Uncommitted and untracked files: copy them to a scratch dir and scan that, so the tree's
  // preexisting files stay out of the result.
  const dirty = [...ctx.diff.values()].filter((d) => d.untracked || d.dirty).map((d) => d.path);
  if (dirty.length) {
    const scratch = mkdtempSync(join(tmpdir(), "thulu-gates-secrets-"));
    try {
      for (const f of dirty) {
        mkdirSync(dirname(join(scratch, f)), { recursive: true });
        copyFileSync(join(ctx.root, f), join(scratch, f));
      }
      const dirSarif = join(ctx.out, "raw", "gitleaks-dir.sarif");
      fresh(dirSarif);
      const dirArgs = modern ? ["dir", ...common, "--report-path", dirSarif, scratch] : ["detect", "--no-git", "--source", scratch, ...common, "--report-path", dirSarif];
      const d = await exec(ctx, res, bin, dirArgs);
      if (d.timedOut) return timedOut(res, ctx.timeoutMs(id) / 1000, false);
      const dirDoc = existsSync(dirSarif) ? tryJSON(readFileSync(dirSarif, "utf8")) : undefined;
      if ((d.code !== 0 && d.code !== 1) || !dirDoc) return gitleaksFailed(res, d);
      findings.push(...fromSarif(dirDoc, scratch));
    } finally {
      rmSync(scratch, { recursive: true, force: true });
    }
  }
  const seen = new Set();
  for (const f of findings) {
    const k = `${f.rule}\0${f.file}\0${f.line}`;
    if (seen.has(k)) continue;
    seen.add(k);
    res.findings.push({ ...f, severity: "critical", blocking: true });
  }
  return settle(res, { passSummary: t("secrets.clean"), failSummary: t("secrets.found", { count: res.findings.length }) });
}

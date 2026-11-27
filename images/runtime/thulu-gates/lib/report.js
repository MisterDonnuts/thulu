// report.json (contract schema 1, read by the verifier agent and the auditor) and report.md (the
// same result for a person: one table of checks and only the findings that matter).

import { t } from "./i18n.js";

const MAX_BLOCKING_PER_CHECK = 30;
const MAX_WARN_PER_CHECK = 10;

// publicCheck leaves out the working fields a check keeps under a leading underscore.
export function publicCheck(res) {
  return Object.fromEntries(Object.entries(res).filter(([k]) => !k.startsWith("_")));
}

// notVerified lists the checks whose result proves nothing: they did not finish, were skipped with
// --skip, or depend on one that did not (coverage_new without a finished tests run). Checks left
// out by --only and the opt-in ones nobody asked for are not listed.
export function notVerified(checks, selection) {
  const out = [];
  for (const c of checks) {
    if (c.status === "error") out.push({ id: c.id, reason: c.summary });
    else if (c.status === "skip" && (c.metrics?.not_verified || selection?.get(c.id) === t("cli.skipped_by_flag"))) out.push({ id: c.id, reason: c.summary });
  }
  return out;
}

// isUnverified is a check that ended without a result: it did not finish, or it was skipped because
// what it depends on did not.
export function isUnverified(c) {
  return c.status === "error" || (c.status === "skip" && Boolean(c.metrics?.not_verified));
}

// applyAcceptance turns the unverified checks a person accepted in the project's profile into
// "accepted": they keep their summary, carry the reason, never block and do not fail the verdict.
// A check with blocking findings is never accepted: those findings are real. It returns copies.
export function applyAcceptance(checks, accepted = {}) {
  return checks.map((c) => {
    const reason = accepted[c.id];
    if (!reason || c.blocking || !isUnverified(c)) return c;
    return { ...c, status: "accepted", blocking: false, metrics: { ...c.metrics, accepted: { reason, status: c.status } } };
  });
}

export function buildReport({ base, head, dirty, defaultBranch, stacks, projects, repoCommands, versions, cfg, checks, selection, quarantine, cacheDir, startedAt, finishedAt, out, reused = null }) {
  const blocking = checks.filter((c) => c.blocking).map((c) => c.id);
  const errored = checks.filter((c) => c.status === "error").map((c) => c.id);
  const tests = checks.find((c) => c.id === "tests");
  return {
    schema: 1,
    base,
    head,
    stack: stacks,
    verdict: blocking.length || errored.length ? "fail" : "pass",
    blocking,
    checks: checks.map(publicCheck),
    errors: errored,
    not_verified: notVerified(checks, selection),
    accepted: checks.filter((c) => c.status === "accepted").map((c) => ({ id: c.id, reason: c.metrics.accepted.reason, status: c.metrics.accepted.status, summary: c.summary })),
    preexisting: Object.fromEntries(checks.filter((c) => c.metrics?.preexisting).map((c) => [c.id, c.metrics.preexisting])),
    quarantine: {
      file: quarantine?.path || null,
      not_run: tests?.metrics?.quarantined || [],
      added: (quarantine?.added || []).map((e) => ({ id: e.id, project: e.project || ".", reason: e.reason })),
      unsupported: tests?.metrics?.quarantine_unsupported || [],
    },
    cache: cacheDir || null,
    reused,
    dirty,
    default_branch: defaultBranch,
    projects: projects.map((p) => ({ stack: p.stack, dir: p.dir || ".", manager: p.manager || null, files: p.files.length })),
    repo_commands: repoCommands,
    declared_versions: versions,
    config: {
      source: cfg.source,
      coverage_min: cfg.coverage_min,
      coverage_min_lines: cfg.coverage_min_lines,
      dup_max: cfg.dup_max,
      critical: cfg.critical,
      mutation_min_efficacy: cfg.mutation_min_efficacy,
      perf_threshold_pct: cfg.perf?.threshold_pct,
      profile: cfg.profile || null,
    },
    started_at: new Date(startedAt).toISOString(),
    duration_ms: finishedAt - startedAt,
    out,
  };
}

const cell = (s) => String(s ?? "").replace(/\|/g, "\\|").replace(/\n+/g, " ");

function statusLabel(s) {
  return t(`report.status.${s}`);
}

function duration(ms) {
  if (ms < 1000) return `${ms} ms`;
  const s = Math.round(ms / 1000);
  return s < 60 ? `${s} s` : `${Math.floor(s / 60)} min ${s % 60} s`;
}

function where(f) {
  if (!f.file) return "";
  return f.line ? `\`${f.file}:${f.line}\`` : `\`${f.file}\``;
}

function findingLine(f) {
  const loc = where(f);
  return `- ${loc ? loc + " " : ""}**${cell(f.rule)}** (${f.severity}): ${cell(f.message)}`;
}

export function renderMarkdown(report) {
  const L = [];
  const verdict = report.verdict === "pass" ? t("report.verdict_pass") : t("report.verdict_fail");
  L.push(`# thulu-gates: ${verdict}`);
  L.push("");
  const head = report.head.slice(0, 12) + (report.dirty ? ` ${t("report.dirty")}` : "");
  L.push(
    t("report.header", {
      base: report.base.slice(0, 12),
      head,
      stack: report.stack.length ? report.stack.join(", ") : "-",
      duration: duration(report.duration_ms),
    }),
  );
  if (report.reused) {
    const r = report.reused;
    L.push("", r.rerun.length ? t("report.reused_partly", { time: r.from, out: r.out, ids: r.rerun.join(", ") }) : t("report.reused", { time: r.from, out: r.out }));
  }
  if (report.blocking.length) L.push("", t("report.blocking_list", { ids: report.blocking.join(", ") }));
  if (report.errors.length) L.push("", t("report.error_list", { ids: report.errors.join(", ") }));
  L.push("");
  L.push(`| ${t("report.col.check")} | ${t("report.col.status")} | ${t("report.col.blocks")} | ${t("report.col.tool")} | ${t("report.col.summary")} | ${t("report.col.time")} |`);
  L.push("|---|---|---|---|---|---|");
  for (const c of report.checks) {
    const tool = [c.tool, c.version].filter(Boolean).join(" ");
    L.push(`| ${c.id} | ${statusLabel(c.status)} | ${c.blocking ? t("report.yes") : ""} | ${cell(tool)} | ${cell(c.summary)} | ${c.duration_ms ? duration(c.duration_ms) : ""} |`);
  }

  const blockingChecks = report.checks.filter((c) => c.findings.some((f) => f.blocking));
  if (blockingChecks.length) {
    L.push("", `## ${t("report.blocking_findings")}`);
    for (const c of blockingChecks) {
      const list = c.findings.filter((f) => f.blocking);
      L.push("", `### ${c.id}`, "");
      for (const f of list.slice(0, MAX_BLOCKING_PER_CHECK)) L.push(findingLine(f));
      if (list.length > MAX_BLOCKING_PER_CHECK) L.push(t("report.more", { count: list.length - MAX_BLOCKING_PER_CHECK, sarif: c.sarif || "report.json" }));
    }
  }

  const warnChecks = report.checks.filter((c) => c.findings.some((f) => !f.blocking && !f.preexisting));
  if (warnChecks.length) {
    L.push("", `## ${t("report.warnings")}`);
    for (const c of warnChecks) {
      const list = c.findings.filter((f) => !f.blocking && !f.preexisting);
      L.push("", `### ${c.id}`, "");
      for (const f of list.slice(0, MAX_WARN_PER_CHECK)) L.push(findingLine(f));
      if (list.length > MAX_WARN_PER_CHECK) L.push(t("report.more", { count: list.length - MAX_WARN_PER_CHECK, sarif: c.sarif || "report.json" }));
    }
  }

  const preChecks = report.checks.filter((c) => c.metrics?.preexisting);
  if (preChecks.length) {
    L.push("", `## ${t("report.preexisting")}`);
    for (const c of preChecks) {
      const list = c.findings.filter((f) => f.preexisting);
      L.push("", `### ${c.id}: ${c.metrics.preexisting}`, "");
      for (const f of list.slice(0, MAX_WARN_PER_CHECK)) L.push(findingLine(f));
      if (list.length > MAX_WARN_PER_CHECK) L.push(t("report.more", { count: list.length - MAX_WARN_PER_CHECK, sarif: c.sarif || "report.json" }));
    }
  }

  const q = report.quarantine;
  if (q && (q.not_run.length || q.added.length || q.unsupported.length)) {
    const notRun = q.not_run.length || q.unsupported.length;
    L.push("", `## ${t(notRun ? "report.quarantine" : "report.quarantine_added")}`, "");
    for (const e of q.not_run) L.push(`- \`${cell(e.id)}\` (${e.project}, ${e.reason})${e.changed ? ` ${t("report.quarantine_changed")}` : ""}`);
    for (const u of q.unsupported) L.push(`- ${u.project}: ${t("tests.quarantine_unsupported", { runners: u.runner })}: ${u.tests.map((x) => `\`${cell(x)}\``).join(", ")}`);
    if (q.added.length) {
      if (notRun) L.push("", t("report.quarantine_added"), "");
      for (const e of q.added) L.push(`- \`${cell(e.id)}\` (${e.project}, ${e.reason})`);
    }
    if (q.file) L.push("", t("report.quarantine_file", { path: q.file }));
  }

  if (report.not_verified?.length) {
    L.push("", `## ${t("report.not_verified")}`, "");
    for (const n of report.not_verified) {
      const c = report.checks.find((x) => x.id === n.id);
      L.push(`- ${n.id}: ${t(c?.status === "error" ? "report.not_verified_error" : "report.not_verified_skip", { why: cell(n.reason) })}`);
    }
  }

  if (report.accepted?.length) {
    L.push("", `## ${t("report.accepted")}`, "");
    for (const a of report.accepted) L.push(`- ${a.id}: ${t("report.accepted_line", { reason: cell(a.reason), why: cell(a.summary) })}`);
  }

  const perf = report.checks.find((c) => c.id === "perf" && c.metrics?.benchmarks?.length);
  if (perf) {
    L.push("", `## ${t("report.perf")}`, "", perf.metrics.method, "");
    L.push(`| ${t("report.col.benchmark")} | ${t("report.col.base")} | ${t("report.col.head")} | Δ | p | n | ${t("report.col.result")} |`);
    L.push("|---|---|---|---|---|---|---|");
    const fmt = (v, ci) => (v === undefined ? "" : `${fmtNs(v)} [${fmtNs(ci[0])}, ${fmtNs(ci[1])}]`);
    for (const r of perf.metrics.benchmarks) {
      L.push(`| ${cell(r.name)} | ${fmt(r.base_median, r.base_ci || [])} | ${fmt(r.head_median, r.head_ci || [])} | ${r.delta_pct ?? ""}${r.delta_pct !== undefined ? "%" : ""} | ${r.p ?? ""} | ${r.n} | ${t(`report.perf_verdict.${r.verdict}`)} |`);
    }
  }

  const listed = new Set((report.not_verified || []).map((n) => n.id));
  const skipped = report.checks.filter((c) => c.status === "skip" && !listed.has(c.id));
  if (skipped.length) {
    L.push("", `## ${t("report.not_run")}`, "");
    for (const c of skipped) L.push(`- ${c.id}: ${cell(c.summary)}`);
  }

  const cmds = report.checks.filter((c) => c.command);
  if (cmds.length) {
    L.push("", `## ${t("report.reproduce")}`, "", "```sh");
    // report.json keeps every command in order (perf runs the same ones round after round).
    for (const c of cmds) L.push(`# ${c.id}`, ...new Set(c.command.split(" ; ")));
    L.push("```");
  }
  L.push("");
  return L.join("\n");
}

function fmtNs(ns) {
  if (!Number.isFinite(ns)) return "";
  if (ns >= 1e9) return `${(ns / 1e9).toFixed(2)} s`;
  if (ns >= 1e6) return `${(ns / 1e6).toFixed(2)} ms`;
  if (ns >= 1e3) return `${(ns / 1e3).toFixed(2)} µs`;
  return `${ns.toFixed(1)} ns`;
}

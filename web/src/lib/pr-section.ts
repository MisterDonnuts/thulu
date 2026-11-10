import { t } from "../i18n";
import { safeUrl } from "./safe-url";
import { runAgoLabel } from "./test-cases";
import { fmtDate } from "./time";
import {
  badgeHTML,
  calloutHTML,
  esc,
  factsHTML,
  icoSvg,
  listRowHTML,
  rowGroupBoxedHTML,
  sectionHeadHTML,
  skeletonHTML,
  type BadgeTone,
} from "./ui";

export type PRState = "open" | "draft" | "merged" | "closed";
export type CIState = "none" | "running" | "passed" | "failed";
export type ReviewState = "none" | "pending" | "approved" | "changes_requested";
export type Mergeable = "ready" | "blocked" | "behind" | "conflicts" | "unknown";

export interface PRCI {
  source: string;
  total: number;
  passed: number;
  failed: number;
  running: number;
  state: CIState;
}

export interface PullRequestStatus {
  project_id: string;
  project_name: string;
  url: string;
  number?: number;
  title?: string;
  state?: PRState;
  head?: string;
  base?: string;
  ci?: PRCI;
  review?: ReviewState;
  mergeable?: Mergeable;
  changed_files?: number;
  additions?: number;
  deletions?: number;
  commits?: number;
  updated_at?: string;
  error?: string;
  error_message?: string;
}

export interface PRLink {
  project_id: string;
  project_name: string;
  url: string;
}

export interface PRSectionInput {
  links: PRLink[];
  rows?: PullRequestStatus[];
  failed: boolean;
}

export interface PRSectionDeps {
  openKeys: ReadonlySet<string>;
}

export const PR_NO_CREDENTIAL = "api.pull_requests.errors.no_credential";

export const PR_POLL_ACTIVE_MS = 20_000;
export const PR_POLL_IDLE_MS = 60_000;

export function prIsActive(pr: PullRequestStatus): boolean {
  if (pr.error || pr.state === "merged" || pr.state === "closed") return false;
  return pr.ci?.state === "running" || pr.review === "pending";
}

export function prPollDelay(rows?: PullRequestStatus[]): number {
  return rows?.some(prIsActive) ? PR_POLL_ACTIVE_MS : PR_POLL_IDLE_MS;
}

export function prMergeReady(pr: PullRequestStatus): boolean {
  return (
    pr.state === "open" &&
    pr.mergeable === "ready" &&
    pr.review === "approved" &&
    (pr.ci?.state === "passed" || pr.ci?.state === "none")
  );
}

export function prTone(pr: PullRequestStatus): string {
  if (pr.error) return "error";
  if (pr.state === "merged") return "merged";
  if (pr.state === "closed") return "closed";
  if (pr.state === "draft") return "draft";
  if (
    pr.ci?.state === "failed" ||
    pr.review === "changes_requested" ||
    pr.mergeable === "conflicts"
  )
    return "failed";
  if (prMergeReady(pr)) return "ready";
  if (pr.ci?.state === "running") return "running";
  return "open";
}

export function prSectionSignature(input: PRSectionInput): string {
  return JSON.stringify({
    links: input.links.map((l) => [l.project_id, l.project_name, l.url]),
    rows: input.rows ?? null,
    failed: input.failed,
  });
}

const STATE_LABEL: Record<PRState, string> = {
  open: "lib.pr_section.state.open",
  draft: "lib.pr_section.state.draft",
  merged: "lib.pr_section.state.merged",
  closed: "lib.pr_section.state.closed",
};

const STATE_TONE: Record<PRState, BadgeTone> = {
  open: "neutral",
  draft: "neutral",
  merged: "accent",
  closed: "muted",
};

const REVIEW_LABEL: Record<ReviewState, string> = {
  none: "lib.pr_section.review.none",
  pending: "lib.pr_section.review.pending",
  approved: "lib.pr_section.review.approved",
  changes_requested: "lib.pr_section.review.changes_requested",
};

const REVIEW_ICON: Partial<Record<ReviewState, string>> = {
  pending: "i-eye",
  approved: "i-check",
  changes_requested: "i-alert",
};

const MERGEABLE_LABEL: Record<Mergeable, string> = {
  ready: "lib.pr_section.mergeable.ready",
  blocked: "lib.pr_section.mergeable.blocked",
  behind: "lib.pr_section.mergeable.behind",
  conflicts: "lib.pr_section.mergeable.conflicts",
  unknown: "lib.pr_section.mergeable.unknown",
};

const CI_ICON: Record<Exclude<CIState, "none">, string> = {
  running: "i-refresh",
  passed: "i-check",
  failed: "i-x",
};

const CI_TONE: Record<Exclude<CIState, "none">, BadgeTone> = {
  running: "info",
  passed: "ok",
  failed: "danger",
};

export function prSectionHTML(input: PRSectionInput, deps: PRSectionDeps): string {
  if (!input.links.length) return "";
  const byProject = new Map((input.rows ?? []).map((r) => [r.project_id, r]));
  const rows = input.links
    .map((link) => {
      const pr = byProject.get(link.project_id);
      if (pr?.url === link.url) {
        if (pr.error === PR_NO_CREDENTIAL) return credentialRowHTML(link);
        return pr.error ? plainRowHTML(link, pr.error_message ?? "") : rowHTML(pr, deps);
      }
      if (input.rows === undefined && !input.failed) return loadingRowHTML(link);
      return plainRowHTML(link, "");
    })
    .join("");
  const note = input.failed
    ? `<div class="pr-note muted" role="status">${esc(t("lib.pr_section.load_failed"))}</div>`
    : "";
  return `${sectionHeadHTML({ title: t("lib.pr_section.title"), count: input.links.length })}
      ${note}
      <div class="pr-list">${rows}</div>`;
}

export function prDetailId(projectID: string): string {
  return `pr-detail-${projectID}`;
}

function rowHTML(pr: PullRequestStatus, deps: PRSectionDeps): string {
  const open = deps.openKeys.has(pr.project_id);
  const title = pr.title ?? pr.url;
  const detailId = prDetailId(pr.project_id);
  const head = listRowHTML({
    as: "button",
    size: "sm",
    wrap: true,
    expanded: open,
    leadHTML: badgeHTML(`#${pr.number ?? ""}`, "neutral", { mono: true, cls: "pr-num" }),
    title,
    titleAttrs: { title: `${pr.project_name} · ${title}` },
    asideHTML: `<span class="pr-indicators">${indicatorsHTML(pr)}</span>`,
    attrs: { "data-pr-toggle": pr.project_id, "aria-controls": detailId },
  });
  const facts = FACTS.map((f) => ({ label: t(f.label), valueHTML: f.value(pr) })).filter(
    (f) => f.valueHTML !== "",
  );
  const hint =
    pr.ci?.source === "actions"
      ? `<div class="pr-hint">${icoSvg("i-info", { size: "sm" })}<span>${esc(t("lib.pr_section.ci.actions_only"))}</span></div>`
      : "";
  const detail = `<div class="row-detail pr-detail" id="${esc(detailId)}"${open ? "" : " hidden"}>${factsHTML(facts)}${hint}${githubLinkHTML(pr.url)}</div>`;
  return rowGroupBoxedHTML(head + detail, {
    edge: true,
    cls: `pr-row pr-row-${prTone(pr)}`,
    attrs: { "data-key": pr.project_id },
  });
}

function indicatorsHTML(pr: PullRequestStatus): string {
  return [statePillHTML(pr), ciChipHTML(pr.ci), reviewIconHTML(pr), readyHTML(pr)].join("");
}

function statePillHTML(pr: PullRequestStatus): string {
  if (!pr.state || pr.state === "open") return "";
  return badgeHTML(t(STATE_LABEL[pr.state]), STATE_TONE[pr.state], {
    shape: "pill",
    cls: `pr-pill pr-pill-${pr.state}`,
  });
}

function ciChipHTML(ci?: PRCI): string {
  if (!ci || ci.state === "none" || !ci.total) return "";
  const icon = (CI_ICON as Partial<Record<string, string>>)[ci.state];
  if (!icon) return "";
  const tip = t("lib.pr_section.ci.tooltip", {
    passed: ci.passed,
    failed: ci.failed,
    running: ci.running,
    total: ci.total,
  });
  return badgeHTML(`${ci.passed}/${ci.total}`, CI_TONE[ci.state], {
    shape: "pill",
    icon,
    spin: ci.state === "running",
    title: tip,
    cls: `pr-ci pr-ci-${ci.state}`,
    attrs: { "aria-label": tip },
  });
}

function reviewIconHTML(pr: PullRequestStatus): string {
  const id = pr.review ? REVIEW_ICON[pr.review] : undefined;
  if (!pr.review || !id) return "";
  const label = t(REVIEW_LABEL[pr.review]);
  return `<span class="pr-review pr-review-${esc(pr.review)}" title="${esc(label)}" aria-label="${esc(label)}">${icoSvg(id, { size: "sm" })}</span>`;
}

function readyHTML(pr: PullRequestStatus): string {
  if (!prMergeReady(pr)) return "";
  const label = t("lib.pr_section.ready_hint");
  return badgeHTML(t("lib.pr_section.ready"), "ok", {
    shape: "pill",
    icon: "i-check",
    title: label,
    cls: "pr-pill pr-pill-ready",
  });
}

interface Fact {
  label: string;
  value: (pr: PullRequestStatus) => string;
}

const FACTS: Fact[] = [
  { label: "lib.pr_section.facts.project", value: (pr) => esc(pr.project_name) },
  {
    label: "lib.pr_section.facts.files",
    value: (pr) =>
      pr.changed_files === undefined
        ? ""
        : esc(t("lib.pr_section.files_count", { count: pr.changed_files })),
  },
  {
    label: "lib.pr_section.facts.lines",
    value: (pr) =>
      pr.additions === undefined && pr.deletions === undefined
        ? ""
        : `<span class="pr-add">+${pr.additions ?? 0}</span> <span class="pr-del">−${pr.deletions ?? 0}</span>`,
  },
  {
    label: "lib.pr_section.facts.commits",
    value: (pr) => (pr.commits === undefined ? "" : String(pr.commits)),
  },
  {
    label: "lib.pr_section.facts.branch",
    value: (pr) =>
      pr.head
        ? `<code>${esc(pr.head)}</code> <span class="muted">→</span> <code>${esc(pr.base ?? "")}</code>`
        : "",
  },
  {
    label: "lib.pr_section.facts.ci",
    value: (pr) =>
      !pr.ci || pr.ci.state === "none"
        ? esc(t("lib.pr_section.ci.none"))
        : esc(
            t("lib.pr_section.ci.detail", {
              passed: pr.ci.passed,
              failed: pr.ci.failed,
              running: pr.ci.running,
              total: pr.ci.total,
            }),
          ),
  },
  {
    label: "lib.pr_section.facts.review",
    value: (pr) => (pr.review ? esc(t(REVIEW_LABEL[pr.review])) : ""),
  },
  {
    label: "lib.pr_section.facts.mergeable",
    value: (pr) =>
      pr.state === "open" || pr.state === "draft"
        ? esc(t(MERGEABLE_LABEL[pr.mergeable ?? "unknown"]))
        : "",
  },
  {
    label: "lib.pr_section.facts.updated",
    value: (pr) =>
      pr.updated_at
        ? `<time datetime="${esc(pr.updated_at)}" title="${esc(fmtDate(pr.updated_at))}">${esc(runAgoLabel(pr.updated_at))}</time>`
        : "",
  },
];

function githubLinkHTML(raw: string): string {
  const url = safeUrl(raw);
  if (!url) return "";
  return `<a class="pr-gh-link" href="${esc(url)}" target="_blank" rel="noopener noreferrer">${icoSvg("i-github")}<span>${esc(t("lib.pr_section.open_on_github"))}</span>${icoSvg("i-arrow-up-right")}</a>`;
}

function plainRowHTML(link: PRLink, why: string): string {
  const url = safeUrl(link.url);
  const name = esc(link.project_name || link.project_id);
  const body = url
    ? `<a class="pr-plain-link" href="${esc(url)}" target="_blank" rel="noopener noreferrer">${icoSvg("i-github")}<span>${esc(t("lib.pr_section.pr_of", { project: link.project_name || link.project_id }))}</span>${icoSvg("i-arrow-up-right")}</a>`
    : `<span class="pr-plain-link">${icoSvg("i-github")}<span>${name}</span></span>`;
  const reason = why ? `<span class="pr-why" title="${esc(why)}">${esc(why)}</span>` : "";
  return rowGroupBoxedHTML(`<div class="pr-row-body">${body}${reason}</div>`, {
    edge: true,
    cls: `pr-row pr-row-plain${why ? " pr-row-error" : ""}`,
    attrs: { "data-key": link.project_id },
  });
}

export function prNumberOf(url: string): string {
  return url.match(/\/pull\/(\d+)/)?.[1] ?? "";
}

function credentialRowHTML(link: PRLink): string {
  const url = safeUrl(link.url);
  const num = prNumberOf(link.url);
  const label = num
    ? t("lib.pr_section.pr_number", { number: num })
    : t("lib.pr_section.pr_of", { project: link.project_name || link.project_id });
  const body = url
    ? `<a class="pr-plain-link" href="${esc(url)}" target="_blank" rel="noopener noreferrer">${icoSvg("i-github")}<span>${esc(label)}</span>${icoSvg("i-arrow-up-right")}</a>`
    : `<span class="pr-plain-link">${icoSvg("i-github")}<span>${esc(label)}</span></span>`;
  const callout = calloutHTML({
    tone: "warn",
    icon: "i-warn",
    bodyHTML: `${esc(t("lib.pr_section.no_credential"))} <a href="/credentials/">${esc(t("lib.pr_section.add_credential"))}</a>`,
    cls: "pr-cred",
  });
  return rowGroupBoxedHTML(`<div class="pr-row-body">${body}${callout}</div>`, {
    edge: true,
    cls: "pr-row pr-row-plain pr-row-cred",
    attrs: { "data-key": link.project_id },
  });
}

function loadingRowHTML(link: PRLink): string {
  const body = `<div class="pr-row-body">
        ${skeletonHTML({ tag: "span", cls: "pr-skel-num" })}
        ${skeletonHTML({ tag: "span", cls: "pr-skel-title" })}
        ${skeletonHTML({ shape: "chip", tag: "span", cls: "pr-skel-chip" })}
        <span class="visually-hidden">${esc(t("lib.pr_section.loading", { project: link.project_name || link.project_id }))}</span>
      </div>`;
  return rowGroupBoxedHTML(body, {
    edge: true,
    cls: "pr-row pr-row-loading",
    attrs: { "data-key": link.project_id, "aria-busy": "true" },
  });
}

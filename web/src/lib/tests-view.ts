// The Tests tab of a task: which acceptance criteria the cases prove, what a person must bring so
// the agent can finish (grouped by the kind of need the agent declared, from the daemon's catalog),
// and each case as one line with its last Testing rounds. Everything here reads the structured
// fields the daemon keeps (criteria, covers, preconditions, needs, tried, writes_data, round); an
// older task without them falls back to what it has: criteria parsed from the brief, a blocked case
// with no needs in the "other" row, the round of a run read from the stage moves.

import { t } from "../i18n";
import { splitBrief } from "./brief";
import { roundAt, type Visit } from "./rounds";
import { fmtDayClock } from "./time";
import {
  casePriorityTagHTML,
  statusLabel,
  type TestCase,
  type TestCaseRun,
  type TestCaseStatus,
} from "./test-cases";
import {
  attrsHTML,
  buttonHTML,
  calloutHTML,
  disclosureHTML,
  emptyStateHTML,
  esc,
  fieldHTML,
  icoSvg,
  mdHTML,
  sectionHeadHTML,
} from "./ui";

export interface TvAttachment {
  id: string;
  filename: string;
  mime_type?: string;
}

// The summary's bar: one segment per status, as wide as its share of the cases. Not run fills the
// rest with the track.
const BAR_GROUPS = ["pass", "fail", "blocked", "queued", "waived"] as const;

export function barSegments(
  cases: ReadonlyArray<Pick<TestCase, "status">>,
): Array<{ group: (typeof BAR_GROUPS)[number]; pct: number }> {
  if (!cases.length) return [];
  return BAR_GROUPS.map((group) => ({
    group,
    pct: (cases.filter((c) => groupOf(c.status) === group).length / cases.length) * 100,
  })).filter((s) => s.pct > 0);
}

function barHTML(cases: readonly TestCase[]): string {
  const segs = barSegments(cases)
    .map((s) => `<i class="tv-bar-${s.group}" style="width:${+s.pct.toFixed(3)}%"></i>`)
    .join("");
  return cases.length ? `<div class="tv-bar" aria-hidden="true">${segs}</div>` : "";
}

// ---------- criteria ----------

export interface CriterionLite {
  key: string;
  text: string;
  approved_at?: string | null;
}

const CRITERION_LINE =
  /^\s*(?:[-*+]\s+|\d+[.)]\s+)?(?:\[[ xX]\]\s+)?(?:\*\*|__)?(AC-\d+)(?:\*\*|__|\b)\s*[:.)\-–—]?\s*(?:\*\*|__)?\s*(.*)$/;

// The criteria of a task: the daemon's list, or for a task written before it kept one, the AC-n
// lines of the brief's acceptance-criteria section.
export function criteriaOf(task: {
  criteria?: CriterionLite[] | null;
  body?: string;
}): CriterionLite[] {
  if (task.criteria?.length) return task.criteria;
  const sec = splitBrief(task.body ?? "").sections.find((s) => s.slug === "acceptance-criteria");
  if (!sec) return [];
  const out: CriterionLite[] = [];
  for (const line of sec.source.split("\n")) {
    const m = CRITERION_LINE.exec(line);
    if (m && !out.some((c) => c.key === m[1])) out.push({ key: m[1], text: m[2].trim() });
  }
  return out;
}

export type CriterionState = "ok" | "waived" | "part" | "wait" | "fail" | "untested" | "none";

export function coversOf(c: Pick<TestCase, "covers">): string[] {
  return (c.covers ?? []).filter(Boolean);
}

// How far the cases that cover a criterion prove it: every one passes (ok), passes with some skipped
// by a person (waived), some pass (part), one waits for a person or runs again (wait), one fails
// (fail), none ran yet (untested), no case covers it (none).
export function criterionState(
  key: string,
  cases: ReadonlyArray<Pick<TestCase, "covers" | "status">>,
): CriterionState {
  const cs = cases.filter((c) => coversOf(c).includes(key));
  if (!cs.length) return "none";
  if (cs.some((c) => c.status === "fail")) return "fail";
  const pass = cs.filter((c) => c.status === "pass").length;
  if (pass === cs.length) return "ok";
  if (cs.every((c) => c.status === "pass" || c.status === "waived")) return "waived";
  if (pass) return "part";
  if (cs.some((c) => c.status === "blocked" || c.status === "queued")) return "wait";
  return "untested";
}

export function criterionStateLabel(s: CriterionState): string {
  switch (s) {
    case "ok":
      return t("lib.tests_view.criteria.state.ok");
    case "waived":
      return t("lib.tests_view.criteria.state.waived");
    case "part":
      return t("lib.tests_view.criteria.state.part");
    case "wait":
      return t("lib.tests_view.criteria.state.wait");
    case "fail":
      return t("lib.tests_view.criteria.state.fail");
    case "untested":
      return t("lib.tests_view.criteria.state.untested");
    case "none":
      return t("lib.tests_view.criteria.state.none");
  }
}

// ---------- needs ----------

export type NeedAction = "access" | "by_hand" | "skip" | "rerun" | "review_video";

export interface NeedKindEntry {
  kind: string;
  icon?: string;
  order: number;
  actions: string[];
  secrets: boolean;
  title?: string;
  help?: string;
}

// The sprite has no glyph for some catalog icons; these stand in.
const ICON_FALLBACK: Partial<Record<string, string>> = {
  smartphone: "monitor",
  globe: "link",
  key: "lock",
};
const SPRITE = new Set([
  "monitor",
  "link",
  "lock",
  "server",
  "eye",
  "help",
  "user",
  "alert",
  "flag",
  "clock",
  "settings",
]);

export function needIcon(icon: string | undefined): string {
  const name = ICON_FALLBACK[icon ?? ""] ?? icon ?? "";
  return SPRITE.has(name) ? name : "help";
}

// A kind the catalog does not list (an older daemon, a catalog that did not load) still gets a
// row: it offers testing by hand and skipping, like "other".
function unknownKind(kind: string): NeedKindEntry {
  return { kind, icon: "help", order: 1000, actions: ["by_hand", "skip"], secrets: false };
}

export function needTitle(k: NeedKindEntry): string {
  const title = (k.title ?? "").trim();
  return title || t("lib.tests_view.needs.untitled");
}

export function newestRuns(c: Pick<TestCase, "runs">): TestCaseRun[] {
  return [...(c.runs ?? [])].sort((a, b) =>
    a.ran_at < b.ran_at ? 1 : a.ran_at > b.ran_at ? -1 : 0,
  );
}

// The run that says why the case waits for a person: its newest blocked run.
export function blockingRun(c: Pick<TestCase, "runs">): TestCaseRun | undefined {
  return newestRuns(c).find((r) => r.verdict === "blocked");
}

export interface NeedGroup {
  kind: NeedKindEntry;
  cases: TestCase[];
  labels: string[];
  secrets: Array<{ name: string; label: string }>;
  tried: number;
  triedResult: string;
  writesData: number;
}

// The blocked cases grouped by what a person has to bring. A case with several needs falls in the
// hardest one to get (lowest catalog order); one without needs (an older agent) falls in "other".
export function needGroups(
  cases: readonly TestCase[],
  catalog: readonly NeedKindEntry[],
): NeedGroup[] {
  const byKind = new Map(catalog.map((k) => [k.kind, k]));
  const kindOf = (k: string) => byKind.get(k) ?? unknownKind(k);
  const groups = new Map<string, NeedGroup>();
  for (const c of cases) {
    if (c.status !== "blocked") continue;
    const run = blockingRun(c);
    const needs = run?.needs ?? [];
    const main = needs.length
      ? needs.map((n) => kindOf(n.kind)).sort((a, b) => a.order - b.order)[0]
      : kindOf("other");
    const g = groups.get(main.kind) ?? {
      kind: main,
      cases: [],
      labels: [],
      secrets: [],
      tried: 0,
      triedResult: "",
      writesData: 0,
    };
    g.cases.push(c);
    for (const n of needs) {
      const label = n.label.trim();
      if (label && !g.labels.includes(label)) g.labels.push(label);
      for (const s of n.secrets ?? []) {
        if (s.name && !g.secrets.some((x) => x.name === s.name))
          g.secrets.push({ name: s.name, label: s.label || s.name });
      }
    }
    if (run?.tried === "simulated" || run?.tried === "partial") {
      g.tried++;
      if (!g.triedResult && run.tried_result) g.triedResult = run.tried_result;
    }
    if (run?.writes_data) g.writesData++;
    groups.set(main.kind, g);
  }
  return [...groups.values()].sort((a, b) => a.kind.order - b.kind.order);
}

export function groupActions(k: NeedKindEntry): NeedAction[] {
  const known: NeedAction[] = ["access", "by_hand", "skip", "rerun", "review_video"];
  return k.actions.filter((a): a is NeedAction => (known as string[]).includes(a));
}

// ---------- rounds ----------

export function runRound(
  r: Pick<TestCaseRun, "round" | "ran_at">,
  visits: readonly Visit[],
): number {
  return r.round && r.round > 0 ? r.round : roundAt(visits, r.ran_at);
}

export interface RoundCell {
  last: TestCaseRun["verdict"];
  all: Array<TestCaseRun["verdict"]>;
}

export function caseRounds(
  c: Pick<TestCase, "runs">,
  visits: readonly Visit[],
): Map<number, RoundCell> {
  const out = new Map<number, RoundCell>();
  const oldest = newestRuns(c).reverse();
  for (const r of oldest) {
    const n = runRound(r, visits);
    const cell = out.get(n) ?? { last: r.verdict, all: [] };
    cell.last = r.verdict;
    cell.all.push(r.verdict);
    out.set(n, cell);
  }
  return out;
}

export function changedWithin(cell: RoundCell): boolean {
  return new Set(cell.all).size > 1;
}

// The rounds in which Testing ran any case, oldest first.
export function testingRounds(
  cases: ReadonlyArray<Pick<TestCase, "runs">>,
  visits: readonly Visit[],
): number[] {
  const set = new Set<number>();
  for (const c of cases) for (const r of c.runs ?? []) set.add(runRound(r, visits));
  return [...set].sort((a, b) => a - b);
}

// The widest drawer with no round columns: the drawer's narrow layout, the same width as the
// @container drawer rule in task-view.css that drops the columns from the grid, so the two never
// disagree.
export const NO_ROUND_COLUMNS_MAX = 699;

// How many round columns fit in a drawer this wide: none on a phone, 4 in the drawer, 6 in the wide
// view.
export function roundColumns(drawerWidth: number): number {
  if (drawerWidth <= NO_ROUND_COLUMNS_MAX) return 0;
  return drawerWidth < 1100 ? 4 : 6;
}

export function visibleRounds(
  all: readonly number[],
  cols: number,
): { shown: number[]; hidden: number } {
  const shown = cols > 0 ? all.slice(-cols) : [];
  return { shown, hidden: all.length - shown.length };
}

// ---------- groups of the list ----------

export type CaseGroup = "blocked" | "queued" | "fail" | "pass" | "waived" | "untested";
export const CASE_GROUPS: readonly CaseGroup[] = [
  "blocked",
  "queued",
  "fail",
  "pass",
  "waived",
  "untested",
];

export function groupOf(s: TestCaseStatus): CaseGroup {
  return s === "blocked" || s === "queued" || s === "fail" || s === "pass" || s === "waived"
    ? s
    : "untested";
}

export function groupLabel(g: CaseGroup): string {
  switch (g) {
    case "blocked":
      return t("lib.tests_view.groups.blocked");
    case "queued":
      return t("lib.tests_view.groups.queued");
    case "fail":
      return t("lib.tests_view.groups.fail");
    case "pass":
      return t("lib.tests_view.groups.pass");
    case "waived":
      return t("lib.tests_view.groups.waived");
    case "untested":
      return t("lib.tests_view.groups.untested");
  }
}

export function groupCases<C extends Pick<TestCase, "status">>(
  cases: readonly C[],
): Array<{ group: CaseGroup; cases: C[] }> {
  return CASE_GROUPS.map((group) => ({
    group,
    cases: cases.filter((c) => groupOf(c.status) === group),
  })).filter((g) => g.cases.length);
}

// The run whose video and captures the case shows: the newest one that has any.
export function evidenceRun(c: Pick<TestCase, "runs">): TestCaseRun | undefined {
  return newestRuns(c).find((r) => r.video_attachment_id || r.capture_attachment_ids.length);
}

export function stepsOf(c: Pick<TestCase, "steps">): string[] {
  return c.steps
    .split("\n")
    .map((l) => l.replace(/^\s*(?:\d+[.)]|[-*+])\s*/, "").trim())
    .filter(Boolean);
}

// The person who skipped a waived case and why: its newest waived run.
export function waiver(c: Pick<TestCase, "runs">): TestCaseRun | undefined {
  return newestRuns(c).find((r) => r.verdict === "waived");
}

// ---------- forms ----------

export type HandVerdict = "pass" | "fail" | "waived";

export function verdictNeedsNote(v: HandVerdict, note: string): boolean {
  return v !== "pass" && !note.trim();
}

// The daemon keeps observed to one short sentence (280 characters) and the detail in note.
export const OBSERVED_MAX = 280;

// What a person's note becomes in a run: observed alone when it fits, else its first sentence (cut to
// OBSERVED_MAX) as observed and the whole text as note, the way thulu-browser records a long one.
export function personRunFields(text: string): { observed: string; note?: string } {
  const all = text.trim();
  const chars = [...all];
  if (chars.length <= OBSERVED_MAX) return { observed: all };
  const first = /^.+?[.!?](?=\s)/su.exec(all)?.[0] ?? "";
  const head = first && [...first].length <= OBSERVED_MAX ? first : "";
  return { observed: head || `${chars.slice(0, OBSERVED_MAX - 1).join("")}…`, note: all };
}

export interface HandState {
  kind: string;
  title: string;
  icon: string;
  keys: string[];
  at: number;
  ticks: Partial<Record<string, number[]>>;
  results: Partial<Record<string, { v: HandVerdict; note: string }>>;
  // Walking the cases again after every one has a result: the runner shows the case at `at`, with
  // its result, instead of the summary.
  reviewing?: boolean;
}

// The summary shows once every case has a result, unless the person went back to review one.
export function handSummary(h: Pick<HandState, "keys" | "results" | "reviewing">): boolean {
  return handDone(h) && !h.reviewing;
}

export function handDone(h: Pick<HandState, "keys" | "results">): boolean {
  return h.keys.length > 0 && h.keys.every((k) => h.results[k]);
}

export function handTally(h: Pick<HandState, "keys" | "results">): Record<HandVerdict, number> {
  const out = { pass: 0, fail: 0, waived: 0 };
  for (const k of h.keys) {
    const r = h.results[k];
    if (r) out[r.v]++;
  }
  return out;
}

export interface AccessDraft {
  secrets: Record<string, string>;
  environment: string;
}

export type AccessProblem = "secret" | "environment" | null;

// Every secret the agent named needs a value, and with secrets an environment to keep them in.
export function accessProblem(
  names: readonly string[],
  d: AccessDraft,
): { problem: AccessProblem; name?: string } {
  const missing = names.find((n) => !(d.secrets[n] ?? "").trim());
  if (missing) return { problem: "secret", name: missing };
  if (names.length && !d.environment) return { problem: "environment" };
  return { problem: null };
}

export function secretIsHidden(name: string): boolean {
  return /PASS|SECRET|TOKEN|KEY|CODE|PIN/i.test(name);
}

// ---------- HTML ----------

export interface EnvChoice {
  id: string;
  label: string;
  allowed: boolean;
}

export interface TestsViewInput {
  cases: TestCase[];
  criteria: CriterionLite[];
  catalog: NeedKindEntry[];
  visits: readonly Visit[];
  attachments: TvAttachment[];
  environments: EnvChoice[];
  // The environments are still loading: the access form waits instead of saying there is none.
  environmentsLoading?: boolean;
  // The thread of the agent's stop, for "See the stop in Comments"; empty when there is none.
  stopThread: string;
  md: (src: string) => string;
  attachmentURL: (id: string) => string;
  authorName: (author: string) => string;
  // The agent that wrote the brief's acceptance criteria section; empty when unknown.
  criteriaBy?: string;
  // The management bar of a case: status, covered, edit, order, delete (or the read-only facts).
  manageHTML: (c: TestCase, index: number) => string;
  // One run in full (the old run card: verdict, author, links, note, evidence, report).
  runHTML: (r: TestCaseRun) => string;
  // The thumbnails of a run's captures and, when withVideo, its video.
  capturesHTML: (r: TestCaseRun, withVideo: boolean) => string;
}

export interface TestsViewState {
  open: ReadonlySet<string>;
  criterion: string;
  criteriaOpen: boolean;
  // The need form open: "access:<kind>" or "skip:<kind>"; empty when none.
  form: string;
  roundCols: number;
  readOnly: boolean;
  hand: HandState | null;
  stageLabel: string;
  canSendBack: boolean;
  // Add / import / run with an agent, and the notices that go with them.
  toolsHTML: string;
  notesHTML: string;
}

const GROUP_ICON: Record<CaseGroup, string> = {
  pass: "check",
  fail: "x",
  blocked: "user",
  queued: "play",
  waived: "arrow-right",
  untested: "",
};

function stIconHTML(s: string): string {
  const g = groupOf(s as TestCaseStatus);
  const label = statusLabel(s as TestCaseStatus);
  const icon = GROUP_ICON[g];
  return `<span class="tv-st tv-st-${g}" role="img" aria-label="${esc(label)}" title="${esc(label)}">${icon ? icoSvg(icon) : ""}</span>`;
}

function keyChipHTML(c: Pick<TestCase, "key" | "title" | "status">, withStatus = false): string {
  return `<button type="button" class="tv-key-chip" data-tv-open="${esc(c.key)}" title="${esc(c.title)}">${withStatus ? stIconHTML(c.status) : ""}<span>${esc(c.key)}</span></button>`;
}

function linkButtonHTML(label: string, attrs: Record<string, string | boolean>): string {
  return buttonHTML({ label, variant: "link", cls: "tv-link", attrs });
}

function acNumber(key: string): string {
  return key.replace(/^AC-/, "");
}

function newestRunOf(cases: readonly TestCase[]): TestCaseRun | undefined {
  return cases
    .flatMap((c) => c.runs ?? [])
    .sort((a, b) => (a.ran_at < b.ran_at ? 1 : a.ran_at > b.ran_at ? -1 : 0))
    .at(0);
}

export function testsSummaryHTML(input: TestsViewInput, state: TestsViewState): string {
  const cases = input.cases;
  const pass = cases.filter((c) => c.status === "pass").length;
  const acs = input.criteria.map((a) => ({ ...a, state: criterionState(a.key, cases) }));
  const proven = acs.filter((a) => a.state === "ok").length;
  const last = newestRunOf(cases);
  const top = `<div class="tv-sum-top">
      <span class="tv-big">${esc(t("lib.tests_view.summary.pass", { pass, total: cases.length }))}</span>
      ${acs.length ? `<span class="tv-muted">${esc(t("lib.tests_view.summary.proven", { proven, total: acs.length }))}</span>` : ""}
      ${last ? `<span class="tv-faint tv-sum-by">${esc(t("lib.tests_view.summary.last_run", { when: fmtDayClock(last.ran_at), who: input.authorName(last.author) }))}</span>` : ""}
    </div>`;
  if (!acs.length) return `<div class="tv-sum">${top}${barHTML(cases)}</div>`;
  const covering = (key: string) => cases.filter((c) => coversOf(c).includes(key));
  const squares = acs
    .map((a) => {
      const tip = [
        `${a.key} · ${criterionStateLabel(a.state)}`,
        a.text,
        covering(a.key)
          .map((c) => `${c.key}: ${statusLabel(c.status)}`)
          .join(" · "),
      ]
        .filter(Boolean)
        .join("\n");
      return `<button${attrsHTML({
        type: "button",
        class: `tv-ac tv-ac-${a.state}`,
        "aria-pressed": state.criterion === a.key ? "true" : "false",
        "data-tv-ac": a.key,
        title: tip,
        "aria-label": t("lib.tests_view.criteria.filter", {
          key: a.key,
          state: criterionStateLabel(a.state),
        }),
      })}>${esc(acNumber(a.key))}</button>`;
    })
    .join("");
  const rows = acs
    .map((a) => {
      const cs = covering(a.key);
      return `<div class="tv-ac-row"><span class="tv-ac tv-ac-${a.state}" title="${esc(`${a.key} · ${criterionStateLabel(a.state)}`)}">${esc(acNumber(a.key))}</span><span class="tv-ac-t">${esc(a.text)}</span><span class="tv-ac-cases">${
        cs.length
          ? cs.map((c) => keyChipHTML(c, true)).join("")
          : `<span class="tv-faint">${esc(criterionStateLabel("none"))}</span>`
      }</span></div>`;
    })
    .join("");
  const key = [
    ["ok", t("lib.tests_view.criteria.key.ok")],
    ["part", t("lib.tests_view.criteria.key.part")],
    ["wait", t("lib.tests_view.criteria.key.wait")],
  ]
    .map(([s, label]) => `<span class="tv-ac-k tv-ac-k-${s}"></span>${esc(label)}`)
    .join("");
  return `<div class="tv-sum">
      ${top}
      ${barHTML(cases)}
      <div class="tv-acs-row">
        <span class="tv-acs-label" id="tv-acs-label">${esc(t("lib.tests_view.criteria.title"))}</span>
        <div class="tv-acs" role="group" aria-labelledby="tv-acs-label">${squares}</div>
        <span class="tv-acs-key" aria-hidden="true">${key}</span>
      </div>
      ${disclosureHTML({
        variant: "code",
        cls: "tv-ac-list",
        open: state.criteriaOpen,
        attrs: { "data-tv-criteria": true },
        summaryHTML: `${esc(t("lib.tests_view.criteria.read"))}${
          input.criteriaBy
            ? ` <span class="tv-faint">${esc(t("lib.tests_view.criteria.written_by", { who: input.criteriaBy }))}</span>`
            : ""
        }`,
        bodyHTML: `<div class="tv-ac-rows">${rows}</div>`,
      })}
    </div>`;
}

function actionButtonHTML(
  a: NeedAction,
  g: NeedGroup,
  primary: boolean,
  state: TestsViewState,
): string {
  const kind = g.kind.kind;
  const variant = primary ? "primary" : "ghost";
  const open = (form: string) => (state.form === form ? "true" : "false");
  switch (a) {
    case "access":
      return buttonHTML({
        label: t("lib.tests_view.needs.access"),
        icon: "lock",
        size: "sm",
        variant,
        attrs: { "data-tv-form": `access:${kind}`, "aria-expanded": open(`access:${kind}`) },
      });
    case "by_hand":
      return buttonHTML({
        label: primary ? t("lib.tests_view.needs.test_by_hand") : t("lib.tests_view.needs.by_hand"),
        icon: "play",
        size: "sm",
        variant,
        attrs: { "data-tv-hand": kind },
      });
    case "review_video":
      return buttonHTML({
        label: t("lib.tests_view.needs.review_video"),
        icon: "eye",
        size: "sm",
        variant,
        attrs: { "data-tv-open": g.cases[0]?.key ?? "" },
      });
    case "rerun":
      return buttonHTML({
        label: t("lib.tests_view.needs.fixed_rerun"),
        icon: "refresh",
        size: "sm",
        variant,
        attrs: { "data-tv-action": "rerun" },
      });
    case "skip":
      return buttonHTML({
        label: t("lib.tests_view.needs.skip"),
        icon: "arrow-right",
        size: "sm",
        variant: "ghost",
        attrs: { "data-tv-form": `skip:${kind}`, "aria-expanded": open(`skip:${kind}`) },
      });
  }
}

function formFootHTML(cancelForm: string, submit: string, disabled = false): string {
  return `<p class="form-error tv-form-error" data-tv-error role="alert" hidden></p>
      <div class="tv-form-foot">
        ${buttonHTML({ label: t("common.buttons.cancel"), size: "sm", attrs: { "data-tv-form": cancelForm } })}
        ${buttonHTML({ label: submit, size: "sm", variant: "primary", type: "submit", disabled })}
      </div>`;
}

export function accessFormHTML(
  g: NeedGroup,
  input: Pick<TestsViewInput, "environments" | "environmentsLoading">,
): string {
  const keys = g.cases.map((c) => c.key).join(", ");
  const kind = g.kind.kind;
  const fields = g.secrets
    .map((s, i) => {
      const id = `tv-secret-${kind}-${i}`;
      return fieldHTML({
        label: s.label,
        for: id,
        errorId: `${id}-error`,
        hintHTML: `<code>${esc(s.name)}</code>`,
        control: `<input${attrsHTML({
          id,
          type: secretIsHidden(s.name) ? "password" : "text",
          "data-tv-secret": s.name,
          autocomplete: "off",
          spellcheck: "false",
          required: true,
        })}>`,
      });
    })
    .join("");
  const usable = input.environments.filter((e) => e.allowed);
  const loading = !usable.length && !!input.environmentsLoading;
  const envField = !g.secrets.length
    ? ""
    : loading
      ? `<p class="tv-faint" role="status">${esc(t("lib.tests_view.access.environments_loading"))}</p>`
      : usable.length
        ? fieldHTML({
            label: t("lib.tests_view.access.environment"),
            for: `tv-env-${kind}`,
            help: t("lib.tests_view.access.environment_help"),
            control: `<select${attrsHTML({ id: `tv-env-${kind}`, "data-tv-env": true })}>${usable
              .map((e) => `<option value="${esc(e.id)}">${esc(e.label)}</option>`)
              .join("")}</select>`,
          })
        : calloutHTML({
            tone: "warn",
            icon: "warn",
            body: t("lib.tests_view.access.no_environment"),
          });
  const lead = g.secrets.length
    ? t("lib.tests_view.access.lead_secrets", { cases: keys })
    : t("lib.tests_view.access.lead", { cases: keys });
  return `<form class="tv-form" data-tv-access="${esc(kind)}" novalidate>
      <p class="tv-nform-h">${esc(lead)}</p>
      ${fields ? `<div class="tv-secret-grid">${fields}</div>` : ""}
      ${envField}
      ${fieldHTML({
        label: t("lib.tests_view.access.reason"),
        for: `tv-access-why-${kind}`,
        optional: true,
        control: `<textarea${attrsHTML({ id: `tv-access-why-${kind}`, name: "reason", rows: "2" })}></textarea>`,
      })}
      ${g.writesData ? `<p class="tv-faint">${esc(t("lib.tests_view.access.writes_data"))}</p>` : ""}
      ${formFootHTML(`access:${kind}`, t("lib.tests_view.access.submit", { count: g.cases.length }), g.secrets.length > 0 && !usable.length)}
    </form>`;
}

export function skipFormHTML(g: NeedGroup): string {
  const keys = g.cases.map((c) => c.key).join(", ");
  const kind = g.kind.kind;
  return `<form class="tv-form" data-tv-skip="${esc(kind)}" novalidate>
      ${fieldHTML({
        label: t("lib.tests_view.skip.reason", { cases: keys }),
        for: `tv-skip-why-${kind}`,
        errorId: `tv-skip-why-${kind}-error`,
        help: t("lib.tests_view.skip.help"),
        control: `<textarea${attrsHTML({ id: `tv-skip-why-${kind}`, name: "reason", rows: "3", required: true })}></textarea>`,
      })}
      ${formFootHTML(`skip:${kind}`, t("lib.tests_view.skip.submit", { count: g.cases.length }))}
    </form>`;
}

function needFactsHTML(g: NeedGroup): string {
  const tried = g.tried
    ? `<span class="tv-nf tv-nf-ok">${icoSvg("check")}${esc(
        g.triedResult
          ? t("lib.tests_view.needs.tried_result", { result: g.triedResult })
          : t("lib.tests_view.needs.tried", { count: g.tried }),
      )}</span>`
    : "";
  const writes = g.writesData
    ? `<span class="tv-nf tv-nf-warn">${icoSvg("alert")}${esc(
        g.writesData === g.cases.length
          ? t("lib.tests_view.needs.writes_all")
          : t("lib.tests_view.needs.writes_some", { count: g.writesData }),
      )}</span>`
    : "";
  return tried || writes ? `<div class="tv-need-facts">${tried}${writes}</div>` : "";
}

function needRowHTML(g: NeedGroup, input: TestsViewInput, state: TestsViewState): string {
  const acts = groupActions(g.kind);
  const primary = acts.find((a) => a !== "skip");
  const kind = g.kind.kind;
  const labels = g.labels.length
    ? `<p class="tv-need-labels">${esc(g.labels.join(" · "))}</p>`
    : "";
  const buttons = state.readOnly
    ? ""
    : `<div class="tv-need-l2"><div class="tv-need-acts">${acts.map((a) => actionButtonHTML(a, g, a === primary, state)).join("")}</div></div>`;
  let form = "";
  if (!state.readOnly && state.form === `access:${kind}`) form = accessFormHTML(g, input);
  else if (!state.readOnly && state.form === `skip:${kind}`) form = skipFormHTML(g);
  return `<div class="tv-need-row" data-kind="${esc(kind)}">
      <span class="tv-need-ic">${icoSvg(needIcon(g.kind.icon))}</span>
      <div class="tv-need-l1"><b>${esc(needTitle(g.kind))}</b><span class="tv-need-cases">${g.cases.map((c) => keyChipHTML(c)).join("")}</span></div>
      ${labels}
      ${needFactsHTML(g)}
      ${buttons}
      ${form}
    </div>`;
}

export function needsBlockHTML(input: TestsViewInput, state: TestsViewState): string {
  if (state.hand) return handRunnerHTML(state.hand, input);
  const groups = needGroups(input.cases, input.catalog);
  const queued = input.cases.filter((c) => c.status === "queued").length;
  if (!groups.length) {
    if (!queued) return "";
    return `<div class="tv-needs-quiet" id="tv-needs" role="status">${icoSvg("play")}<span>${esc(
      t("lib.tests_view.needs.running_again", { count: queued }),
    )}</span></div>`;
  }
  const blocked = groups.reduce((n, g) => n + g.cases.length, 0);
  const sub = queued
    ? t("lib.tests_view.needs.sub_queued", { count: blocked, queued })
    : t("lib.tests_view.needs.sub", { count: blocked });
  const links = state.readOnly
    ? []
    : [
        state.canSendBack
          ? linkButtonHTML(t("lib.tests_view.needs.send_back"), { "data-tv-action": "send_back" })
          : "",
        linkButtonHTML(t("lib.tests_view.needs.rerun", { stage: state.stageLabel }), {
          "data-tv-action": "rerun",
        }),
      ];
  if (input.stopThread) {
    links.push(
      linkButtonHTML(t("lib.tests_view.needs.see_stop"), { "data-tv-stop": input.stopThread }),
    );
  }
  const shown = links.filter(Boolean);
  const alt = shown.length
    ? `<p class="tv-alt">${esc(t("lib.tests_view.needs.code_wrong"))} ${shown.join(`<span class="tv-faint" aria-hidden="true">·</span>`)}</p>`
    : "";
  return `<section class="tv-needs" id="tv-needs" aria-labelledby="tv-needs-h">
      ${sectionHeadHTML({ title: t("lib.tests_view.needs.title"), id: "tv-needs-h", level: 4, asideHTML: `<span class="tv-sec-r">${esc(sub)}</span>` })}
      <div class="tv-need-rows">${groups.map((g) => needRowHTML(g, input, state)).join("")}</div>
      ${alt}
    </section>`;
}

function passIfHTML(c: TestCase, input: Pick<TestsViewInput, "md">): string {
  if (!c.expected.trim()) return "";
  return `<div class="tv-passif"><h5 class="tv-h">${icoSvg("check", { size: "sm" })}${esc(t("lib.tests_view.detail.pass_if"))}</h5>${mdHTML(input.md(c.expected), { compact: true })}</div>`;
}

function verdictBarHTML(
  key: string,
  note: string,
  mode: "hand" | "case",
  chosen?: HandVerdict,
): string {
  // In the runner the result already given stays pressed, so walking back shows it.
  const btn = (v: HandVerdict, icon: string, label: string) =>
    buttonHTML({
      label,
      icon,
      size: "sm",
      variant: "default",
      attrs: {
        "data-tv-verdict": v,
        "data-key": key,
        "data-mode": mode,
        ...(mode === "hand" ? { "aria-pressed": String(v === chosen) } : {}),
      },
    });
  return `<div class="tv-verdicts">
      ${btn("pass", "check", t("lib.tests_view.verdict.pass"))}
      ${btn("fail", "x", t("lib.tests_view.verdict.fail"))}
      ${btn("waived", "arrow-right", t("lib.tests_view.verdict.skip"))}
      <input${attrsHTML({
        type: "text",
        class: "tv-note",
        value: note,
        "data-tv-note": key,
        "data-mode": mode,
        placeholder: t("lib.tests_view.verdict.note"),
        "aria-label": t("lib.tests_view.verdict.note_label", { key }),
      })}>
      <p class="form-error tv-form-error" data-tv-note-error="${esc(key)}" role="alert" hidden></p>
    </div>`;
}

export function handRunnerHTML(h: HandState, input: Pick<TestsViewInput, "cases" | "md">): string {
  const byKey = new Map(input.cases.map((c) => [c.key, c]));
  const done = handSummary(h);
  const dots = h.keys
    .map((k, i) => {
      const r = h.results[k];
      const cls = ["tv-hdot", r && `tv-hdot-${r.v}`, i === h.at && !done && "tv-hdot-cur"]
        .filter(Boolean)
        .join(" ");
      return `<button type="button" class="${cls}" data-tv-hgo="${i}" aria-label="${esc(r ? `${k} · ${statusLabel(r.v)}` : k)}">${esc(k)}</button>`;
    })
    .join("");
  const head = `<div class="tv-hand-h"><span class="tv-need-ic">${icoSvg(needIcon(h.icon))}</span><b>${esc(t("lib.tests_view.hand.title"))}</b><span class="tv-muted">${esc(h.title)}</span><div class="tv-hdots">${dots}</div>${buttonHTML({ iconOnly: true, icon: "x", label: t("lib.tests_view.hand.close"), size: "sm", variant: "ghost", attrs: { "data-tv-hand-close": true } })}</div>`;
  const wrap = (body: string, cls = "tv-hand-body") =>
    `<section class="tv-hand" id="tv-needs" aria-label="${esc(t("lib.tests_view.hand.title"))}">${head}<div class="${cls}">${body}</div></section>`;
  if (done) {
    const n = handTally(h);
    const parts = [
      n.pass ? t("lib.tests_view.hand.n_pass", { count: n.pass }) : "",
      n.fail ? t("lib.tests_view.hand.n_fail", { count: n.fail }) : "",
      n.waived ? t("lib.tests_view.hand.n_skipped", { count: n.waived }) : "",
    ].filter(Boolean);
    return wrap(
      `<p>${esc(t("lib.tests_view.hand.done", { tally: parts.join(", ") }))}</p>
        <p class="form-error tv-form-error" data-tv-error role="alert" hidden></p>
        <div class="tv-hand-done-row">
          ${buttonHTML({ label: t("lib.tests_view.hand.save"), size: "sm", variant: "primary", attrs: { "data-tv-hand-save": true } })}
          ${buttonHTML({ label: t("lib.tests_view.hand.review"), size: "sm", attrs: { "data-tv-hgo": "0" } })}
        </div>`,
      "tv-hand-done",
    );
  }
  const c = byKey.get(h.keys[h.at]);
  if (!c) return "";
  const ticks = new Set(h.ticks[c.key] ?? []);
  const steps = stepsOf(c);
  const mine = h.results[c.key];
  const run = blockingRun(c);
  const checks = steps.length
    ? `<ol class="tv-checks">${steps
        .map((s, i) => {
          const on = ticks.has(i);
          return `<li><button type="button" class="tv-check${on ? " tv-check-on" : ""}" data-tv-tick="${i}" aria-pressed="${on}"><span class="tv-box">${on ? icoSvg("check", { size: "sm" }) : ""}</span><span>${esc(s)}</span></button></li>`;
        })
        .join("")}</ol>`
    : c.description
      ? mdHTML(input.md(c.description), { compact: true })
      : "";
  return wrap(`<div class="tv-hand-title"><span class="tv-key">${esc(c.key)}</span> ${esc(c.title)} <span class="tv-faint">· ${esc(t("lib.tests_view.hand.case_n", { n: h.at + 1, total: h.keys.length }))}</span></div>
      ${c.preconditions ? `<p class="tv-pre">${esc(c.preconditions)}</p>` : ""}
      <div class="tv-hand-grid">${checks}${passIfHTML(c, input)}</div>
      ${run?.tried_result ? `<p class="tv-hand-tried">${esc(t("lib.tests_view.needs.tried_result", { result: run.tried_result }))}</p>` : ""}
      ${verdictBarHTML(c.key, mine?.note ?? "", "hand", mine?.v)}
      <div class="tv-hand-nav">
        ${buttonHTML({ label: t("lib.tests_view.hand.previous"), variant: "link", disabled: h.at === 0, attrs: { "data-tv-hgo": String(h.at - 1) } })}
        ${handDone(h) ? buttonHTML({ label: t("lib.tests_view.hand.summary"), variant: "link", attrs: { "data-tv-hand-summary": true } }) : ""}
        ${buttonHTML({ label: t("lib.tests_view.hand.next"), variant: "link", disabled: h.at >= h.keys.length - 1, attrs: { "data-tv-hgo": String(h.at + 1) } })}
      </div>`);
}

function cellHTML(round: number, cell: RoundCell | undefined): string {
  const v = cell?.last ?? "";
  const mixed = !!cell && changedWithin(cell);
  const tip = cell
    ? t("lib.tests_view.list.cell", {
        round,
        verdicts: cell.all.map((x) => statusLabel(x)).join(" → "),
      })
    : t("lib.tests_view.list.cell_none", { round });
  return `<span class="tv-rc"><i class="tv-cell tv-cell-${v || "none"}${mixed ? " tv-cell-mixed" : ""}" role="img" aria-label="${esc(tip)}" title="${esc(tip)}">${v === "fail" ? icoSvg("x") : ""}</i></span>`;
}

function caseRowHTML(
  c: TestCase,
  index: number,
  input: TestsViewInput,
  state: TestsViewState,
  rounds: number[],
): string {
  const open = state.open.has(c.key);
  const cells = caseRounds(c, input.visits);
  const ev = evidenceRun(c);
  const covers = coversOf(c)
    .map((k) => `<span class="tv-acmini" title="${esc(k)}">${esc(acNumber(k))}</span>`)
    .join("");
  const video = ev?.video_attachment_id
    ? `<span class="tv-vid" title="${esc(t("lib.tests_view.list.has_video"))}">${icoSvg("camera")}</span>`
    : "";
  return `<div class="tc-case tv-case${open ? " tv-case-open" : ""}" id="tc-${esc(c.key)}" data-key="${esc(c.key)}">
      <div class="tv-row" role="button" tabindex="0" data-tv-toggle="${esc(c.key)}" aria-expanded="${open}" aria-controls="tv-detail-${esc(c.key)}">
        ${stIconHTML(c.status)}<span class="tv-key">${esc(c.key)}</span>
        <span class="tv-title"><span class="tv-name">${esc(c.title)}</span>${c.priority === "critical" ? casePriorityTagHTML(c.priority, { pill: true, cls: "tv-crit" }) : ""}${video}</span>
        <span class="tv-cov">${covers}</span>
        ${rounds.map((r) => cellHTML(r, cells.get(r))).join("")}
        <span class="tv-chev">${icoSvg("chevron-right")}</span>
      </div>
      ${open ? caseDetailHTML(c, index, input, state) : ""}
    </div>`;
}

function blockedFactsHTML(c: TestCase, catalog: readonly NeedKindEntry[]): string {
  const run = c.status === "blocked" ? blockingRun(c) : undefined;
  if (!run) return "";
  const kinds = new Map(catalog.map((k) => [k.kind, k]));
  const needs = (run.needs ?? []).map((n) => {
    const k = kinds.get(n.kind);
    return `<span class="tv-fact">${icoSvg(needIcon(k?.icon))}${esc(n.label || (k ? needTitle(k) : n.kind))}</span>`;
  });
  let tried = "";
  if (run.tried === "simulated" || run.tried === "partial") {
    const key =
      run.tried === "simulated"
        ? "lib.tests_view.detail.tried_simulated"
        : "lib.tests_view.detail.tried_partial";
    tried = `<span class="tv-fact tv-fact-ok">${icoSvg("check")}${esc(t(key))}</span>`;
  } else if (run.tried === "none") {
    tried = `<span class="tv-fact tv-fact-no">${icoSvg("x")}${esc(t("lib.tests_view.detail.tried_none"))}</span>`;
  }
  const writes = run.writes_data
    ? `<span class="tv-fact tv-fact-warn">${icoSvg("alert")}${esc(t("lib.tests_view.detail.writes_data"))}</span>`
    : "";
  const all = [...needs, tried, writes].join("");
  return all ? `<div class="tv-facts">${all}</div>` : "";
}

function isPlayable(a: TvAttachment | undefined): a is TvAttachment {
  return !!a && (/^video\//.test(a.mime_type ?? "") || /\.(mp4|webm|mov)$/i.test(a.filename));
}

function videoHTML(r: TestCaseRun, input: TestsViewInput): string {
  const v = input.attachments.find((a) => a.id === r.video_attachment_id);
  if (!r.video_attachment_id || !isPlayable(v)) return "";
  const vtt =
    r.vtt_attachment_id && input.attachments.some((a) => a.id === r.vtt_attachment_id)
      ? `<track kind="subtitles" default src="${esc(input.attachmentURL(r.vtt_attachment_id))}" label="${esc(t("lib.tests_view.detail.subtitles"))}">`
      : "";
  return `<video class="tv-video" controls preload="metadata" playsinline src="${esc(input.attachmentURL(v.id))}">${vtt}</video>`;
}

export function caseDetailHTML(
  c: TestCase,
  index: number,
  input: TestsViewInput,
  state: TestsViewState,
): string {
  const runs = newestRuns(c);
  const last = runs.at(0);
  const steps = stepsOf(c);
  const ev = evidenceRun(c);
  const saw = last
    ? `<p class="tv-saw">${esc(last.observed || last.note || t("lib.tests_view.detail.no_observed"))}</p>${
        last.observed && last.note
          ? disclosureHTML({
              variant: "code",
              cls: "tv-full-note",
              summary: t("lib.tests_view.detail.full_note"),
              bodyHTML: mdHTML(input.md(last.note), { compact: true }),
            })
          : ""
      }`
    : `<p class="tv-muted">${esc(t("lib.tests_view.detail.not_run"))}</p>`;
  let evidence = "";
  if (ev) {
    const video = videoHTML(ev, input);
    evidence = `<h5 class="tv-h tv-h-gap">${esc(t("lib.tests_view.detail.evidence"))} <span class="tv-h-sub">· ${esc(
      t("lib.tests_view.detail.evidence_of", {
        round: runRound(ev, input.visits),
        verdict: statusLabel(ev.verdict).toLowerCase(),
      }),
    )}</span></h5>${video}${input.capturesHTML(ev, !video)}`;
  }
  const history = runs.length
    ? `<h5 class="tv-h tv-h-gap">${esc(t("lib.tests_view.detail.history"))}</h5>
       <ol class="tv-tl">${[...runs]
         .reverse()
         .map((r) => {
           const tip = `${statusLabel(r.verdict)} · ${input.authorName(r.author)}${r.observed ? `\n${r.observed}` : ""}`;
           return `<li class="tv-tl-i" title="${esc(tip)}">${stIconHTML(r.verdict)}<small>${esc(t("lib.tests_view.list.round_short", { n: runRound(r, input.visits) }))}</small></li>`;
         })
         .join(`<li class="tv-tl-l" aria-hidden="true"></li>`)}</ol>
       ${disclosureHTML({
         variant: "code",
         cls: "tv-runs",
         summary: t("lib.tests_view.detail.all_runs", { count: runs.length }),
         bodyHTML: runs.map((r) => input.runHTML(r)).join(""),
       })}`
    : "";
  const w = c.status === "waived" ? waiver(c) : undefined;
  const skipped = w
    ? calloutHTML({
        tone: "info",
        icon: "arrow-right",
        cls: "tv-wide",
        title: t("lib.tests_view.detail.skipped_by", { who: input.authorName(w.author) }),
        body: w.note || t("lib.tests_view.detail.no_reason"),
      })
    : "";
  const verdicts = state.readOnly || c.status === "waived" ? "" : verdictBarHTML(c.key, "", "case");
  const how =
    c.preconditions || steps.length || c.expected || c.description
      ? `${c.preconditions ? `<p class="tv-pre">${esc(c.preconditions)}</p>` : ""}
          ${steps.length ? `<ol class="tv-steps">${steps.map((s) => `<li>${esc(s)}</li>`).join("")}</ol>` : ""}
          ${c.description && !c.preconditions ? mdHTML(input.md(c.description), { compact: true, cls: "tv-desc" }) : ""}
          ${passIfHTML(c, input)}`
      : `<p class="tv-muted">${esc(t("lib.tests_view.detail.no_steps"))}</p>`;
  return `<div class="tv-detail" id="tv-detail-${esc(c.key)}">
      ${skipped}
      <div class="tv-col">
        <h5 class="tv-h">${esc(t("lib.tests_view.detail.how"))}</h5>
        ${how}
      </div>
      <div class="tv-col">
        <h5 class="tv-h">${esc(t("lib.tests_view.detail.saw"))}</h5>
        ${blockedFactsHTML(c, input.catalog)}
        ${saw}
        ${evidence}
        ${history}
      </div>
      ${verdicts}
      <div class="tc-edit-slot tv-wide"></div>
      <div class="tv-wide">${input.manageHTML(c, index)}</div>
    </div>`;
}

function legendHTML(count: number): string {
  const sw = (cls: string, label: string, inner = "") =>
    `<span><i class="tv-cell ${cls}" aria-hidden="true">${inner}</i>${esc(label)}</span>`;
  return `<div class="tv-legend">
      ${sw("tv-cell-pass", t("lib.tests_view.legend.pass"))}
      ${sw("tv-cell-fail", t("lib.tests_view.legend.fail"), icoSvg("x"))}
      ${sw("tv-cell-blocked", t("lib.tests_view.legend.blocked"))}
      ${sw("tv-cell-none", t("lib.tests_view.legend.none"))}
      ${sw("tv-cell-pass tv-cell-mixed", t("lib.tests_view.legend.mixed"))}
      <span class="tv-faint">${esc(t("lib.tests_view.legend.columns", { count }))}</span>
    </div>`;
}

export function caseListHTML(input: TestsViewInput, state: TestsViewState): string {
  const all = input.cases;
  const filter = state.criterion
    ? input.criteria.find((a) => a.key === state.criterion)
    : undefined;
  const shown = filter ? all.filter((c) => coversOf(c).includes(filter.key)) : all;
  const { shown: rounds, hidden } = visibleRounds(
    testingRounds(all, input.visits),
    state.roundCols,
  );
  const roundHead = rounds
    .map((r, i) => {
      const more =
        i === 0 && hidden
          ? `<b class="tv-more-r" title="${esc(t("lib.tests_view.list.more_rounds", { count: hidden }))}">${esc(t("lib.tests_view.list.more", { count: hidden }))}</b>`
          : "";
      return `<span class="tv-rc">${more}${esc(t("lib.tests_view.list.round_short", { n: r }))}</span>`;
    })
    .join("");
  const head = `<div class="tv-thead" aria-hidden="true"><span></span><span>${esc(t("lib.tests_view.list.case"))}</span><span>${esc(t("lib.tests_view.list.title"))}</span><span>${esc(t("lib.tests_view.list.covers"))}</span>${roundHead}<span></span></div>`;
  const groups = groupCases(shown)
    .map(
      (g) =>
        `<div class="tv-group">${esc(t("lib.tests_view.list.group", { group: groupLabel(g.group), count: g.cases.length }))}</div>${g.cases
          .map((c) => caseRowHTML(c, all.indexOf(c), input, state, rounds))
          .join("")}`,
    )
    .join("");
  const aside = filter
    ? `<span class="tv-sec-r tv-filter" role="status">${esc(t("lib.tests_view.list.covering", { key: filter.key }))} · ${linkButtonHTML(t("lib.tests_view.list.show_all"), { "data-tv-ac": filter.key })}</span>`
    : "";
  const filterText = filter
    ? `<p class="tv-ac-text"><b>${esc(filter.key)}</b> ${esc(filter.text)}</p>`
    : "";
  const body = all.length
    ? `<div class="tv-list" data-rounds="${rounds.length}" style="--tv-rn:${rounds.length}">${head}${groups}</div>${rounds.length ? legendHTML(rounds.length) : ""}`
    : emptyStateHTML({ size: "inline", dashed: true, body: t("lib.tests_view.list.empty") });
  return `<section class="tv-cases" aria-labelledby="tv-cases-h">
      ${sectionHeadHTML({ title: t("lib.tests_view.list.heading"), id: "tv-cases-h", level: 4, count: all.length || undefined, asideHTML: aside, actionsHTML: state.toolsHTML })}
      ${state.notesHTML}
      <div class="tc-add-slot"></div>
      ${filterText}
      ${body}
    </section>`;
}

// The whole tab: summary, what a person must bring, and the list.
export function testsViewHTML(input: TestsViewInput, state: TestsViewState): string {
  const summary = input.cases.length ? testsSummaryHTML(input, state) : "";
  return `${summary}${needsBlockHTML(input, state)}${caseListHTML(input, state)}`;
}

// The tab's count: the cases that wait for a person, else every case.
export function testsTabCount(cases: ReadonlyArray<Pick<TestCase, "status">>): {
  n: number;
  warn: boolean;
} {
  const blocked = cases.filter((c) => c.status === "blocked").length;
  return blocked ? { n: blocked, warn: true } : { n: cases.length, warn: false };
}

// The tab's accessible name, with what its count means.
export function testsTabName(c: { n: number; warn: boolean }): string {
  if (!c.n) return t("lib.tests_view.tab.plain");
  return c.warn
    ? t("lib.tests_view.tab.blocked", { count: c.n })
    : t("lib.tests_view.tab.cases", { count: c.n });
}

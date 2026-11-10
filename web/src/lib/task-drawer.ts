import { t } from "../i18n";
import { repoChipHTML } from "./chips";
import { shortId } from "./task-card";
import { badgeHTML, buttonHTML, esc, icoSvg, listRowHTML, propEmptyHTML } from "./ui";

export function emptyValueHTML(text: string): string {
  return propEmptyHTML(text);
}

export interface Dep {
  task_id: string;
  title?: string;
  kind: string;
  stage: string;
}

type DepLook = "unmet" | "met" | "soft" | "plain";

const DEP_ICON: Record<DepLook, string> = {
  unmet: "i-lock",
  met: "i-check",
  soft: "i-link",
  plain: "i-arrow-right",
};

function depRowHTML(d: Dep, look: DepLook): string {
  const word =
    look === "unmet"
      ? badgeHTML(t("lib.task_drawer.deps.blocking"), "warn", { size: "sm" })
      : look === "soft"
        ? badgeHTML(t("lib.task_drawer.deps.soft"), "muted", { size: "sm", edge: "dashed" })
        : "";
  return listRowHTML({
    size: "sm",
    wrap: true,
    cls: `dep dep-${look}`,
    attrs: { "data-dep-id": d.task_id },
    leadHTML: `<span class="dep-ico">${icoSvg(DEP_ICON[look])}</span>`,
    title: (d.title ?? "") || shortId(d.task_id),
    badgesHTML: word || undefined,
  });
}

function depListHTML(rows: string[]): string {
  if (rows.length === 0) return emptyValueHTML(t("lib.task_drawer.deps.none"));
  return `<div class="row-list dep-list">${rows.join("")}</div>`;
}

export function blockedByHTML(
  deps: Dep[] | undefined,
  isTerminal: (stageID: string) => boolean,
): string {
  const list = deps ?? [];
  const hard = list.filter((d) => d.kind === "hard");
  const soft = list.filter((d) => d.kind !== "hard");
  return depListHTML([
    ...hard.map((d) => depRowHTML(d, isTerminal(d.stage) ? "met" : "unmet")),
    ...soft.map((d) => depRowHTML(d, "soft")),
  ]);
}

export function blocksHTML(deps: Dep[] | undefined): string {
  return depListHTML((deps ?? []).map((d) => depRowHTML(d, d.kind === "hard" ? "plain" : "soft")));
}

export interface BranchInput {
  branch: string;
  prCount: number;
  shared: boolean;
  pr: boolean;
  merge: boolean;
}

export function branchHTML(b: BranchInput): string {
  const branch = b.branch
    ? `<span class="branch-line"><code class="branch-name" title="${esc(b.branch)}">${esc(b.branch)}</code>${buttonHTML({ iconOnly: true, icon: "copy", label: t("lib.task_drawer.branch.copy"), size: "sm", variant: "ghost", cls: "copy-btn", attrs: { "data-copy": b.branch } })}</span>`
    : emptyValueHTML(t("lib.task_drawer.branch.none"));
  const shared = b.shared
    ? `<span class="shared-tag">${icoSvg("i-corner-up")}${esc(t("lib.task_drawer.branch.shared"))}</span><span class="branch-note">${esc(t("lib.task_drawer.branch.shared_note"))}</span>`
    : "";
  const force =
    !b.shared && b.branch && b.pr && b.prCount === 0
      ? buttonHTML({
          label: t("lib.task_drawer.branch.force_pr"),
          icon: "github",
          size: "sm",
          attrs: { "data-force-pr": true },
        })
      : "";
  const merge =
    !b.shared && b.branch && b.merge
      ? buttonHTML({
          label: t("lib.task_drawer.branch.merge"),
          icon: "branch",
          size: "sm",
          attrs: { "data-merge": true },
        })
      : "";
  return `${branch}${shared}${force}${merge}`;
}

export function projectChipsHTML(ids: string[], name: (id: string) => string): string {
  if (ids.length === 0) return emptyValueHTML(t("lib.task_drawer.projects.none"));
  return ids
    .map((id) => repoChipHTML(name(id), { href: `/project/?id=${encodeURIComponent(id)}` }))
    .join("");
}

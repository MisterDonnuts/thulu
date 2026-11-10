import { t } from "../i18n";
import { esc } from "./ui";

export interface RequiredApproval {
  key: string;
  label: string;
  task_types?: string[];
  reset_on_change?: boolean;
}

export const ON_CHANGES = ["resume", "kick_back", "stay"] as const;
export type OnChangesRequested = (typeof ON_CHANGES)[number];

export interface StageApprovals {
  required_approvals?: RequiredApproval[] | null;
  on_changes_requested?: string;
  changes_kick_back_to?: string;
}

export interface Named {
  id: string;
  label: string;
}

export interface ApprovalsPatch {
  required_approvals: RequiredApproval[];
  on_changes_requested: OnChangesRequested;
  changes_kick_back_to: string;
}

const KEY = /^[a-z0-9_]{1,32}$/;

export function approvalKeyFromLabel(label: string): string {
  return label
    .normalize("NFD")
    .replace(/[̀-ͯ]/g, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "_")
    .replace(/^_+|_+$/g, "")
    .slice(0, 32);
}

export function onChangesOf(st: StageApprovals): OnChangesRequested {
  const v = st.on_changes_requested ?? "";
  return (ON_CHANGES as readonly string[]).includes(v) ? (v as OnChangesRequested) : "resume";
}

// Keys must be unique slugs: the daemon matches approval instances to definitions by key.
export function normalizeRequiredApprovals(rows: RequiredApproval[]): {
  rows: RequiredApproval[];
  problem: string | null;
} {
  const out: RequiredApproval[] = [];
  const seen = new Set<string>();
  for (const r of rows) {
    const label = r.label.trim();
    const rawKey = r.key.trim();
    if (!label && !rawKey) continue;
    if (!label)
      return { rows: out, problem: t("lib.required_approvals.errors.label", { key: rawKey }) };
    const key = rawKey || approvalKeyFromLabel(label);
    if (!KEY.test(key))
      return { rows: out, problem: t("lib.required_approvals.errors.key", { key }) };
    if (seen.has(key))
      return { rows: out, problem: t("lib.required_approvals.errors.duplicate", { key }) };
    seen.add(key);
    out.push({
      key,
      label,
      task_types: [...new Set(r.task_types ?? [])].sort(),
      reset_on_change: r.reset_on_change !== false,
    });
  }
  return { rows: out, problem: null };
}

const ON_CHANGES_KEY: Record<OnChangesRequested, string> = {
  resume: "lib.required_approvals.on_changes.resume",
  kick_back: "lib.required_approvals.on_changes.kick_back",
  stay: "lib.required_approvals.on_changes.stay",
};

export function approvalsSummary(st: StageApprovals): string {
  return t("lib.required_approvals.summary", { count: (st.required_approvals ?? []).length });
}

// Type ids the board no longer has stay listed, so saving does not silently widen the rule to every
// task.
export function approvalRowHTML(r: RequiredApproval, types: Named[]): string {
  const picked = new Set(r.task_types ?? []);
  const known = new Set(types.map((tt) => tt.id));
  const all = [
    ...types,
    ...[...picked].filter((id) => !known.has(id)).map((id) => ({ id, label: id })),
  ];
  const typeBoxes = all
    .map(
      (tt) => `<label class="st-flag">
          <input type="checkbox" data-ra-type="${esc(tt.id)}"${picked.has(tt.id) ? " checked" : ""} />
          ${esc(tt.label)}
        </label>`,
    )
    .join("");
  return `<div class="ra-row" data-ra-row>
      <div class="ra-row-main">
        <input type="text" class="ra-label" data-ra="label" value="${esc(r.label)}"
               placeholder="${esc(t("lib.required_approvals.label_placeholder"))}" />
        <input type="text" class="ra-key" data-ra="key" value="${esc(r.key)}"
               placeholder="${esc(t("lib.required_approvals.key_placeholder"))}"
               title="${esc(t("lib.required_approvals.key_title"))}" />
        <label class="st-flag" title="${esc(t("lib.required_approvals.reset_title"))}">
          <input type="checkbox" data-ra="reset"${r.reset_on_change !== false ? " checked" : ""} />
          ${esc(t("lib.required_approvals.reset"))}
        </label>
        <span class="st-spacer"></span>
        <button type="button" class="btn btn-ghost btn-sm" data-ra-remove>${esc(t("common.buttons.remove"))}</button>
      </div>
      ${
        all.length
          ? `<div class="ra-types" title="${esc(t("lib.required_approvals.types_title"))}">
              <span class="ra-types-k">${esc(t("lib.required_approvals.types"))}</span>${typeBoxes}
            </div>`
          : ""
      }
    </div>`;
}

export function approvalRowEl(r: RequiredApproval, types: Named[]): HTMLElement {
  const tpl = document.createElement("template");
  tpl.innerHTML = approvalRowHTML(r, types);
  return tpl.content.firstElementChild as HTMLElement;
}

export function approvalsEditorHTML(st: StageApprovals, types: Named[], earlier: Named[]): string {
  const rows = (st.required_approvals ?? []).map((r) => approvalRowHTML(r, types)).join("");
  const mode = onChangesOf(st);
  const modeOpts = ON_CHANGES.map(
    (m) =>
      `<option value="${m}"${m === mode ? " selected" : ""}>${esc(t(ON_CHANGES_KEY[m]))}</option>`,
  ).join("");
  const target = st.changes_kick_back_to ?? "";
  const targetOpts =
    `<option value="">—</option>` +
    earlier
      .map(
        (s) =>
          `<option value="${esc(s.id)}"${s.id === target ? " selected" : ""}>${esc(s.label)}</option>`,
      )
      .join("");
  return `<p class="ra-intro">${esc(t("lib.required_approvals.intro"))}</p>
    <div class="ra-rows" data-ra-rows>${rows}</div>
    <button type="button" class="btn btn-ghost btn-sm" data-ra-add>${esc(t("lib.required_approvals.add"))}</button>
    <div class="ra-changes">
      <label class="st-flag" title="${esc(t("lib.required_approvals.on_changes_title"))}">
        ${esc(t("lib.required_approvals.on_changes_label"))}
        <select data-ra="on_changes">${modeOpts}</select>
      </label>
      <label class="st-flag" data-ra-kick${mode === "kick_back" ? "" : " hidden"}>
        ${esc(t("lib.required_approvals.kick_back_to"))}
        <select data-ra="kick_back_to">${targetOpts}</select>
      </label>
    </div>`;
}

export function readApprovalsEditor(root: ParentNode): {
  rows: RequiredApproval[];
  on_changes_requested: OnChangesRequested;
  changes_kick_back_to: string;
} {
  const rows = [...root.querySelectorAll<HTMLElement>("[data-ra-row]")].map((row) => ({
    key: row.querySelector<HTMLInputElement>('[data-ra="key"]')?.value ?? "",
    label: row.querySelector<HTMLInputElement>('[data-ra="label"]')?.value ?? "",
    reset_on_change: row.querySelector<HTMLInputElement>('[data-ra="reset"]')?.checked ?? true,
    task_types: [...row.querySelectorAll<HTMLInputElement>("[data-ra-type]")]
      .filter((cb) => cb.checked)
      .map((cb) => cb.dataset.raType ?? ""),
  }));
  const mode = root.querySelector<HTMLSelectElement>('[data-ra="on_changes"]')?.value ?? "";
  const on_changes_requested = (ON_CHANGES as readonly string[]).includes(mode)
    ? (mode as OnChangesRequested)
    : "resume";
  const kick = root.querySelector<HTMLSelectElement>('[data-ra="kick_back_to"]')?.value ?? "";
  return {
    rows,
    on_changes_requested,
    changes_kick_back_to: on_changes_requested === "kick_back" ? kick : "",
  };
}

export function approvalsPatch(root: ParentNode): ApprovalsPatch | { problem: string } {
  const read = readApprovalsEditor(root);
  const { rows, problem } = normalizeRequiredApprovals(read.rows);
  if (problem) return { problem };
  return {
    required_approvals: rows,
    on_changes_requested: read.on_changes_requested,
    changes_kick_back_to: read.changes_kick_back_to,
  };
}

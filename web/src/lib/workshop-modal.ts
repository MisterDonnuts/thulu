import { api, ApiError } from "./api";
import { bindModal } from "./modal";
import {
  $,
  bindDropOverlay,
  bindItemList,
  bindTabs,
  buttonHTML,
  calloutHTML,
  catalogItemHTML,
  checkHTML,
  codeBlockHTML,
  disclosureHTML,
  emptyRowHTML,
  emptyStateHTML,
  entityHeadHTML,
  esc,
  factsHTML,
  fieldHTML,
  filterChipsHTML,
  icoSvg,
  segmentedHTML,
  setChipsPressed,
  setCount,
} from "./ui";
import { badgeHTML } from "./badges";
import { t } from "../i18n";
import type { Stage } from "./stages";
import {
  applyVerb,
  applyWorkshop,
  badgeOf,
  BundleError,
  choicesOf,
  copyActionText,
  filterEntries,
  getWorkshopItem,
  itemKey,
  listWorkshop,
  planWorkshop,
  previewBody,
  readBundleFile,
  resultIsEmpty,
  statusTone,
  tagLabel,
  tagsOf,
  withTypedNames,
  writes,
  writesOf,
  type Action,
  type ApplyResult,
  type ItemRef,
  type Plan,
  type PlanInput,
  type PlanItem,
  type PlanNotice,
  type Resolution,
  type WorkshopEntry,
  type WorkshopFile,
  type WorkshopKind,
  type WorkshopStatus,
} from "./workshop";

export interface WorkshopModalOpts {
  // Read at each use: the session arrives after the page mounts the modal.
  canWrite: () => boolean;
  onApplied: (res: ApplyResult) => void;
  toast: (msg: string, kind?: "ok" | "err" | "info") => void;
}

export interface WorkshopModal {
  open: (kind: WorkshopKind) => void;
  isOpen: () => boolean;
  close: () => void;
}

// Labels spelled out key by key so the i18n catalog guard sees each key named.
const STATUS_LABEL: Record<WorkshopStatus, () => string> = {
  available: () => t("workshop.status.available"),
  installed: () => t("workshop.status.installed"),
  update: () => t("workshop.status.update"),
  changed: () => t("workshop.status.changed"),
  reserved: () => t("workshop.status.reserved"),
};

const ACTION_LABEL: Record<Action, () => string> = {
  install: () => t("workshop.plan.action.install"),
  same: () => t("workshop.plan.action.same"),
  update: () => t("workshop.plan.action.update"),
  conflict: () => t("workshop.plan.action.conflict"),
  reserved: () => t("workshop.plan.action.reserved"),
  rejected: () => t("workshop.plan.action.rejected"),
};

const RESOLUTION_LABEL: Record<Resolution, () => string> = {
  install: () => t("workshop.plan.resolution.install"),
  keep: () => t("workshop.plan.resolution.keep"),
  update: () => t("workshop.plan.resolution.update"),
  replace: () => t("workshop.plan.resolution.replace"),
  copy: () => t("workshop.plan.resolution.copy"),
  skip: () => t("workshop.plan.resolution.skip"),
};

const BADGE_LABEL: Record<Resolution | "keep_yours", () => string> = {
  install: () => t("workshop.plan.badge.install"),
  keep: () => t("workshop.plan.badge.keep"),
  keep_yours: () => t("workshop.plan.badge.keep_yours"),
  update: () => t("workshop.plan.badge.update"),
  replace: () => t("workshop.plan.badge.replace"),
  copy: () => t("workshop.plan.badge.copy"),
  skip: () => t("workshop.plan.badge.skip"),
};

const KIND_LABEL: Record<WorkshopKind, () => string> = {
  agent: () => t("workshop.kind.agent"),
  skill: () => t("workshop.kind.skill"),
};

const APPLY_LABEL: Record<ReturnType<typeof applyVerb>, (count: number) => string> = {
  install: (count) => t("workshop.plan.apply.install", { count }),
  update: (count) => t("workshop.plan.apply.update", { count }),
  apply: (count) => t("workshop.plan.apply.mixed", { count }),
};

const NOTICE_TEXT: Partial<Record<string, (name: string, refs: string) => string>> = {
  missing_skill: (name, refs) => t("workshop.notice.missing_skill", { name, refs }),
  missing_mcp: (name, refs) => t("workshop.notice.missing_mcp", { name, refs }),
  unknown_stage: (name, refs) => t("workshop.notice.unknown_stage", { name, refs }),
  image: (name, refs) => t("workshop.notice.image", { name, refs }),
  image_saas: (name, refs) => t("workshop.notice.image_saas", { name, refs }),
  image_import: (name, refs) => t("workshop.notice.image_import", { name, refs }),
  tools_skipped: (_name, refs) => t("workshop.notice.tools_skipped", { refs }),
  reserved: (name) => t("workshop.notice.reserved", { name }),
};

const IMPORT_ERROR: Record<BundleError["code"], () => string> = {
  too_large: () => t("workshop.import.too_large"),
  not_json: () => t("workshop.import.not_json"),
};

export function mountWorkshop(signal: AbortSignal, opts: WorkshopModalOpts): WorkshopModal {
  const overlay = $("ws-shop");
  const browse = $("ws-shop-browse");
  const planEl = $("ws-shop-plan");
  const list = $("ws-shop-list");
  const detail = $("ws-shop-detail");
  const search = $("ws-shop-q") as HTMLInputElement;
  const tagsEl = $("ws-shop-tags");
  const fileInput = $("ws-shop-file") as HTMLInputElement;
  const drop = $("ws-shop-drop");
  const importBtn = $("ws-shop-import") as HTMLButtonElement;

  let kind: WorkshopKind = "agent";
  let entries: WorkshopEntry[] = [];
  let loaded = false;
  let selected: string | null = null;
  let tags: string[] = [];
  let input: PlanInput | null = null;
  let plan: Plan | null = null;
  let busy = false;
  // Bumped by every plan request that replaces the plan on screen; a rename answer
  // arriving after a newer request is dropped.
  let planRequests = 0;
  let picks: ItemRef[] = [];
  let stageNames = new Map<string, string>();
  const picksEl = $("ws-shop-picks");

  const modal = bindModal(overlay, {
    beforeClose: () => !busy,
    onClose: () => showBrowse(),
  });

  const kindTabs = bindTabs($("ws-shop-tabs"), {
    onChange: (k, how) => {
      if (how.user) setTab(k as WorkshopKind);
    },
  });

  function setTab(k: WorkshopKind) {
    kind = k;
    tags = [];
    selected = null;
    kindTabs.select(k);
    renderTags();
    renderList();
    void renderDetail();
  }

  async function loadStages() {
    try {
      // The daemon answers null for a workspace with no boards.
      const stages = (await api.get<Stage[] | null>("/stages?include_virtual=1")) ?? [];
      stageNames = new Map(stages.map((st) => [st.id, st.label || st.id]));
    } catch {
      stageNames = new Map();
    }
  }

  async function load() {
    try {
      entries = await listWorkshop();
      loaded = true;
    } catch (e) {
      list.innerHTML = emptyRowHTML(errText(e), { as: "div", tone: "danger" });
      return;
    }
    for (const k of ["agent", "skill"] as const) {
      const n = entries.filter((e) => e.kind === k).length;
      const el = document.getElementById(`ws-shop-tab-${k}-count`);
      if (el) setCount(el, n, { pill: true, hideZero: true });
    }
    picks = picks.filter((p) => entries.some((e) => e.kind === p.kind && e.name === p.name));
    renderTags();
    renderList();
    renderPicks();
    void renderDetail();
  }

  function visible(): WorkshopEntry[] {
    return filterEntries(entries, kind, search.value, tags);
  }

  function renderTags() {
    const all = tagsOf(entries, kind);
    tagsEl.innerHTML = all.length
      ? filterChipsHTML(
          all.map((tag) => ({ value: tag, label: tagLabel(tag) })),
          tags,
          { attr: "tag", label: t("workshop.search.tags") },
        )
      : "";
  }

  function statusLabel(e: WorkshopEntry): string {
    return STATUS_LABEL[e.status]();
  }

  function pickable(e: WorkshopEntry): boolean {
    return opts.canWrite() && installLabel(e) !== null;
  }

  function picked(e: WorkshopEntry): boolean {
    return picks.some((p) => p.kind === e.kind && p.name === e.name);
  }

  function renderList() {
    if (!loaded) return;
    const rows = visible();
    if (rows.length === 0) {
      const empty = entries.some((e) => e.kind === kind)
        ? t("workshop.list.no_match")
        : kind === "agent"
          ? t("workshop.list.empty_agent")
          : t("workshop.list.empty_skill");
      list.innerHTML = emptyRowHTML(empty, { as: "div" });
      return;
    }
    list.innerHTML = rows
      .map(
        (e) => `<div class="wsp-row-wrap">
          ${
            pickable(e)
              ? checkHTML({
                  labelHTML: `<span class="visually-hidden">${esc(t("workshop.list.pick", { name: e.name }))}</span>`,
                  checked: picked(e),
                  cls: "wsp-pick",
                  inputAttrs: { "data-pick": e.name },
                })
              : ""
          }
          ${catalogItemHTML({
            name: e.name,
            description: e.description,
            badgesHTML: badgeHTML(statusLabel(e), statusTone(e.status)),
            current: e.name === selected,
            attrs: { "data-name": e.name },
          })}
        </div>`,
      )
      .join("");
  }

  function renderPicks() {
    picksEl.hidden = picks.length === 0;
    if (picks.length === 0) {
      picksEl.innerHTML = "";
      return;
    }
    picksEl.innerHTML = `<span class="wsp-picks-count">${esc(t("workshop.list.picked", { count: picks.length }))}</span>
      ${buttonHTML({ label: t("workshop.list.clear"), size: "sm", attrs: { "data-picks-clear": true } })}
      ${buttonHTML({ label: t("workshop.list.install_picked", { count: picks.length }), variant: "primary", size: "sm", attrs: { "data-picks-install": true } })}`;
  }

  const NONE = () => t("workshop.detail.none");
  const NONE_F = () => t("workshop.detail.none_f");

  function chips(values: string[] | undefined, mono = true, none = NONE): string {
    if (!values || values.length === 0) return `<span class="wsp-none">${esc(none())}</span>`;
    return chipList(values.map((v) => badgeHTML(v, "neutral", { mono })));
  }

  function stageChips(ids: string[] | undefined): string {
    if (!ids || ids.length === 0) return `<span class="wsp-none">${esc(NONE_F())}</span>`;
    return chipList(
      ids.map((id) => {
        const name = stageNames.get(id);
        return name
          ? badgeHTML(name, "neutral", { title: id })
          : badgeHTML(id, "muted", { mono: true, title: t("workshop.detail.stage_missing") });
      }),
    );
  }

  function chipList(badges: string[]): string {
    return `<span class="wsp-chips">${badges.join("")}</span>`;
  }

  async function renderDetail() {
    const split = detail.parentElement;
    split?.classList.toggle("show-detail", selected !== null);
    const e = entries.find((x) => x.kind === kind && x.name === selected);
    if (!e) {
      detail.innerHTML = emptyStateHTML({ size: "sm", body: t("workshop.detail.pick") });
      return;
    }
    detail.innerHTML = detailHTML(e, null);
    wireDetail(e);
    try {
      const full = await getWorkshopItem(e.kind, e.name);
      if (selected !== e.name || kind !== e.kind) return;
      detail.innerHTML = detailHTML(e, full.files);
      wireDetail(e);
    } catch (err) {
      opts.toast(errText(err), "err");
    }
  }

  function detailHTML(e: WorkshopEntry, files: WorkshopFile[] | null): string {
    const rows: Array<[string, string]> = [];
    if (e.kind === "agent") {
      rows.push([t("workshop.detail.skills"), chips(e.skills, true, NONE_F)]);
      rows.push([t("workshop.detail.stages"), stageChips(e.stages)]);
      rows.push([t("workshop.detail.mcp"), chips(e.mcp)]);
      if (e.image) rows.push([t("workshop.detail.image"), chips([e.image])]);
    } else {
      rows.push([t("workshop.detail.requires"), chips(e.requires, true, NONE_F)]);
      rows.push([t("workshop.detail.used_by"), chips(e.used_by)]);
      if (e.mcp?.length) rows.push([t("workshop.detail.mcp"), chips(e.mcp)]);
    }
    rows.push([t("workshop.detail.revision"), esc(installedText(e))]);
    if (e.tags.length) rows.push([t("workshop.detail.tags"), chips(e.tags.map(tagLabel), false)]);
    const fileBlock =
      files === null
        ? `<div class="wsp-none">${esc(t("common.status.loading"))}</div>`
        : disclosureHTML({
            variant: "code",
            summary: t("workshop.detail.files", { count: files.length }),
            bodyHTML: files
              .map((f) => {
                const body = previewBody(f);
                return `<div class="wsp-file"><div class="wsp-file-path">${esc(f.path)}</div>${
                  body === null
                    ? `<div class="wsp-none">${esc(t("workshop.detail.binary"))}</div>`
                    : codeBlockHTML({ text: body, multiline: true, maxHeight: "lg" })
                }</div>`;
              })
              .join(""),
          });
    const action = installLabel(e);
    return `${entityHeadHTML({
      title: e.name,
      level: 3,
      badgesHTML: badgeHTML(statusLabel(e), statusTone(e.status)),
      actions: [
        {
          label: t("workshop.detail.back"),
          icon: "chevron-left",
          cls: "wsp-back",
          attrs: { "data-back": true },
        },
      ],
    })}
      <div class="wsp-detail-body">
        ${e.description ? `<p class="wsp-desc">${esc(e.description)}</p>` : ""}
        ${factsHTML(rows.map(([label, valueHTML]) => ({ label, valueHTML })))}
        ${fileBlock}
      </div>
      <div class="wsp-detail-foot">
        <span class="wsp-foot-note">${esc(footNote(e))}</span>
        ${opts.canWrite() && action ? buttonHTML({ label: action, variant: "primary", attrs: { "data-install": true } }) : ""}
      </div>`;
  }

  function installedText(e: WorkshopEntry): string {
    if (e.installed_revision && e.installed_revision !== e.revision) {
      return t("workshop.detail.revision_installed", {
        rev: e.revision,
        installed: e.installed_revision,
      });
    }
    return t("workshop.detail.revision_value", { rev: e.revision });
  }

  function installLabel(e: WorkshopEntry): string | null {
    switch (e.status) {
      case "available":
        return t("workshop.buttons.install");
      case "update":
        return t("workshop.buttons.update");
      case "changed":
        return t("workshop.buttons.review");
      case "installed":
      case "reserved":
        return null;
    }
  }

  function footNote(e: WorkshopEntry): string {
    if (!opts.canWrite()) return t("workshop.detail.viewer");
    if (e.disabled) return t("workshop.detail.disabled");
    if (e.status === "installed") return t("workshop.detail.already");
    if (e.status === "reserved") return t("workshop.detail.reserved");
    if (e.kind === "agent" && e.skills?.length)
      return t("workshop.detail.brings", { count: e.skills.length });
    return "";
  }

  function wireDetail(e: WorkshopEntry) {
    detail.querySelector("[data-back]")?.addEventListener("click", () => {
      selected = null;
      renderList();
      void renderDetail();
    });
    detail.querySelector("[data-install]")?.addEventListener("click", () => {
      void startPlan({ items: [{ kind: e.kind, name: e.name }] });
    });
  }

  async function startPlan(next: PlanInput) {
    input = next;
    plan = null;
    showPlan();
    planEl.innerHTML = emptyStateHTML({
      size: "sm",
      loading: true,
      title: t("workshop.plan.loading"),
    });
    try {
      plan = await planWorkshop(next);
      renderPlan();
    } catch (e) {
      planEl.innerHTML = `<div class="wsp-plan-body">${calloutHTML({ tone: "danger", icon: "alert", body: errText(e), attrs: { role: "alert" } })}</div>
        <div class="wsp-plan-foot">${buttonHTML({ label: t("workshop.plan.back"), attrs: { "data-plan-back": true } })}</div>`;
      planEl.querySelector("[data-plan-back]")?.addEventListener("click", showBrowse);
    }
  }

  function actionText(it: PlanItem, isImport: boolean): string {
    const asCopy = copyActionText(it);
    if (asCopy) return asCopy;
    if (it.action === "update" && isImport) return t("workshop.plan.action.update_import");
    if (it.disabled) return t("workshop.plan.action.disabled");
    return ACTION_LABEL[it.action]();
  }

  function itemSub(it: PlanItem, isImport: boolean): string {
    const parts: string[] = [actionText(it, isImport)];
    if (it.action === "conflict" && it.edited) parts.push(t("workshop.plan.edited"));
    if (it.kind === "skill" && it.used_by?.length) {
      parts.push(t("workshop.plan.used_by", { agents: it.used_by.join(", ") }));
    }
    if (it.kind === "agent" && (it.resolution === "replace" || it.resolution === "update")) {
      parts.push(t("workshop.plan.history_kept"));
    }
    return parts.join(". ");
  }

  function noticeText(n: PlanNotice, isImport: boolean): string {
    const refs = (n.refs ?? []).join(", ");
    if (n.code === "kept_skill") {
      return isImport
        ? t("workshop.notice.kept_skill_import", { name: n.name ?? "", refs })
        : t("workshop.notice.kept_skill", { name: n.name ?? "", refs });
    }
    if (n.code === "secret") {
      return t("workshop.notice.secret", {
        kind: KIND_LABEL[n.kind ?? "agent"](),
        name: n.name ?? "",
        refs,
      });
    }
    const text = NOTICE_TEXT[n.code];
    return text ? text(n.name ?? "", refs) : n.code;
  }

  function noticeAction(n: PlanNotice): string {
    if (n.code !== "kept_skill" || !plan) return "";
    const skill = n.refs?.[0];
    const it = plan.items.find((x) => x.kind === "skill" && x.name === skill);
    if (!it?.options.includes("copy")) return "";
    return buttonHTML({
      label: t("workshop.plan.resolution.copy"),
      size: "sm",
      attrs: { "data-notice-copy": itemKey(it) },
    });
  }

  function renderPlan() {
    if (!plan) return;
    const p = plan;
    const isImport = p.source === "import";
    const writes = writesOf(p);
    const lead = isImport ? t("workshop.plan.lead_import") : t("workshop.plan.lead");
    const notices = p.notices.length
      ? `<ul class="wsp-notices">${p.notices
          .map(
            (n) =>
              `<li>${calloutHTML({
                tone: n.severity === "error" ? "danger" : "warn",
                icon: n.severity === "error" ? "x-circle" : "warn",
                body: noticeText(n, isImport),
                actionsHTML: noticeAction(n),
              })}</li>`,
          )
          .join("")}</ul>`
      : "";
    const items = p.items
      .map((it) => {
        const icon = it.kind === "agent" ? "i-bot" : "i-skills";
        const seg =
          it.options.length > 1
            ? segmentedHTML(
                `r-${itemKey(it)}`,
                it.options.map((o) => ({
                  value: o,
                  label: RESOLUTION_LABEL[o](),
                  inputAttrs: { "data-key": itemKey(it) },
                })),
                it.resolution,
                {
                  cards: true,
                  label: t("workshop.plan.choose", { name: it.name }),
                  cls: "wsp-seg",
                },
              )
            : "";
        const copyName =
          it.resolution === "copy"
            ? fieldHTML({
                label: t("workshop.plan.copy_name"),
                for: `ws-copy-${itemKey(it)}`,
                layout: "inline",
                cls: "wsp-copy-name",
                control: `<input type="text" id="${esc(`ws-copy-${itemKey(it)}`)}" spellcheck="false" autocomplete="off" data-copy-key="${esc(itemKey(it))}" value="${esc(it.target)}" />`,
              })
            : "";
        return `<div class="wsp-item">${icoSvg(icon)}<span class="wsp-item-name">${esc(it.name)}</span>${badgeHTML(
          BADGE_LABEL[badgeOf(it)](),
          writesTone(it.resolution),
        )}<span class="wsp-item-sub">${esc(itemSub(it, isImport))}</span>${seg}${copyName}</div>`;
      })
      .join("");
    const applyLabel =
      writes === 0 ? t("workshop.plan.nothing") : APPLY_LABEL[applyVerb(p)](writes);
    planEl.innerHTML = `<div class="wsp-plan-body">
        <h4>${esc(t("workshop.plan.title"))}</h4>
        <p class="wsp-plan-lead">${esc(lead)}</p>
        ${notices}
        <div class="wsp-items">${items}</div>
      </div>
      <div class="wsp-plan-foot">
        ${buttonHTML({ label: t("workshop.plan.back"), attrs: { "data-plan-back": true } })}
        ${buttonHTML({ label: applyLabel, variant: "primary", attrs: { "data-plan-apply": true } })}
      </div>`;
    planEl.querySelector("[data-plan-back]")?.addEventListener("click", showBrowse);
    const applyBtn = planEl.querySelector<HTMLButtonElement>("[data-plan-apply]");
    if (applyBtn) applyBtn.disabled = p.blocked || writes === 0;
    applyBtn?.addEventListener("click", () => void apply());
    planEl
      .querySelectorAll<HTMLInputElement>("input[type=radio]")
      .forEach((r) =>
        r.addEventListener(
          "change",
          () => void rechoose(r.dataset.key ?? "", r.value as Resolution),
        ),
      );
    planEl
      .querySelectorAll<HTMLButtonElement>("[data-notice-copy]")
      .forEach((b) =>
        b.addEventListener("click", () => void rechoose(b.dataset.noticeCopy ?? "", "copy")),
      );
    planEl.querySelectorAll<HTMLInputElement>("[data-copy-key]").forEach((inp) => {
      const commit = () => void rename(inp.dataset.copyKey ?? "", inp.value.trim());
      inp.addEventListener("change", commit);
      inp.addEventListener("keydown", (e) => {
        if (e.key === "Enter") {
          e.preventDefault();
          commit();
        }
      });
    });
  }

  function writesTone(r: Resolution): "muted" | "ok" {
    return writes(r) ? "ok" : "muted";
  }

  async function rechoose(key: string, res: Resolution) {
    if (!plan || !input) return;
    const picked = choicesOf(plan);
    const choices = {
      resolutions: { ...picked.resolutions, [key]: res },
      names: Object.fromEntries(Object.entries(picked.names).filter(([k]) => k !== key)),
    };
    try {
      plan = await planWorkshop(input, choices);
      renderPlan();
      planEl
        .querySelector<HTMLInputElement>(`input[data-key="${CSS.escape(key)}"][value="${res}"]`)
        ?.focus();
    } catch (e) {
      opts.toast(errText(e), "err");
    }
  }

  async function rename(key: string, name: string) {
    if (!plan || !input || !name || busy) return;
    const choices = choicesOf(plan);
    if (choices.names[key] === name) return;
    choices.names[key] = name;
    const asked = ++planRequests;
    try {
      const next = await planWorkshop(input, choices);
      // Install was pressed meanwhile: apply() planned with this name itself.
      if (asked !== planRequests) return;
      plan = next;
      renderPlan();
      planEl.querySelector<HTMLInputElement>(`[data-copy-key="${CSS.escape(key)}"]`)?.focus();
    } catch (e) {
      opts.toast(errText(e), "err");
      renderPlan();
    }
  }

  async function apply() {
    if (!plan || !input || busy) return;
    const btn = planEl.querySelector<HTMLButtonElement>("[data-plan-apply]");
    busy = true;
    if (btn) {
      btn.disabled = true;
      btn.textContent = t("workshop.plan.applying");
    }
    try {
      // A copy name still in its input has not reached the plan: replan with it first.
      const typed = Object.fromEntries(
        Array.from(planEl.querySelectorAll<HTMLInputElement>("[data-copy-key]")).map((inp) => [
          inp.dataset.copyKey ?? "",
          inp.value,
        ]),
      );
      const renamed = withTypedNames(plan, typed);
      planRequests++;
      if (renamed) plan = await planWorkshop(input, renamed);
      const res = await applyWorkshop(input, choicesOf(plan));
      busy = false;
      picks = [];
      renderPicks();
      opts.onApplied(res);
      renderResult(res);
      void load();
    } catch (e) {
      busy = false;
      opts.toast(errText(e), "err");
      if (btn) {
        btn.disabled = false;
        btn.textContent = APPLY_LABEL[applyVerb(plan)](writesOf(plan));
      }
    }
  }

  function renderResult(res: ApplyResult) {
    const r = res.result;
    const names = (refs: ItemRef[]) => refs.map((x) => x.name).join(", ");
    const groups: Array<[string, string]> = [];
    if (r.installed.length)
      groups.push([
        t("workshop.result.installed", { count: r.installed.length }),
        names(r.installed),
      ]);
    if (r.updated.length)
      groups.push([t("workshop.result.updated", { count: r.updated.length }), names(r.updated)]);
    if (r.replaced.length)
      groups.push([t("workshop.result.replaced", { count: r.replaced.length }), names(r.replaced)]);
    if (r.copied.length)
      groups.push([
        t("workshop.result.copied", { count: r.copied.length }),
        r.copied.map((c) => t("workshop.result.copy_as", { from: c.from, to: c.to })).join(", "),
      ]);
    if (r.skipped.length)
      groups.push([t("workshop.result.skipped", { count: r.skipped.length }), names(r.skipped)]);
    const body = resultIsEmpty(r)
      ? `<p class="wsp-plan-lead">${esc(t("workshop.result.nothing"))}</p>`
      : factsHTML(groups.map(([label, value]) => ({ label, value })));
    const newAgent = [...r.installed, ...r.copied].some((x) => x.kind === "agent");
    const hint = newAgent
      ? `<p class="wsp-plan-lead">${esc(t("workshop.result.agent_hint"))}</p>`
      : "";
    planEl.innerHTML = `<div class="wsp-plan-body">
        <h4>${icoSvg("i-check")}${esc(t("workshop.result.title"))}</h4>
        ${body}
        ${hint}
      </div>
      <div class="wsp-plan-foot">
        ${buttonHTML({ label: t("workshop.result.back"), variant: "primary", attrs: { "data-plan-back": true } })}
      </div>`;
    input = null;
    plan = null;
    planEl
      .querySelector<HTMLButtonElement>("[data-plan-back]")
      ?.addEventListener("click", showBrowse);
    planEl.querySelector<HTMLButtonElement>("[data-plan-back]")?.focus();
  }

  function showPlan() {
    browse.hidden = true;
    planEl.hidden = false;
  }

  function showBrowse() {
    planEl.hidden = true;
    browse.hidden = false;
    input = null;
    plan = null;
  }

  async function importFile(file: File) {
    try {
      const bundle = await readBundleFile(file);
      await startPlan({ bundle });
    } catch (e) {
      const msg = e instanceof BundleError ? IMPORT_ERROR[e.code]() : errText(e);
      opts.toast(msg, "err");
    }
  }

  importBtn.addEventListener("click", () => fileInput.click(), { signal });
  fileInput.addEventListener(
    "change",
    () => {
      const f = fileInput.files?.[0];
      fileInput.value = "";
      if (f) void importFile(f);
    },
    { signal },
  );

  // Modal.astro writes the body (.wsp-body) without an id.
  const body = overlay.querySelector<HTMLElement>(".wsp-body") ?? overlay;
  bindDropOverlay(body, drop, {
    signal,
    enabled: () => opts.canWrite() && !busy,
    onJson: (f) => void importFile(f),
  });

  search.addEventListener("input", () => renderList(), { signal });
  tagsEl.addEventListener(
    "click",
    (e) => {
      const b = (e.target as HTMLElement).closest<HTMLElement>("[data-tag]");
      if (!b) return;
      const tag = b.dataset.tag ?? "";
      tags = tags.includes(tag) ? tags.filter((x) => x !== tag) : [...tags, tag];
      // Toggled in place: redrawing the chips would lose the focus.
      setChipsPressed(tagsEl, tags, "tag");
      renderList();
    },
    { signal },
  );
  const unbindList = bindItemList(list, {
    onPick: (row) => {
      selected = row.dataset.name ?? null;
      void renderDetail();
    },
  });
  signal.addEventListener("abort", unbindList);
  list.addEventListener(
    "change",
    (e) => {
      const box = (e.target as HTMLElement).closest<HTMLInputElement>("[data-pick]");
      if (!box) return;
      const name = box.dataset.pick ?? "";
      picks = picks.filter((p) => !(p.kind === kind && p.name === name));
      if (box.checked) picks.push({ kind, name });
      renderPicks();
    },
    { signal },
  );
  picksEl.addEventListener(
    "click",
    (e) => {
      const el = e.target as HTMLElement;
      if (el.closest("[data-picks-clear]")) {
        picks = [];
        renderPicks();
        renderList();
      } else if (el.closest("[data-picks-install]") && picks.length) {
        void startPlan({ items: [...picks] });
      }
    },
    { signal },
  );

  return {
    open(k: WorkshopKind) {
      importBtn.hidden = !opts.canWrite();
      showBrowse();
      search.value = "";
      picks = [];
      renderPicks();
      setTab(k);
      modal.open(search);
      void loadStages().then(load);
    },
    isOpen: modal.isOpen,
    close: () => modal.close(),
  };
}

function errText(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  if (e instanceof Error) return e.message;
  return t("workshop.errors.generic");
}

// Strings, not an .astro component: the list is redrawn from JavaScript every minute, so scoped
// component styles would not reach it (COMPONENTS.md, same call as lib/badges.ts).

import { t } from "../i18n";
import { scheduleLabel } from "./evidence-view";
import { safeUrl } from "./safe-url";
import {
  attrsHTML,
  badgeHTML,
  buttonHTML,
  calloutHTML,
  chartPanelsHTML,
  emptyStateHTML,
  esc,
  groupHeadHTML,
  listRowHTML,
  listRowsHTML,
  sectionHeadHTML,
  statGroupHTML,
  statTileHTML,
  tabPanelAttrs,
  tabsHTML,
  tagHTML,
  type ListRowOptions,
} from "./ui";
import {
  PUSHED_UNITS,
  pushedSignals,
  signalLabel,
  type SeriesPoint,
  type SignalMapping,
} from "./signals";

export interface Environment {
  id: string;
  project_id: string;
  name: string;
  display_name?: string;
  system?: string;
  provider: string;
  region?: string;
  account?: string;
  url?: string;
  selectors?: Record<string, string>;
  is_production: boolean;
  has_credential: boolean;
}

export interface Mapping extends SignalMapping {
  id: string;
  environment_id: string;
  source: Record<string, unknown>;
  status: "proposed" | "active" | "disabled" | "not_applicable";
  created_by: string;
  note?: string;
}

export interface CustomMetric {
  id: string;
  service: string;
  name: string;
  kind: "counter" | "gauge";
  unit?: string;
  display_name?: string;
  labels: string[];
  label_limit: number;
  status: "discovered" | "approved" | "rejected";
  approved_by?: string;
  first_seen: string;
  last_seen: string;
}

export interface CustomMetricsResponse {
  metrics: CustomMetric[];
  discovered: number;
  registered: number;
  capacity: number;
}

export interface Dashboard {
  id: string;
  environment_id: string;
  name: string;
  description?: string;
  status: "proposed" | "active" | "archived";
  panels: Array<{ id: string; type: string; title: string }>;
  created_by: string;
  approved_by?: string;
  updated_at: string;
}

export interface EnvDetail {
  mappings: Mapping[];
  points: SeriesPoint[];
  custom: CustomMetricsResponse;
  dashboards: Dashboard[];
}

export interface EnvCardDeps {
  detail: (envID: string) => EnvDetail | undefined;
  openDashID: (envID: string) => string | undefined;
  dashPanels: (dashID: string) => string[] | undefined;
  fmtSignal: (signal: string, value: number, unit?: string) => string;
  sigAge: (bucket: string) => string;
  sparkline: (values: number[]) => string;
  testing?: (envID: string) => EnvTesting | undefined;
}

export interface EnvTesting {
  id: string;
  base_url: string;
  secret_names?: string[] | null;
  schedule: string;
  schedule_tz: string;
}

const METRIC_STATUS: Record<string, string> = {
  discovered: t("lib.env_cards.custom.status_discovered"),
  approved: t("lib.env_cards.custom.status_approved"),
  rejected: t("lib.env_cards.custom.status_rejected"),
};

const METRIC_KIND: Record<string, string> = {
  counter: t("lib.env_cards.labels.metric_kinds.counter"),
  gauge: t("lib.env_cards.labels.metric_kinds.gauge"),
};

export function metricLabel(m: CustomMetric): string {
  const shown = m.display_name ?? "";
  return shown === "" ? m.name : shown;
}

export function groupBySystem(environments: Environment[]): Array<[string, Environment[]]> {
  const groups = new Map<string, Environment[]>();
  for (const e of environments) {
    const key = e.system ?? "";
    const bucket = groups.get(key);
    if (bucket) bucket.push(e);
    else groups.set(key, [e]);
  }
  return [...groups.entries()].sort((a, b) =>
    a[0] === "" ? 1 : b[0] === "" ? -1 : a[0].localeCompare(b[0]),
  );
}

export function envProposalsHTML(e: Environment, d: EnvCardDeps): string {
  const proposed = (d.detail(e.id)?.mappings ?? []).filter((m) => m.status === "proposed");
  if (!proposed.length) return "";
  const by = proposed[0].created_by;
  const list = proposed
    .map((m) => badgeHTML(`${m.service}·${m.signal}`, "neutral", { mono: true }))
    .join(" ");
  return calloutHTML({
    tone: "accent",
    title: t("lib.env_cards.proposals.count", { count: proposed.length }),
    bodyHTML:
      (by ? `<p>${esc(t("lib.env_cards.proposals.by", { who: by }))}</p>` : "") +
      `<div class="env-meta env-meta-stacked">${list}</div>` +
      `<p>${esc(t("lib.env_cards.proposals.inert"))}</p>`,
    actionsHTML: buttonHTML({
      label: t("lib.env_cards.buttons.approve"),
      variant: "primary",
      attrs: { "data-env-approve": e.id },
    }),
    cls: "env-proposal",
  });
}

export function envSignalsHTML(e: Environment, d: EnvCardDeps): string {
  const detail = d.detail(e.id);
  if (!detail) return "";
  const active = detail.mappings.filter((m) => m.status === "active");
  const na = detail.mappings.filter((m) => m.status === "not_applicable");
  const pushed = pushedSignals(detail);
  if (!active.length && !na.length && !pushed.length) return "";

  const naTiles = na.map((m) =>
    statTileHTML({
      size: "sm",
      na: true,
      label: `${m.service} · ${signalLabel(m.signal)}`,
      value: t("lib.env_cards.signals.na"),
      sub: m.note ?? t("lib.env_cards.signals.not_applicable_here"),
      attrs: { title: m.note ?? t("lib.env_cards.signals.not_applicable") },
    }),
  );

  const tiles = active.map((m) => {
    const pts = detail.points
      .filter((p) => p.signal === m.signal && p.service === m.service)
      .sort((a, b) => a.bucket.localeCompare(b.bucket));
    const label = `${m.service} · ${signalLabel(m.signal)}`;
    if (!pts.length) {
      return statTileHTML({
        size: "sm",
        tone: "muted",
        label,
        value: "—",
        sub: t("lib.env_cards.signals.no_data"),
      });
    }
    const last = pts[pts.length - 1];
    const unit = typeof m.source.unit === "string" ? m.source.unit : undefined;
    return statTileHTML({
      size: "sm",
      label,
      value: d.fmtSignal(m.signal, last.value, unit),
      sparkHTML: d.sparkline(pts.map((p) => p.value)),
      sub: d.sigAge(last.bucket),
    });
  });

  const pushedTiles = pushed.map(({ service, signal, pts }) => {
    const last = pts[pts.length - 1];
    return statTileHTML({
      size: "sm",
      label: `${service} · ${signalLabel(signal)}`,
      value: d.fmtSignal(signal, last.value, PUSHED_UNITS[signal]),
      sparkHTML: d.sparkline(pts.map((p) => p.value)),
      subHTML: `<span class="sig-pushed">${esc(t("lib.env_cards.signals.pushed_age", { age: d.sigAge(last.bucket) }))}</span>`,
    });
  });

  return statGroupHTML([...tiles, ...pushedTiles, ...naTiles], {
    cls: "sig-strip",
    label: t("lib.env_cards.signals.title"),
  });
}

export function customMetricsHTML(e: Environment, d: EnvCardDeps): string {
  const reg = d.detail(e.id)?.custom;
  if (!reg?.metrics.length) return "";
  const row = (m: CustomMetric): ListRowOptions => {
    const labels = m.labels.length
      ? `<span class="cm-labels">${m.labels.map((l) => esc(l)).join(" · ")}</span>`
      : `<span class="cm-labels">${esc(t("lib.env_cards.custom.no_labels"))}</span>`;
    const id = { "data-cm-id": m.id };
    const kind = badgeHTML(METRIC_KIND[m.kind] ?? m.kind, "neutral", { mono: true });
    const name = {
      titleHTML: `${esc(m.service)} · <strong>${esc(metricLabel(m))}</strong>`,
      wrap: "all" as const,
    };
    if (m.status === "discovered") {
      return {
        ...name,
        badgesHTML: kind,
        metaHTML: labels,
        aside: d.sigAge(m.last_seen),
        actions: [
          {
            label: t("lib.env_cards.buttons.approve"),
            variant: "primary",
            attrs: { "data-cm-approve": e.id, ...id },
          },
          { label: t("lib.env_cards.buttons.reject"), attrs: { "data-cm-reject": e.id, ...id } },
        ],
      };
    }
    return {
      ...name,
      badgesHTML:
        kind +
        badgeHTML(METRIC_STATUS[m.status] ?? m.status, m.status === "approved" ? "ok" : "neutral", {
          mono: m.status !== "approved",
        }),
      metaHTML: labels,
      aside: d.sigAge(m.last_seen),
      dim: m.status === "rejected",
      actions: [
        {
          label: t("lib.env_cards.buttons.back_to_waiting"),
          variant: "ghost",
          attrs: { "data-cm-reset": e.id, ...id },
        },
        {
          label: t("common.buttons.delete"),
          variant: "danger",
          attrs: { "data-cm-delete": e.id, ...id },
        },
      ],
    };
  };
  const waiting = reg.discovered
    ? badgeHTML(t("lib.env_cards.custom.waiting", { count: reg.discovered }), "accent")
    : "";
  const capacity =
    reg.capacity && reg.registered >= reg.capacity * 0.8
      ? badgeHTML(
          t("lib.env_cards.custom.registered", {
            registered: reg.registered,
            capacity: reg.capacity,
          }),
          "warn",
        )
      : "";
  return `<div class="cm-block">
        ${sectionHeadHTML({ title: t("lib.env_cards.custom.title"), level: 4, asideHTML: waiting + capacity })}
        <p class="env-block-lede">${esc(t("lib.env_cards.custom.hint"))}</p>
        ${listRowsHTML(reg.metrics.map(row))}
      </div>`;
}

export function dashboardsHTML(e: Environment, d: EnvCardDeps): string {
  const dashboards = d.detail(e.id)?.dashboards ?? [];
  const metrics = d.detail(e.id)?.custom.metrics ?? [];
  const approved = metrics.filter((m) => m.status === "approved");
  const unlockText = metrics.length
    ? t("lib.env_cards.dashboards.approve_first")
    : t("lib.env_cards.dashboards.no_metrics");
  const unlock = approved.length ? "" : `<p class="env-block-lede">${esc(unlockText)}</p>`;
  if (!dashboards.length && !approved.length) {
    return `<div class="dash-block">
        ${sectionHeadHTML({ title: t("lib.env_cards.dashboards.title"), level: 4 })}
        ${emptyStateHTML({ size: "inline", dashed: true, body: unlockText })}
      </div>`;
  }
  const openID = d.openDashID(e.id);
  const live = dashboards.filter((x) => x.status !== "archived");
  const open = dashboards.find((x) => x.id === openID);
  const prefix = `dash-${e.id}-`;
  const panelId = `${prefix}panel`;
  // active is null while no dashboard is open: a proposal is not opened unasked (loadOpenDashboard
  // in project.astro).
  const tabs = live.length
    ? tabsHTML({
        variant: "pill",
        label: t("lib.env_cards.dashboards.title"),
        idPrefix: prefix,
        active: open ? open.id : null,
        attrs: { "data-dash-env": e.id },
        items: live.map((x) => ({
          id: x.id,
          label: x.name,
          panelId,
          badge:
            x.status === "proposed"
              ? { label: t("lib.env_cards.dashboards.proposed"), tone: "accent" as const }
              : undefined,
        })),
      })
    : "";

  const canBuild = approved.length > 0;
  const newBtn = canBuild
    ? buttonHTML({
        label: t("lib.env_cards.dashboards.new"),
        size: "sm",
        title: t("lib.env_cards.dashboards.new_title"),
        attrs: { "data-dash-new": e.id },
      })
    : "";

  const editBtn =
    open && canBuild
      ? buttonHTML({
          label: t("common.buttons.edit"),
          size: "sm",
          variant: "ghost",
          attrs: { "data-dash-edit": e.id, "data-dash-id": open.id },
        })
      : "";
  let body = "";
  let removeBtn = "";
  if (open) {
    if (open.status === "active") {
      removeBtn = buttonHTML({
        label: t("common.buttons.delete"),
        size: "sm",
        variant: "danger",
        attrs: { "data-dash-remove": e.id, "data-dash-id": open.id },
      });
    }
    const review =
      open.status === "proposed"
        ? calloutHTML({
            tone: "accent",
            bodyHTML:
              `<p>${t("lib.env_cards.dashboards.review_html", { name: esc(open.name), who: esc(open.created_by), count: open.panels.length })}</p>` +
              `<p>${esc(t("lib.env_cards.dashboards.review_hint"))}</p>`,
            actionsHTML:
              buttonHTML({
                label: t("lib.env_cards.buttons.approve"),
                variant: "primary",
                attrs: { "data-dash-approve": e.id, "data-dash-id": open.id },
              }) +
              buttonHTML({
                label: t("common.buttons.discard"),
                variant: "danger",
                attrs: { "data-dash-del": e.id, "data-dash-id": open.id },
              }),
            cls: "env-proposal",
          })
        : "";
    const panels = d.dashPanels(open.id);
    const grid = panels
      ? panels.length
        ? chartPanelsHTML(panels)
        : emptyStateHTML({ size: "sm", body: t("lib.env_cards.dashboards.no_panels") })
      : emptyStateHTML({ size: "sm", loading: true });
    const tab = { id: open.id, label: open.name, panelId };
    body = `<div${attrsHTML(tabPanelAttrs(tab, true, { idPrefix: prefix }))} class="dash-panel">${review}${grid}</div>`;
  }
  const none =
    canBuild && !tabs
      ? `<p class="env-block-lede">${esc(t("lib.env_cards.dashboards.none_yet"))}</p>`
      : "";
  const actions = newBtn + editBtn + removeBtn;
  const bar =
    tabs || actions
      ? `<div class="dash-bar">${tabs}${actions ? `<div class="dash-actions">${actions}</div>` : ""}</div>`
      : "";
  return `<div class="dash-block">${bar}${unlock}${none}${body}</div>`;
}

function envURL(url: string | undefined): string {
  if (!url) return "";
  const href = safeUrl(url);
  if (!href) return `<span>${esc(url)}</span>`;
  return `<a href="${esc(href)}" target="_blank" rel="noopener noreferrer">${esc(url)}</a>`;
}

export function envTestingHTML(e: Environment, d: EnvCardDeps): string {
  const ev = d.testing?.(e.id);
  if (!ev) return "";
  const logins = (ev.secret_names ?? []).length;
  const facts = [
    ev.base_url
      ? `<span>${esc(ev.base_url)}</span>`
      : `<span class="muted">${esc(t("lib.env_cards.testing.no_url"))}</span>`,
    `<span>${esc(logins ? t("lib.env_cards.testing.logins", { count: logins }) : t("lib.env_cards.testing.no_logins"))}</span>`,
    `<span>${esc(ev.schedule ? scheduleLabel(ev.schedule, ev.schedule_tz) : t("lib.env_cards.testing.manual"))}</span>`,
  ];
  const href = `/evidence/?tab=environments&env=${encodeURIComponent(ev.id)}`;
  return calloutHTML({
    icon: "i-tt-checklist",
    title: t("lib.env_cards.testing.title"),
    bodyHTML: `<span class="env-meta">${facts.join("")}</span>`,
    actionsHTML: `<a class="env-testing-link" href="${esc(href)}">${esc(t("lib.env_cards.testing.edit"))}</a>`,
    cls: "env-testing",
  });
}

export function envCardHTML(e: Environment, d: EnvCardDeps): string {
  const sel = Object.entries(e.selectors ?? {})
    .map(([k, v]) => badgeHTML(`${k}=${v}`, "neutral", { mono: true }))
    .join(" ");
  const cred = e.has_credential
    ? tagHTML(t("lib.env_cards.credential.set"), {
        button: true,
        color: "green",
        title: t("lib.env_cards.credential.change_title"),
        attrs: { "data-env-cred": e.id },
      })
    : tagHTML(t("lib.env_cards.credential.attach"), {
        button: true,
        dashed: true,
        icon: "plus",
        title: t("lib.env_cards.credential.attach_title"),
        attrs: { "data-env-cred": e.id },
      });
  const active = (d.detail(e.id)?.mappings ?? []).filter((m) => m.status === "active");
  const mapped = active.length
    ? badgeHTML(t("lib.env_cards.badges.signals", { count: active.length }), "accent")
    : e.has_credential
      ? badgeHTML(t("lib.env_cards.badges.not_mapped"), "warn")
      : "";
  const shown = e.display_name?.trim();
  const name = shown
    ? `<span class="env-name">${esc(shown)}</span><span class="env-slug">${esc(e.name)}</span>`
    : `<span class="env-name">${esc(e.name)}</span>`;
  const meta = [
    e.account
      ? `<span>${esc(t("lib.env_cards.labels.account", { account: e.account }))}</span>`
      : "",
    envURL(e.url),
    sel,
  ].join("");
  const head = listRowHTML({
    titleHTML: name,
    badgesHTML:
      (e.is_production ? badgeHTML(t("lib.env_cards.badges.production"), "warn") : "") +
      badgeHTML(e.provider, "neutral", { mono: true }) +
      (e.region ? badgeHTML(e.region, "neutral", { mono: true }) : "") +
      cred +
      mapped,
    metaHTML: meta ? `<span class="env-meta">${meta}</span>` : undefined,
    actions: [
      {
        label: t("lib.env_cards.actions.collect"),
        icon: "refresh",
        title: t("lib.env_cards.actions.collect_aria"),
        attrs: { "data-env-collect": e.id },
      },
      {
        label: t("lib.env_cards.actions.telemetry"),
        icon: "zap",
        title: t("lib.env_cards.actions.telemetry_aria"),
        attrs: { "data-env-ingest": e.id },
      },
      {
        iconOnly: true,
        icon: "edit",
        label: t("lib.env_cards.actions.edit_aria"),
        attrs: { "data-env-edit": e.id },
      },
      {
        iconOnly: true,
        icon: "trash",
        variant: "danger",
        label: t("lib.env_cards.actions.delete_aria"),
        attrs: { "data-env-del": e.id },
      },
    ],
  });
  const blocks =
    envTestingHTML(e, d) +
    envProposalsHTML(e, d) +
    envSignalsHTML(e, d) +
    customMetricsHTML(e, d) +
    dashboardsHTML(e, d);
  return `<div class="env-item">${head}${blocks ? `<div class="env-detail">${blocks}</div>` : ""}</div>`;
}

export function envListHTML(environments: Environment[], d: EnvCardDeps): string {
  const ordered = groupBySystem(environments);
  return ordered
    .map(([system, envs]) => {
      const heading =
        system && ordered.length > 1
          ? groupHeadHTML({ kicker: t("lib.env_cards.labels.system"), title: system })
          : "";
      return `${heading}<div class="row-list">${envs.map((e) => envCardHTML(e, d)).join("")}</div>`;
    })
    .join("");
}

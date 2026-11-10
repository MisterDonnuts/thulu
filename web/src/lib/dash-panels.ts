import { t } from "../i18n";
import { metricLabel, type CustomMetric } from "./env-cards";
import { buttonHTML, esc, fieldHTML, panelHTML } from "./ui";

export interface ChartType {
  type: string;
  title: string;
  description: string;
  min_series: number;
  max_series: number;
  group_by: "forbidden" | "optional" | "required";
  kinds?: string[];
  time_axis: boolean;
}

export interface ChartCatalog {
  charts: ChartType[];
  aggs_by_kind: Record<string, string[]>;
}

export interface PanelSpec {
  id?: string;
  type: string;
  title: string;
  group_by?: string;
  range_hours?: number;
  width?: number;
  series: Array<{ metric: string; service: string; agg: string; label?: string }>;
}

export function blankPanel(metrics: CustomMetric[]): PanelSpec | null {
  const m = metrics.at(0);
  if (!m) return null;
  return {
    type: "line",
    title: metricLabel(m),
    range_hours: 24,
    width: 1,
    series: [{ metric: m.name, service: m.service, agg: m.kind === "counter" ? "sum" : "last" }],
  };
}

export function chartFits(c: ChartType, m: CustomMetric | undefined): boolean {
  if (!m) return true;
  if (c.kinds?.length && !c.kinds.includes(m.kind)) return false;
  if (c.group_by === "required" && !m.labels.length) return false;
  return true;
}

function field(label: string, id: string, control: string, grow = false): string {
  return fieldHTML({ label, for: id, control, size: "sm", grow });
}

export function panelRowHTML(
  p: PanelSpec,
  i: number,
  metrics: CustomMetric[],
  catalog: ChartCatalog,
): string {
  const spec = catalog.charts.find((c) => c.type === p.type);
  const first = p.series.at(0);
  const metric = metrics.find((m) => m.name === first?.metric && m.service === first.service);
  const multi = p.series.length > 1;

  const typeOpts = catalog.charts
    .filter((c) => c.type === p.type || chartFits(c, metric))
    .map(
      (c) =>
        `<option value="${esc(c.type)}" ${c.type === p.type ? "selected" : ""} title="${esc(c.description)}">${esc(c.title)}</option>`,
    )
    .join("");
  const metricOpts = metrics
    .map((m, mi) => {
      const sel = m.name === first?.metric && m.service === first.service ? "selected" : "";
      return `<option value="${mi}" ${sel}>${esc(m.service)} · ${esc(metricLabel(m))} (${esc(m.kind)})</option>`;
    })
    .join("");
  const aggOpts = (catalog.aggs_by_kind[metric?.kind ?? "counter"] ?? [])
    .map(
      (a) => `<option value="${esc(a)}" ${a === first?.agg ? "selected" : ""}>${esc(a)}</option>`,
    )
    .join("");
  const groupOpts = [`<option value="">${esc(t("lib.dash_panels.options.no_group"))}</option>`]
    .concat(
      (metric?.labels ?? []).map(
        (l) => `<option value="${esc(l)}" ${l === p.group_by ? "selected" : ""}>${esc(l)}</option>`,
      ),
    )
    .join("");

  const head =
    field(
      t("lib.dash_panels.fields.title"),
      `dp-${i}-title`,
      `<input id="dp-${i}-title" type="text" value="${esc(p.title)}" placeholder="${esc(t("lib.dash_panels.labels.title_placeholder"))}" data-dp="${i}" data-dp-field="title" />`,
      true,
    ) +
    field(
      t("lib.dash_panels.fields.type"),
      `dp-${i}-type`,
      `<select id="dp-${i}-type" data-dp="${i}" data-dp-field="type">${typeOpts}</select>`,
    ) +
    buttonHTML({
      iconOnly: true,
      icon: "trash",
      variant: "danger",
      size: "sm",
      label: t("lib.dash_panels.buttons.remove"),
      attrs: { "data-dp-del": i },
    });
  const what = multi
    ? `<span class="muted">${esc(t("lib.dash_panels.labels.multi_series", { count: p.series.length }))}</span>`
    : field(
        t("lib.dash_panels.fields.metric"),
        `dp-${i}-metric`,
        `<select id="dp-${i}-metric" data-dp="${i}" data-dp-field="metric">${metricOpts}</select>`,
        true,
      ) +
      field(
        t("lib.dash_panels.fields.agg"),
        `dp-${i}-agg`,
        `<select id="dp-${i}-agg" data-dp="${i}" data-dp-field="agg">${aggOpts}</select>`,
      );
  const how =
    field(
      t("lib.dash_panels.fields.group_by"),
      `dp-${i}-group_by`,
      `<select id="dp-${i}-group_by" data-dp="${i}" data-dp-field="group_by" ${spec?.group_by === "forbidden" ? "disabled" : ""}
            title="${esc(spec?.group_by === "required" ? t("lib.dash_panels.hints.group_required") : t("lib.dash_panels.hints.group_optional"))}">${groupOpts}</select>`,
    ) +
    field(
      t("lib.dash_panels.fields.range"),
      `dp-${i}-range`,
      `<input id="dp-${i}-range" type="number" min="1" max="2160" value="${p.range_hours ?? 24}" data-dp="${i}" data-dp-field="range_hours" />`,
    ) +
    field(
      t("lib.dash_panels.fields.width"),
      `dp-${i}-width`,
      `<select id="dp-${i}-width" data-dp="${i}" data-dp-field="width">
        <option value="1" ${(p.width ?? 1) === 1 ? "selected" : ""}>${esc(t("lib.dash_panels.options.half_row"))}</option>
        <option value="2" ${p.width === 2 ? "selected" : ""}>${esc(t("lib.dash_panels.options.full_row"))}</option>
      </select>`,
    );
  return panelHTML({
    size: "sm",
    tag: "div",
    bodyHTML:
      `<div class="form-stack">` +
      `<div class="form-row">${head}</div>` +
      `<div class="form-row">${what}${how}</div>` +
      `</div>`,
  });
}

export function applyPanelEdit(
  p: PanelSpec,
  field: string,
  value: string,
  metrics: CustomMetric[],
  catalog: ChartCatalog,
): boolean {
  switch (field) {
    case "title":
      p.title = value;
      return false;
    case "type": {
      p.type = value;
      const spec = catalog.charts.find((c) => c.type === value);
      // The server rejects group_by on a chart that forbids it and requires one where required, so
      // keep it in step with the field the form hides or shows.
      if (spec?.group_by === "forbidden") p.group_by = "";
      if (spec?.group_by === "required" && !p.group_by) {
        const first = p.series.at(0);
        const m = metrics.find((x) => x.name === first?.metric && x.service === first.service);
        p.group_by = m?.labels[0] ?? "";
      }
      return true;
    }
    case "metric": {
      const m = metrics.at(Number(value));
      if (!m) return false;
      p.series[0] = {
        metric: m.name,
        service: m.service,
        agg: (catalog.aggs_by_kind[m.kind] ?? ["sum"])[0],
      };
      if (p.group_by && !m.labels.includes(p.group_by)) p.group_by = "";
      const current = catalog.charts.find((c) => c.type === p.type);
      if (current && !chartFits(current, m)) {
        const next = catalog.charts.find((c) => chartFits(c, m));
        if (next) applyPanelEdit(p, "type", next.type, metrics, catalog);
      }
      return true;
    }
    case "agg":
      if (p.series[0]) p.series[0].agg = value;
      return false;
    case "group_by":
      p.group_by = value;
      return false;
    case "range_hours":
      p.range_hours = Number(value) || 24;
      return false;
    case "width":
      p.width = Number(value) || 1;
      return false;
  }
  return false;
}

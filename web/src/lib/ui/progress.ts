import { t } from "../../i18n";
import { attrsHTML, esc, type Attrs } from "./dom";

export type ProgressSize = "xs" | "sm" | "md";

// progress.css maps every tone on both .seg-progress-seg and .meter-bar;
// scripts/progress-tones.test.mjs checks the lists agree.
export const PROGRESS_TONES = [
  "accent",
  "ok",
  "warn",
  "danger",
  "info",
  "special",
  "muted",
  "faint",
  "track",
  "viz-1",
  "viz-2",
  "viz-3",
  "viz-4",
  "viz-5",
  "viz-6",
  "viz-7",
  "viz-8",
] as const;

export type ProgressTone = (typeof PROGRESS_TONES)[number];

const SIZES = new Set<string>(["xs", "sm", "md"]);

function checkSize(size: string): void {
  if (!SIZES.has(size)) throw new Error(`progress: no size "${size}"`); // i18n-ignore a programming error
}

function pct(n: number): number {
  return Math.round(Math.max(0, Math.min(100, n)) * 100) / 100;
}

export interface ProgressSegment {
  value: number;
  tone: ProgressTone;
  label?: string;
  outline?: boolean;
}

export interface ProgressBarLook {
  size?: ProgressSize;
  split?: boolean;
  cls?: string;
}

export function progressBarClasses(look: ProgressBarLook = {}): string {
  const { size = "sm", split = false, cls } = look;
  checkSize(size);
  return [
    "seg-progress",
    size !== "sm" && `seg-progress-${size}`,
    split && "seg-progress-split",
    cls,
  ]
    .filter(Boolean)
    .join(" ");
}

export interface SegmentPart {
  cls: string;
  tone: ProgressTone;
  style: string;
  title?: string;
}

export function progressSegments(segments: ProgressSegment[], total?: number): SegmentPart[] {
  const sum = segments.reduce((s, g) => s + Math.max(0, g.value), 0);
  const whole = Math.max(total ?? sum, sum);
  if (whole <= 0) return [];
  const parts: SegmentPart[] = segments
    .filter((g) => g.value > 0)
    .map((g) => ({
      cls: g.outline ? "seg-progress-seg seg-progress-outline" : "seg-progress-seg",
      tone: g.tone,
      style: `flex-basis:${pct((g.value / whole) * 100)}%`,
      title: g.label,
    }));
  if (whole > sum) {
    parts.push({
      cls: "seg-progress-seg",
      tone: "track",
      style: `flex-basis:${pct(((whole - sum) / whole) * 100)}%`,
    });
  }
  return parts;
}

export interface ProgressBarOptions extends ProgressBarLook {
  total?: number;
  label?: string;
  decorative?: boolean;
  attrs?: Attrs;
}

export function progressBarA11y(
  segments: ProgressSegment[],
  opts: Pick<ProgressBarOptions, "label" | "decorative"> = {},
): Attrs {
  if (opts.decorative) return { "aria-hidden": "true" };
  const name =
    opts.label ??
    segments
      .filter((g) => g.value > 0 && g.label)
      .map((g) => g.label)
      .join(", ");
  return { role: "img", "aria-label": name || t("ui.progress.none") };
}

export function progressBarHTML(
  segments: ProgressSegment[],
  opts: ProgressBarOptions = {},
): string {
  const attrs = {
    class: progressBarClasses(opts),
    ...progressBarA11y(segments, opts),
    ...opts.attrs,
  };
  const segs = progressSegments(segments, opts.total)
    .map(
      (p) =>
        `<span${attrsHTML({ class: p.cls, "data-tone": p.tone, style: p.style, title: p.title })}></span>`,
    )
    .join("");
  return `<span${attrsHTML(attrs)}>${segs}</span>`;
}

export interface MeterThresholds {
  warn: number;
  danger: number;
}

export const METER_THRESHOLDS: MeterThresholds = { warn: 75, danger: 90 };

export function meterTone(
  value: number,
  thresholds: MeterThresholds = METER_THRESHOLDS,
  base: ProgressTone = "accent",
): ProgressTone {
  if (value >= thresholds.danger) return "danger";
  if (value >= thresholds.warn) return "warn";
  return base;
}

export interface MeterLook {
  size?: ProgressSize;
  inline?: boolean;
  cls?: string;
}

export function meterClasses(look: MeterLook = {}): string {
  const { size = "sm", inline = false, cls } = look;
  checkSize(size);
  return ["meter-bar", size !== "sm" && `meter-bar-${size}`, inline && "meter-bar-inline", cls]
    .filter(Boolean)
    .join(" ");
}

export interface MeterA11yInput {
  value: number;
  label?: string;
  labelledBy?: string;
  valueText?: string;
}

export function meterA11y(input: MeterA11yInput): Attrs {
  const { value, label, labelledBy, valueText } = input;
  if (!label && !labelledBy) throw new Error("meter: a meter needs a label or labelledBy"); // i18n-ignore a programming error
  return {
    role: "meter",
    "aria-valuemin": "0",
    "aria-valuemax": "100",
    "aria-valuenow": String(Math.round(pct(value))),
    "aria-valuetext": valueText,
    "aria-label": label,
    "aria-labelledby": labelledBy,
  };
}

export interface MeterOptions extends MeterLook, Omit<MeterA11yInput, "value"> {
  tone?: ProgressTone;
  thresholds?: MeterThresholds | boolean;
  attrs?: Attrs;
}

export function meterToneOf(
  value: number,
  opts: Pick<MeterOptions, "tone" | "thresholds">,
): ProgressTone {
  const base = opts.tone ?? "accent";
  if (!opts.thresholds) return base;
  const th = opts.thresholds === true ? METER_THRESHOLDS : opts.thresholds;
  return meterTone(value, th, base);
}

export function meterFillStyle(value: number): string {
  // i18n-ignore a CSS declaration, not text
  return `width:${pct(value)}%`;
}

export function meterHTML(value: number, opts: MeterOptions): string {
  const attrs = {
    class: meterClasses(opts),
    "data-tone": meterToneOf(value, opts),
    ...meterA11y({ value, ...opts }),
    ...opts.attrs,
  };
  return `<span${attrsHTML(attrs)}><span class="meter-bar-fill" style="${meterFillStyle(value)}"></span></span>`;
}

export function setMeter(
  el: HTMLElement,
  value: number,
  opts: Pick<MeterOptions, "tone" | "thresholds" | "valueText"> = {},
): void {
  const fill = el.querySelector<HTMLElement>(".meter-bar-fill");
  fill?.setAttribute("style", meterFillStyle(value));
  el.dataset.tone = meterToneOf(value, opts);
  el.setAttribute("aria-valuenow", String(Math.round(pct(value))));
  if (opts.valueText) el.setAttribute("aria-valuetext", opts.valueText);
  else el.removeAttribute("aria-valuetext");
}

export interface ProgressCount {
  done: number;
  total: number;
  segments?: ProgressSegment[];
}

export interface ProgressOptions extends ProgressBarLook {
  text?: "short" | "long";
  attrs?: Attrs;
}

export function progressText(
  p: Pick<ProgressCount, "done" | "total">,
  text: "short" | "long" = "short",
): string {
  return text === "long"
    ? t("ui.progress.long", { done: p.done, total: p.total })
    : t("ui.progress.short", { done: p.done, total: p.total });
}

export interface ProgressTextOptions {
  text?: "short" | "long";
  id?: string;
  cls?: string;
  attrs?: Attrs;
}

export function progressTextHTML(
  p: Pick<ProgressCount, "done" | "total"> | null,
  opts: ProgressTextOptions = {},
): string {
  const attrs = {
    class: ["progress-line-text", opts.cls].filter(Boolean).join(" "),
    id: opts.id,
    ...opts.attrs,
  };
  const text = p && p.total > 0 ? esc(progressText(p, opts.text)) : "";
  return `<span${attrsHTML(attrs)}>${text}</span>`;
}

export function progressHTML(p: ProgressCount, opts: ProgressOptions = {}): string {
  if (p.total <= 0) return "";
  const segments = p.segments ?? [{ value: p.done, tone: "ok" }];
  const bar = progressBarHTML(segments, {
    total: p.total,
    size: opts.size ?? "xs",
    split: opts.split,
    decorative: true,
  });
  const attrs = { class: ["progress-line", opts.cls].filter(Boolean).join(" "), ...opts.attrs };
  return `<span${attrsHTML(attrs)}>${bar}${progressTextHTML(p, { text: opts.text })}</span>`;
}

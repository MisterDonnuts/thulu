// Keep dateInputHTML() in sync with DateInput.astro and .date-input in styles/primitives/form.css.

import { locale, t } from "../../i18n";
import { buttonHTML } from "./button";
import { attrsHTML, type Attrs } from "./dom";
import { setFieldError } from "./form";

export type DateMode = "datetime" | "date" | "time";

const OPTIONS: Record<DateMode, Intl.DateTimeFormatOptions> = {
  datetime: {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  },
  date: { year: "numeric", month: "2-digit", day: "2-digit" },
  time: { hour: "2-digit", minute: "2-digit" },
};

export function nativeDateType(mode: DateMode): "datetime-local" | "date" | "time" {
  return mode === "datetime" ? "datetime-local" : mode;
}

const formats = new Map<string, Intl.DateTimeFormat>();

function dateFormat(mode: DateMode, loc: string): Intl.DateTimeFormat {
  const key = `${loc}|${mode}`;
  let f = formats.get(key);
  if (!f) {
    f = new Intl.DateTimeFormat(loc, OPTIONS[mode]);
    formats.set(key, f);
  }
  return f;
}

type Field = "day" | "month" | "year" | "hour" | "minute";
const FIELDS: readonly string[] = ["day", "month", "year", "hour", "minute"];

// Every field differs so formatToParts reveals the locale's field order; do not change to a date
// with repeated numbers.
const SAMPLE = new Date(2026, 10, 22, 13, 45);

function layout(mode: DateMode, loc: string) {
  const f = dateFormat(mode, loc);
  const order = f
    .formatToParts(SAMPLE)
    .map((p) => p.type)
    .filter((type): type is Field => FIELDS.includes(type));
  const cycle = f.resolvedOptions().hourCycle;
  const h12 = mode !== "date" && (cycle === "h12" || cycle === "h11");
  const period = (h: number) =>
    f.formatToParts(new Date(2026, 0, 5, h, 0)).find((p) => p.type === "dayPeriod")?.value ?? "";
  return { order, h12, am: period(1), pm: period(13) };
}

const pad = (n: number, width = 2) => String(n).padStart(width, "0");

const VALUE: Record<DateMode, RegExp> = {
  datetime: /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})/,
  date: /^(\d{4})-(\d{2})-(\d{2})$/,
  time: /^(\d{2}):(\d{2})/,
};

function dateOfValue(value: string, mode: DateMode): Date | null {
  const m = VALUE[mode].exec(value);
  if (!m) return null;
  const n = m.slice(1).map(Number);
  if (mode === "time") return new Date(2000, 0, 1, n[0], n[1]);
  return new Date(n[0], n[1] - 1, n[2], n[3] ?? 0, n[4] ?? 0);
}

export function formatDateValue(value: string, mode: DateMode = "datetime", loc = locale): string {
  const d = dateOfValue(value, mode);
  return d ? dateFormat(mode, loc).format(d) : "";
}

const flat = (s: string) => s.toLowerCase().replace(/[\s.\u00a0\u202f]/g, "");

export function parseDateText(
  text: string,
  mode: DateMode = "datetime",
  loc = locale,
): string | null {
  const s = text.trim();
  if (!s) return "";
  const nums = s.match(/\d+/g) ?? [];
  const { order, h12, am, pm } = layout(mode, loc);
  const need = mode === "time" ? 1 : 3;
  if (nums.length < need || nums.length > order.length) return null;
  const v: Record<Field, number> = { day: 0, month: 0, year: 0, hour: 0, minute: 0 };
  order.forEach((field, i) => {
    if (i < nums.length) v[field] = Number(nums[i]);
  });
  const yearAt = order.indexOf("year");
  if (yearAt >= 0 && nums[yearAt].length <= 2) v.year += 2000;
  if (h12) {
    const typed = flat(s);
    const isPm = pm !== "" && typed.includes(flat(pm));
    const isAm = am !== "" && typed.includes(flat(am));
    if (isPm || isAm) {
      if (v.hour < 1 || v.hour > 12) return null;
      v.hour = (v.hour % 12) + (isPm ? 12 : 0);
    }
  }
  if (v.hour > 23 || v.minute > 59) return null;
  const time = `${pad(v.hour)}:${pad(v.minute)}`;
  if (mode === "time") return time;
  const d = new Date(v.year, v.month - 1, v.day);
  if (
    v.year < 1000 ||
    v.year > 9999 ||
    d.getFullYear() !== v.year ||
    d.getMonth() !== v.month - 1 ||
    d.getDate() !== v.day
  )
    return null;
  const day = `${pad(v.year, 4)}-${pad(v.month)}-${pad(v.day)}`;
  return mode === "date" ? day : `${day}T${time}`;
}

export function datePlaceholder(mode: DateMode = "datetime", loc = locale): string {
  const letters: Record<Field, string> = {
    day: t("ui.date.day"),
    month: t("ui.date.month"),
    year: t("ui.date.year"),
    hour: t("ui.date.hour"),
    minute: t("ui.date.minute"),
  };
  const { am, pm } = layout(mode, loc);
  return dateFormat(mode, loc)
    .formatToParts(SAMPLE)
    .map((p) => {
      if (FIELDS.includes(p.type)) return letters[p.type as Field];
      return p.type === "dayPeriod" ? `${am}/${pm}` : p.value;
    })
    .join("");
}

export function dateInvalidMessage(mode: DateMode = "datetime", loc = locale): string {
  const key = mode === "time" ? "ui.date.invalid_time" : "ui.date.invalid";
  return t(key, { example: datePlaceholder(mode, loc) });
}

export function datePickLabel(mode: DateMode): string {
  if (mode === "time") return t("ui.date.pick_time");
  if (mode === "date") return t("ui.date.pick_date");
  return t("ui.date.pick_datetime");
}

export interface DateInputOptions {
  id: string;
  mode?: DateMode;
  value?: string;
  name?: string;
  cls?: string;
  attrs?: Attrs;
}

export function dateTextAttrs(o: DateInputOptions): Attrs {
  const mode = o.mode ?? "datetime";
  return {
    id: o.id,
    class: "date-input-text",
    type: "text",
    autocomplete: "off",
    spellcheck: "false",
    placeholder: datePlaceholder(mode),
    value: o.value ? formatDateValue(o.value, mode) : undefined,
  };
}

export const dateErrorId = (id: string): string => `${id}-error`;

export function dateInputHTML(o: DateInputOptions): string {
  const mode = o.mode ?? "datetime";
  const box = { class: ["date-input", o.cls].filter(Boolean).join(" "), "data-date-input": mode };
  return (
    `<span${attrsHTML(box)}>` +
    `<input${attrsHTML(dateTextAttrs(o))}${attrsHTML(o.attrs)}>` +
    `<input${attrsHTML({ class: "date-input-value", type: "hidden", name: o.name, value: o.value ?? "" })}>` +
    `<input${attrsHTML({ class: "date-input-native", type: nativeDateType(mode), tabindex: "-1", "aria-hidden": "true", value: o.value === "" ? undefined : o.value })}>` +
    buttonHTML({
      iconOnly: true,
      icon: mode === "time" ? "clock" : "calendar",
      label: datePickLabel(mode),
      size: "sm",
      variant: "ghost",
      cls: "date-input-pick",
      attrs: { "data-date-pick": "", "aria-controls": o.id },
    }) +
    `</span>` +
    `<p${attrsHTML({ class: "field-error date-input-error", id: dateErrorId(o.id), hidden: true })}></p>`
  );
}

interface DateParts {
  box: HTMLElement;
  text: HTMLInputElement;
  hidden: HTMLInputElement;
  native: HTMLInputElement | null;
  mode: DateMode;
}

function partsOf(target: string | Element | null): DateParts | null {
  const el = typeof target === "string" ? document.getElementById(target) : target;
  const box = el?.closest<HTMLElement>(".date-input");
  const text = box?.querySelector<HTMLInputElement>(".date-input-text");
  const hidden = box?.querySelector<HTMLInputElement>(".date-input-value");
  if (!box || !text || !hidden) return null;
  const mode = (box.dataset.dateInput ?? "datetime") as DateMode;
  return { box, text, hidden, native: box.querySelector(".date-input-native"), mode };
}

function showError(p: DateParts, message: string): void {
  if (p.box.closest(".field")) {
    setFieldError(p.text, message);
    return;
  }
  if (message) p.text.setAttribute("aria-invalid", "true");
  else p.text.removeAttribute("aria-invalid");
}

function sync(p: DateParts, report: boolean): string {
  const v = parseDateText(p.text.value, p.mode);
  p.hidden.value = v ?? "";
  if (p.native) p.native.value = v ?? "";
  const message = v === null ? dateInvalidMessage(p.mode) : "";
  if (!message || report) showError(p, message);
  return message;
}

// "" means empty or not a date; call dateInputError() before acting on the value.
export function readDateInput(target: string | Element | null): string {
  const p = partsOf(target);
  if (!p) return "";
  sync(p, false);
  return p.hidden.value;
}

export function dateInputError(target: string | Element | null): string {
  const p = partsOf(target);
  return p ? sync(p, true) : "";
}

export function dateInputsValid(
  targets: Array<string | Element | null>,
  opts: { focus?: boolean } = {},
): boolean {
  let first: HTMLInputElement | null = null;
  for (const target of targets) {
    const p = partsOf(target);
    if (p && sync(p, true) && !first) first = p.text;
  }
  if (first && opts.focus) first.focus();
  return first === null;
}

export function writeDateInput(target: string | Element | null, value: string): void {
  const p = partsOf(target);
  if (!p) return;
  p.text.value = formatDateValue(value, p.mode);
  sync(p, false);
}

const bound = new WeakSet<Document | HTMLElement>();

export function bindDateInputs(root: Document | HTMLElement = document): void {
  if (bound.has(root)) return;
  bound.add(root);
  root.addEventListener("input", (e) => {
    const el = e.target as Element | null;
    if (el?.classList.contains("date-input-text")) {
      const p = partsOf(el);
      if (p) sync(p, false);
    }
  });
  root.addEventListener("focusout", (e) => {
    const el = e.target as Element | null;
    if (!el?.classList.contains("date-input-text")) return;
    const p = partsOf(el);
    if (!p) return;
    sync(p, true);
    if (p.hidden.value) p.text.value = formatDateValue(p.hidden.value, p.mode);
  });
  const fromNative = (e: Event) => {
    const el = e.target as Element | null;
    if (!el?.classList.contains("date-input-native")) return;
    e.stopPropagation();
    if (e.type !== "change") return;
    const p = partsOf(el);
    if (!p?.native) return;
    writeDateInput(p.text, p.native.value);
    p.text.dispatchEvent(new Event("input", { bubbles: true }));
    p.text.dispatchEvent(new Event("change", { bubbles: true }));
  };
  root.addEventListener("input", fromNative, true);
  root.addEventListener("change", fromNative, true);
  root.addEventListener("click", (e) => {
    const btn = (e.target as Element | null)?.closest<HTMLButtonElement>("[data-date-pick]");
    const p = btn ? partsOf(btn) : null;
    if (!p?.native) return;
    sync(p, false);
    try {
      p.native.showPicker();
    } catch {
      // showPicker() throws on older browsers; fall back to typing in the box.
      p.text.focus();
    }
  });
}

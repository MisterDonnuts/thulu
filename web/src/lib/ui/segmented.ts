import { attrsHTML, esc, type Attrs } from "./dom";
import { icoSvg } from "./icon";

export type SegmentedSize = "sm" | "md";

const SIZES = new Set<string>(["sm", "md"]);

export interface SegmentedOption {
  value: string;
  label: string;
  icon?: string;
  disabled?: boolean;
  attrs?: Attrs;
  inputAttrs?: Attrs;
}

export interface SegmentedLook {
  size?: SegmentedSize;
  iconOnly?: boolean;
  cards?: boolean;
  fill?: boolean;
  cls?: string;
}

export function segmentedClasses(look: SegmentedLook = {}): string {
  const { size = "md", iconOnly, cards, fill, cls } = look;
  if (!SIZES.has(size)) throw new Error(`segmented: no size "${size}"`); // i18n-ignore a programming error
  if (iconOnly && cards) throw new Error("segmented: cards carry words, not icons alone"); // i18n-ignore a programming error
  return [
    "segmented",
    size === "sm" && "segmented-sm",
    iconOnly && "segmented-icons",
    cards && "segmented-cards",
    fill && "segmented-fill",
    cls,
  ]
    .filter(Boolean)
    .join(" ");
}

export function segmentedCheck(options: SegmentedOption[], iconOnly?: boolean): void {
  if (options.length < 2) throw new Error("segmented: a choice needs two options"); // i18n-ignore a programming error
  const seen = new Set<string>();
  for (const o of options) {
    if (seen.has(o.value)) throw new Error(`segmented: value "${o.value}" twice`); // i18n-ignore a programming error
    seen.add(o.value);
    if (iconOnly && !o.icon) throw new Error(`segmented: option "${o.value}" has no icon`); // i18n-ignore a programming error
  }
}

export function segmentedLabelClasses(iconOnly?: boolean): string {
  return iconOnly ? "segmented-label visually-hidden" : "segmented-label";
}

export interface SegmentedOptions extends SegmentedLook {
  label?: string;
  labelledBy?: string;
  describedBy?: string;
  disabled?: boolean;
  id?: string;
  attrs?: Attrs;
}

// With no matching value nothing is checked, so an unknown stored value is not saved back as
// another.
export function segmentedHTML(
  name: string,
  options: SegmentedOption[],
  value?: string,
  o: SegmentedOptions = {},
): string {
  segmentedCheck(options, o.iconOnly);
  const group: Attrs = {
    class: segmentedClasses(o),
    id: o.id,
    role: "radiogroup",
    "aria-label": o.label,
    "aria-labelledby": o.labelledBy,
    "aria-describedby": o.describedBy,
  };
  const items = options
    .map((opt) => {
      const label: Attrs = {
        class: "segmented-opt",
        title: o.iconOnly ? opt.label : undefined,
      };
      const input: Attrs = {
        type: "radio",
        class: "segmented-input",
        name,
        value: opt.value,
        checked: value !== undefined && opt.value === value,
        disabled: o.disabled === true || opt.disabled === true,
      };
      const glyph = opt.icon ? icoSvg(opt.icon, { size: "sm", cls: "segmented-ico" }) : "";
      return (
        `<label${attrsHTML(label)}${attrsHTML(opt.attrs)}>` +
        `<input${attrsHTML(input)}${attrsHTML(opt.inputAttrs)}>${glyph}` +
        `<span class="${segmentedLabelClasses(o.iconOnly)}">${esc(opt.label)}</span></label>`
      );
    })
    .join("");
  return `<div${attrsHTML(group)}${attrsHTML(o.attrs)}>${items}</div>`;
}

export function segmentedEl(
  name: string,
  options: SegmentedOption[],
  value?: string,
  o: SegmentedOptions = {},
): HTMLDivElement {
  const tpl = document.createElement("template");
  tpl.innerHTML = segmentedHTML(name, options, value, o);
  return tpl.content.firstElementChild as HTMLDivElement;
}

const RADIOS = "input.segmented-input";

export function segmentedValue(root: ParentNode): string | null {
  const on = root.querySelector<HTMLInputElement>(`${RADIOS}:checked`);
  return on ? on.value : null;
}

export function setSegmentedValue(root: ParentNode, value: string): boolean {
  for (const input of root.querySelectorAll<HTMLInputElement>(RADIOS)) {
    if (input.value === value) {
      input.checked = true;
      return true;
    }
  }
  return false;
}

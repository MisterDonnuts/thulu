// Folding long blocks of the task view: a clamp for a body of text (a few lines, a fade, and a
// Show more button that exists only when something is really hidden), and the open/close of a
// section or a row whose body is a separate element.

export const CLAMP_LINES = 8;

// Elements a person can tab to. Anything in this list that sits below the cut of a clamped block
// gets `inert`, so Tab never lands on text the person cannot see.
export const FOCUSABLE =
  'a[href], button, input, select, textarea, summary, iframe, audio[controls], video[controls], [contenteditable="true"], [tabindex]:not([tabindex="-1"])'; // i18n-ignore a selector

const INERT_MARK = "data-clamp-inert"; // i18n-ignore an attribute name

export function lineHeightPx(style: Pick<CSSStyleDeclaration, "lineHeight" | "fontSize">): number {
  const size = parseFloat(style.fontSize);
  const font = Number.isFinite(size) && size > 0 ? size : 13;
  const raw = (style.lineHeight || "").trim();
  const n = parseFloat(raw);
  if (!Number.isFinite(n) || n <= 0) return font * 1.5;
  return raw.endsWith("px") ? n : n * font;
}

export function clampLimit(lines: number, lineHeight: number): number {
  return Math.round(lines * lineHeight);
}

// The slack is one line: a button that reveals a single line costs more than the line.
export function overflows(contentHeight: number, limit: number, slack = 0): boolean {
  return contentHeight > limit + slack;
}

export function belowCut(rect: Pick<DOMRect, "bottom">, cut: number): boolean {
  return rect.bottom > cut + 0.5;
}

export function prefersReducedMotion(): boolean {
  try {
    return (
      typeof window.matchMedia === "function" &&
      // i18n-ignore a media query
      window.matchMedia("(prefers-reduced-motion: reduce)").matches
    );
  } catch {
    return false;
  }
}

function motionMs(): number {
  if (prefersReducedMotion()) return 0;
  try {
    const v = getComputedStyle(document.documentElement).getPropertyValue("--dur-normal");
    const ms = parseFloat(v);
    if (!Number.isFinite(ms)) return 0;
    return v.trim().endsWith("ms") ? ms : ms * 1000;
  } catch {
    return 0;
  }
}

// Plays the shared expand motion on a body that just opened. The class comes off when it ends, so
// a later repaint of the same element does not play it again.
export function revealIn(el: HTMLElement | null | undefined): void {
  if (!el || prefersReducedMotion()) return;
  el.classList.remove("expand-in");
  void el.offsetWidth;
  el.classList.add("expand-in");
  el.addEventListener("animationend", () => el.classList.remove("expand-in"), { once: true });
}

// Opens or closes a body controlled by a button with aria-expanded. A closed body is `hidden`, so
// nothing inside it is reachable by Tab.
export function setDisclosed(
  button: Element | null | undefined,
  body: HTMLElement | null | undefined,
  open: boolean,
  opts: { animate?: boolean } = {},
): void {
  if (button) button.setAttribute("aria-expanded", String(open));
  if (!body) return;
  const was = !body.hidden;
  body.hidden = !open;
  if (open && !was && opts.animate) revealIn(body);
}

function setButtonText(button: HTMLElement, text: string): void {
  for (const node of Array.from(button.childNodes)) {
    if (node.nodeType === Node.TEXT_NODE && (node.nodeValue ?? "").trim()) {
      if (node.nodeValue !== text) node.nodeValue = text;
      return;
    }
  }
  button.append(text);
}

export type ClampState = "off" | "shut" | "open";

export interface ClampOptions {
  // The element that clips: it gets data-clamp and the --clamp-h custom property.
  box: HTMLElement;
  // The content inside the box, never height-limited itself, so its height is the full height.
  content: HTMLElement;
  toggle: HTMLButtonElement;
  labels: { more: string; less: string };
  lines?: number;
  expanded?: boolean;
  onToggle?: (expanded: boolean) => void;
}

export interface Clamp {
  readonly expanded: boolean;
  readonly state: ClampState;
  measure(): void;
  setExpanded(open: boolean): void;
  destroy(): void;
}

export function bindClamp(o: ClampOptions): Clamp {
  const lines = o.lines ?? CLAMP_LINES;
  let expanded = !!o.expanded;
  let over = false;
  let limit = 0;
  let frame = 0;

  const state = (): ClampState => (!over ? "off" : expanded ? "open" : "shut");

  const release = () => {
    o.content.querySelectorAll(`[${INERT_MARK}]`).forEach((el) => {
      el.removeAttribute("inert");
      el.removeAttribute(INERT_MARK);
    });
  };

  const shutBelowCut = () => {
    const cut = o.box.getBoundingClientRect().bottom;
    o.content.querySelectorAll<HTMLElement>(FOCUSABLE).forEach((el) => {
      const hide = belowCut(el.getBoundingClientRect(), cut);
      if (hide && !el.hasAttribute("inert")) {
        el.setAttribute("inert", "");
        el.setAttribute(INERT_MARK, "");
      } else if (!hide && el.hasAttribute(INERT_MARK)) {
        el.removeAttribute("inert");
        el.removeAttribute(INERT_MARK);
      }
    });
  };

  const paint = () => {
    const s = state();
    o.box.style.setProperty("--clamp-h", `${limit}px`);
    if (o.box.dataset.clamp !== s) o.box.dataset.clamp = s;
    o.toggle.hidden = !over;
    o.toggle.setAttribute("aria-expanded", String(expanded));
    setButtonText(o.toggle, expanded ? o.labels.less : o.labels.more);
    if (s === "shut") shutBelowCut();
    else release();
  };

  const measure = () => {
    frame = 0;
    const lh = lineHeightPx(getComputedStyle(o.box));
    limit = clampLimit(lines, lh);
    over = overflows(o.content.scrollHeight, limit, lh);
    paint();
  };

  const schedule = () => {
    if (frame) return;
    frame = requestAnimationFrame(measure);
  };

  const animate = (from: number, to: number) => {
    const ms = motionMs();
    if (!ms || typeof o.box.animate !== "function" || from === to) return;
    o.box.animate(
      [
        { height: `${from}px`, overflow: "hidden" },
        { height: `${to}px`, overflow: "hidden" },
      ],
      { duration: ms, easing: "cubic-bezier(0.2, 0.8, 0.2, 1)" }, // i18n-ignore --ease-out
    );
  };

  const setExpanded = (open: boolean) => {
    if (open === expanded) return;
    const from = o.box.getBoundingClientRect().height;
    expanded = open;
    paint();
    if (!over) return;
    animate(from, open ? o.content.scrollHeight : limit);
    if (!open && typeof o.toggle.scrollIntoView === "function") {
      o.toggle.scrollIntoView({ block: "nearest" });
    }
  };

  const onClick = () => {
    setExpanded(!expanded);
    o.onToggle?.(expanded);
  };
  o.toggle.addEventListener("click", onClick);

  const ro = typeof ResizeObserver === "function" ? new ResizeObserver(schedule) : null;
  ro?.observe(o.content);
  ro?.observe(o.box);
  const mo = new MutationObserver(schedule);
  mo.observe(o.content, { childList: true, subtree: true, characterData: true });

  measure();

  return {
    get expanded() {
      return expanded;
    },
    get state() {
      return state();
    },
    measure,
    setExpanded,
    destroy() {
      if (frame) cancelAnimationFrame(frame);
      frame = 0;
      ro?.disconnect();
      mo.disconnect();
      o.toggle.removeEventListener("click", onClick);
      release();
    },
  };
}

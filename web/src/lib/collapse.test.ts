// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  CLAMP_LINES,
  belowCut,
  bindClamp,
  clampLimit,
  lineHeightPx,
  overflows,
  setDisclosed,
  type Clamp,
} from "./collapse";

function one<T extends Element = HTMLElement>(sel: string): T {
  const el = document.querySelector<T>(sel);
  if (!el) throw new Error(`no ${sel}`);
  return el;
}

describe("lineHeightPx", () => {
  it("reads a computed pixel line height as is", () => {
    expect(lineHeightPx({ lineHeight: "20px", fontSize: "13px" })).toBe(20);
  });

  it("multiplies a unitless line height by the font size", () => {
    expect(lineHeightPx({ lineHeight: "1.5", fontSize: "14px" })).toBe(21);
  });

  it("falls back to 1.5 times the font size for normal", () => {
    expect(lineHeightPx({ lineHeight: "normal", fontSize: "16px" })).toBe(24);
    expect(lineHeightPx({ lineHeight: "", fontSize: "" })).toBe(19.5);
  });
});

describe("overflows", () => {
  it("is true only when the content is taller than the limit plus the slack", () => {
    expect(overflows(200, 160, 20)).toBe(true);
    expect(overflows(180, 160, 20)).toBe(false);
    expect(overflows(170, 160, 20)).toBe(false);
    expect(overflows(161, 160)).toBe(true);
  });

  it("limits to the given number of lines", () => {
    expect(clampLimit(CLAMP_LINES, 20)).toBe(CLAMP_LINES * 20);
  });
});

describe("belowCut", () => {
  it("flags an element whose bottom passes the cut", () => {
    expect(belowCut({ bottom: 300 }, 200)).toBe(true);
    expect(belowCut({ bottom: 200 }, 200)).toBe(false);
    expect(belowCut({ bottom: 120 }, 200)).toBe(false);
  });
});

describe("setDisclosed", () => {
  it("syncs aria-expanded with the hidden body", () => {
    document.body.innerHTML = `<button aria-expanded="false"></button><div hidden></div>`;
    const btn = one<HTMLButtonElement>("button");
    const body = one("div");
    setDisclosed(btn, body, true);
    expect(btn.getAttribute("aria-expanded")).toBe("true");
    expect(body.hidden).toBe(false);
    setDisclosed(btn, body, false);
    expect(btn.getAttribute("aria-expanded")).toBe("false");
    expect(body.hidden).toBe(true);
  });
});

describe("bindClamp", () => {
  let box: HTMLElement;
  let content: HTMLElement;
  let toggle: HTMLButtonElement;
  let clamp: Clamp | null = null;
  let height = 0;

  const rect = (top: number, bottom: number) =>
    ({ top, bottom, height: bottom - top, left: 0, right: 0, width: 0, x: 0, y: top }) as DOMRect;

  beforeEach(() => {
    document.body.innerHTML = `
      <div id="box" style="line-height: 20px; font-size: 13px">
        <div id="content"><p>One</p><a href="#a" id="top-link">top</a><a href="#b" id="low-link">low</a></div>
      </div>
      <button id="more" type="button" hidden>Show more</button>`;
    box = one("#box");
    content = one("#content");
    toggle = one<HTMLButtonElement>("#more");
    Object.defineProperty(content, "scrollHeight", { configurable: true, get: () => height });
    box.getBoundingClientRect = () => rect(0, CLAMP_LINES * 20);
    one("#top-link").getBoundingClientRect = () => rect(10, 30);
    one("#low-link").getBoundingClientRect = () => rect(400, 420);
  });

  afterEach(() => {
    clamp?.destroy();
    clamp = null;
    vi.restoreAllMocks();
  });

  const bind = (expanded = false): Clamp => {
    const c = bindClamp({
      box,
      content,
      toggle,
      expanded,
      labels: { more: "Show more", less: "Show less" },
    });
    clamp = c;
    return c;
  };

  it("shows no control when the content fits", () => {
    height = 120;
    expect(bind().state).toBe("off");
    expect(box.dataset.clamp).toBe("off");
    expect(toggle.hidden).toBe(true);
  });

  it("shows no control when only one line would be hidden", () => {
    height = CLAMP_LINES * 20 + 15;
    bind();
    expect(toggle.hidden).toBe(true);
  });

  it("clamps long content by default and offers Show more", () => {
    height = 600;
    expect(bind().state).toBe("shut");
    expect(box.dataset.clamp).toBe("shut");
    expect(box.style.getPropertyValue("--clamp-h")).toBe(`${CLAMP_LINES * 20}px`);
    expect(toggle.hidden).toBe(false);
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toggle.textContent).toBe("Show more");
  });

  it("makes links below the cut unreachable by Tab, and gives them back when opened", () => {
    height = 600;
    const c = bind();
    const low = one("#low-link");
    const top = one("#top-link");
    expect(low.hasAttribute("inert")).toBe(true);
    expect(top.hasAttribute("inert")).toBe(false);
    toggle.click();
    expect(c.expanded).toBe(true);
    expect(box.dataset.clamp).toBe("open");
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(toggle.textContent).toBe("Show less");
    expect(low.hasAttribute("inert")).toBe(false);
    toggle.click();
    expect(low.hasAttribute("inert")).toBe(true);
  });

  it("re-measures: content that shrinks drops the control", () => {
    height = 600;
    const c = bind(true);
    expect(toggle.hidden).toBe(false);
    height = 100;
    c.measure();
    expect(toggle.hidden).toBe(true);
    expect(box.dataset.clamp).toBe("off");
  });

  it("re-measures when the content changes", async () => {
    height = 100;
    bind();
    expect(toggle.hidden).toBe(true);
    height = 900;
    content.insertAdjacentHTML("beforeend", "<p>More text</p>");
    await new Promise((r) => setTimeout(r, 0));
    await new Promise((r) => requestAnimationFrame(() => r(null)));
    expect(toggle.hidden).toBe(false);
  });

  it("stops listening once destroyed", () => {
    height = 600;
    bind().destroy();
    toggle.click();
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(one("#low-link").hasAttribute("inert")).toBe(false);
    clamp = null;
  });
});

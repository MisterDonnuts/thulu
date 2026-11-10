// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import { countClasses, countHidden, countHTML, countText, setCount } from "./count";

describe("countClasses", () => {
  it("writes count for the bare number and count-pill with its size and tone", () => {
    expect(countClasses()).toBe("count");
    expect(countClasses({ cls: "col-count-vis" })).toBe("count col-count-vis");
    expect(countClasses({ pill: true })).toBe("count-pill");
    expect(countClasses({ pill: true, size: "sm", tone: "accent" })).toBe(
      "count-pill count-sm count-accent",
    );
    expect(countClasses({ pill: true, tone: "danger", cls: "x" })).toBe(
      "count-pill count-danger x",
    );
  });

  it("refuses a tone or a size on the bare number, and one that does not exist", () => {
    expect(() => countClasses({ tone: "warn" })).toThrow(/needs pill/);
    expect(() => countClasses({ size: "sm" })).toThrow(/needs pill/);
    expect(() => countClasses({ pill: true, tone: "ok" as never })).toThrow(/no tone "ok"/);
    expect(() => countClasses({ pill: true, size: "lg" as never })).toThrow(/no size "lg"/);
  });
});

describe("countText", () => {
  it("groups the number and caps it at max", () => {
    expect(countText(3)).toBe("3");
    expect(countText(1234)).toBe((1234).toLocaleString("en"));
    expect(countText(99, 99)).toBe("99");
    expect(countText(100, 99)).toBe("99+");
  });

  it("takes a string as it is", () => {
    expect(countText("3 / 5")).toBe("3 / 5");
  });
});

describe("countHTML", () => {
  it("writes the bare number", () => {
    expect(countHTML(3)).toBe('<span class="count">3</span>');
  });

  it("makes the number bold when words follow it in the pill", () => {
    expect(countHTML(2, { pill: true, label: "questions", id: "stat-q" })).toBe(
      '<span id="stat-q" class="count-pill"><strong>2</strong> questions</span>',
    );
  });

  it("puts a dot before the number", () => {
    expect(countHTML("3 / 3", { pill: true, dot: "ok", pulse: true, label: "up" })).toBe(
      '<span class="count-pill"><span class="dot dot-ok dot-pulse" aria-hidden="true"></span><strong>3 / 3</strong> up</span>',
    );
  });

  it("hides at zero when asked, caps at max and names itself for screen readers", () => {
    expect(countHTML(0, { pill: true, size: "sm", tone: "accent", hideZero: true })).toBe(
      '<span class="count-pill count-sm count-accent" hidden>0</span>',
    );
    expect(countHTML(120, { pill: true, max: 99, srLabel: "tasks wait <on> you" })).toBe(
      '<span class="count-pill">99+<span class="visually-hidden"> tasks wait &lt;on&gt; you</span></span>',
    );
  });

  it("escapes the label, the tooltip and a string number", () => {
    expect(countHTML("<1>", { pill: true, label: "<b>", title: '"x"' })).toBe(
      '<span class="count-pill" title="&quot;x&quot;"><strong>&lt;1&gt;</strong> &lt;b&gt;</span>',
    );
  });

  it("refuses a dot or words on the bare number", () => {
    expect(() => countHTML(1, { dot: true })).toThrow(/needs pill/);
    expect(() => countHTML(1, { label: "x" })).toThrow(/needs pill/);
  });
});

describe("countHidden", () => {
  it("hides only zero, and only with hideZero", () => {
    expect(countHidden(0)).toBe(false);
    expect(countHidden(0, true)).toBe(true);
    expect(countHidden("0", true)).toBe(true);
    expect(countHidden(1, true)).toBe(false);
  });
});

describe("setCount", () => {
  it("repaints the number and shows or hides the count, keeping the id", () => {
    const host = document.createElement("div");
    const opts = { pill: true, size: "sm", tone: "accent", max: 99, hideZero: true } as const;
    host.innerHTML = countHTML(0, { ...opts, id: "sb-inbox-badge" });
    const el = host.firstElementChild as HTMLElement;
    expect(el.hidden).toBe(true);

    setCount(el, 150, { ...opts, title: "150 waiting" });
    expect(el.outerHTML).toBe(
      '<span id="sb-inbox-badge" class="count-pill count-sm count-accent" title="150 waiting">99+</span>',
    );

    setCount(el, 0, opts);
    expect(el.hidden).toBe(true);
    expect(el.hasAttribute("title")).toBe(false);
  });
});

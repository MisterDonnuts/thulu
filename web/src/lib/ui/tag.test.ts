import { describe, expect, it } from "vitest";

import {
  CHIP_COLORS,
  chipColor,
  isHexColor,
  priorityLevelLook,
  priorityTagHTML,
  tagClasses,
  tagColorStyle,
  tagDot,
  tagElement,
  tagHTML,
} from "./tag";

describe("chipColor", () => {
  it("hashes a name into a stable bucket", () => {
    expect(chipColor("frontend")).toBe(chipColor("frontend"));
    for (const name of ["", "a", "web-app", "facturación"]) {
      expect(CHIP_COLORS).toContain(chipColor(name));
    }
  });
});

describe("isHexColor", () => {
  it("takes #hex and nothing that could break out of a style", () => {
    expect(isHexColor("#a78bfa")).toBe(true);
    expect(isHexColor("#FFF")).toBe(true);
    expect(isHexColor("red")).toBe(false);
    expect(isHexColor("#fff;background:url(x)")).toBe(false);
    expect(isHexColor(undefined)).toBe(false);
  });
});

describe("tagClasses", () => {
  it("writes tag, the size, the paint, then the modifiers and extras", () => {
    expect(tagClasses()).toBe("tag");
    expect(tagClasses({ size: "sm", color: "blue" })).toBe("tag tag-sm tag-blue");
    expect(tagClasses({ size: "lg", color: "purple" })).toBe("tag tag-lg tag-purple");
    expect(tagClasses({ color: "#a78bfa" })).toBe("tag tag-tint");
    expect(tagClasses({ tone: "warn", dashed: true })).toBe("tag tag-warn tag-dashed");
    expect(tagClasses({ tone: "special", inline: true, cls: "card-agent" })).toBe(
      "tag tag-special tag-inline card-agent",
    );
    expect(tagClasses({ mono: true, removable: true })).toBe("tag tag-mono tag-removable");
  });

  it("leaves a tag grey for a colour that is neither a bucket nor a hex", () => {
    expect(tagClasses({ color: "salmon" })).toBe("tag");
    expect(tagColorStyle("salmon")).toBeUndefined();
  });

  it("refuses a colour and a tone together", () => {
    expect(() => tagClasses({ color: "blue", tone: "warn" })).toThrow(/colour and a tone/);
    expect(tagClasses({ color: "blue", tone: "neutral" })).toBe("tag tag-blue");
  });
});

describe("tagDot", () => {
  it("paints the dot in the tag's colour, a bucket or a hex", () => {
    expect(tagDot(undefined)).toBeNull();
    expect(tagDot(true)).toEqual({ cls: "tag-dot" });
    expect(tagDot("green")).toEqual({ cls: "tag-dot tag-dot-green" });
    expect(tagDot("#10b981")).toEqual({ cls: "tag-dot", style: "--tag-mark:#10b981" });
    expect(tagDot("nope")).toEqual({ cls: "tag-dot" });
  });
});

describe("tagElement", () => {
  it("is a link with href, a button when pressable, a span otherwise", () => {
    expect(tagElement({})).toBe("span");
    expect(tagElement({ href: "/project/?id=1" })).toBe("a");
    expect(tagElement({ button: true })).toBe("button");
    expect(tagElement({ pressed: false })).toBe("button");
  });

  it("refuses an X inside a link or a button", () => {
    expect(() => tagElement({ href: "/x", removable: true })).toThrow(/removable/);
    expect(() => tagElement({ pressed: true, removable: {} })).toThrow(/removable/);
  });
});

describe("tagHTML", () => {
  it("writes a grey span with the label escaped", () => {
    expect(tagHTML("<b>&")).toBe(
      '<span class="tag"><span class="tag-label">&lt;b&gt;&amp;</span></span>',
    );
  });

  it("carries a stage colour as --tag-color with a dot", () => {
    expect(tagHTML("Review", { color: "#a78bfa", dot: true })).toBe(
      '<span class="tag tag-tint" style="--tag-color:#a78bfa"><span class="tag-dot" aria-hidden="true"></span><span class="tag-label">Review</span></span>',
    );
  });

  it("links a repo with its own dot colour and the full name as title", () => {
    expect(tagHTML("web", { dot: "pink", href: "/project/?id=p1", title: "web", size: "sm" })).toBe(
      '<a class="tag tag-sm" href="/project/?id=p1" title="web"><span class="tag-dot tag-dot-pink" aria-hidden="true"></span><span class="tag-label">web</span></a>',
    );
  });

  it("writes a glyph before the label", () => {
    expect(tagHTML("coder", { tone: "special", icon: "i-bot", inline: true })).toBe(
      '<span class="tag tag-special tag-inline"><svg class="ico" aria-hidden="true"><use href="#i-bot"/></svg><span class="tag-label">coder</span></span>',
    );
  });

  it("makes a picker option a pressable button with a check", () => {
    const off = tagHTML("api", { pressed: false, color: "blue", attrs: { "data-id": "p1" } });
    expect(off).toBe(
      '<button class="tag tag-blue" type="button" aria-pressed="false" data-id="p1"><svg class="ico tag-check" aria-hidden="true"><use href="#i-check"/></svg><span class="tag-label">api</span></button>',
    );
    expect(tagHTML("api", { pressed: true })).toContain('aria-pressed="true"');
  });

  it("disables only a button", () => {
    expect(tagHTML("x", { button: true, disabled: true })).toContain(" disabled>");
    expect(tagHTML("x", { disabled: true })).not.toContain("disabled");
  });

  it("gives a removable tag an X named after the label", () => {
    expect(tagHTML("docs", { removable: { attrs: { "data-tag": "docs" } } })).toBe(
      '<span class="tag tag-removable"><span class="tag-label">docs</span><button type="button" class="tag-x" aria-label="Remove docs" data-tag="docs"><svg class="ico" aria-hidden="true"><use href="#i-x"/></svg></button></span>',
    );
    expect(tagHTML("docs", { removable: { label: "Drop" } })).toContain('aria-label="Drop"');
    expect(tagHTML('a"b', { removable: true })).toContain('aria-label="Remove a&quot;b"');
  });

  it("writes the caller's id and attributes", () => {
    expect(
      tagHTML("x", { id: "dr-stage", button: true, attrs: { "aria-haspopup": "listbox" } }),
    ).toBe(
      '<button id="dr-stage" class="tag" type="button" aria-haspopup="listbox"><span class="tag-label">x</span></button>',
    );
  });
});

describe("priorityTagHTML", () => {
  it("maps each level to a tone and a glyph", () => {
    expect(priorityLevelLook("urgent")).toEqual({ tone: "danger", icon: "i-alert" });
    expect(priorityLevelLook("low")).toEqual({ tone: "muted", icon: "i-arrow-down" });
    expect(priorityLevelLook("toString")).toBeNull();
  });

  it("is inline by default and a pill on request", () => {
    expect(priorityTagHTML("High", "high")).toBe(
      '<span class="tag tag-warn tag-inline"><svg class="ico" aria-hidden="true"><use href="#i-arrow-up"/></svg><span class="tag-label">High</span></span>',
    );
    expect(priorityTagHTML("Medium", "medium", { pill: true })).toMatch(/^<span class="tag">/);
    expect(priorityTagHTML("?", "whatever")).toBe("");
  });
});

import { describe, expect, it } from "vitest";

import {
  authorLineHTML,
  avatarA11y,
  avatarClasses,
  avatarHTML,
  avatarMark,
  initialsOf,
} from "./avatar";

describe("initialsOf", () => {
  it("takes two words' first letters or one word's first two", () => {
    expect(initialsOf("Ana Díaz")).toBe("AD");
    expect(initialsOf("ana.diaz@example.com")).toBe("AD");
    expect(initialsOf("ana@example.com")).toBe("AN");
    expect(initialsOf("dev")).toBe("DE");
    expect(initialsOf("x")).toBe("X");
    expect(initialsOf("  ")).toBe("?");
    expect(initialsOf(undefined)).toBe("?");
    expect(initialsOf("élan vital")).toBe("ÉV");
  });
});

describe("avatarClasses", () => {
  it("writes avatar, then size, shape, kind and pending", () => {
    expect(avatarClasses()).toBe("avatar");
    expect(
      avatarClasses({ size: "md", shape: "square", kind: "agent", pending: true, cls: "x" }),
    ).toBe("avatar avatar-md avatar-square avatar-agent avatar-pending x");
    expect(avatarClasses({ kind: "system" })).toBe("avatar avatar-system");
  });

  it("refuses a size or a shape that does not exist", () => {
    // @ts-expect-error: not a size
    expect(() => avatarClasses({ size: "lg" })).toThrow(/no size "lg"/);
    // @ts-expect-error: not a shape
    expect(() => avatarClasses({ shape: "hex" })).toThrow(/no shape "hex"/);
  });
});

describe("avatarMark", () => {
  it("shows letters for people and a glyph for agents, the system and invites", () => {
    expect(avatarMark({ name: "Ana Díaz" })).toEqual({ text: "AD" });
    expect(avatarMark({ name: "dev", kind: "agent" })).toEqual({ icon: "i-bot" });
    expect(avatarMark({ kind: "system" })).toEqual({ icon: "i-octopus" });
    expect(avatarMark({ pending: true, name: "a@b.c" })).toEqual({ icon: "i-send" });
    expect(avatarMark({ name: "dev", kind: "agent", letters: true })).toEqual({ text: "DE" });
    expect(avatarMark({ name: "x", icon: "tt-users" })).toEqual({ icon: "tt-users" });
  });
});

describe("avatarHTML", () => {
  it("is hidden next to the name it stands for", () => {
    expect(avatarHTML({ name: "Ana Díaz" })).toBe(
      '<span class="avatar" aria-hidden="true">AD</span>',
    );
    expect(avatarA11y("Ana")).toEqual({ role: "img", "aria-label": "Ana" });
  });

  it("writes the glyph, the status dot and escapes", () => {
    expect(avatarHTML({ kind: "agent", name: "dev", size: "md" })).toBe(
      '<span class="avatar avatar-md avatar-agent" aria-hidden="true"><svg class="ico ico-sm" aria-hidden="true"><use href="#i-bot"/></svg></span>',
    );
    const chat = avatarHTML({
      kind: "agent",
      name: "<dev>",
      shape: "square",
      letters: true,
      status: "ok",
      pulse: true,
      label: "dev, running",
    });
    expect(chat).toBe(
      '<span class="avatar avatar-square avatar-agent" role="img" aria-label="dev, running">&lt;D' +
        '<span class="avatar-status avatar-status-pulse" data-tone="ok"></span></span>',
    );
  });
});

describe("authorLineHTML", () => {
  const now = Date.parse("2026-10-17T12:00:00Z");

  it("writes name, the time relative with the absolute in the title, and the stage", () => {
    const html = authorLineHTML(
      { name: "Ana", kind: "person", at: "2026-10-17T11:55:00Z", stage: "Review", avatar: true },
      now,
    );
    expect(
      html.startsWith('<div class="author-line"><span class="avatar" aria-hidden="true">AN</span>'),
    ).toBe(true);
    expect(html).toContain('<span class="author-line-name">Ana</span>');
    expect(html).toContain('datetime="2026-10-17T11:55:00Z" data-ts="2026-10-17T11:55:00Z"');
    expect(html).toMatch(/<time class="author-line-time"[^>]* title="[^"]+">5 min/);
    expect(html).toContain('<span class="author-line-in">in Review</span>');
    expect(html).not.toContain("author-line-kind");
  });

  it("says agent after an agent's name, so it does not rest on the avatar's colour", () => {
    const html = authorLineHTML({ name: "dev", kind: "agent" }, now);
    expect(html).toBe(
      '<div class="author-line"><span class="author-line-name">dev</span><span class="author-line-kind">agent</span></div>',
    );
  });

  it("takes avatar options and escapes the name", () => {
    const html = authorLineHTML({ name: "<x>", kind: "person", avatar: { size: "md" } }, now);
    expect(html).toContain('<span class="avatar avatar-md" aria-hidden="true">&lt;X</span>');
    expect(html).toContain('<span class="author-line-name">&lt;x&gt;</span>');
  });
});

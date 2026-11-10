import { describe, expect, it } from "vitest";

import {
  DEFAULT_SECTION,
  SETTINGS_SECTIONS,
  resolveSection,
  sectionDenial,
} from "./settings-sections";

describe("resolveSection", () => {
  it("takes the hash with or without the #", () => {
    expect(resolveSection("#boards", false)).toBe("boards");
    expect(resolveSection("boards", false)).toBe("boards");
  });

  it("falls back to boards for an empty or unknown hash", () => {
    expect(DEFAULT_SECTION).toBe("boards");
    expect(resolveSection("", false)).toBe(DEFAULT_SECTION);
    expect(resolveSection("#nope", false)).toBe(DEFAULT_SECTION);
    expect(resolveSection("#Boards", false)).toBe(DEFAULT_SECTION);
  });

  it("sends the login-gated sections home when the install has no login", () => {
    expect(resolveSection("#team", true)).toBe(DEFAULT_SECTION);
    expect(resolveSection("#account", true)).toBe(DEFAULT_SECTION);
    expect(resolveSection("#team", false)).toBe("team");
  });

  it("leaves the sections that read no user alone", () => {
    for (const key of [
      "boards",
      "llm",
      "api",
      "health",
      "updates",
      "audit",
      "language",
      "webhooks",
    ]) {
      expect(resolveSection(`#${key}`, true)).toBe(key);
    }
  });

  it("gives every section a title and a subtitle", () => {
    for (const [key, s] of Object.entries(SETTINGS_SECTIONS)) {
      expect(s.title, key).toBeTruthy();
      expect(s.subtitle, key).toBeTruthy();
    }
  });
});

describe("sectionDenial", () => {
  it("keeps the admin sections from a developer and a viewer", () => {
    for (const key of ["webhooks", "api", "audit", "updates"]) {
      expect(sectionDenial(key, { role: "developer", operator: false })).toBe("role");
      expect(sectionDenial(key, { role: "viewer" })).toBe("role");
    }
  });

  it("lets an admin in, except to updates outside the operator workspace", () => {
    expect(sectionDenial("audit", { role: "admin", operator: false })).toBeNull();
    expect(sectionDenial("updates", { role: "admin", operator: false })).toBe("operator");
    expect(sectionDenial("updates", { role: "owner", operator: true })).toBeNull();
  });

  it("leaves the sections every member reads alone", () => {
    for (const key of ["boards", "llm", "health", "team", "language", "account"]) {
      expect(sectionDenial(key, { role: "viewer", operator: false })).toBeNull();
    }
  });

  it("lets in a session that does not say, and ignores unknown names", () => {
    expect(sectionDenial("updates", null)).toBeNull();
    expect(sectionDenial("audit", {})).toBeNull();
    expect(sectionDenial("toString", { role: "viewer" })).toBeNull();
  });
});

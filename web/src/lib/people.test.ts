import { describe, expect, it } from "vitest";

import { deviceLabel, memberInitials, memberLabel } from "./people";

describe("memberInitials", () => {
  it("takes the first letter of the first two words of a name", () => {
    expect(memberInitials({ name: "Ana Example" })).toBe("AE");
  });

  it("splits an email on its dot and its at", () => {
    expect(memberInitials({ email: "ana.example@acme.io" })).toBe("AE");
    expect(memberInitials({ email: "ana@acme.io" })).toBe("AA");
  });

  it("prefers the name, then the email, then the id", () => {
    expect(memberInitials({ name: "Ana Example", email: "zz@example.net" })).toBe("AE");
    expect(memberInitials({ user_id: "usr_91ab" })).toBe("US");
  });

  it("takes two letters when there is only one word", () => {
    expect(memberInitials({ name: "ana" })).toBe("AN");
  });

  it("returns empty rather than throwing on an empty member", () => {
    expect(memberInitials({})).toBe("");
  });

  it("names a member by name, then email, then id", () => {
    expect(memberLabel({ name: "", email: "ana@acme.io", user_id: "u1" })).toBe("ana@acme.io");
    expect(memberLabel({ user_id: "u1" })).toBe("u1");
    expect(memberLabel({})).toBe("");
  });
});

describe("deviceLabel", () => {
  const CHROME_MAC =
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36";

  it("names the browser and the OS", () => {
    expect(deviceLabel(CHROME_MAC)).toBe("Chrome on macOS");
  });

  it("does not let Edge and Opera pass as Chrome", () => {
    expect(deviceLabel(CHROME_MAC + " Edg/140.0")).toBe("Edge on macOS");
    expect(deviceLabel(CHROME_MAC + " OPR/120.0")).toBe("Opera on macOS");
  });

  it("does not let Chrome pass as Safari", () => {
    expect(deviceLabel("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Safari/605.1")).toBe(
      "Safari on iOS",
    );
    expect(deviceLabel("Mozilla/5.0 (X11; Linux) Firefox/144.0")).toBe("Firefox on Linux");
  });

  it("reads an iPhone or iPad as iOS, though its user-agent says Mac OS X", () => {
    expect(
      deviceLabel(
        "Mozilla/5.0 (iPad; CPU OS 17_4 like Mac OS X) AppleWebKit/605.1 CriOS/140.0 Safari/604.1",
      ),
    ).toBe("Chrome on iOS");
    expect(
      deviceLabel(
        "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) FxiOS/144.0 Safari/605.1",
      ),
    ).toBe("Firefox on iOS");
    expect(deviceLabel("Mozilla/5.0 (Linux; Android 15) Chrome/140.0 Mobile Safari/537.36")).toBe(
      "Chrome on Android",
    );
  });

  it("says what it knows when it only knows half", () => {
    expect(deviceLabel("Firefox/144.0")).toBe("Firefox");
    expect(deviceLabel("Mozilla/5.0 (Windows NT 10.0)")).toBe("Windows");
  });

  it("reads no user-agent as a program and an unreadable one as unknown", () => {
    expect(deviceLabel("")).toBe("API client");
    expect(deviceLabel("octopus-cli/1.4")).toBe("Unknown device");
  });
});

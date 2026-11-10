import { describe, expect, it } from "vitest";
import { loginBadge, loginMeta, loginWho, type EngineLogin } from "./engine-logins";

const base: EngineLogin = { engine: "claude-code", connected: false, pending: false };

describe("engine login rows", () => {
  it("name who is signed in, email first", () => {
    expect(loginWho({ ...base, email: "ada@example.com", org: "Acme" })).toBe(
      "ada@example.com · Acme",
    );
    expect(loginWho({ ...base, org: "Acme" })).toBe("Acme");
    expect(loginWho(base)).toBe("");
  });

  it("say connected only when the CLI says signed in", () => {
    expect(loginBadge({ ...base, connected: true }).tone).toBe("ok");
    expect(loginBadge({ ...base, pending: true }).tone).toBe("warn");
    expect(loginBadge(base).tone).toBe("muted");
  });

  it("tell whose credential the person's runs use", () => {
    expect(loginMeta({ ...base, connected: true, email: "ada@example.com" })).toContain(
      "ada@example.com",
    );
    expect(loginMeta(base)).not.toContain("@");
  });
});

import { describe, expect, it } from "vitest";
import { loginBadge, loginMeta, loginSteps, loginWho, type EngineLogin } from "./engine-logins";

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

  it("tell preparing apart from waiting for the code", () => {
    const preparing = { ...base, pending: true, phase: "preparing" as const };
    const waiting = { ...base, pending: true, phase: "waiting_code" as const };
    expect(loginBadge(preparing).label).not.toBe(loginBadge(waiting).label);
    expect(loginMeta(preparing, "Claude Code")).toContain("Claude Code");
    expect(loginMeta(waiting)).not.toBe(loginMeta(base));
  });
});

describe("engine login steps", () => {
  it("show the elapsed time while the CLI is prepared, with no link yet", () => {
    const steps = loginSteps("Claude Code", { phase: "preparing" }, 8, false);
    expect(steps.map((s) => s.state)).toEqual(["now", "pending", "pending"]);
    expect(steps[0].detail).toContain("8");
    expect(steps[0].note).toBeTruthy();
    expect(steps[1].action).toBeUndefined();
  });

  it("offer the link once it exists, then ask for the code after it was opened", () => {
    const flow = { phase: "waiting_code" as const, url: "https://claude.ai/oauth/authorize?x=1" };
    const before = loginSteps("Claude Code", flow, 12, false);
    expect(before.map((s) => s.state)).toEqual(["done", "now", "pending"]);
    expect(before[1].action?.href).toBe(flow.url);
    expect(before[1].action?.attrs?.target).toBe("_blank");
    const after = loginSteps("Claude Code", flow, 20, true);
    expect(after.map((s) => s.state)).toEqual(["done", "done", "now"]);
  });

  it("mark the preparation as blocked when it failed", () => {
    const steps = loginSteps("Claude Code", { phase: "failed", error: "no image" }, 3, false);
    expect(steps[0].state).toBe("blocked");
    expect(steps[1].action).toBeUndefined();
  });
});

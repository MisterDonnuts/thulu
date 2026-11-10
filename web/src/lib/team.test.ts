import { describe, expect, it } from "vitest";

import {
  invitationLink,
  licenseNoticeHidden,
  licenseSeatsText,
  liveKeysBy,
  loginConfigLines,
  revokeKeysOption,
  seatsFull,
  seatsText,
  waitingMeta,
  type LoginConfig,
} from "./team";

describe("invitationLink", () => {
  it("opens the login page with the invitation's id", () => {
    expect(invitationLink("https://thulu.acme.io", "ab12 cd")).toBe(
      "https://thulu.acme.io/login/?invite=ab12%20cd",
    );
  });
});

describe("seatsText", () => {
  it("shows used of limit", () => {
    expect(seatsText({ used: 3, limit: 5 })).toBe("3 of 5 seats");
  });

  it("shows nothing without a limit", () => {
    expect(seatsText({ used: 3, limit: 0 })).toBe("");
    expect(seatsText(null)).toBe("");
  });
});

describe("seatsFull", () => {
  it("is full once members and invitations reach the limit", () => {
    expect(seatsFull({ used: 3, limit: 3 })).toBe(true);
    expect(seatsFull({ used: 4, limit: 3 })).toBe(true);
    expect(seatsFull({ used: 2, limit: 3 })).toBe(false);
  });

  it("is never full without a limit or an answer", () => {
    expect(seatsFull({ used: 9, limit: 0 })).toBe(false);
    expect(seatsFull(null)).toBe(false);
  });
});

describe("revokeKeysOption", () => {
  it("offers to revoke the keys the member holds, by count", () => {
    expect(revokeKeysOption(3)).toEqual({
      label: "Also revoke the 3 API keys they created in this workspace",
      hint: "Keys you don't revoke keep working after they leave.",
      name: "revoke_keys",
    });
  });

  it("offers it in general words when the keys could not be listed", () => {
    expect(revokeKeysOption(null)?.label).toBe(
      "Also revoke the API keys they created in this workspace",
    );
  });

  it("offers nothing when there is nothing to revoke", () => {
    expect(revokeKeysOption(0)).toBeUndefined();
  });
});

describe("liveKeysBy", () => {
  const keys = [
    { created_by: "u1" },
    { created_by: "u1", revoked_at: "2026-10-01T00:00:00Z" },
    { created_by: "u2" },
  ];
  it("counts only the member's live keys", () => {
    expect(liveKeysBy(keys, "u1")).toBe(1);
    expect(liveKeysBy(keys, "u3")).toBe(0);
  });
});

describe("waitingMeta", () => {
  it("tells a request from the login page apart from an invitation", () => {
    const base = { email: "a@example.org", workspaces: [], invited_at: "" };
    expect(waitingMeta({ ...base, requested: true, name: "Ana" }, "10/16/2026")).toBe(
      "Ana asked for access on 10/16/2026",
    );
    expect(waitingMeta({ ...base, workspaces: ["Acme", "Beta"] }, "x")).toBe(
      "Invited to Acme, Beta",
    );
  });
});

describe("loginConfigLines", () => {
  const cfg: LoginConfig = {
    oidc: { name: "Example ID", issuer: "https://id.example.com" },
    github: false,
    allowed_domains: ["acme.io"],
    allowed_groups: [],
    groups_claim: "groups",
    signup_mode: "allowlist",
    allowed_emails: ["a@acme.io", "b@acme.io"],
    personal_workspaces: true,
    max_owned_workspaces: 2,
    default_workspace: "",
    default_role: "viewer",
    group_role_map: "",
    autolink: false,
  };

  it("pairs every value with the variable that changes it", () => {
    const lines = loginConfigLines(cfg);
    expect(lines[0]).toEqual({
      label: "Identity provider",
      value: "Example ID (https://id.example.com)",
      env: "OCTOPUS_OIDC_ISSUER, OCTOPUS_GITHUB_CLIENT_ID",
    });
    expect(lines.find((l) => l.env === "OCTOPUS_SSO_ALLOWED_DOMAINS")?.value).toBe("acme.io");
    expect(lines.every((l) => l.env.startsWith("OCTOPUS_"))).toBe(true);
  });

  it("says where a newcomer lands", () => {
    const landing = (c: LoginConfig) =>
      loginConfigLines(c).find((l) => l.env.startsWith("OCTOPUS_SSO_DEFAULT_WORKSPACE"))?.value;
    expect(landing({ ...cfg, default_workspace: "acme", default_role: "developer" })).toBe(
      "Joins acme as developer",
    );
    expect(landing({ ...cfg, personal_workspaces: false })).toBe("Waits until an admin adds them");
  });

  it("counts environment emails only when there are some", () => {
    const signup = (c: LoginConfig) =>
      loginConfigLines(c).find((l) => l.env.startsWith("OCTOPUS_SIGNUP_MODE"))?.value;
    expect(signup(cfg)).toBe("By invitation: the Access list, plus 2 emails from the environment");
    expect(signup({ ...cfg, allowed_emails: [] })).toBe("By invitation: the Access list");
    expect(signup({ ...cfg, signup_mode: "open" })).toBe("Open: anyone the provider authenticates");
  });
});

describe("licenseSeatsText", () => {
  it("says nothing within the free people or the license", () => {
    expect(licenseSeatsText(null)).toBe("");
    expect(licenseSeatsText({ applies: false })).toBe("");
    expect(licenseSeatsText({ applies: true, people: 4, included: 4 })).toBe("");
  });

  it("asks a team over the free size to buy a plan, without a license", () => {
    expect(licenseSeatsText({ applies: true, people: 8, included: 4 })).toBe(
      "Teams of more than 4 people are asked to buy a plan; nothing is limited. This install has 8.",
    );
  });

  it("names the license's people when the install has more", () => {
    expect(licenseSeatsText({ applies: true, people: 14, included: 10, licensed: true })).toBe(
      "This license covers up to 10 people. This install has 14, 4 over.",
    );
  });
});

describe("licenseNoticeHidden", () => {
  it("stays hidden for 30 days after Hide", () => {
    const day = 24 * 60 * 60 * 1000;
    expect(licenseNoticeHidden(0, 10 * day)).toBe(false);
    expect(licenseNoticeHidden(day, 30 * day)).toBe(true);
    expect(licenseNoticeHidden(day, 31 * day)).toBe(false);
  });
});

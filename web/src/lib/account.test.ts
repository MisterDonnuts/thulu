import { describe, expect, it } from "vitest";

import { ApiError } from "./api";
import {
  blockingWorkspaces,
  createLockText,
  emailConfirmed,
  identityProviderNames,
  memberLabel,
  ownedBy,
  canInvite,
  roleLabel,
  slugConfirmed,
  switchTarget,
  transferCandidates,
  workspaceActions,
  workspaceColor,
  workspaceInitial,
  workspaceMeta,
  workspaceNameProblem,
  type Membership,
} from "./account";

const ws = (id: string, owner: string, extra: Partial<Membership> = {}): Membership => ({
  id,
  name: id,
  slug: id,
  owner_id: owner,
  created_at: "2026-10-01T00:00:00Z",
  role: owner === "me" ? "owner" : "developer",
  member_count: 1,
  current: false,
  ...extra,
});

describe("switchTarget", () => {
  it("keeps the section and drops what named the old workspace's things", () => {
    expect(switchTarget("/tasks/")).toBe("/tasks/");
    expect(switchTarget("/settings/")).toBe("/settings/");
    expect(switchTarget("")).toBe("/");
  });

  it("sends a page about one project back to the list", () => {
    expect(switchTarget("/project/")).toBe("/projects/");
    expect(switchTarget("/project")).toBe("/projects/");
    expect(switchTarget("/projects/")).toBe("/projects/");
  });
});

describe("workspaceInitial and workspaceColor", () => {
  it("takes the first letter, whole even when it is not ASCII", () => {
    expect(workspaceInitial("  acme")).toBe("A");
    expect(workspaceInitial("ñandú")).toBe("Ñ");
    expect(workspaceInitial("   ")).toBe("?");
  });

  it("picks the same chart colour for the same id", () => {
    expect(workspaceColor("0000000000000a02")).toBe(workspaceColor("0000000000000a02"));
    expect(workspaceColor("abc")).toMatch(/^var\(--viz-[1-8]\)$/);
  });
});

describe("canInvite", () => {
  it("lets owners and admins invite", () => {
    expect(canInvite("owner")).toBe(true);
    expect(canInvite("admin")).toBe(true);
    expect(canInvite("developer")).toBe(false);
    expect(canInvite(undefined)).toBe(false);
  });
});

describe("emailConfirmed", () => {
  it("ignores case and surrounding spaces, like the daemon", () => {
    expect(emailConfirmed("  Ana@Example.com ", "ana@example.com")).toBe(true);
  });

  it("refuses anything else, including an empty account email", () => {
    expect(emailConfirmed("ana@example.net", "ana@example.com")).toBe(false);
    expect(emailConfirmed("ana", "ana@example.com")).toBe(false);
    expect(emailConfirmed("", "")).toBe(false);
  });
});

describe("slugConfirmed", () => {
  it("wants the slug exactly, spaces around it aside", () => {
    expect(slugConfirmed(" acme ", "acme")).toBe(true);
    expect(slugConfirmed("ACME", "acme")).toBe(false);
    expect(slugConfirmed("", "")).toBe(false);
  });
});

describe("ownedBy and transferCandidates", () => {
  it("keeps only the workspaces the account owns", () => {
    const list = [ws("a", "me"), ws("b", "bob"), ws("c", "me")];
    expect(ownedBy(list, "me").map((m) => m.id)).toEqual(["a", "c"]);
  });

  it("never offers the owner as the new owner", () => {
    const members = [
      { user_id: "me", role: "owner" },
      { user_id: "bob", role: "admin" },
    ];
    expect(transferCandidates(members, "me").map((m) => m.user_id)).toEqual(["bob"]);
  });
});

describe("blockingWorkspaces", () => {
  it("reads the workspaces a refused deletion names", () => {
    const err = new ApiError(409, "you own a workspace other people use", {
      error: "…",
      code: "auth.account.errors.owns_shared_workspaces",
      workspaces: [{ id: "a", name: "A", slug: "a", member_count: 3 }],
    });
    expect(blockingWorkspaces(err)).toEqual([{ id: "a", name: "A", slug: "a", member_count: 3 }]);
  });

  it("finds none in any other failure", () => {
    expect(blockingWorkspaces(new ApiError(400, "confirmation required", {}))).toEqual([]);
    expect(blockingWorkspaces(new ApiError(409, "conflict", { workspaces: "a" }))).toEqual([]);
    expect(blockingWorkspaces(new Error("offline"))).toEqual([]);
  });
});

describe("labels", () => {
  it("names a member by name and email when both differ", () => {
    expect(
      memberLabel({ user_id: "u1", name: "Ana", email: "ana@example.com", role: "admin" }),
    ).toBe("Ana (ana@example.com)");
    expect(memberLabel({ user_id: "u1", email: "ana@example.com", role: "admin" })).toBe(
      "ana@example.com",
    );
    expect(memberLabel({ user_id: "u1", role: "admin" })).toBe("u1");
  });

  it("words the known roles and passes others through", () => {
    expect(roleLabel("owner")).toBe("owner");
    expect(roleLabel("auditor")).toBe("auditor");
  });
});

describe("identityProviderNames", () => {
  const providers = [
    { key: "google", name: "Google" },
    { key: "example", name: " Example ID " },
  ];
  it("names each linked provider the way the sign-in page does", () => {
    expect(identityProviderNames(["example", "google"], providers)).toBe("Example ID, Google");
  });
  it("falls back to the key for a provider the install dropped", () => {
    expect(identityProviderNames(["okta"], providers)).toBe("okta");
  });
  it("is empty for an account with no provider link", () => {
    expect(identityProviderNames([], providers)).toBe("");
  });
});

describe("workspaceNameProblem", () => {
  it("takes a name with letters in it, trimmed", () => {
    expect(workspaceNameProblem("  Acme Web ")).toBe("");
  });

  it("refuses an empty name and one past the daemon's limit", () => {
    expect(workspaceNameProblem("   ")).toBe("Write a name.");
    expect(workspaceNameProblem("x".repeat(81))).toBe("The name can have at most 80 characters.");
  });

  it("counts characters, not bytes", () => {
    expect(workspaceNameProblem("ñ".repeat(80))).toBe("");
  });
});

describe("createLockText", () => {
  it("names the cap with its number", () => {
    expect(createLockText({ create_workspace_reason: "cap", create_workspace_max: 2 })).toBe(
      "You already own 2 workspaces, the most one account can own here.",
    );
  });

  it("says when only the operator creates workspaces", () => {
    expect(createLockText({ create_workspace_reason: "reserved" })).toMatch(/run this install/);
  });

  it("falls back to the general sentence for a daemon that does not say why", () => {
    expect(createLockText({})).toBe(createLockText({ create_workspace_reason: "" }));
  });
});

describe("workspaceActions", () => {
  it("offers a switch away, and deletes only the owned workspace the session is in", () => {
    expect(workspaceActions(ws("a", "me"), true)).toEqual(["switch", "rename", "transfer"]);
    expect(workspaceActions(ws("a", "me", { current: true }), true)).toEqual([
      "rename",
      "transfer",
      "delete",
    ]);
  });

  it("lets an admin rename and nothing more, and a developer only switch", () => {
    expect(workspaceActions(ws("b", "x", { role: "admin", current: true }), false)).toEqual([
      "rename",
    ]);
    expect(workspaceActions(ws("b", "x"), false)).toEqual(["switch"]);
  });
});

describe("workspaceMeta", () => {
  it("names the slug, the role, the members and whether it is the current one", () => {
    const line = workspaceMeta(ws("acme", "x", { member_count: 3, current: true }));
    expect(line.startsWith(`acme · ${roleLabel("developer")} · `)).toBe(true);
    expect(line).toContain("3");
    expect(line.endsWith(" · current")).toBe(true);
    expect(workspaceMeta(ws("acme", "x"))).not.toContain("current");
  });
});

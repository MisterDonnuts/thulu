import { describe, expect, it } from "vitest";

import {
  PRESETS,
  ROTATE_GRACE_HOURS,
  defaultKeyDays,
  expiresIn,
  expiryHint,
  expiryProblem,
  graceLabel,
  grantedLine,
  keyActions,
  keyDetails,
  keyExpiry,
  keyState,
  maxKeyDays,
  permissionSummary,
  permissionChoices,
  presetSelection,
  roleLabel,
  roleWithin,
  rotatedLine,
} from "./api-keys";

describe("permissionChoices", () => {
  it("gives every resource a read and a write box, then the extras", () => {
    const choices = permissionChoices();
    expect(choices.slice(0, 2).map((c) => c.value)).toEqual(["tasks:read", "tasks:write"]);
    expect(choices.at(-1)?.value).toBe("internal:use");
    expect(choices).toHaveLength(8 * 2 + 2);
  });

  it("offers the member-management pair like any other resource", () => {
    const values = permissionChoices().map((c) => c.value);
    expect(values).toContain("members:read");
    expect(values).toContain("members:write");
  });

  it("only puts a hint on the boxes that need explaining", () => {
    const byValue = Object.fromEntries(permissionChoices().map((c) => [c.value, c.hint]));
    expect(byValue["tasks:read"]).toBe("");
    expect(byValue["keys:write"]).toBe("Let this key create more keys");
  });
});

describe("presetSelection", () => {
  const values = permissionChoices().map((c) => c.value);

  it("ticks exactly what the preset lists", () => {
    expect(presetSelection("ci", values)).toEqual(PRESETS.ci);
  });

  it("ticks every box for the wildcard preset", () => {
    expect(presetSelection("full", values)).toEqual(values);
  });

  it("ticks nothing for a preset that does not exist", () => {
    expect(presetSelection("", values)).toEqual([]);
    expect(presetSelection("root", values)).toEqual([]);
  });

  it("lets a read-only key list members, as the daemon's preset does", () => {
    expect(presetSelection("read-only", values)).toContain("members:read");
    expect(presetSelection("read-only", values)).not.toContain("members:write");
  });

  it("ignores a permission no box on screen offers", () => {
    expect(presetSelection("ci", ["tasks:read"])).toEqual(["tasks:read"]);
  });
});

describe("grantedLine", () => {
  it("reports the role, the permissions and the lifetime, in words", () => {
    expect(grantedLine("admin", ["tasks:read", "tasks:write"])).toBe(
      "admin in this workspace · Read: Tasks · Write: Tasks · never expires.",
    );
  });

  it("says nothing rather than leaving the middle blank", () => {
    expect(grantedLine(undefined, [])).toBe(
      "developer in this workspace · nothing · never expires.",
    );
  });

  it("lowercases the expiry so it reads as one sentence", () => {
    expect(grantedLine("owner", ["*"], "2030-01-02T00:00:00Z")).toContain("· expires ");
  });
});

describe("roleWithin", () => {
  it("allows the caller's own role and anything below it", () => {
    expect(roleWithin("admin", "admin")).toBe(true);
    expect(roleWithin("viewer", "admin")).toBe(true);
    expect(roleWithin("owner", "admin")).toBe(false);
  });

  it("leaves an unknown caller to the server", () => {
    expect(roleWithin("owner", undefined)).toBe(true);
  });
});

describe("key expiry", () => {
  const day = 86_400;

  it("reads the cap in whole days, and no cap as null", () => {
    expect(maxKeyDays(90 * day)).toBe(90);
    expect(maxKeyDays(36 * 3600)).toBe(1);
    expect(maxKeyDays(3600)).toBe(0);
    expect(maxKeyDays(0)).toBeNull();
    expect(maxKeyDays(undefined)).toBeNull();
  });

  it("starts the field at the cap, blank without one", () => {
    expect(defaultKeyDays(90)).toBe("90");
    expect(defaultKeyDays(null)).toBe("");
    expect(defaultKeyDays(0)).toBe("");
  });

  it("accepts blank and anything up to the cap", () => {
    expect(expiryProblem("", 90)).toBeNull();
    expect(expiryProblem("90", 90)).toBeNull();
    expect(expiryProblem("400", null)).toBeNull();
  });

  it("refuses a lifetime past the cap, naming it", () => {
    expect(expiryProblem("91", 90)).toBe(
      "This install caps API keys at 90 days (OCTOPUS_API_KEY_MAX_TTL).",
    );
    expect(expiryProblem("1", 0)).toMatch(/leave the expiry blank/);
  });

  it("refuses what is not a whole number of days", () => {
    expect(expiryProblem("0", 90)).toBe("Expiry must be a whole number of days.");
    expect(expiryProblem("1.5", 90)).toBe("Expiry must be a whole number of days.");
    expect(expiryProblem("soon", 90)).toBe("Expiry must be a whole number of days.");
  });

  it("says what blank means on this install", () => {
    expect(expiryHint(90)).toBe(
      "Keys on this install live at most 90 days; blank means the maximum.",
    );
    expect(expiryHint(null)).toBe(
      "This install sets no cap (OCTOPUS_API_KEY_MAX_TTL=0): leave the expiry blank for a key that never expires.",
    );
    expect(expiryHint(null, false)).toBe(
      "Leave it blank for the install's maximum (90 days unless OCTOPUS_API_KEY_MAX_TTL says otherwise).",
    );
  });
});

describe("permissionSummary", () => {
  it("groups the boxes by verb in the form's words", () => {
    expect(permissionSummary(["tasks:read", "projects:read", "tasks:write", "keys:write"])).toBe(
      "Read: Tasks, Projects · Write: Tasks · Mint keys",
    );
  });

  it("says everything for the wildcard and nothing for an empty grant", () => {
    expect(permissionSummary(["*"])).toBe("Everything");
    expect(permissionSummary([])).toBe("no permissions");
  });

  it("keeps a permission the catalog does not know, as stored", () => {
    expect(permissionSummary(["runs:read", "billing:write"])).toBe(
      "Read: Runs & usage · billing:write",
    );
  });
});

describe("keyExpiry", () => {
  const now = new Date("2026-10-16T12:00:00Z");
  it("tells a dead key from a live one", () => {
    expect(keyExpiry("2026-10-01T00:00:00Z", now)).toMatchObject({ expired: true });
    expect(keyExpiry("2026-10-01T00:00:00Z", now).text).toMatch(/^Expired on /);
    expect(keyExpiry("2026-11-01T00:00:00Z", now)).toMatchObject({ expired: false });
    expect(keyExpiry("2026-11-01T00:00:00Z", now).text).toMatch(/^Expires /);
    expect(keyExpiry("2026-11-01T12:00:00Z", now).text).toContain("Nov 1, 2026");
    expect(keyExpiry(undefined, now)).toEqual({ expired: false, text: "No expiry" });
  });
});

describe("keyDetails", () => {
  it("puts role, grant, expiry, creator and last use on one line", () => {
    const line = keyDetails(
      {
        role: "viewer",
        permissions: ["tasks:read"],
        created_by_email: "ana@example.com",
      },
      new Date("2026-10-16T12:00:00Z"),
    );
    expect(line).toBe(
      "viewer · Read: Tasks · No expiry · minted by ana@example.com · last used: never",
    );
  });
});

describe("keyState", () => {
  const now = new Date("2026-10-16T12:00:00Z");
  const soon = "2026-10-17T12:00:00Z";
  const past = "2026-10-15T12:00:00Z";
  it("tells a rotated key in its grace from its replacement", () => {
    expect(keyState({ expires_at: soon, replaced_by: "new" }, now)).toBe("grace");
    expect(keyState({ expires_at: soon }, now)).toBe("live");
  });

  it("tells a key a rotation ended from one that reached its date", () => {
    expect(keyState({ expires_at: past, replaced_by: "new" }, now)).toBe("replaced");
    expect(keyState({ expires_at: past }, now)).toBe("expired");
  });

  it("puts a revoke and a task scope first", () => {
    expect(keyState({ revoked_at: past, replaced_by: "new" }, now)).toBe("revoked");
    expect(keyState({ scope_task_id: "t1" }, now)).toBe("task_scoped");
  });

  it("offers rotate only on a live key and revoke only on a working one", () => {
    expect(keyActions("live")).toEqual({ rotate: true, revoke: true });
    expect(keyActions("grace")).toEqual({ rotate: false, revoke: true });
    for (const s of ["replaced", "expired", "revoked", "task_scoped"] as const)
      expect(keyActions(s)).toEqual({ rotate: false, revoke: false });
  });

  it("says until when a key in its grace works, and when a replaced one stopped", () => {
    const grace = keyDetails({ expires_at: soon, replaced_by: "new" }, now);
    expect(grace).toContain("Works until ");
    const ended = keyDetails({ expires_at: past, replaced_by: "new" }, now);
    expect(ended).toContain("Stopped working on ");
    expect(ended).toContain("when it was rotated");
    expect(ended).not.toContain("Expired");
  });
});

describe("roleLabel", () => {
  it("translates the four roles and passes others through", () => {
    expect(roleLabel("developer")).toBe("developer");
    expect(roleLabel("auditor")).toBe("auditor");
    expect(roleLabel(undefined)).toBe("");
  });
});

describe("rotation", () => {
  it("labels each grace", () => {
    expect(ROTATE_GRACE_HOURS.map(graceLabel)).toEqual([
      "Ends now",
      "1 hour",
      "1 day",
      "3 days",
      "7 days",
    ]);
  });

  it("says whether the old key still works", () => {
    const now = new Date("2026-10-16T12:00:00Z");
    expect(rotatedLine(undefined, now)).toBe("The old key no longer works.");
    expect(rotatedLine("2026-10-16T11:59:00Z", now)).toBe("The old key no longer works.");
    expect(rotatedLine("2026-10-17T12:00:00Z", now)).toMatch(/^The old key works until /);
  });
});

describe("expiresIn", () => {
  const now = new Date("2026-10-16T12:00:00Z");
  it("counts down in the coarsest unit that reads well", () => {
    expect(expiresIn("2026-10-16T12:45:00Z", now)).toBe("expires in 45 min.");
    expect(expiresIn("2026-10-16T15:00:00Z", now)).toBe("expires in 3 hr.");
    expect(expiresIn("2026-10-20T12:00:00Z", now)).toBe("expires in 4 days");
    expect(expiresIn("2026-10-16T11:00:00Z", now)).toBe("expired");
  });
});

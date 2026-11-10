import { describe, expect, it } from "vitest";
import type { Credential } from "./credentials";
import {
  CLOUD_PROVIDERS,
  NEW_CRED,
  credLabel,
  credentialForEnv,
  envErrorField,
  formatSelectors,
  grantsWith,
  grantsWithout,
  parseSelectors,
} from "./env-form";

function cred(over: Partial<Credential> = {}): Credential {
  return { id: "c1", name: "prod-aws", provider: "aws", kind: "keys", state: "valid", ...over };
}

describe("parseSelectors", () => {
  it("reads one key=value per line", () => {
    expect(parseSelectors("tag:Environment=prod\nlog_group=/aws/lambda/api")).toEqual({
      "tag:Environment": "prod",
      log_group: "/aws/lambda/api",
    });
  });
  it("ignores blank lines, so a trailing newline makes no empty key", () => {
    expect(parseSelectors("a=1\n\n  \n")).toEqual({ a: "1" });
  });
  it("skips a line with no separator and one that starts with it", () => {
    expect(parseSelectors("nonsense\n=orphan\nb=2")).toEqual({ b: "2" });
  });
  it("keeps the value's own equals signs", () => {
    expect(parseSelectors("filter=a=b")).toEqual({ filter: "a=b" });
  });
  it("trims around the separator", () => {
    expect(parseSelectors("  a  =  1  ")).toEqual({ a: "1" });
  });
});

describe("formatSelectors", () => {
  it("round-trips through parseSelectors", () => {
    const m = { a: "1", b: "2" };
    expect(parseSelectors(formatSelectors(m))).toEqual(m);
  });
  it("renders nothing for an absent map", () => {
    expect(formatSelectors(undefined)).toBe("");
    expect(formatSelectors({})).toBe("");
  });
});

describe("credentialForEnv", () => {
  const granted = cred({
    id: "c1",
    grants: [{ subject_type: "environment", subject_id: "e1" }],
  });
  const elsewhere = cred({
    id: "c2",
    grants: [{ subject_type: "project", subject_id: "e1" }],
  });

  it("finds the credential granted to that environment", () => {
    expect(credentialForEnv([elsewhere, granted], "e1")?.id).toBe("c1");
  });
  it("ignores a grant of the same id under another subject type", () => {
    expect(credentialForEnv([elsewhere], "e1")).toBeUndefined();
  });
  it("copes with a credential that has no grants at all", () => {
    expect(credentialForEnv([cred()], "e1")).toBeUndefined();
  });
});

describe("credLabel", () => {
  it("names the provider", () => {
    expect(credLabel(cred())).toBe("prod-aws — aws");
  });
  it("says so when the key is switched off, before anything else", () => {
    expect(credLabel(cred({ disabled: true, state: "invalid" }))).toBe(
      "prod-aws — aws, switched off",
    );
  });
  it("surfaces a bad state", () => {
    expect(credLabel(cred({ state: "expired" }))).toBe("prod-aws — aws, expired");
  });
});

describe("grantsWithout / grantsWith", () => {
  const c = cred({
    grants: [
      { subject_type: "environment", subject_id: "e1" },
      { subject_type: "environment", subject_id: "e2" },
      { subject_type: "project", subject_id: "e1" },
    ],
  });

  it("removes only this environment's grant", () => {
    expect(grantsWithout(c, "e1")).toEqual([
      { subject_type: "environment", subject_id: "e2" },
      { subject_type: "project", subject_id: "e1" },
    ]);
  });
  it("appends without touching the existing grants", () => {
    expect(grantsWith(cred(), "e9")).toEqual([{ subject_type: "environment", subject_id: "e9" }]);
    expect(grantsWith(undefined, "e9")).toHaveLength(1);
    expect(grantsWith(c, "e9")).toHaveLength(4);
  });
});

describe("constants", () => {
  it("offers credentials only where the daemon reads them", () => {
    expect([...CLOUD_PROVIDERS].sort()).toEqual(["aws", "gcp"]);
    expect(CLOUD_PROVIDERS.has("prometheus")).toBe(false);
  });
  it("uses a sentinel no credential id can be", () => {
    expect(NEW_CRED).toBe("__new__");
  });
});

describe("envErrorField", () => {
  it("names the control a refusal of one field is about", () => {
    expect(envErrorField({ error: "…", code: "project.errors.env_name_invalid" })).toBe("env-name");
    expect(envErrorField({ code: "project.errors.env_system_invalid" })).toBe("env-system");
    expect(envErrorField({ code: "project.errors.env_url_invalid" })).toBe("env-url");
    expect(envErrorField({ code: "project.errors.env_selector_key_blank" })).toBe("env-selectors");
  });
  it("leaves any other refusal to the whole form", () => {
    expect(envErrorField({ code: "project.errors.env_duplicate" })).toBeUndefined();
    expect(envErrorField({ code: "env_name_invalid" })).toBeUndefined();
    expect(envErrorField({ code: "project.errors.constructor" })).toBeUndefined();
    expect(envErrorField({ error: "boom" })).toBeUndefined();
    expect(envErrorField(undefined)).toBeUndefined();
    expect(envErrorField("text")).toBeUndefined();
  });
});

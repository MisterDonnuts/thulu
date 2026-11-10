import { describe, expect, it } from "vitest";

import { contactHref, providerNames } from "./legal";

describe("contactHref", () => {
  it("links an email address as mail", () => {
    expect(contactHref(" privacy@example.com ")).toBe("mailto:privacy@example.com");
  });

  it("links an https page as itself", () => {
    expect(contactHref("https://example.com/privacy")).toBe("https://example.com/privacy");
  });

  it("links nothing else, a script URL least of all", () => {
    expect(contactHref("javascript:alert(1)")).toBeNull();
    expect(contactHref("http://example.com")).toBeNull();
    expect(contactHref("Calle Falsa 123, Buenos Aires")).toBeNull();
    expect(contactHref("")).toBeNull();
  });
});

describe("providerNames", () => {
  it("joins the configured providers' names", () => {
    expect(
      providerNames({
        sso_providers: [
          { key: "oidc", name: "Example ID" },
          { key: "github", name: "GitHub" },
        ],
      }),
    ).toBe("Example ID, GitHub");
  });

  it("is empty without providers", () => {
    expect(providerNames({})).toBe("");
  });
});

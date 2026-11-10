import { describe, expect, it } from "vitest";
import { personName } from "./person";

describe("personName", () => {
  it("never shows the raw dev-mode signature", () => {
    expect(personName("user")).toBe("Someone");
    expect(personName("ana@example.com")).toBe("ana@example.com");
  });
});

import { describe, expect, it } from "vitest";
import { chatTitle } from "./data";

describe("chatTitle", () => {
  it("drops the agent prefix of a title the chat was opened with", () => {
    expect(chatTitle({ title: "Thulu-manager · ABC-123: fix", agent_type: "thulu-manager" })).toBe(
      "ABC-123: fix",
    );
  });

  it("keeps a title the first message or a person gave", () => {
    expect(chatTitle({ title: "Why ABC-123 came back", agent_type: "thulu-manager" })).toBe(
      "Why ABC-123 came back",
    );
  });

  it("names the agent when there is nothing else", () => {
    expect(chatTitle({ title: "", agent_type: "thulu-manager" })).toBe("Thulu-manager");
    expect(chatTitle({ title: "Thulu-manager", agent_type: "thulu-manager" })).toBe(
      "Thulu-manager",
    );
  });
});

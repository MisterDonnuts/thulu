import { describe, expect, it } from "vitest";
import { t } from "../i18n";
import { firstSentence, serverDescription, toolDescription } from "./mcp-tool-text";

describe("toolDescription", () => {
  it("uses the catalog for a tool Thulu ships", () => {
    expect(toolDescription("thulu-github", "pr_merge", "Merge a pull request.")).toBe(
      t("lib.mcp_tool_text.thulu_github.pr_merge"),
    );
  });

  it("falls back to the server's text for anything else", () => {
    expect(toolDescription("thulu-github", "pr_new_thing", "From the server.")).toBe(
      "From the server.",
    );
    expect(toolDescription("ticket-reader", "ticket_get", "Reads a ticket.")).toBe(
      "Reads a ticket.",
    );
  });
});

describe("serverDescription", () => {
  it("uses the catalog for a server Thulu ships", () => {
    expect(serverDescription("thulu-cloud", "Read-only window onto your")).toBe(
      t("lib.mcp_tool_text.server.thulu_cloud"),
    );
  });

  it("keeps the first whole sentence of any other server's text", () => {
    expect(
      serverDescription("ticket-reader", "Reads tickets from the\nhelpdesk. Needs a token."),
    ).toBe("Reads tickets from the helpdesk.");
  });
});

describe("firstSentence", () => {
  it("keeps a text with no sentence end whole", () => {
    expect(firstSentence("  one line\nand more ")).toBe("one line and more");
  });

  it("does not cut at a dot inside a word", () => {
    expect(firstSentence("Talks to api.example.com for you. Then more.")).toBe(
      "Talks to api.example.com for you.",
    );
  });
});

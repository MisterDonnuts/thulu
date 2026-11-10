import { describe, expect, it } from "vitest";

import { qaRedirectTarget } from "./qa-redirect";

describe("qaRedirectTarget", () => {
  it("lands on the From tasks tab", () => {
    expect(qaRedirectTarget("")).toBe("/evidence/?tab=tasks");
  });

  it("keeps the filters and the hash", () => {
    expect(qaRedirectTarget("?status=fail,blocked&q=log+in", "#x")).toBe(
      "/evidence/?tab=tasks&status=fail%2Cblocked&q=log+in#x",
    );
  });

  it("does not let a tab parameter win", () => {
    expect(qaRedirectTarget("?tab=runs&board_id=b1")).toBe("/evidence/?tab=tasks&board_id=b1");
  });
});

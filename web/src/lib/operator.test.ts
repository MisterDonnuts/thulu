// @vitest-environment jsdom
import { describe, expect, it } from "vitest";

import { ApiError } from "./api";
import { OPERATOR_ENV, isOperatorRefusal, operatorNote, showReadOnly } from "./operator";

describe("isOperatorRefusal", () => {
  it("recognises the daemon's operator-only 403 by the variable it names", () => {
    const e = new ApiError(
      403,
      `the MCP registry is shared by every workspace, so only the operator may change it. Set ${OPERATOR_ENV} to …`,
    );
    expect(isOperatorRefusal(e)).toBe(true);
  });

  it("leaves every other refusal alone", () => {
    expect(isOperatorRefusal(new ApiError(403, "requires admin role"))).toBe(false);
    expect(isOperatorRefusal(new ApiError(400, OPERATOR_ENV))).toBe(false);
    expect(isOperatorRefusal(new Error(OPERATOR_ENV))).toBe(false);
  });
});

describe("showReadOnly", () => {
  it("adds one note and disables the controls it is given", () => {
    const host = document.createElement("div");
    host.innerHTML = `<input id="a" /><button id="b"></button><span id="c"></span>`;
    const controls = [host.querySelector("#a"), host.querySelector("#b"), host.querySelector("#c")];

    showReadOnly(host, "Sandbox limits apply to every workspace.", controls);
    showReadOnly(host, "Sandbox limits apply to every workspace.", controls);

    const notes = host.querySelectorAll(".readonly-note");
    expect(notes).toHaveLength(1);
    expect(host.firstElementChild).toBe(notes[0]);
    expect(notes[0].className).toBe("callout callout-warn callout-bare readonly-note");
    expect(notes[0].getAttribute("role")).toBe("note");
    expect(notes[0].textContent).toBe(operatorNote("Sandbox limits apply to every workspace."));
    expect((host.querySelector("#a") as HTMLInputElement).disabled).toBe(true);
    expect((host.querySelector("#b") as HTMLButtonElement).disabled).toBe(true);
  });
});

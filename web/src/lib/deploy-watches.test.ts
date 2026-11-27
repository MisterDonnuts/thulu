// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import {
  deployWatchColumns,
  deployWatchRowsHTML,
  intervalLabel,
  lastSeen,
  sourceLabel,
  type DeployWatch,
} from "./deploy-watches";

const now = Date.parse("2026-11-18T12:00:00Z");

function watch(over: Partial<DeployWatch> = {}): DeployWatch {
  return {
    id: "dw1",
    project_id: "p1",
    source: "http",
    target: "https://shop.example.com/version",
    ...over,
  };
}

describe("intervalLabel", () => {
  it("says a GitHub watch fires on each event", () => {
    expect(intervalLabel(0)).toBe("On each event");
    expect(intervalLabel(undefined)).toBe("On each event");
  });
  it("writes whole hours as hours and the rest as minutes", () => {
    expect(intervalLabel(3600)).toBe("Every hour");
    expect(intervalLabel(7200)).toBe("Every 2 h");
    expect(intervalLabel(900)).toBe("Every 15 min");
  });
});

describe("sourceLabel", () => {
  it("names each source of the menu and passes an unknown one through", () => {
    expect(sourceLabel("github_workflow")).toBe("GitHub workflow");
    expect(sourceLabel("github_deployment")).toBe("GitHub deployment");
    expect(sourceLabel("cloud")).toBe("Cloud Run");
    expect(sourceLabel("http")).toBe("URL");
    expect(sourceLabel("ssh")).toBe("ssh");
  });
});

describe("lastSeen", () => {
  it("puts a read error ahead of the last state", () => {
    const s = lastSeen(
      watch({ last_state: "success", last_error: "HTTP 403 from run.googleapis.com" }),
      now,
    );
    expect(s.tone).toBe("danger");
    expect(s.sub).toContain("403");
  });
  it("shows the version and when it was read", () => {
    const s = lastSeen(
      watch({ last_state: "success", last_version: "v2", last_checked_at: "2026-11-18T11:50:00Z" }),
      now,
    );
    expect(s.tone).toBe("ok");
    expect(s.sub).toMatch(/^v2 · /);
  });
  it("marks a failing deploy and a watch with no reading yet", () => {
    expect(lastSeen(watch({ last_state: "failure" }), now).tone).toBe("warn");
    expect(lastSeen(watch(), now)).toEqual({ label: "No reading yet", tone: "muted", sub: "" });
  });
  it("does not leave a GitHub watch waiting for a reading Thulu never takes", () => {
    for (const source of ["github_workflow", "github_deployment"]) {
      expect(lastSeen(watch({ source, target: "production" }), now)).toEqual({
        label: "GitHub notifies",
        tone: "muted",
        sub: "",
      });
    }
  });
});

describe("deployWatchRowsHTML", () => {
  it("draws one row per watch with a delete button that carries its id", () => {
    const html = deployWatchRowsHTML(
      [
        watch({ interval_seconds: 900, created_by: "thulu-release" }),
        watch({ id: "dw2", source: "github_deployment", target: "production" }),
      ],
      now,
    );
    const doc = new DOMParser().parseFromString(
      `<table><tbody>${html}</tbody></table>`,
      "text/html",
    );
    const rows = doc.querySelectorAll("tr[data-row]");
    expect(rows).toHaveLength(2);
    expect(rows[0].querySelectorAll("td")).toHaveLength(deployWatchColumns().length);
    expect(rows[0].textContent).toContain("registered by thulu-release");
    expect(rows[1].textContent).toContain("On each event");
    expect(
      [...doc.querySelectorAll("[data-dw-del]")].map((b) => b.getAttribute("data-dw-del")),
    ).toEqual(["dw1", "dw2"]);
  });
  it("escapes a target with markup", () => {
    const html = deployWatchRowsHTML([watch({ target: "https://x.example.com/<img src=x>" })], now);
    expect(html).not.toContain("<img");
  });
  it("says what happens when there is no watch", () => {
    expect(deployWatchRowsHTML([], now)).toContain("The release agent registers one");
  });
});

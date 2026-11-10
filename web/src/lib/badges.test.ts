import { describe, expect, it } from "vitest";

import {
  badgeHTML,
  credentialStateBadge,
  mcpHealthBadge,
  mcpSourceBadge,
  mcpStatusBadge,
  mcpUncheckedBadge,
  runStatusBadge,
  sandboxStatusBadge,
  statusBadges,
  stepStatusBadge,
} from "./badges";

describe("badgeHTML", () => {
  it("drops the modifier for the neutral tone", () => {
    expect(badgeHTML("idle")).toBe('<span class="badge">idle</span>');
  });

  it("adds one modifier class per tone", () => {
    expect(badgeHTML("ready", "ok")).toBe('<span class="badge badge-ok">ready</span>');
    expect(badgeHTML("failed", "danger")).toBe('<span class="badge badge-danger">failed</span>');
  });

  it("escapes the label", () => {
    expect(badgeHTML("<img src=x>", "warn")).toBe(
      '<span class="badge badge-warn">&lt;img src=x&gt;</span>',
    );
  });
});

describe("status tables", () => {
  it("colours the sandbox lifecycle", () => {
    expect(sandboxStatusBadge("ready")).toContain("badge-ok");
    expect(sandboxStatusBadge("starting")).toContain("badge-warn");
    expect(sandboxStatusBadge("evicted")).toContain("badge-danger");
    expect(sandboxStatusBadge("idle")).toBe('<span class="badge">Idle</span>');
  });

  it("puts a person's word on the sandbox pill", () => {
    expect(sandboxStatusBadge("ready")).toBe('<span class="badge badge-ok">Ready</span>');
    expect(sandboxStatusBadge("evicted")).toContain(">Evicted<");
  });

  it("colours MCP container status", () => {
    expect(mcpStatusBadge("running")).toContain("badge-ok");
    expect(mcpStatusBadge("dead")).toContain("badge-danger");
    expect(mcpStatusBadge("missing")).toContain("badge-muted");
  });

  it("labels MCP status from the catalog, and keeps a status it does not know", () => {
    expect(mcpStatusBadge("exited")).toBe('<span class="badge badge-danger">Stopped</span>');
    expect(mcpStatusBadge("configured")).toBe('<span class="badge">Configured</span>');
    expect(mcpStatusBadge("sleepwalking")).toBe('<span class="badge">sleepwalking</span>');
  });

  it("colours automation run outcomes", () => {
    expect(runStatusBadge("success")).toContain("badge-ok");
    expect(runStatusBadge("timeout")).toContain("badge-danger");
    expect(runStatusBadge("canceled")).toBe('<span class="badge">canceled</span>');
  });

  it("labels automation statuses from the catalog, not the raw value", () => {
    expect(runStatusBadge("timeout")).toBe('<span class="badge badge-danger">timed out</span>');
    expect(stepStatusBadge("ok")).toBe('<span class="badge badge-ok">ok</span>');
    expect(stepStatusBadge("failed")).toContain("badge-danger");
  });

  it("falls back to the neutral pill for an unknown status", () => {
    expect(sandboxStatusBadge("hibernating")).toBe('<span class="badge">hibernating</span>');
    expect(runStatusBadge("hibernating")).toBe('<span class="badge">hibernating</span>');
  });
});

describe("mcpHealthBadge", () => {
  it("shows a dash, not a verdict, when nothing has been probed", () => {
    expect(mcpHealthBadge()).toBe('<span class="muted">—</span>');
    expect(mcpHealthBadge("")).toBe('<span class="muted">—</span>');
  });

  it("colours the three health states", () => {
    expect(mcpHealthBadge("healthy")).toContain("badge-ok");
    expect(mcpHealthBadge("starting")).toContain("badge-warn");
    expect(mcpHealthBadge("unhealthy")).toContain("badge-danger");
    expect(mcpHealthBadge("unhealthy")).toContain("Not responding");
  });

  it("says a server was not checked without calling it down", () => {
    expect(mcpUncheckedBadge()).toBe('<span class="badge badge-muted">Not checked</span>');
  });
});

describe("credentialStateBadge", () => {
  it("lets disabled win over the stored state", () => {
    expect(credentialStateBadge({ state: "valid", disabled: true })).toBe(
      '<span class="badge badge-muted">Disabled</span>',
    );
  });

  it("folds the day into the label when the credential is expiring or expired", () => {
    const html = credentialStateBadge({ state: "expiring", expires_at: "2026-07-17T10:32:05Z" });
    expect(html).toContain("badge-warn");
    expect(html).toContain(">Expires ");
    expect(html).not.toContain(":32");
    expect(credentialStateBadge({ state: "expired", expires_at: "2026-07-17T10:32:05Z" })).toMatch(
      /badge-danger">Expired \S/,
    );
  });

  it("keeps the state's word when there is no date to show", () => {
    expect(credentialStateBadge({ state: "expiring" })).toBe(
      '<span class="badge badge-warn">Expiring</span>',
    );
    expect(credentialStateBadge({ state: "valid" })).toBe(
      '<span class="badge badge-ok">Valid</span>',
    );
    expect(credentialStateBadge({ state: "unknown" })).toBe(
      '<span class="badge badge-muted">Not checked</span>',
    );
  });

  it("shows a state it does not know as sent", () => {
    expect(credentialStateBadge({ state: "revoked" })).toBe('<span class="badge">revoked</span>');
  });
});

describe("statusBadges", () => {
  const badge = statusBadges(
    {
      pass: { tone: "ok", label: "Passed" },
      cloning: { tone: "accent", label: "Cloning", icon: "i-clock" },
    },
    { shape: "pill" },
  );

  it("gives every status of the table the same look", () => {
    expect(badge("pass")).toBe('<span class="badge badge-ok badge-pill">Passed</span>');
  });

  it("puts the status's glyph before its word", () => {
    expect(badge("cloning")).toBe(
      '<span class="badge badge-accent badge-pill"><svg class="ico" aria-hidden="true"><use href="#i-clock"/></svg>Cloning</span>',
    );
  });

  it("shows an unknown status as sent, neutral and escaped", () => {
    expect(badge("<b>")).toBe('<span class="badge badge-pill">&lt;b&gt;</span>');
    expect(badge("toString")).toBe('<span class="badge badge-pill">toString</span>');
  });
});

describe("mcpSourceBadge", () => {
  it("marks a registered server with the accent tone", () => {
    expect(mcpSourceBadge("registered")).toContain('class="badge badge-accent badge-mono"');
  });
  it("draws an env server dashed, with a tooltip that says where it is set", () => {
    const html = mcpSourceBadge("env");
    expect(html).toContain("badge-dashed");
    expect(html).toMatch(/title="[^"]+"/);
  });
  it("leaves a runtime server plain", () => {
    expect(mcpSourceBadge("runtime")).toMatch(/^<span class="badge badge-mono">[^<]+<\/span>$/);
  });
});

// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import { roleLabel, workspaceColor, type Membership } from "./account";
import { workspaceItemHTML } from "./workspace-menu";

const ws = (over: Partial<Membership> = {}): Membership =>
  ({ id: "ws-1", name: "Acme", role: "admin", current: false, ...over }) as Membership;

describe("workspaceItemHTML", () => {
  it("writes a menu choice: the mark on its colour, the name, the role under it", () => {
    const html = workspaceItemHTML(ws());
    expect(html).toMatch(/^<button class="menu-item" type="button" role="menuitemradio"/);
    expect(html).toContain('aria-checked="false"');
    expect(html).toContain('data-ws="ws-1"');
    expect(html).toContain(
      `<span class="menu-icon"><span class="sb-ws-mark" aria-hidden="true" style="--ws-color:${workspaceColor("ws-1")}">A</span></span>`,
    );
    expect(html).toContain('<span class="menu-title">Acme</span>');
    expect(html).toContain(`<span class="menu-sub">${roleLabel("admin")}</span>`);
    expect(html).not.toContain("menu-check");
  });

  it("checks the workspace this session is in", () => {
    const html = workspaceItemHTML(ws({ current: true }));
    expect(html).toContain('aria-checked="true"');
    expect(html).toContain("menu-check");
  });

  it("escapes the name in the title, the tooltip and the mark", () => {
    const html = workspaceItemHTML(ws({ name: '<b>"x"</b>' }));
    expect(html).not.toContain("<b>");
    expect(html).toContain('title="&lt;b&gt;&quot;x&quot;&lt;/b&gt;"');
    expect(html).toContain("&lt;b&gt;&quot;x&quot;&lt;/b&gt;</span>");
  });
});

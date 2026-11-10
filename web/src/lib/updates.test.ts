// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import {
  advisoriesHTML,
  checkErrorSummary,
  commandHTML,
  historyColumns,
  historyRowsHTML,
  lastErrorHTML,
  noReleasesLine,
  packageButtons,
  packageFilename,
  releaseHTML,
  severityTone,
  sourceLine,
  sourceShortName,
  unreportedChanges,
  unreportedNoteHTML,
  updateBadge,
  versionsColumns,
  versionsRowsHTML,
  type UpdateHistoryEntry,
  type UpdateRelease,
  type UpdateStatus,
} from "./updates";

function rel(over: Partial<UpdateRelease> = {}): UpdateRelease {
  return {
    version: "0.3.0",
    channel: "stable",
    published_at: "2026-10-14T12:00:00Z",
    notes: "## Fixes\n\n- faster boards",
    commit: "a".repeat(40),
    migrations: { added: 6, contract: 1 },
    requires: ["OCTOPUS_SECRETBOX_KEY"],
    manual_steps: [],
    advisories: [],
    upgrade_from: ">=0.2.0",
    can_upgrade: true,
    rollback: { to: "0.2.0", without_restore: true },
    blockers: [],
    command: "thulu update -version 0.3.0",
    ...over,
  };
}

describe("releaseHTML", () => {
  it("shows the facts, the command and the package download, and no way to apply", () => {
    const html = releaseHTML(rel(), "0.2.0");
    expect(html).toContain("thulu update -version 0.3.0");
    expect(html).toContain("needs no database restore");
    expect(html).toContain("OCTOPUS_SECRETBOX_KEY");
    expect(html).toContain('data-format="md"');
    expect(html).not.toMatch(/apply|install now|update now/i);
  });

  it("escapes what the release source wrote", () => {
    const html = releaseHTML(
      rel({ notes: "<img src=x onerror=alert(1)>", manual_steps: ["<script>x</script>"] }),
      "0.2.0",
    );
    expect(html).not.toContain("<script>");
    expect(html).not.toMatch(/<img/);
  });

  it("keeps javascript: links out of the notes link", () => {
    const html = releaseHTML(rel({ notes_url: "javascript:alert(1)" }), "0.2.0");
    expect(html).not.toContain("javascript:");
  });

  it("shows only the listing for an unverified preview", () => {
    const html = releaseHTML(
      rel({
        version: "0.3.1-dev.gabc1234",
        channel: "preview",
        unverified: "Preview build, not verified here",
        migrations: { added: 0, contract: 0 },
        requires: [],
        upgrade_from: "",
        command: "thulu update -version 0.3.1-dev.gabc1234",
      }),
      "0.3.0",
    );
    expect(html).toContain("not verified here");
    expect(html).toContain("thulu update -version 0.3.1-dev.gabc1234");
    expect(html).not.toContain("migration");
    expect(html).not.toContain("upd-pkg");
  });

  it("lists the blockers", () => {
    const html = releaseHTML(rel({ blockers: ["required variable X is not set"] }), "0.2.0");
    expect(html).toContain("required variable X is not set");
  });

  it("says why a version cannot go in with a danger callout, and an unverified one with a warn", () => {
    const tpl = document.createElement("template");
    tpl.innerHTML = releaseHTML(rel({ blockers: ["X is not set"] }), "0.2.0");
    const blocked = tpl.content.querySelector<HTMLElement>(".callout");
    expect(blocked?.className).toBe("callout callout-danger");
    expect(blocked?.getAttribute("role")).toBe("note");
    expect(blocked?.querySelector(".callout-ico")).not.toBeNull();

    tpl.innerHTML = releaseHTML(rel({ unverified: "not verified here", blockers: [] }), "0.2.0");
    expect(tpl.content.querySelector(".callout")?.className).toBe("callout callout-warn");
    expect(tpl.content.querySelector(".badge.badge-muted")?.textContent).toBe("stable");
  });
});

describe("the release's buttons", () => {
  it("offers the package as Markdown and as JSON, small row buttons the page wires", () => {
    const tpl = document.createElement("template");
    tpl.innerHTML = packageButtons("0.3.0");
    const btns = [...tpl.content.querySelectorAll<HTMLButtonElement>("button.upd-pkg")];
    expect(btns.map((b) => b.dataset.format)).toEqual(["md", "json"]);
    for (const b of btns) {
      expect(b.className).toBe("btn btn-sm upd-pkg");
      expect(b.type).toBe("button");
      expect(b.dataset.to).toBe("0.3.0");
    }
  });

  it("shows the command escaped in a code block whose Copy button names it", () => {
    const tpl = document.createElement("template");
    tpl.innerHTML = commandHTML('thulu update -version "0.3.0" <x>');
    const block = tpl.content.querySelector<HTMLElement>(".code-block");
    expect(block?.className).toBe("code-block code-block-line code-block-wrap upd-cmd");
    expect(block?.querySelector("code.code-block-text")?.textContent).toBe(
      'thulu update -version "0.3.0" <x>',
    );
    const copy = block?.querySelector<HTMLButtonElement>("button[data-code-copy]");
    expect(copy?.getAttribute("aria-label")).toBe("Copy the update command");
    expect(copy?.outerHTML).not.toContain("thulu update");
  });

  it("lets the caller name the command", () => {
    const tpl = document.createElement("template");
    tpl.innerHTML = commandHTML("octopus restore x.tar.gz", "the restore command");
    expect(tpl.content.querySelector("button")?.getAttribute("aria-label")).toBe(
      "Copy the restore command",
    );
  });
});

describe("advisoriesHTML", () => {
  it("is empty without advisories", () => {
    expect(advisoriesHTML([])).toBe("");
  });

  it("names each advisory and where it is fixed", () => {
    const html = advisoriesHTML([
      {
        id: "GHSA-1",
        severity: "high",
        summary: "token leak",
        fixed_in: "0.3.0",
        url: "https://github.com/advisories/GHSA-1",
      },
    ]);
    expect(html).toContain("GHSA-1");
    expect(html).toContain("fixed in 0.3.0");
    expect(html).toContain("badge-danger");
  });

  it("is one danger callout announced as an alert, its list inside the text", () => {
    const tpl = document.createElement("template");
    tpl.innerHTML = advisoriesHTML([
      { id: "GHSA-1", severity: "high", summary: "token leak", fixed_in: "0.3.0" },
    ]);
    const box = tpl.content.firstElementChild as HTMLElement;
    expect(box.className).toBe("callout callout-danger");
    expect(box.getAttribute("role")).toBe("alert");
    expect(box.querySelector(".callout-title")?.textContent).not.toBe("");
    expect(box.querySelector(".callout-text ul.upd-list li")).not.toBeNull();
  });
});

describe("severityTone", () => {
  it("maps severities to badge tones", () => {
    expect(severityTone("CRITICAL")).toBe("danger");
    expect(severityTone("moderate")).toBe("warn");
    expect(severityTone("low")).toBe("muted");
  });
});

describe("sourceLine", () => {
  const base: UpdateStatus = {
    version: "0.2.0",
    dev_build: false,
    channel: "stable",
    mode: "docker",
    source: "github",
    source_off: false,
    channels: ["stable"],
    interval_seconds: 43200,
    update_available: false,
    advisories: [],
    available: [],
    rejected: [],
  };

  it("says where and how often", () => {
    expect(sourceLine(base)).toContain("github");
    expect(sourceLine(base)).toContain("12 hours");
    expect(sourceLine(base)).toContain("not checked yet");
  });

  it("says when lookups are off or misconfigured", () => {
    expect(sourceLine({ ...base, source_off: true })).toContain("off");
    expect(sourceLine({ ...base, source_error: "bad url" })).toContain("bad url");
  });
});

describe("versionsRowsHTML", () => {
  it("gives the running version no command and the others theirs", () => {
    const html = versionsRowsHTML({
      mode: "docker",
      rollback_known: true,
      versions: [
        { version: "0.3.0", current: true },
        {
          version: "0.2.0",
          current: false,
          without_restore: true,
          command: "thulu update -to 0.2.0",
        },
        {
          version: "0.1.0",
          current: false,
          snapshot_missing: true,
          command: "thulu update -to 0.1.0 -restore <snapshot>",
        },
      ],
    });
    expect(html).toContain("running");
    expect(html).toContain("thulu update -to 0.2.0");
    expect(html).toContain("-restore &lt;snapshot&gt;");
    expect(html).toContain("No snapshot recorded");
  });

  it("says from when a restore loses data, and not for an image-only rollback", () => {
    const html = versionsRowsHTML({
      mode: "docker",
      rollback_known: true,
      versions: [
        { version: "0.3.0", current: true },
        {
          version: "0.2.0",
          current: false,
          since: "2026-09-01T12:00:00Z",
          until: "2026-10-01T12:00:00Z",
          snapshot: "octopus_20261001_120000.sql",
          command: "thulu update -to 0.2.0 -restore octopus_20261001_120000.sql",
        },
        {
          version: "0.1.0",
          current: false,
          without_restore: true,
          command: "thulu update -to 0.1.0",
        },
      ],
    });
    const rows = html.split("</tr>");
    expect(rows[1]).toContain("Restores the database: anything written since");
    expect(rows[1]).not.toContain("No snapshot recorded");
    expect(rows[2]).not.toContain("Restores the database");
    expect(rows[2]).toContain("No database restore needed");
  });

  it("still warns about the loss when the version's dates are unknown", () => {
    const html = versionsRowsHTML({
      mode: "docker",
      rollback_known: true,
      versions: [
        {
          version: "0.2.0",
          current: false,
          command: "thulu update -to 0.2.0 -restore <snapshot>",
          snapshot_missing: true,
        },
      ],
    });
    expect(html).toContain("Restores the database");
    expect(html).toContain("No snapshot recorded");
  });
});

describe("historyRowsHTML", () => {
  it("escapes the reported detail", () => {
    const html = historyRowsHTML([
      {
        at: "2026-10-01T12:00:00Z",
        kind: "system.update_reported",
        from: "0.2.0",
        to: "0.3.0",
        result: "rolled_back",
        operator: "ops@host",
        detail: "<b>x</b>",
        actor: "deploy key",
      },
    ]);
    expect(html).toContain("&lt;b&gt;x&lt;/b&gt;");
    expect(html).toContain("rolled back");
    expect(html).toContain("ops@host");
  });

  it("shows the reported detail as a capped log, red when the run failed", () => {
    const row = (result: string) =>
      historyRowsHTML([
        {
          at: "2026-10-01T12:00:00Z",
          kind: "system.update_reported",
          from: "0.2.0",
          to: "0.3.0",
          result,
          detail: "line one\nline two",
          actor: "deploy key",
        },
      ]);
    const tpl = document.createElement("template");
    tpl.innerHTML = `<table><tbody>${row("failed")}</tbody></table>`;
    const log = tpl.content.querySelector(".upd-detail");
    expect(log?.className).toBe(
      "code-block code-block-multi code-block-wrap code-block-h-sm code-block-danger upd-detail",
    );
    expect(log?.querySelector("pre")?.textContent).toBe("line one\nline two");
    tpl.innerHTML = `<table><tbody>${row("ok")}</tbody></table>`;
    expect(tpl.content.querySelector(".upd-detail")?.className).not.toContain("danger");
  });

  it("names the first start instead of an unknown version, and a good run in words", () => {
    const html = historyRowsHTML([
      {
        at: "2026-10-01T12:00:00Z",
        kind: "system.version_changed",
        from: "",
        to: "dev",
        actor: "system",
      },
      {
        at: "2026-10-02T12:00:00Z",
        kind: "system.update_reported",
        from: "dev",
        to: "0.3.0",
        result: "ok",
        actor: "deploy key",
      },
    ]);
    expect(html).not.toContain("?");
    expect(html).toContain("first start</span> → dev");
    expect(html).toContain(">succeeded<");
  });
});

describe("packageFilename", () => {
  it("keeps only version characters", () => {
    expect(packageFilename("0.2.0", "0.3.0", "md")).toBe("octopus-update-0.2.0-to-0.3.0.md");
    expect(packageFilename("dev", "../x", "json")).toBe("octopus-update-dev-to-.._x.json");
  });
});

describe("a failed lookup", () => {
  const st: UpdateStatus = {
    version: "0.3.2",
    dev_build: false,
    channel: "stable",
    mode: "docker",
    source: "github MisterDonnuts/thulu",
    source_off: false,
    channels: ["stable"],
    interval_seconds: 43200,
    last_check: "2026-10-16T10:00:00Z",
    last_error:
      'list releases from github MisterDonnuts/thulu: Get "https://api.github.com/repos/x/releases": dial tcp: lookup api.github.com: no such host',
    update_available: false,
    advisories: [],
    available: [],
    rejected: [],
  };

  it("does not claim the install is the newest", () => {
    expect(noReleasesLine(st)).not.toBe(noReleasesLine({ ...st, last_error: undefined }));
    expect(noReleasesLine(st)).toContain("unknown");
    expect(updateBadge(st)).toEqual({ label: "could not check", tone: "warn" });
    expect(updateBadge({ ...st, last_error: undefined }).tone).toBe("ok");
  });

  it("sums the error up and folds the detail", () => {
    expect(checkErrorSummary(st.last_error ?? "", st.source)).toBe("No connection to GitHub.");
    const html = lastErrorHTML(st);
    expect(html).toContain("Could not look for releases.");
    expect(html).toContain('<details class="callout-detail">');
    expect(html).toContain("no such host");
    expect(html).toMatch(/^<div class="callout callout-danger" role="note">/);
    expect(lastErrorHTML({ ...st, last_error: undefined })).toBe("");
  });

  it("tells a rate limit and a wrong source apart", () => {
    expect(checkErrorSummary("GET releases: 403 rate limit exceeded", st.source)).toContain(
      "limiting",
    );
    expect(
      checkErrorSummary("GET releases: 404 Not Found", "https://mirror.example.com/octopus"),
    ).toBe(
      "mirror.example.com does not have the releases this install asks for: check OCTOPUS_UPDATE_SOURCE.",
    );
    expect(checkErrorSummary("bad manifest", "file:///srv/releases")).toBe(
      "The lookup at file:///srv/releases failed.",
    );
  });

  it("names sources the way people do", () => {
    expect(sourceShortName("github MisterDonnuts/thulu")).toBe("GitHub");
    expect(sourceShortName("https://mirror.example.com/x")).toBe("mirror.example.com");
  });
});

describe("unreportedChanges", () => {
  const entry = (kind: string, to: string, at: string): UpdateHistoryEntry => ({
    kind,
    to,
    at,
    from: "0.3.1",
    actor: "system",
  });

  it("flags a restart no update run reported", () => {
    const list = [
      entry("system.update_reported", "0.3.2", "2026-10-16T10:05:00Z"),
      entry("system.version_changed", "0.3.2", "2026-10-16T10:02:00Z"),
      entry("system.version_changed", "0.3.1", "2026-10-01T09:00:00Z"),
    ];
    expect(unreportedChanges(list).map((e) => e.to)).toEqual(["0.3.1"]);
    const html = unreportedNoteHTML(list);
    expect(html).toContain("0.3.1");
    expect(html).toContain("OCTOPUS_API_KEY");
    expect(html).toContain('href="/settings/#api"');
    expect(html).toMatch(/^<div class="callout callout-warn" role="note">/);
  });

  it("does not pair a report of the same version days apart", () => {
    const list = [
      entry("system.update_reported", "0.3.2", "2026-10-10T10:00:00Z"),
      entry("system.version_changed", "0.3.2", "2026-10-16T10:00:00Z"),
    ];
    expect(unreportedChanges(list)).toHaveLength(1);
  });

  it("leaves out the first boot, which no update brought", () => {
    const firstBoot = {
      ...entry("system.version_changed", "0.3.1", "2026-10-01T09:00:00Z"),
      from: "",
    };
    expect(unreportedChanges([firstBoot])).toEqual([]);
    expect(unreportedNoteHTML([firstBoot])).toBe("");
    const later = entry("system.version_changed", "0.3.2", "2026-10-16T10:02:00Z");
    expect(unreportedChanges([later, firstBoot]).map((e) => e.to)).toEqual(["0.3.2"]);
  });

  it("says nothing when every restart has its report", () => {
    expect(
      unreportedNoteHTML([entry("system.update_reported", "0.3.2", "2026-10-16T10:00:00Z")]),
    ).toBe("");
  });
});

describe("update tables", () => {
  it("mark the column with a command or a log to take a stacked card's whole width", () => {
    expect(versionsColumns().map((c) => c.cls)).toEqual([undefined, undefined, "upd-wide"]);
    expect(historyColumns().map((c) => c.cls)).toEqual([
      undefined,
      undefined,
      undefined,
      "upd-wide",
    ]);
  });

  it("label every history cell for the stacked phone layout and mark a failed run", () => {
    const html = historyRowsHTML([
      {
        at: "2026-10-01T12:00:00Z",
        kind: "system.update_reported",
        from: "0.2.0",
        to: "0.3.0",
        result: "failed",
        actor: "deploy key",
      },
      {
        at: "2026-10-02T12:00:00Z",
        kind: "system.update_reported",
        from: "0.3.0",
        to: "0.3.1",
        result: "ok",
        actor: "deploy key",
      },
    ]);
    const [bad, good] = html.split("</tr>");
    expect(bad).toContain('data-tone="danger"');
    expect(good).not.toContain("data-tone");
    for (const col of historyColumns()) expect(bad).toContain(`data-label="${col.label}"`);
  });

  it("give the kept versions three columns, the command filling the row", () => {
    expect(versionsColumns().map((c) => c.width)).toEqual(["shrink", "shrink", "fill"]);
    const html = versionsRowsHTML({
      versions: [{ version: "0.3.1", since: "2026-10-01T12:00:00Z", current: true }],
    } as Parameters<typeof versionsRowsHTML>[0]);
    expect(html.match(/<td/g)).toHaveLength(3);
    expect(html).toContain("0.3.1");
  });
});

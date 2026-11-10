// @vitest-environment jsdom

import { describe, expect, it } from "vitest";

import { backupHTML, backupStale, offsiteLine, type BackupStatus } from "./backups";

const b: BackupStatus = {
  found: true,
  archive: "octopus_20261016_031500.tar.gz",
  backup_dir: "/srv/octopus/backups",
  size_bytes: 3 * 1024 * 1024,
  finished_at: "2026-10-16T03:15:09Z",
  kept: 5,
  keep: 5,
  source: "compose",
  remote: "u1@box:octopus",
  remote_state: "ok",
  restore_command:
    "RESTORE=1 OCTOPUS_COMPOSE_FILE=/srv/octopus/deploy/docker-compose.yml /srv/octopus/deploy/backup.sh /srv/octopus/backups/octopus_20261016_031500.tar.gz",
};

describe("backupHTML", () => {
  const now = new Date("2026-10-16T12:00:00Z");
  it("shows the archive, its size, the count against the cap and the restore command", () => {
    const html = backupHTML(b, now);
    expect(html).toContain("octopus_20261016_031500.tar.gz");
    expect(html).toContain("3.1 MB");
    expect(html).toContain("5 snapshots of 5 (OCTOPUS_BACKUP_KEEP)");
    expect(html).toContain('<code class="code-block-text">RESTORE=1 OCTOPUS_COMPOSE_FILE=');
    expect(html).toContain('aria-label="Copy the restore command"');
    expect(html).not.toContain("OCTOPUS_DATABASE_URL");
  });

  it("asks for the database URL on a host-mode restore", () => {
    expect(backupHTML({ ...b, needs_database_url: true }, now)).toContain("OCTOPUS_DATABASE_URL");
  });

  it("explains how to get a first backup", () => {
    expect(backupHTML({ found: false }, now)).toContain("deploy/backup.sh");
  });

  it("escapes what the status file says", () => {
    expect(backupHTML({ ...b, archive: "<img src=x>" }, now)).not.toContain("<img");
  });

  it("writes the facts as a fact list, the archive in mono and the off-site copy as a badge", () => {
    const tpl = document.createElement("template");
    tpl.innerHTML = backupHTML(b, now);
    const mono = [...tpl.content.querySelectorAll("dl.fact-list.upd-facts dd.fact-list-mono")];
    expect(mono.map((d) => d.textContent)).toContain("octopus_20261016_031500.tar.gz");
    expect(tpl.content.querySelector("dl.fact-list dd .badge.badge-ok")).not.toBeNull();
  });

  it("flags a stale backup with a warn badge", () => {
    const later = new Date("2026-10-19T12:00:00Z");
    expect(backupHTML(b, later)).toContain('class="badge badge-warn"');
  });
});

describe("offsiteLine", () => {
  it("says where the copy went, or that there is none", () => {
    expect(offsiteLine(b)).toEqual({ text: "copied to u1@box:octopus", tone: "ok" });
    expect(offsiteLine({ ...b, remote_state: "failed" }).tone).toBe("danger");
    expect(offsiteLine({ ...b, remote_state: "pending" }).tone).toBe("warn");
    expect(offsiteLine({ ...b, remote: "", remote_state: "none" }).text).toContain(
      "OCTOPUS_BACKUP_REMOTE",
    );
  });
});

describe("backupStale", () => {
  it("flags a backup older than a day and a half", () => {
    expect(backupStale(b, new Date("2026-10-16T12:00:00Z"))).toBe(false);
    expect(backupStale(b, new Date("2026-10-18T12:00:00Z"))).toBe(true);
  });
});

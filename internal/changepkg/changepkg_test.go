package changepkg

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/deployfiles"
	"github.com/MisterDonnuts/thulu/internal/release"
)

func manifest() *release.Manifest {
	d := func(c string) string { return "sha256:" + strings.Repeat(c, 64) }
	return &release.Manifest{
		Schema: release.SchemaVersion, Version: "0.3.0", Channel: release.ChannelStable,
		Commit: strings.Repeat("a", 40), PublishedAt: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC),
		Images: map[string]string{
			"octopus":       "ghcr.io/misterdonnuts/octopus@" + d("1"),
			"octopus-agent": "ghcr.io/misterdonnuts/octopus-agent@" + d("2"),
		},
		ThirdParty:  map[string]string{"postgres": "postgres:16-alpine@" + d("3")},
		Chart:       release.Chart{Ref: "oci://ghcr.io/misterdonnuts/charts/octopus:0.3.0", Digest: d("4")},
		UpgradeFrom: ">=0.2.0", RollbackTo: "0.2.0",
		Migrations:  release.Migrations{Added: 6, Contract: 1},
		Requires:    []string{"OCTOPUS_SECRETBOX_KEY"},
		ManualSteps: []string{"Upgrade Postgres to 17 with a dump and restore."},
		Advisories:  []release.Advisory{{ID: "GHSA-1", Severity: "high", Summary: "a | b", Affected: "<0.3.0"}},
	}
}

func TestBuildDockerToRollbackTarget(t *testing.T) {
	p := Build(manifest(), Facts{
		From: "0.2.0", Mode: ModeDocker, Source: "github MisterDonnuts/thulu",
		Notes:      "## Fixed\n- stuff",
		MissingEnv: []string{"OCTOPUS_SECRETBOX_KEY"},
		Files: []deployfiles.Change{
			{Path: "docker-compose.yml", Kind: deployfiles.Updated, Diff: "--- a\n+++ b\n-x\n+y\n"},
			{Path: "Caddyfile", Kind: deployfiles.Edited, Diff: "--- c\n+++ d\n"},
		},
	})
	if !p.RollbackWithoutRestore {
		t.Error("0.2.0 is rollback_to")
	}
	if len(p.Advisories) != 1 {
		t.Errorf("advisories = %v", p.Advisories)
	}
	if len(p.Blockers) != 2 {
		t.Errorf("blockers = %v", p.Blockers)
	}
	if p.Plan[1] != "thulu update -version 0.3.0 -ack-manual-steps" {
		t.Errorf("plan = %v", p.Plan)
	}
	if !strings.Contains(p.Rollback[1], "`thulu update -to 0.2.0`") {
		t.Errorf("rollback = %v", p.Rollback)
	}
	md := string(p.Markdown())
	for _, want := range []string{
		"# Thulu change package: 0.2.0 → 0.3.0",
		"| GHSA-1 | high | a \\| b |",
		"Contract (drop or rename something an older version reads): 1",
		"Not set on this install: `OCTOPUS_SECRETBOX_KEY`",
		"- `deploy/Caddyfile`: edited",
		"````diff",
		"cosign verify --key octopus-release.pub ghcr.io/misterdonnuts/octopus@sha256:1111",
		"cosign verify --key octopus-release.pub ghcr.io/misterdonnuts/charts/octopus@sha256:4444",
		"> ## Fixed",
		"not covered by the signature",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q", want)
		}
	}
	b, err := p.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var back Package
	if err := json.Unmarshal(b, &back); err != nil || back.To != "0.3.0" || len(back.Images) != 2 {
		t.Errorf("json round trip: %v %+v", err, back)
	}
}

func TestBuildKubernetesFurtherBack(t *testing.T) {
	m := manifest()
	m.ManualSteps = nil
	p := Build(m, Facts{From: "0.2.5", Mode: ModeKubernetes})
	if p.RollbackWithoutRestore {
		t.Error("0.2.5 is not rollback_to")
	}
	if p.Plan[1] != "thulu update -version 0.3.0 -ack-manual-steps -backup-confirmed" {
		t.Errorf("plan = %v", p.Plan)
	}
	if !strings.Contains(strings.Join(p.Rollback, " "), "-restore external") {
		t.Errorf("rollback = %v", p.Rollback)
	}
	if len(p.Blockers) != 0 {
		t.Errorf("blockers = %v", p.Blockers)
	}
}

func TestBlockers(t *testing.T) {
	for from, want := range map[string]string{
		"":      "could not be read",
		"dev":   "unstamped",
		"0.4.0": "older than the installed",
		"0.1.0": "upgrades only from >=0.2.0",
	} {
		p := Build(manifest(), Facts{From: from})
		if len(p.Blockers) == 0 || !strings.Contains(p.Blockers[0], want) {
			t.Errorf("from %q: %v", from, p.Blockers)
		}
	}
	if p := Build(manifest(), Facts{From: "0.3.0"}); len(p.Blockers) != 0 {
		t.Errorf("re-apply blocked: %v", p.Blockers)
	}
}

func TestBuildWritesInTheRequestedLocale(t *testing.T) {
	es := Build(manifest(), Facts{From: "0.2.0", Mode: ModeDocker, Locale: "es"})
	if md := string(es.Markdown()); !strings.HasPrefix(md, "# Paquete de cambio de Thulu: 0.2.0 → 0.3.0") {
		t.Errorf("es markdown starts %q", md[:min(len(md), 80)])
	}
	en := Build(manifest(), Facts{From: "0.2.0", Mode: ModeDocker, Locale: "en"})
	if md := string(en.Markdown()); !strings.HasPrefix(md, "# Thulu change package: 0.2.0 → 0.3.0") {
		t.Errorf("en markdown starts %q", md[:min(len(md), 80)])
	}
	if es.Backup == en.Backup || es.Rollback[0] == en.Rollback[0] {
		t.Errorf("the prose fields did not follow the locale: %q / %q", es.Backup, en.Backup)
	}
}

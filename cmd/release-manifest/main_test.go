package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/release"
	"github.com/MisterDonnuts/thulu/internal/schema"
	"github.com/MisterDonnuts/thulu/internal/semver"
)

func digest(c byte) string { return "sha256:" + strings.Repeat(string(c), 64) }

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUpgradeFrom(t *testing.T) {
	for v, want := range map[string]string{
		"0.3.0":      ">=0.2.0",
		"0.3.4":      ">=0.2.0",
		"0.2.0":      ">=0.1.0",
		"1.0.0":      ">=1.0.0",
		"1.4.0-rc.1": ">=1.3.0",
	} {
		if got := upgradeFrom(semver.MustParse(v)); got != want {
			t.Errorf("upgradeFrom(%s) = %s, want %s", v, got, want)
		}
	}
}

func TestRollbackTo(t *testing.T) {
	l := &schema.Lock{Releases: []string{"0.1.0", "0.2.0", "0.2.1", "0.3.0"}}
	for v, want := range map[string]string{
		"0.2.0": "",
		"0.2.1": "0.2.0",
		"0.3.0": "0.2.1",
		"0.1.0": "",
		"1.0.0": "",
	} {
		if got := rollbackTo(l, v); got != want {
			t.Errorf("rollbackTo(%s) = %q, want %q", v, got, want)
		}
	}
	l.Releases = append(l.Releases, "1.0.0")
	if got := rollbackTo(l, "1.1.0"); got != "1.0.0" {
		t.Errorf("rollbackTo(1.1.0) = %q, want 1.0.0", got)
	}
}

func TestBuildFromTheRepository(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "images", "octopus.ref"), "ghcr.io/misterdonnuts/octopus@"+digest('1')+"\n")
	write(t, filepath.Join(dir, "images", "octopus-agent.ref"), "ghcr.io/misterdonnuts/octopus-agent@"+digest('2'))
	write(t, filepath.Join(dir, "dist", "octopus_0.2.0_linux_amd64"), "elf")
	write(t, filepath.Join(dir, "dist", "octopus_0.2.0_darwin_arm64"), "macho")
	write(t, filepath.Join(dir, "dist", "octopus_0.2.0_linux_amd64.sha256"), "x")
	write(t, filepath.Join(dir, "dist", "octopus_0.2.0_checksums.txt"), "x")
	write(t, filepath.Join(dir, "dist", "octopus_0.2.0_deploy.tar.gz"), "tgz")
	write(t, filepath.Join(dir, "releases", "0.2.0.json"),
		`{"requires":["OCTOPUS_SECRETBOX_KEY"],"manual_steps":[],"advisories":[]}`)

	raw, err := os.ReadFile(filepath.Join("..", "..", schema.LockPath))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := schema.ParseLock(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	lock = before(lock, "0.2.0")
	args := func(lockPath string) []string {
		return []string{
			"-version", "v0.2.0", "-commit", strings.Repeat("c", 40),
			"-images", filepath.Join(dir, "images"),
			"-third-party", "postgres=postgres:16-alpine@" + digest('3'),
			"-chart-ref", "oci://ghcr.io/misterdonnuts/charts/octopus:0.2.0", "-chart-digest", digest('4'),
			"-bundle", filepath.Join(dir, "dist", "octopus_0.2.0_deploy.tar.gz"),
			"-binaries", filepath.Join(dir, "dist"),
			"-lock", lockPath, "-meta", filepath.Join(dir, "releases"),
			"-o", filepath.Join(dir, "out.json"),
		}
	}

	unreleased := filepath.Join(dir, "unreleased.lock")
	write(t, unreleased, string(lock.Format()))
	if err := run(args(unreleased), os.Stdout); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("built a manifest from a lock that was never marked: %v", err)
	}

	marked, err := schema.MarkReleased(lock, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(dir, "ready.lock")
	write(t, ready, string(marked.Format()))
	if err := run(args(ready), os.Stdout); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "out.json"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := release.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if m.RollbackTo != "" || m.UpgradeFrom != ">=0.1.0" || m.Channel != release.ChannelStable {
		t.Errorf("policy fields = rollback %q, upgrade %q, channel %q", m.RollbackTo, m.UpgradeFrom, m.Channel)
	}
	if m.Migrations.Added != len(lock.Entries)-countReleased(lock) {
		t.Errorf("migrations added = %d", m.Migrations.Added)
	}
	if len(m.Binaries) != 2 || m.Binaries["linux_amd64"].SHA256 != release.SHA256Hex([]byte("elf")) {
		t.Errorf("binaries = %+v", m.Binaries)
	}
	if m.DeployBundle.SHA256 != release.SHA256Hex([]byte("tgz")) || m.Requires[0] != "OCTOPUS_SECRETBOX_KEY" {
		t.Errorf("bundle/meta = %+v %v", m.DeployBundle, m.Requires)
	}
}

func TestBuildPreview(t *testing.T) {
	in := inputs{
		Version: "0.3.1-dev.gabc1234", Commit: strings.Repeat("c", 40),
		Images:   map[string]string{"octopus": "ghcr.io/x/octopus@" + digest('1')},
		ChartRef: "oci://ghcr.io/x/charts/octopus:0.3.1-dev.gabc1234", ChartDigest: digest('2'),
		Bundle:   release.Artifact{File: "octopus_0.3.1-dev.gabc1234_deploy.tar.gz", SHA256: digest('3')[7:]},
		Binaries: map[string]release.Artifact{"linux_amd64": {File: "octopus_0.3.1-dev.gabc1234_linux_amd64", SHA256: digest('4')[7:]}},
		Lock:     &schema.Lock{},
	}
	in.PublishedAt = in.PublishedAt.Add(1)
	m, err := build(in)
	if err != nil {
		t.Fatal(err)
	}
	if m.Channel != release.ChannelPreview || m.RollbackTo != "" {
		t.Errorf("preview = channel %s, rollback %q", m.Channel, m.RollbackTo)
	}
	in.Version, in.Channel = "0.3.1", release.ChannelPreview
	if _, err := build(in); err == nil {
		t.Error("a release version on the preview channel")
	}
}

func countReleased(l *schema.Lock) int {
	n := 0
	for _, e := range l.Entries {
		if e.ReleasedIn != "" {
			n++
		}
	}
	return n
}

func TestReadMetaRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "0.3.0.json"), `{"requires":[],"surprise":1}`)
	if _, err := readMeta(dir, "0.3.0"); err == nil {
		t.Error("unknown field accepted")
	}
	if m, err := readMeta(dir, "0.4.0"); err != nil || m.Requires != nil {
		t.Errorf("missing meta = %+v, %v", m, err)
	}
}

func before(l *schema.Lock, version string) *schema.Lock {
	out := &schema.Lock{}
	for _, r := range l.Releases {
		if semver.Less(r, version) {
			out.Releases = append(out.Releases, r)
		}
	}
	for _, e := range l.Entries {
		if e.ReleasedIn != "" && !semver.Less(e.ReleasedIn, version) {
			e.ReleasedIn = ""
		}
		out.Entries = append(out.Entries, e)
	}
	return out
}

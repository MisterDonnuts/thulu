package updater

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/agentimage"
	"github.com/MisterDonnuts/thulu/internal/deployfiles"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
	"github.com/MisterDonnuts/thulu/internal/release"
	"github.com/MisterDonnuts/thulu/internal/release/releasetest"
)

var (
	filesV1 = map[string]string{"docker-compose.standalone.yml": composeV1, "backup.sh": "#!/bin/sh\n# v1\n"}
	filesV2 = map[string]string{"docker-compose.standalone.yml": strings.Replace(composeV1, "1.0.0", "1.1.0", 1),
		"backup.sh": "#!/bin/sh\n# v1\n", "Caddyfile": ":80\n"}
)

func twoReleases(t *testing.T, edit func(*release.Manifest)) (*harness, *release.Manifest) {
	h := newHarness(t)
	h.publish("1.0.0", filesV1, nil)
	m := h.publish("1.1.0", filesV2, func(m *release.Manifest) {
		m.RollbackTo = "1.0.0"
		if edit != nil {
			edit(m)
		}
	})
	h.install("1.0.0", filesV1, "")
	return h, m
}

func TestUpdateInstallsAVerifiedRelease(t *testing.T) {
	h, m := twoReleases(t, nil)
	h.write(deployfiles.OverrideFile, "services: {}\n", 0o644)
	opts := h.opts("1.1.0")
	opts.Version = "1.1.0"
	if err := h.update(opts, initwizard.ModeDocker); err != nil {
		t.Fatalf("update: %v\n%s", err, h.out.String())
	}

	if h.daemon.version != "1.1.0" {
		t.Errorf("daemon runs %q", h.daemon.version)
	}
	env := h.read("deploy/.env")
	for _, want := range []string{"OCTOPUS_VERSION=1.1.0", blockStart, "OCTOPUS_IMAGE=" + m.Images["octopus"],
		"OCTOPUS_PG_IMAGE=" + m.ThirdParty["postgres"], "OCTOPUS_KEPT_VERSIONS=1.1.0,1.0.0",
		"OCTOPUS_KEPT_SNAPSHOTS=1.0.0=octopus_20261001_120000.sql"} {
		if !strings.Contains(env, want) {
			t.Errorf(".env lacks %q:\n%s", want, env)
		}
	}
	if got := h.read("deploy/docker-compose.standalone.yml"); got != filesV2["docker-compose.standalone.yml"] {
		t.Errorf("compose file not rewritten:\n%s", got)
	}
	if got := h.read("deploy/Caddyfile"); got != ":80\n" {
		t.Errorf("new file not written: %q", got)
	}
	rec, err := deployfiles.LoadRecord(h.root)
	if err != nil || rec == nil || rec.Version != "1.1.0" || len(rec.Files) != 3 {
		t.Errorf("record = %+v, %v", rec, err)
	}
	if !h.runner.ran("compose -f deploy/docker-compose.standalone.yml -f deploy/docker-compose.override.yml up -d --no-build") {
		t.Errorf("compose up without the override: %v", h.runner.cmds)
	}
	for _, n := range []string{"octopus", "octopus-agent-claude-code", "octopus-agent-codex", "octopus-agent-opencode",
		"octopus-runtime", "octopus-mcp-octopus-api"} {
		if !contains(h.images.pulled, m.Images[n]) {
			t.Errorf("%s not pulled: %v", n, h.images.pulled)
		}
	}
	if contains(h.images.pulled, m.Images["octopus-sandbox"]) {
		t.Error("the Kubernetes sandbox image was pulled on a Docker install")
	}
	if contains(h.images.pulled, m.Images["octopus-agent"]) || strings.Contains(env, "OCTOPUS_AGENT_IMAGE=") {
		t.Errorf("the combined agent image was pinned or pulled:\n%s", env)
	}

	st, err := LoadState(h.root)
	if err != nil {
		t.Fatal(err)
	}
	if st.Result != ResultOK || st.From.Version != "1.0.0" || st.To.Version != "1.1.0" || st.BackupPath == "" ||
		st.Operator != "ops@host" || st.RollbackTo != "1.0.0" {
		t.Errorf("state = %+v", st)
	}
	if len(st.Kept) != 2 || st.Kept[0].Version != "1.1.0" || st.Kept[1].Snapshot != "octopus_20261001_120000.sql" {
		t.Errorf("kept = %+v", st.Kept)
	}
	for _, f := range []string{StateFile, "deploy/.env"} {
		if info, err := os.Stat(filepath.Join(h.root, f)); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, %v", f, info.Mode(), err)
		}
	}

	ev := h.daemon.lastEvent(t)
	if ev.From != "1.0.0" || ev.To != "1.1.0" || ev.Result != ResultOK || ev.Mode != "docker" || ev.Operator != "ops@host" ||
		ev.StartedAt.IsZero() || ev.FinishedAt.Before(ev.StartedAt) {
		t.Errorf("event = %+v", ev)
	}
	if h.daemon.auth[0] != "Bearer test-key" {
		t.Errorf("event auth = %q", h.daemon.auth[0])
	}
	if strings.Contains(h.out.String(), "test-key") {
		t.Error("the API key was printed")
	}
}

func TestUpdatePullsTheEnginesInitChose(t *testing.T) {
	h, m := twoReleases(t, nil)
	if err := initwizard.SaveAnswers(h.root, &initwizard.Config{Engines: []string{"codex"}}); err != nil {
		t.Fatal(err)
	}
	opts := h.opts("1.1.0")
	opts.Version = "1.1.0"
	if err := h.update(opts, initwizard.ModeDocker); err != nil {
		t.Fatalf("update: %v\n%s", err, h.out.String())
	}
	if !contains(h.images.pulled, m.Images["octopus-agent-codex"]) {
		t.Errorf("codex's image not pulled: %v", h.images.pulled)
	}
	env := h.read("deploy/.env")
	for _, n := range []string{"claude-code", "opencode"} {
		ref := m.Images[agentimage.Name(n)]
		if contains(h.images.pulled, ref) {
			t.Errorf("%s's image pulled although init did not choose it", n)
		}
		if !strings.Contains(env, agentimage.Env(n)+"="+ref) {
			t.Errorf(".env does not pin %s:\n%s", n, env)
		}
	}
}

func TestUpdateGoesBackWhenTheNewVersionNeverGetsReady(t *testing.T) {
	h, _ := twoReleases(t, nil)
	envBefore := h.read("deploy/.env")
	h.runner.broken = map[string]bool{"1.1.0": true}
	opts := h.opts("1.1.0")
	opts.Version = "1.1.0"
	err := h.update(opts, initwizard.ModeDocker)
	if err == nil || !strings.Contains(err.Error(), "1.0.0 is running again") {
		t.Fatalf("err = %v\n%s", err, h.out.String())
	}
	if h.daemon.version != "1.0.0" || !h.daemon.ready {
		t.Errorf("daemon = %q ready=%v", h.daemon.version, h.daemon.ready)
	}
	if got := h.read("deploy/.env"); got != envBefore {
		t.Errorf(".env not restored:\n%s", got)
	}
	if got := h.read("deploy/docker-compose.standalone.yml"); got != composeV1 {
		t.Errorf("compose file not restored:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(h.root, "deploy", "Caddyfile")); !os.IsNotExist(err) {
		t.Error("a file the release added survived the rollback")
	}
	if len(h.runner.restored) != 0 {
		t.Error("restored a snapshot going back to rollback_to")
	}
	st, _ := LoadState(h.root)
	if st.Result != ResultRolledBack || st.Detail == "" {
		t.Errorf("state = %+v", st)
	}
	if ev := h.daemon.lastEvent(t); ev.Result != ResultRolledBack {
		t.Errorf("event = %+v", ev)
	}

	h.runner.broken = nil
	if err := h.update(opts, initwizard.ModeDocker); err != nil {
		t.Fatalf("retry: %v\n%s", err, h.out.String())
	}
	if h.daemon.version != "1.1.0" {
		t.Errorf("after retry daemon = %q", h.daemon.version)
	}
}

func TestUpdateRestoresTheSnapshotWhenTheTargetIsNotRollbackSafe(t *testing.T) {
	h, _ := twoReleases(t, func(m *release.Manifest) { m.RollbackTo = "" })
	h.runner.broken = map[string]bool{"1.1.0": true}
	opts := h.opts("1.1.0")
	opts.Version = "1.1.0"
	if err := h.update(opts, initwizard.ModeDocker); err == nil {
		t.Fatal("a version that never came up was reported as installed")
	}
	if !h.runner.ran("stop octopus") || len(h.runner.restored) != 1 || h.runner.restored[0] != "-- dump\n" {
		t.Errorf("snapshot not restored: %v / %q", h.runner.cmds, h.runner.restored)
	}
	if h.daemon.version != "1.0.0" {
		t.Errorf("daemon = %q", h.daemon.version)
	}
}

func TestUpdateStopsOnAnEditedDeployFile(t *testing.T) {
	h, _ := twoReleases(t, nil)
	h.write("deploy/docker-compose.standalone.yml", composeV1+"# mine\n", 0o644)
	envBefore := h.read("deploy/.env")
	opts := h.opts("1.1.0")
	opts.Version = "1.1.0"
	err := h.update(opts, initwizard.ModeDocker)
	if err == nil {
		t.Fatal("an edited deploy file was overwritten")
	}
	for _, want := range []string{"edited by hand", "+++ deploy/docker-compose.standalone.yml (release)", "-# mine", deployfiles.OverrideFile} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	if h.read("deploy/.env") != envBefore || len(h.images.pulled) != 0 || h.backups != 0 || h.runner.ran("up") {
		t.Error("something changed before the preflight finished")
	}
}

func TestUpdateRefusesAMissingRequiredVariable(t *testing.T) {
	h, _ := twoReleases(t, func(m *release.Manifest) { m.Requires = []string{"OCTOPUS_NEW_SECRET"} })
	opts := h.opts("1.1.0")
	opts.Version = "1.1.0"
	err := h.update(opts, initwizard.ModeDocker)
	if err == nil || !strings.Contains(err.Error(), "OCTOPUS_NEW_SECRET") {
		t.Fatalf("err = %v", err)
	}
	if len(h.images.pulled) != 0 {
		t.Error("pulled before the preflight passed")
	}
}

func TestUpdateWaitsForManualSteps(t *testing.T) {
	h, _ := twoReleases(t, func(m *release.Manifest) { m.ManualSteps = []string{"rotate the webhook secret"} })
	opts := h.opts("1.1.0")
	opts.Version = "1.1.0"
	err := h.update(opts, initwizard.ModeDocker)
	if err == nil || !strings.Contains(err.Error(), "-ack-manual-steps") || !strings.Contains(err.Error(), "rotate the webhook secret") {
		t.Fatalf("err = %v", err)
	}
	opts.AckManualSteps = true
	if err := h.update(opts, initwizard.ModeDocker); err != nil {
		t.Fatalf("acknowledged: %v", err)
	}
}

func TestUpdateRefusesAnUnsignedImage(t *testing.T) {
	h, _ := twoReleases(t, nil)
	other := releasetest.NewKey(t)
	m := h.releases["1.1.0"]
	ref := releasetest.PushImage(t, h.host, "misterdonnuts/octopus-agent-codex", "evil")
	other.SignImage(t, ref)
	m.Images["octopus-agent-codex"] = ref
	h.sign(m)
	envBefore := h.read("deploy/.env")
	opts := h.opts("1.1.0")
	opts.Version = "1.1.0"
	err := h.update(opts, initwizard.ModeDocker)
	if err == nil || !strings.Contains(err.Error(), "image signature") {
		t.Fatalf("err = %v", err)
	}
	if len(h.images.pulled) != 0 || h.read("deploy/.env") != envBefore {
		t.Error("an install changed before every signature was checked")
	}
}

func TestUpdateRefusesATamperedManifest(t *testing.T) {
	h, _ := twoReleases(t, nil)
	body, err := os.ReadFile(filepath.Join(h.srcDir, "1.1.0", release.ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(body), `"rollback_to": "1.0.0"`, `"rollback_to": "0.9.0"`, 1)
	if tampered == string(body) {
		t.Fatal("the test did not change the manifest")
	}
	h.writeSource("1.1.0", release.ManifestFile, []byte(tampered))
	opts := h.opts("1.1.0")
	opts.Version = "1.1.0"
	if err := h.update(opts, initwizard.ModeDocker); err == nil {
		t.Fatal("a manifest whose signature does not match was used")
	}
	if len(h.images.pulled) != 0 || h.runner.ran("up") {
		t.Error("acted on an unverified manifest")
	}
}

func TestUpdateRefusesABundleThatDoesNotMatchTheManifest(t *testing.T) {
	h, _ := twoReleases(t, nil)
	h.writeSource("1.1.0", "octopus-deploy-1.1.0.tar.gz", buildBundle(t, map[string]string{"docker-compose.standalone.yml": "evil"}))
	opts := h.opts("1.1.0")
	opts.Version = "1.1.0"
	err := h.update(opts, initwizard.ModeDocker)
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("err = %v", err)
	}
	if h.read("deploy/docker-compose.standalone.yml") != composeV1 {
		t.Error("deploy file written from a bundle the manifest does not name")
	}
}

func TestUpdateRefusesAnUnsupportedUpgradePath(t *testing.T) {
	h, _ := twoReleases(t, func(m *release.Manifest) { m.UpgradeFrom = ">=1.0.5" })
	opts := h.opts("1.1.0")
	opts.Version = "1.1.0"
	err := h.update(opts, initwizard.ModeDocker)
	if err == nil || !strings.Contains(err.Error(), "intermediate release") {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateRefusesAChannelThatIsNotAllowed(t *testing.T) {
	h, _ := twoReleases(t, nil)
	h.publish("1.2.0-rc.1", filesV2, func(m *release.Manifest) { m.Channel = release.ChannelPrerelease })
	opts := h.opts("1.2.0-rc.1")
	opts.Version = "1.2.0-rc.1"
	err := h.update(opts, initwizard.ModeDocker)
	if err == nil || !strings.Contains(err.Error(), release.EnvChannels) {
		t.Fatalf("err = %v", err)
	}
	t.Setenv(release.EnvChannels, "stable,prerelease")
	if err := h.update(opts, initwizard.ModeDocker); err != nil {
		t.Fatalf("with the channel allowed: %v", err)
	}
}

func TestApplyInstallsTheConfiguredVersion(t *testing.T) {
	h, _ := twoReleases(t, nil)
	h.write("deploy/.env", strings.Replace(h.read("deploy/.env"), "OCTOPUS_VERSION=1.0.0", "OCTOPUS_VERSION=1.1.0", 1), 0o600)
	h.daemon.set("1.0.0", true)
	if err := h.apply(h.opts("1.1.0")); err != nil {
		t.Fatalf("apply: %v\n%s", err, h.out.String())
	}
	if h.daemon.version != "1.1.0" {
		t.Errorf("daemon = %q", h.daemon.version)
	}
	backups := h.backups
	if err := h.apply(h.opts("1.1.0")); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if h.backups != backups {
		t.Error("a same-version apply took a snapshot")
	}
}

func TestAReportThatFailsDoesNotFailTheUpdate(t *testing.T) {
	h, _ := twoReleases(t, nil)
	opts := h.opts("1.1.0")
	opts.Version = "1.1.0"
	opts.APIKey = ""
	in := h.installStruct(initwizard.ModeDocker)
	in.DevToken = ""
	if err := runUpdate(context.Background(), opts, in); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.out.String(), "result not sent to the audit log") {
		t.Errorf("output:\n%s", h.out.String())
	}
	if _, err := LoadState(h.root); err != nil {
		t.Fatal(err)
	}
}

func TestBranchPointsToPreviewBuilds(t *testing.T) {
	err := Run(context.Background(), Options{Root: t.TempDir(), Branch: "feat/x"})
	if err == nil || !strings.Contains(err.Error(), "octopus-preview.yml") {
		t.Fatalf("err = %v", err)
	}
}

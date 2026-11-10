package updater

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agentimage"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
	"github.com/MisterDonnuts/thulu/internal/release"
)

func threeReleases(t *testing.T) *harness {
	h, _ := twoReleases(t, nil)
	h.publish("1.2.0", filesV2, func(m *release.Manifest) { m.RollbackTo = "1.1.0" })
	for _, v := range []string{"1.1.0", "1.2.0"} {
		opts := h.opts(v)
		opts.Version = v
		if err := h.update(opts, initwizard.ModeDocker); err != nil {
			t.Fatalf("update to %s: %v\n%s", v, err, h.out.String())
		}
	}
	return h
}

func TestToRollbackToIsImagesOnly(t *testing.T) {
	h, _ := twoReleases(t, nil)
	opts := h.opts("1.1.0")
	opts.Version = "1.1.0"
	if err := h.update(opts, initwizard.ModeDocker); err != nil {
		t.Fatal(err)
	}
	opts = h.opts("1.0.0")
	opts.To = "1.0.0"
	if err := h.update(opts, initwizard.ModeDocker); err != nil {
		t.Fatalf("-to: %v\n%s", err, h.out.String())
	}
	if h.daemon.version != "1.0.0" || len(h.runner.restored) != 0 {
		t.Errorf("daemon %q, restored %d", h.daemon.version, len(h.runner.restored))
	}
	if got := h.read("deploy/docker-compose.standalone.yml"); got != composeV1 {
		t.Errorf("deploy files not taken back:\n%s", got)
	}
	if !strings.Contains(h.read("deploy/.env"), "OCTOPUS_VERSION=1.0.0") {
		t.Error("OCTOPUS_VERSION not moved back")
	}
}

func TestToRollbackToWithRestoreRestores(t *testing.T) {
	h, _ := twoReleases(t, nil)
	opts := h.opts("1.1.0")
	opts.Version = "1.1.0"
	if err := h.update(opts, initwizard.ModeDocker); err != nil {
		t.Fatal(err)
	}
	opts = h.opts("1.0.0")
	opts.To = "1.0.0"
	opts.Restore = "octopus_20261001_120000.sql"
	if err := h.update(opts, initwizard.ModeDocker); err != nil {
		t.Fatalf("-to -restore: %v\n%s", err, h.out.String())
	}
	if h.daemon.version != "1.0.0" || len(h.runner.restored) != 1 {
		t.Errorf("daemon %q, restored %v", h.daemon.version, h.runner.restored)
	}
	if !strings.Contains(h.out.String(), "replaced by octopus_20261001_120000.sql") {
		t.Errorf("the confirmation does not name the restore:\n%s", h.out.String())
	}
}

func TestToFurtherBackNeedsTheSnapshot(t *testing.T) {
	h := threeReleases(t)
	opts := h.opts("1.0.0")
	opts.To = "1.0.0"
	err := h.update(opts, initwizard.ModeDocker)
	if err == nil || !strings.Contains(err.Error(), "thulu update -to 1.0.0 -restore octopus_20261001_120000.sql") {
		t.Fatalf("err = %v", err)
	}
	if h.daemon.version != "1.2.0" {
		t.Error("the refusal changed something")
	}

	opts.Restore = "octopus_20261001_120000.sql"
	if err := h.update(opts, initwizard.ModeDocker); err != nil {
		t.Fatalf("-to -restore: %v\n%s", err, h.out.String())
	}
	if h.daemon.version != "1.0.0" || len(h.runner.restored) != 1 {
		t.Errorf("daemon %q, restored %v", h.daemon.version, h.runner.restored)
	}
	if !h.runner.ran("stop octopus") {
		t.Error("restored with the daemon running")
	}
}

func TestToRefusesARestorePathThatIsNotASnapshot(t *testing.T) {
	h := threeReleases(t)
	opts := h.opts("1.0.0")
	opts.To, opts.Restore = "1.0.0", "nope.sql"
	if err := h.update(opts, initwizard.ModeDocker); err == nil {
		t.Fatal("a missing snapshot was accepted")
	}
	opts.Restore = "external"
	if err := h.update(opts, initwizard.ModeDocker); err == nil || !strings.Contains(err.Error(), "does not run") {
		t.Fatalf("-restore external on a bundled database: %v", err)
	}
}

func TestRollbackGoesToThePreviousVersion(t *testing.T) {
	h := threeReleases(t)
	opts := h.opts("1.1.0")
	opts.Rollback = true
	if err := h.update(opts, initwizard.ModeDocker); err != nil {
		t.Fatalf("-rollback: %v\n%s", err, h.out.String())
	}
	if h.daemon.version != "1.1.0" {
		t.Errorf("daemon = %q", h.daemon.version)
	}
}

func TestToANewerVersionIsAnUpdate(t *testing.T) {
	h, _ := twoReleases(t, nil)
	opts := h.opts("1.1.0")
	opts.To = "1.1.0"
	if err := h.update(opts, initwizard.ModeDocker); err == nil || !strings.Contains(err.Error(), "-version 1.1.0") {
		t.Fatalf("err = %v", err)
	}
}

func TestKeptVersionsListAndPin(t *testing.T) {
	h := threeReleases(t)
	h.out.Reset()
	opts := h.opts("1.2.0")
	opts.ListKept = true
	if err := h.update(opts, initwizard.ModeDocker); err != nil {
		t.Fatal(err)
	}
	out := h.out.String()
	for _, want := range []string{"* 1.2.0", "running", "thulu update -to 1.1.0\n",
		"thulu update -to 1.0.0 -restore octopus_20261001_120000.sql"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing lacks %q:\n%s", want, out)
		}
	}

	opts = h.opts("1.2.0")
	opts.Keep = "1.0.0"
	if err := h.update(opts, initwizard.ModeDocker); err != nil {
		t.Fatal(err)
	}
	st, _ := LoadState(h.root)
	if !st.Kept[2].Pinned {
		t.Errorf("kept = %+v", st.Kept)
	}
	opts.Keep = "0.1.0"
	if err := h.update(opts, initwizard.ModeDocker); err == nil {
		t.Error("pinned a version that is not kept")
	}
}

func TestNextKeptDropsTheOldestUnpinned(t *testing.T) {
	prev := []KeptVersion{
		{Version: "1.2.0", Images: []string{"d@sha256:12"}},
		{Version: "1.1.0", Images: []string{"d@sha256:11"}, Snapshot: "octopus_2_1.sql"},
		{Version: "1.0.0", Images: []string{"d@sha256:10"}, Snapshot: "octopus_1_1.sql", Pinned: true},
		{Version: "0.9.0", Images: []string{"d@sha256:09"}, Snapshot: "octopus_0_1.sql"},
	}
	list, dropped := nextKept(prev,
		KeptVersion{Version: "1.2.0", Images: []string{"ignored"}, Snapshot: "octopus_3_1.sql", SnapshotMethod: BackupArchive},
		KeptVersion{Version: "1.3.0", Images: []string{"d@sha256:13"}}, 3)
	var got []string
	for _, k := range list {
		got = append(got, k.Version)
	}
	if strings.Join(got, ",") != "1.3.0,1.2.0,1.1.0,1.0.0" {
		t.Errorf("list = %v (a pinned version does not count against the limit)", got)
	}
	if list[1].Snapshot != "octopus_3_1.sql" || list[1].Images[0] != "d@sha256:12" {
		t.Errorf("leaving version = %+v", list[1])
	}
	if len(dropped) != 1 || dropped[0].Version != "0.9.0" {
		t.Errorf("dropped = %+v", dropped)
	}
	env := keptEnv(list)
	if env[envKeptVersions] != "1.3.0,1.2.0,1.1.0,1.0.0" || !strings.Contains(env[envKeptImages], "d@sha256:10") ||
		strings.Contains(env[envKeptImages], "d@sha256:09") {
		t.Errorf("env = %v", env)
	}
}

func TestRetentionSparesKeptSnapshots(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "backups")
	writeSnapshots(t, dir, "octopus_20260101_000000.sql", "octopus_20260102_000000.sql", "octopus_20260103_000000.sql")
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(root, &State{Kept: []KeptVersion{{Version: "1.0.0", Snapshot: "octopus_20260101_000000.sql"}}}); err != nil {
		t.Fatal(err)
	}
	old, err := BackupsBeyondRetention(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(old) != 1 || filepath.Base(old[0].Path) != "octopus_20260102_000000.sql" {
		t.Errorf("beyond retention = %+v", old)
	}
}

func TestKubernetesUpgradesWithHelmByDigest(t *testing.T) {
	h, m := twoReleases(t, nil)
	h.write("deploy/values-octopus.yaml", "x: 1\n", 0o600)
	opts := h.opts("1.1.0")
	opts.Version = "1.1.0"
	if err := h.update(opts, initwizard.ModeKubernetes); err == nil || !strings.Contains(err.Error(), "-backup-confirmed") {
		t.Fatalf("an external database without -backup-confirmed: %v", err)
	}
	opts.BackupConfirmed = true
	if err := h.update(opts, initwizard.ModeKubernetes); err != nil {
		t.Fatalf("kube: %v\n%s", err, h.out.String())
	}
	var helm string
	for _, c := range h.runner.cmds {
		if strings.HasPrefix(c, "helm upgrade") {
			helm = c
		}
	}
	_, digest, _ := strings.Cut(m.Images["octopus"], "@")
	for _, want := range []string{"--install octopus", "--atomic", "--wait", "--namespace octopus", "image.digest=" + digest,
		"image.tag=1.1.0", "agentRuntime.engineImages.codex=" + m.Images["octopus-agent-codex"],
		"agentRuntime.sandboxImage=" + m.Images["octopus-sandbox"],
		"mcp.octopusApi.image.tag=1.1.0@sha256:", "-f deploy/values-octopus.yaml"} {
		if !strings.Contains(helm, want) {
			t.Errorf("helm lacks %q:\n%s", want, helm)
		}
	}
	if strings.Contains(helm, "agentRuntime.agentImage") {
		t.Errorf("helm sets the operator's agent image:\n%s", helm)
	}
	if h.backups != 0 || len(h.images.pulled) != 0 {
		t.Error("a cluster upgrade pulled or dumped on this host")
	}
}

func TestKubernetesRollsBackAReleaseThatNeverGetsReady(t *testing.T) {
	h, _ := twoReleases(t, nil)
	h.write("deploy/values-octopus.yaml", "x: 1\n", 0o600)
	h.runner.broken = map[string]bool{"1.1.0": true}
	opts := h.opts("1.1.0")
	opts.Version, opts.BackupConfirmed = "1.1.0", true
	err := h.update(opts, initwizard.ModeKubernetes)
	if err == nil || !h.runner.ran("helm rollback octopus") {
		t.Fatalf("err = %v, cmds = %v", err, h.runner.cmds)
	}
	if ev := h.daemon.lastEvent(t); ev.Result != ResultRolledBack || ev.Mode != "kubernetes" {
		t.Errorf("event = %+v", ev)
	}
}

func TestKubernetesFailureBeforeHelmIsNotARollback(t *testing.T) {
	h, _ := twoReleases(t, nil)
	opts := h.opts("1.1.0")
	opts.Version, opts.BackupConfirmed, opts.ReuseValues = "1.1.0", true, true
	opts.Deps.FetchChart = func(context.Context, string) ([]byte, error) {
		return nil, errors.New("registry unreachable")
	}
	err := h.update(opts, initwizard.ModeKubernetes)
	if err == nil || !strings.Contains(err.Error(), "nothing changed") {
		t.Fatalf("err = %v", err)
	}
	if h.runner.ran("helm upgrade") {
		t.Error("helm ran")
	}
	if ev := h.daemon.lastEvent(t); ev.Result != ResultFailed {
		t.Errorf("event = %+v, want %s", ev, ResultFailed)
	}
}

func TestKubernetesRefusesAnUpgradeWithoutValues(t *testing.T) {
	h, _ := twoReleases(t, nil)
	opts := h.opts("1.1.0")
	opts.Version, opts.BackupConfirmed = "1.1.0", true
	err := h.update(opts, initwizard.ModeKubernetes)
	if err == nil || !strings.Contains(err.Error(), "-reuse-values") {
		t.Fatalf("err = %v, want a refusal that names -reuse-values", err)
	}
	if h.runner.ran("helm") {
		t.Errorf("helm ran: %v", h.runner.cmds)
	}

	opts.ReuseValues = true
	if err := h.update(opts, initwizard.ModeKubernetes); err != nil {
		t.Fatalf("-reuse-values: %v\n%s", err, h.out.String())
	}
	if !h.runner.ran("helm upgrade") || !strings.Contains(lastHelm(h.runner.cmds), "--reuse-values") {
		t.Errorf("helm upgrade without --reuse-values: %v", h.runner.cmds)
	}
}

func TestKubernetesTakesAValuesFile(t *testing.T) {
	h, _ := twoReleases(t, nil)
	values := filepath.Join(t.TempDir(), "prod.yaml")
	if err := os.WriteFile(values, []byte("x: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := h.opts("1.1.0")
	opts.Version, opts.BackupConfirmed = "1.1.0", true
	opts.Values = filepath.Join(filepath.Dir(values), "missing.yaml")
	if err := h.update(opts, initwizard.ModeKubernetes); err == nil || !strings.Contains(err.Error(), "missing.yaml") {
		t.Fatalf("a values file that is not there: %v", err)
	}
	opts.Values = values
	if err := h.update(opts, initwizard.ModeKubernetes); err != nil {
		t.Fatalf("-values: %v\n%s", err, h.out.String())
	}
	if !strings.Contains(lastHelm(h.runner.cmds), "-f "+values) {
		t.Errorf("helm upgrade without -f %s: %v", values, h.runner.cmds)
	}
}

func lastHelm(cmds []string) string {
	var helm string
	for _, c := range cmds {
		if strings.HasPrefix(c, "helm upgrade") {
			helm = c
		}
	}
	return helm
}

func TestTheTargetVersionsCLIRunsTheUpdate(t *testing.T) {
	h, m := twoReleases(t, nil)
	var bin string
	var args []string
	opts := h.opts("1.0.0")
	opts.Version = "1.1.0"
	opts.BackupConfirmed = true
	opts.Deps.Exec = func(_ context.Context, b string, a []string) error {
		bin, args = b, a
		return nil
	}
	if err := h.update(opts, initwizard.ModeDocker); err != nil {
		t.Fatal(err)
	}
	art := m.Binaries[runtime.GOOS+"_"+runtime.GOARCH]
	if sum, err := fileSHA256(bin); err != nil || sum != art.SHA256 {
		t.Errorf("ran %s (sha %s, %v)", bin, sum, err)
	}
	got := strings.Join(args, " ")
	for _, want := range []string{"apply -version 1.1.0", "-delegated", "-backup-confirmed", "-yes", "-root " + h.root} {
		if !strings.Contains(got, want) {
			t.Errorf("args lack %q: %s", want, got)
		}
	}
	if strings.Contains(got, "test-key") {
		t.Error("the API key went on the command line")
	}
	if h.runner.ran("up") {
		t.Error("the old CLI applied the update itself")
	}

	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	h.writeSource("1.1.0", art.File, []byte("evil"))
	bin = ""
	if err := h.update(opts, initwizard.ModeDocker); err == nil || !strings.Contains(err.Error(), "sha256") || bin != "" {
		t.Fatalf("err = %v, ran %q", err, bin)
	}

	opts.NoFetchCLI = true
	if err := h.update(opts, initwizard.ModeDocker); err == nil || !strings.Contains(err.Error(), "-no-fetch-cli") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckWritesTheChangePackageAndChangesNothing(t *testing.T) {
	h, _ := twoReleases(t, nil)
	envBefore := h.read("deploy/.env")
	out := filepath.Join(t.TempDir(), "change.json")
	opts := h.opts("1.1.0")
	opts.Version, opts.Check, opts.Output = "1.1.0", true, out
	if err := h.update(opts, initwizard.ModeDocker); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var pkg map[string]any
	if err := json.Unmarshal(b, &pkg); err != nil {
		t.Fatal(err)
	}
	if pkg["from"] != "1.0.0" || pkg["to"] != "1.1.0" {
		t.Errorf("package = %s", b)
	}
	if info, _ := os.Stat(out); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode())
	}
	if h.read("deploy/.env") != envBefore || len(h.runner.cmds) != 0 || len(h.images.pulled) != 0 {
		t.Error("-check changed the install")
	}

	opts.Output = filepath.Join(t.TempDir(), "change.md")
	if err := h.update(opts, initwizard.ModeDocker); err != nil {
		t.Fatal(err)
	}
	opts.Output = "change.txt"
	if err := h.update(opts, initwizard.ModeDocker); err == nil {
		t.Error("wrote a change package in an unknown format")
	}
}

func TestLockKeepsASecondUpdateOut(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o750); err != nil {
		t.Fatal(err)
	}
	unlock, err := acquireLock(root, "a@h", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLock(root, "b@h", time.Now()); err == nil {
		t.Fatal("two updates held the lock")
	}
	unlock()
	unlock2, err := acquireLock(root, "b@h", time.Now())
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	unlock2()

	host, _ := os.Hostname()
	stale, _ := json.Marshal(lockInfo{PID: 1 << 30, Host: host, Operator: "x", Started: time.Now()})
	if err := os.WriteFile(filepath.Join(root, LockFile), stale, 0o600); err != nil {
		t.Fatal(err)
	}
	guard := filepath.Join(root, LockFile+".takeover")
	if err := os.WriteFile(guard, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLock(root, "c@h", time.Now()); err == nil {
		t.Fatal("took over a lock another run was taking over")
	}
	if err := os.Remove(guard); err != nil {
		t.Fatal(err)
	}
	unlock3, err := acquireLock(root, "c@h", time.Now())
	if err != nil {
		t.Fatalf("stale lock not taken over: %v", err)
	}
	if _, err := os.Stat(guard); !os.IsNotExist(err) {
		t.Error("the takeover guard was left behind")
	}
	unlock3()
}

func TestRemoveStaleLeavesALockThatChanged(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, LockFile)
	stale := lockInfo{PID: 1 << 30, Host: "h", Started: time.Unix(1, 0).UTC()}
	live, _ := json.Marshal(lockInfo{PID: os.Getpid(), Host: "h", Started: time.Now().UTC()})
	if err := os.WriteFile(path, live, 0o600); err != nil {
		t.Fatal(err)
	}
	if removeStale(path, stale) {
		t.Fatal("removed a lock that is no longer the stale one")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the live lock is gone: %v", err)
	}
}

func TestEnvBlockReplacesTheKeysItOwns(t *testing.T) {
	raw := "OCTOPUS_VERSION=1.0.0\nOCTOPUS_IMAGE=old\nOCTOPUS_ROLLBACK_IMAGE=x\nKEEP=me\n"
	got, disabled, err := writeBlock(raw, map[string]string{envDaemonImage: "reg/octopus@sha256:1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(disabled, ",") != "OCTOPUS_IMAGE,OCTOPUS_ROLLBACK_IMAGE" {
		t.Errorf("disabled = %v", disabled)
	}
	if !strings.Contains(got, "KEEP=me") || !strings.Contains(got, "# OCTOPUS_IMAGE=old") ||
		readBlock(got)[envDaemonImage] != "reg/octopus@sha256:1" {
		t.Errorf("block:\n%s", got)
	}
	again, _, err := writeBlock(got, map[string]string{envDaemonImage: "reg/octopus@sha256:1"})
	if err != nil || again != got {
		t.Errorf("rewrite is not idempotent:\n%s", again)
	}
	if _, _, err := writeBlock(raw, map[string]string{envDaemonImage: "x\nEVIL=1"}); err == nil {
		t.Error("a value with a newline was written")
	}
	if _, _, err := writeBlock(raw, map[string]string{"NOT_MANAGED": "x"}); err == nil {
		t.Error("a key outside the block was written")
	}
}

func TestEnvBlockMovesFromTheOneAgentImage(t *testing.T) {
	old := "OCTOPUS_VERSION=0.3.2\n" + blockStart + ": x\n" +
		"OCTOPUS_IMAGE=reg/octopus@sha256:1\nOCTOPUS_AGENT_IMAGE=reg/octopus-agent@sha256:2\n" + blockEnd + "\n"
	values := map[string]string{
		envDaemonImage:                "reg/octopus@sha256:3",
		agentimage.Env("claude-code"): "reg/octopus-agent-claude-code@sha256:4",
		agentimage.Env("codex"):       "reg/octopus-agent-codex@sha256:5",
		agentimage.Env("opencode"):    "reg/octopus-agent-opencode@sha256:6",
		envKeptImages:                 "reg/octopus@sha256:1,reg/octopus-agent@sha256:2",
	}
	got, disabled, err := writeBlock(old, values)
	if err != nil {
		t.Fatal(err)
	}
	block := readBlock(got)
	if _, ok := block[agentimage.EnvOverride]; ok || len(disabled) != 0 {
		t.Errorf("the old pin survived (disabled %v):\n%s", disabled, got)
	}
	if block["OCTOPUS_AGENT_CODEX_IMAGE"] != "reg/octopus-agent-codex@sha256:5" {
		t.Errorf("block:\n%s", got)
	}
	if imgs := blockImages(got, func(string) string { return "" }); len(imgs) != 4 {
		t.Errorf("blockImages = %v, want the daemon and the three engines", imgs)
	}

	custom := "OCTOPUS_AGENT_IMAGE=acme/agent:1\n" + old
	got, disabled, err = writeBlock(custom, values)
	if err != nil || len(disabled) != 0 || !strings.HasPrefix(got, "OCTOPUS_AGENT_IMAGE=acme/agent:1\n") {
		t.Errorf("the operator's image was not left alone (disabled %v, err %v):\n%s", disabled, err, got)
	}
}

func TestPostEventReportsAFailure(t *testing.T) {
	h := newHarness(t)
	err := postEvent(context.Background(), h.daemon.srv.Client(), h.daemon.srv.URL+"/nowhere", "", "k", Event{})
	if err == nil {
		t.Fatal("a 404 counted as recorded")
	}
	if strings.Contains(err.Error(), "k") && strings.Contains(err.Error(), "Bearer") {
		t.Error("the error carries the key")
	}
}

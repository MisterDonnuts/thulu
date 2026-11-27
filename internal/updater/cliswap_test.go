package updater

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/clicopy"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
	"github.com/MisterDonnuts/thulu/internal/release"
)

// realTemp is a temp dir with its symlinks resolved (/var is /private/var on macOS), as the paths
// the sync prints are.
func realTemp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// Fake CLIs are files "thulu <version>": IsThulu and Version read them instead of running them.
func fakeCLI(t *testing.T, path, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("thulu "+version), 0o755); err != nil {
		t.Fatal(err)
	}
}

func fakeVersion(path string) string {
	b, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(b), "thulu ") {
		return ""
	}
	return strings.TrimPrefix(string(b), "thulu ")
}

type cliFixture struct {
	self    string
	pathDir []string
	service string
}

func (f *cliFixture) ops() *CLIOps {
	return &CLIOps{
		Executable: func() (string, error) { return f.self, nil },
		PathList:   func() string { return strings.Join(f.pathDir, string(os.PathListSeparator)) },
		IsThulu:    func(p string) bool { return fakeVersion(p) != "" },
		Version:    func(_ context.Context, p string) string { return fakeVersion(p) },
		Replace:    clicopy.Replace,
		ServiceBinary: func(context.Context) (string, bool) {
			return f.service, f.service != ""
		},
	}
}

func syncRun(t *testing.T, f *cliFixture, sync bool) (*updateRun, *bytes.Buffer) {
	t.Helper()
	t.Setenv(InvokedCLIEnv, "")
	var out bytes.Buffer
	return &updateRun{
		opts: Options{SyncCLI: sync},
		deps: &Deps{SelfVersion: "1.2.0", CLI: f.ops()},
		in:   &Install{Root: t.TempDir()},
		out:  &out,
	}, &out
}

func TestSyncCLIReplacesEveryOldCopyOnPath(t *testing.T) {
	base := realTemp(t)
	self := filepath.Join(base, "fetched", "thulu")
	fakeCLI(t, self, "1.2.0")
	local := filepath.Join(base, "home", ".local", "bin")
	usr := filepath.Join(base, "usr", "local", "bin")
	current := filepath.Join(base, "opt", "bin")
	fakeCLI(t, filepath.Join(local, "thulu"), "1.1.0")
	// octopus from before the rename, a symlink to the same file: replaced once, through its target.
	if err := os.Symlink(filepath.Join(local, "thulu"), filepath.Join(local, "octopus")); err != nil {
		t.Fatal(err)
	}
	fakeCLI(t, filepath.Join(usr, "octopus"), "1.0.0")
	fakeCLI(t, filepath.Join(current, "thulu"), "1.2.0")
	if err := os.WriteFile(filepath.Join(usr, "thulu"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	f := &cliFixture{self: self, pathDir: []string{local, "relative/dir", usr, current}}
	u, out := syncRun(t, f, true)
	u.syncCLI(t.Context(), &release.Manifest{Version: "1.2.0"}, "")

	for _, p := range []string{filepath.Join(local, "thulu"), filepath.Join(usr, "octopus")} {
		if v := fakeVersion(p); v != "1.2.0" {
			t.Errorf("%s is %q after the sync", p, v)
		}
	}
	if fi, err := os.Lstat(filepath.Join(local, "octopus")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the octopus symlink must stay a symlink: %v %v", fi, err)
	}
	if b, _ := os.ReadFile(filepath.Join(usr, "thulu")); string(b) != "#!/bin/sh\n" {
		t.Error("a file called thulu that is not Thulu must be left alone")
	}
	got := out.String()
	for _, want := range []string{
		"Replaced the CLI at " + filepath.Join(local, "thulu") + ": 1.1.0 → 1.2.0",
		"Replaced the CLI at " + filepath.Join(usr, "octopus") + ": 1.0.0 → 1.2.0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "Replaced") != 2 {
		t.Errorf("one line per file replaced:\n%s", got)
	}
}

func TestSyncCLISaysLoudlyWhatToRunForAnUnwritableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	base := realTemp(t)
	self := filepath.Join(base, "self", "thulu")
	fakeCLI(t, self, "1.2.0")
	locked := filepath.Join(base, "usr", "local", "bin")
	fakeCLI(t, filepath.Join(locked, "thulu"), "1.1.0")
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	u, out := syncRun(t, &cliFixture{self: self, pathDir: []string{locked}}, true)
	u.syncCLI(t.Context(), &release.Manifest{Version: "1.2.0"}, "")

	got := out.String()
	dst := filepath.Join(locked, "thulu")
	for _, want := range []string{
		"!! The CLI at " + dst + " is still 1.1.0 (this update installed 1.2.0): " + locked + " is not writable",
		clicopy.ManualCommand(self, dst),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if fakeVersion(dst) != "1.1.0" {
		t.Error("the locked copy changed")
	}
}

func TestSyncCLIReplacesTheBinaryThePersonRan(t *testing.T) {
	base := realTemp(t)
	src := filepath.Join(base, "fetched", "thulu")
	fakeCLI(t, src, "1.2.0")
	ran := filepath.Join(base, "Downloads", "thulu")
	fakeCLI(t, ran, "1.1.0")
	f := &cliFixture{self: src}
	u, _ := syncRun(t, f, true)
	t.Setenv(InvokedCLIEnv, ran)
	u.opts.Delegated = true
	u.syncCLI(t.Context(), &release.Manifest{Version: "1.2.0"}, src)
	if fakeVersion(ran) != "1.2.0" {
		t.Fatalf("the CLI the person ran, off PATH, must be updated too: %q", fakeVersion(ran))
	}
}

func TestSyncCLITellsWhatTheRunnerServiceNeeds(t *testing.T) {
	base := realTemp(t)
	self := filepath.Join(base, "self", "thulu")
	fakeCLI(t, self, "1.2.0")
	bin := filepath.Join(base, "bin")
	fakeCLI(t, filepath.Join(bin, "thulu"), "1.1.0")

	f := &cliFixture{self: self, pathDir: []string{bin}, service: filepath.Join(bin, "thulu")}
	u, out := syncRun(t, f, true)
	u.syncCLI(t.Context(), &release.Manifest{Version: "1.2.0"}, "")
	if !strings.Contains(out.String(), "thulu runner service restart") {
		t.Fatalf("a service running a replaced file needs a restart:\n%s", out.String())
	}

	stale := filepath.Join(base, "old", "octopus")
	fakeCLI(t, stale, "1.0.0")
	f = &cliFixture{self: self, service: stale}
	u, out = syncRun(t, f, true)
	u.syncCLI(t.Context(), &release.Manifest{Version: "1.2.0"}, "")
	if !strings.Contains(out.String(), "still 1.0.0") || !strings.Contains(out.String(), "thulu runner service install") {
		t.Fatalf("a service on a copy off PATH needs a reinstall:\n%s", out.String())
	}
}

func TestSyncCLIOnlyWhenTheRealCLIAsks(t *testing.T) {
	base := realTemp(t)
	self := filepath.Join(base, "self", "thulu")
	fakeCLI(t, self, "1.2.0")
	bin := filepath.Join(base, "bin")
	fakeCLI(t, filepath.Join(bin, "thulu"), "1.1.0")
	u, out := syncRun(t, &cliFixture{self: self, pathDir: []string{bin}}, false)
	u.syncCLI(t.Context(), &release.Manifest{Version: "1.2.0"}, "")
	if fakeVersion(filepath.Join(bin, "thulu")) != "1.1.0" || out.Len() != 0 {
		t.Fatal("without SyncCLI the update never touches the binaries on PATH")
	}
}

func TestSyncCLILeavesDevBuildsAlone(t *testing.T) {
	base := realTemp(t)
	self := filepath.Join(base, "self", "thulu")
	fakeCLI(t, self, "1.2.0")
	bin := filepath.Join(base, "bin")
	fakeCLI(t, filepath.Join(bin, "thulu"), "dev")
	u, out := syncRun(t, &cliFixture{self: self, pathDir: []string{bin}}, true)
	u.syncCLI(t.Context(), &release.Manifest{Version: "1.2.0"}, "")
	if fakeVersion(filepath.Join(bin, "thulu")) != "dev" || !strings.Contains(out.String(), "development build") {
		t.Fatalf("a dev build on PATH is someone's checkout:\n%s", out.String())
	}
}

func TestVersionsDrift(t *testing.T) {
	v := Versions{Self: "1.2.0",
		Copies:  []clicopy.Copy{{Path: "/a/thulu", Version: "1.2.0"}, {Path: "/b/thulu", Version: ""}},
		Service: &clicopy.Copy{Path: "/c/thulu", Version: "1.1.0"},
		Stack:   "1.2.0", StackURL: "http://x"}
	lines := v.Drift()
	got := strings.Join(lines, "\n")
	for _, want := range []string{"/b/thulu is unknown", "runner service runs /c/thulu, which is 1.1.0"} {
		if !strings.Contains(got, want) {
			t.Errorf("drift lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "/a/thulu") || strings.Contains(got, "the stack at") {
		t.Errorf("what agrees is not drift:\n%s", got)
	}
	if len((Versions{Self: "1.2.0"}).Drift()) != 0 {
		t.Error("nothing to compare, nothing to say")
	}
}

func TestReadVersionsSkipsThisCLI(t *testing.T) {
	base := realTemp(t)
	self := filepath.Join(base, "bin", "thulu")
	fakeCLI(t, self, "1.2.0")
	other := filepath.Join(base, "other", "octopus")
	fakeCLI(t, other, "1.1.0")
	t.Setenv(InvokedCLIEnv, "")
	f := &cliFixture{self: self, pathDir: []string{filepath.Dir(self), filepath.Dir(other)}, service: self}
	v := ReadVersions(t.Context(), "1.2.0", "", f.ops())
	if len(v.Copies) != 1 || v.Copies[0].Path != other || v.Copies[0].Version != "1.1.0" {
		t.Fatalf("copies = %+v", v.Copies)
	}
	if v.Service == nil || v.Service.Version != "1.2.0" {
		t.Fatalf("service = %+v", v.Service)
	}
}

// harnessCLI gives an update run a PATH with an old CLI on it, and a fake new one as this process.
func harnessCLI(t *testing.T, opts *Options) (onPath string) {
	t.Helper()
	base := realTemp(t)
	self := filepath.Join(base, "self", "thulu")
	fakeCLI(t, self, "1.1.0")
	onPath = filepath.Join(base, "bin", "thulu")
	fakeCLI(t, onPath, "1.0.0")
	t.Setenv(InvokedCLIEnv, "")
	opts.SyncCLI = true
	opts.Deps.CLI = (&cliFixture{self: self, pathDir: []string{filepath.Dir(onPath)}}).ops()
	return onPath
}

func TestUpdateReplacesTheCLIOnPathAfterItSucceeds(t *testing.T) {
	h, _ := twoReleases(t, nil)
	opts := h.opts("1.1.0")
	opts.Version = "1.1.0"
	onPath := harnessCLI(t, &opts)
	if err := h.update(opts, initwizard.ModeDocker); err != nil {
		t.Fatalf("update: %v\n%s", err, h.out.String())
	}
	if fakeVersion(onPath) != "1.1.0" || !strings.Contains(h.out.String(), "Replaced the CLI at "+onPath) {
		t.Fatalf("CLI on PATH = %q\n%s", fakeVersion(onPath), h.out.String())
	}
}

func TestUpdateLeavesTheCLIWhenItRollsBack(t *testing.T) {
	h, _ := twoReleases(t, nil)
	h.runner.broken = map[string]bool{"1.1.0": true}
	opts := h.opts("1.1.0")
	opts.Version = "1.1.0"
	onPath := harnessCLI(t, &opts)
	if err := h.update(opts, initwizard.ModeDocker); err == nil {
		t.Fatal("the broken release must fail")
	}
	if fakeVersion(onPath) != "1.0.0" {
		t.Fatalf("a rolled-back update must not move the CLI: %q", fakeVersion(onPath))
	}
}

// The case of the 1.1.0 report: the stack is already on the newest release, but the CLI on PATH is
// not. "Already on" must still bring the CLI along.
func TestUpdateAlreadyOnStillFixesTheCLI(t *testing.T) {
	h, _ := twoReleases(t, nil)
	opts := h.opts("1.1.0")
	opts.Version = "1.1.0"
	if err := h.update(opts, initwizard.ModeDocker); err != nil {
		t.Fatalf("first update: %v\n%s", err, h.out.String())
	}
	h.out.Reset()
	opts = h.opts("1.1.0")
	onPath := harnessCLI(t, &opts)
	if err := h.update(opts, initwizard.ModeDocker); err != nil {
		t.Fatalf("second update: %v\n%s", err, h.out.String())
	}
	if !strings.Contains(h.out.String(), "Already on 1.1.0") || fakeVersion(onPath) != "1.1.0" {
		t.Fatalf("CLI on PATH = %q\n%s", fakeVersion(onPath), h.out.String())
	}
}

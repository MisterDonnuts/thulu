package sandbox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/project"
)

func diskReader(dir string) project.SetupFileReader {
	return func(rel string) ([]byte, bool, error) {
		p := filepath.Join(dir, rel)
		info, err := os.Stat(p)
		if errors.Is(err, os.ErrNotExist) || (err == nil && !info.Mode().IsRegular()) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		b, err := os.ReadFile(p)
		return b, err == nil, err
	}
}

func shellSetupKey(t *testing.T, m project.InitManifest, work string) string {
	t.Helper()
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "setup.sh"), []byte(m.Setup), 0o644); err != nil {
		t.Fatal(err)
	}
	prep := setupPrepareScript(setupSpec{dir: d, repo: work, projectID: "p1", manifest: m})
	if err := os.WriteFile(filepath.Join(d, "prepare.sh"), []byte(prep), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `. "$1/prepare.sh" && octopus_setup_key`, "key", d)
	cmd.Dir = work
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shell key: %s: %v", out, err)
	}
	return strings.TrimSpace(string(out))
}

func requireBins(t *testing.T, bins ...string) {
	t.Helper()
	for _, b := range bins {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s not available", b)
		}
	}
}

func TestSetupKeyGoMatchesShell(t *testing.T) {
	requireBins(t, "bash", "sha256sum", "cut")
	cases := []struct {
		name  string
		m     project.InitManifest
		files map[string]string
	}{
		{
			name:  "lock file and tools",
			m:     project.InitManifest{Setup: "npm ci\n", Tools: map[string]string{"node": "22", "go": "1.25.1"}, Cache: project.InitCache{Key: []string{"package-lock.json"}}},
			files: map[string]string{"package-lock.json": `{"lockfileVersion":3}`},
		},
		{
			name: "missing key file, implicit mise files, odd names",
			m: project.InitManifest{
				Setup: "set -e\npip install -r requirements.txt",
				Cache: project.InitCache{Key: []string{"requirements.txt", "absent.lock", "dir with space/it's.lock", ".mise.toml"}},
			},
			files: map[string]string{
				"requirements.txt":         "flask==3\n",
				"dir with space/it's.lock": "x",
				".tool-versions":           "python 3.12.1\n",
				"mise.toml":                "[tools]\nnode = \"22\"\n",
				".mise.toml":               "",
			},
		},
		{
			name:  "no key files at all",
			m:     project.InitManifest{Setup: "make deps"},
			files: map[string]string{},
		},
		{
			name:  "support files",
			m:     project.InitManifest{Setup: "npm ci", Files: map[string]string{"seed.mjs": "x", "dir/it's.sh": "y\n"}, Cache: project.InitCache{Key: []string{"package-lock.json"}}},
			files: map[string]string{"package-lock.json": "{}"},
		},
		{
			name:  "binary content and a symlink",
			m:     project.InitManifest{Setup: "true", Cache: project.InitCache{Key: []string{"bin.lock", "link.lock", "sub"}}},
			files: map[string]string{"bin.lock": "\x00\xff\x01\n\n", "real.lock": "r", "sub/x": "dir, not a file"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			work := t.TempDir()
			for name, content := range c.files {
				p := filepath.Join(work, name)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if c.name == "binary content and a symlink" {
				if err := os.Symlink("real.lock", filepath.Join(work, "link.lock")); err != nil {
					t.Fatal(err)
				}
			}
			want, err := project.SetupKey(c.m, diskReader(work))
			if err != nil {
				t.Fatal(err)
			}
			if got := shellSetupKey(t, c.m, work); got != want {
				text, _ := project.SetupKeyText(c.m, diskReader(work))
				t.Fatalf("shell key %s != Go key %s; Go text:\n%s", got, want, text)
			}
		})
	}
}

type setupRig struct {
	*initRig
	cache string
	count string
}

func newSetupRig(t *testing.T, setup string, withCache bool) *setupRig {
	t.Helper()
	requireBins(t, "sha256sum", "tar")
	cache := ""
	if withCache {
		cache = t.TempDir()
	}
	t.Setenv("OCTOPUS_CACHE_DIR", cache)
	count := filepath.Join(t.TempDir(), "count")
	t.Setenv("SETUP_COUNT", count)
	m := project.InitManifest{
		Setup: setup,
		Cache: project.InitCache{Key: []string{"package-lock.json"}, Paths: []string{"node_modules"}},
	}
	r := newInitRig(t, initScript("test -f node_modules/dep/index.js && echo started", m))
	if err := os.WriteFile(filepath.Join(r.work, "package-lock.json"), []byte(`{"v":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return &setupRig{initRig: r, cache: cache, count: count}
}

const fakeInstall = `echo install >> "$SETUP_COUNT"
mkdir -p node_modules/dep && echo 'module.exports=1' > node_modules/dep/index.js`

func (r *setupRig) run(t *testing.T) InitRunResult {
	t.Helper()
	var res InitRunResult
	text, isErr := r.call(t, "init_run", `{"project":"app"}`, nil)
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatalf("init_run: %s", text)
	}
	if isErr && res.State == InitStateReady {
		t.Fatalf("ready but an error: %s", text)
	}
	return res
}

func (r *setupRig) installs(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile(r.count)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "install")
}

func TestInitRun_SetupRunsThenSkips(t *testing.T) {
	r := newSetupRig(t, fakeInstall, true)

	first := r.run(t)
	if first.State != InitStateReady || first.Setup == nil || first.Setup.Status != SetupStateRan || first.Setup.Key == "" {
		t.Fatalf("first run = %+v / %+v", first, first.Setup)
	}
	if !strings.Contains(first.LogTail, "install") && r.installs(t) != 1 {
		t.Fatalf("setup did not run: %s", first.LogTail)
	}
	want, _ := project.SetupKey(r.scripts.projects[0].Script.Manifest, diskReader(r.work))
	if first.Setup.Key != want {
		t.Fatalf("run key %s != Go key %s", first.Setup.Key, want)
	}

	second := r.run(t)
	if second.State != InitStateReady || second.Setup == nil || second.Setup.Status != SetupStateSkipped || r.installs(t) != 1 {
		t.Fatalf("second run = %+v / %+v, installs %d", second, second.Setup, r.installs(t))
	}

	if err := os.WriteFile(filepath.Join(r.work, "package-lock.json"), []byte(`{"v":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	third := r.run(t)
	if third.Setup == nil || third.Setup.Status != SetupStateRan || third.Setup.Key == first.Setup.Key || r.installs(t) != 2 {
		t.Fatalf("after a lock change = %+v, installs %d", third.Setup, r.installs(t))
	}

	if err := os.RemoveAll(filepath.Join(r.work, "node_modules")); err != nil {
		t.Fatal(err)
	}
	fourth := r.run(t)
	if fourth.State != InitStateReady || fourth.Setup.Status != SetupStateRan || r.installs(t) != 3 {
		t.Fatalf("after node_modules went = %+v, installs %d", fourth.Setup, r.installs(t))
	}
	if _, err := os.Stat(filepath.Join(r.work, "octopus-setup.marker")); err == nil {
		t.Fatal("the marker landed in the worktree")
	}
}

func TestInitRun_SetupWithoutCacheRunsEveryTime(t *testing.T) {
	r := newSetupRig(t, fakeInstall, false)
	for i := 1; i <= 2; i++ {
		res := r.run(t)
		if res.State != InitStateReady || res.Setup == nil || res.Setup.Status != SetupStateRan || !res.Setup.NoCache {
			t.Fatalf("run %d = %+v / %+v", i, res, res.Setup)
		}
		if r.installs(t) != i {
			t.Fatalf("run %d: %d installs", i, r.installs(t))
		}
	}
}

func TestInitRun_SetupFailureStopsBeforeStart(t *testing.T) {
	r := newSetupRig(t, "echo npm ERR >&2; exit 9", true)
	res := r.run(t)
	if res.State != InitStateFailed || res.ExitCode == nil || *res.ExitCode != 9 {
		t.Fatalf("result = %+v", res)
	}
	if res.Setup == nil || res.Setup.Status != SetupStateFailed || res.Setup.ExitCode == nil || *res.Setup.ExitCode != 9 {
		t.Fatalf("setup = %+v", res.Setup)
	}
	if strings.Contains(res.LogTail, "started") || !strings.Contains(res.LogTail, "npm ERR") || res.Message == "" {
		t.Fatalf("log/message = %q / %q", res.LogTail, res.Message)
	}
}

func TestPrewarmThenRestore(t *testing.T) {
	requireBins(t, "zstd")
	r := newSetupRig(t, fakeInstall, true)
	old := prewarmPollEvery
	prewarmPollEvery = 50 * time.Millisecond
	t.Cleanup(func() { prewarmPollEvery = old })
	m := r.scripts.projects[0].Script.Manifest
	spec := PrewarmSpec{WorkspaceID: "ws-1", ProjectID: "p-app", Name: "App", Dir: "app", Manifest: m, HostPath: r.work}

	res, err := r.h.mgr.Prewarm(t.Context(), spec)
	if err != nil {
		t.Fatalf("prewarm: %v", err)
	}
	want, _ := project.SetupKey(m, diskReader(r.work))
	snap := filepath.Join(r.cache, "snapshots", "p-app", want+".tar.zst")
	if res.Key != want || res.SizeBytes <= 0 || res.Reused {
		t.Fatalf("prewarm = %+v, want key %s", res, want)
	}
	if _, err := os.Stat(snap); err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
	usage, err := r.h.mgr.store.cacheUsagePerWorkspace(t.Context(), time.Unix(0, 0), true)
	if err != nil || len(usage) != 1 || usage[0].Source != CacheSourcePrewarm || usage[0].TotalBytes <= 0 ||
		len(usage[0].Snapshots) != 1 || usage[0].Snapshots[0].ProjectID != "p-app" {
		t.Fatalf("measurement after the prewarm = %+v, %v", usage, err)
	}
	if again, err := r.h.mgr.Prewarm(t.Context(), spec); err != nil || !again.Reused || r.installs(t) != 1 {
		t.Fatalf("second prewarm = %+v, %v, installs %d", again, err, r.installs(t))
	}

	if err := os.RemoveAll(filepath.Join(r.work, "node_modules")); err != nil {
		t.Fatal(err)
	}
	run := r.run(t)
	if run.State != InitStateReady || run.Setup == nil || run.Setup.Status != SetupStateRestored || run.Setup.Snapshot != want {
		t.Fatalf("task run = %+v / %+v", run, run.Setup)
	}
	if r.installs(t) != 1 {
		t.Fatalf("the task installed: %d", r.installs(t))
	}
	if next := r.run(t); next.Setup.Status != SetupStateSkipped {
		t.Fatalf("after a restore = %+v", next.Setup)
	}

	var pruned []string
	for _, v := range []string{`{"v":2}`, `{"v":3}`} {
		if err := os.WriteFile(filepath.Join(r.work, "package-lock.json"), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
		// ls -t orders by mtime; each snapshot must be at least a second newer than the last.
		time.Sleep(1100 * time.Millisecond)
		res, err := r.h.mgr.Prewarm(t.Context(), spec)
		if err != nil {
			t.Fatalf("prewarm %s: %v", v, err)
		}
		pruned = append(pruned, res.Pruned...)
	}
	left, _ := filepath.Glob(filepath.Join(r.cache, "snapshots", "p-app", "*.tar.zst"))
	if len(left) != 2 || len(pruned) != 1 || pruned[0] != want {
		t.Fatalf("snapshots left %v, pruned %v (want %s)", left, pruned, want)
	}

	if _, err := r.h.mgr.store.Get(t.Context(), PrewarmOwner("p-app")); err == nil {
		t.Fatal("the prewarm sandbox row is still there")
	}
}

func TestPrewarmFromArchive(t *testing.T) {
	requireBins(t, "zstd", "base64")
	r := newSetupRig(t, fakeInstall, true)
	old := prewarmPollEvery
	prewarmPollEvery = 50 * time.Millisecond
	t.Cleanup(func() { prewarmPollEvery = old })
	m := r.scripts.projects[0].Script.Manifest

	// Random bytes do not compress, so the archive spans three chunks.
	big := make([]byte, prewarmChunk*5/2)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	archive := func() (io.ReadCloser, error) {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(zw)
		for name, body := range map[string]string{"package-lock.json": `{"v":"archive"}`, "big.bin": string(big)} {
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
				return nil, err
			}
			if _, err := io.WriteString(tw, body); err != nil {
				return nil, err
			}
		}
		if err := tw.Close(); err != nil {
			return nil, err
		}
		if err := zw.Close(); err != nil {
			return nil, err
		}
		return io.NopCloser(&buf), nil
	}
	res, err := r.h.mgr.Prewarm(t.Context(), PrewarmSpec{ProjectID: "p-arch", Name: "Arch", Dir: "arch", Manifest: m, Archive: archive})
	if err != nil {
		t.Fatalf("prewarm: %v", err)
	}
	src := filepath.Join(initProjectsRoot, "arch")
	if b, _ := os.ReadFile(filepath.Join(src, "big.bin")); !bytes.Equal(b, big) {
		t.Fatalf("big.bin arrived with %d bytes, want %d", len(b), len(big))
	}
	want, _ := project.SetupKey(m, diskReader(src))
	if res.Key != want {
		t.Fatalf("key %s, want %s", res.Key, want)
	}
	if _, err := os.Stat(filepath.Join(r.cache, "snapshots", "p-arch", want+".tar.zst")); err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
}

func TestPrewarmFailures(t *testing.T) {
	requireBins(t, "zstd")
	old := prewarmPollEvery
	prewarmPollEvery = 50 * time.Millisecond
	t.Cleanup(func() { prewarmPollEvery = old })

	t.Run("setup fails", func(t *testing.T) {
		r := newSetupRig(t, "echo resolver blew up; exit 3", true)
		m := r.scripts.projects[0].Script.Manifest
		_, err := r.h.mgr.Prewarm(t.Context(), PrewarmSpec{ProjectID: "p-app", Dir: "app", Manifest: m, HostPath: r.work})
		if apperr.KindOf(err) != apperr.KindInvalid || !strings.Contains(apperr.Text(err), "resolver blew up") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no cache", func(t *testing.T) {
		r := newSetupRig(t, fakeInstall, false)
		m := r.scripts.projects[0].Script.Manifest
		_, err := r.h.mgr.Prewarm(t.Context(), PrewarmSpec{ProjectID: "p-app", Dir: "app", Manifest: m, HostPath: r.work})
		if apperr.KindOf(err) != apperr.KindUnavailable {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("nothing to snapshot", func(t *testing.T) {
		r := newSetupRig(t, "true", true)
		m := r.scripts.projects[0].Script.Manifest
		_, err := r.h.mgr.Prewarm(t.Context(), PrewarmSpec{ProjectID: "p-app", Dir: "app", Manifest: m, HostPath: r.work})
		if apperr.KindOf(err) != apperr.KindInvalid || !strings.Contains(apperr.Text(err), "node_modules") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("not eligible", func(t *testing.T) {
		r := newSetupRig(t, fakeInstall, true)
		_, err := r.h.mgr.Prewarm(t.Context(), PrewarmSpec{ProjectID: "p-app", Dir: "app", Manifest: project.InitManifest{Setup: "x"}, HostPath: r.work})
		if apperr.KindOf(err) != apperr.KindInvalid {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestPrewarmOwner(t *testing.T) {
	if id, ok := PrewarmProjectID(PrewarmOwner("abc")); !ok || id != "abc" {
		t.Fatalf("round trip = %q %v", id, ok)
	}
	for _, owner := range []string{"task-1", "auto:run", "prewarm:"} {
		if _, ok := PrewarmProjectID(owner); ok {
			t.Fatalf("%q read as a prewarm owner", owner)
		}
	}
}

func TestParseSetupProbe(t *testing.T) {
	p := parseInitProbe("alive=1\nexit=\nphase=setup\nstart_at=\nsetup_tools=installed\ntools_ms=1200\nsetup_key=abc\nready=-\n")
	if p.phase != "setup" || p.setup == nil || p.setup.Status != SetupStateRunning || p.setup.Tools != "installed" || p.setup.ToolsMs != 1200 {
		t.Fatalf("probe = %+v / %+v", p, p.setup)
	}
	p = parseInitProbe("alive=1\nexit=\nphase=start\nstart_at=1700000000\nsetup_tools=none\nsetup_status=none\nready=1\n")
	if p.setup != nil || p.startAt != 1700000000 {
		t.Fatalf("a run without setup reported %+v", p.setup)
	}
	if s := setupSummary(&InitSetupReport{Status: SetupStateRestored, Snapshot: "0123456789abcdef", RestoreMs: 1500}); !strings.Contains(s, "0123456789ab") || strings.Contains(s, "cdef") {
		t.Fatalf("summary = %q", s)
	}
}

func TestInitRun_SupportFiles(t *testing.T) {
	r := newSetupRig(t, `bash "$OCTOPUS_INIT_FILES/install.sh"`, true)
	sc := r.scripts.projects[0].Script
	sc.Script = `test -f node_modules/dep/index.js && bash "$OCTOPUS_INIT_FILES/tools/hello.sh"`
	sc.Manifest.Files = map[string]string{
		"install.sh":     fakeInstall,
		"tools/hello.sh": "echo started from octopus",
	}
	first := r.run(t)
	if first.State != InitStateReady || first.Setup == nil || first.Setup.Status != SetupStateRan || r.installs(t) != 1 {
		t.Fatalf("first run = %+v / %+v", first, first.Setup)
	}
	if !strings.Contains(first.LogTail, "started from octopus") {
		t.Fatalf("the script did not read its support file: %s", first.LogTail)
	}
	if _, err := os.Stat(filepath.Join(r.work, "install.sh")); err == nil {
		t.Fatal("a support file landed in the checkout")
	}
	want, _ := project.SetupKey(sc.Manifest, diskReader(r.work))
	if first.Setup.Key != want {
		t.Fatalf("run key %s != Go key %s", first.Setup.Key, want)
	}

	if second := r.run(t); second.Setup == nil || second.Setup.Status != SetupStateSkipped || r.installs(t) != 1 {
		t.Fatalf("second run = %+v", second.Setup)
	}

	sc.Manifest.Files["install.sh"] = fakeInstall + "\n# v2"
	if third := r.run(t); third.Setup == nil || third.Setup.Status != SetupStateRan || r.installs(t) != 2 {
		t.Fatalf("after editing a support file = %+v, installs %d", third.Setup, r.installs(t))
	}

	delete(sc.Manifest.Files, "tools/hello.sh")
	sc.Script = `test ! -e "$OCTOPUS_INIT_FILES/tools/hello.sh" && echo dropped`
	if fourth := r.run(t); fourth.State != InitStateReady || !strings.Contains(fourth.LogTail, "dropped") {
		t.Fatalf("a dropped support file lingered: %+v", fourth)
	}
}

func TestInitFilesArchive(t *testing.T) {
	files := map[string]string{"b.sh": "echo b", "a/seed.mjs": "console.log(1)"}
	data, err := initFilesArchive(files)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		if string(body) != files[hdr.Name] || hdr.Mode != 0o755 {
			t.Fatalf("%s: %q mode %o", hdr.Name, body, hdr.Mode)
		}
		names = append(names, hdr.Name)
	}
	if strings.Join(names, ",") != "a/seed.mjs,b.sh" {
		t.Fatalf("entries = %v, want sorted by path", names)
	}
}

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

// Shaped like api.github.com's real answer: pretty-printed, body last, an uploader id inside each
// asset.
const releaseFixture = `{
  "url": "https://api.github.com/repos/o/n/releases/1001",
  "assets_url": "https://api.github.com/repos/o/n/releases/1001/assets",
  "html_url": "https://github.com/o/n/releases/tag/v0.1.0",
  "id": 1001,
  "author": {
    "login": "someone",
    "id": 1
  },
  "node_id": "RE_kwDO",
  "tag_name": "v0.1.0",
  "target_commitish": "main",
  "name": "Thulu v0.1.0",
  "draft": false,
  "prerelease": false,
  "body": "changelog text that mentions \"tag_name\": \"v9.9.9\" and an asset called \"name\": \"octopus_9.9.9_linux_amd64\" on purpose"
}`

const assetFixture = `  {
    "url": "https://api.github.com/repos/o/n/releases/assets/%[1]d",
    "id": %[1]d,
    "node_id": "RA_kwDO",
    "name": "%[2]s",
    "label": null,
    "uploader": {
      "login": "someone",
      "id": 1
    },
    "content_type": "application/octet-stream",
    "size": %[3]d
  }`

type fakeRelease struct {
	assets map[string][]byte

	mu         sync.Mutex
	auths      []string
	downloaded []string
	hits       int
	apiHits    int
}

func (f *fakeRelease) served(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downloaded = append(f.downloaded, name)
}

func (f *fakeRelease) downloads() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.downloaded...)
}

func (f *fakeRelease) fetched(name string) bool {
	for _, n := range f.downloads() {
		if n == name {
			return true
		}
	}
	return false
}

func (f *fakeRelease) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits
}

func (f *fakeRelease) serve(t *testing.T) *httptest.Server {
	t.Helper()
	names := make([]string, 0, len(f.assets))
	for n := range f.assets {
		names = append(names, n)
	}
	sort.Strings(names)
	ids := map[int]string{}
	parts := make([]string, 0, len(names))
	for i, n := range names {
		id := 11 + i
		ids[id] = n
		parts = append(parts, fmt.Sprintf(assetFixture, id, n, len(f.assets[n])))
	}
	assetsJSON := "[\n" + strings.Join(parts, ",\n") + "\n]"

	mux := http.NewServeMux()
	release := func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, releaseFixture) }
	mux.HandleFunc("/repos/o/n/releases/latest", release)
	mux.HandleFunc("/repos/o/n/releases/tags/v0.1.0", release)
	mux.HandleFunc("/repos/o/n/releases/1001/assets", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, assetsJSON)
	})
	mux.HandleFunc("/repos/o/n/releases/assets/{id}", func(w http.ResponseWriter, r *http.Request) {
		var id int
		if _, err := fmt.Sscan(r.PathValue("id"), &id); err != nil || ids[id] == "" {
			http.NotFound(w, r)
			return
		}
		f.served(ids[id])
		w.Write(f.assets[ids[id]])
	})
	// github.com itself, for an install without a token.
	mux.HandleFunc("/o/n/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/o/n/releases/tag/v0.1.0", http.StatusFound)
	})
	mux.HandleFunc("/o/n/releases/tag/v0.1.0", func(w http.ResponseWriter, _ *http.Request) {})
	mux.HandleFunc("/o/n/releases/download/v0.1.0/{name}", func(w http.ResponseWriter, r *http.Request) {
		b, ok := f.assets[r.PathValue("name")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		f.served(r.PathValue("name"))
		w.Write(b)
	})
	mux.HandleFunc("/mirror/v0.1.0/{name}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		f.mu.Unlock()
		b, ok := f.assets[r.PathValue("name")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		f.served(r.PathValue("name"))
		w.Write(b)
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits++
		f.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			f.mu.Lock()
			f.apiHits++
			f.mu.Unlock()
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runInstaller(t *testing.T, srv *httptest.Server, dest string, extra ...string) (string, error) {
	t.Helper()
	return runScript(t, "install.sh", nil, srv, dest, extra...)
}

func runScript(t *testing.T, script string, args []string, srv *httptest.Server, dest string, extra ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(),
		"OCTOPUS_API="+srv.URL,
		"OCTOPUS_REPO=o/n",
		"OCTOPUS_INSTALL_DIR="+dest,
		// Without these the installer falls back to `gh auth token` and would read a real
		// credential.
		"GITHUB_TOKEN=fixture-token",
		"GH_TOKEN=fixture-token",
		"OCTOPUS_COSIGN_BIN="+filepath.Join(t.TempDir(), "no-cosign"),
		"OCTOPUS_INSECURE_SKIP_VERIFY=",
		"OCTOPUS_REQUIRE_SIGNATURE=",
		"OCTOPUS_RELEASE_BASE_URL=",
		"OCTOPUS_LOCALE=en",
	)
	cmd.Env = append(cmd.Env, extra...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func assetNameFor(version string) string {
	arch := runtime.GOARCH
	return fmt.Sprintf("octopus_%s_%s_%s", version, runtime.GOOS, arch)
}

func skipUnlessUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("install.sh only builds names for linux and darwin, not %s", runtime.GOOS)
	}
}

var testPayload = []byte("#!/bin/sh\necho 0.1.0\n")

func sumLine(payload []byte, name string) []byte {
	sum := sha256.Sum256(payload)
	return []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")
}

func tarGz(t *testing.T, members ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, m := range members {
		if err := tw.WriteHeader(&tar.Header{Name: m[0], Mode: 0o755, Size: int64(len(m[1])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(m[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func signedRelease(t *testing.T) *fakeRelease {
	t.Helper()
	name := assetNameFor("0.1.0")
	archive := tarGz(t, [2]string{"octopus", string(testPayload)}, [2]string{"LICENSE", "license text"})
	sums := sumLine([]byte("other"), "octopus_0.1.0_other_arch")
	sums = append(sums, sumLine(testPayload, name)...)
	sums = append(sums, sumLine(archive, name+".tar.gz")...)
	return &fakeRelease{assets: map[string][]byte{
		name:                          testPayload,
		name + ".sha256":              sumLine(testPayload, name),
		name + ".tar.gz":              archive,
		"octopus_0.1.0_checksums.txt": sums,
		"octopus_0.1.0_checksums.txt.sigstore.json": []byte(`{"fixture":"bundle"}`),
	}}
}

func fakeCosign(t *testing.T, exitCode int) (bin, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	bin = filepath.Join(dir, "cosign")
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" > %q\nexit %d\n", argsFile, exitCode)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsFile
}

func assertInstalled(t *testing.T, dest, out string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dest, "thulu"))
	if err != nil {
		t.Fatalf("nothing was installed: %v\n%s", err, out)
	}
	if string(got) != string(testPayload) {
		t.Errorf("installed the wrong bytes")
	}
	info, err := os.Stat(filepath.Join(dest, "thulu"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed binary is not executable (%v)", info.Mode())
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the install left more than the binary in %s: %v", dest, entries)
	}
}

func assertNotInstalled(t *testing.T, dest string) {
	t.Helper()
	if _, statErr := os.Stat(filepath.Join(dest, "thulu")); statErr == nil {
		t.Error("a binary was installed anyway")
	}
}

func TestInstallerDownloadsAndVerifies(t *testing.T) {
	skipUnlessUnix(t)
	dest := t.TempDir()
	out, err := runInstaller(t, signedRelease(t).serve(t), dest)
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Thulu v0.1.0") {
		t.Errorf("the release was not resolved — this is what a parser that cannot read "+
			"pretty-printed JSON looks like:\n%s", out)
	}
	if !strings.Contains(out, "checksum ok") {
		t.Errorf("the checksum was never verified:\n%s", out)
	}
	if !strings.Contains(out, "cosign not found") {
		t.Errorf("a skipped signature check must be said out loud:\n%s", out)
	}
	assertInstalled(t, dest, out)
}

func TestInstallerFallsBackToPerBinaryChecksum(t *testing.T) {
	skipUnlessUnix(t)
	name := assetNameFor("0.1.0")
	rel := &fakeRelease{assets: map[string][]byte{
		name:             testPayload,
		name + ".sha256": sumLine(testPayload, name),
	}}
	dest := t.TempDir()
	out, err := runInstaller(t, rel.serve(t), dest)
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "checksum ok") {
		t.Errorf("the checksum was never verified:\n%s", out)
	}
	assertInstalled(t, dest, out)
}

func TestInstallerRefusesABadChecksum(t *testing.T) {
	skipUnlessUnix(t)
	rel := signedRelease(t)
	name := assetNameFor("0.1.0") + ".tar.gz"
	rel.assets["octopus_0.1.0_checksums.txt"] = []byte(strings.Repeat("0", 64) + "  " + name + "\n")

	dest := t.TempDir()
	out, err := runInstaller(t, rel.serve(t), dest, "OCTOPUS_INSECURE_SKIP_VERIFY=1")
	if err == nil {
		t.Fatalf("install.sh accepted a binary whose checksum did not match:\n%s", out)
	}
	if !strings.Contains(out, "checksum mismatch") {
		t.Errorf("the refusal does not name the reason:\n%s", out)
	}
	assertNotInstalled(t, dest)
}

func TestInstallerRefusesWithoutChecksum(t *testing.T) {
	skipUnlessUnix(t)
	name := assetNameFor("0.1.0")
	rel := &fakeRelease{assets: map[string][]byte{name: testPayload}}
	srv := rel.serve(t)

	dest := t.TempDir()
	out, err := runInstaller(t, srv, dest)
	if err == nil {
		t.Fatalf("install.sh installed a binary it could not verify:\n%s", out)
	}
	if !strings.Contains(out, "publishes no checksum") {
		t.Errorf("the refusal does not name the reason:\n%s", out)
	}
	assertNotInstalled(t, dest)

	dest = t.TempDir()
	out, err = runInstaller(t, srv, dest, "OCTOPUS_INSECURE_SKIP_VERIFY=1")
	if err != nil {
		t.Fatalf("OCTOPUS_INSECURE_SKIP_VERIFY=1 should install anyway: %v\n%s", err, out)
	}
	if !strings.Contains(out, "installing anyway") {
		t.Errorf("an unverified install must be said out loud:\n%s", out)
	}
	assertInstalled(t, dest, out)
}

func TestInstallerRefusesWithoutHashTool(t *testing.T) {
	skipUnlessUnix(t)
	bin := t.TempDir()
	for _, tool := range []string{"curl", "grep", "sed", "tr", "head", "cut", "awk", "mktemp", "rm", "mkdir", "cp", "chmod", "mv", "uname", "cat", "tar", "gzip"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not found: %v", tool, err)
		}
		if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	dest := t.TempDir()
	out, err := runInstaller(t, signedRelease(t).serve(t), dest, "PATH="+bin)
	if err == nil {
		t.Fatalf("install.sh installed without being able to check the hash:\n%s", out)
	}
	if !strings.Contains(out, "no sha256sum or shasum") || strings.Contains(out, "checksum ok") {
		t.Errorf("wrong message for a missing hash tool:\n%s", out)
	}
	assertNotInstalled(t, dest)
}

func TestInstallerVerifiesSignatureWithCosign(t *testing.T) {
	skipUnlessUnix(t)
	cosign, argsFile := fakeCosign(t, 0)
	dest := t.TempDir()
	out, err := runInstaller(t, signedRelease(t).serve(t), dest, "OCTOPUS_COSIGN_BIN="+cosign)
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "signature ok") {
		t.Errorf("the signature was never checked:\n%s", out)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("cosign was not called: %v", err)
	}
	for _, want := range []string{
		"verify-blob",
		"--bundle",
		"octopus_0.1.0_checksums.txt.sigstore.json",
		`^https://github\.com/o/n/\.github/workflows/octopus-release\.yml@refs/tags/v`,
		"https://token.actions.githubusercontent.com",
	} {
		if !strings.Contains(string(args), want) {
			t.Errorf("cosign args lack %q: %s", want, args)
		}
	}
	assertInstalled(t, dest, out)
}

func TestInstallerRefusesABadSignature(t *testing.T) {
	skipUnlessUnix(t)
	cosign, _ := fakeCosign(t, 1)
	dest := t.TempDir()
	out, err := runInstaller(t, signedRelease(t).serve(t), dest,
		"OCTOPUS_COSIGN_BIN="+cosign, "OCTOPUS_INSECURE_SKIP_VERIFY=1")
	if err == nil {
		t.Fatalf("install.sh accepted checksums whose signature failed:\n%s", out)
	}
	if !strings.Contains(out, "signature check failed") {
		t.Errorf("the refusal does not name the reason:\n%s", out)
	}
	assertNotInstalled(t, dest)
}

func TestInstallerRefusesAnUnsignedReleaseWhenCosignIsPresent(t *testing.T) {
	skipUnlessUnix(t)
	cosign, _ := fakeCosign(t, 0)
	rel := signedRelease(t)
	delete(rel.assets, "octopus_0.1.0_checksums.txt.sigstore.json")
	dest := t.TempDir()
	out, err := runInstaller(t, rel.serve(t), dest, "OCTOPUS_COSIGN_BIN="+cosign)
	if err == nil {
		t.Fatalf("install.sh installed an unsigned release with cosign available:\n%s", out)
	}
	if !strings.Contains(out, "no signed checksums") {
		t.Errorf("the refusal does not name the reason:\n%s", out)
	}
	assertNotInstalled(t, dest)
}

func TestInstallerRequireSignatureWithoutCosign(t *testing.T) {
	skipUnlessUnix(t)
	dest := t.TempDir()
	out, err := runInstaller(t, signedRelease(t).serve(t), dest, "OCTOPUS_REQUIRE_SIGNATURE=1")
	if err == nil {
		t.Fatalf("OCTOPUS_REQUIRE_SIGNATURE=1 installed without cosign:\n%s", out)
	}
	assertNotInstalled(t, dest)
}

func TestInstallerFromMirror(t *testing.T) {
	skipUnlessUnix(t)
	rel := signedRelease(t)
	srv := rel.serve(t)
	dest := t.TempDir()
	out, err := runInstaller(t, srv, dest,
		"OCTOPUS_RELEASE_BASE_URL="+srv.URL+"/mirror/", "OCTOPUS_VERSION=0.1.0")
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "checksum ok") {
		t.Errorf("the checksum was never verified:\n%s", out)
	}
	assertInstalled(t, dest, out)
	rel.mu.Lock()
	defer rel.mu.Unlock()
	if len(rel.auths) == 0 {
		t.Fatal("nothing was fetched from the mirror")
	}
	for _, a := range rel.auths {
		if a != "" {
			t.Errorf("the GitHub token was sent to the mirror: %q", a)
		}
	}
}

func TestInstallerMirrorNeedsAVersion(t *testing.T) {
	skipUnlessUnix(t)
	srv := signedRelease(t).serve(t)
	dest := t.TempDir()
	out, err := runInstaller(t, srv, dest, "OCTOPUS_RELEASE_BASE_URL="+srv.URL+"/mirror")
	if err == nil || !strings.Contains(out, "needs OCTOPUS_VERSION") {
		t.Fatalf("a mirror without a version should fail by name: %v\n%s", err, out)
	}
}

func TestInstallerPrefersTheArchive(t *testing.T) {
	skipUnlessUnix(t)
	rel := signedRelease(t)
	dest := t.TempDir()
	out, err := runInstaller(t, rel.serve(t), dest)
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}
	assertInstalled(t, dest, out)
	name := assetNameFor("0.1.0")
	if !rel.fetched(name+".tar.gz") || rel.fetched(name) {
		t.Errorf("downloaded %v, want the archive and not the binary", rel.downloads())
	}
}

func TestInstallerFallsBackToTheBinary(t *testing.T) {
	skipUnlessUnix(t)
	rel := signedRelease(t)
	name := assetNameFor("0.1.0")
	delete(rel.assets, name+".tar.gz")
	rel.assets["octopus_0.1.0_checksums.txt"] = sumLine(testPayload, name)
	dest := t.TempDir()
	out, err := runInstaller(t, rel.serve(t), dest)
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "checksum ok") || !rel.fetched(name) {
		t.Errorf("the binary was not downloaded and checked:\n%s", out)
	}
	assertInstalled(t, dest, out)
}

func TestInstallerRefusesAnArchiveWithoutTheBinary(t *testing.T) {
	skipUnlessUnix(t)
	rel := signedRelease(t)
	name := assetNameFor("0.1.0")
	archive := tarGz(t, [2]string{"LICENSE", "license text"})
	rel.assets[name+".tar.gz"] = archive
	rel.assets["octopus_0.1.0_checksums.txt"] = sumLine(archive, name+".tar.gz")
	dest := t.TempDir()
	out, err := runInstaller(t, rel.serve(t), dest)
	if err == nil || !strings.Contains(out, "has no octopus binary inside") {
		t.Fatalf("an archive without the binary should fail by name: %v\n%s", err, out)
	}
	assertNotInstalled(t, dest)
}

func TestInstallerRerunIsANoop(t *testing.T) {
	skipUnlessUnix(t)
	rel := signedRelease(t)
	srv := rel.serve(t)
	dest := t.TempDir()
	if out, err := runInstaller(t, srv, dest); err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}
	before := len(rel.downloads())
	out, err := runInstaller(t, srv, dest)
	if err != nil {
		t.Fatalf("the second run failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "v0.1.0 is already installed at "+filepath.Join(dest, "thulu")) {
		t.Errorf("the second run does not say the release is already there:\n%s", out)
	}
	if got := rel.downloads(); len(got) != before {
		t.Errorf("the second run downloaded %v", got[before:])
	}
}

func TestInstallerTruncatedRunsNothing(t *testing.T) {
	skipUnlessUnix(t)
	src, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	last := strings.LastIndex(strings.TrimRight(string(src), "\n"), "\n") + 1
	if got := strings.TrimSpace(string(src[last:])); got != `main "$@"` {
		t.Fatalf("the last line of install.sh is %q, want main \"$@\"", got)
	}
	rel := signedRelease(t)
	srv := rel.serve(t)
	dir := t.TempDir()
	for i := 1; i <= 40; i++ {
		cut := last * i / 41
		script := filepath.Join(dir, fmt.Sprintf("install-%d.sh", cut))
		if err := os.WriteFile(script, src[:cut], 0o600); err != nil {
			t.Fatal(err)
		}
		dest := t.TempDir()
		out, _ := runScript(t, script, nil, srv, dest)
		if n := rel.requests(); n != 0 {
			t.Fatalf("cut at byte %d of %d, the script made %d requests:\n%s", cut, len(src), n, out)
		}
		assertNotInstalled(t, dest)
	}
}

func TestInstallerVersionFlag(t *testing.T) {
	skipUnlessUnix(t)
	srv := signedRelease(t).serve(t)
	dest := t.TempDir()
	out, err := runScript(t, "install.sh", []string{"--version", "0.1.0"}, srv, dest)
	if err != nil {
		t.Fatalf("--version 0.1.0 failed: %v\n%s", err, out)
	}
	assertInstalled(t, dest, out)

	dest = t.TempDir()
	out, err = runScript(t, "install.sh", []string{"--version=9.9.9"}, srv, dest)
	if err == nil || !strings.Contains(out, "cannot find a release") {
		t.Fatalf("a release that does not exist should fail by name: %v\n%s", err, out)
	}
	assertNotInstalled(t, dest)
}

func TestInstallerHelpAndBadArguments(t *testing.T) {
	skipUnlessUnix(t)
	rel := signedRelease(t)
	srv := rel.serve(t)
	out, err := runScript(t, "install.sh", []string{"--help"}, srv, t.TempDir())
	if err != nil || !strings.Contains(out, "https://github.com/o/n/releases/latest/download/install.sh") {
		t.Errorf("--help: %v\n%s", err, out)
	}
	for _, args := range [][]string{{"--bogus"}, {"--version"}} {
		out, err := runScript(t, "install.sh", args, srv, t.TempDir())
		if err == nil {
			t.Errorf("%v was accepted:\n%s", args, out)
		}
	}
	if n := rel.requests(); n != 0 {
		t.Errorf("help and argument errors made %d requests", n)
	}
}

func TestInstallerWithoutATerminalPrintsTheNextCommand(t *testing.T) {
	skipUnlessUnix(t)
	dest := t.TempDir()
	out, err := runInstaller(t, signedRelease(t).serve(t), dest, "PATH="+dest+":"+os.Getenv("PATH"))
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "[Y/n]") || !strings.Contains(out, "\n  thulu init") {
		t.Errorf("want the next command and no question:\n%s", out)
	}
}

func TestInstallerRefusesAnUnwritableDirectory(t *testing.T) {
	skipUnlessUnix(t)
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	dest := t.TempDir()
	if err := os.Chmod(dest, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dest, 0o755); err != nil {
			t.Error(err)
		}
	})
	rel := signedRelease(t)
	out, err := runInstaller(t, rel.serve(t), dest)
	if err == nil || !strings.Contains(out, "does not use sudo") {
		t.Fatalf("an unwritable directory should fail by name: %v\n%s", err, out)
	}
	if n := rel.requests(); n != 0 {
		t.Errorf("the script made %d requests before refusing", n)
	}
}

func TestInstallerWithoutATokenSkipsTheAPI(t *testing.T) {
	skipUnlessUnix(t)
	dest := t.TempDir()
	rel := signedRelease(t)
	srv := rel.serve(t)
	out, err := runInstaller(t, srv, dest, "GITHUB_TOKEN=", "GH_TOKEN=", "OCTOPUS_GITHUB_WEB="+srv.URL)
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Thulu v0.1.0") {
		t.Errorf("the latest release was not resolved from the redirect:\n%s", out)
	}
	rel.mu.Lock()
	api := rel.apiHits
	rel.mu.Unlock()
	if api != 0 {
		t.Errorf("an install without a token made %d API requests; the anonymous API limit is 60 an hour", api)
	}
	assertInstalled(t, dest, out)
}

func TestInstallerWithoutATokenAndNoRelease(t *testing.T) {
	skipUnlessUnix(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/o/n/releases/latest" {
			http.Redirect(w, r, "/o/n/releases", http.StatusFound)
		}
	}))
	t.Cleanup(srv.Close)
	out, err := runInstaller(t, srv, t.TempDir(), "GITHUB_TOKEN=", "GH_TOKEN=", "OCTOPUS_GITHUB_WEB="+srv.URL)
	if err == nil || !strings.Contains(out, "none is published yet") {
		t.Fatalf("no release should fail by saying so: %v\n%s", err, out)
	}
}

func TestInstallerWithoutATokenNamesAMissingVersion(t *testing.T) {
	skipUnlessUnix(t)
	srv := signedRelease(t).serve(t)
	out, err := runScript(t, "install.sh", []string{"--version", "9.9.9"}, srv, t.TempDir(),
		"GITHUB_TOKEN=", "GH_TOKEN=", "OCTOPUS_GITHUB_WEB="+srv.URL)
	if err == nil || !strings.Contains(out, "has no release v9.9.9") {
		t.Fatalf("a version that does not exist should fail by name: %v\n%s", err, out)
	}
}

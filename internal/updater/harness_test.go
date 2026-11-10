package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/registry"

	"github.com/MisterDonnuts/thulu/internal/deployfiles"
	"github.com/MisterDonnuts/thulu/internal/dockergc/dockergctest"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
	"github.com/MisterDonnuts/thulu/internal/release"
	"github.com/MisterDonnuts/thulu/internal/release/releasetest"
)

const composeV1 = "services:\n  octopus:\n    image: ${OCTOPUS_IMAGE}\n# 1.0.0\n"

type harness struct {
	t        *testing.T
	key      *releasetest.Key
	host     string
	srcDir   string
	root     string
	daemon   *fakeDaemon
	runner   *fakeRunner
	images   *fakeImages
	docker   *dockergctest.Docker
	out      bytes.Buffer
	releases map[string]*release.Manifest
	backups  int
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	reg := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(reg.Close)
	h := &harness{
		t:        t,
		key:      releasetest.NewKey(t),
		host:     strings.TrimPrefix(reg.URL, "http://"),
		srcDir:   t.TempDir(),
		root:     t.TempDir(),
		images:   &fakeImages{},
		docker:   &dockergctest.Docker{},
		releases: map[string]*release.Manifest{},
	}
	h.daemon = newFakeDaemon(t)
	h.runner = &fakeRunner{h: h}
	for _, k := range []string{"OCTOPUS_API_KEY", "OCTOPUS_IMAGE_REGISTRY", "OCTOPUS_CHART_REGISTRY", "OCTOPUS_UPDATE_SOURCE",
		"OCTOPUS_UPDATE_CHANNELS", "OCTOPUS_KEEP_VERSIONS", "OCTOPUS_BACKUP_KEEP", "COMPOSE_PROJECT_NAME",
		"OCTOPUS_VERSION", "OCTOPUS_DATABASE_URL", "OCTOPUS_KEPT_IMAGES", "OCTOPUS_ROLLBACK_IMAGE_DAYS"} {
		t.Setenv(k, "")
	}
	if err := os.MkdirAll(filepath.Join(h.root, "deploy"), 0o750); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) publish(version string, files map[string]string, edit func(*release.Manifest)) *release.Manifest {
	t := h.t
	t.Helper()
	m := &release.Manifest{
		Schema:      release.SchemaVersion,
		Version:     version,
		Channel:     release.ChannelStable,
		Commit:      strings.Repeat("a", 40),
		PublishedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Images:      map[string]string{},
		ThirdParty:  map[string]string{},
		UpgradeFrom: ">=1.0.0",
		Binaries:    map[string]release.Artifact{},
	}
	for _, n := range []string{"octopus", "octopus-agent", "octopus-agent-claude-code", "octopus-agent-codex", "octopus-agent-opencode",
		"octopus-runtime", "octopus-sandbox", "octopus-mcp-octopus-api"} {
		ref := releasetest.PushImage(t, h.host, "misterdonnuts/"+n, version)
		h.key.SignImage(t, ref)
		m.Images[n] = ref
	}
	m.ThirdParty["postgres"] = releasetest.PushImage(t, h.host, "library/postgres", "16-alpine")
	chart := releasetest.PushImage(t, h.host, "misterdonnuts/charts/octopus", version)
	h.key.SignImage(t, chart)
	_, digest, _ := strings.Cut(chart, "@")
	m.Chart = release.Chart{Ref: "oci://" + h.host + "/misterdonnuts/charts/octopus:" + version, Digest: digest}

	bundle := buildBundle(t, files)
	m.DeployBundle = release.Artifact{File: "octopus-deploy-" + version + ".tar.gz", SHA256: deployfiles.Hash(bundle)}
	h.writeSource(version, m.DeployBundle.File, bundle)
	bin := []byte("#!/bin/sh\necho octopus " + version + "\n")
	platform := runtime.GOOS + "_" + runtime.GOARCH
	m.Binaries[platform] = release.Artifact{File: "octopus-" + platform, SHA256: deployfiles.Hash(bin)}
	h.writeSource(version, "octopus-"+platform, bin)
	if edit != nil {
		edit(m)
	}
	h.sign(m)
	h.releases[version] = m
	return m
}

func (h *harness) sign(m *release.Manifest) {
	body, sig := h.key.SignManifest(h.t, m)
	h.writeSource(m.Version, release.ManifestFile, body)
	h.writeSource(m.Version, release.SignatureFile, sig)
	var idx struct {
		Releases []release.Listing `json:"releases"`
	}
	if b, err := os.ReadFile(filepath.Join(h.srcDir, release.IndexFile)); err == nil {
		if err := json.Unmarshal(b, &idx); err != nil {
			h.t.Fatal(err)
		}
	}
	idx.Releases = append(idx.Releases, release.Listing{Version: m.Version, Channel: m.Channel, PublishedAt: m.PublishedAt,
		Notes: "Notes for " + m.Version})
	b, _ := json.Marshal(idx)
	if err := os.WriteFile(filepath.Join(h.srcDir, release.IndexFile), b, 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) writeSource(version, file string, body []byte) {
	h.t.Helper()
	dir := filepath.Join(h.srcDir, version)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), body, 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) install(version string, files map[string]string, extraEnv string) {
	h.t.Helper()
	env := "OCTOPUS_VERSION=" + version + "\n" +
		"OCTOPUS_DATABASE_URL=postgres://octopus:pw@pg:5432/octopus?sslmode=disable\n" +
		"OCTOPUS_MCP_SERVERS=octopus-api\n" + extraEnv
	h.write("deploy/.env", env, 0o600)
	for p, body := range files {
		h.write(filepath.Join("deploy", p), body, 0o644)
	}
	h.daemon.set(version, true)
}

func (h *harness) write(rel, body string, mode os.FileMode) {
	h.t.Helper()
	p := filepath.Join(h.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) read(rel string) string {
	h.t.Helper()
	b, err := os.ReadFile(filepath.Join(h.root, rel))
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

func (h *harness) installStruct(mode initwizard.Mode) *Install {
	in := &Install{
		Root: h.root, Mode: mode, ComposeFile: "deploy/docker-compose.standalone.yml",
		Release: "octopus", Namespace: "octopus", DaemonURL: h.daemon.srv.URL,
	}
	in.RunningVersion = in.Running(context.Background())
	return in
}

func (h *harness) opts(self string) Options {
	return Options{
		Root:     h.root,
		Yes:      true,
		APIKey:   "test-key",
		Operator: "ops@host",
		Runner:   h.runner,
		Out:      &h.out,
		Docker:   h.docker,
		Deps: Deps{
			Keys:         h.key.Keys,
			Source:       &release.Dir{Root: h.srcDir},
			Images:       h.images,
			SelfVersion:  self,
			ReadyTimeout: 300 * time.Millisecond,
			FetchChart:   func(context.Context, string) ([]byte, error) { return []byte("chart"), nil },
			Backup: func(context.Context, *Install, io.Writer) (string, BackupMethod, error) {
				h.backups++
				name := fmt.Sprintf("octopus_2026100%d_120000.sql", h.backups)
				h.write(filepath.Join("backups", name), "-- dump\n", 0o600)
				return filepath.Join(h.root, "backups", name), BackupContainerDump, nil
			},
		},
	}
}

func (h *harness) update(opts Options, mode initwizard.Mode) error {
	return runUpdate(context.Background(), opts, h.installStruct(mode))
}

func (h *harness) apply(opts Options) error {
	return runApply(context.Background(), opts, h.installStruct(initwizard.ModeDocker))
}

func buildBundle(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		body := []byte(files[n])
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
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

type fakeDaemon struct {
	srv     *httptest.Server
	mu      sync.Mutex
	version string
	ready   bool
	events  []Event
	auth    []string
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	d := &fakeDaemon{}
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		switch r.URL.Path {
		case "/health/ready":
			if !d.ready {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		case "/info":
			if !d.ready {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"version": d.version})
		case "/updates/events":
			var ev Event
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&ev) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			d.events = append(d.events, ev)
			d.auth = append(d.auth, r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(d.srv.Close)
	return d
}

func (d *fakeDaemon) set(version string, ready bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.version, d.ready = version, ready
}

func (d *fakeDaemon) lastEvent(t *testing.T) Event {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.events) == 0 {
		t.Fatal("no event was posted")
	}
	return d.events[len(d.events)-1]
}

type fakeRunner struct {
	h        *harness
	cmds     []string
	broken   map[string]bool
	restored []string
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	cmd := name + " " + strings.Join(args, " ")
	r.cmds = append(r.cmds, cmd)
	switch {
	case name == "docker" && contains(args, "up"):
		env, err := initwizard.LoadEnvFile(filepath.Join(r.h.root, "deploy", ".env"))
		if err != nil {
			return err
		}
		v := env.Get("OCTOPUS_VERSION")
		r.h.daemon.set(v, !r.broken[v])
	case name == "docker" && contains(args, "stop"):
		r.h.daemon.set("", false)
	case name == "helm" && len(args) > 0 && args[0] == "upgrade":
		v := ""
		for i, a := range args {
			if a == "--set" && strings.HasPrefix(args[i+1], "image.tag=") {
				v = strings.TrimPrefix(args[i+1], "image.tag=")
			}
		}
		r.h.daemon.set(v, !r.broken[v])
	}
	return nil
}

func (r *fakeRunner) RunInput(ctx context.Context, stdin io.Reader, name string, args ...string) error {
	b, err := io.ReadAll(stdin)
	if err != nil {
		return err
	}
	r.restored = append(r.restored, string(b))
	return r.Run(ctx, name, args...)
}

func (r *fakeRunner) ran(sub string) bool {
	for _, c := range r.cmds {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

type fakeImages struct {
	pulled, tagged []string
}

func (f *fakeImages) Pull(_ context.Context, ref string) error {
	f.pulled = append(f.pulled, ref)
	return nil
}

func (f *fakeImages) Tag(_ context.Context, ref, tag string) error {
	f.tagged = append(f.tagged, ref+" "+tag)
	return nil
}

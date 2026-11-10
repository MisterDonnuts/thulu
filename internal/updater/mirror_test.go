package updater

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"

	"github.com/MisterDonnuts/thulu/internal/initwizard"
	"github.com/MisterDonnuts/thulu/internal/release"
)

func TestMirrorThenApplyFromTheMirror(t *testing.T) {
	h, m := twoReleases(t, nil)
	reg := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(reg.Close)
	mirrorHost := strings.TrimPrefix(reg.URL, "http://") + "/bank/octopus"
	dir := t.TempDir()

	for _, v := range []string{"1.0.0", "1.1.0"} {
		var out strings.Builder
		err := Mirror(context.Background(), MirrorOptions{
			Version: v, Registry: mirrorHost, Dir: dir, Out: &out,
			Getenv: func(string) string { return "" },
			Deps:   Deps{Keys: h.key.Keys, Source: &release.Dir{Root: h.srcDir}},
		})
		if err != nil {
			t.Fatalf("mirror %s: %v\n%s", v, err, out.String())
		}
	}

	for _, n := range m.ImageNames() {
		ref, err := MirrorRef(mirrorHost, m.Images[n])
		if err != nil {
			t.Fatal(err)
		}
		if err := h.key.Keys.VerifyImage(context.Background(), ref); err != nil {
			t.Errorf("%s at the mirror: %v", n, err)
		}
	}
	chart := mirrorChartRepo("", mirrorHost, m.Chart.Ref) + "@" + m.Chart.Digest
	if err := h.key.Keys.VerifyImage(context.Background(), chart); err != nil {
		t.Errorf("chart at the mirror: %v", err)
	}

	platform := runtime.GOOS + "_" + runtime.GOARCH
	for _, f := range []string{release.IndexFile, "1.1.0/" + release.ManifestFile, "1.1.0/" + release.SignatureFile,
		"1.1.0/" + m.DeployBundle.File, "1.1.0/" + m.Binaries[platform].File, "1.1.0/" + MirrorFile} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("mirror lacks %s", f)
		}
	}
	var rec MirrorRecord
	b, _ := os.ReadFile(filepath.Join(dir, "1.1.0", MirrorFile))
	if err := json.Unmarshal(b, &rec); err != nil || rec.Version != "1.1.0" || len(rec.Images) == 0 || rec.Chart == nil {
		t.Errorf("mirror record = %s (%v)", b, err)
	}
	listed, err := (&release.Dir{Root: dir}).List(context.Background())
	if err != nil || len(listed) != 2 || listed[0].Version != "1.1.0" {
		t.Errorf("releases.json = %+v, %v", listed, err)
	}

	opts := h.opts("1.1.0")
	opts.Version = "1.1.0"
	opts.Registry = mirrorHost
	opts.Deps.Source = &release.Dir{Root: dir}
	if err := h.update(opts, initwizard.ModeDocker); err != nil {
		t.Fatalf("apply from the mirror: %v\n%s", err, h.out.String())
	}
	for _, ref := range h.images.pulled {
		if !strings.HasPrefix(ref, mirrorHost+"/") {
			t.Errorf("pulled %s from outside the mirror", ref)
		}
	}
	want, _ := MirrorRef(mirrorHost, m.Images["octopus"])
	if !strings.Contains(h.read("deploy/.env"), "OCTOPUS_IMAGE="+want) {
		t.Errorf(".env does not name the mirrored daemon:\n%s", h.read("deploy/.env"))
	}
}

func TestMirrorRefusesAnUnverifiedRelease(t *testing.T) {
	h, _ := twoReleases(t, nil)
	h.writeSource("1.1.0", release.SignatureFile, h.key.SignBlob(t, []byte("something else")))
	dir := t.TempDir()
	err := Mirror(context.Background(), MirrorOptions{
		Version: "1.1.0", Dir: dir, Getenv: func(string) string { return "" },
		Deps: Deps{Keys: h.key.Keys, Source: &release.Dir{Root: h.srcDir}},
	})
	if err == nil {
		t.Fatal("mirrored a manifest whose signature does not verify")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("wrote %d entries before verifying", len(entries))
	}
}

func TestMirrorRefKeepsTheDigest(t *testing.T) {
	got, err := MirrorRef("registry.bank/octopus/", "ghcr.io/misterdonnuts/octopus-mcp-github@sha256:"+strings.Repeat("a", 64))
	if err != nil || got != "registry.bank/octopus/octopus-mcp-github@sha256:"+strings.Repeat("a", 64) {
		t.Errorf("got %q, %v", got, err)
	}
	if _, err := MirrorRef("r", "ghcr.io/x:1.0"); err == nil {
		t.Error("a tag-only reference was moved")
	}
	if got := mirrorChartRepo("oci://charts.bank/o/", "ignored", "ghcr.io/misterdonnuts/charts/octopus"); got != "charts.bank/o/octopus" {
		t.Errorf("chart repo = %q", got)
	}
}

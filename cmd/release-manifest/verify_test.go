package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/registry"

	"github.com/MisterDonnuts/thulu/internal/release"
	"github.com/MisterDonnuts/thulu/internal/release/releasetest"
)

func TestChartRef(t *testing.T) {
	got := chartRef(release.Chart{Ref: "oci://ghcr.io/misterdonnuts/charts/octopus:0.3.0", Digest: digest('a')})
	if got != "ghcr.io/misterdonnuts/charts/octopus@"+digest('a') {
		t.Errorf("chartRef = %s", got)
	}
	got = chartRef(release.Chart{Ref: "oci://localhost:5000/charts/octopus:0.3.0", Digest: digest('a')})
	if got != "localhost:5000/charts/octopus@"+digest('a') {
		t.Errorf("chartRef with a port = %s", got)
	}
}

func TestVerifyRelease(t *testing.T) {
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	ctx := context.Background()
	key := releasetest.NewKey(t)

	octopus := releasetest.PushImage(t, host, "octopus", "0.3.0")
	agent := releasetest.PushImage(t, host, "octopus-agent", "0.3.0")
	chart := releasetest.PushImage(t, host, "charts/octopus", "0.3.0")
	for _, ref := range []string{octopus, agent, chart} {
		key.SignImage(t, ref)
	}
	_, chartDigest, _ := strings.Cut(chart, "@")

	dir := t.TempDir()
	bundle := []byte("deploy tree")
	m := &release.Manifest{
		Schema: release.SchemaVersion, Version: "0.3.0", Channel: release.ChannelStable,
		Commit: strings.Repeat("c", 40), PublishedAt: time.Unix(1790000000, 0).UTC(),
		Images:       map[string]string{"octopus": octopus, "octopus-agent": agent},
		Chart:        release.Chart{Ref: "oci://" + host + "/charts/octopus:0.3.0", Digest: chartDigest},
		DeployBundle: release.Artifact{File: "octopus_0.3.0_deploy.tar.gz", SHA256: release.SHA256Hex(bundle)},
		Binaries:     map[string]release.Artifact{"linux_amd64": {File: "octopus_0.3.0_linux_amd64", SHA256: digest('b')[7:]}},
		UpgradeFrom:  ">=0.2.0", Requires: []string{}, ManualSteps: []string{}, Advisories: []release.Advisory{},
	}
	body, sig := key.SignManifest(t, m)
	path := filepath.Join(dir, release.ManifestFile)
	write(t, path, string(body))
	write(t, path+".sig", string(sig))
	write(t, filepath.Join(dir, m.DeployBundle.File), string(bundle))

	var out bytes.Buffer
	if err := verifyRelease(ctx, path, key.Keys, nil, &out); err != nil {
		t.Fatalf("signed release: %v\n%s", err, out.String())
	}
	if n := strings.Count(out.String(), "signature ok"); n != 4 {
		t.Errorf("%d signatures reported ok:\n%s", n, out.String())
	}

	if err := verifyRelease(ctx, path, releasetest.NewKey(t).Keys, nil, &out); err == nil {
		t.Error("verified with another key")
	}

	write(t, filepath.Join(dir, m.DeployBundle.File), "other tree")
	if err := verifyRelease(ctx, path, key.Keys, nil, &out); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Errorf("swapped bundle: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, m.DeployBundle.File)); err != nil {
		t.Fatal(err)
	}

	unsigned := releasetest.PushImage(t, host, "octopus-sandbox", "0.3.0")
	m.Images["octopus-sandbox"] = unsigned
	body, sig = key.SignManifest(t, m)
	write(t, path, string(body))
	write(t, path+".sig", string(sig))
	err := verifyRelease(ctx, path, key.Keys, nil, &out)
	if err == nil || !strings.Contains(err.Error(), "1 of 4") {
		t.Errorf("unsigned image: %v", err)
	}
}

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/deploy"
)

func TestBundleIsReproducibleAndPlain(t *testing.T) {
	var a, b bytes.Buffer
	if err := write(&a, deploy.Files); err != nil {
		t.Fatal(err)
	}
	if err := write(&b, deploy.Files); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("two bundles of the same tree differ")
	}

	gz, err := gzip.NewReader(&a)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag != tar.TypeReg || strings.HasPrefix(h.Name, "/") || strings.Contains(h.Name, "..") {
			t.Errorf("entry %q (type %c) is not a plain relative file", h.Name, h.Typeflag)
		}
		if !h.ModTime.Equal(epoch) {
			t.Errorf("%s carries a timestamp", h.Name)
		}
		seen[h.Name] = true
	}
	for _, want := range []string{"docker-compose.yml", "docker-compose.standalone.yml", "backup.sh",
		"helm/octopus/Chart.yaml", "helm/octopus/templates/_helpers.tpl"} {
		if !seen[want] {
			t.Errorf("bundle misses %s", want)
		}
	}
	for name := range seen {
		if name == ".env" || strings.HasPrefix(name, "values-") {
			t.Errorf("bundle carries a per-install file %s", name)
		}
	}
}

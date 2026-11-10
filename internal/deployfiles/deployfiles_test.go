package deployfiles

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	name     string
	body     string
	typeflag byte
	link     string
}

func bundle(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		tf := e.typeflag
		if tf == 0 {
			tf = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Typeflag: tf, Mode: 0o4777, Size: int64(len(e.body)), Linkname: e.link}
		if tf != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if tf == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
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

func TestReadBundle(t *testing.T) {
	tree, err := ReadBundle(bytes.NewReader(bundle(t,
		entry{name: "docker-compose.yml", body: "services: {}\n"},
		entry{name: "backup.sh", body: "#!/bin/sh\n"},
		entry{name: "helm/octopus/Chart.yaml", body: "name: octopus\n"},
	)))
	if err != nil {
		t.Fatal(err)
	}
	if len(tree) != 3 || tree["backup.sh"].Mode != 0o755 || tree["docker-compose.yml"].Mode != 0o644 {
		t.Errorf("tree = %v", tree)
	}
}

func TestReadBundleRefusesEscapes(t *testing.T) {
	for name, e := range map[string]entry{
		"parent":       {name: "../etc/passwd", body: "x"},
		"absolute":     {name: "/etc/passwd", body: "x"},
		"nested up":    {name: "helm/../../x", body: "x"},
		"symlink":      {name: "link", typeflag: tar.TypeSymlink, link: "/etc/passwd"},
		"hardlink":     {name: "hard", typeflag: tar.TypeLink, link: "../../etc/passwd"},
		"directory":    {name: "helm/", typeflag: tar.TypeDir},
		"device":       {name: "dev", typeflag: tar.TypeChar},
		"env":          {name: ".env", body: "OCTOPUS_JWT_SECRET=stolen"},
		"override":     {name: "docker-compose.override.yml", body: "x"},
		"record":       {name: ".octopus-files.json", body: "{}"},
		"values":       {name: "values-octopus.yaml", body: "x"},
		"backslash":    {name: `helm\..\x`, body: "x"},
		"dot segment":  {name: "./docker-compose.yml", body: "x"},
		"double slash": {name: "helm//x", body: "x"},
	} {
		if _, err := ReadBundle(bytes.NewReader(bundle(t, entry{name: "ok.yml", body: "x"}, e))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ReadBundle(bytes.NewReader(bundle(t,
		entry{name: "a.yml", body: "1"}, entry{name: "a.yml", body: "2"}))); err == nil {
		t.Error("duplicate accepted")
	}
	if _, err := ReadBundle(strings.NewReader("not gzip")); err == nil {
		t.Error("garbage accepted")
	}
}

func writeDeploy(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, "deploy", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPlanAndApply(t *testing.T) {
	root := t.TempDir()
	writeDeploy(t, root, map[string]string{
		"docker-compose.yml": "v1 compose\n",
		"Caddyfile":          "v1 caddy, edited by hand\n",
		"backup.sh":          "same\n",
		"old.yml":            "gone in v2\n",
		".env":               "OCTOPUS_JWT_SECRET=s\n",
	})
	rec := &Record{Version: "0.1.0", Files: map[string]string{
		"docker-compose.yml": Hash([]byte("v1 compose\n")),
		"Caddyfile":          Hash([]byte("v1 caddy\n")),
		"old.yml":            Hash([]byte("gone in v2\n")),
	}}
	target := Tree{
		"docker-compose.yml": {Body: []byte("v2 compose\n"), Mode: 0o644},
		"Caddyfile":          {Body: []byte("v2 caddy\n"), Mode: 0o644},
		"backup.sh":          {Body: []byte("same\n"), Mode: 0o755},
		"helm/new.yaml":      {Body: []byte("new\n"), Mode: 0o644},
	}
	plan, err := NewPlan(root, rec, nil, target, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Kind{}
	for _, c := range plan.Changes {
		got[c.Path] = c.Kind
	}
	want := map[string]Kind{
		"docker-compose.yml": Updated, "Caddyfile": Edited, "backup.sh": Unchanged,
		"helm/new.yaml": Added, "old.yml": Obsolete,
	}
	for p, k := range want {
		if got[p] != k {
			t.Errorf("%s = %s, want %s", p, got[p], k)
		}
	}
	conflicts := plan.Conflicts()
	if len(conflicts) != 1 || !strings.Contains(conflicts[0].Diff, "-v1 caddy, edited by hand") ||
		!strings.Contains(conflicts[0].Diff, "+v2 caddy") {
		t.Fatalf("conflicts = %+v", conflicts)
	}
	if _, _, err := Apply(root, plan, target); err == nil {
		t.Fatal("applied over a hand edit")
	}

	if err := os.Remove(filepath.Join(root, "deploy", "Caddyfile")); err != nil {
		t.Fatal(err)
	}
	plan, err = NewPlan(root, rec, nil, target, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	newRec, undo, err := Apply(root, plan, target)
	if err != nil {
		t.Fatal(err)
	}
	for p, f := range target {
		b, err := os.ReadFile(filepath.Join(root, "deploy", filepath.FromSlash(p)))
		if err != nil || string(b) != string(f.Body) {
			t.Errorf("%s = %q, %v", p, b, err)
		}
		if newRec.Files[p] != Hash(f.Body) {
			t.Errorf("record for %s", p)
		}
	}
	if _, ok := newRec.Files["old.yml"]; ok {
		t.Error("obsolete file still recorded")
	}
	info, _ := os.Stat(filepath.Join(root, "deploy", "docker-compose.yml"))
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", info.Mode())
	}
	if env, _ := os.ReadFile(filepath.Join(root, "deploy", ".env")); string(env) != "OCTOPUS_JWT_SECRET=s\n" {
		t.Error(".env touched")
	}

	if err := undo(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "deploy", "docker-compose.yml")); string(b) != "v1 compose\n" {
		t.Errorf("undo left %q", b)
	}
	if _, err := os.Stat(filepath.Join(root, "deploy", "helm", "new.yaml")); !os.IsNotExist(err) {
		t.Error("undo kept an added file")
	}
}

func TestPlanUsesTheBaselineWithoutARecord(t *testing.T) {
	root := t.TempDir()
	writeDeploy(t, root, map[string]string{"docker-compose.yml": "v1\n", "Caddyfile": "mine\n"})
	target := Tree{"docker-compose.yml": {Body: []byte("v2\n")}, "Caddyfile": {Body: []byte("v2\n")}}

	plan, err := NewPlan(root, nil, map[string]string{"docker-compose.yml": Hash([]byte("v1\n"))}, target, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Writes()) != 1 || len(plan.Conflicts()) != 1 || plan.Conflicts()[0].Path != "Caddyfile" {
		t.Errorf("plan = %+v", plan)
	}
}

func TestRecordRoundTrip(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o750); err != nil {
		t.Fatal(err)
	}
	if r, err := LoadRecord(root); r != nil || err != nil {
		t.Fatalf("missing = %v, %v", r, err)
	}
	if err := SaveRecord(root, &Record{Version: "0.2.0", Files: map[string]string{"a": "b"}}); err != nil {
		t.Fatal(err)
	}
	r, err := LoadRecord(root)
	if err != nil || r.Version != "0.2.0" || r.Files["a"] != "b" {
		t.Errorf("%+v, %v", r, err)
	}
}

func TestDiff(t *testing.T) {
	a := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14\n15\n"
	b := strings.Replace(strings.Replace(a, "2\n", "two\n", 1), "14\n", "", 1)
	d := Diff("x", []byte(a), []byte(b))
	for _, want := range []string{"--- x (on disk)", "+++ x (release)", "-2", "+two", "-14", "@@ -1,5 +1,5 @@"} {
		if !strings.Contains(d, want) {
			t.Errorf("diff lacks %q:\n%s", want, d)
		}
	}
	if strings.Contains(d, " 8\n") {
		t.Errorf("unrelated lines in the diff:\n%s", d)
	}
	if Diff("x", []byte(a), []byte(a)) != "" {
		t.Error("diff of equal inputs")
	}
}

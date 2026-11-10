package types

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/docker/api/types/mount"
)

func TestIsSocketPassthrough(t *testing.T) {
	cases := map[string]bool{
		"/var/run/docker.sock": true,
		"/setup.sh":            false,
		"/workspace/runs":      false,
		"":                     false,
	}
	for target, want := range cases {
		if got := isSocketPassthrough(target); got != want {
			t.Errorf("isSocketPassthrough(%q) = %v, want %v", target, got, want)
		}
	}
}

func TestBuildSocketMounts(t *testing.T) {
	spec := &ComposeServiceSpec{Volumes: []string{
		"./setup.sh:/setup.sh:ro",
		"/var/run/docker.sock:/var/run/docker.sock",
		"./engines:/engines:ro",
	}}
	mounts := buildSocketMounts(spec)
	if len(mounts) != 1 {
		t.Fatalf("want 1 socket mount, got %d: %+v", len(mounts), mounts)
	}
	if mounts[0].Source != "/var/run/docker.sock" || mounts[0].Target != "/var/run/docker.sock" {
		t.Errorf("unexpected socket mount: %+v", mounts[0])
	}

	if got := buildSocketMounts(&ComposeServiceSpec{Volumes: []string{"./setup.sh:/setup.sh:ro"}}); len(got) != 0 {
		t.Errorf("want no mounts without socket, got %+v", got)
	}
}

func TestBuildCopyIns(t *testing.T) {
	at := AgentType{Dir: "/agents/probe"}
	spec := &ComposeServiceSpec{Volumes: []string{
		"./setup.sh:/setup.sh:ro",
		"./engines:/engines:ro",
		"/var/run/docker.sock:/var/run/docker.sock",
		"./runs:/workspace/runs",
	}}
	ins := buildCopyIns(spec, at)
	if len(ins) != 2 {
		t.Fatalf("want 2 copy-ins (socket + runs skipped), got %d: %+v", len(ins), ins)
	}
	want := map[string]string{
		"/setup.sh": "/agents/probe/setup.sh",
		"/engines":  "/agents/probe/engines",
	}
	for _, ci := range ins {
		src, ok := want[ci.target]
		if !ok {
			t.Errorf("unexpected copy-in target %q", ci.target)
			continue
		}
		if ci.src != src {
			t.Errorf("target %q: src = %q, want %q", ci.target, ci.src, src)
		}
	}
}

func TestBuildVolumeMounts(t *testing.T) {
	if got := buildVolumeMounts(LaunchParams{WorkspacePaths: map[string]string{"webshop": "x"}}); got != nil {
		t.Errorf("want nil without DataVolume, got %+v", got)
	}

	params := LaunchParams{
		DataVolume:     "octopus-data",
		WorkspacePaths: map[string]string{"webshop": "projects/.worktrees/webshop--task-1"},
		StagePaths:     map[string]string{"webshop": ".project-stage/webshop"},
	}
	mounts := buildVolumeMounts(params)
	if len(mounts) != 2 {
		t.Fatalf("want 2 mounts (worktree + sidecar), got %d: %+v", len(mounts), mounts)
	}
	var wt, side *mount.Mount
	for i := range mounts {
		switch mounts[i].Target {
		case "/workspace/projects/webshop":
			wt = &mounts[i]
		case "/workspace/projects/webshop/.thulu":
			side = &mounts[i]
		}
	}
	if wt == nil || side == nil {
		t.Fatalf("missing worktree or sidecar mount: %+v", mounts)
	}
	if wt.Type != mount.TypeVolume || wt.Source != "octopus-data" || wt.ReadOnly {
		t.Errorf("worktree mount wrong: %+v", wt)
	}
	if wt.VolumeOptions == nil || wt.VolumeOptions.Subpath != "projects/.worktrees/webshop--task-1" {
		t.Errorf("worktree subpath wrong: %+v", wt.VolumeOptions)
	}
	if !side.ReadOnly || side.VolumeOptions == nil || side.VolumeOptions.Subpath != ".project-stage/webshop" {
		t.Errorf("sidecar mount wrong: %+v / %+v", side, side.VolumeOptions)
	}
}

func TestTarPathFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "setup.sh")
	content := []byte("#!/bin/sh\necho hi\n")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}

	buf, err := tarPath(src, info, "/setup.sh")
	if err != nil {
		t.Fatal(err)
	}
	entries := readTar(t, buf)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d: %v", len(entries), keys(entries))
	}
	if got, ok := entries["setup.sh"]; !ok || !bytes.Equal(got, content) {
		t.Errorf("entry setup.sh mismatch: ok=%v content=%q", ok, got)
	}
}

func TestTarPathDir(t *testing.T) {
	dir := t.TempDir()
	engines := filepath.Join(dir, "engines")
	if err := os.MkdirAll(engines, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(engines, "claude-code.sh"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(engines)
	if err != nil {
		t.Fatal(err)
	}

	buf, err := tarPath(engines, info, "/engines")
	if err != nil {
		t.Fatal(err)
	}
	entries := readTar(t, buf)
	if got, ok := entries["engines/claude-code.sh"]; !ok || !bytes.Equal(got, []byte("ok")) {
		t.Errorf("entry engines/claude-code.sh mismatch: ok=%v content=%q", ok, got)
	}
}

func TestUntarStripRoot(t *testing.T) {
	dst := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	writeFileEntry(t, tw, "runs/_index.md", "report")
	writeFileEntry(t, tw, "runs/sub/comment.md", "hi")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := untarStripRoot(&buf, dst, "runs"); err != nil {
		t.Fatalf("untarStripRoot: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "_index.md")); err != nil || string(b) != "report" {
		t.Errorf("_index.md: err=%v content=%q", err, b)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "sub", "comment.md")); err != nil || string(b) != "hi" {
		t.Errorf("sub/comment.md: err=%v content=%q", err, b)
	}
}

func TestUntarStripRootRejectsEscape(t *testing.T) {
	dst := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	writeFileEntry(t, tw, "runs/../escape.txt", "pwned")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := untarStripRoot(&buf, dst, "runs"); err == nil {
		t.Fatal("expected error on path escape, got nil")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dst), "escape.txt")); err == nil {
		t.Error("escape file was written outside dst")
	}
}

func readTar(t *testing.T, r io.Reader) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			b, _ := io.ReadAll(tr)
			out[hdr.Name] = b
		}
	}
	return out
}

func writeFileEntry(t *testing.T, tw *tar.Writer, name, content string) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tw, content); err != nil {
		t.Fatal(err)
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestDirTarBelongsToTheAgent(t *testing.T) {
	buf, err := dirTar("workspace/runs")
	if err != nil {
		t.Fatalf("dirTar: %v", err)
	}
	h, err := tar.NewReader(buf).Next()
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	if h.Name != "workspace/runs/" || h.Typeflag != tar.TypeDir {
		t.Errorf("entry = %q (type %q), want a workspace/runs/ dir", h.Name, string(h.Typeflag))
	}
	if h.Uid != agentUID || h.Gid != agentUID {
		t.Errorf("owner = %d:%d, want %d:%d", h.Uid, h.Gid, agentUID, agentUID)
	}
}

package exportbundle_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/exportbundle"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func TestRoundTripUploadProjectCode(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	src := buildStores(t, testpg.New(t), data)

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := "# hosted\n"
	must(t, tw.WriteHeader(&tar.Header{Name: "README.md", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}), "tar header")
	_, err := tw.Write([]byte(body))
	must(t, err, "tar body")
	must(t, tw.Close(), "tar close")
	must(t, gz.Close(), "gzip close")
	archive := filepath.Join(t.TempDir(), "up.tar.gz")
	must(t, os.WriteFile(archive, buf.Bytes(), 0o600), "write archive")
	proj, _, err := src.s.Projects.CreateFromUpload(ctx, "Hosted", archive, project.CreateOpts{WorkspaceID: ws}, project.DefaultUploadLimits())
	must(t, err, "create upload project")

	bundle, files, err := exportbundle.Export(ctx, src.s, ws)
	must(t, err, "export")
	if len(bundle.Projects) != 1 || bundle.Projects[0].CodeTarPath != "code/"+proj.ID+".bundle" {
		t.Fatalf("project sections = %+v", bundle.Projects)
	}
	var out bytes.Buffer
	must(t, exportbundle.Write(&out, bundle, files), "write")
	got, gotFiles, err := exportbundle.Read(&out)
	must(t, err, "read")

	must(t, os.RemoveAll(filepath.Join(data, "projects")), "drop the old disk")
	dst := buildStores(t, testpg.New(t), data)
	_, err = exportbundle.Import(ctx, dst.s, got, gotFiles, dst.attachmentsDir)
	must(t, err, "import")
	restored, err := dst.s.Projects.Get(ctx, proj.ID)
	must(t, err, "get restored")
	if restored.Source != project.SourceUpload {
		t.Errorf("source = %q", restored.Source)
	}
	readme, err := os.ReadFile(filepath.Join(restored.Path, "README.md"))
	if err != nil || string(readme) != body {
		t.Errorf("README.md = %q, %v", readme, err)
	}
	cmd := exec.CommandContext(ctx, "git", "branch", "--list")
	cmd.Dir = filepath.Join(data, "projects", ".hosted", proj.ID+".git")
	branches, err := cmd.Output()
	if err != nil || !strings.Contains(string(branches), "main") {
		t.Errorf("hosted repository branches = %q, %v", branches, err)
	}

	got.Projects[0].CodeTarPath = "code/missing.bundle"
	if _, err := exportbundle.Import(ctx, dst.s, got, map[string][]byte{}, dst.attachmentsDir); err == nil {
		t.Error("want a refusal for missing code")
	}
}

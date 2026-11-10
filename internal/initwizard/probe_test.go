package initwizard

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestKubeContextIgnoresAPluginComplainingOnStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake kubectl is a shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\necho 'F1103 cred.go:145] print credential failed: gcloud reauthentication failed' >&2\necho gke_demo_cluster\n"
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	got, err := KubeContext(context.Background())
	if err != nil || got != "gke_demo_cluster" {
		t.Fatalf("KubeContext = %q, %v; want the context alone", got, err)
	}
}

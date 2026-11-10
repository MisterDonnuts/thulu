package dockerx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadInstallIDIsStable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	first, err := LoadInstallID(dir)
	if err != nil || first == "" {
		t.Fatalf("first load = %q, %v", first, err)
	}
	again, err := LoadInstallID(dir)
	if err != nil || again != first {
		t.Fatalf("second load = %q, %v; want %q", again, err, first)
	}
	other, err := LoadInstallID(t.TempDir())
	if err != nil || other == first {
		t.Fatalf("another data dir got %q (%v), want a different id", other, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, installIDFile)); err != nil || len(b) == 0 {
		t.Fatalf("id file = %q, %v", b, err)
	}
}

func TestForeignInstall(t *testing.T) {
	t.Cleanup(func() { SetInstallID("") })

	SetInstallID("")
	if ForeignInstall(map[string]string{InstallLabel: "x"}) {
		t.Error("a process with no id of its own called a container foreign")
	}
	labels := map[string]string{}
	StampInstall(labels)
	if _, ok := labels[InstallLabel]; ok {
		t.Error("a process with no id stamped a label")
	}

	SetInstallID("mine")
	StampInstall(labels)
	if labels[InstallLabel] != "mine" {
		t.Errorf("stamped labels = %v", labels)
	}
	for _, c := range []struct {
		labels map[string]string
		want   bool
	}{
		{map[string]string{InstallLabel: "mine"}, false},
		{map[string]string{InstallLabel: "theirs"}, true},
		{map[string]string{}, false},
		{nil, false},
	} {
		if got := ForeignInstall(c.labels); got != c.want {
			t.Errorf("ForeignInstall(%v) = %v, want %v", c.labels, got, c.want)
		}
	}
}

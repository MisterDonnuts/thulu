package enginecache

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/errdefs"

	"github.com/MisterDonnuts/thulu/internal/dockerx"
)

type fakeVolumes struct {
	vols    map[string]volume.Volume
	created []volume.CreateOptions
	fail    error
}

func (f *fakeVolumes) VolumeInspect(_ context.Context, name string) (volume.Volume, error) {
	if f.fail != nil {
		return volume.Volume{}, f.fail
	}
	if v, ok := f.vols[name]; ok {
		return v, nil
	}
	return volume.Volume{}, errdefs.NotFound(errors.New("no such volume"))
}

func (f *fakeVolumes) VolumeCreate(_ context.Context, o volume.CreateOptions) (volume.Volume, error) {
	f.created = append(f.created, o)
	v := volume.Volume{Name: o.Name, Labels: o.Labels}
	f.vols[o.Name] = v
	return v, nil
}

func withInstall(t *testing.T, id string) {
	t.Helper()
	dockerx.SetInstallID(id)
	t.Cleanup(func() { dockerx.SetInstallID("") })
}

func TestEnsureCreatesOneLabelledVolumePerScope(t *testing.T) {
	withInstall(t, "abcdef123456")
	f := &fakeVolumes{vols: map[string]volume.Volume{}}
	ctx := context.Background()

	a, err := Ensure(ctx, f, WorkspaceScope("w1"))
	if err != nil {
		t.Fatal(err)
	}
	if a != "octopus-engines-abcdef12-ws-w1" {
		t.Errorf("name = %q", a)
	}
	again, err := Ensure(ctx, f, WorkspaceScope("w1"))
	if err != nil || again != a || len(f.created) != 1 {
		t.Errorf("second Ensure = %q, %v after %d creates; want the same volume, created once", again, err, len(f.created))
	}
	if got := f.created[0].Labels; got[VolumeLabel] != "ws-w1" || got[dockerx.InstallLabel] != "abcdef123456" {
		t.Errorf("labels = %v", got)
	}
	b, err := Ensure(ctx, f, UserScope("u1"))
	if err != nil || b == a {
		t.Errorf("a person's volume = %q, %v; want its own, apart from the workspace's %q", b, err, a)
	}
}

func TestEnsureRefusesAVolumeItDidNotLabel(t *testing.T) {
	withInstall(t, "abcdef123456")
	ctx := context.Background()
	for name, v := range map[string]volume.Volume{
		"unlabelled":      {},
		"other scope":     {Labels: map[string]string{VolumeLabel: "ws-w2", dockerx.InstallLabel: "abcdef123456"}},
		"another install": {Labels: map[string]string{VolumeLabel: "ws-w1", dockerx.InstallLabel: "zzz"}},
	} {
		f := &fakeVolumes{vols: map[string]volume.Volume{VolumeName("ws-w1"): v}}
		if _, err := Ensure(ctx, f, WorkspaceScope("w1")); err == nil {
			t.Errorf("%s: Ensure adopted a volume this install did not label for the scope", name)
		}
	}
}

func TestEnsureNeedsAScopeAndReportsDockerErrors(t *testing.T) {
	ctx := context.Background()
	for _, scope := range []string{"", WorkspaceScope(""), UserScope("")} {
		if _, err := Ensure(ctx, &fakeVolumes{vols: map[string]volume.Volume{}}, scope); err == nil {
			t.Errorf("Ensure(%q) = nil error, want one: every scope shares the binary it installs", scope)
		}
	}
	f := &fakeVolumes{vols: map[string]volume.Volume{}, fail: errors.New("daemon down")}
	if _, err := Ensure(ctx, f, WorkspaceScope("w1")); err == nil || len(f.created) != 0 {
		t.Errorf("Ensure with a failing inspect = %v after %d creates", err, len(f.created))
	}
}

func TestVolumeNameKeepsUnsafeScopesApart(t *testing.T) {
	withInstall(t, "")
	a, b := VolumeName("ws-a/b"), VolumeName("ws-a:b")
	if a == b || strings.ContainsAny(a, "/:") {
		t.Errorf("VolumeName = %q and %q; want distinct Docker-safe names", a, b)
	}
}

package dockergc_test

import (
	"context"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/volume"

	"github.com/MisterDonnuts/thulu/internal/dockergc"
	"github.com/MisterDonnuts/thulu/internal/dockergc/dockergctest"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/personallogin"
)

func TestPersonalLoginVolumesGoOnlyWithUninstall(t *testing.T) {
	dockerx.SetInstallID("this-install")
	t.Cleanup(func() { dockerx.SetInstallID("") })
	loginVol := func(name, install string) *volume.Volume {
		return &volume.Volume{Name: name, Labels: map[string]string{
			personallogin.VolumeLabel: "claude-code", personallogin.UserLabel: "u1", dockerx.InstallLabel: install,
		}}
	}
	d := &dockergctest.Docker{Finished: map[string]string{}}
	d.Volumes = []*volume.Volume{
		loginVol("octopus-login-claude-code-u1", "this-install"),
		loginVol("octopus-login-claude-code-u2", "other-install"),
	}
	volumes := func(opts dockergc.Options) string {
		t.Helper()
		plan, err := dockergc.Scan(context.Background(), d, opts)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, v := range plan.Volumes {
			names = append(names, v.Name)
		}
		return strings.Join(names, ",")
	}

	opts := dockergc.Options{Now: now}
	if got := volumes(opts); got != "" {
		t.Errorf("janitor plans %q, want no volume", got)
	}
	opts.Uninstall = true
	if got := volumes(opts); got != "octopus-login-claude-code-u1" {
		t.Errorf("uninstall plans %q, want only this install's login", got)
	}
}

package dockergc_test

import (
	"context"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/volume"

	"github.com/MisterDonnuts/thulu/internal/dockergc"
	"github.com/MisterDonnuts/thulu/internal/dockergc/dockergctest"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/enginecache"
)

func TestEngineCacheVolumesGoOnlyWithUninstall(t *testing.T) {
	dockerx.SetInstallID("this-install")
	t.Cleanup(func() { dockerx.SetInstallID("") })
	engineVol := func(name, install string) *volume.Volume {
		return &volume.Volume{Name: name, Labels: map[string]string{
			enginecache.VolumeLabel: "ws-w1", dockerx.InstallLabel: install,
		}}
	}
	d := &dockergctest.Docker{Finished: map[string]string{}}
	d.Volumes = []*volume.Volume{
		engineVol("octopus-engines-this-ins-ws-w1", "this-install"),
		engineVol("octopus-engines-other-in-ws-w1", "other-install"),
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
		t.Errorf("janitor plans %q, want no volume: removing it only costs the next run a download", got)
	}
	opts.Uninstall = true
	if got := volumes(opts); got != "octopus-engines-this-ins-ws-w1" {
		t.Errorf("uninstall plans %q, want only this install's engine volume", got)
	}
}

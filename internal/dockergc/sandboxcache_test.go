package dockergc_test

import (
	"context"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/volume"

	"github.com/MisterDonnuts/thulu/internal/dockergc"
	"github.com/MisterDonnuts/thulu/internal/dockergc/dockergctest"
	"github.com/MisterDonnuts/thulu/internal/sandbox/sandboxid"
)

func TestSandboxCacheVolumesGoOnlyWithUninstall(t *testing.T) {
	cacheVol := func(name, ws string) *volume.Volume {
		return &volume.Volume{Name: name, Labels: map[string]string{sandboxid.CacheVolumeLabel: ws}}
	}
	d := &dockergctest.Docker{Finished: map[string]string{}}
	d.Volumes = []*volume.Volume{
		cacheVol("octopus-cache-ws1", "ws1"),
		cacheVol("octopus-cache-ws1-snapshots", "ws1"),
		{Name: "someone-elses"},
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
	if got := volumes(opts); got != "octopus-cache-ws1,octopus-cache-ws1-snapshots" {
		t.Errorf("uninstall plans %q, want both cache volumes and nothing else", got)
	}
}

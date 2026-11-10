package daemon

import (
	"context"
	"errors"
	"os"
	"testing"

	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
)

type fakeInspector struct {
	info dtypes.ContainerJSON
	err  error
}

func (f fakeInspector) ContainerInspect(context.Context, string) (dtypes.ContainerJSON, error) {
	return f.info, f.err
}

func TestDataVolumeOf(t *testing.T) {
	self := func(mounts ...dtypes.MountPoint) fakeInspector {
		return fakeInspector{info: dtypes.ContainerJSON{
			Config: &container.Config{Hostname: "abc123"},
			Mounts: mounts,
		}}
	}
	cases := []struct {
		name        string
		docker      fakeInspector
		wantVolume  string
		wantMounted bool
	}{
		{
			name: "a compose project prefixes the volume",
			docker: self(dtypes.MountPoint{
				Type: mount.TypeVolume, Name: "thulu_octopus-data", Destination: "/data",
			}),
			wantVolume: "thulu_octopus-data", wantMounted: true,
		},
		{
			name: "a bind mount has no volume to hand over",
			docker: self(dtypes.MountPoint{
				Type: mount.TypeBind, Source: "/srv/thulu", Destination: "/data",
			}),
			wantMounted: true,
		},
		{
			name: "another mount does not count",
			docker: self(dtypes.MountPoint{
				Type: mount.TypeVolume, Name: "other", Destination: "/cache",
			}),
		},
		{name: "a daemon on the host finds no container", docker: fakeInspector{err: errors.New("no such container")}},
		{
			name: "a container that merely shares the name is not this daemon",
			docker: fakeInspector{info: dtypes.ContainerJSON{
				Config: &container.Config{Hostname: "someone-else"},
				Mounts: []dtypes.MountPoint{{Type: mount.TypeVolume, Name: "theirs", Destination: "/data"}},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, mounted := dataVolumeOf(context.Background(), tc.docker, "abc123", "/data")
			if got != tc.wantVolume || mounted != tc.wantMounted {
				t.Fatalf("got %q/%v, want %q/%v", got, mounted, tc.wantVolume, tc.wantMounted)
			}
		})
	}
}

func TestAdoptDataVolumeKeepsAnExplicitValue(t *testing.T) {
	t.Setenv(dataVolumeEnv, "chosen")
	adoptDataVolume(context.Background(), fakeInspector{err: errors.New("must not be asked")}, "/data")
	if got := getenvForTest(dataVolumeEnv); got != "chosen" {
		t.Fatalf("env = %q", got)
	}
}

func getenvForTest(k string) string { return os.Getenv(k) }

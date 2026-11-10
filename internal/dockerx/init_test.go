package dockerx

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
)

type fakeImages struct {
	entrypoint []string
	noConfig   bool
	err        error
	inspected  int
}

func (f *fakeImages) ImageInspectWithRaw(context.Context, string) (types.ImageInspect, []byte, error) {
	f.inspected++
	if f.err != nil {
		return types.ImageInspect{}, nil, f.err
	}
	if f.noConfig {
		return types.ImageInspect{}, nil, nil
	}
	return types.ImageInspect{Config: &container.Config{Entrypoint: f.entrypoint}}, nil, nil
}

func TestStartsWithInit(t *testing.T) {
	cases := []struct {
		entrypoint []string
		want       bool
	}{
		{[]string{"/usr/bin/tini", "-g", "--", "/usr/local/bin/sandbox-execagent"}, true},
		{[]string{"/sbin/tini", "--", "./octopus", "serve"}, true},
		{[]string{"tini"}, true},
		{[]string{"/usr/bin/dumb-init", "--"}, true},
		{[]string{"/usr/local/bin/sandbox-execagent"}, false},
		{[]string{"/bin/bash", "/workspace/kube-entrypoint.sh"}, false},
		{[]string{"/usr/bin/tinyproxy"}, false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := StartsWithInit(tc.entrypoint); got != tc.want {
			t.Errorf("StartsWithInit(%q) = %v, want %v", tc.entrypoint, got, tc.want)
		}
	}
}

func TestNeedsInit(t *testing.T) {
	ctx := context.Background()

	tini := &fakeImages{entrypoint: []string{"/usr/bin/tini", "-g", "--", "/bin/bash", "/workspace/kube-entrypoint.sh"}}
	if NeedsInit(ctx, tini, "octopus-agent:1", nil) {
		t.Error("an image whose entrypoint is tini must not get Docker's init")
	}

	if !NeedsInit(ctx, &fakeImages{}, "octopus-runtime:latest", nil) {
		t.Error("an image without an entrypoint runs its command as PID 1 and needs Docker's init")
	}

	override := &fakeImages{entrypoint: []string{"/usr/bin/tini", "--"}}
	if !NeedsInit(ctx, override, "octopus-agent:1", []string{"/workspace/entrypoint.sh"}) {
		t.Error("an entrypoint override without an init needs Docker's init")
	}
	if NeedsInit(ctx, override, "octopus-agent:1", []string{"/usr/bin/tini", "--", "/workspace/entrypoint.sh"}) {
		t.Error("an entrypoint override that starts with tini needs nothing")
	}
	if override.inspected != 0 {
		t.Errorf("inspected the image %d times; the override alone decides", override.inspected)
	}

	if !NeedsInit(ctx, &fakeImages{err: errors.New("no such image")}, "gone:1", nil) {
		t.Error("an image that cannot be read must get Docker's init")
	}
	if !NeedsInit(ctx, &fakeImages{noConfig: true}, "odd:1", nil) {
		t.Error("an image without a config must get Docker's init")
	}
}

func TestApplyInit(t *testing.T) {
	ctx := context.Background()

	hc := &container.HostConfig{}
	ApplyInit(ctx, &fakeImages{entrypoint: []string{"/usr/bin/tini", "--"}}, "img", nil, hc)
	if hc.Init != nil {
		t.Errorf("Init = %v, want unset for an image that brings tini", *hc.Init)
	}

	hc = &container.HostConfig{}
	ApplyInit(ctx, &fakeImages{}, "img", nil, hc)
	if hc.Init == nil || !*hc.Init {
		t.Error("Init must be true for an image without an init")
	}
}

package imageref

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/errdefs"
)

type fakeImages struct {
	present map[string]bool
	pulled  []string
	stream  string
}

func (f *fakeImages) ImageInspectWithRaw(_ context.Context, ref string) (types.ImageInspect, []byte, error) {
	if f.present[ref] {
		return types.ImageInspect{}, nil, nil
	}
	return types.ImageInspect{}, nil, errdefs.NotFound(errors.New("no such image"))
}

func (f *fakeImages) ImagePull(_ context.Context, ref string, _ image.PullOptions) (io.ReadCloser, error) {
	f.pulled = append(f.pulled, ref)
	return io.NopCloser(strings.NewReader(f.stream)), nil
}

func TestEnsureLocalNeverPullsBareNames(t *testing.T) {
	f := &fakeImages{present: map[string]bool{}}
	err := EnsureLocal(context.Background(), f, "octopus-docker-agent:latest")
	var missing *MissingError
	if !errors.As(err, &missing) || missing.Ref != "octopus-docker-agent:latest" {
		t.Fatalf("bare missing name must be a MissingError, got %v", err)
	}
	if len(f.pulled) != 0 {
		t.Errorf("a bare name must never be pulled (Docker Hub squatting), pulled %v", f.pulled)
	}
}

func TestEnsureLocalPullsRegistryRefs(t *testing.T) {
	f := &fakeImages{present: map[string]bool{}, stream: `{"status":"Pulling"}` + "\n" + `{"status":"Done"}`}
	if err := EnsureLocal(context.Background(), f, "registry.corp/octopus/agent:1"); err != nil {
		t.Fatal(err)
	}
	if len(f.pulled) != 1 {
		t.Errorf("pulled = %v", f.pulled)
	}

	f.stream = `{"error":"unauthorized: authentication required"}`
	err := EnsureLocal(context.Background(), f, "registry.corp/octopus/agent:2")
	if err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("an error in the pull stream must surface, got %v", err)
	}

	f.present["registry.corp/octopus/agent:3"] = true
	f.pulled = nil
	if err := EnsureLocal(context.Background(), f, "registry.corp/octopus/agent:3"); err != nil || len(f.pulled) != 0 {
		t.Errorf("present image: err=%v pulled=%v", err, f.pulled)
	}
}

package dockergctest

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/errdefs"
)

type Docker struct {
	Containers []types.Container
	Finished   map[string]string
	Self       map[string]types.ContainerJSON
	Images     []image.Summary
	Volumes    []*volume.Volume
	Networks   []network.Summary

	mu      sync.Mutex
	Removed []string
}

func (d *Docker) ContainerList(_ context.Context, _ container.ListOptions) ([]types.Container, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]types.Container(nil), d.Containers...), nil
}

func (d *Docker) ContainerInspect(_ context.Context, id string) (types.ContainerJSON, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if self, ok := d.Self[id]; ok {
		return self, nil
	}
	for i := range d.Containers {
		c := &d.Containers[i]
		if c.ID != id {
			continue
		}
		return types.ContainerJSON{
			ContainerJSONBase: &types.ContainerJSONBase{
				ID:    c.ID,
				State: &types.ContainerState{Status: c.State, FinishedAt: d.Finished[c.ID]},
			},
			Config: &container.Config{Labels: c.Labels},
		}, nil
	}
	//i18n:ignore test helper output
	return types.ContainerJSON{}, errdefs.NotFound(fmt.Errorf("no such container: %s", id))
}

func (d *Docker) ContainerRemove(_ context.Context, id string, opts container.RemoveOptions) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.Containers {
		c := &d.Containers[i]
		if c.ID != id {
			continue
		}
		if c.State == "running" && !opts.Force {
			//i18n:ignore test helper output
			return errdefs.Conflict(fmt.Errorf("container %s is running", id))
		}
		d.Containers = append(d.Containers[:i], d.Containers[i+1:]...)
		d.Removed = append(d.Removed, "container "+id)
		return nil
	}
	//i18n:ignore test helper output
	return errdefs.NotFound(fmt.Errorf("no such container: %s", id))
}

func (d *Docker) ImageList(_ context.Context, _ image.ListOptions) ([]image.Summary, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]image.Summary(nil), d.Images...), nil
}

func (d *Docker) ImageRemove(_ context.Context, ref string, _ image.RemoveOptions) ([]image.DeleteResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.Images {
		img := &d.Images[i]
		tagIdx := indexOf(img.RepoTags, ref)
		if img.ID != ref && tagIdx < 0 {
			continue
		}
		lastRef := img.ID == ref || len(img.RepoTags) == 1
		if lastRef && d.usedLocked(img.ID) {
			//i18n:ignore test helper output
			return nil, errdefs.Conflict(fmt.Errorf("image %s is used by a container", ref))
		}
		d.Removed = append(d.Removed, "image "+ref)
		if !lastRef {
			img.RepoTags = append(img.RepoTags[:tagIdx], img.RepoTags[tagIdx+1:]...)
			return []image.DeleteResponse{{Untagged: ref}}, nil
		}
		d.Images = append(d.Images[:i], d.Images[i+1:]...)
		return []image.DeleteResponse{{Deleted: ref}}, nil
	}
	//i18n:ignore test helper output
	return nil, errdefs.NotFound(fmt.Errorf("no such image: %s", ref))
}

func (d *Docker) usedLocked(id string) bool {
	for i := range d.Containers {
		if d.Containers[i].ImageID == id {
			return true
		}
	}
	return false
}

func (d *Docker) VolumeList(_ context.Context, _ volume.ListOptions) (volume.ListResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return volume.ListResponse{Volumes: append([]*volume.Volume(nil), d.Volumes...)}, nil
}

func (d *Docker) VolumeRemove(_ context.Context, name string, _ bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, v := range d.Volumes {
		if v.Name == name {
			d.Volumes = append(d.Volumes[:i], d.Volumes[i+1:]...)
			d.Removed = append(d.Removed, "volume "+name)
			return nil
		}
	}
	//i18n:ignore test helper output
	return errdefs.NotFound(fmt.Errorf("no such volume: %s", name))
}

func (d *Docker) NetworkList(_ context.Context, _ network.ListOptions) ([]network.Summary, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]network.Summary(nil), d.Networks...), nil
}

func (d *Docker) NetworkRemove(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.Networks {
		if name := d.Networks[i].Name; d.Networks[i].ID == id || name == id {
			d.Networks = append(d.Networks[:i], d.Networks[i+1:]...)
			d.Removed = append(d.Removed, "network "+name)
			return nil
		}
	}
	//i18n:ignore test helper output
	return errdefs.NotFound(fmt.Errorf("no such network: %s", id))
}

func (d *Docker) RemovedMatching(substr string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, r := range d.Removed {
		if strings.Contains(r, substr) {
			out = append(out, r)
		}
	}
	return out
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

func Container(id, name, img, imageID, state string, labels map[string]string) types.Container {
	return types.Container{
		ID: id, Names: []string{"/" + name}, Image: img, ImageID: imageID, State: state, Labels: labels,
	}
}

func Image(id string, size int64, tags ...string) image.Summary {
	return image.Summary{ID: id, Size: size, RepoTags: tags, Created: 1_700_000_000}
}

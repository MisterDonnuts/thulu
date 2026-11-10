package infra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/api/types/volume"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/personallogin"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
)

type fakeDocker struct {
	info    system.Info
	list    []types.Container
	listErr error
	stats   map[string][]container.StatsResponse
	df      types.DiskUsage

	mu sync.Mutex
}

func (f *fakeDocker) Info(context.Context) (system.Info, error) { return f.info, nil }

func (f *fakeDocker) ContainerList(context.Context, container.ListOptions) ([]types.Container, error) {
	return f.list, f.listErr
}

func (f *fakeDocker) ContainerStatsOneShot(_ context.Context, id string) (container.StatsResponseReader, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := f.stats[id]
	if len(q) == 0 {
		return container.StatsResponseReader{}, errors.New("no such container")
	}
	f.stats[id] = q[1:]
	b, _ := json.Marshal(q[0])
	return container.StatsResponseReader{Body: io.NopCloser(bytes.NewReader(b))}, nil
}

func (f *fakeDocker) DiskUsage(context.Context, types.DiskUsageOptions) (types.DiskUsage, error) {
	return f.df, nil
}

func statsAt(read time.Time, usage, system uint64, mem, inactive uint64) container.StatsResponse {
	var s container.StatsResponse
	s.Read = read
	s.CPUStats.CPUUsage.TotalUsage = usage
	s.CPUStats.SystemUsage = system
	s.CPUStats.OnlineCPUs = 4
	s.MemoryStats.Usage = mem
	s.MemoryStats.Limit = 1 << 30
	s.MemoryStats.Stats = map[string]uint64{"inactive_file": inactive}
	s.Networks = map[string]container.NetworkStats{"eth0": {RxBytes: 100, TxBytes: 50}}
	s.BlkioStats.IoServiceBytesRecursive = []container.BlkioStatEntry{{Op: "Read", Value: 7}, {Op: "write", Value: 9}}
	s.PidsStats.Current = 12
	return s
}

func TestCPUCores(t *testing.T) {
	t0 := time.Unix(1000, 0)
	cases := []struct {
		name      string
		prev, cur cpuSample
		want      float64
	}{
		{"host-time ratio", cpuSample{usage: 0, system: 0}, cpuSample{usage: 2e9, system: 4e9, online: 4}, 2},
		{"wall clock fallback", cpuSample{usage: 0, read: t0}, cpuSample{usage: 5e8, read: t0.Add(time.Second)}, 0.5},
		{"restarted container", cpuSample{usage: 9e9}, cpuSample{usage: 1e9, system: 1e9, online: 4}, 0},
		{"no elapsed time", cpuSample{usage: 1, read: t0}, cpuSample{usage: 2, read: t0}, 0},
	}
	for _, c := range cases {
		if got := cpuCores(c.prev, c.cur); !near(got, c.want) {
			t.Errorf("%s: cpuCores = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMemoryUsed(t *testing.T) {
	var v2, v1, odd container.MemoryStats
	v2.Usage, v2.Stats = 1000, map[string]uint64{"inactive_file": 300}
	v1.Usage, v1.Stats = 1000, map[string]uint64{"total_inactive_file": 100}
	odd.Usage, odd.Stats = 10, map[string]uint64{"inactive_file": 50}
	if memoryUsed(v2) != 700 || memoryUsed(v1) != 900 || memoryUsed(odd) != 10 {
		t.Errorf("memoryUsed = %d %d %d, want 700 900 10", memoryUsed(v2), memoryUsed(v1), memoryUsed(odd))
	}
}

func TestDockerSampleClassifiesAndMeasures(t *testing.T) {
	t0 := time.Unix(2000, 0)
	f := &fakeDocker{
		info: system.Info{ServerVersion: "27.5.1", NCPU: 4, MemTotal: 8 << 30, DockerRootDir: "/var/lib/docker"},
		list: []types.Container{
			{ID: "a1", Names: []string{"/octopus-coder-t1"}, State: "running", Created: 1700000000,
				Labels: map[string]string{agent.LabelManaged: "true", agent.LabelType: "coder", agent.LabelWorkspaceID: "ws1"}},
			{ID: "b2", Names: []string{"/old-run"}, State: "exited",
				Labels: map[string]string{agent.LabelManaged: "true"}},
			{ID: "c3", Names: []string{"/redis"}, State: "paused"},
		},
		stats: map[string][]container.StatsResponse{
			"a1": {
				statsAt(t0, 1e9, 10e9, 500, 100),
				statsAt(t0.Add(15*time.Second), 3e9, 18e9, 600, 100),
			},
		},
	}
	d := newDockerCollector(f)

	info, cs, probs := d.sample(context.Background())
	if len(probs) != 0 {
		t.Fatalf("problems = %+v", probs)
	}
	if info.Containers != 3 || info.Running != 1 || info.Paused != 1 || info.Stopped != 1 || info.ServerVersion != "27.5.1" {
		t.Errorf("info = %+v", info)
	}
	a := cs[0]
	if a.Kind != KindAgent || a.Name != "octopus-coder-t1" || a.WorkspaceID != "ws1" || !a.HasUsage {
		t.Errorf("agent row = %+v", a)
	}
	if a.CPUCores != 0 {
		t.Errorf("first read CPU = %v, want 0 until there is a previous read", a.CPUCores)
	}
	if a.MemBytes != 400 || a.NetRx != 100 || a.BlkRead != 7 || a.BlkWrite != 9 || a.Pids != 12 {
		t.Errorf("usage = %+v", a)
	}
	if cs[1].HasUsage || cs[2].Kind != KindOther {
		t.Errorf("stopped/foreign rows = %+v %+v", cs[1], cs[2])
	}

	_, cs, _ = d.sample(context.Background())
	if !near(cs[0].CPUCores, 1) {
		t.Errorf("second read CPU = %v cores, want 1", cs[0].CPUCores)
	}

	f.list = f.list[1:]
	d.sample(context.Background())
	if _, ok := d.prev["a1"]; ok {
		t.Error("counters of a removed container were kept")
	}
}

func TestDockerSampleListFailureIsAProblem(t *testing.T) {
	f := &fakeDocker{listErr: errors.New("socket closed")}
	_, cs, probs := newDockerCollector(f).sample(context.Background())
	if cs != nil || len(probs) != 1 || probs[0].Source != "docker" {
		t.Errorf("containers %v problems %+v, want none and one docker problem", cs, probs)
	}
}

func TestDockerDiskUsage(t *testing.T) {
	f := &fakeDocker{df: types.DiskUsage{
		LayersSize: 5000,
		Images:     make([]*image.Summary, 3),
		Volumes: []*volume.Volume{
			{Name: "small", UsageData: &volume.UsageData{Size: 10}},
			{Name: "big", UsageData: &volume.UsageData{Size: 900}},
			{Name: "unsized"},
			{Name: "octopus-login-claude-code-u1", Labels: map[string]string{personallogin.VolumeLabel: "claude-code"},
				UsageData: &volume.UsageData{Size: 5}},
		},
		BuildCache: []*types.BuildCache{{Size: 40}, {Size: 60, Shared: true}},
	}}
	var info DockerInfo
	if err := newDockerCollector(f).diskUsage(context.Background(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Images != 3 || info.ImagesSize != 5000 || info.VolumesSize != 915 || info.BuildCacheSize != 40 {
		t.Errorf("df = %+v", info)
	}
	if len(info.Volumes) != 3 || info.Volumes[0].Name != "big" || info.Volumes[2].Size != -1 {
		t.Errorf("volumes = %+v, want largest first and unsized as -1", info.Volumes)
	}
}

func TestDockerSampleLeavesAnotherInstallsContainersOut(t *testing.T) {
	dockerx.SetInstallID("this-install")
	t.Cleanup(func() { dockerx.SetInstallID("") })
	f := &fakeDocker{list: []types.Container{
		{ID: "s1", State: "running", Labels: map[string]string{
			sandbox.SandboxLabel: "true", dockerx.InstallLabel: "this-install"}},
		{ID: "s2", State: "running", Labels: map[string]string{
			sandbox.SandboxLabel: "true", sandbox.SandboxTaskLabel: "t-other", dockerx.InstallLabel: "other-install"}},
		{ID: "a2", State: "running", Labels: map[string]string{
			agent.LabelManaged: "true", agent.LabelWorkspaceID: "ws-other", dockerx.InstallLabel: "other-install"}},
		{ID: "s3", State: "running", Labels: map[string]string{sandbox.SandboxLabel: "true"}},
	}}
	_, cs, _ := newDockerCollector(f).sample(context.Background())
	want := map[string]Kind{"s1": KindSandbox, "s2": KindOtherInstall, "a2": KindOtherInstall, "s3": KindSandbox}
	for _, c := range cs {
		if c.Kind != want[c.ID] {
			t.Errorf("%s: kind = %q, want %q", c.ID, c.Kind, want[c.ID])
		}
		if c.Kind == KindOtherInstall && (c.WorkspaceID != "" || c.TaskID != "") {
			t.Errorf("%s: another install's container is attributed to workspace %q, task %q", c.ID, c.WorkspaceID, c.TaskID)
		}
	}
}

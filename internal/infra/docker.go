package infra

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/system"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/personallogin"
)

type DockerAPI interface {
	Info(ctx context.Context) (system.Info, error)
	ContainerList(ctx context.Context, options container.ListOptions) ([]types.Container, error)
	ContainerStatsOneShot(ctx context.Context, containerID string) (container.StatsResponseReader, error)
	DiskUsage(ctx context.Context, options types.DiskUsageOptions) (types.DiskUsage, error)
}

// Each stats call is a round trip through dockerd's cgroup reader; don't open one per container
// every sample.
const statsParallel = 6

const statsTimeout = 5 * time.Second

// The one-shot stats endpoint returns no previous sample, so the CPU rate needs our last read.
type cpuSample struct {
	usage  uint64
	system uint64
	online uint32
	read   time.Time
}

type dockerCollector struct {
	api DockerAPI

	mu   sync.Mutex
	prev map[string]cpuSample
}

func newDockerCollector(api DockerAPI) *dockerCollector {
	return &dockerCollector{api: api, prev: map[string]cpuSample{}}
}

func (d *dockerCollector) sample(ctx context.Context) (*DockerInfo, []Container, []Problem) {
	var problems []Problem
	info := &DockerInfo{}
	if in, err := d.api.Info(ctx); err != nil {
		problems = append(problems, Problem{Source: "docker", Err: apperr.Wrap(err, "infra.errors.docker_info")})
	} else {
		info.ServerVersion = in.ServerVersion
		info.OperatingSystem = in.OperatingSystem
		info.KernelVersion = in.KernelVersion
		info.NCPU = in.NCPU
		info.MemTotal = in.MemTotal
		info.StorageDriver = in.Driver
		info.RootDir = in.DockerRootDir
	}
	list, err := d.api.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		problems = append(problems, Problem{Source: "docker", Err: apperr.Wrap(err, "infra.errors.docker_list")})
		return info, nil, problems
	}
	info.ComposeProject = selfComposeProject(list)
	var projects []string
	if info.ComposeProject != "" {
		projects = []string{info.ComposeProject}
	}

	out := make([]Container, len(list))
	for i := range list {
		ctr := &list[i]
		c := Container{
			ID:     ctr.ID,
			Name:   containerName(ctr),
			Kind:   classifyDocker(ctr, projects),
			State:  ctr.State,
			Status: ctr.Status,
			Image:  ctr.Image,
		}
		if ctr.Created > 0 {
			c.StartedAt = time.Unix(ctr.Created, 0).UTC()
		}
		// Attribute only this install's containers: another install's workspace ids would leak in
		// as unknown workspaces.
		if c.Kind != KindOther && c.Kind != KindOtherInstall {
			dockerAttribution(&c, ctr.Labels)
		}
		c.running = ctr.State == "running"
		switch ctr.State {
		case "running":
			info.Running++
		case "paused":
			info.Paused++
		default:
			info.Stopped++
		}
		out[i] = c
	}
	info.Containers = len(list)
	d.readStats(ctx, out)
	return info, out, problems
}

func (d *dockerCollector) readStats(ctx context.Context, cs []Container) {
	sem := make(chan struct{}, statsParallel)
	var wg sync.WaitGroup
	live := map[string]bool{}
	for i := range cs {
		if !cs[i].running {
			continue
		}
		live[cs[i].ID] = true
		wg.Add(1)
		sem <- struct{}{}
		go func(c *Container) {
			defer wg.Done()
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, statsTimeout)
			defer cancel()
			st, err := d.oneShot(cctx, c.ID)
			if err != nil {
				return
			}
			d.apply(c, st)
		}(&cs[i])
	}
	wg.Wait()
	d.mu.Lock()
	for id := range d.prev {
		if !live[id] {
			delete(d.prev, id)
		}
	}
	d.mu.Unlock()
}

func (d *dockerCollector) oneShot(ctx context.Context, id string) (*container.StatsResponse, error) {
	r, err := d.api.ContainerStatsOneShot(ctx, id)
	if err != nil {
		return nil, err
	}
	defer logx.Close(r.Body, i18n.Log("infra.log.stats_body", "id", id))
	var st container.StatsResponse
	if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (d *dockerCollector) apply(c *Container, st *container.StatsResponse) {
	cur := cpuSample{
		usage:  st.CPUStats.CPUUsage.TotalUsage,
		system: st.CPUStats.SystemUsage,
		online: st.CPUStats.OnlineCPUs,
		read:   st.Read,
	}
	d.mu.Lock()
	prev, had := d.prev[c.ID]
	d.prev[c.ID] = cur
	d.mu.Unlock()

	c.HasUsage = true
	if had {
		c.CPUCores = cpuCores(prev, cur)
	}
	c.MemBytes = memoryUsed(st.MemoryStats)
	c.MemLimit = st.MemoryStats.Limit
	for _, n := range st.Networks {
		c.NetRx += n.RxBytes
		c.NetTx += n.TxBytes
	}
	for _, e := range st.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(e.Op) {
		case "read":
			c.BlkRead += e.Value
		case "write":
			c.BlkWrite += e.Value
		}
	}
	c.Pids = st.PidsStats.Current
}

// Where dockerd reports no host CPU time (rootless, some cgroup v2 setups), wall-clock time between
// reads stands in.
func cpuCores(prev, cur cpuSample) float64 {
	if cur.usage < prev.usage {
		return 0
	}
	du := float64(cur.usage - prev.usage)
	if cur.system > prev.system && cur.online > 0 {
		return du / float64(cur.system-prev.system) * float64(cur.online)
	}
	if wall := cur.read.Sub(prev.read); wall > 0 {
		return du / float64(wall.Nanoseconds())
	}
	return 0
}

// cgroup v2 names the counter inactive_file, v1 total_inactive_file.
func memoryUsed(m container.MemoryStats) uint64 {
	cache, ok := m.Stats["inactive_file"]
	if !ok {
		cache = m.Stats["total_inactive_file"]
	}
	if cache > m.Usage {
		return m.Usage
	}
	return m.Usage - cache
}

// dockerd walks every volume to size it, too slow for the fast loop on a disk of worktrees.
func (d *dockerCollector) diskUsage(ctx context.Context, info *DockerInfo) error {
	du, err := d.api.DiskUsage(ctx, types.DiskUsageOptions{})
	if err != nil {
		return apperr.Wrap(err, "infra.errors.docker_df")
	}
	info.Images = len(du.Images)
	info.ImagesSize = du.LayersSize
	info.Volumes = info.Volumes[:0]
	info.VolumesSize = 0
	for _, v := range du.Volumes {
		size := int64(-1)
		if v.UsageData != nil {
			size = v.UsageData.Size
		}
		if size > 0 {
			info.VolumesSize += size
		}
		// Personal login volume names carry the user's id: count the size, hide the name.
		if _, personal := v.Labels[personallogin.VolumeLabel]; personal {
			continue
		}
		info.Volumes = append(info.Volumes, VolumeSize{Name: v.Name, Size: size})
	}
	sort.Slice(info.Volumes, func(i, j int) bool { return info.Volumes[i].Size > info.Volumes[j].Size })
	info.BuildCacheSize = 0
	for _, b := range du.BuildCache {
		if !b.Shared {
			info.BuildCacheSize += b.Size
		}
	}
	return nil
}

// Inside a container the hostname is the container id's prefix.
func selfComposeProject(list []types.Container) string {
	host, err := os.Hostname()
	if err != nil || len(host) < 12 {
		return ""
	}
	for i := range list {
		if strings.HasPrefix(list[i].ID, host) {
			return list[i].Labels[labelComposeProject]
		}
	}
	return ""
}

func containerName(ctr *types.Container) string {
	if len(ctr.Names) == 0 {
		if len(ctr.ID) > 12 {
			return ctr.ID[:12]
		}
		return ctr.ID
	}
	return strings.TrimPrefix(ctr.Names[0], "/")
}

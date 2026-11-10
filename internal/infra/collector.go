package infra

import (
	"context"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/kubeapi"
	"github.com/MisterDonnuts/thulu/internal/types"
)

const (
	DefaultInterval     = 15 * time.Second
	DefaultDBInterval   = time.Minute
	DefaultSlowInterval = 10 * time.Minute
	DefaultRetention    = 2 * time.Hour

	slowTimeout   = 3 * time.Minute
	sampleTimeout = 12 * time.Second
)

type Config struct {
	ProcRoot   string
	DataDir    string
	ExtraDisks []string

	Docker DockerAPI
	Kube   *KubeSource

	DB            *sql.DB
	Settings      SettingsReader
	Admission     func() Admission
	SandboxesHeld func(context.Context) (int, error)
	Plan          func(workspaceID string) (plan string, concurrency int)

	Interval, DBInterval, SlowInterval, Retention time.Duration
	Now                                           func() time.Time
}

type Collector struct {
	cfg    Config
	proc   procFS
	docker *dockerCollector

	// Touched only by the sampling goroutine, so no lock.
	prevCPU   cpuTimes
	hasCPU    bool
	prevNet   map[string]netCounters
	prevNetAt time.Time
	db        dbStats
	dbAt      time.Time
	dbProbs   []Problem
	slowBusy  atomic.Bool
	slowAt    time.Time

	mu         sync.RWMutex
	snap       Snapshot
	containers []Container
	history    *ring[Point]
	dockerDF   *DockerInfo
	storage    Storage
	wsStorage  map[string]int64
	slowProbs  []Problem
}

func New(cfg Config) *Collector {
	if cfg.ProcRoot == "" {
		cfg.ProcRoot = "/proc"
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.DBInterval <= 0 {
		cfg.DBInterval = DefaultDBInterval
	}
	if cfg.SlowInterval <= 0 {
		cfg.SlowInterval = DefaultSlowInterval
	}
	if cfg.Retention <= 0 {
		cfg.Retention = DefaultRetention
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	c := &Collector{
		cfg:       cfg,
		proc:      procFS{root: cfg.ProcRoot},
		prevNet:   map[string]netCounters{},
		history:   newRing[Point](int(cfg.Retention / cfg.Interval)),
		wsStorage: map[string]int64{},
	}
	if cfg.Docker != nil {
		c.docker = newDockerCollector(cfg.Docker)
	}
	return c
}

func (c *Collector) Run(ctx context.Context) {
	log.Printf("[infra] %s", i18n.Log("infra.log.started", "interval", c.cfg.Interval, "slow", c.cfg.SlowInterval))
	t := time.NewTicker(c.cfg.Interval)
	defer t.Stop()
	for {
		c.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *Collector) tick(ctx context.Context) {
	now := c.cfg.Now()
	if c.cfg.DB != nil && (c.dbAt.IsZero() || now.Sub(c.dbAt) >= c.cfg.DBInterval) {
		dctx, cancel := context.WithTimeout(ctx, sampleTimeout)
		c.db, c.dbProbs = readDB(dctx, c.cfg.DB, startOfDay(now))
		cancel()
		c.dbAt = now
	}
	if c.slowAt.IsZero() || now.Sub(c.slowAt) >= c.cfg.SlowInterval {
		if c.slowBusy.CompareAndSwap(false, true) {
			c.slowAt = now
			projects := c.db.projects
			go c.slow(ctx, projects)
		}
	}
	sctx, cancel := context.WithTimeout(ctx, sampleTimeout)
	defer cancel()
	c.sample(sctx, now)
}

func (c *Collector) slow(ctx context.Context, projects []projectRow) {
	defer c.slowBusy.Store(false)
	ctx, cancel := context.WithTimeout(ctx, slowTimeout)
	defer cancel()
	var problems []Problem
	var df *DockerInfo
	if c.docker != nil {
		df = &DockerInfo{}
		if err := c.docker.diskUsage(ctx, df); err != nil {
			problems = append(problems, Problem{Source: "docker", Err: err})
			df = nil
		} else {
			df.DiskUsageAt = c.cfg.Now()
		}
	}
	var walk storageWalk
	if c.cfg.DataDir != "" {
		walk = walkDataDir(ctx, c.cfg.DataDir)
		walk.storage.ComputedAt = c.cfg.Now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if df != nil {
		c.dockerDF = df
	}
	if c.cfg.DataDir != "" {
		c.storage = walk.storage
		c.wsStorage = workspaceStorage(walk.byProject, projects)
	}
	c.slowProbs = problems
}

func (c *Collector) sample(ctx context.Context, now time.Time) {
	snap := Snapshot{CollectedAt: now.UTC(), IntervalSeconds: int(c.cfg.Interval / time.Second)}
	snap.Host = c.sampleHost(now)
	snap.Problems = append(snap.Problems, c.dbProbs...)

	var containers []Container
	switch {
	case c.docker != nil:
		info, cs, probs := c.docker.sample(ctx)
		snap.Runtime = Runtime{Mode: "docker", Docker: info}
		containers = cs
		snap.Problems = append(snap.Problems, probs...)
	case c.cfg.Kube != nil:
		info, cs, probs := sampleKube(ctx, *c.cfg.Kube)
		snap.Runtime = Runtime{Mode: "kubernetes", Kube: info}
		containers = cs
		snap.Problems = append(snap.Problems, probs...)
	default:
		snap.Runtime = Runtime{Mode: "none"}
	}

	names := workspaceNames(c.db.names)
	for i := range containers {
		containers[i].WorkspaceID, containers[i].WorkspaceName = names.resolve(containers[i].WorkspaceID)
	}
	if k := snap.Runtime.Kube; k != nil {
		for i := range k.Quotas {
			k.Quotas[i].WorkspaceID, k.Quotas[i].WorkspaceName = names.resolve(k.Quotas[i].WorkspaceID)
		}
		for i := range k.PVCs {
			k.PVCs[i].WorkspaceID, _ = names.resolve(k.PVCs[i].WorkspaceID)
		}
	}
	sort.SliceStable(containers, func(i, j int) bool { return lessContainer(&containers[i], &containers[j]) })

	snap.Kinds, snap.Totals = kindUsage(containers)
	var octProbs []Problem
	snap.Octopus, octProbs = c.octopus(ctx, containers)
	snap.Problems = append(snap.Problems, octProbs...)

	c.mu.Lock()
	defer c.mu.Unlock()
	if df := c.dockerDF; df != nil && snap.Runtime.Docker != nil {
		d := snap.Runtime.Docker
		d.DiskUsageAt, d.Images, d.ImagesSize = df.DiskUsageAt, df.Images, df.ImagesSize
		d.VolumesSize, d.BuildCacheSize, d.Volumes = df.VolumesSize, df.BuildCacheSize, df.Volumes
	}
	snap.Storage = c.storage
	for i := range snap.Octopus.Workspaces {
		snap.Octopus.Workspaces[i].StorageBytes = c.wsStorage[snap.Octopus.Workspaces[i].ID]
	}
	snap.Problems = append(snap.Problems, c.slowProbs...)
	c.snap = snap
	c.containers = containers
	c.history.push(Point{
		T:           snap.CollectedAt,
		CPUPercent:  snap.Host.CPUPercent,
		MemPercent:  snap.Host.Memory.Percent,
		DiskPercent: dataDiskPercent(snap.Host.Disks),
		RxBps:       snap.Host.Net.RxBps,
		TxBps:       snap.Host.Net.TxBps,
		Running:     snap.Totals.Running,
		ActiveRuns:  snap.Octopus.ActiveRunsTotal,
	})
}

func (c *Collector) sampleHost(now time.Time) Host {
	h := Host{Arch: runtime.GOARCH, Threads: runtime.NumCPU(), Cores: runtime.NumCPU()}
	if name, err := os.Hostname(); err == nil {
		h.Hostname = name
	}
	h.InContainer = inContainer()
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		h.OS = parseOSRelease(b)
	}
	if h.OS == "" {
		h.OS = runtime.GOOS
	}
	h.ProcAvailable = c.proc.available()
	if h.ProcAvailable {
		c.readProc(&h, now)
	}
	h.Disks = c.disks()
	return h
}

func (c *Collector) readProc(h *Host, now time.Time) {
	if b, err := c.proc.read("stat"); err == nil {
		if cur, ok := parseStat(b); ok {
			if c.hasCPU {
				h.CPUPercent = cpuPercent(c.prevCPU, cur)
			}
			c.prevCPU, c.hasCPU = cur, true
		}
	}
	if b, err := c.proc.read("meminfo"); err == nil {
		h.Memory, _ = parseMeminfo(b)
	}
	if b, err := c.proc.read("loadavg"); err == nil {
		h.Load, _ = parseLoadavg(b)
	}
	if b, err := c.proc.read("uptime"); err == nil {
		h.UptimeSeconds, _ = parseUptime(b)
	}
	if b, err := c.proc.read("sys/kernel/osrelease"); err == nil {
		h.Kernel = strings.TrimSpace(string(b))
	}
	if b, err := c.proc.read("cpuinfo"); err == nil {
		ci := parseCPUInfo(b)
		h.CPUModel = ci.model
		if ci.threads > 0 {
			h.Threads, h.Cores = ci.threads, ci.cores
		}
	}
	b, hostNet, err := c.netDev()
	if err == nil {
		cur := parseNetDev(b)
		h.Net = netRates(c.prevNet, cur, now.Sub(c.prevNetAt), !c.prevNetAt.IsZero())
		c.prevNet, c.prevNetAt = cur, now
	}
	h.NetInContainer = h.InContainer && !hostNet
}

// /proc/net links to self/net, the reader's own netns; with a host /proc mounted in, read pid 1's
// to get the host's counters.
func (c *Collector) netDev() (b []byte, hostNet bool, err error) {
	if c.cfg.ProcRoot != "/proc" {
		if host, herr := c.proc.read("1/net/dev"); herr == nil {
			return host, true, nil
		}
	}
	b, err = c.proc.read("net/dev")
	return b, false, err
}

func netRates(prev, cur map[string]netCounters, dt time.Duration, hasPrev bool) Net {
	var n Net
	secs := dt.Seconds()
	names := make([]string, 0, len(cur))
	for name := range cur {
		if physicalIface(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		cc := cur[name]
		ni := NetInterface{Name: name, RxBytes: cc.rx, TxBytes: cc.tx}
		if p, ok := prev[name]; ok && hasPrev && secs > 0 && cc.rx >= p.rx && cc.tx >= p.tx {
			ni.RxBps = float64(cc.rx-p.rx) / secs
			ni.TxBps = float64(cc.tx-p.tx) / secs
		}
		n.RxBps += ni.RxBps
		n.TxBps += ni.TxBps
		n.Interfaces = append(n.Interfaces, ni)
	}
	return n
}

func (c *Collector) diskCandidates() [][2]string {
	out := [][2]string{}
	if c.cfg.DataDir != "" {
		out = append(out, [2]string{"data", c.cfg.DataDir})
	}
	out = append(out, [2]string{"root", "/"})
	c.mu.RLock()
	root := ""
	if c.snap.Runtime.Docker != nil {
		root = c.snap.Runtime.Docker.RootDir
	}
	c.mu.RUnlock()
	if root == "" {
		root = "/var/lib/docker"
	}
	out = append(out,
		[2]string{"docker", root},
		[2]string{"containerd", "/var/lib/rancher/k3s"},
		[2]string{"containerd", "/var/lib/containerd"},
		[2]string{"postgres", "/var/lib/postgresql/data"},
	)
	for _, p := range c.cfg.ExtraDisks {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, [2]string{"extra", p})
		}
	}
	return out
}

func (c *Collector) disks() []Disk {
	var out []Disk
	byDev := map[uint64]int{}
	for _, rp := range c.diskCandidates() {
		role, path := rp[0], filepath.Clean(rp[1])
		if _, err := os.Stat(path); err != nil {
			continue
		}
		d, dev, err := statDisk(path)
		if err != nil {
			continue
		}
		if i, ok := byDev[dev]; ok && dev != 0 {
			if !contains(out[i].Roles, role) {
				out[i].Roles = append(out[i].Roles, role)
			}
			continue
		}
		d.Roles = []string{role}
		byDev[dev] = len(out)
		out = append(out, d)
	}
	return out
}

func (c *Collector) octopus(ctx context.Context, cs []Container) (Octopus, []Problem) {
	o := Octopus{Counts: c.db.counts, Postgres: c.db.postgres}
	if c.cfg.Admission != nil {
		o.Queue = c.cfg.Admission()
	}
	agg := aggregateRunning(cs)
	o.ActiveRuns, o.ActiveRunsTotal = agg.runs, agg.agents
	limits, problems := c.limits(ctx, o.Queue, agg)
	o.Limits = limits
	o.Workspaces = c.workspaceLoads(agg.byWorkspace)
	return o, problems
}

type wsAgg struct {
	containers, agents int
	cpu                float64
	mem                uint64
}

type runningAgg struct {
	byKind      map[Kind]int
	byWorkspace map[string]*wsAgg
	runs        []ActiveRun
	agents      int
	mentions    int
}

func aggregateRunning(cs []Container) runningAgg {
	agg := runningAgg{byKind: map[Kind]int{}, byWorkspace: map[string]*wsAgg{}}
	runs := map[[3]string]*ActiveRun{}
	for i := range cs {
		ct := &cs[i]
		if !ct.running {
			continue
		}
		agg.byKind[ct.Kind]++
		if ct.WorkspaceID != "" {
			a := agg.byWorkspace[ct.WorkspaceID]
			if a == nil {
				a = &wsAgg{}
				agg.byWorkspace[ct.WorkspaceID] = a
			}
			a.containers++
			a.cpu += ct.CPUCores
			a.mem += ct.MemBytes
			if ct.Kind == KindAgent {
				a.agents++
			}
		}
		if ct.Kind != KindAgent {
			continue
		}
		key := [3]string{ct.WorkspaceID, ct.Agent, ct.Stage}
		r := runs[key]
		if r == nil {
			r = &ActiveRun{WorkspaceID: ct.WorkspaceID, WorkspaceName: ct.WorkspaceName, Agent: ct.Agent, Stage: ct.Stage}
			runs[key] = r
		}
		r.Count++
		agg.agents++
		if ct.Stage == string(types.StageMention) {
			agg.mentions++
		}
	}
	for _, r := range runs {
		agg.runs = append(agg.runs, *r)
	}
	sort.Slice(agg.runs, func(i, j int) bool {
		a, b := agg.runs[i], agg.runs[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.WorkspaceName+a.Agent+a.Stage < b.WorkspaceName+b.Agent+b.Stage
	})
	return agg
}

func (c *Collector) workspaceLoads(perWS map[string]*wsAgg) []WorkspaceLoad {
	ids := append([]string(nil), c.db.workspaces...)
	seen := map[string]bool{"": true}
	for _, id := range ids {
		seen[id] = true
	}
	extra := func(id string) {
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	for id := range perWS {
		extra(id)
	}
	for id := range c.db.usage {
		extra(id)
	}
	out := make([]WorkspaceLoad, 0, len(ids))
	for _, id := range ids {
		w := WorkspaceLoad{ID: id, Name: c.db.names[id]}
		if a := perWS[id]; a != nil {
			w.RunningContainers, w.RunningAgents, w.CPUCores, w.MemBytes = a.containers, a.agents, a.cpu, a.mem
		}
		if u, ok := c.db.usage[id]; ok {
			w.RunsToday, w.TokensToday, w.CostToday = u.runs, u.tokens, u.cost
		}
		if c.cfg.Plan != nil {
			w.Plan, w.ConcurrencyLimit = c.cfg.Plan(id)
		}
		out = append(out, w)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.RunningContainers != b.RunningContainers {
			return a.RunningContainers > b.RunningContainers
		}
		return a.CostToday > b.CostToday
	})
	return out
}

// Agent gates take the max of admitted and running: containers that outlived a daemon restart or
// were
// adopted by the watcher use the machine without an admission slot.
func (c *Collector) limits(ctx context.Context, q Admission, agg runningAgg) ([]Limit, []Problem) {
	out := []Limit{
		{Key: limitAgents, Cap: q.Agent.Cap, InUse: max(q.Agent.Inflight, agg.agents-agg.mentions)},
		{Key: limitMentions, Cap: q.Mention.Cap, InUse: max(q.Mention.Inflight, agg.mentions)},
		{Key: limitSandbox, InUse: agg.byKind[KindSandbox]},
		{Key: limitChats, InUse: agg.byKind[KindChat]},
	}
	var problems []Problem
	if held := c.cfg.SandboxesHeld; held != nil {
		if n, err := held(ctx); err != nil {
			problems = append(problems, Problem{Source: "postgres",
				Err: apperr.Wrap(err, "infra.errors.sandboxes_held")})
		} else {
			out[2].InUse = n
		}
	}
	s := c.cfg.Settings
	if s == nil {
		return out, problems
	}
	read := func(l *Limit, get func(context.Context) (int, error)) {
		n, err := get(ctx)
		if err != nil {
			problems = append(problems, Problem{Source: "settings",
				Err: apperr.Wrap(err, "infra.errors.setting", "key", l.Key)})
			return
		}
		l.Cap = n
	}
	if q.Agent.Cap == 0 {
		read(&out[0], s.AgentMaxContainers)
	}
	if q.Mention.Cap == 0 {
		read(&out[1], s.MentionMaxContainers)
	}
	read(&out[2], s.SandboxMax)
	read(&out[3], s.ChatMax)
	return out, problems
}

// Problems is a fresh slice the caller may append to; everything else is shared and must not be
// modified.
func (c *Collector) Overview() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := c.snap
	s.Problems = append([]Problem(nil), c.snap.Problems...)
	return s
}

func (c *Collector) History(d time.Duration) []Point {
	c.mu.RLock()
	all := c.history.values()
	c.mu.RUnlock()
	if len(all) == 0 {
		return all
	}
	cut := all[len(all)-1].T.Add(-d)
	i := sort.Search(len(all), func(i int) bool { return !all[i].T.Before(cut) })
	return all[i:]
}

func (c *Collector) Containers() []Container {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]Container(nil), c.containers...)
}

func (c *Collector) Forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.containers = slices.DeleteFunc(c.containers, func(ct Container) bool { return ct.ID == id })
}

func (c *Collector) Interval() time.Duration { return c.cfg.Interval }

func kindUsage(cs []Container) ([]KindUsage, ContainerTotals) {
	agg := map[Kind]*KindUsage{}
	var t ContainerTotals
	for i := range cs {
		ct := &cs[i]
		t.Total++
		k := agg[ct.Kind]
		if k == nil {
			k = &KindUsage{Kind: ct.Kind}
			agg[ct.Kind] = k
		}
		k.Count++
		if ct.running {
			t.Running++
			k.Running++
			k.CPUCores += ct.CPUCores
			k.MemBytes += ct.MemBytes
		}
	}
	out := make([]KindUsage, 0, len(agg))
	for _, kind := range kindOrder {
		if k := agg[kind]; k != nil {
			out = append(out, *k)
		}
	}
	return out, t
}

func lessContainer(a, b *Container) bool {
	if a.running != b.running {
		return a.running
	}
	if ka, kb := kindRank(a.Kind), kindRank(b.Kind); ka != kb {
		return ka < kb
	}
	return a.Name < b.Name
}

func kindRank(k Kind) int {
	for i, o := range kindOrder {
		if o == k {
			return i
		}
	}
	return len(kindOrder)
}

type workspaceNames map[string]string

func (n workspaceNames) resolve(id string) (string, string) {
	if id == "" {
		return "", ""
	}
	if name, ok := n[id]; ok {
		return id, name
	}
	for wid, name := range n {
		if kubeapi.SanitizeLabel(wid) == id {
			return wid, name
		}
	}
	return id, ""
}

func dataDiskPercent(ds []Disk) float64 {
	for _, d := range ds {
		if contains(d.Roles, "data") {
			return d.Percent
		}
	}
	if len(ds) > 0 {
		return ds[0].Percent
	}
	return 0
}

func inContainer() bool {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return true
	}
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

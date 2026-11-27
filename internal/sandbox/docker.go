package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"

	"github.com/MisterDonnuts/thulu/internal/agentnet"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/catrust"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/imageref"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/proxyenv"
	"github.com/MisterDonnuts/thulu/internal/sandbox/sandboxid"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func rootlessMode() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("OCTOPUS_ROOTLESS_AGENTS")))
	return v == "true" || v == "1" || v == "yes"
}

const DefaultRuntimeImage = sandboxid.DefaultRuntimeImage

func RuntimeImageFromEnv(getenv func(string) string) string {
	if v := strings.TrimSpace(getenv(EnvSandboxImage)); v != "" {
		return v
	}
	return DefaultRuntimeImage
}

const ExecOutputCap = 100 * 1024

// The watcher finds sandbox containers by these labels.
const SandboxLabel = sandboxid.ContainerLabel
const SandboxTaskLabel = "thulu.sandbox.task"

type Docker struct {
	cli    *client.Client
	image  string
	limits dockerLimits
	cache  bool
	// Memory of the Docker host (the VM under Colima or Docker Desktop), read once; 0 while unknown.
	hostMem atomic.Int64
}

const (
	// Default 127.0.0.1: agent-network peers reach the sandbox by name, so only a person on the
	// Docker host needs the published port.
	EnvSandboxPublishIP = "OCTOPUS_SANDBOX_PUBLISH_IP"
	EnvSandboxPidsLimit = "OCTOPUS_SANDBOX_PIDS_LIMIT"
	EnvSandboxCPUs      = "OCTOPUS_SANDBOX_CPUS"
	// Size of the sandbox's /tmp, a tmpfs: by default 2g or less (see defaultTmpfsSize), a size such
	// as "1g" used as given, "off" for the image's disk-backed one.
	EnvSandboxTmpSize = "OCTOPUS_SANDBOX_TMP_SIZE"
)

const (
	defaultSandboxPublishIP = "127.0.0.1"
	defaultSandboxPids      = 4096
	defaultSandboxCPUs      = 2.0
	defaultSandboxTmpSize   = "2g"
)

// Dropped from Docker's default set: NET_RAW (packet spoofing), MKNOD, SETFCAP, SETPCAP,
// SYS_CHROOT, AUDIT_WRITE.
var sandboxRootCaps = []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "SETUID", "SETGID", "KILL", "NET_BIND_SERVICE"}

type dockerLimits struct {
	publishIP string
	pids      int64
	nanoCPUs  int64
	// Empty leaves /tmp on the container's disk, where nothing bounds it.
	tmpSize string
	// The operator set tmpSize: it is used as given. The default shrinks to fit the memory.
	tmpExplicit bool
}

func limitsFromEnv(getenv func(string) string) dockerLimits {
	l := dockerLimits{
		publishIP: defaultSandboxPublishIP, pids: defaultSandboxPids, nanoCPUs: int64(defaultSandboxCPUs * 1e9),
		tmpSize: defaultSandboxTmpSize,
	}
	if v := strings.TrimSpace(getenv(EnvSandboxPublishIP)); v != "" {
		if net.ParseIP(v) != nil {
			l.publishIP = v
		} else {
			log.Printf("[sandbox] %s", i18n.Log("sandbox.log.publish_ip_ignored", "env", EnvSandboxPublishIP))
		}
	}
	if v := strings.TrimSpace(getenv(EnvSandboxPidsLimit)); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			l.pids = n
		} else {
			log.Printf("[sandbox] %s", i18n.Log("sandbox.log.pids_limit_ignored", "env", EnvSandboxPidsLimit))
		}
	}
	if v := strings.ToLower(strings.TrimSpace(getenv(EnvSandboxTmpSize))); v != "" {
		switch {
		case v == "off" || v == "0":
			l.tmpSize = ""
		case tmpSizeRe.MatchString(v):
			l.tmpSize = v
			l.tmpExplicit = true
		default:
			log.Printf("[sandbox] %s", i18n.Log("sandbox.log.tmp_size_ignored", "env", EnvSandboxTmpSize))
		}
	}
	if v := strings.TrimSpace(getenv(EnvSandboxCPUs)); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			l.nanoCPUs = int64(f * 1e9)
		} else {
			log.Printf("[sandbox] %s", i18n.Log("sandbox.log.cpus_ignored", "env", EnvSandboxCPUs))
		}
	}
	return l
}

var tmpSizeRe = regexp.MustCompile(`^[1-9]\d*[kmg]?$`)

// A tmpfs lives in memory, and the sandbox's memory cap does not protect a Docker host that has
// less than it (a 3 GB Colima VM under the 6 GB default): the default /tmp takes at most a quarter
// of the cap and an eighth of the host, so a repo copied to /tmp fills /tmp, not the host's RAM.
const (
	tmpfsCapShare  = 4
	tmpfsHostShare = 8
	tmpfsFloor     = 64 << 20
)

func (d *Docker) tmpfsSize(memoryBytes int64) string {
	if d.limits.tmpSize == "" || d.limits.tmpExplicit {
		return d.limits.tmpSize
	}
	return defaultTmpfsSize(memoryBytes, d.hostMem.Load())
}

func defaultTmpfsSize(memoryBytes, hostMem int64) string {
	size := int64(2 << 30)
	if memoryBytes >= 256<<20 {
		size = min(size, memoryBytes/tmpfsCapShare)
	}
	if hostMem > 0 {
		size = min(size, hostMem/tmpfsHostShare)
	}
	size = max(size, tmpfsFloor)
	return strconv.FormatInt(size>>20, 10) + "m"
}

// learnHostMemory reads the Docker host's memory once; a failed read is tried again on the next
// create.
func (d *Docker) learnHostMemory(ctx context.Context) {
	if d.cli == nil || d.hostMem.Load() > 0 {
		return
	}
	if info, err := d.cli.Info(ctx); err == nil && info.MemTotal > 0 {
		d.hostMem.Store(info.MemTotal)
	}
}

func NewDocker(cli *client.Client, image string) *Docker {
	if image == "" {
		image = DefaultRuntimeImage
	}
	cache := cacheOnFromEnv(os.Getenv)
	return &Docker{cli: cli, image: imageref.Resolve(image), limits: limitsFromEnv(os.Getenv), cache: cache}
}

func (d *Docker) Image() string { return d.image }

func (d *Docker) MountsHostPaths() bool { return true }

type CreateOpts struct {
	TaskID        string
	WorkspaceID   string
	WorkspacePath string
	ProjectsPaths map[string]string
	ExposePorts   []int
	ExtraLabels   map[string]string
	ExtraEnv      []string
	// Values below 256 MB are ignored: such a container cannot even boot Node.
	MemoryBytes int64
	// Daemon-in-container: host paths do not exist on the Docker host, so they become subpaths of
	// DataVolume relative to DataRoot.
	// Mirrors the same handling in types.LaunchParams.
	DataVolume string
	DataRoot   string
	// Only the prewarm job may set SnapshotsWritable.
	SnapshotsWritable bool
}

type CreateResult struct {
	ContainerID string
	HostPorts   map[int]int
}

func (d *Docker) Create(ctx context.Context, opts CreateOpts) (*CreateResult, error) {
	if opts.TaskID == "" {
		return nil, apperr.Invalid("sandbox.errors.create_task_required")
	}
	exposePorts := appPorts(opts)

	if err := imageref.EnsureLocal(ctx, d.cli, d.image); err != nil {
		return nil, apperr.Wrap(err, "sandbox.errors.image")
	}
	netName, err := agentnet.Ensure(ctx, d.cli)
	if err != nil {
		return nil, apperr.Wrap(err, "sandbox.errors.network")
	}
	caBundle, err := catrust.Load()
	if err != nil {
		return nil, apperr.Wrap(err, "sandbox.errors.extra_ca_bundle")
	}

	exposed := nat.PortSet{}
	bindings := nat.PortMap{}
	for _, p := range exposePorts {
		key := nat.Port(strconv.Itoa(p) + "/tcp")
		exposed[key] = struct{}{}
		bindings[key] = []nat.PortBinding{{HostIP: d.limits.publishIP, HostPort: ""}}
	}

	mounts := buildSandboxMounts(opts)
	gitMounts, gitEnv := sandboxWorktreeGit(opts)
	mounts = append(mounts, gitMounts...)
	cacheMounts, cacheVars := d.cacheWiring(opts)
	mounts = append(mounts, cacheMounts...)

	labels := map[string]string{
		SandboxLabel:     "true",
		SandboxTaskLabel: opts.TaskID,
	}
	for k, v := range opts.ExtraLabels {
		labels[k] = v
	}
	dockerx.StampInstall(labels)

	containerName := ContainerName(opts.TaskID)
	// A container under this name has no row (the caller checked): a daemon that died between create
	// and record, or a Release whose remove failed. Left there it fails this create on the name.
	if rmErr := d.cli.ContainerRemove(ctx, containerName, container.RemoveOptions{Force: true, RemoveVolumes: true}); rmErr == nil {
		log.Printf("[sandbox] %s", i18n.Log("sandbox.log.replaced_stale_container", "task", opts.TaskID, "name", containerName))
	} else if !client.IsErrNotFound(rmErr) {
		return nil, apperr.Wrap(rmErr, "sandbox.errors.remove_stale_container", "name", containerName)
	}

	rootless := rootlessMode()
	env := sandboxEnv(append(append(slices.Clip(opts.ExtraEnv), gitEnv...), cacheVars...), len(caBundle) > 0)
	cfg := sandboxContainerConfig(d.image, labels, exposed, env, rootless)

	d.learnHostMemory(ctx)
	host := d.sandboxHostConfig(mounts, bindings, netName, rootless, opts.MemoryBytes)
	host.LogConfig = dockerx.ContainerLogConfig(ctx, d.cli)
	dockerx.ApplyInit(ctx, d.cli, d.image, cfg.Entrypoint, host)
	netCfg := &network.NetworkingConfig{}

	resp, err := d.cli.ContainerCreate(ctx, cfg, host, netCfg, nil, containerName)
	if err != nil {
		return nil, apperr.Wrap(err, "sandbox.errors.container_create")
	}
	if len(caBundle) > 0 {
		if caErr := d.copyCABundle(ctx, resp.ID, caBundle); caErr != nil {
			//nolint:errcheck,contextcheck // best-effort cleanup after copy failure; fresh ctx because original may be canceled
			_ = d.cli.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
			return nil, caErr
		}
	}
	if startErr := d.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); startErr != nil {
		//nolint:errcheck,contextcheck // best-effort cleanup after start failure; fresh ctx because original may be canceled
		_ = d.cli.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
		return nil, apperr.Wrap(startErr, "sandbox.errors.container_start")
	}

	hostPorts, err := d.inspectPortMap(ctx, resp.ID)
	if err != nil {
		//nolint:errcheck,contextcheck // best-effort cleanup after inspect failure; fresh ctx because original may be canceled
		_ = d.cli.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
		return nil, apperr.Wrap(err, "sandbox.errors.inspect_port_map")
	}
	logx.Warn(agentnet.ConnectPeers(ctx, d.cli, netName), "sandbox.log.connect_peers", "network", netName)

	return &CreateResult{
		ContainerID: shortID(resp.ID),
		HostPorts:   hostPorts,
	}, nil
}

func ContainerName(ownerID string) string { return "octopus-sb-" + sanitizeTaskID(ownerID) }

// ListedContainer is a sandbox container (a pod in Kubernetes) as the runtime lists it, row or not.
type ListedContainer struct {
	ID      string
	Name    string
	Owner   string
	State   string
	Created time.Time
}

// ListSandboxes lists this install's sandbox containers in any state.
func (d *Docker) ListSandboxes(ctx context.Context) ([]ListedContainer, error) {
	list, err := d.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(
		filters.Arg("label", SandboxLabel+"=true"),
	)})
	if err != nil {
		return nil, err
	}
	out := make([]ListedContainer, 0, len(list))
	for i := range list {
		c := &list[i]
		if dockerx.ForeignInstall(c.Labels) {
			continue
		}
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out = append(out, ListedContainer{
			ID: shortID(c.ID), Name: name, Owner: c.Labels[SandboxTaskLabel], State: c.State,
			Created: time.Unix(c.Created, 0),
		})
	}
	return out, nil
}

// Never set Entrypoint: the image's init (tini) must stay PID 1 to reap what `docker exec` commands
// leave behind.
func sandboxContainerConfig(image string, labels map[string]string, exposed nat.PortSet, env []string, rootless bool) *container.Config {
	cfg := &container.Config{
		Image:        image,
		Cmd:          []string{"sleep", "infinity"},
		Labels:       labels,
		ExposedPorts: exposed,
		Env:          env,
		Tty:          false,
	}
	if rootless {
		cfg.User = "1000:1000"
	}
	return cfg
}

// No workspace id means no cache: a cache is never shared between workspaces.
func (d *Docker) cacheWiring(opts CreateOpts) ([]mount.Mount, []string) {
	if !d.cache || opts.WorkspaceID == "" {
		return nil, nil
	}
	return dockerCacheMounts(opts.WorkspaceID, opts.SnapshotsWritable), cacheEnvList()
}

func (d *Docker) sandboxHostConfig(
	mounts []mount.Mount, bindings nat.PortMap, netName string, rootless bool, memoryBytes int64,
) *container.HostConfig {
	host := &container.HostConfig{
		Mounts:        mounts,
		PortBindings:  bindings,
		AutoRemove:    false,
		RestartPolicy: container.RestartPolicy{Name: "no"},
		NetworkMode:   container.NetworkMode(netName),
		ExtraHosts:    agentnet.ExtraHosts(),
		SecurityOpt:   []string{"no-new-privileges:true"}, //i18n:ignore Docker security option
		CapDrop:       []string{"ALL"},
	}
	if !rootless {
		host.CapAdd = append([]string(nil), sandboxRootCaps...)
	}
	// Swap pinned to the memory cap so a runaway build cannot bleed into host swap.
	if memoryBytes >= 256*1024*1024 {
		host.Memory = memoryBytes
		host.MemorySwap = memoryBytes
	}
	if d.limits.pids > 0 {
		pids := d.limits.pids
		host.PidsLimit = &pids
	}
	host.NanoCPUs = d.limits.nanoCPUs
	// A sized tmpfs: copies of the repo, logs and caches written to /tmp end at the cap instead of
	// filling the Docker host's disk. Mode 1777 and exec keep builds and the rootless user working.
	if size := d.tmpfsSize(memoryBytes); size != "" {
		//i18n:ignore Docker tmpfs mount options
		host.Tmpfs = map[string]string{"/tmp": "rw,exec,nosuid,nodev,mode=1777,size=" + size}
	}
	return host
}

// Node reads NODE_EXTRA_CA_CERTS only at startup; other tools get the CA from the profile hook
// every `bash -lc` exec sources.
func sandboxEnv(extra []string, ca bool) []string {
	env := withSandboxDefaults(extra)
	have := map[string]bool{}
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			have[kv[:i]] = true
		}
	}
	add := proxyenv.List(os.Getenv)
	if ca {
		add = append(add, "NODE_EXTRA_CA_CERTS="+catrust.ContainerPath)
	}
	for _, kv := range add {
		if k := kv[:strings.IndexByte(kv, '=')]; !have[k] {
			env = append(env, kv)
		}
	}
	return env
}

func (d *Docker) copyCABundle(ctx context.Context, containerID string, ca []byte) error {
	archive, err := catrust.Archive(ca)
	if err != nil {
		return apperr.Wrap(err, "sandbox.errors.extra_ca_bundle")
	}
	if err := d.cli.CopyToContainer(ctx, containerID, "/", archive, container.CopyToContainerOptions{}); err != nil {
		return apperr.Wrap(err, "sandbox.errors.copy_extra_ca_bundle")
	}
	return nil
}

func (d *Docker) PeerAddress(ctx context.Context, containerID string, port int) (string, bool) {
	netName := agentnet.Name()
	if netName == "" {
		return "", false
	}
	info, err := d.cli.ContainerInspect(ctx, containerID)
	if err != nil || info.ContainerJSONBase == nil || info.NetworkSettings == nil {
		return "", false
	}
	if _, on := info.NetworkSettings.Networks[netName]; !on {
		return "", false
	}
	logx.Warn(agentnet.ConnectPeers(ctx, d.cli, netName), "sandbox.log.connect_peers", "network", netName)
	return fmt.Sprintf("http://%s:%d", strings.TrimPrefix(info.Name, "/"), port), true
}

// 4 GB old-space (V8 default ~2 GB) so Angular/webpack/vite/next builds don't die with "JavaScript
// heap out of memory" inside the 6 GB cgroup.
var sandboxDefaultEnv = []string{
	"NODE_OPTIONS=--max-old-space-size=4096",
}

func withSandboxDefaults(extra []string) []string {
	if len(sandboxDefaultEnv) == 0 {
		return extra
	}
	have := make(map[string]struct{}, len(extra))
	for _, kv := range extra {
		if eq := strings.IndexByte(kv, '='); eq > 0 {
			have[kv[:eq]] = struct{}{}
		}
	}
	out := make([]string, 0, len(extra)+len(sandboxDefaultEnv))
	out = append(out, extra...)
	for _, kv := range sandboxDefaultEnv {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		if _, set := have[kv[:eq]]; set {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func (d *Docker) Remove(ctx context.Context, containerID string) error {
	err := d.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{
		Force:         true,
		RemoveVolumes: true,
	})
	if err != nil && !client.IsErrNotFound(err) {
		return err
	}
	return nil
}

func (d *Docker) Alive(ctx context.Context, containerID string) bool {
	info, err := d.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return false
	}
	return info.State != nil && info.State.Running
}

type ExecOpts struct {
	Command   string
	Cwd       string
	Env       map[string]string
	TimeoutMs int
	// Bulk data (a prewarm's source archive) goes through Stdin to avoid the argument length limit.
	Stdin string
	// Written by Manager.Exec before the command; the runtimes never see them.
	Files []ExecFile
}

// JSON tags must match the schema documented in the MCP exec tool description.
type ExecResult struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out"`
	TimeoutMs  int    `json:"timeout_ms,omitempty"`
}

func (d *Docker) Exec(ctx context.Context, containerID string, opts ExecOpts) (*ExecResult, error) {
	if strings.TrimSpace(opts.Command) == "" {
		return nil, apperr.Invalid("sandbox.errors.exec_command_required")
	}
	cwd := opts.Cwd
	if cwd == "" {
		cwd = "/workspace"
	}

	var wrapped string
	if opts.TimeoutMs > 0 {
		secs := float64(opts.TimeoutMs) / 1000.0
		wrapped = fmt.Sprintf(
			"timeout --kill-after=2 --signal=TERM %.3f bash -lc %s",
			secs, shellQuote(opts.Command),
		)
	} else {
		wrapped = fmt.Sprintf("bash -lc %s", shellQuote(opts.Command))
	}

	envSlice := make([]string, 0, len(opts.Env))
	for k, v := range opts.Env {
		envSlice = append(envSlice, k+"="+v)
	}

	createResp, err := d.cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          []string{"bash", "-lc", wrapped},
		WorkingDir:   cwd,
		Env:          envSlice,
		AttachStdin:  opts.Stdin != "",
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
	})
	if err != nil {
		return nil, apperr.Wrap(err, "sandbox.errors.exec_create")
	}

	start := time.Now()
	attach, err := d.cli.ContainerExecAttach(ctx, createResp.ID, container.ExecStartOptions{
		Tty: false,
	})
	if err != nil {
		return nil, apperr.Wrap(err, "sandbox.errors.exec_attach")
	}
	defer attach.Close()
	// Stdin is written concurrently with reading output so neither side blocks; CloseWrite gives
	// the command EOF.
	stdinDone := make(chan error, 1)
	if opts.Stdin != "" {
		go func() {
			_, werr := io.Copy(attach.Conn, strings.NewReader(opts.Stdin))
			if cerr := attach.CloseWrite(); werr == nil {
				werr = cerr
			}
			stdinDone <- werr
		}()
	} else {
		stdinDone <- nil
	}

	var stdout, stderr capBuffer
	stdout.cap = ExecOutputCap
	stderr.cap = ExecOutputCap
	if _, copyErr := stdcopy.StdCopy(&stdout, &stderr, attach.Reader); copyErr != nil && !errors.Is(copyErr, io.EOF) {
		return nil, apperr.Wrap(copyErr, "sandbox.errors.read_exec_stream")
	}
	if stdinErr := <-stdinDone; stdinErr != nil {
		return nil, apperr.Wrap(stdinErr, "sandbox.errors.exec_stdin")
	}

	inspect, err := d.cli.ContainerExecInspect(ctx, createResp.ID)
	if err != nil {
		return nil, apperr.Wrap(err, "sandbox.errors.exec_inspect")
	}

	exitCode := inspect.ExitCode
	timedOut := opts.TimeoutMs > 0 && (exitCode == 124 || exitCode == 137)

	return &ExecResult{
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		ExitCode:   exitCode,
		DurationMs: time.Since(start).Milliseconds(),
		TimedOut:   timedOut,
	}, nil
}

func (d *Docker) inspectPortMap(ctx context.Context, containerID string) (map[int]int, error) {
	info, err := d.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, err
	}
	out := map[int]int{}
	if info.NetworkSettings == nil {
		return out, nil
	}
	for portKey, bindings := range info.NetworkSettings.Ports {
		parts := strings.SplitN(string(portKey), "/", 2)
		containerPort, err := strconv.Atoi(parts[0])
		if err != nil || len(bindings) == 0 {
			continue
		}
		hostPort, err := strconv.Atoi(bindings[0].HostPort)
		if err != nil {
			continue
		}
		out[containerPort] = hostPort
	}
	return out, nil
}

type capBuffer struct {
	buf     bytes.Buffer
	cap     int
	dropped int
	stopped bool
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if c.stopped {
		c.dropped += len(p)
		return len(p), nil
	}
	remain := c.cap - c.buf.Len()
	if remain <= 0 {
		c.stopped = true
		c.dropped += len(p)
		return len(p), nil
	}
	if len(p) > remain {
		c.buf.Write(p[:remain])
		c.dropped += len(p) - remain
		c.stopped = true
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *capBuffer) String() string {
	if c.dropped == 0 {
		return c.buf.String()
	}
	return c.buf.String() + i18n.Text("sandbox.output.truncated", "bytes", c.dropped)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func sanitizeTaskID(id string) string {
	if id == "" {
		return "unknown"
	}
	out := make([]byte, 0, len(id))
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '_', c == '.', c == '-':
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func buildSandboxMounts(opts CreateOpts) []mount.Mount {
	mounts := make([]mount.Mount, 0, 1+len(opts.ProjectsPaths))
	if opts.DataVolume != "" {
		if opts.WorkspacePath != "" {
			if sub, ok := relSubpath(opts.DataRoot, opts.WorkspacePath); ok {
				mounts = append(mounts, mount.Mount{
					Type:          mount.TypeVolume,
					Source:        opts.DataVolume,
					Target:        "/workspace",
					VolumeOptions: &mount.VolumeOptions{Subpath: sub},
				})
			}
		}
		for name, path := range opts.ProjectsPaths {
			if path == "" {
				continue
			}
			sub, ok := relSubpath(opts.DataRoot, path)
			if !ok {
				continue
			}
			mounts = append(mounts, mount.Mount{
				Type:          mount.TypeVolume,
				Source:        opts.DataVolume,
				Target:        "/workspace/projects/" + name,
				VolumeOptions: &mount.VolumeOptions{Subpath: sub},
			})
		}
		return mounts
	}
	if opts.WorkspacePath != "" {
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: opts.WorkspacePath,
			Target: "/workspace",
		})
	}
	for name, path := range opts.ProjectsPaths {
		if path == "" {
			continue
		}
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: path,
			Target: "/workspace/projects/" + name,
		})
	}
	return mounts
}

// Shared with the agent and chat launchers; see types/worktreegit.go for why the mount is
// read-only.
func sandboxWorktreeGit(opts CreateOpts) ([]mount.Mount, []string) {
	extra, names := types.WorktreeGitMounts("sandbox", opts.ProjectsPaths, opts.DataVolume != "", opts.DataRoot)
	mounts := make([]mount.Mount, 0, len(extra))
	for _, em := range extra {
		mounts = append(mounts, em.DockerMount(opts.DataVolume))
	}
	lookup := func(key string) (string, bool) {
		for _, kv := range opts.ExtraEnv {
			if k, v, ok := strings.Cut(kv, "="); ok && k == key {
				return v, true
			}
		}
		return "", false
	}
	vars := types.WorktreeGitEnv(lookup, types.ProjectMountTargets(names))
	env := make([]string, 0, len(vars))
	for k, v := range vars {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	return mounts, env
}

func relSubpath(root, abs string) (string, bool) {
	if root == "" || abs == "" {
		return "", false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return rel, true
}

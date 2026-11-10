package mcp

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/agentnet"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/dockerx"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"gopkg.in/yaml.v3"
)

type Service struct {
	Name           string `json:"name"`
	Image          string `json:"image"`
	ContainerName  string `json:"container_name"`
	HostPort       int    `json:"host_port,omitempty"`
	ContainerPort  int    `json:"container_port,omitempty"`
	Status         string `json:"status"`
	State          string `json:"state,omitempty"`
	Health         string `json:"health,omitempty"`
	ContainerID    string `json:"container_id,omitempty"`
	StartedAt      string `json:"started_at,omitempty"`
	RestartCount   int    `json:"restart_count,omitempty"`
	ImageAvailable bool   `json:"image_available"`

	// The frontend treats an empty Source as "runtime".
	Source             string `json:"source,omitempty"`
	URL                string `json:"url,omitempty"`
	Description        string `json:"description,omitempty"`
	ForwardsCredential bool   `json:"forwards_credential,omitempty"`
	Transport          string `json:"transport,omitempty"`
	Auth               string `json:"auth,omitempty"`
	CredentialRef      string `json:"credential_ref,omitempty"`
	HeaderTemplate     string `json:"header_template,omitempty"`
	HealthDetail       string `json:"health_detail,omitempty"`
}

type serviceSpec struct {
	Image         string   `yaml:"image"`
	ContainerName string   `yaml:"container_name"`
	Restart       string   `yaml:"restart"`
	Ports         []string `yaml:"ports"`
	ExtraHosts    []string `yaml:"extra_hosts"`
	DNS           []string `yaml:"dns"`
	Environment   []string `yaml:"environment"`
	Volumes       []string `yaml:"volumes"`
}

type composeFile struct {
	Services map[string]serviceSpec `yaml:"services"`
}

type Manager struct {
	cli         *client.Client
	composePath string
	composeDir  string

	mountsMu   sync.Mutex
	selfMounts []dockertypes.MountPoint
	mountsOK   bool
}

func NewManager(cli *client.Client, composePath string) *Manager {
	return &Manager{
		cli:         cli,
		composePath: composePath,
		composeDir:  filepath.Dir(composePath),
	}
}

func (m *Manager) ComposePath() string { return m.composePath }

func (m *Manager) loadCompose() (map[string]serviceSpec, []string, error) {
	data, err := os.ReadFile(m.composePath)
	// Release installs (`thulu init`) have no tools/ directory; a missing compose file means no
	// MCPs, not an error.
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, apperr.Wrap(err, "mcp.errors.read_compose", "path", m.composePath)
	}
	var cf composeFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return nil, nil, apperr.Wrap(err, "mcp.errors.parse_compose", "path", m.composePath)
	}
	names := make([]string, 0, len(cf.Services))
	for n := range cf.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	return cf.Services, names, nil
}

func (m *Manager) List(ctx context.Context) ([]Service, error) {
	services, names, err := m.loadCompose()
	if err != nil {
		return nil, err
	}
	out := make([]Service, 0, len(names))
	for _, name := range names {
		spec := services[name]
		out = append(out, m.toService(ctx, name, spec))
	}
	return out, nil
}

func (m *Manager) toService(ctx context.Context, name string, spec serviceSpec) Service {
	cname := spec.ContainerName
	if cname == "" {
		cname = name
	}
	host, ctn := firstPort(spec.Ports)
	sv := Service{
		Name:          name,
		Image:         spec.Image,
		ContainerName: cname,
		HostPort:      host,
		ContainerPort: ctn,
		Status:        "missing",
	}
	if spec.Image != "" {
		sv.ImageAvailable = m.imageExists(ctx, spec.Image)
	}
	insp, err := m.cli.ContainerInspect(ctx, cname)
	if err != nil {
		if client.IsErrNotFound(err) {
			return sv
		}
		sv.Status = "unknown"
		return sv
	}
	sv.ContainerID = insp.ID
	sv.State = insp.State.Status
	sv.Status = mapState(insp.State)
	sv.StartedAt = insp.State.StartedAt
	sv.RestartCount = insp.RestartCount
	if insp.State.Health != nil {
		sv.Health = insp.State.Health.Status
	}
	return sv
}

func mapState(st *dockertypes.ContainerState) string {
	switch {
	case st == nil:
		return "unknown"
	case st.Running:
		return "running"
	case st.Restarting:
		return "restarting"
	case st.Paused:
		return "paused"
	case st.Dead:
		return "dead"
	case st.Status != "":
		return st.Status
	default:
		return "unknown"
	}
}

func (m *Manager) imageExists(ctx context.Context, ref string) bool {
	if !strings.Contains(ref, ":") {
		ref += ":latest"
	}
	imgs, err := m.cli.ImageList(ctx, image.ListOptions{
		Filters: filters.NewArgs(filters.Arg("reference", ref)),
	})
	if err != nil {
		return false
	}
	return len(imgs) > 0
}

func firstPort(ports []string) (host, ctn int) {
	for _, p := range ports {
		_, hostPort, ctnPort, ok := splitPortMapping(p)
		if !ok {
			continue
		}
		h, err1 := strconv.Atoi(hostPort)
		c, err2 := strconv.Atoi(strings.SplitN(ctnPort, "/", 2)[0])
		if err1 != nil || err2 != nil {
			continue
		}
		return h, c
	}
	return 0, 0
}

func (m *Manager) resolveService(ctx context.Context, name string) (serviceSpec, string, *dockertypes.ContainerJSON, error) {
	services, _, err := m.loadCompose()
	if err != nil {
		return serviceSpec{}, "", nil, err
	}
	spec, ok := services[name]
	if !ok {
		return serviceSpec{}, "", nil, apperr.NotFound("mcp.errors.service_not_in_compose", "name", name)
	}
	cname := spec.ContainerName
	if cname == "" {
		cname = name
	}
	insp, err := m.cli.ContainerInspect(ctx, cname)
	if err != nil {
		if client.IsErrNotFound(err) {
			return spec, cname, nil, nil
		}
		return spec, cname, nil, apperr.Wrap(err, "mcp.errors.inspect_container", "container", cname)
	}
	return spec, cname, &insp, nil
}

func (m *Manager) Start(ctx context.Context, name string) error {
	spec, cname, insp, err := m.resolveService(ctx, name)
	if err != nil {
		return err
	}
	switch {
	case insp == nil:
		if !m.imageExists(ctx, spec.Image) {
			return apperr.Precondition("mcp.errors.image_not_built", "image", spec.Image, "compose", m.composePath, "service", name)
		}
		if err := m.createContainer(ctx, name, spec, cname); err != nil {
			return err
		}
	case insp.State.Running:
		return nil
	}
	if err := m.cli.ContainerStart(ctx, cname, container.StartOptions{}); err != nil {
		return apperr.Wrap(err, "mcp.errors.start_container", "container", cname)
	}
	return nil
}

func (m *Manager) Stop(ctx context.Context, name string) error {
	_, cname, insp, err := m.resolveService(ctx, name)
	if err != nil {
		return err
	}
	if insp == nil || !insp.State.Running {
		return nil
	}
	timeout := 10
	return m.cli.ContainerStop(ctx, cname, container.StopOptions{Timeout: &timeout})
}

func (m *Manager) Restart(ctx context.Context, name string) error {
	_, cname, insp, err := m.resolveService(ctx, name)
	if err != nil {
		return err
	}
	if insp == nil {
		return m.Start(ctx, name)
	}
	timeout := 10
	return m.cli.ContainerRestart(ctx, cname, container.StopOptions{Timeout: &timeout})
}

func (m *Manager) createContainer(ctx context.Context, svcName string, spec serviceSpec, cname string) error {
	if spec.Image == "" {
		return apperr.Invalid("mcp.errors.service_has_no_image", "service", svcName)
	}

	env := expandEnvList(spec.Environment)
	exposed, bindings, err := portConfig(spec.Ports)
	if err != nil {
		return apperr.Wrap(err, "mcp.errors.service_ports", "service", svcName)
	}
	mounts, err := m.resolveMounts(ctx, spec.Volumes)
	if err != nil {
		return apperr.Wrap(err, "mcp.errors.service_volumes", "service", svcName)
	}

	cfg := &container.Config{
		Image:        spec.Image,
		Env:          env,
		ExposedPorts: exposed,
		Labels: map[string]string{
			"com.docker.compose.project": "tools",
			"com.docker.compose.service": svcName,
			"octopus.mcp":                "true",
			"octopus.mcp.service":        svcName,
		},
	}
	dockerx.StampInstall(cfg.Labels)
	hostCfg := &container.HostConfig{
		PortBindings:  bindings,
		Mounts:        mounts,
		ExtraHosts:    spec.ExtraHosts,
		DNS:           spec.DNS,
		RestartPolicy: parseRestart(spec.Restart),
		LogConfig:     dockerx.ContainerLogConfig(ctx, m.cli),
	}
	netCfg := agentNetworkConfig(svcName)

	resp, err := m.cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, cname)
	if err != nil {
		return apperr.Wrap(err, "mcp.errors.create_container", "container", cname)
	}
	_ = resp
	return nil
}

func splitPortMapping(raw string) (ip, host, ctn string, ok bool) {
	raw = strings.Trim(strings.TrimSpace(raw), "\"'")
	parts := strings.Split(raw, ":")
	switch len(parts) {
	case 2:
		return "", strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
	case 3:
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2]), true
	}
	return "", "", "", false
}

func portConfig(ports []string) (nat.PortSet, nat.PortMap, error) {
	exposed := nat.PortSet{}
	bindings := nat.PortMap{}
	for _, raw := range ports {
		if strings.Trim(strings.TrimSpace(raw), "\"'") == "" {
			continue
		}
		hostIP, host, ctnPort, ok := splitPortMapping(raw)
		if !ok {
			return nil, nil, apperr.Invalid("mcp.errors.invalid_port_mapping", "mapping", raw)
		}
		// No address means every interface, as in compose; never widen a loopback mapping.
		if hostIP == "" {
			hostIP = "0.0.0.0"
		}
		proto := "tcp"
		if strings.Contains(ctnPort, "/") {
			p := strings.SplitN(ctnPort, "/", 2)
			ctnPort, proto = p[0], p[1]
		}
		p, err := nat.NewPort(proto, ctnPort)
		if err != nil {
			return nil, nil, apperr.WrapKind(apperr.KindInvalid, err, "mcp.errors.invalid_container_port", "port", ctnPort)
		}
		exposed[p] = struct{}{}
		bindings[p] = append(bindings[p], nat.PortBinding{HostIP: hostIP, HostPort: host})
	}
	return exposed, bindings, nil
}

// Agents dial http://<service>:3000/sse; on the default bridge that name resolves to nothing.
func agentNetworkConfig(svcName string) *network.NetworkingConfig {
	name := agentnet.Name()
	if name == "" {
		return &network.NetworkingConfig{}
	}
	return &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
		name: {Aliases: []string{svcName}},
	}}
}

func parseRestart(policy string) container.RestartPolicy {
	switch strings.TrimSpace(policy) {
	case "no", "":
		return container.RestartPolicy{Name: "no"}
	case "always":
		return container.RestartPolicy{Name: "always"}
	case "on-failure":
		return container.RestartPolicy{Name: "on-failure"}
	case "unless-stopped":
		return container.RestartPolicy{Name: "unless-stopped"}
	default:
		return container.RestartPolicy{Name: "no"}
	}
}

// When the daemon runs in a container, src is an in-container path the host Docker engine cannot
// see; toHostPath translates it.
func (m *Manager) resolveMounts(ctx context.Context, vols []string) ([]mount.Mount, error) {
	out := make([]mount.Mount, 0, len(vols))
	for _, v := range vols {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		v = expandEnv(v)
		parts := strings.SplitN(v, ":", 3)
		if len(parts) < 2 {
			return nil, apperr.Invalid("mcp.errors.invalid_volume", "volume", v)
		}
		src, dst := parts[0], parts[1]
		ro := len(parts) == 3 && strings.Contains(parts[2], "ro")
		if !filepath.IsAbs(src) {
			src = filepath.Join(m.composeDir, src)
			src = filepath.Clean(src)
		}
		hostSrc := m.toHostPath(ctx, src)
		out = append(out, mount.Mount{
			Type:     mount.TypeBind,
			Source:   hostSrc,
			Target:   dst,
			ReadOnly: ro,
		})
	}
	return out, nil
}

func (m *Manager) toHostPath(ctx context.Context, inContainerPath string) string {
	mounts, err := m.loadSelfMounts(ctx)
	if err != nil || len(mounts) == 0 {
		return inContainerPath
	}
	var best *dockertypes.MountPoint
	for i := range mounts {
		mp := &mounts[i]
		if mp.Destination == "" {
			continue
		}
		if inContainerPath != mp.Destination &&
			!strings.HasPrefix(inContainerPath, mp.Destination+"/") {
			continue
		}
		if best == nil || len(mp.Destination) > len(best.Destination) {
			best = mp
		}
	}
	if best == nil {
		return inContainerPath
	}
	rel, err := filepath.Rel(best.Destination, inContainerPath)
	if err != nil {
		return inContainerPath
	}
	return filepath.Join(best.Source, rel)
}

func (m *Manager) loadSelfMounts(ctx context.Context) ([]dockertypes.MountPoint, error) {
	m.mountsMu.Lock()
	defer m.mountsMu.Unlock()
	if m.mountsOK {
		return m.selfMounts, nil
	}
	m.mountsOK = true

	data, err := os.ReadFile("/etc/hostname")
	if err != nil {
		return nil, nil
	}
	cid := strings.TrimSpace(string(data))
	if cid == "" {
		return nil, nil
	}
	insp, err := m.cli.ContainerInspect(ctx, cid)
	if err != nil {
		return nil, nil
	}
	m.selfMounts = insp.Mounts
	return m.selfMounts, nil
}

func expandEnvList(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = expandEnv(s)
	}
	return out
}

func expandEnv(s string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		j := strings.Index(s[i+2:], "}")
		if j < 0 {
			b.WriteString(s[i:])
			return b.String()
		}
		token := s[i+2 : i+2+j]
		s = s[i+2+j+1:]
		key := token
		def := ""
		if idx := strings.Index(token, ":-"); idx >= 0 {
			key = token[:idx]
			def = token[idx+2:]
		}
		val, ok := os.LookupEnv(key)
		if !ok || val == "" {
			val = def
		}
		b.WriteString(val)
	}
}

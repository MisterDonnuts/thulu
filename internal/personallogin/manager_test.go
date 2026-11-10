package personallogin

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/engine/claudecode"
	"github.com/MisterDonnuts/thulu/internal/enginecache"
)

type fakeDocker struct {
	mu       sync.Mutex
	volumes  map[string]volume.Volume
	created  []*container.Config
	hosts    []*container.HostConfig
	removed  []string
	codes    []string
	loggedIn bool
	next     int
	ctrs     map[string]*fakeCtr
}

type fakeCtr struct {
	cfg  *container.Config
	cli  net.Conn
	exit chan container.WaitResponse
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{volumes: map[string]volume.Volume{}, ctrs: map[string]*fakeCtr{}}
}

func (d *fakeDocker) ImageInspectWithRaw(context.Context, string) (dockertypes.ImageInspect, []byte, error) {
	return dockertypes.ImageInspect{}, nil, nil
}

func (d *fakeDocker) ImagePull(context.Context, string, image.PullOptions) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (d *fakeDocker) VolumeCreate(_ context.Context, o volume.CreateOptions) (volume.Volume, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if v, ok := d.volumes[o.Name]; ok {
		return v, nil
	}
	v := volume.Volume{Name: o.Name, Labels: o.Labels}
	d.volumes[o.Name] = v
	return v, nil
}

func (d *fakeDocker) VolumeInspect(_ context.Context, name string) (volume.Volume, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	v, ok := d.volumes[name]
	if !ok {
		return volume.Volume{}, errdefs.NotFound(io.EOF)
	}
	return v, nil
}

func (d *fakeDocker) VolumeRemove(_ context.Context, name string, _ bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.volumes, name)
	return nil
}

func (d *fakeDocker) ContainerCreate(_ context.Context, cfg *container.Config, host *container.HostConfig,
	_ *network.NetworkingConfig, _ *ocispec.Platform, _ string,
) (container.CreateResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.next++
	id := "ctr-" + string(rune('0'+d.next))
	d.created = append(d.created, cfg)
	d.hosts = append(d.hosts, host)
	d.ctrs[id] = &fakeCtr{cfg: cfg, exit: make(chan container.WaitResponse, 1)}
	return container.CreateResponse{ID: id}, nil
}

func (d *fakeDocker) ContainerAttach(_ context.Context, id string, _ container.AttachOptions) (dockertypes.HijackedResponse, error) {
	d.mu.Lock()
	c := d.ctrs[id]
	loggedIn := d.loggedIn
	d.mu.Unlock()
	daemonSide, ctrSide := net.Pipe()
	c.cli = ctrSide
	if c.cfg.Tty {
		return dockertypes.HijackedResponse{Conn: daemonSide, Reader: bufio.NewReader(daemonSide)}, nil
	}
	var framed bytes.Buffer
	if slices.Contains(c.cfg.Cmd, "status") {
		body := `{"loggedIn":false,"authMethod":"none"}`
		if loggedIn {
			body = `{"loggedIn":true,"authMethod":"claude.ai","email":"ada@example.com","orgName":"Ada"}`
		}
		_, _ = stdcopy.NewStdWriter(&framed, stdcopy.Stdout).Write([]byte(body))
	}
	return dockertypes.HijackedResponse{Conn: daemonSide, Reader: bufio.NewReader(&framed)}, nil
}

func (d *fakeDocker) ContainerStart(_ context.Context, id string, _ container.StartOptions) error {
	d.mu.Lock()
	c := d.ctrs[id]
	d.mu.Unlock()
	if !c.cfg.Tty {
		c.exit <- container.WaitResponse{StatusCode: 0}
		return nil
	}
	go func() {
		_, _ = io.WriteString(c.cli, claudeLoginTerm)
		in := bufio.NewReader(c.cli)
		for {
			code, err := in.ReadString('\r')
			if err != nil {
				c.exit <- container.WaitResponse{StatusCode: 137}
				return
			}
			code = strings.TrimSuffix(code, "\r")
			d.mu.Lock()
			d.codes = append(d.codes, code)
			ok := code == "good"
			d.loggedIn = d.loggedIn || ok
			d.mu.Unlock()
			switch {
			case ok:
				c.exit <- container.WaitResponse{StatusCode: 0}
				return
			case code == "garbled":
				_, _ = io.WriteString(c.cli, "Invalid code. Please make sure the full code was copied.\r\n")
				continue
			}
			c.exit <- container.WaitResponse{StatusCode: 1}
			return
		}
	}()
	return nil
}

func (d *fakeDocker) ContainerWait(_ context.Context, id string, _ container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ctrs[id].exit, make(chan error)
}

func (d *fakeDocker) ContainerRemove(_ context.Context, id string, _ container.RemoveOptions) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.removed = append(d.removed, id)
	if c := d.ctrs[id]; c != nil && c.cli != nil {
		_ = c.cli.Close()
	}
	return nil
}

func newTestManager(d *fakeDocker) *Manager {
	return NewManager(Config{Docker: d, Image: func(string) (string, string) { return "octopus-agent:test", "" }})
}

func TestManager_LoginRelaysTheCodeAndKeepsTheVolume(t *testing.T) {
	d := newFakeDocker()
	m := newTestManager(d)
	ctx := context.Background()

	url, err := m.Start(ctx, "ada", claudecode.Name)
	if err != nil || url != claudeURL {
		t.Fatalf("Start = %q, %v", url, err)
	}
	vol := VolumeName(claudecode.Name, "ada")
	login, host := d.created[0], d.hosts[0]
	if !login.Tty || !login.OpenStdin || host.LogConfig.Type != "none" {
		t.Errorf("login container tty=%v stdin=%v log=%q: it needs a terminal and must keep no log",
			login.Tty, login.OpenStdin, host.LogConfig.Type)
	}
	if len(host.Mounts) != 2 || host.Mounts[0].Source != vol || host.Mounts[0].Target != claudecode.LoginConfigDir {
		t.Errorf("mounts = %+v", host.Mounts)
	}
	// The CLI installs itself on first use into the person's own engine volume, never a workspace's.
	if m := host.Mounts[1]; m.Source != enginecache.VolumeName(enginecache.UserScope("ada")) || m.Target != enginecache.MountPath {
		t.Errorf("engine mount = %+v", m)
	}
	if !slices.Contains(login.Env, enginecache.EnvDir+"="+enginecache.MountPath) {
		t.Errorf("env = %v, want %s", login.Env, enginecache.EnvDir)
	}
	if !slices.Contains(login.Env, "CLAUDE_CONFIG_DIR="+claudecode.LoginConfigDir) {
		t.Errorf("env = %v", login.Env)
	}

	st, err := m.Complete(ctx, "ada", claudecode.Name, "  good\n")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !st.Connected || st.Email != "ada@example.com" || st.Org != "Ada" {
		t.Errorf("status = %+v", st)
	}
	if !slices.Equal(d.codes, []string{"good"}) {
		t.Errorf("the CLI read %q", d.codes)
	}
	if _, ok := m.Volume(ctx, "ada", claudecode.Name); !ok {
		t.Error("a completed login left no volume for runs")
	}
	if _, ok := m.Volume(ctx, "bob", claudecode.Name); ok {
		t.Error("bob has a login he never made")
	}
}

func TestManager_FailedFirstLoginRemovesTheVolume(t *testing.T) {
	d := newFakeDocker()
	m := newTestManager(d)
	ctx := context.Background()
	if _, err := m.Start(ctx, "ada", claudecode.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Complete(ctx, "ada", claudecode.Name, "bad"); err == nil {
		t.Fatal("a refused code was reported as signed in")
	}
	if _, ok := m.Volume(ctx, "ada", claudecode.Name); ok {
		t.Error("the failed login's volume is still there")
	}
	if _, err := m.Complete(ctx, "ada", claudecode.Name, "good"); err == nil {
		t.Error("a code was accepted with no login waiting for it")
	}
}

func TestManager_GarbledPasteKeepsTheLoginOpen(t *testing.T) {
	old := promptGrace
	promptGrace = 50 * time.Millisecond
	t.Cleanup(func() { promptGrace = old })
	d := newFakeDocker()
	m := newTestManager(d)
	ctx := context.Background()
	if _, err := m.Start(ctx, "ada", claudecode.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Complete(ctx, "ada", claudecode.Name, "garbled"); err == nil {
		t.Fatal("a paste the CLI asked again for was reported as signed in")
	}
	st, err := m.Complete(ctx, "ada", claudecode.Name, "good")
	if err != nil || !st.Connected {
		t.Fatalf("second paste = %+v, %v", st, err)
	}
	if !slices.Equal(d.codes, []string{"garbled", "good"}) {
		t.Errorf("the CLI read %q", d.codes)
	}
}

func TestManager_RejectsControlCharacters(t *testing.T) {
	d := newFakeDocker()
	m := newTestManager(d)
	if _, err := m.Start(context.Background(), "ada", claudecode.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Complete(context.Background(), "ada", claudecode.Name, "go\rod"); err == nil {
		t.Error("a code with a carriage return in the middle was accepted")
	}
	m.Cancel(context.Background(), "ada", claudecode.Name)
	if len(d.codes) != 0 {
		t.Errorf("the CLI read %q", d.codes)
	}
}

func TestManager_DisconnectAndForeignVolumes(t *testing.T) {
	d := newFakeDocker()
	m := newTestManager(d)
	ctx := context.Background()
	if _, err := m.Start(ctx, "ada", claudecode.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Complete(ctx, "ada", claudecode.Name, "good"); err != nil {
		t.Fatal(err)
	}
	if err := m.Disconnect(ctx, "ada", claudecode.Name); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Volume(ctx, "ada", claudecode.Name); ok {
		t.Error("disconnect left the volume")
	}
	st, err := m.Status(ctx, "ada", claudecode.Name)
	if err != nil || st.Connected {
		t.Errorf("status after disconnect = %+v, %v", st, err)
	}

	name := VolumeName(claudecode.Name, "eve")
	d.volumes[name] = volume.Volume{Name: name, Labels: map[string]string{VolumeLabel: claudecode.Name}}
	if _, ok := m.Volume(ctx, "eve", claudecode.Name); ok {
		t.Error("a volume without the user label counted as eve's login")
	}
}

func TestManager_OnlyACompletedLoginIsConnected(t *testing.T) {
	d := newFakeDocker()
	m := newTestManager(d)
	ctx := context.Background()
	if _, err := m.Start(ctx, "ada", claudecode.Name); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Volume(ctx, "ada", claudecode.Name); ok {
		t.Error("a login waiting for its code counted as connected")
	}
	if _, err := m.Complete(ctx, "ada", claudecode.Name, "good"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Volume(ctx, "ada", claudecode.Name); !ok {
		t.Fatal("a completed login did not count")
	}

	if _, err := m.Start(ctx, "ada", claudecode.Name); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Volume(ctx, "ada", claudecode.Name); ok {
		t.Error("a volume with a login in progress counted as connected")
	}
	m.Cancel(ctx, "ada", claudecode.Name)
	if _, ok := m.Volume(ctx, "ada", claudecode.Name); !ok {
		t.Error("cancelling a second login lost the first")
	}

	d.mu.Lock()
	d.loggedIn = false
	d.mu.Unlock()
	if _, ok := NewManager(m.cfg).Volume(ctx, "ada", claudecode.Name); ok {
		t.Error("a volume the CLI says holds no login counted as connected")
	}
}

func TestManager_AnotherInstallsVolumeIsNotAdopted(t *testing.T) {
	dockerx.SetInstallID("a1b2c3d4e5f6")
	t.Cleanup(func() { dockerx.SetInstallID("") })
	if got := VolumeName(claudecode.Name, "ada"); got != "octopus-login-a1b2c3d4-claude-code-ada" {
		t.Errorf("VolumeName = %q", got)
	}

	d := newFakeDocker()
	m := newTestManager(d)
	ctx := context.Background()
	name := VolumeName(claudecode.Name, "ada")
	theirs := map[string]string{VolumeLabel: claudecode.Name, UserLabel: "ada", dockerx.InstallLabel: "ffffffff"}
	d.volumes[name] = volume.Volume{Name: name, Labels: theirs}
	if _, err := m.Start(ctx, "ada", claudecode.Name); err == nil {
		t.Fatal("a login started on another install's volume")
	}
	if v, ok := d.volumes[name]; !ok || v.Labels[dockerx.InstallLabel] != "ffffffff" {
		t.Error("the other install's volume was removed or changed")
	}
	if len(d.created) != 0 {
		t.Error("a login container ran against the other install's volume")
	}
}

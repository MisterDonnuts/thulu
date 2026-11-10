package personallogin

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/MisterDonnuts/thulu/internal/agentimage"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/enginecache"
	"github.com/MisterDonnuts/thulu/internal/imageref"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

type Docker interface {
	imageref.DockerImages
	VolumeCreate(ctx context.Context, options volume.CreateOptions) (volume.Volume, error)
	VolumeInspect(ctx context.Context, volumeID string) (volume.Volume, error)
	VolumeRemove(ctx context.Context, volumeID string, force bool) error
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig,
		networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	ContainerAttach(ctx context.Context, container string, options container.AttachOptions) (dockertypes.HijackedResponse, error)
	ContainerStart(ctx context.Context, container string, options container.StartOptions) error
	ContainerWait(ctx context.Context, containerID string, condition container.WaitCondition) (<-chan container.WaitResponse, <-chan error)
	ContainerRemove(ctx context.Context, container string, options container.RemoveOptions) error
}

const ContainerLabel = "octopus.personal-login.cmd"

const (
	imageUser = "node"
	imageHome = "/home/node"
)

const (
	urlWait     = 60 * time.Second
	codeWait    = 90 * time.Second
	commandWait = 60 * time.Second
	flowTTL     = 10 * time.Minute
	outputCap   = 64 << 10
)

var promptGrace = 10 * time.Second

type Config struct {
	Docker Docker
	Image  func(engineName string) (image, env string)
	Env    func(engineName string) map[string]string
}

type Manager struct {
	cfg   Config
	mu    sync.Mutex
	flows map[flowKey]*flow
	ready map[flowKey]bool
}

func NewManager(cfg Config) *Manager {
	return &Manager{cfg: cfg, flows: map[flowKey]*flow{}, ready: map[flowKey]bool{}}
}

type flowKey struct{ user, engine string }

type flow struct {
	containerID string
	hj          dockertypes.HijackedResponse
	term        *terminal
	done        chan struct{}
	exitCode    int64
	created     bool
	timer       *time.Timer
	once        sync.Once
}

type Status struct {
	Engine    string `json:"engine"`
	Connected bool   `json:"connected"`
	Pending   bool   `json:"pending"`
	engine.LoginStatus
}

// A volume alone is not a login: it exists before the code is pasted, or stays empty after a
// restart mid-login.
// A pending login reads as not connected, which also keeps runs off a volume the login container is
// writing.
func (m *Manager) Volume(ctx context.Context, userID, engineName string) (string, bool) {
	if m.pending(userID, engineName) {
		return "", false
	}
	name, ok, err := lookupVolume(ctx, m.cfg.Docker, engineName, userID)
	if err != nil || !ok {
		return "", false
	}
	key := flowKey{userID, engineName}
	m.mu.Lock()
	ready, known := m.ready[key]
	m.mu.Unlock()
	if !known {
		st, err := m.Status(ctx, userID, engineName)
		if err != nil {
			return "", false
		}
		ready = st.Connected
	}
	return name, ready
}

func (m *Manager) remember(key flowKey, ready bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ready[key] = ready
}

func (m *Manager) forget(key flowKey) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.ready, key)
}

func (m *Manager) Status(ctx context.Context, userID, engineName string) (Status, error) {
	pl, err := loginOf(engineName)
	if err != nil {
		return Status{}, err
	}
	st := Status{Engine: engineName, Pending: m.pending(userID, engineName)}
	vol, ok, err := lookupVolume(ctx, m.cfg.Docker, engineName, userID)
	if err != nil {
		return st, apperr.Wrap(err, "personallogin.errors.volume")
	}
	if !ok {
		m.forget(flowKey{userID, engineName})
	}
	if !ok || st.Pending {
		return st, nil
	}
	out, code, err := m.run(ctx, userID, engineName, pl, vol, pl.Status)
	if err != nil {
		return st, err
	}
	if code != 0 {
		return st, apperr.Unavailable("personallogin.errors.command_failed", "code", code)
	}
	ls, err := pl.ParseStatus(out)
	if err != nil {
		return st, err
	}
	st.LoginStatus = ls
	st.Connected = ls.LoggedIn
	m.remember(flowKey{userID, engineName}, st.Connected)
	return st, nil
}

func (m *Manager) Start(ctx context.Context, userID, engineName string) (string, error) {
	pl, err := loginOf(engineName)
	if err != nil {
		return "", err
	}
	m.Cancel(context.WithoutCancel(ctx), userID, engineName)
	m.forget(flowKey{userID, engineName})
	vol, existed, err := m.ensureVolume(ctx, engineName, userID)
	if err != nil {
		return "", err
	}
	f := &flow{done: make(chan struct{}), created: !existed}
	key := flowKey{userID, engineName}
	fail := func(err error) (string, error) {
		m.finish(context.WithoutCancel(ctx), key, f, vol, false)
		return "", err
	}

	id, err := m.create(ctx, userID, engineName, pl, vol, pl.Login, true)
	if err != nil {
		return fail(err)
	}
	f.containerID = id
	hj, err := m.cfg.Docker.ContainerAttach(ctx, id, container.AttachOptions{Stream: true, Stdin: true, Stdout: true, Stderr: true})
	if err != nil {
		return fail(apperr.Wrap(err, "personallogin.errors.container"))
	}
	f.hj = hj
	waitC, errC := m.cfg.Docker.ContainerWait(context.WithoutCancel(ctx), id, container.WaitConditionNextExit)
	if err := m.cfg.Docker.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		hj.Close()
		return fail(apperr.Wrap(err, "personallogin.errors.container"))
	}
	go func() {
		defer close(f.done)
		select {
		case r := <-waitC:
			f.exitCode = r.StatusCode
		case <-errC:
			f.exitCode = -1
		}
	}()

	term := newTerminal()
	f.term = term
	go term.read(hj.Reader)
	select {
	case url := <-term.found:
		f.timer = time.AfterFunc(flowTTL, func() { m.finish(context.Background(), key, f, vol, false) })
		m.mu.Lock()
		m.flows[key] = f
		m.mu.Unlock()
		return url, nil
	case <-f.done:
		return fail(apperr.Unavailable("personallogin.errors.no_url"))
	case <-time.After(urlWait):
		return fail(apperr.Unavailable("personallogin.errors.no_url"))
	case <-ctx.Done():
		return fail(ctx.Err())
	}
}

func (m *Manager) Complete(ctx context.Context, userID, engineName, code string) (Status, error) {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 4096 || strings.ContainsFunc(code, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return Status{}, apperr.Invalid("personallogin.errors.bad_code")
	}
	key := flowKey{userID, engineName}
	m.mu.Lock()
	f := m.flows[key]
	m.mu.Unlock()
	if f == nil {
		return Status{}, apperr.Conflict("personallogin.errors.no_pending_login")
	}
	vol := VolumeName(engineName, userID)
	f.term.reset()
	if _, err := io.WriteString(f.hj.Conn, code+"\r"); err != nil {
		m.finish(context.WithoutCancel(ctx), key, f, vol, false)
		return Status{}, apperr.Wrap(err, "personallogin.errors.login_failed")
	}
	// For a paste it cannot read, the CLI prints and keeps running instead of exiting; output plus
	// promptGrace means the code was refused.
	overall := time.NewTimer(codeWait)
	defer overall.Stop()
	var grace <-chan time.Time
wait:
	for {
		select {
		case <-f.done:
			break wait
		case <-f.term.said:
			grace = time.After(promptGrace)
		case <-grace:
			f.term.reset()
			return Status{}, apperr.Invalid("personallogin.errors.code_not_accepted")
		case <-overall.C:
			m.finish(context.WithoutCancel(ctx), key, f, vol, false)
			return Status{}, apperr.Unavailable("personallogin.errors.login_timeout")
		case <-ctx.Done():
			return Status{}, ctx.Err()
		}
	}
	ok := f.exitCode == 0
	m.finish(context.WithoutCancel(ctx), key, f, vol, ok)
	if !ok {
		return Status{}, apperr.Invalid("personallogin.errors.login_failed")
	}
	return m.Status(ctx, userID, engineName)
}

func (m *Manager) Cancel(ctx context.Context, userID, engineName string) {
	key := flowKey{userID, engineName}
	m.mu.Lock()
	f := m.flows[key]
	m.mu.Unlock()
	if f != nil {
		m.finish(ctx, key, f, VolumeName(engineName, userID), false)
	}
}

func (m *Manager) Disconnect(ctx context.Context, userID, engineName string) error {
	pl, err := loginOf(engineName)
	if err != nil {
		return err
	}
	m.Cancel(ctx, userID, engineName)
	m.forget(flowKey{userID, engineName})
	vol, ok, err := lookupVolume(ctx, m.cfg.Docker, engineName, userID)
	if err != nil {
		return apperr.Wrap(err, "personallogin.errors.volume")
	}
	if !ok {
		return nil
	}
	// Logout first so the account forgets this login; a failed logout still removes the volume.
	if _, code, err := m.run(ctx, userID, engineName, pl, vol, pl.Logout); err != nil || code != 0 {
		logx.Warn(commandErr(err, code), "personallogin.log.logout_failed", "engine", engineName)
	}
	if err := m.cfg.Docker.VolumeRemove(ctx, vol, false); err != nil {
		if errdefs.IsConflict(err) {
			return apperr.Conflict("personallogin.errors.volume_in_use")
		}
		return apperr.Wrap(err, "personallogin.errors.volume")
	}
	return nil
}

func (m *Manager) pending(userID, engineName string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.flows[flowKey{userID, engineName}] != nil
}

func (m *Manager) finish(ctx context.Context, key flowKey, f *flow, vol string, loggedIn bool) {
	f.once.Do(func() {
		m.mu.Lock()
		if m.flows[key] == f {
			delete(m.flows, key)
		}
		delete(m.ready, key)
		m.mu.Unlock()
		if f.timer != nil {
			f.timer.Stop()
		}
		if f.hj.Conn != nil {
			f.hj.Close()
		}
		if f.containerID != "" {
			logx.Warn(m.cfg.Docker.ContainerRemove(ctx, f.containerID, container.RemoveOptions{Force: true}),
				"personallogin.log.remove_container_failed", "id", f.containerID)
			if f.done != nil {
				select {
				case <-f.done:
				case <-time.After(commandWait):
				}
			}
		}
		if f.created && !loggedIn {
			logx.Warn(m.cfg.Docker.VolumeRemove(ctx, vol, true), "personallogin.log.remove_volume_failed", "volume", vol)
		}
	})
}

// Docker answers a create for an existing name with that volume; one not labeled as this install's
// login of this person is refused, never adopted or removed.
func (m *Manager) ensureVolume(ctx context.Context, engineName, userID string) (string, bool, error) {
	name, ok, err := lookupVolume(ctx, m.cfg.Docker, engineName, userID)
	if err != nil {
		return "", false, apperr.Wrap(err, "personallogin.errors.volume")
	}
	if ok {
		return name, true, nil
	}
	name = VolumeName(engineName, userID)
	v, err := m.cfg.Docker.VolumeCreate(ctx, volume.CreateOptions{Name: name, Labels: volumeLabels(engineName, userID)})
	if err != nil {
		return "", false, apperr.Wrap(err, "personallogin.errors.volume")
	}
	if !ownVolume(v, engineName, userID) {
		return "", false, apperr.Conflict("personallogin.errors.volume_taken", "volume", name)
	}
	return name, false, nil
}

// LogConfig none: the login's terminal shows the authorization URL, so Docker must keep no log.
func (m *Manager) create(
	ctx context.Context, userID, engineName string, pl *engine.PersonalLogin, vol string, argv []string, tty bool,
) (string, error) {
	img, imgEnv := "", agentimage.EnvOverride
	if m.cfg.Image != nil {
		img, imgEnv = m.cfg.Image(engineName)
		img = imageref.Resolve(img)
	}
	if img == "" {
		return "", apperr.Precondition("personallogin.errors.no_agent_image", "env", imgEnv)
	}
	if err := imageref.EnsureLocal(ctx, m.cfg.Docker, img); err != nil {
		return "", err
	}
	// The person's own engine volume: images install the CLI on first use, and a workspace's volume
	// would let that workspace's agents change the binary this login runs.
	engines, err := enginecache.Ensure(ctx, m.cfg.Docker, enginecache.UserScope(userID))
	if err != nil {
		return "", err
	}
	env := []string{"HOME=" + imageHome, pl.ConfigDirEnv + "=" + pl.ConfigDir, enginecache.EnvDir + "=" + enginecache.MountPath}
	if m.cfg.Env != nil {
		for k, v := range m.cfg.Env(engineName) {
			env = append(env, k+"="+v)
		}
	}
	if tty {
		// One long line for the URL: a wrapped one would not be found.
		env = append(env, "COLUMNS=4000", "TERM=xterm")
	}
	labels := map[string]string{ContainerLabel: engineName}
	dockerx.StampInstall(labels)
	//i18n:ignore a shell command, not a message
	script := `chown ` + imageUser + `:` + imageUser + ` "$` + pl.ConfigDirEnv + `" "$` + enginecache.EnvDir + `" && exec gosu ` + imageUser + ` "$@"`
	cfg := &container.Config{
		Image: img,
		// Root only to chown the root-owned volume; the script then drops to node with gosu, like
		// the agent entrypoint.
		User:         "root",
		Entrypoint:   []string{"/bin/sh", "-c", script, "sh"},
		Cmd:          argv,
		Env:          env,
		Labels:       labels,
		Tty:          tty,
		OpenStdin:    tty,
		AttachStdin:  tty,
		AttachStdout: true,
		AttachStderr: true,
	}
	host := &container.HostConfig{
		Mounts: []mount.Mount{
			{Type: mount.TypeVolume, Source: vol, Target: pl.ConfigDir},
			{Type: mount.TypeVolume, Source: engines, Target: enginecache.MountPath},
		},
		LogConfig: container.LogConfig{Type: "none"},
	}
	if tty {
		host.ConsoleSize = [2]uint{50, 4000}
	}
	resp, err := m.cfg.Docker.ContainerCreate(ctx, cfg, host, nil, nil, "")
	if err != nil {
		return "", apperr.Wrap(err, "personallogin.errors.container")
	}
	return resp.ID, nil
}

func (m *Manager) run(ctx context.Context, userID, engineName string, pl *engine.PersonalLogin, vol string, argv []string) ([]byte, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, commandWait)
	defer cancel()
	id, err := m.create(ctx, userID, engineName, pl, vol, argv, false)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		logx.Warn(m.cfg.Docker.ContainerRemove(context.WithoutCancel(ctx), id, container.RemoveOptions{Force: true}),
			"personallogin.log.remove_container_failed", "id", id)
	}()
	hj, err := m.cfg.Docker.ContainerAttach(ctx, id, container.AttachOptions{Stream: true, Stdout: true, Stderr: true})
	if err != nil {
		return nil, 0, apperr.Wrap(err, "personallogin.errors.container")
	}
	defer hj.Close()
	waitC, errC := m.cfg.Docker.ContainerWait(ctx, id, container.WaitConditionNextExit)
	if err := m.cfg.Docker.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return nil, 0, apperr.Wrap(err, "personallogin.errors.container")
	}
	var stdout bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, io.Discard, hj.Reader); err != nil {
		return nil, 0, apperr.Wrap(err, "personallogin.errors.container")
	}
	select {
	case r := <-waitC:
		return stdout.Bytes(), r.StatusCode, nil
	case err := <-errC:
		return nil, 0, apperr.Wrap(err, "personallogin.errors.container")
	case <-ctx.Done():
		return nil, 0, apperr.Unavailable("personallogin.errors.command_timeout")
	}
}

func commandErr(err error, code int64) error {
	if err != nil {
		return err
	}
	return apperr.Unavailable("personallogin.errors.command_failed", "code", code)
}

func loginOf(engineName string) (*engine.PersonalLogin, error) {
	pl := engine.PersonalLoginOf(engineName)
	if pl == nil {
		return nil, apperr.Invalid("personallogin.errors.unsupported_engine", "engine", engineName)
	}
	return pl, nil
}

type terminal struct {
	found chan string
	said  chan struct{}
	mu    sync.Mutex
	buf   []byte
	sent  bool
}

func newTerminal() *terminal {
	return &terminal{found: make(chan string, 1), said: make(chan struct{}, 1)}
}

func (t *terminal) read(r io.Reader) {
	chunk := make([]byte, 4096)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			t.write(chunk[:n])
		}
		if err != nil {
			return
		}
	}
}

func (t *terminal) write(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.buf)+len(p) > outputCap {
		t.buf = append([]byte(nil), t.buf[len(t.buf)/2:]...)
	}
	t.buf = append(t.buf, p...)
	if !t.sent {
		if url := AuthorizationURL(t.buf); url != "" {
			t.found <- url
			t.sent = true
			t.buf = nil
		}
		return
	}
	select {
	case t.said <- struct{}{}:
	default:
	}
}

func (t *terminal) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = nil
}

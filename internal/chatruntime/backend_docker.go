package chatruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/enginecache"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/procreap"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type dockerAPI interface {
	ContainerInspect(ctx context.Context, containerID string) (dockertypes.ContainerJSON, error)
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	ContainerList(ctx context.Context, options container.ListOptions) ([]dockertypes.Container, error)
	ContainerExecCreate(ctx context.Context, container string, options container.ExecOptions) (dockertypes.IDResponse, error)
	ContainerExecAttach(ctx context.Context, execID string, config container.ExecStartOptions) (dockertypes.HijackedResponse, error)
	ContainerExecInspect(ctx context.Context, execID string) (container.ExecInspect, error)
}

type launchFunc func(ctx context.Context, at types.AgentType, params types.LaunchParams) (string, error)

type dockerBackend struct {
	docker     dockerAPI
	launch     launchFunc
	dataDir    string
	dataVolume string
}

func (b *dockerBackend) Available() bool { return true }

func (b *dockerBackend) Create(ctx context.Context, spec CreateSpec) (string, error) {
	c, at := spec.Chat, spec.AgentType
	env := spec.Env

	if engine.HostAuthApplies(env) {
		engName := env["ENGINE"]
		if engName == "" {
			engName = at.Engine
		}
		if eng, err := engine.Get(engName); err == nil {
			for _, kv := range eng.AuthEnv(ctx) {
				if k, v, ok := strings.Cut(kv, "="); ok {
					env[k] = v
				}
			}
		}
	}

	stateDir := b.stateDir(c.ID)
	if err := os.MkdirAll(stateDir, 0o750); err != nil {
		return "", apperr.Wrap(err, "chatruntime.errors.create_state_dir")
	}

	params := types.LaunchParams{
		Env:       env,
		NoBuild:   true,
		Resources: spec.Resources,
		// The watcher/registry/board filter on thulu.agent=true; chat containers must flip it off.
		LabelOverrides: map[string]string{
			agent.LabelManaged:     "false",
			agent.LabelChat:        "true",
			agent.LabelChatID:      c.ID,
			agent.LabelEngine:      env["ENGINE"],
			agent.LabelWorkspaceID: c.WorkspaceID,
		},
		ExtraMounts: []types.ExtraMount{
			{Source: stateDir, Target: "/home/node/.claude"},
		},
	}
	if c.WorkspaceID != "" {
		params.EngineCacheScope = enginecache.WorkspaceScope(c.WorkspaceID)
	}
	worktrees := types.CodePaths(spec.Code)
	if len(worktrees) > 0 {
		params.WorkspacePaths = worktrees
		params.WorkspaceReadOnly = spec.WorkspaceReadOnly
	}
	if b.dataVolume != "" {
		params.UseCopyMode = true
		params.DataVolume = b.dataVolume
		params.WorkspacePaths = types.VolumeSubpaths("chat", b.dataDir, params.WorkspacePaths)
		params.ExtraMounts = []types.ExtraMount{
			{Source: filepath.Join("chats", c.ID, "claude"), Target: "/home/node/.claude"},
		}
	}
	params.AddWorktreeGit("chat", worktrees, b.dataDir)
	return b.launch(ctx, *at, params)
}

// The turn runs in its own session under timeout, and writes its process group here so the daemon
// can kill the whole group when it stops waiting: Docker has no call that kills an exec.
const chatTurnPgidFile = "/tmp/.octopus-chat-turn.pgid"

// envChatTurn marks every process a turn starts, so what it leaves running in the background is
// found and killed after the turn.
const envChatTurn = "OCTOPUS_CHAT_TURN"

// Seconds the turn's timeout leaves the engine between TERM and KILL.
const chatTurnKillAfter = 5

const chatTurnDefaultSeconds = 5 * 60

func chatTurnCmd(timeoutSecs int) []string {
	//i18n:ignore shell command run in the chat container
	script := `echo $$ > "$1"; exec timeout --kill-after=` + strconv.Itoa(chatTurnKillAfter) +
		` "$0" /workspace/snippets/chat-turn.sh`
	return []string{"setsid", "-w", "sh", "-c", script, strconv.Itoa(timeoutSecs), chatTurnPgidFile}
}

func turnSeconds(ctx context.Context) int {
	dl, ok := ctx.Deadline()
	if !ok {
		return chatTurnDefaultSeconds
	}
	return max(1, int(math.Ceil(time.Until(dl).Seconds())))
}

func (b *dockerBackend) Exec(ctx context.Context, spec ExecSpec) (ExecOutput, error) {
	marker := newTurnMarker()
	env := append(slices.Clip(spec.Env), envChatTurn+"="+marker)
	create, err := b.docker.ContainerExecCreate(ctx, spec.ContainerID, container.ExecOptions{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Env:          env,
		Cmd:          chatTurnCmd(turnSeconds(ctx)),
	})
	if err != nil {
		return ExecOutput{}, apperr.Wrap(err, "chatruntime.errors.exec_create")
	}
	attach, err := b.docker.ContainerExecAttach(ctx, create.ID, container.ExecStartOptions{})
	if err != nil {
		return ExecOutput{}, apperr.Wrap(err, "chatruntime.errors.exec_attach")
	}
	defer attach.Close()
	// Whatever the turn left running dies after it, however it ended. Off the reply's path: the
	// marker is this turn's alone, so a next turn's processes are safe from it.
	defer func() {
		go b.reapTurn(context.WithoutCancel(ctx), spec.ContainerID, marker)
	}()

	// Half-close stdin or chat-turn.sh's `cat` never returns.
	go func() {
		_, werr := io.WriteString(attach.Conn, spec.Stdin)
		logx.Warn(werr, "chatruntime.log.write_exec_stdin")
		if cw, ok := attach.Conn.(interface{ CloseWrite() error }); ok {
			logx.Warn(cw.CloseWrite(), "chatruntime.log.close_exec_stdin")
		}
	}()

	var stdoutBuf, stderrBuf bytes.Buffer
	copied := make(chan error, 1)
	go func() {
		var stdout io.Writer = &stdoutBuf
		if spec.Stdout != nil {
			stdout = io.MultiWriter(&stdoutBuf, spec.Stdout)
		}
		_, copyErr := stdcopy.StdCopy(stdout, &stderrBuf, attach.Reader)
		copied <- copyErr
	}()
	var copyErr error
	select {
	case copyErr = <-copied:
	case <-ctx.Done():
		// The daemon stops waiting: the turn must stop too, or it keeps spending tokens while the
		// next turn starts on the same session.
		b.killTurnGroup(context.WithoutCancel(ctx), spec.ContainerID)
		attach.Close()
		<-copied
		return ExecOutput{}, apperr.Wrap(ctx.Err(), "chatruntime.errors.turn_stopped")
	}
	if copyErr != nil && !errors.Is(copyErr, io.EOF) {
		return ExecOutput{}, apperr.Wrap(copyErr, "chatruntime.errors.read_exec_stream")
	}

	out := ExecOutput{Stdout: stdoutBuf.Bytes(), Stderr: stderrBuf.Bytes()}
	if inspect, inspectErr := b.docker.ContainerExecInspect(ctx, create.ID); inspectErr == nil {
		out.ExitCode = inspect.ExitCode
	}
	return out, nil
}

func newTurnMarker() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

const turnCleanupTimeout = 30 * time.Second

func (b *dockerBackend) killTurnGroup(ctx context.Context, containerID string) {
	ctx, cancel := context.WithTimeout(ctx, turnCleanupTimeout)
	defer cancel()
	//i18n:ignore shell command run in the chat container
	script := `p=$(cat "$0" 2>/dev/null) && [ -n "$p" ] && kill -KILL -- "-$p"; rm -f "$0"; true`
	if _, err := b.smallExec(ctx, containerID, []string{"sh", "-c", script, chatTurnPgidFile}, nil); err != nil {
		log.Printf("[chat] %s", i18n.Log("chatruntime.log.turn_kill_failed", "container", containerID, "err", err))
		return
	}
	log.Printf("[chat] %s", i18n.Log("chatruntime.log.turn_group_killed", "container", containerID))
}

func (b *dockerBackend) reapTurn(ctx context.Context, containerID, marker string) {
	b.reapByEnv(ctx, containerID, marker, procreap.Matching(envChatTurn, marker))
}

// SweepStaleTurns kills every turn process the container still runs, and what each turn left. The
// daemon that waited on those turns is gone with their reaps: a turn it started keeps running under
// its timeout, spending tokens on the session a new turn is about to use, and whatever it left in
// the background lives as long as the chat stays in use. Call it only when no turn of this daemon
// runs in the container.
func (b *dockerBackend) SweepStaleTurns(ctx context.Context, containerID string) bool {
	b.reapByEnv(ctx, containerID, "*", procreap.Carrying(envChatTurn))
	return true
}

func (b *dockerBackend) reapByEnv(ctx context.Context, containerID, turn string, reapEnv map[string]string) {
	ctx, cancel := context.WithTimeout(ctx, turnCleanupTimeout)
	defer cancel()
	env := make([]string, 0, len(reapEnv))
	for k, v := range reapEnv {
		env = append(env, k+"="+v)
	}
	stdout, err := b.smallExec(ctx, containerID, []string{"bash", "-c", procreap.Script()}, env)
	if err != nil {
		log.Printf("[chat] %s", i18n.Log("chatruntime.log.turn_reap_failed", "container", containerID, "err", err))
		return
	}
	for _, l := range procreap.Parse(stdout) {
		key := "chatruntime.log.turn_process_reaped"
		if l.Survivor {
			key = "chatruntime.log.turn_process_survived"
		}
		log.Printf("[chat] %s", i18n.Log(key, "container", containerID, "turn", turn, "process", l.Process))
	}
}

// smallExec runs a short command and returns its stdout.
func (b *dockerBackend) smallExec(ctx context.Context, containerID string, cmd, env []string) (string, error) {
	create, err := b.docker.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		AttachStdout: true, AttachStderr: true, Cmd: cmd, Env: env,
	})
	if err != nil {
		return "", apperr.Wrap(err, "chatruntime.errors.exec_create")
	}
	attach, err := b.docker.ContainerExecAttach(ctx, create.ID, container.ExecStartOptions{})
	if err != nil {
		return "", apperr.Wrap(err, "chatruntime.errors.exec_attach")
	}
	defer attach.Close()
	var stdout bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, io.Discard, attach.Reader); err != nil && !errors.Is(err, io.EOF) {
		return stdout.String(), apperr.Wrap(err, "chatruntime.errors.read_exec_stream")
	}
	return stdout.String(), nil
}

func (b *dockerBackend) Running(ctx context.Context, containerID string) bool {
	if containerID == "" {
		return false
	}
	info, err := b.docker.ContainerInspect(ctx, containerID)
	return err == nil && info.State != nil && info.State.Running
}

func (b *dockerBackend) Remove(ctx context.Context, containerID string) {
	if containerID == "" {
		return
	}
	err := b.docker.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
	if err != nil && !client.IsErrNotFound(err) {
		log.Printf("[chat] %s", i18n.Log("chatruntime.log.remove_container_failed", "container", containerID, "err", err))
	}
}

// Skip other installs' containers on the same engine, or Reconcile removes their running chats as
// orphans.
func (b *dockerBackend) ListManaged(ctx context.Context) ([]string, error) {
	args := filters.NewArgs(filters.Arg("label", agent.LabelChat+"=true"))
	containers, err := b.docker.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(containers))
	for i := range containers {
		if dockerx.ForeignInstall(containers[i].Labels) {
			continue
		}
		short := containers[i].ID
		if len(short) > 12 {
			short = short[:12]
		}
		out = append(out, short)
	}
	return out, nil
}

func (b *dockerBackend) stateDir(chatID string) string {
	return filepath.Join(b.dataDir, "chats", chatID, "claude")
}

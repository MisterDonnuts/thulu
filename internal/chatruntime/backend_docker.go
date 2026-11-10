package chatruntime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

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

func (b *dockerBackend) Exec(ctx context.Context, spec ExecSpec) (ExecOutput, error) {
	create, err := b.docker.ContainerExecCreate(ctx, spec.ContainerID, container.ExecOptions{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Env:          spec.Env,
		Cmd:          []string{"/workspace/snippets/chat-turn.sh"},
	})
	if err != nil {
		return ExecOutput{}, apperr.Wrap(err, "chatruntime.errors.exec_create")
	}
	attach, err := b.docker.ContainerExecAttach(ctx, create.ID, container.ExecStartOptions{})
	if err != nil {
		return ExecOutput{}, apperr.Wrap(err, "chatruntime.errors.exec_attach")
	}
	defer attach.Close()

	// Half-close stdin or chat-turn.sh's `cat` never returns.
	go func() {
		_, werr := io.WriteString(attach.Conn, spec.Stdin)
		logx.Warn(werr, "chatruntime.log.write_exec_stdin")
		if cw, ok := attach.Conn.(interface{ CloseWrite() error }); ok {
			logx.Warn(cw.CloseWrite(), "chatruntime.log.close_exec_stdin")
		}
	}()

	var stdoutBuf, stderrBuf bytes.Buffer
	if _, copyErr := stdcopy.StdCopy(&stdoutBuf, &stderrBuf, attach.Reader); copyErr != nil && !errors.Is(copyErr, io.EOF) {
		return ExecOutput{}, apperr.Wrap(copyErr, "chatruntime.errors.read_exec_stream")
	}

	out := ExecOutput{Stdout: stdoutBuf.Bytes(), Stderr: stderrBuf.Bytes()}
	if inspect, inspectErr := b.docker.ContainerExecInspect(ctx, create.ID); inspectErr == nil {
		out.ExitCode = inspect.ExitCode
	}
	return out, nil
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

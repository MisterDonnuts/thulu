package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/project"
)

const evidenceOwnerPrefix = "evidence:"

// The queue's concurrency policy and the boot cleanup recognize Evidence owners by this prefix.
func EvidenceOwner(batchID string) string { return evidenceOwnerPrefix + batchID }

func EvidenceBatchID(owner string) (string, bool) {
	id, ok := strings.CutPrefix(owner, evidenceOwnerPrefix)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

// AppPhaseTools, AppPhaseSetup and AppPhaseStart must equal the init run's phase names (setup.go).
const (
	AppPhaseSource = "source"
	AppPhaseTools  = "tools"
	AppPhaseSetup  = "setup"
	AppPhaseStart  = "start"
)

const (
	AppNoRoom        = "no_room"
	AppSourceFailed  = "source_failed"
	AppLaunchFailed  = "launch_failed"
	AppToolsFailed   = "tools_failed"
	AppSetupFailed   = "setup_failed"
	AppStartFailed   = "start_failed"
	AppReadyTimeout  = "ready_timeout"
	AppNoPreviewPort = "no_preview_port"
	AppParamsMissing = "params_missing"
)

type AppError struct {
	Reason string
	Code   string
	Log    string
	Err    error
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return i18n.Text("sandbox.errors.app_failed_cause", "reason", e.Reason, "cause", apperr.Text(e.Err))
	}
	return i18n.Text("sandbox.errors.app_failed", "reason", e.Reason)
}

func (e *AppError) Unwrap() error { return e.Err }

type AppSpec struct {
	WorkspaceID string
	ProjectID   string
	Name        string
	Dir         string
	Script      *project.InitScript
	HostPath    string
	// Archive's Close reports whether the whole archive was produced.
	Archive      func() (io.ReadCloser, error)
	Owner, Stage string
	Progress     func(phase string)
}

type AppResult struct {
	URL  string
	Port int
	Log  string
}

func appSourceDir() string { return initRoot + "/app-source" }

// RunApp never tears the sandbox down, on any path; the caller must Destroy it.
func (m *Manager) RunApp(ctx context.Context, spec AppSpec) (AppResult, error) {
	if spec.Script == nil {
		return AppResult{}, apperr.Invalid("sandbox.errors.app_no_script")
	}
	if (spec.HostPath == "") == (spec.Archive == nil) || spec.Owner == "" {
		return AppResult{}, apperr.Internal("sandbox.errors.app_source")
	}
	// Params get their defaults as init_run gives them: a script reading ${WEB_PORT} under set -u
	// stops on an unset one.
	params, err := resolveInitParams(spec.Script.Manifest, nil)
	if err != nil {
		return AppResult{}, &AppError{Reason: AppParamsMissing, Log: apperr.Text(err), Err: err}
	}
	if spec.Progress == nil {
		spec.Progress = func(string) {}
	}
	opts := AcquireDockerOpts{WorkspaceID: spec.WorkspaceID}
	if spec.HostPath != "" {
		opts.ProjectsPaths = map[string]string{spec.Dir: spec.HostPath}
	}
	// Always a fresh sandbox: a leftover one holds another commit.
	logx.Warn(m.Destroy(ctx, spec.Owner), "sandbox.log.app_teardown", "owner", spec.Owner)

	t := &initTarget{owner: spec.Owner, stage: spec.Stage, opts: opts, params: params,
		proj: InitProject{ID: spec.ProjectID, Name: spec.Name, Dir: spec.Dir, Mounted: true, Script: spec.Script}}
	res, err := m.runApp(ctx, t, spec)
	if errors.Is(err, errInitQueued) {
		logx.Warn(m.queue.Cancel(context.WithoutCancel(ctx), spec.Owner), "sandbox.log.cancel_queue_entry", "task", spec.Owner)
		return AppResult{}, &AppError{Reason: AppNoRoom}
	}
	return res, err
}

func (m *Manager) runApp(ctx context.Context, t *initTarget, spec AppSpec) (AppResult, error) {
	if spec.Archive != nil {
		d := appSourceDir()
		//i18n:ignore shell command run in the sandbox
		if err := m.prewarmStep(ctx, t, fmt.Sprintf(`rm -rf %[1]s && mkdir -p %[1]s`, shellSingleQuote(d)), ""); err != nil {
			return AppResult{}, appSourceError(ctx, err)
		}
		spec.Progress(AppPhaseSource)
		if err := m.prewarmSendSource(ctx, t, spec.Archive, d, path.Join(initProjectsRoot, spec.Dir)); err != nil {
			return AppResult{}, appSourceError(ctx, err)
		}
	}
	run, err := m.launchInit(ctx, t)
	if err != nil {
		if errors.Is(err, errInitQueued) || ctx.Err() != nil {
			return AppResult{}, err
		}
		return AppResult{}, &AppError{Reason: AppLaunchFailed, Log: apperr.Text(err), Err: err}
	}
	if spec.Archive == nil {
		spec.Progress(AppPhaseSource)
	}
	run, err = m.waitApp(ctx, t, run, spec.Progress)
	if err != nil {
		return AppResult{}, err
	}
	tail := strings.TrimSpace(m.initLogTail(ctx, t))
	switch run.State {
	case InitStateReady:
		return m.appURL(ctx, t, tail)
	case InitStateTimeout:
		return AppResult{}, &AppError{Reason: AppReadyTimeout, Log: tail}
	}
	if s := run.Setup; s != nil && s.Status == SetupStateFailed {
		if s.Tools == ToolsFailed {
			return AppResult{}, &AppError{Reason: AppToolsFailed, Log: tail}
		}
		return AppResult{}, &AppError{Reason: AppSetupFailed, Log: tail}
	}
	code := "?"
	if run.ExitCode != nil {
		code = strconv.Itoa(*run.ExitCode)
	}
	return AppResult{}, &AppError{Reason: AppStartFailed, Code: code, Log: tail}
}

func appSourceError(ctx context.Context, err error) error {
	if errors.Is(err, errInitQueued) || ctx.Err() != nil {
		return err
	}
	return &AppError{Reason: AppSourceFailed, Log: apperr.Text(err), Err: err}
}

// No timeout here: the run's own deadline (launchInit, capped by project.InitMaxTimeoutSeconds)
// bounds the wait.
func (m *Manager) waitApp(ctx context.Context, t *initTarget, run InitRun, progress func(string)) (InitRun, error) {
	seen := ""
	for {
		updated, ok, err := m.probeInit(ctx, t, run)
		if err != nil {
			return run, err
		}
		if !ok {
			run.finish(InitStateFailed, initSupersededMessage())
			return run, nil
		}
		run = updated
		if p := run.Phase; p != seen && (p == AppPhaseTools || p == AppPhaseSetup || p == AppPhaseStart) {
			seen = p
			progress(p)
		}
		if run.State != InitStateRunning {
			return run, nil
		}
		select {
		case <-ctx.Done():
			return run, ctx.Err()
		case <-time.After(initPollEvery):
		}
	}
}

func (m *Manager) appURL(ctx context.Context, t *initTarget, tail string) (AppResult, error) {
	sb, err := m.store.Get(ctx, t.owner)
	if err != nil {
		return AppResult{}, err
	}
	for _, port := range t.proj.Script.Manifest.Ports {
		if _, ok := sb.HostPorts[port]; !ok {
			continue
		}
		info, err := m.PreviewURL(ctx, t.owner, port)
		if err != nil {
			return AppResult{}, &AppError{Reason: AppNoPreviewPort, Log: apperr.Text(err), Err: err}
		}
		return AppResult{URL: info.URL, Port: port, Log: tail}, nil
	}
	return AppResult{}, &AppError{Reason: AppNoPreviewPort, Log: tail}
}

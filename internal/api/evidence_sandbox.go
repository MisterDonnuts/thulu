package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/evidence"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
)

type evidenceSandbox struct{ s *Server }

var (
	evidenceRoomRetry = 15 * time.Second
	evidenceRoomWait  = 20 * time.Minute
)

const evidenceSandboxStopMax = 2 * time.Minute

func (e evidenceSandbox) Owner(batchID string) string { return sandbox.EvidenceOwner(batchID) }

func (e evidenceSandbox) Start(ctx context.Context, req evidence.SandboxRequest) (*evidence.SandboxApp, error) {
	s := e.s
	progress := req.Progress
	if progress == nil {
		progress = func(string) {}
	}
	proj, err := s.projectStore.Get(ctx, req.ProjectID)
	if err != nil {
		return nil, sandboxErr(err)
	}
	sc, err := s.projectStore.InitScript(ctx, proj.ID)
	if errors.Is(err, project.ErrNoInitScript) {
		return nil, &evidence.SandboxFailure{Reason: evidence.SandboxReason(evidence.SandboxNoInitScript)}
	}
	if err != nil {
		return nil, sandboxErr(err)
	}
	branch := req.Branch
	if branch == "" {
		branch = proj.DefaultBranch
	}

	progress(evidence.PhaseSource)
	// The project's read-only token is used here only and never handed to the sandbox.
	token := s.projectGithubTokenWith(ctx, proj.ID, githubapp.PermsClone)
	logx.Warn(s.projectStore.FetchBranch(ctx, proj.ID, branch, token), "api.evidence.log.sandbox_fetch_failed",
		"branch", branch, "project", proj.ID)
	commit, err := s.projectStore.BranchCommit(ctx, proj.ID, branch)
	if err != nil {
		if apperr.KindOf(err) == apperr.KindNotFound {
			return nil, &evidence.SandboxFailure{Reason: evidence.SandboxReason(evidence.SandboxBranchNotFound, "branch", branch)}
		}
		return nil, sandboxErr(err)
	}

	spec := sandbox.AppSpec{
		WorkspaceID: proj.WorkspaceID, ProjectID: proj.ID, Name: proj.Name, Dir: filepath.Base(proj.Path),
		Script: sc, Owner: sandbox.EvidenceOwner(req.BatchID), Stage: evidence.RunnerStage, Progress: progress,
	}
	if f := e.source(ctx, &spec, req.BatchID, commit); f != nil {
		return nil, f
	}
	res, err := e.runWithRoom(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &evidence.SandboxApp{Owner: spec.Owner, URL: res.URL, Commit: commit, Port: res.Port}, nil
}

func (e evidenceSandbox) source(ctx context.Context, spec *sandbox.AppSpec, batchID, commit string) *evidence.SandboxFailure {
	ps := e.s.projectStore
	if !e.s.sandbox.mgr.MountsHostPaths() {
		spec.Archive = func() (io.ReadCloser, error) { return ps.OpenCommitArchive(ctx, spec.ProjectID, commit) }
		return nil
	}
	dir, err := ps.AppSourceDir(batchID)
	if err == nil {
		err = ps.ExtractCommit(ctx, spec.ProjectID, commit, dir)
	}
	if err != nil {
		return &evidence.SandboxFailure{Reason: evidence.SandboxReason(evidence.SandboxSourceFailed),
			Log: apperr.Localize(err, i18n.SourceLocale)}
	}
	spec.HostPath = dir
	return nil
}

func (e evidenceSandbox) runWithRoom(ctx context.Context, spec sandbox.AppSpec) (sandbox.AppResult, error) {
	deadline := time.Now().Add(evidenceRoomWait)
	for {
		res, err := e.s.sandbox.mgr.RunApp(ctx, spec)
		if err == nil {
			return res, nil
		}
		var ae *sandbox.AppError
		switch {
		case ctx.Err() != nil:
			return res, ctx.Err()
		case !errors.As(err, &ae):
			return res, sandboxErr(err)
		case ae.Reason != sandbox.AppNoRoom:
			return res, appFailure(ae)
		case time.Now().After(deadline):
			return res, &evidence.SandboxFailure{Reason: evidence.SandboxReason(evidence.SandboxNoRoom)}
		}
		spec.Progress(evidence.PhaseRoom)
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(evidenceRoomRetry):
		}
	}
}

func appFailure(ae *sandbox.AppError) *evidence.SandboxFailure {
	var reason evidence.Reason
	switch ae.Reason {
	case sandbox.AppSourceFailed:
		reason = evidence.SandboxReason(evidence.SandboxSourceFailed)
	case sandbox.AppLaunchFailed:
		reason = evidence.SandboxReason(evidence.SandboxLaunchFailed)
	case sandbox.AppToolsFailed:
		reason = evidence.SandboxReason(evidence.SandboxToolsFailed)
	case sandbox.AppSetupFailed:
		reason = evidence.SandboxReason(evidence.SandboxSetupFailed)
	case sandbox.AppStartFailed:
		reason = evidence.SandboxReason(evidence.SandboxStartFailed, "code", ae.Code)
	case sandbox.AppReadyTimeout:
		reason = evidence.SandboxReason(evidence.SandboxReadyTimeout)
	case sandbox.AppNoPreviewPort:
		reason = evidence.SandboxReason(evidence.SandboxNoPreviewPort)
	case sandbox.AppParamsMissing:
		reason = evidence.SandboxReason(evidence.SandboxParamsMissing, "names", errArg(ae.Err, "names"))
	default:
		reason = evidence.SandboxReason(evidence.SandboxError, "error", ae.Error())
	}
	return &evidence.SandboxFailure{Reason: reason, Log: ae.Log}
}

func errArg(err error, key string) string {
	var ae *apperr.Error
	if !errors.As(err, &ae) {
		return ""
	}
	for i := 0; i+1 < len(ae.Args); i += 2 {
		if k, ok := ae.Args[i].(string); ok && k == key {
			return fmt.Sprint(ae.Args[i+1])
		}
	}
	return ""
}

func sandboxErr(err error) *evidence.SandboxFailure {
	return &evidence.SandboxFailure{Reason: evidence.SandboxReason(evidence.SandboxError,
		"error", apperr.Localize(err, i18n.SourceLocale))}
}

func (e evidenceSandbox) Stop(ctx context.Context, owner string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), evidenceSandboxStopMax)
	defer cancel()
	logx.Warn(e.s.sandbox.mgr.Destroy(ctx, owner), "api.evidence.log.sandbox_stop_failed", "owner", owner)
	if id, ok := sandbox.EvidenceBatchID(owner); ok {
		if dir, err := e.s.projectStore.AppSourceDir(id); err == nil {
			logx.Warn(os.RemoveAll(dir), "api.evidence.log.sandbox_stop_failed", "owner", owner)
		}
	}
}

func (e evidenceSandbox) Leftovers(ctx context.Context) []string {
	seen := map[string]bool{}
	var out []string
	add := func(owner string) {
		if !seen[owner] {
			seen[owner] = true
			out = append(out, owner)
		}
	}
	if st := e.s.sandbox.store; st != nil {
		all, err := st.ListAll(ctx)
		logx.Warn(err, "api.evidence.log.sandbox_list_failed")
		for i := range all {
			if _, ok := sandbox.EvidenceBatchID(all[i].TaskID); ok {
				add(all[i].TaskID)
			}
		}
	}
	for _, name := range e.s.projectStore.AppSourceNames() {
		add(sandbox.EvidenceOwner(name))
	}
	return out
}

package project

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

// Every row existing before these migrations has its clone on disk, which the empty default means.
var cloneMigrations = []migrate.Migration{
	{ID: 27, SQL: `ALTER TABLE projects ADD COLUMN IF NOT EXISTS clone_status TEXT NOT NULL DEFAULT ''`},
	{ID: 28, SQL: `ALTER TABLE projects ADD COLUMN IF NOT EXISTS clone_error TEXT NOT NULL DEFAULT ''`},
	{ID: 29, SQL: `ALTER TABLE projects ADD COLUMN IF NOT EXISTS clone_detail TEXT NOT NULL DEFAULT ''`},
}

// Generous on purpose: it only stops a stalled transfer from holding a project in "cloning"
// forever.
const backgroundCloneTimeout = 30 * time.Minute

type cloneRegistry struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func (r *cloneRegistry) start(id string, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancels == nil {
		r.cancels = map[string]context.CancelFunc{}
	}
	r.cancels[id] = cancel
}

func (r *cloneRegistry) stop(id string) {
	r.mu.Lock()
	cancel := r.cancels[id]
	delete(r.cancels, id)
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Store) CreateInBackground(ctx context.Context, name, repoURL string, opts CreateOpts, done func(*Project)) (*Project, error) {
	proj, err := s.prepareCreate(ctx, name, repoURL, opts)
	if err != nil {
		return nil, err
	}
	proj.CloneStatus = CloneCloning
	if err := s.insertProject(ctx, proj, proj.templateJSON()); err != nil {
		return nil, err
	}
	// The clone outlives the request; WithoutCancel keeps ctx's values (the caller's language).
	cloneCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backgroundCloneTimeout)
	s.clones.start(proj.ID, cancel)
	go func() {
		defer s.clones.stop(proj.ID)
		out := s.runBackgroundClone(cloneCtx, *proj, opts)
		if done != nil {
			done(out)
		}
	}()
	return proj, nil
}

func (s *Store) runBackgroundClone(ctx context.Context, proj Project, opts CreateOpts) *Project {
	err := gitCloneAuth(ctx, opts.GitHubToken, proj.RepoURL, proj.Path, opts.DefaultBranch)
	if err == nil {
		finishClone(&proj, opts.DefaultBranch)
		proj.CloneStatus = ""
	} else {
		logx.Warn(os.RemoveAll(proj.Path), "project.log.cleanup_clone_dir", "path", proj.Path)
		proj.CloneStatus = CloneFailed
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = apperr.Timeout("project.errors.clone_timeout")
		}
		proj.CloneError, proj.CloneDetail = cloneReason(ctx, err)
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), landingWriteTimeout)
	defer cancel()
	res, werr := s.db.ExecContext(writeCtx,
		s.rb(`UPDATE projects SET default_branch = ?, clone_status = ?, clone_error = ?, clone_detail = ?
		 WHERE id = ? AND clone_status = ?`),
		proj.DefaultBranch, proj.CloneStatus, proj.CloneError, proj.CloneDetail, proj.ID, CloneCloning)
	logx.Warn(werr, "project.log.clone_state", "id", proj.ID)
	n, nerr := rowsAffected(res)
	logx.Warn(nerr, "project.log.clone_state", "id", proj.ID)
	if werr == nil && nerr == nil && n == 0 && err == nil {
		// No row updated: the project was deleted while it cloned, so the clone is nobody's.
		logx.Warn(os.RemoveAll(proj.Path), "project.log.cleanup_clone_dir", "path", proj.Path)
	}
	return &proj
}

func cloneReason(ctx context.Context, err error) (reason, detail string) {
	var e *apperr.Error
	if !errors.As(err, &e) {
		return apperr.LocalizeCtx(ctx, apperr.New("project.errors.clone_git")), err.Error()
	}
	reason = apperr.LocalizeCtx(ctx, &apperr.Error{Key: e.Key, Args: e.Args})
	if e.Err != nil {
		detail = apperr.LocalizeCtx(ctx, e.Err)
	}
	return reason, detail
}

func (s *Store) FailInterruptedClones(ctx context.Context) {
	stuck, err := s.interruptedClones(ctx)
	if err != nil {
		logx.Warn(err, "project.log.clone_state", "id", "*")
		return
	}
	for _, r := range stuck {
		if r.path != "" {
			logx.Warn(os.RemoveAll(r.path), "project.log.cleanup_clone_dir", "path", r.path)
		}
		_, err := s.db.ExecContext(ctx,
			s.rb(`UPDATE projects SET clone_status = ?, clone_error = ? WHERE id = ? AND clone_status = ?`),
			CloneFailed, apperr.Text(apperr.Unavailable("project.errors.clone_interrupted")), r.id, CloneCloning)
		logx.Warn(err, "project.log.clone_state", "id", r.id)
	}
}

type interruptedClone struct{ id, path string }

func (s *Store) interruptedClones(ctx context.Context) ([]interruptedClone, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(`SELECT id, path FROM projects WHERE clone_status = ?`), CloneCloning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stuck []interruptedClone
	for rows.Next() {
		var r interruptedClone
		if err := rows.Scan(&r.id, &r.path); err != nil {
			return nil, err
		}
		stuck = append(stuck, r)
	}
	return stuck, rows.Err()
}

func rowsAffected(res interface{ RowsAffected() (int64, error) }) (int64, error) {
	if res == nil {
		return 0, nil
	}
	return res.RowsAffected()
}

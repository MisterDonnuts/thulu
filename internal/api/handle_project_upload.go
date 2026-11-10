package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/task"
)

const maxUploadMetaBytes = 64 << 10

type uploadMeta struct {
	Name           string            `json:"name"`
	Description    string            `json:"description,omitempty"`
	DefaultBoardID string            `json:"default_board_id,omitempty"`
	StageTemplate  map[string]string `json:"stage_template,omitempty"`
}

type uploadResponse struct {
	Project *project.Project     `json:"project"`
	Report  project.UploadReport `json:"report"`
}

func (s *Server) handleUploadProject(w http.ResponseWriter, r *http.Request) {
	mr, err := r.MultipartReader()
	if err != nil {
		writeErrKey(w, r, http.StatusBadRequest, "api.projects.errors.upload_multipart")
		return
	}
	part, err := mr.NextPart()
	if err != nil || part.FormName() != "meta" {
		writeErrKey(w, r, http.StatusBadRequest, "api.projects.errors.upload_meta_first")
		return
	}
	var meta uploadMeta
	if err = json.NewDecoder(io.LimitReader(part, maxUploadMetaBytes)).Decode(&meta); err != nil {
		writeError(w, r, http.StatusBadRequest, apperr.WrapKind(apperr.KindInvalid, err, "api.server.errors.invalid_body"))
		return
	}
	meta.Name = strings.TrimSpace(meta.Name)
	if meta.Name == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.projects.errors.name_required")
		return
	}
	wid, ok := s.newResourceWorkspace(w, r, "project")
	if !ok {
		return
	}
	if meta.DefaultBoardID != "" && !s.boardVisibleToCaller(r, meta.DefaultBoardID) {
		writeErrKey(w, r, http.StatusBadRequest, "api.boards.errors.unknown_board", "board", meta.DefaultBoardID)
		return
	}
	src, lim, done, ok := s.receiveUploadArchive(w, r, mr, wid)
	if !ok {
		return
	}
	defer done()
	log.Printf("[api] %s", i18n.Log("api.projects.log.creating_upload", "name", meta.Name))
	proj, report, err := s.projectStore.CreateFromUpload(r.Context(), meta.Name, src, project.CreateOpts{
		Description:    meta.Description,
		StageTemplate:  meta.StageTemplate,
		DefaultBoardID: meta.DefaultBoardID,
		WorkspaceID:    wid,
	}, lim)
	if err != nil {
		log.Printf("[api] %s", i18n.Log("api.projects.log.create_failed", "err", err))
		writeError(w, r, 0, err)
		return
	}
	writeJSON(w, http.StatusCreated, uploadResponse{Project: proj, Report: report})
}

func (s *Server) handleReuploadProject(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.projectForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if !proj.Hosted() {
		writeErrKey(w, r, http.StatusBadRequest, "project.errors.not_upload_project", "name", proj.Name)
		return
	}
	mode := r.URL.Query().Get("mode")
	switch mode {
	case "", project.ReuploadMerge, project.ReuploadReplace:
	default:
		writeErrKey(w, r, http.StatusBadRequest, "api.projects.errors.reupload_mode", "mode", mode)
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		writeErrKey(w, r, http.StatusBadRequest, "api.projects.errors.upload_multipart")
		return
	}
	src, lim, done, ok := s.receiveUploadArchive(w, r, mr, proj.WorkspaceID)
	if !ok {
		return
	}
	defer done()
	report, err := s.projectStore.ReuploadProject(r.Context(), proj.ID, src, lim, project.ReuploadOpts{Mode: mode})
	var moved *project.MainMovedError
	var clash *project.ReuploadConflictError
	switch {
	case errors.As(err, &moved):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":  i18n.TCtx(r.Context(), "project.errors.main_moved", "branch", moved.Branch, "count", moved.Ahead),
			"code":   "api.projects.errors.main_moved",
			"branch": moved.Branch,
			"ahead":  moved.Ahead,
		})
		return
	case errors.As(err, &clash):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":     i18n.TCtx(r.Context(), "project.errors.reupload_conflict", "branch", clash.Branch, "count", len(clash.Files)),
			"code":      "api.projects.errors.reupload_conflict",
			"branch":    clash.Branch,
			"conflicts": clash.Files,
		})
		return
	case err != nil:
		writeError(w, r, 0, err)
		return
	}
	writeJSON(w, http.StatusOK, uploadResponse{Project: proj, Report: report})
}

func (s *Server) receiveUploadArchive(
	w http.ResponseWriter, r *http.Request, mr *multipart.Reader, wid string,
) (string, project.UploadLimits, func(), bool) {
	lim := project.DefaultUploadLimits()
	part, err := mr.NextPart()
	if err != nil || part.FormName() != "archive" {
		writeErrKey(w, r, http.StatusBadRequest, "api.projects.errors.upload_archive_missing")
		return "", lim, nil, false
	}
	var guard *task.AttachmentWriter
	if s.taskStore != nil {
		guard, err = s.taskStore.ReserveWorkspaceStorage(r.Context(), wid)
		if err != nil {
			writeError(w, r, 0, err)
			return "", lim, nil, false
		}
		if u, uerr := s.taskStore.WorkspaceStorage(r.Context(), wid); uerr == nil && u.RemainingBytes >= 0 {
			lim.MaxBytes = min(lim.MaxBytes, u.RemainingBytes)
		}
	}
	release := func() {
		if guard != nil {
			guard.Done()
		}
	}
	f, err := s.projectStore.IncomingFile()
	if err != nil {
		release()
		writeError(w, r, http.StatusInternalServerError, err)
		return "", lim, nil, false
	}
	name := f.Name()
	done := func() {
		logx.Warn(os.Remove(name), "api.projects.log.remove_upload", "path", name)
		release()
	}
	body := io.LimitReader(part, project.MaxUploadArchiveBytes+1)
	if guard != nil {
		body = guard.Limit(body)
	}
	n, err := io.Copy(f, body)
	closeErr := f.Close()
	switch {
	case err != nil || closeErr != nil:
		done()
		writeError(w, r, http.StatusBadRequest, apperr.Wrap(errors.Join(err, closeErr), "api.projects.errors.upload_read"))
		return "", lim, nil, false
	case n > project.MaxUploadArchiveBytes:
		done()
		writeErrKey(w, r, http.StatusRequestEntityTooLarge, "project.errors.upload_too_big", "max", "200 MiB")
		return "", lim, nil, false
	}
	if guard != nil {
		if err := guard.Check(n); err != nil {
			done()
			writeError(w, r, 0, err)
			return "", lim, nil, false
		}
	}
	return name, lim, done, true
}

func (s *Server) handleProjectBranches(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.projectForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	branches, err := s.projectStore.Branches(r.Context(), proj.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	type taskBranch struct {
		Branch string `json:"branch"`
		TaskID string `json:"task_id"`
		Title  string `json:"title,omitempty"`
	}
	tasks := []taskBranch{}
	ids, err := s.projectStore.TaskBranches(r.Context(), proj.ID)
	logx.Warn(err, "api.projects.log.task_branches", "project", proj.ID)
	for _, id := range ids {
		tb := taskBranch{Branch: project.TaskBranchName(id), TaskID: id}
		if s.taskStore != nil {
			if t, err := s.taskStore.Get(r.Context(), id); err == nil && t.WorkspaceID == proj.WorkspaceID {
				tb.Title = t.Title
			}
		}
		tasks = append(tasks, tb)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"default_branch": proj.DefaultBranch,
		"branches":       branches,
		"task_branches":  tasks,
	})
}

func (s *Server) handleProjectArchive(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.projectForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	branch := r.URL.Query().Get("branch")
	if branch == "" {
		branch = proj.DefaultBranch
	}
	rc, err := s.projectStore.ArchiveBranch(r.Context(), proj.ID, branch)
	if err != nil {
		writeError(w, r, 0, err)
		return
	}
	defer rc.Close()
	file := sanitizeDownloadName(proj.Name + "-" + branch + ".zip")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+file+`"`) //i18n:ignore HTTP header value
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := io.Copy(w, rc); err != nil {
		logx.Warn(err, "api.projects.log.archive_stream", "project", proj.ID, "branch", branch)
	}
}

func sanitizeDownloadName(name string) string {
	var b strings.Builder
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '-', c == '_':
			b.WriteRune(c)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

type mergeResponse struct {
	project.MergeResult
	ProjectID string `json:"project_id"`
}

func (s *Server) handleTaskMerge(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	var body struct {
		ProjectID string `json:"project_id,omitempty"`
		Target    string `json:"target,omitempty"`
	}
	if !decodeOptionalBody(w, r, &body) {
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}
	proj, ok := s.mergeProject(w, r, t, body.ProjectID)
	if !ok {
		return
	}
	target := strings.TrimSpace(body.Target)
	if target == "" {
		target = proj.DefaultBranch
	}
	if err := project.ValidateBranchName(target); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	ctx := r.Context()
	if !s.publishTaskBranch(w, r, t, proj.ID) {
		return
	}
	branch := s.taskBranchFor(t)
	res, err := s.projectStore.MergeInto(ctx, proj.ID, branch, target)
	if err != nil {
		if apperr.KindOf(err) == apperr.KindNotFound {
			writeErrKey(w, r, http.StatusConflict, "api.tasks.errors.merge_nothing", "branch", branch)
			return
		}
		writeError(w, r, 0, err)
		return
	}
	resp := mergeResponse{MergeResult: res, ProjectID: proj.ID}
	if res.Status == project.MergeConflict {
		msg := i18n.TCtx(ctx, "api.tasks.errors.merge_conflict_files", "target", target, "count", len(res.Conflicts))
		writeMergeConflict(w, msg, res.Conflicts, s.kube != nil)
		return
	}
	if res.Status != project.MergeUpToDate {
		note := task.NoteOf("api.tasks.timeline.merged", "who", author.Label(), "branch", branch,
			"target", target, "project", proj.Name, "commit", shortSHA(res.Commit))
		summary := note.Text()
		if _, err := s.taskStore.AddNote(ctx, t.ID, "", task.SourceSystem, "octopus", note); err != nil {
			logx.Warn(err, "api.tasks.log.merge_comment", "task", t.ID)
		}
		logx.Warn(s.taskStore.RecordEvent(ctx, t.ID, task.EventBranchMerged, proj.ID, author.Label(), summary, resp),
			"api.tasks.log.merge_event", "task", t.ID)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) publishTaskBranch(w http.ResponseWriter, r *http.Request, t *task.Task, projectID string) bool {
	ctx := r.Context()
	if s.kube == nil {
		wt, err := s.worktreeGitForProject(ctx, t, projectID)
		if err != nil {
			writeError(w, r, 0, err)
			return false
		}
		if dirty, err := wt.HasChanges(); err == nil && dirty {
			if _, err := wt.Commit(i18n.TCtx(ctx, "api.github.labels.default_commit_message", "task", t.ID)); err != nil {
				writeError(w, r, 0, err)
				return false
			}
		}
	}
	if _, _, err := s.pushTaskBranchForProject(ctx, t, projectID); err != nil {
		var cerr *project.RebaseConflictError
		if errors.As(err, &cerr) {
			writeMergeConflict(w, apperr.LocalizeCtx(ctx, cerr), cerr.Files, s.kube != nil)
			return false
		}
		writeError(w, r, 0, err)
		return false
	}
	return true
}

// agentCanResolve is true only on Kubernetes: in Docker the agent's .git is read-only, so it cannot
// merge the target and commit.
func writeMergeConflict(w http.ResponseWriter, msg string, files []string, agentCanResolve bool) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":             msg,
		"code":              "api.tasks.errors.merge_conflict",
		"status":            project.MergeConflict,
		"conflicts":         files,
		"agent_can_resolve": agentCanResolve,
	})
}

func (s *Server) mergeProject(w http.ResponseWriter, r *http.Request, t *task.Task, id string) (*project.Project, bool) {
	var found *project.Project
	for _, pid := range t.ProjectIDs {
		if id != "" && pid != id {
			continue
		}
		p, err := s.projectStore.Get(r.Context(), pid)
		if err != nil || !p.Hosted() {
			continue
		}
		if found != nil {
			writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.merge_pick_project")
			return nil, false
		}
		found = p
	}
	if found == nil {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.merge_no_upload_project")
		return nil, false
	}
	return found, true
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

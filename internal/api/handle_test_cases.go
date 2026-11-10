package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/task"
)

func credentialAuthor(r *http.Request) string {
	ctx := r.Context()
	if auth.IsAPIKeyAuthed(ctx) {
		if scope := auth.APIKeyRunScope(ctx); scope.IsRun() {
			if scope.Agent == "" {
				return ""
			}
			return "agent:" + scope.Agent
		}
		if name := auth.APIKeyName(ctx); name != "" {
			return "key:" + name
		}
		return "key:" + auth.APIKeyID(ctx)
	}
	if c := auth.ClaimsFromContext(ctx); c != nil {
		if c.Email != "" {
			return c.Email
		}
		return "user:" + c.UserID
	}
	if auth.DevMode() {
		return "user"
	}
	return ""
}

func requireCredentialAuthor(w http.ResponseWriter, r *http.Request) (string, bool) {
	author := credentialAuthor(r)
	if author == "" {
		writeError(w, r, http.StatusForbidden, auth.ErrUnsigned)
		return "", false
	}
	return author, true
}

func statusForTestCaseErr(err error) int {
	switch {
	case errors.Is(err, task.ErrInvalid):
		return http.StatusBadRequest
	case errors.Is(err, task.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, task.ErrConflict):
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

func writeTestCaseErr(w http.ResponseWriter, r *http.Request, err error) {
	writeError(w, r, statusForTestCaseErr(err), err)
}

func (s *Server) testCaseWriteFor(w http.ResponseWriter, r *http.Request) (t *task.Task, author string, ok bool) {
	t, ok = s.taskRowForCaller(w, r, r.PathValue("id"))
	if !ok {
		return nil, "", false
	}
	author, ok = requireCredentialAuthor(w, r)
	return t, author, ok
}

func (s *Server) handleListTestCases(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.taskRowForCaller(w, r, id); !ok {
		return
	}
	cases, err := s.taskStore.ListTestCases(r.Context(), id)
	if err != nil {
		writeTestCaseErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(cases))
}

func (s *Server) handleAddTestCases(w http.ResponseWriter, r *http.Request) {
	t, author, ok := s.testCaseWriteFor(w, r)
	if !ok {
		return
	}
	id := t.ID
	var body struct {
		task.TestCase
		Cases []task.TestCase `json:"cases"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	in := body.Cases
	if len(in) == 0 {
		in = []task.TestCase{body.TestCase}
	}
	created, err := s.taskStore.AddTestCases(r.Context(), id, author, in)
	if err != nil {
		writeTestCaseErr(w, r, err)
		return
	}
	s.recordCasesAdded(r, id, author, created)
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleImportTestCasesFromBody(w http.ResponseWriter, r *http.Request) {
	t, author, ok := s.testCaseWriteFor(w, r)
	if !ok {
		return
	}
	created, err := s.taskStore.ImportTestCasesFromBody(r.Context(), t.ID, author, t.Body)
	if err != nil {
		writeTestCaseErr(w, r, err)
		return
	}
	s.recordCasesAdded(r, t.ID, author, created)
	writeJSON(w, http.StatusOK, nonNil(created))
}

func (s *Server) recordCasesAdded(r *http.Request, taskID, author string, cases []task.TestCase) {
	for i := range cases {
		s.recordEvent(r.Context(), taskID, task.EventTestCaseAdded, cases[i].ID, author,
			fmt.Sprintf("%s · %s", cases[i].Key, cases[i].Title), nil)
	}
}

func (s *Server) handleUpdateTestCase(w http.ResponseWriter, r *http.Request) {
	t, author, ok := s.testCaseWriteFor(w, r)
	if !ok {
		return
	}
	id := t.ID
	var p task.TestCasePatch
	if !decodeBody(w, r, &p) {
		return
	}
	c, err := s.taskStore.UpdateTestCase(r.Context(), id, r.PathValue("case"), author, p)
	if err != nil {
		writeTestCaseErr(w, r, err)
		return
	}
	s.recordEvent(r.Context(), id, task.EventTestCaseUpdated, c.ID, author,
		fmt.Sprintf("%s · %s (%s)", c.Key, c.Title, c.Status), p)
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleDeleteTestCase(w http.ResponseWriter, r *http.Request) {
	t, author, ok := s.testCaseWriteFor(w, r)
	if !ok {
		return
	}
	id := t.ID
	c, err := s.taskStore.GetTestCase(r.Context(), id, r.PathValue("case"))
	if err != nil {
		writeTestCaseErr(w, r, err)
		return
	}
	if err := s.taskStore.DeleteTestCase(r.Context(), id, c.ID); err != nil {
		writeTestCaseErr(w, r, err)
		return
	}
	s.recordEvent(r.Context(), id, task.EventTestCaseDeleted, c.ID, author,
		fmt.Sprintf("%s · %s", c.Key, c.Title), nil)
	writeStatus(w, "deleted")
}

func (s *Server) handleRecordTestCaseRun(w http.ResponseWriter, r *http.Request) {
	t, author, ok := s.testCaseWriteFor(w, r)
	if !ok {
		return
	}
	id := t.ID
	var body struct {
		Verdict              string   `json:"verdict"`
		Observed             string   `json:"observed"`
		Note                 string   `json:"note"`
		VideoAttachmentID    string   `json:"video_attachment_id"`
		TakeAttachmentID     string   `json:"take_attachment_id"`
		ReportID             string   `json:"report_id"`
		CommentID            string   `json:"comment_id"`
		CaptureAttachmentIDs []string `json:"capture_attachment_ids"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	run, c, err := s.taskStore.RecordTestCaseRun(r.Context(), id, r.PathValue("case"), task.TestCaseRun{
		Verdict: body.Verdict, Observed: body.Observed, Note: body.Note,
		VideoAttachmentID: body.VideoAttachmentID, TakeAttachmentID: body.TakeAttachmentID,
		ReportID: body.ReportID, CommentID: body.CommentID, CaptureAttachmentIDs: body.CaptureAttachmentIDs,
		Author: author,
	})
	if err != nil {
		writeTestCaseErr(w, r, err)
		return
	}
	s.recordEvent(r.Context(), id, task.EventTestCaseRun, c.ID, author,
		fmt.Sprintf("%s · %s → %s", c.Key, c.Title, run.Verdict), map[string]string{"run_id": run.ID})
	writeJSON(w, http.StatusCreated, map[string]any{"run": run, "test_case": c})
}

func (s *Server) handleListWorkspaceTestCases(w http.ResponseWriter, r *http.Request) {
	f, err := testCaseFilterFrom(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	f.WorkspaceID, f.Unrestricted = s.callerWorkspace(r)
	rows, counts, err := s.taskStore.ListWorkspaceTestCases(r.Context(), f)
	if err != nil {
		writeTestCaseErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"test_cases": nonNil(rows), "counts": counts.Status, "covered_counts": counts.Covered})
}

func testCaseFilterFrom(r *http.Request) (task.TestCaseFilter, error) {
	q := r.URL.Query()
	f := task.TestCaseFilter{
		BoardID:   q.Get("board_id"),
		ProjectID: q.Get("project_id"),
		Q:         strings.TrimSpace(q.Get("q")),
		Uncovered: q.Get("uncovered") == "1" || q.Get("uncovered") == "true",
	}
	for _, st := range strings.Split(q.Get("status"), ",") {
		if st = strings.TrimSpace(st); st != "" {
			f.Statuses = append(f.Statuses, st)
		}
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return f, apperr.Invalid("api.tasks.errors.limit_not_positive")
		}
		f.Limit = n
	}
	return f, nil
}

func (s *Server) copyTestCasesToClone(r *http.Request, srcID, cloneID string) error {
	return s.taskStore.CopyTestCases(r.Context(), srcID, cloneID, credentialAuthor(r))
}

package api

import (
	"net/http"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// GET /tasks/{id}/criteria: the task's acceptance criteria, in order.
func (s *Server) handleListCriteria(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskRowForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	list, err := s.taskStore.ListCriteria(r.Context(), t.ID)
	if err != nil {
		writeTestCaseErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(list))
}

// PUT /tasks/{id}/criteria {"criteria": [{"key": "AC-1", "text": "..."}]}: replaces the list and
// rewrites the brief's acceptance-criteria section from it.
func (s *Server) handleSetCriteria(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Criteria []task.CriterionInput `json:"criteria"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	author, ok := requireCredentialAuthor(w, r)
	if !ok {
		return
	}
	// The section lives in the body, which the section handlers rewrite under the same lock.
	s.sectionsMu.Lock()
	defer s.sectionsMu.Unlock()
	t, ok := s.taskRowForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	list, err := s.taskStore.SetCriteria(r.Context(), t.ID, author, body.Criteria)
	if err != nil {
		writeTestCaseErr(w, r, err)
		return
	}
	s.recordEvent(r.Context(), t.ID, task.EventSectionUpdated, task.CriteriaSection, author,
		i18n.Text("api.tasks.timeline.criteria_set", "count", len(list)), nil)
	writeJSON(w, http.StatusOK, nonNil(list))
}

// POST /tasks/{id}/criteria/approve {"keys": [...]}: a person approves the named criteria, every one
// when keys is empty. Changing a criterion's text later clears its approval.
func (s *Server) handleApproveCriteria(w http.ResponseWriter, r *http.Request) {
	t, author, ok := s.testCaseWriteFor(w, r)
	if !ok || !signedInPersonOnly(w, r, author, "api.tasks.errors.criteria_approve_person_only") {
		return
	}
	var body struct {
		Keys []string `json:"keys"`
	}
	if !decodeOptionalBody(w, r, &body) {
		return
	}
	list, err := s.taskStore.ApproveCriteria(r.Context(), t.ID, author, body.Keys)
	if err != nil {
		writeTestCaseErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(list))
}

// needKindView is one need kind as GET /test-cases/need-kinds serves it: the catalog entry plus
// its title and help in the caller's language.
type needKindView struct {
	task.NeedKind
	Title string `json:"title"`
	Help  string `json:"help"`
}

// GET /test-cases/need-kinds: what a blocked case can need from a person, hardest to get first.
func (s *Server) handleListNeedKinds(w http.ResponseWriter, r *http.Request) {
	locale := i18n.RequestLocale(r)
	kinds := task.NeedKinds()
	out := make([]needKindView, len(kinds))
	for i, k := range kinds {
		out[i] = needKindView{NeedKind: k, Title: i18n.T(locale, k.TitleKey), Help: i18n.T(locale, k.HelpKey)}
	}
	writeJSON(w, http.StatusOK, out)
}

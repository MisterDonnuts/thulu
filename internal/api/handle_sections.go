package api

import (
	"net/http"
	"strconv"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// sectionsMu serializes the read-modify-write of the body: the store's Update is last-write-wins on
// the full row.

type sectionsResponse struct {
	Preamble string         `json:"preamble"`
	Sections []task.Section `json:"sections"`
}

func sectionsOf(t *task.Task) sectionsResponse {
	pre, secs := task.ParseSections(t.Body)
	secs = nonNil(secs)
	return sectionsResponse{Preamble: pre, Sections: secs}
}

func (s *Server) handleListSections(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, sectionsOf(t))
}

func (s *Server) handleSetSection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	slug := r.PathValue("slug")
	var body struct {
		Title   string `json:"title,omitempty"`
		Content string `json:"content"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}

	s.sectionsMu.Lock()
	defer s.sectionsMu.Unlock()
	t, ok := s.taskForCaller(w, r, id)
	if !ok {
		return
	}
	newBody, err := task.SetSection(t.Body, slug, body.Title, body.Content)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	updated, err := s.taskStore.Update(r.Context(), id, task.Patch{Body: &newBody})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	title := body.Title
	if title == "" {
		title = task.CanonicalSectionTitle(slug)
	}
	s.recordEvent(r.Context(), id, task.EventSectionUpdated, slug, author.Name,
		i18n.Text("api.tasks.timeline.section_updated", "section", title), nil)
	writeJSON(w, http.StatusOK, sectionsOf(updated))
}

func (s *Server) handleDeleteSection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	slug := r.PathValue("slug")
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}

	s.sectionsMu.Lock()
	defer s.sectionsMu.Unlock()
	t, ok := s.taskForCaller(w, r, id)
	if !ok {
		return
	}
	title := slug
	_, secs := task.ParseSections(t.Body)
	for _, sec := range secs {
		if sec.Slug == slug {
			title = sec.Title
		}
	}
	newBody, found := task.RemoveSection(t.Body, slug)
	if !found {
		writeErrKey(w, r, http.StatusNotFound, "api.tasks.errors.section_not_found", "section", slug)
		return
	}
	updated, err := s.taskStore.Update(r.Context(), id, task.Patch{Body: &newBody})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.recordEvent(r.Context(), id, task.EventSectionUpdated, slug, author.Name,
		i18n.Text("api.tasks.timeline.section_removed", "section", title), nil)
	writeJSON(w, http.StatusOK, sectionsOf(updated))
}

func (s *Server) handleCheckSectionItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	slug := r.PathValue("slug")
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || index < 1 {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.index_not_positive")
		return
	}
	var body struct {
		Checked *bool  `json:"checked"`
		Note    string `json:"note,omitempty"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Checked == nil {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.checked_required")
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}

	s.sectionsMu.Lock()
	defer s.sectionsMu.Unlock()
	t, ok := s.taskForCaller(w, r, id)
	if !ok {
		return
	}
	newBody, err := task.SetChecklistItem(t.Body, slug, index, *body.Checked, body.Note)
	if err != nil {
		writeError(w, r, 0, err)
		return
	}
	updated, err := s.taskStore.Update(r.Context(), id, task.Patch{Body: &newBody})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	summary := i18n.Text("api.tasks.timeline.item_unchecked", "index", index, "section", slug)
	if *body.Checked {
		summary = i18n.Text("api.tasks.timeline.item_checked", "index", index, "section", slug)
	}
	s.recordEvent(r.Context(), id, task.EventChecklistToggle, slug, author.Name, summary, nil)
	writeJSON(w, http.StatusOK, sectionsOf(updated))
}

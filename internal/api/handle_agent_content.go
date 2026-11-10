package api

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

func (s *Server) storedAgentFiles(w http.ResponseWriter, r *http.Request, store *agentstore.Store, tenant string) {
	files, err := store.ListOwner(r.Context(), tenant, agentstore.KindAgent, r.PathValue("name"))
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	out := make([]agentFileEntry, 0, len(files))
	for i := range files {
		out = append(out, agentFileEntry{Path: files[i].Path, Kind: "file", Size: int64(files[i].Size)})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) storedAgentFileRead(w http.ResponseWriter, r *http.Request, store *agentstore.Store, tenant string) {
	f, err := store.ReadFile(r.Context(), tenant, agentstore.KindAgent, r.PathValue("name"), storedPath(r))
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	writeText(w, f.Body)
}

// Every write must be audited: this log is the only record of who changed what future runs of this
// agent are told.
func (s *Server) storedAgentFileWrite(w http.ResponseWriter, r *http.Request, store *agentstore.Store, tenant string) {
	name := r.PathValue("name")
	if _, err := store.GetAgent(r.Context(), tenant, name); err != nil {
		// Refuse files for agents the workspace lacks: no run would read them, yet the write would
		// answer 200.
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	body, ok := readContentBody(w, r)
	if !ok {
		return
	}
	path, actor := storedPath(r), actorID(r)
	f, err := store.PutFile(r.Context(), tenant, agentstore.KindAgent, name, path, body, actor)
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	s.auditContentChange(r, tenant, agentstore.KindAgent, name, path, agentstore.ActionUpdated, f.SHA)
	writeJSON(w, http.StatusOK, map[string]any{"size": f.Size, "sha256": f.SHA})
}

func (s *Server) storedAgentFileDelete(w http.ResponseWriter, r *http.Request, store *agentstore.Store, tenant string) {
	name, path, actor := r.PathValue("name"), storedPath(r), actorID(r)
	if err := store.DeleteFile(r.Context(), tenant, agentstore.KindAgent, name, path, actor); err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	s.auditContentChange(r, tenant, agentstore.KindAgent, name, path, agentstore.ActionDeleted, "")
	writeStatus(w, "deleted")
}

// 1<<20 is the store's per-file limit; the extra byte lets an oversized body reach the store's own
// check.
func readContentBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, apperr.WrapKind(apperr.KindInvalid, err, "api.agents.errors.read_body"))
		return nil, false
	}
	return body, true
}

func writeText(w http.ResponseWriter, body []byte) {
	//i18n:ignore HTTP header value, not a message
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(body)
}

func storedPath(r *http.Request) string {
	return strings.TrimPrefix(r.PathValue("path"), "/")
}

var contentChangeSummaries = map[string]map[string]string{
	agentstore.KindAgent: {
		agentstore.ActionUpdated:  "api.agents.audit.content_updated",
		agentstore.ActionDeleted:  "api.agents.audit.content_deleted",
		agentstore.ActionRestored: "api.agents.audit.content_restored",
	},
	agentstore.KindSkill: {
		agentstore.ActionUpdated:  "api.skills.audit.content_updated",
		agentstore.ActionDeleted:  "api.skills.audit.content_deleted",
		agentstore.ActionRestored: "api.skills.audit.content_restored",
	},
}

func (s *Server) auditContentChange(r *http.Request, tenant, kind, owner, path, action, sha string) {
	if s.auditStore == nil {
		return
	}
	resourceType := "agent_content"
	if kind == agentstore.KindSkill {
		resourceType = "skill_content"
	}
	if agentstore.IsReservedTenant(tenant) {
		tenant = ""
		resourceType = "system_skill_content"
	}
	s.auditStore.LogRequestIn(r, tenant, audit.KindAgentContentChanged, owner+"/"+path, resourceType,
		i18n.Text(contentChangeSummaries[kind][action], "agent", owner, "skill", owner, "path", path),
		map[string]string{"kind": kind, "owner": owner, "path": path, "sha256": sha, "action": action})
}

func (s *Server) auditAgentDef(r *http.Request, tenant, name, summaryKey string) {
	if s.auditStore == nil {
		return
	}
	s.auditStore.LogRequestIn(r, tenant, audit.KindAgentDefChanged, name, "agent",
		i18n.Text(summaryKey, "agent", name), map[string]string{"agent": name})
}

func (s *Server) handleAgentRevisions(w http.ResponseWriter, r *http.Request) {
	s.listRevisions(w, r, agentstore.KindAgent)
}

func (s *Server) handleAgentRevisionBody(w http.ResponseWriter, r *http.Request) {
	s.revisionBody(w, r, agentstore.KindAgent)
}

func (s *Server) handleRestoreAgentRevision(w http.ResponseWriter, r *http.Request) {
	s.restoreRevision(w, r, agentstore.KindAgent)
}

func (s *Server) handleAgentDefRevisions(w http.ResponseWriter, r *http.Request) {
	store, tenant, ok := s.workspaceStore(w, r)
	if !ok {
		return
	}
	if store == nil {
		writeJSON(w, http.StatusOK, []agentstore.DefRevision{})
		return
	}
	revs, err := store.ListDefRevisions(r.Context(), tenant, r.PathValue("name"))
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	writeJSON(w, http.StatusOK, revs)
}

func (s *Server) handleRestoreAgentDefRevision(w http.ResponseWriter, r *http.Request) {
	store, tenant, ok := s.revisionStore(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErrKey(w, r, http.StatusBadRequest, "api.agents.errors.revision_id_not_number")
		return
	}
	name := r.PathValue("name")
	a, err := store.RestoreDefRevision(r.Context(), tenant, name, id, actorID(r))
	if errors.Is(err, agentstore.ErrNotFound) {
		writeErrKey(w, r, http.StatusNotFound, "api.agents.errors.revision_not_found")
		return
	}
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	s.invalidateCatalog(tenant)
	s.auditAgentDef(r, tenant, name, "api.agents.audit.definition_restored")
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) listRevisions(w http.ResponseWriter, r *http.Request, kind string) {
	store, tenant, ok := s.workspaceStore(w, r)
	if !ok {
		return
	}
	if store == nil {
		writeJSON(w, http.StatusOK, []agentstore.Revision{})
		return
	}
	revs, err := store.ListRevisions(r.Context(), tenant, kind, r.PathValue("name"),
		strings.TrimPrefix(r.URL.Query().Get("path"), "/"),
		atoiDefault(r.URL.Query().Get("limit"), 0))
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(revs))
}

func (s *Server) revisionBody(w http.ResponseWriter, r *http.Request, kind string) {
	store, tenant, ok := s.revisionStore(w, r)
	if !ok {
		return
	}
	rev, ok := s.ownerRevision(w, r, store, tenant, kind)
	if !ok {
		return
	}
	body, err := store.RevisionBody(r.Context(), tenant, rev.ID)
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	writeText(w, body)
}

func (s *Server) restoreRevision(w http.ResponseWriter, r *http.Request, kind string) {
	store, tenant, ok := s.revisionStore(w, r)
	if !ok {
		return
	}
	rev, ok := s.ownerRevision(w, r, store, tenant, kind)
	if !ok {
		return
	}
	mode := strings.TrimSpace(r.URL.Query().Get("mode"))
	actor := actorID(r)
	f, err := store.RestoreRevision(r.Context(), tenant, rev.ID, mode, actor)
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return
	}
	s.invalidateCatalog(tenant)
	s.auditContentChange(r, tenant, kind, f.Owner, f.Path, agentstore.ActionRestored, f.SHA)
	writeJSON(w, http.StatusOK, map[string]any{
		"owner": f.Owner, "path": f.Path, "size": f.Size, "sha256": f.SHA,
	})
}

func (s *Server) revisionStore(w http.ResponseWriter, r *http.Request) (*agentstore.Store, string, bool) {
	store, tenant, ok := s.workspaceStore(w, r)
	if !ok {
		return nil, "", false
	}
	if store == nil {
		writeErrKey(w, r, http.StatusNotFound, "api.agents.errors.no_stored_content")
		return nil, "", false
	}
	return store, tenant, true
}

// Revision ids are a BIGSERIAL shared by every tenant, kind and owner, so kind and owner must be
// checked here.
// Refuse with 404, not 403, so callers cannot learn that a revision exists (matches forCaller).
func (s *Server) ownerRevision(
	w http.ResponseWriter, r *http.Request, store *agentstore.Store, tenant, kind string,
) (agentstore.Revision, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErrKey(w, r, http.StatusBadRequest, "api.agents.errors.revision_id_not_number")
		return agentstore.Revision{}, false
	}
	rev, err := store.GetRevision(r.Context(), tenant, id)
	if errors.Is(err, agentstore.ErrNotFound) {
		writeErrKey(w, r, http.StatusNotFound, "api.agents.errors.revision_not_found")
		return agentstore.Revision{}, false
	}
	if err != nil {
		writeError(w, r, statusForStoreErr(err), err)
		return agentstore.Revision{}, false
	}
	if rev.Kind != kind || rev.Owner != r.PathValue("name") {
		writeErrKey(w, r, http.StatusNotFound, "api.agents.errors.revision_not_found")
		return agentstore.Revision{}, false
	}
	return rev, true
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

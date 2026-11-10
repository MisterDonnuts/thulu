package api

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func logExportError(r *http.Request, err error) {
	//nolint:gosec // G706: query string sanitized with logx.Safe (strips control chars)
	log.Printf("[audit] %s", i18n.Log("api.audit.log.export_failed", "query", logx.Safe(r.URL.RawQuery), "err", err))
}

func auditOptsFromQuery(q url.Values, wsID string) audit.ListOptions {
	opts := audit.ListOptions{
		WorkspaceID: wsID,
		ActorID:     q.Get("actor_id"),
		ResourceID:  q.Get("resource_id"),
		Kind:        audit.EventKind(q.Get("kind")),
	}
	if v := q.Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			opts.Since = t
		}
	}
	if v := q.Get("until"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			opts.Until = t
		}
	}
	return opts
}

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	if s.auditStore == nil {
		writeErrKey(w, r, http.StatusNotFound, "api.audit.errors.disabled")
		return
	}

	page, ok := pageFromQuery(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	callerWID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	opts := auditOptsFromQuery(q, callerWID)
	opts.Cursor = page.Cursor
	// Don't use page.Limit: callers rely on this endpoint's older 1000 ceiling, which the store
	// enforces.
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			opts.Limit = n
		}
	}
	if o := q.Get("offset"); o != "" {
		if n, err := strconv.Atoi(o); err == nil {
			opts.Offset = n
		}
	}

	entries, next, err := s.auditStore.ListPage(opts)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	setNextCursor(w, next)
	writeJSON(w, http.StatusOK, auditEntryViews(entries, i18n.RequestLocale(r), s.boards))
}

type auditEntryView struct {
	audit.Entry
	// Summary is part of the hashed row and exported verbatim; never rewrite it, translate into
	// SummaryText only.
	SummaryText string `json:"summary_text"`
}

var auditSummaries = sync.OnceValue(func() *i18n.Retranslator {
	return i18n.NewRetranslator(i18n.Default(), isAuditSummaryKey).WithLegacy(legacyAuditSummaries)
})

var legacyAuditSummaries = map[string]string{
	"auth.audit_legacy.api_key_created": "auth.audit.api_key_created",
}

func auditValues(boards stagepkg.Boards) i18n.ValueFunc {
	flow := &flowLabels{boards: boards}
	return func(locale, key string, vals map[string]any) {
		switch {
		case strings.HasPrefix(key, "auth.audit."): //i18n:ignore a catalog key prefix
			authAuditValues(locale, vals)
		case strings.HasPrefix(key, "api.stages.audit."): //i18n:ignore a catalog key prefix
			flow.stageValues(locale, vals)
		case strings.HasPrefix(key, "api.boards.audit."): //i18n:ignore a catalog key prefix
			flow.boardValues(locale, vals)
		case key == "api.updates.audit.reported":
			updateAuditValues(locale, vals)
		}
	}
}

func authAuditValues(locale string, vals map[string]any) {
	for name, value := range vals {
		raw, ok := value.(string)
		if !ok {
			continue
		}
		switch name {
		case "role":
			vals[name] = auth.RoleLabel(locale, raw)
		case "permissions":
			vals[name] = auth.PermissionsLabel(locale, auth.DecodePermissions(raw))
		case "until":
			vals[name] = auth.AuditTimeLabel(locale, raw)
		}
	}
}

func updateAuditValues(locale string, vals map[string]any) {
	if result, ok := vals["result"].(string); ok {
		if key, known := updateResults[result]; known {
			vals["result"] = i18n.T(locale, key)
		}
	}
	if from, ok := vals["from"].(string); ok {
		for _, l := range i18n.Default().Locales() {
			if from == i18n.T(l, "api.updates.labels.unknown_version") {
				vals["from"] = i18n.T(locale, "api.updates.labels.unknown_version")
				break
			}
		}
	}
}

type flowLabels struct {
	boards stagepkg.Boards
	list   []stagepkg.BoardInfo
	stages map[string][]types.StageInfo
}

func (f *flowLabels) board(id string) (stagepkg.BoardInfo, bool) {
	if f.list == nil {
		f.list = f.boards.BoardList()
		f.stages = map[string][]types.StageInfo{}
	}
	for _, b := range f.list {
		if b.ID == id {
			return b, true
		}
	}
	return stagepkg.BoardInfo{}, false
}

func (f *flowLabels) boardLabel(locale, id string) string {
	if b, ok := f.board(id); ok {
		if label := stagepkg.LocalizeSeededBoard(b, locale).Label; label != "" {
			return label
		}
		return id
	}
	if id == stagepkg.DefaultBoardID {
		return i18n.T(locale, "stage.labels.default_board")
	}
	return id
}

func (f *flowLabels) stageLabel(locale, boardID, id string) string {
	var stages []types.StageInfo
	if _, ok := f.board(boardID); ok {
		// Boards.Board falls back to the default board for an unknown id, so call it only for ids
		// the list has.
		if stages, ok = f.stages[boardID]; !ok {
			stages = f.boards.Board(boardID).List()
			f.stages[boardID] = stages
		}
	}
	seeded := stagepkg.LocalizeSeeded(stages, locale)
	for i := range seeded {
		if string(seeded[i].ID) == id && seeded[i].Label != "" {
			return seeded[i].Label
		}
	}
	if name, ok := stagepkg.SeededName(locale, types.Stage(id)); ok {
		return name
	}
	return id
}

func (f *flowLabels) stageValues(locale string, vals map[string]any) {
	boardID := ""
	if b, ok := vals["board"].(string); ok {
		boardID = b
	}
	for _, name := range []string{"stage", "dest"} {
		if id, ok := vals[name].(string); ok {
			vals[name] = f.stageLabel(locale, boardID, id)
		}
	}
	if boardID != "" {
		vals["board"] = f.boardLabel(locale, boardID)
	}
}

func (f *flowLabels) boardValues(locale string, vals map[string]any) {
	for _, name := range []string{"board", "dest"} {
		if id, ok := vals[name].(string); ok {
			vals[name] = f.boardLabel(locale, id)
		}
	}
}

func isAuditSummaryKey(key string) bool {
	//i18n:ignore catalog key segments, not messages
	return strings.Contains(key, ".audit.") || strings.HasPrefix(key, "audit.comments.")
}

func auditEntryViews(entries []audit.Entry, locale string, boards stagepkg.Boards) []auditEntryView {
	out := make([]auditEntryView, 0, len(entries))
	rt := auditSummaries().WithValues(auditValues(boards))
	for i := range entries {
		text, _ := rt.Translate(entries[i].Summary, locale)
		out = append(out, auditEntryView{Entry: entries[i], SummaryText: text})
	}
	return out
}

func (s *Server) handleExportAudit(w http.ResponseWriter, r *http.Request) {
	if s.auditStore == nil {
		writeErrKey(w, r, http.StatusNotFound, "api.audit.errors.disabled")
		return
	}
	q := r.URL.Query()
	format := q.Get("format")
	if format == "" {
		format = "csv"
	}

	callerWID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	opts := auditOptsFromQuery(q, callerWID)

	stamp := time.Now().UTC().Format("20060102-150405")
	ext, contentType := "csv", "text/csv"
	if format != "csv" {
		ext, contentType = "ndjson", "application/x-ndjson"
	}
	w.Header().Set("Content-Type", contentType)
	//i18n:ignore HTTP header value, not a message
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="octopus-audit-%s.%s"`, stamp, ext))
	if err := s.auditStore.Export(opts, format, w); errors.Is(err, audit.ErrExportUnavailable) {
		w.Header().Del("Content-Disposition")
		writeErrKey(w, r, http.StatusForbidden, "api.audit.errors.export_requires_ee")
	} else if err != nil {
		logExportError(r, err)
	}
}

// The hash chain spans every workspace: only operator-workspace admins (OCTOPUS_OPERATOR_WORKSPACE)
// or unrestricted callers may read it.
func (s *Server) handleVerifyAudit(w http.ResponseWriter, r *http.Request) {
	if s.auditStore == nil {
		writeErrKey(w, r, http.StatusNotFound, "api.audit.errors.disabled")
		return
	}
	wid, unrestricted := s.callerWorkspace(r)
	if op := strings.TrimSpace(os.Getenv(OperatorWorkspaceEnv)); !unrestricted && (op == "" || op != wid) {
		writeErrKey(w, r, http.StatusForbidden, "api.audit.errors.verify_operator_only", "env", OperatorWorkspaceEnv)
		return
	}
	rep, err := s.auditStore.VerifyChain(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

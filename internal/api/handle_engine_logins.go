package api

import (
	"context"
	"net/http"
	"os"

	"github.com/MisterDonnuts/thulu/internal/agentimage"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/personallogin"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// Only a person may reach these routes; any API key, a run's included, gets 403.
// Never return, log or record the code or anything the CLI keeps in the volume. The URL goes only
// to the person whose sign-in it is, while that sign-in is open, and is never logged.

func (s *Server) wirePersonalLogins() {
	if s.kube != nil || s.docker == nil || s.taskDispatcher == nil {
		return
	}
	d := s.taskDispatcher
	s.personalLogins = personallogin.NewManager(personallogin.Config{
		Docker: s.docker,
		Image:  func(eng string) (string, string) { return agentimage.FromEnv(os.Getenv).For(eng) },
		Env:    d.LoginEgressEnv,
	})
	d.WithPersonalLogins(s.personalLogins)
}

func personID(ctx context.Context) (string, bool) {
	if _, ok := auth.PersonRole(ctx); !ok {
		return "", false
	}
	id := auth.ActorFromContext(ctx).ID
	return id, id != ""
}

func launchCtx(reqCtx context.Context) context.Context {
	ctx := context.WithoutCancel(reqCtx)
	if id, ok := personID(reqCtx); ok {
		ctx = task.WithTriggeredBy(ctx, id)
	}
	return ctx
}

func (s *Server) engineLoginCaller(w http.ResponseWriter, r *http.Request, withEngine bool) (string, string, bool) {
	if s.personalLogins == nil {
		writeErrKey(w, r, http.StatusNotFound, "api.engine_logins.errors.unavailable")
		return "", "", false
	}
	id, ok := personID(r.Context())
	if !ok {
		writeErrKey(w, r, http.StatusForbidden, "api.chats.errors.person_required",
			"what", i18n.TCtx(r.Context(), "api.engine_logins.labels.person_connecting"))
		return "", "", false
	}
	if !withEngine {
		return id, "", true
	}
	name := r.PathValue("engine")
	if engine.PersonalLoginOf(name) == nil || name == "" {
		writeErrKey(w, r, http.StatusNotFound, "api.engine_logins.errors.unsupported_engine", "engine", name)
		return "", "", false
	}
	return id, name, true
}

func (s *Server) handleListEngineLogins(w http.ResponseWriter, r *http.Request) {
	id, _, ok := s.engineLoginCaller(w, r, false)
	if !ok {
		return
	}
	out := []personallogin.Status{}
	for _, name := range engine.Known() {
		if engine.PersonalLoginOf(name) == nil {
			continue
		}
		st, err := s.personalLogins.Status(r.Context(), id, name)
		if err != nil {
			writeError(w, r, 0, err)
			return
		}
		out = append(out, st)
	}
	writeJSON(w, http.StatusOK, map[string]any{"logins": out})
}

func (s *Server) handleStartEngineLogin(w http.ResponseWriter, r *http.Request) {
	id, name, ok := s.engineLoginCaller(w, r, true)
	if !ok {
		return
	}
	if err := s.personalLogins.Begin(r.Context(), id, name); err != nil {
		writeError(w, r, 0, err)
		return
	}
	writeJSON(w, http.StatusAccepted, flowBody(r, s.personalLogins.Flow(id, name)))
}

// The dashboard polls this while the CLI is prepared: the first sign-in installs it, which takes
// several seconds before there is a link to show.
func (s *Server) handleEngineLoginFlow(w http.ResponseWriter, r *http.Request) {
	id, name, ok := s.engineLoginCaller(w, r, true)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, flowBody(r, s.personalLogins.Flow(id, name)))
}

type engineLoginFlow struct {
	Phase personallogin.Phase `json:"phase"`
	URL   string              `json:"url,omitempty"`
	Error string              `json:"error,omitempty"`
}

func flowBody(r *http.Request, fs personallogin.FlowState) engineLoginFlow {
	out := engineLoginFlow{Phase: fs.Phase, URL: fs.URL}
	if fs.Err != nil {
		out.Error = apperr.BodyFor(r, fs.Err).Error
	}
	return out
}

func (s *Server) handleCompleteEngineLogin(w http.ResponseWriter, r *http.Request) {
	id, name, ok := s.engineLoginCaller(w, r, true)
	if !ok {
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	st, err := s.personalLogins.Complete(r.Context(), id, name, body.Code)
	body.Code = ""
	if err != nil {
		writeError(w, r, 0, err)
		return
	}
	s.emit(r, audit.KindEngineLoginConnected, "", name, "engine_login",
		i18n.Text("api.engine_logins.audit.connected", "engine", name), nil)
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleCancelEngineLogin(w http.ResponseWriter, r *http.Request) {
	id, name, ok := s.engineLoginCaller(w, r, true)
	if !ok {
		return
	}
	s.personalLogins.Cancel(context.WithoutCancel(r.Context()), id, name)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDisconnectEngineLogin(w http.ResponseWriter, r *http.Request) {
	id, name, ok := s.engineLoginCaller(w, r, true)
	if !ok {
		return
	}
	if err := s.personalLogins.Disconnect(context.WithoutCancel(r.Context()), id, name); err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	s.emit(r, audit.KindEngineLoginDisconnected, "", name, "engine_login",
		i18n.Text("api.engine_logins.audit.disconnected", "engine", name), nil)
	w.WriteHeader(http.StatusNoContent)
}

package api

import (
	"net/http"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// Authorship comes from the credential (auth.Author), never from a request body, form field or
// query parameter.

func requireAuthor(w http.ResponseWriter, r *http.Request) (auth.Author, bool) {
	a, err := auth.AuthorFromRequest(r)
	if err != nil {
		writeError(w, r, http.StatusForbidden, err)
		return auth.Author{}, false
	}
	return a, true
}

func authorLabel(r *http.Request) string {
	a, err := auth.AuthorFromRequest(r)
	if err != nil {
		return ""
	}
	return a.Label()
}

// Source decides whether a comment launches the agents it mentions, so it must come from the
// credential, never the request.
func commentSource(a auth.Author) task.Source {
	switch {
	case a.IsAuditor():
		return task.SourceAuditor
	case a.Agent:
		return task.SourceAgent
	default:
		return task.SourceUser
	}
}

package auth

import (
	"context"
	"net/http"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// Derived from the credential only, never from the request body: otherwise an agent could sign as
// the person it works for.
type Author struct {
	// Agent names cannot contain ":" or "@", so prefixed ids and emails never collide with an agent
	// name.
	Name  string
	Agent bool
}

func (a Author) Label() string {
	if a.Agent {
		return "agent:" + a.Name
	}
	return a.Name
}

// Reserved by agentstore; no agent may take this name.
const AuditorAgent = "auditor"

func (a Author) IsAuditor() bool { return a.Agent && a.Name == AuditorAgent }

const DevModeAuthorName = "user"

var ErrUnsigned error = apperr.Unauthenticated("auth.errors.unsigned")

func AuthorFromRequest(r *http.Request) (Author, error) {
	if r == nil {
		return Author{}, ErrUnsigned
	}
	return AuthorFromContext(r.Context())
}

// Run keys must be checked first: read as an operator key, a run key would sign as a person. A run
// key without an agent is refused.
func AuthorFromContext(ctx context.Context) (Author, error) {
	if IsAPIKeyAuthed(ctx) {
		if scope := APIKeyRunScope(ctx); scope.IsRun() {
			if scope.Agent == "" {
				return Author{}, ErrUnsigned
			}
			return Author{Name: scope.Agent, Agent: true}, nil
		}
		// Prefixed so an operator key named "manager" or after a person never reads as that agent
		// or person.
		if name := APIKeyName(ctx); name != "" {
			return Author{Name: "key:" + name}, nil
		}
		return Author{Name: "key:" + APIKeyID(ctx)}, nil
	}
	if c := ClaimsFromContext(ctx); c != nil {
		if c.Email != "" {
			return Author{Name: c.Email}, nil
		}
		return Author{Name: "user:" + c.UserID}, nil
	}
	if DevSession(ctx) {
		return Author{Name: DevModeAuthorName}, nil
	}
	return Author{}, ErrUnsigned
}

// Stable id of the credential, for ownership: unlike Author it survives an email change or a key
// rename. A run key owns nothing; it reaches a chat only through its RunScope.
func OwnerFromContext(ctx context.Context) string {
	if IsAPIKeyAuthed(ctx) {
		if APIKeyRunScope(ctx).IsRun() || APIKeyID(ctx) == "" {
			return ""
		}
		return "key:" + APIKeyID(ctx)
	}
	if c := ClaimsFromContext(ctx); c != nil {
		if c.UserID == "" {
			return ""
		}
		return "user:" + c.UserID
	}
	if DevSession(ctx) {
		return "dev:" + DevModeAuthorName
	}
	return ""
}

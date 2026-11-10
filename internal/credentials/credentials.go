package credentials

import (
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

type State string

const (
	// Not a failure: fine-grained GitHub PATs report no scopes, and blocking on unknown would break
	// working setups.
	StateUnknown State = "unknown"
	StateValid   State = "valid"
	// StateExpiring and StateExpired are derived at read time (deriveState), never stored.
	StateExpiring State = "expiring"
	StateExpired  State = "expired"
	StateInvalid  State = "invalid"
)

// Stored as a code, not a sentence: the page and CLI word it in the reader's language.
const IntrospectUnreachable = "provider_unreachable"

// A week: enough to rotate a key through a normal change process.
const ExpiryWarning = 7 * 24 * time.Hour

// Never holds the secret; Store.Payload is the single decrypt path.
type Credential struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Kind        string `json:"kind"`

	// Must be safe to display: an access key id or service-account email, never a secret half.
	Hint     string `json:"hint,omitempty"`
	Identity string `json:"identity,omitempty"`

	State           State     `json:"state"`
	Capabilities    []string  `json:"capabilities,omitempty"`
	IntrospectError string    `json:"introspect_error,omitempty"`
	ValidatedAt     time.Time `json:"validated_at,omitzero"`
	ExpiresAt       time.Time `json:"expires_at,omitzero"`

	Disabled bool `json:"disabled,omitempty"`

	Grants []Grant `json:"grants,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	CreatedBy string    `json:"created_by,omitempty"`
}

// On update an empty Payload keeps the stored secret.
type Input struct {
	Name     string
	Provider string
	Kind     string
	Payload  map[string]string
}

// Closed set: Reach.Covers switches on it, and an unknown value would be stored and silently never
// match.
type SubjectType string

const (
	SubjectProject     SubjectType = "project"
	SubjectEnvironment SubjectType = "environment"
	SubjectWorkspace   SubjectType = "workspace"
)

type Grant struct {
	SubjectType SubjectType `json:"subject_type"`
	SubjectID   string      `json:"subject_id,omitempty"`
}

func (g Grant) Valid() error {
	switch g.SubjectType {
	case SubjectProject, SubjectEnvironment:
		if strings.TrimSpace(g.SubjectID) == "" {
			return apperr.Invalid("credentials.errors.grant_needs_subject", "type", g.SubjectType)
		}
		return nil
	case SubjectWorkspace:
		if strings.TrimSpace(g.SubjectID) != "" {
			return apperr.Invalid("credentials.errors.workspace_grant_has_subject")
		}
		return nil
	default:
		return apperr.Invalid("credentials.errors.unknown_subject_type", "type", g.SubjectType)
	}
}

type Reach struct {
	WorkspaceID    string
	ProjectIDs     []string
	EnvironmentIDs []string
}

func (r Reach) Covers(g Grant) bool {
	switch g.SubjectType {
	case SubjectWorkspace:
		return true // Safe because the caller is already confined to one workspace upstream.
	case SubjectProject:
		return contains(r.ProjectIDs, g.SubjectID)
	case SubjectEnvironment:
		return contains(r.EnvironmentIDs, g.SubjectID)
	default:
		return false
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// Only disable overrides are stored; no row means enabled. Grant, capability and stage allowlist do
// the containment, not this table.
type ToolPolicy struct {
	Server  string `json:"server"`
	Tool    string `json:"tool"`
	Enabled bool   `json:"enabled"`
}

func PolicyKey(server, tool string) string { return server + "/" + tool }

func deriveState(stored State, expiresAt, now time.Time) State {
	if stored == StateInvalid {
		return StateInvalid
	}
	if !expiresAt.IsZero() {
		switch {
		case !expiresAt.After(now):
			return StateExpired
		case expiresAt.Sub(now) <= ExpiryWarning:
			return StateExpiring
		}
	}
	return stored
}

// Expiring is usable on purpose: the warning prompts rotation, it must not break running work
// early.
func (c *Credential) Usable() bool {
	if c.Disabled {
		return false
	}
	switch c.State {
	case StateValid, StateExpiring, StateUnknown:
		return true
	default:
		return false
	}
}

// No discovered capabilities answers true for everything: fine-grained GitHub PATs and
// non-introspectable providers would otherwise break on upgrade.
// Grant, policy and stage allowlist still apply.
func (c *Credential) HasCapability(perm string) bool {
	if len(c.Capabilities) == 0 {
		return true
	}
	return contains(c.Capabilities, perm)
}

func (c *Credential) MissingCapabilities(want []string) []string {
	if len(c.Capabilities) == 0 {
		return nil
	}
	var missing []string
	for _, w := range want {
		if !contains(c.Capabilities, w) {
			missing = append(missing, w)
		}
	}
	return missing
}

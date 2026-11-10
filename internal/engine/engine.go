package engine

import (
	"context"
	"io"
	"iter"
	"os"
	"strings"
	"time"
)

func MatchKeyKind(kinds []KeyKind, key string) (KeyKind, bool) {
	for _, k := range kinds {
		if k.Prefix == "" {
			continue
		}
		if strings.HasPrefix(key, k.Prefix) {
			return k, true
		}
	}
	return KeyKind{}, false
}

func KeyKindByID(kinds []KeyKind, id string) (KeyKind, bool) {
	for _, k := range kinds {
		if k.ID == id {
			return k, true
		}
	}
	return KeyKind{}, false
}

type Engine interface {
	// Must stay stable: agents pin to it.
	Name() string

	// Malformed lines are skipped: one bad line must not break a session's accounting, and tail
	// reads start mid-line.
	ParseStream(r io.Reader) iter.Seq[Event]

	AuthEnv(ctx context.Context) []string

	Capabilities() Capabilities
}

type localizedEngine interface {
	CapabilitiesIn(ctx context.Context) Capabilities
}

func CapabilitiesIn(ctx context.Context, e Engine) Capabilities {
	if le, ok := e.(localizedEngine); ok {
		return le.CapabilitiesIn(ctx)
	}
	return e.Capabilities()
}

type Capabilities struct {
	ResumableSessions bool

	StreamJSON bool

	StreamingInput bool

	EffortFlag bool

	MCP bool

	Skills bool

	Models []string

	// Must match the adapter's own fallback in images/snippets/engines/<name>.sh.
	DefaultModel string

	TurnBudget bool

	Pricing []ModelPrice

	Label string

	Provider string

	APIProviders []string

	ProviderEnv string

	KeyKinds []KeyKind

	BaseURLEnv string

	OrgIDEnv string

	AuthorizationEnv string

	CloudProviders []string

	// Must not return the provider key vars: the dispatcher clears those before applying it.
	CloudEnv func(CloudConfig) map[string]string

	CloudGatewayEnv func(cfg CloudConfig, runToken string) map[string]string

	// Must set only the variables its Federated KeyKind lists in EnvVars.
	FederationEnv func(cfg FederationConfig, identityToken string) map[string]string

	FederationAudience string

	NonessentialTrafficEnv map[string]string

	PersonalLogin *PersonalLogin
}

// Field names mirror usage.Pricing, which is seeded from these rows.
type ModelPrice struct {
	Model              string
	InPerMTok          float64
	OutPerMTok         float64
	CacheReadPerMTok   float64
	CacheCreatePerMTok float64

	// Only for names distinctive enough not to appear inside another vendor's model ids.
	Family bool
}

type KeyKind struct {
	// Stable: sent over the wire in PUT /llm-key.
	ID string

	Label string

	Prefix string

	EnvVars []string

	Clears []string

	// A Claude Pro/Max OAuth token is bound to the Claude Code client, so the gateway cannot
	// present it.
	NoGateway bool

	OwnerOnly bool

	Federated bool

	// Names an operator env var that must be on ("1", "true" or "yes") for this kind to be stored
	// or used when agents run in Kubernetes. Empty: usable everywhere.
	KubeOptIn string
}

// Usable reports whether this kind may be stored or used under the daemon's agent runtime.
func (k KeyKind) Usable() bool {
	if k.KubeOptIn == "" || !strings.EqualFold(strings.TrimSpace(os.Getenv("OCTOPUS_AGENT_RUNTIME")), "kubernetes") {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(k.KubeOptIn))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

type EventKind int

const (
	EventUnknown EventKind = iota

	EventInit

	EventAssistant

	EventRateLimit

	EventResult

	EventToolUse

	EventToolResult

	EventUserMessage
)

type Event struct {
	Kind EventKind

	Model string

	TS time.Time

	// Claude Code prints one assistant line per content block, all with the same id and usage:
	// count each id once.
	MessageID       string
	ParentToolUseID string
	OutputBytes     int

	SessionID string

	Usage *Usage

	RateLimit *RateLimit

	SessionMode string

	Result         string
	IsError        bool
	Outcome        string
	DurationMs     int64
	NumTurns       int
	CostUSD        float64
	StopReason     string
	APIError       bool
	APIErrorStatus int

	ToolName  string
	ToolInput map[string]any
	ToolUseID string

	ResultBytes int
	ResultTail  string
	Synthetic   bool

	ModelUsage []ModelUsage
}

const ResultTailBytes = 2048

type ModelUsage struct {
	Model         string
	InputTokens   int
	OutputTokens  int
	CacheRead     int
	CacheCreation int
	CostUSD       float64
}

type Usage struct {
	InputTokens     int
	OutputTokens    int
	CacheRead       int
	CacheCreation   int
	CacheCreation1h int
	ServiceTier     string
}

func (u Usage) PromptSize() int {
	return u.InputTokens + u.CacheRead + u.CacheCreation
}

type RateLimit struct {
	Status        string
	ResetsAt      int64
	RateLimitType string
	Utilization   float64
	IsOverage     bool
}

func LineTimestamp(prefix string) time.Time {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return time.Time{}
	}
	if i := strings.IndexByte(prefix, ' '); i >= 0 {
		prefix = prefix[:i]
	}
	ts, err := time.Parse(time.RFC3339Nano, prefix)
	if err != nil {
		return time.Time{}
	}
	return ts.UTC()
}

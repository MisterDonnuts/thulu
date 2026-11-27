package automation

import (
	"context"
	"time"
)

// Ports must scope every call to the workspace id they are given; the engine never assumes the
// caller already scoped it.

type NewTask struct {
	WorkspaceID      string
	Title            string
	Body             string
	BoardID          string
	TypeID           string
	Stage            string
	ProjectIDs       []string
	StageAssignments map[string]string
	Tags             []string
	Priority         string
	AutomationID     string
	ChainDepth       int
}

type TaskSummary struct {
	ID        string            `json:"id"`
	Title     string            `json:"title"`
	Stage     string            `json:"stage"`
	Status    string            `json:"status"`
	BoardID   string            `json:"board_id"`
	Tags      []string          `json:"tags"`
	PRURLs    map[string]string `json:"pr_urls,omitempty"`
	UpdatedAt time.Time         `json:"updated_at"`
	Open      bool              `json:"open"`
}

type TaskQuery struct {
	WorkspaceID string
	BoardID     string
	Stage       string
	Tag         string
	ProjectID   string
	WithPR      bool
	Open        bool
	StaleFor    time.Duration
	Limit       int
}

type MoveRequest struct {
	WorkspaceID  string
	TaskID       string
	Stage        string
	BoardID      string
	Note         string
	AutomationID string
	ChainDepth   int
	// Called before a move that wakes the destination stage's agent, with its name; an error
	// refuses the move. The engine checks max_agent_runs_per_day here.
	BeforeAgent func(agent string) error
}

// A gated move is an answer, not an error: the task stays where it was and Approvals says why.
type MoveResult struct {
	TaskID    string   `json:"task_id"`
	Title     string   `json:"title"`
	From      string   `json:"from"`
	To        string   `json:"to"`
	Moved     bool     `json:"moved"`
	Gated     bool     `json:"gated"`
	Approvals []string `json:"approvals,omitempty"`
	// The agent the move woke in the destination stage, if any.
	Agent string `json:"agent,omitempty"`
}

type TaskOps interface {
	CreateTask(ctx context.Context, req NewTask) (taskID, agent string, err error)
	PlannedAgent(ctx context.Context, req NewTask) (agent string, err error)
	// Implementations must refuse a task outside workspaceID: recipes name tasks by id, and a
	// foreign id must not read or write another tenant's board.
	CommentTask(ctx context.Context, workspaceID, taskID, body string) (title string, err error)
	StageAgent(ctx context.Context, workspaceID, taskID string) (agent string, answers bool, err error)
	MentionAgent(ctx context.Context, workspaceID, taskID, agent, body string) (title string, err error)
	// Same workspace rule as CommentTask.
	MoveTask(ctx context.Context, req MoveRequest) (MoveResult, error)
	TaskOpen(ctx context.Context, taskID string) (open bool, err error)
	QueryTasks(ctx context.Context, q TaskQuery) ([]TaskSummary, error)
}

// apiKey is the run's credential; forward it only to servers that act on Thulu for the caller.
type MCPCaller interface {
	CallTool(ctx context.Context, server, tool string, args map[string]any, apiKey string) (result map[string]any, err error)
}

type ScriptRequest struct {
	WorkspaceID string
	Script      string
	Env         map[string]string
	Timeout     time.Duration
}

type ScriptResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

type ScriptRunner interface {
	RunScript(ctx context.Context, owner string, req ScriptRequest) (ScriptResult, error)
	ReleaseSandbox(ctx context.Context, owner string) error
}

// A missing or foreign-workspace environment must be an error, never "no data", or a typo'd id
// reads as a quiet system forever.
type MetricReader interface {
	Aggregate(ctx context.Context, workspaceID, envID, metric string, window time.Duration, agg string) (value float64, ok bool, err error)
}

type CredentialResolver interface {
	Secret(ctx context.Context, workspaceID, name string) (string, error)
}

type KeyMinter interface {
	MintAutomationKey(ctx context.Context, workspaceID, automationID, runID string, ttl time.Duration) (rawKey, keyID string, err error)
	RevokeAPIKey(ctx context.Context, keyID string) error
}

// DeployWatchObserver reads a polled deploy source once for the watch, records what it saw and says
// whether it changed since the previous observation. The first observation is a baseline and never
// counts as a change. Same workspace rule as TaskOps.CommentTask.
type DeployWatchObserver interface {
	Observe(ctx context.Context, workspaceID, watchID string) (map[string]any, error)
}

// Any port may be nil (e.g. a CE build without a sandbox); steps needing it must fail, not panic.
type Deps struct {
	Tasks   TaskOps
	MCP     MCPCaller
	Scripts ScriptRunner
	Metrics MetricReader
	Creds   CredentialResolver
	Keys    KeyMinter
	Watches DeployWatchObserver
}

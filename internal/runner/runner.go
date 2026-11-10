package runner

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/engine"
)

// The daemon refuses a runner on another version, so an old runner fails loudly instead of
// misreading a job.
// Bump on any incompatible wire change.
const ProtocolVersion = "1"

const ProtocolHeader = "X-Octopus-Runner-Protocol"

const (
	RegistrationPrefix = "ornreg_"
	CredentialPrefix   = "orn_"
)

// The routing launcher and the agent handlers tell a runner job from a Docker container or a pod by
// this prefix.
const ContainerPrefix = "runner-"

func IsRunnerRun(runID string) bool { return strings.HasPrefix(runID, ContainerPrefix) }

func JobIDFromRun(runID string) string { return strings.TrimPrefix(runID, ContainerPrefix) }

func RunIDForJob(jobID string) string { return ContainerPrefix + jobID }

const (
	StateQueued   = "queued"
	StateRunning  = "running"
	StateFinished = "finished"
	StateLost     = "lost"
)

const (
	ExitLost     = 125
	ExitCanceled = 130
)

// The heartbeat is a quarter of the lease so two can be lost to a bad network before the job is.
const (
	DefaultLease        = 60 * time.Second
	DefaultHeartbeat    = 15 * time.Second
	DefaultClaimTimeout = 10 * time.Minute
	ClaimWait           = 25 * time.Second
)

// The scripts read this value (engine_auth_check).
const SourceRunnerLogin = engine.LLMSourceRunnerLogin

// Set by the daemon (the MCP proxy reads it back from the run's grant) and again by the runner for
// the scripts.
const HostRunEnv = "OCTOPUS_HOST_RUN"

type Runner struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspace_id"`
	Name        string     `json:"name"`
	Hostname    string     `json:"hostname,omitempty"`
	Version     string     `json:"version,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	CreatedBy   string     `json:"created_by,omitempty"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	Online      bool       `json:"online"`
}

type Job struct {
	ID          string
	RunnerID    string
	WorkspaceID string
	TaskID      string
	ProjectID   string
	InstanceID  string
	AgentName   string
	AgentType   string
	Engine      string
	Stage       string
	LaunchKind  string
	State       string
	Cancel      bool
	ExitCode    *int
	Result      FinishRequest
	CreatedAt   time.Time
	ClaimedAt   *time.Time
	FinishedAt  *time.Time
	Reaped      bool
	Spec        JobSpec
}

func (j *Job) RunID() string { return RunIDForJob(j.ID) }

func (j *Job) Done() bool { return j.State == StateFinished || j.State == StateLost }

type JobSpec struct {
	ID         string            `json:"id"`
	InstanceID string            `json:"instance_id"`
	TaskID     string            `json:"task_id"`
	Stage      string            `json:"stage"`
	AgentName  string            `json:"agent_name"`
	LaunchKind string            `json:"launch_kind,omitempty"`
	ReadOnly   bool              `json:"read_only,omitempty"`
	Env        map[string]string `json:"env"`
	Projects   []JobProject      `json:"projects"`
}

type JobProject struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	Branch        string `json:"branch"`
	RepoURL       string `json:"repo_url,omitempty"`
	// The daemon fetches the runner's work from origin into its own clone, so the branch is pushed
	// after every clean run, not only push_branch stages.
	Push bool `json:"push,omitempty"`
}

type RegisterRequest struct {
	Token    string `json:"token"`
	Hostname string `json:"hostname,omitempty"`
	Version  string `json:"version,omitempty"`
}

type RegisterResponse struct {
	RunnerID    string `json:"runner_id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Credential  string `json:"credential"`
}

type ClaimRequest struct {
	Projects []string `json:"projects,omitempty"`
	Version  string   `json:"version,omitempty"`
}

type ClaimResponse struct {
	Job              *JobSpec `json:"job,omitempty"`
	LeaseSeconds     int      `json:"lease_seconds,omitempty"`
	HeartbeatSeconds int      `json:"heartbeat_seconds,omitempty"`
}

type HeartbeatResponse struct {
	Cancel       bool `json:"cancel"`
	LeaseSeconds int  `json:"lease_seconds"`
}

type FinishRequest struct {
	ExitCode int            `json:"exit_code"`
	Pushed   []PushedBranch `json:"pushed,omitempty"`
	Error    string         `json:"error,omitempty"`
	Warnings []string       `json:"warnings,omitempty"`
}

type PushedBranch struct {
	ProjectID string `json:"project_id"`
	Branch    string `json:"branch"`
	Commit    string `json:"commit,omitempty"`
	Remote    string `json:"remote,omitempty"`
}

func newSecret(prefix string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func hashSecret(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

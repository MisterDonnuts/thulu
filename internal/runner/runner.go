package runner

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/engine"
)

// ProtocolVersion is what this runner speaks. The daemon accepts every version from ProtocolFolders
// up and hands each runner only the jobs it understands (JobSpec.MinProtocol), so an old runner keeps
// serving folder projects. Bump on any wire change an older runner would misread.
const (
	ProtocolFolders = 1
	// Projects fetched from the daemon (JobProject.Source).
	ProtocolDaemonCode = 2
	// Mid-run messages (POST jobs/{id}/steer).
	ProtocolSteer = 2
	// Chat turns (LaunchKindChatTurn) and ClaimRequest.Kinds.
	ProtocolChatTurn = 2
	ProtocolVersion  = 2
)

// AcceptedProtocol reads the version a runner declares; zero when the daemon cannot serve it.
func AcceptedProtocol(header string) int {
	v, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || v < ProtocolFolders || v > ProtocolVersion {
		return 0
	}
	return v
}

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

// RunnerIDEnv carries the machine the dispatcher placed a run on; the launcher routes by it alone.
const RunnerIDEnv = "OCTOPUS_RUNNER_ID"

// SteerEnabledEnv is "1" when the dispatcher armed the run for mid-run messages.
const SteerEnabledEnv = "STEER_ENABLED"

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
	Protocol    int        `json:"protocol"`
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

// ChatTurn reads the column, not the spec: a lost or canceled job's spec is wiped.
func (j *Job) ChatTurn() bool { return j.LaunchKind == LaunchKindChatTurn }

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
	// A chat turn's chat: the machine keeps one folder per chat so the engine resumes its session.
	ChatID string `json:"chat_id,omitempty"`
	// What the turn's script reads as its message. Like Env, it leaves the database with the claim.
	Stdin string `json:"stdin,omitempty"`
	// Where the runner pushes its work on the projects it fetched from the daemon: the daemon has the
	// task branch checked out, so a push there would be refused.
	ResultRef string `json:"result_ref,omitempty"`
}

// A job whose every project carries its folder, or comes from the daemon, needs nothing from the
// runner's local config, so any runner of the task's machine can take it.
func (s JobSpec) SelfContained() bool {
	if len(s.Projects) == 0 {
		return false
	}
	for _, p := range s.Projects {
		if p.Path == "" && !p.FromDaemon() {
			return false
		}
	}
	return true
}

// MinProtocol is the oldest runner protocol that understands the job. Claim hands a job only to
// runners at or above it.
func (s JobSpec) MinProtocol() int {
	need := ProtocolFolders
	if s.Env[SteerEnabledEnv] == "1" {
		need = ProtocolSteer
	}
	if s.ChatTurn() {
		need = max(need, ProtocolChatTurn)
	}
	for _, p := range s.Projects {
		if p.FromDaemon() {
			return max(need, ProtocolDaemonCode)
		}
	}
	return need
}

// LaunchKindChatTurn is one turn of a chat: the machine runs chat-turn.sh in the chat's own folder
// and sends back what the engine printed, as the turn's outputs (ChatTurnOutput).
const LaunchKindChatTurn = "chat_turn"

// ChatTurnOutput is the file, inside a chat turn's outputs, that holds the engine's stdout.
const ChatTurnOutput = "turn.jsonl"

func (s JobSpec) ChatTurn() bool { return s.LaunchKind == LaunchKindChatTurn }

// SourceDaemon marks a project the runner fetches from the daemon's /git/ instead of a folder of its
// machine.
const SourceDaemon = "daemon"

type JobProject struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	Branch        string `json:"branch"`
	RepoURL       string `json:"repo_url,omitempty"`
	// The folder recorded in the project. Empty for projects mapped only in the runner's own
	// config, which a runner before folders in the dashboard still serves.
	Path string `json:"path,omitempty"`
	// The daemon fetches the runner's work from origin into its own clone, so the branch is pushed
	// after every clean run, not only push_branch stages.
	Push bool `json:"push,omitempty"`
	// Empty for a folder of the machine; SourceDaemon for a project kept by Thulu.
	Source string `json:"source,omitempty"`
}

func (p JobProject) FromDaemon() bool { return p.Source == SourceDaemon }

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
	// The kinds of job the runner has a free slot for (ClaimRun, ClaimChat); empty takes any.
	Kinds []string `json:"kinds,omitempty"`
}

// A runner keeps chat turns in a pool of their own, so a turn never waits behind a long run.
const (
	ClaimRun  = "run"
	ClaimChat = "chat"
)

type ClaimResponse struct {
	Job              *JobSpec `json:"job,omitempty"`
	LeaseSeconds     int      `json:"lease_seconds,omitempty"`
	HeartbeatSeconds int      `json:"heartbeat_seconds,omitempty"`
}

type HeartbeatResponse struct {
	Cancel       bool `json:"cancel"`
	LeaseSeconds int  `json:"lease_seconds"`
}

// The runner polls for the messages sent to a running job and reports, on its next poll, what
// happened to each one it took.
type SteerRequest struct {
	Acks []SteerAck `json:"acks,omitempty"`
}

type SteerAck struct {
	ID     string `json:"id"`
	Result string `json:"result"`
}

type SteerResponse struct {
	Messages []SteerMessage `json:"messages,omitempty"`
	// The job no longer runs: stop polling.
	Done bool `json:"done,omitempty"`
}

// Line is one steer-driver.js queue line, opaque to the runner.
type SteerMessage struct {
	ID   string `json:"id"`
	Line string `json:"line"`
}

// What became of a steer line on the machine; the same outcomes as the Docker launcher's exec.
const (
	SteerAppended = "appended"
	// The agent stopped reading (or the job ended): the message waits for a continuation.
	SteerSealed = "sealed"
	SteerLocked = "locked"
	SteerFailed = "error"
)

// MaxSteerLine caps one queued message.
const MaxSteerLine = 256 << 10

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

// NewJobID names a job before it is queued, for a caller that must wait on it from the start.
func NewJobID() string { return newID() }

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func hashSecret(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// What a runner reports about its machine: the folders its owner allowed, the git repositories
// found inside them, and the projects its local config already maps (adopted into the dashboard
// once). The daemon never reads the disk; it shows this list and the runner re-checks every path.
type Inventory struct {
	Roots     []string          `json:"roots"`
	Repos     []InventoryRepo   `json:"repos"`
	Mapped    map[string]string `json:"mapped,omitempty"`
	Truncated bool              `json:"truncated,omitempty"`
	ScannedAt time.Time         `json:"scanned_at"`
	Engines   []EngineState     `json:"engines,omitempty"`
}

// EngineState is what the machine says about one engine's CLI: whether it is on the PATH and whose
// login it holds.
type EngineState struct {
	Engine    string    `json:"engine"`
	Installed bool      `json:"installed"`
	Version   string    `json:"version,omitempty"`
	LoggedIn  bool      `json:"logged_in"`
	Email     string    `json:"email,omitempty"`
	Org       string    `json:"org,omitempty"`
	Method    string    `json:"method,omitempty"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// Engine is the state reported for name, or nil when the machine never reported it.
func (inv *Inventory) Engine(name string) *EngineState {
	if inv == nil {
		return nil
	}
	for i := range inv.Engines {
		if inv.Engines[i].Engine == name {
			return &inv.Engines[i]
		}
	}
	return nil
}

type InventoryRepo struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Branch string `json:"branch,omitempty"`
	// Credentials stripped by the runner before it reports it.
	Origin string `json:"origin,omitempty"`
}

// What `thulu add` sends from inside a repository on the machine. The runner reports its folders
// first, so the daemon checks the path against the same list the dashboard uses.
type AddProjectRequest struct {
	Path string `json:"path"`
	Name string `json:"name,omitempty"`
}

type AddProjectResponse struct {
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
	// False when the folder already was a project on this runner: running the command twice is safe.
	Created bool `json:"created"`
}

// Limits on what a runner may report, so a home folder full of checkouts stays a list a person can
// read and a body the daemon can hold.
const (
	MaxInventoryRepos = 500
	MaxInventoryRoots = 50
)

// Within reports whether path is one of roots or inside one. A relative path is never inside;
// the runner resolves symlinks before it calls this.
func Within(roots []string, path string) bool {
	// Cleaned first, so "/code/../secrets" is judged as "/secrets" and not by its prefix.
	if !filepath.IsAbs(path) {
		return false
	}
	path = filepath.Clean(path)
	for _, r := range roots {
		if r == "" || !filepath.IsAbs(r) {
			continue
		}
		r = filepath.Clean(r)
		if path == r || strings.HasPrefix(path, strings.TrimSuffix(r, "/")+"/") {
			return true
		}
	}
	return false
}

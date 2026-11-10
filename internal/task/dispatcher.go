package task

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/browserrun"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/runner"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type KeyMinter interface {
	MintRunKey(ctx context.Context, scope auth.RunScope, workspaceID string, ttl time.Duration) (rawKey, keyID string, err error)
	RevokeAPIKey(ctx context.Context, keyID string) error
}

// Caps a run key's life if the revoke-on-exit callback is lost (daemon crash mid-run).
const runKeyTTL = 24 * time.Hour

type tokenLookup interface {
	GetGithubToken(ctx context.Context, projectID string) (*githubapp.Token, error)
}

type SandboxReleaser interface {
	Release(ctx context.Context, taskID string) error
}

type AgentLauncher interface {
	LaunchForStage(
		ctx context.Context,
		t *Task,
		stage Stage,
		agentType *types.AgentType,
		env map[string]string,
		worktreePaths map[string]string,
		stagePaths map[string]string,
		workspaceReadOnly bool,
	) (containerID, instanceID, outputDir string, err error)

	OnExit(ctx context.Context, containerID string, cb func(exitCode int, outputDir, worktreePath string))

	FindAgentType(workspaceID, name string) *types.AgentType

	ContainerAlive(containerID string) bool

	ScanExitedTaskAgents(ctx context.Context) ([]ExitedTaskAgent, error)
}

type ExitedTaskAgent struct {
	ContainerID string
	InstanceID  string
	AgentType   string
	OutputDir   string
	ExitCode    int
	LaunchKind  LaunchKind
}

type Dispatcher struct {
	store    *Store
	launcher AgentLauncher
	projects *project.Store
	secrets  tokenLookup
	sandbox  SandboxReleaser
	keys     KeyMinter
	llmKeys  LLMKeyResolver
	onStatus func(t *Task, status RunStatus)
	onStage  func(t *Task, from Stage)
	slots    *agentSlots
	boards   stagepkg.Boards

	// Separate from slots on purpose: conversations must not queue behind stage runs,
	// and a burst of mentions must not take the capacity stage runs wait for.
	convSlots      *agentSlots
	personalLogins PersonalLogins
	mentions       *mentionLane
	chatMentions   ChatMentionRunner
	agentComments  AgentCommentHook

	llmResolver  LLMResolver
	llmProfTouch LLMProfileTouch
	llmAudit     LLMResolveAudit
	llmRefusal   LLMProfileRefusal
	tokenOpt     TokenOptimizerWorker
	llm          llmRouting

	agentBaseURL string

	agentPushesResults bool

	// Shared with the chat runtime (WithMCPForwarder) so /mcps registrations reach both.
	mcpFwd *mcp.Forwarder

	browser browserrun.Config

	branchPusher BranchPusher

	credGate CredentialGate

	steerable sync.Map

	conv *convLane

	quota       RunQuota
	runDeadline func(workspaceID string) time.Duration
	workloadID  WorkloadIdentity

	room         *roomLine
	queueMinutes func() int
	slotWaits    sync.Map
	stoppedBy    sync.Map

	// Serializes Reconcile (startup and every K8s janitor sweep): concurrent passes would replay
	// the same orphan twice.
	reconcileMu sync.Mutex

	sessionsDir string
	// A profile can route a run to another engine than its label; the session id must be parsed
	// with the engine the run actually got.
	runEngines  sync.Map
	runProfiles sync.Map
}

type CredentialGate func(ctx context.Context, t *Task) ([]credentials.Fault, error)

type BranchPusher func(ctx context.Context, t *Task, projectID string) (bool, error)

// Covers fetch, rebase onto the default branch and push, possibly after waiting for a sandbox to be
// provisioned.
const branchPushTimeout = 15 * time.Minute

// host.docker.internal works on Docker Desktop and Colima; on Linux with network_mode: host it
// resolves to the host gateway.
const defaultAgentBaseURL = "http://host.docker.internal:8585"

func (d *Dispatcher) WithTaskKeys(m KeyMinter) *Dispatcher {
	d.keys = m
	return d
}

func (d *Dispatcher) WithLLMKeys(r LLMKeyResolver) *Dispatcher {
	d.llmKeys = r
	return d
}

func (d *Dispatcher) WithTokenLookup(tl tokenLookup) *Dispatcher {
	if tl != nil {
		d.secrets = tl
	}
	return d
}

func (d *Dispatcher) WithLLMRefusals(fn LLMProfileRefusal) *Dispatcher {
	d.llmRefusal = fn
	return d
}

func (d *Dispatcher) WithLLMResolver(r LLMResolver, touch LLMProfileTouch, audit LLMResolveAudit) *Dispatcher {
	d.llmResolver = r
	d.llmProfTouch = touch
	d.llmAudit = audit
	return d
}

func NewDispatcher(store *Store, launcher AgentLauncher, projects *project.Store, secretsLookup tokenLookup, sandboxReleaser SandboxReleaser) *Dispatcher { //nolint:lll // constructor signature
	return &Dispatcher{
		store: store, launcher: launcher, projects: projects,
		secrets: secretsLookup, sandbox: sandboxReleaser,
		slots:        newAgentSlots(nil),
		convSlots:    newAgentSlots(nil),
		mentions:     newMentionLane(),
		conv:         newConvLane(),
		room:         newRoomLine(),
		boards:       stagepkg.StaticBoards(),
		agentBaseURL: defaultAgentBaseURL,
		mcpFwd:       mcp.NewForwarder(),
	}
}

func (d *Dispatcher) WithStages(r stagepkg.Registry) *Dispatcher {
	if r != nil {
		d.boards = stagepkg.SingleBoard(r)
	}
	return d
}

func (d *Dispatcher) WithBoards(b stagepkg.Boards) *Dispatcher {
	if b != nil {
		d.boards = b
	}
	return d
}

// Ask every flow question through stagesFor: tasks on different boards, or of different types,
// disagree on what a stage means.
func (d *Dispatcher) stagesFor(t *Task) stagepkg.Registry {
	if t == nil {
		return d.boards.Board(d.boards.DefaultBoardID())
	}
	return d.boards.BoardFor(t.BoardID, t.TypeID)
}

func (d *Dispatcher) WithMCPForwarder(f *mcp.Forwarder) *Dispatcher {
	if f != nil {
		d.mcpFwd = f
	}
	return d
}

func (d *Dispatcher) WithAgentBaseURL(u string) *Dispatcher {
	if u != "" {
		d.agentBaseURL = u
	}
	return d
}

func (d *Dispatcher) AgentBaseURL() string { return d.agentBaseURL }

func (d *Dispatcher) WithMCPURLs(urls map[string]string) *Dispatcher {
	d.mcpFwd.SetMany(urls)
	return d
}

func (d *Dispatcher) SetMCPURL(key, url string) { d.mcpFwd.Set(key, url) }

func (d *Dispatcher) SetMCPForwardsCredential(name string, on bool) {
	d.mcpFwd.SetForwardsCredential(name, on)
}

func (d *Dispatcher) SetMCPTransport(name, transport string) {
	d.mcpFwd.SetTransport(name, transport)
}

func (d *Dispatcher) DeleteMCPURL(key string) { d.mcpFwd.Delete(key) }

func (d *Dispatcher) MCPURLs() map[string]string { return d.mcpFwd.Snapshot() }

// A runner has no per-run browser container, and a per-run daemon keeps runs apart on purpose, so
// runner runs get no browser rather than the shared one.
func (d *Dispatcher) applyMCPURLs(ctx context.Context, env map[string]string, onRunner bool) {
	if onRunner {
		env[runner.HostRunEnv] = "1"
		if d.browser.PerRun() && browserrun.Granted(env["MCP_TOOLS"]) {
			env["MCP_TOOLS"] = mcp.WithoutServer(env["MCP_TOOLS"], browserrun.ServerName)
			log.Printf("[task] %s", i18n.Log("task.log.runner_no_per_run_browser", "task", env["TASK_ID"]))
		}
	}
	mcp.ApplyLaunchEnv(ctx, env, d.mcpFwd, d.browser)
}

func (d *Dispatcher) WithBrowser(cfg browserrun.Config) *Dispatcher {
	d.browser = cfg
	return d
}

func (d *Dispatcher) WithAgentPushesResults() *Dispatcher {
	d.agentPushesResults = true
	return d
}

func (d *Dispatcher) WithBranchPusher(p BranchPusher) *Dispatcher {
	d.branchPusher = p
	return d
}

func resultRefFor(sandboxRootID string) string { return project.ResultBranchName(sandboxRootID) }

func (d *Dispatcher) WithCredentialGate(g CredentialGate) *Dispatcher {
	if g != nil {
		d.credGate = g
	}
	return d
}

func (d *Dispatcher) WithAgentCap(capFn func() int) *Dispatcher {
	d.slots.setCap(capFn)
	return d
}

func (d *Dispatcher) WithConversationCap(capFn func() int) *Dispatcher {
	d.convSlots.setCap(capFn)
	return d
}

func (d *Dispatcher) WithWorkspaceConcurrency(fn func(workspaceID string) int) *Dispatcher {
	d.slots.withWorkspaceCap(fn)
	d.convSlots.withWorkspaceCap(fn)
	return d
}

func (d *Dispatcher) HandleStageEntered(ctx context.Context, t *Task, prev Stage) {
	if t == nil {
		return
	}
	d.room.drop(t.ID)
	d.dropSlotWait(t.ID)

	stages := d.stagesFor(t)
	info, _ := stages.Get(t.Stage)
	prevInfo, _ := stages.Get(prev)

	if info.Terminal && !prevInfo.Terminal {
		d.notifyDependentsUnblocked(ctx, t.ID)
		d.cleanupDoneTask(ctx, t)
		return
	}

	if !info.Initial && len(t.ProjectIDs) > 0 {
		t = d.ensureWorktrees(ctx, t)
	}

	// A subtask shares the root's branch and PR; only the root opens the group's PR.
	if info.OpenPR && !t.IsSubtask() {
		label := info.Label
		if label == "" {
			label = string(t.Stage)
		}
		t = d.ensurePullRequests(ctx, t, label)
	}

	agentName := t.AssignedAgent(t.Stage)
	stageName := d.stageNote(t, t.Stage)
	if agentName == "" {
		if d.missingAgent(ctx, t, info) {
			d.stageWithoutAgent(ctx, t, "", CauseNoAgent, NoteOf("task.comments.stage_no_agent", "stage", stageName))
		}
		return
	}
	agentType := d.launcher.FindAgentType(t.WorkspaceID, agentName)
	if agentType == nil {
		d.stageWithoutAgent(ctx, t, agentName, CauseAgentMissing,
			NoteOf("task.comments.agent_type_not_found", "agent", agentName, "stage", stageName))
		return
	}
	if len(agentType.ValidStages) > 0 && !agentType.SupportsStage(t.Stage) {
		d.stageWithoutAgent(ctx, t, agentName, CauseAgentInvalid, NoteOf("task.comments.agent_invalid_for_stage",
			"agent", agentName, "stage", stageName, "valid", d.stageNames(t, agentType.ValidStages)))
		return
	}

	go d.launchStage(ctx, t, t.Stage, agentName, agentType, launchOpts{})
}

func (d *Dispatcher) stageWithoutAgent(ctx context.Context, t *Task, agent, cause string, why Note) {
	logx.Warn(d.runNotStarted(ctx, t, t.Stage, agent, launchOpts{Kind: LaunchStage}, cause, why,
		noAgentReason(cause, agent, d.stageNote(t, t.Stage))),
		"task.log.stage_without_agent", "task", t.ID)
}

func noAgentReason(cause, agent string, stage any) error {
	switch cause {
	case CauseAgentMissing:
		return apperr.Precondition("task.errors.stage_agent_missing", "agent", agent, "stage", stage)
	case CauseAgentInvalid:
		return apperr.Precondition("task.errors.stage_agent_invalid", "agent", agent, "stage", stage)
	}
	return apperr.Precondition("task.errors.stage_has_no_agent", "stage", stage)
}

func (d *Dispatcher) stageNames(t *Task, stages []Stage) apperr.List {
	names := apperr.List{Errs: make([]error, 0, len(stages)), Sep: ", "}
	for _, s := range stages {
		names.Errs = append(names.Errs, stageNameErr(d.stageNote(t, s)))
	}
	return names
}

func stageNameErr(v any) error {
	switch n := v.(type) {
	case error:
		return n
	case string:
		return typedName(n)
	}
	return typedName("")
}

type typedName string

func (n typedName) Error() string { return string(n) }

func (d *Dispatcher) RelaunchTurns(ctx context.Context, t *Task) int {
	base := 0
	if agent := t.AssignedAgent(t.Stage); agent != "" {
		if at := d.launcher.FindAgentType(t.WorkspaceID, agent); at != nil {
			base = turnCount(at.TurnsForStage(string(t.Stage)))
		}
	}
	if t.Status == StatusOutOfTurns || t.Status == StatusNeedsMoreTurns {
		if last := d.store.lastRunBudget(ctx, t.ID, t.Stage); last > base {
			base = last
		}
		if base > 0 {
			return base * 2
		}
	}
	if base <= 0 {
		return 20
	}
	return base
}

const relaunchWait = 20 * time.Second

func (d *Dispatcher) missingAgent(ctx context.Context, t *Task, info types.StageInfo) bool {
	if info.Terminal || info.Virtual {
		return false
	}
	if d.projects != nil {
		for _, pid := range t.ProjectIDs {
			p, err := d.projects.Get(ctx, pid)
			if err == nil && p.StageTemplate[string(info.ID)] != "" {
				return true
			}
		}
	}
	found, err := d.store.BoardStageHasAgent(ctx, t.WorkspaceID, t.BoardID, string(info.ID), t.ID)
	if err != nil {
		log.Printf("[task] %s", i18n.Log("task.log.board_stage_agent_failed", "task", t.ID, "err", err))
		return false
	}
	return found
}

func (d *Dispatcher) FindAgentTypeForRelaunch(workspaceID, name string) *types.AgentType {
	return d.launcher.FindAgentType(workspaceID, name)
}

func (d *Dispatcher) RelaunchStage(ctx context.Context, t *Task, turnsOverride int) error {
	if t == nil {
		return apperr.Internal("task.errors.nil_task")
	}
	// Status==running can survive a daemon restart; only refuse if the recorded container is
	// actually alive.
	if t.Status == StatusRunning && t.CurrentRunID != "" && d.launcher.ContainerAlive(t.CurrentRunID) {
		return apperr.Conflict("task.errors.run_in_flight")
	}
	if d.room.waitingTask(t.ID) || d.waitingSlot(t.ID) {
		return apperr.Conflict("task.errors.run_queued")
	}
	agentName := t.AssignedAgent(t.Stage)
	if agentName == "" {
		return apperr.Precondition("task.errors.no_agent_for_stage", "stage", t.Stage)
	}
	agentType := d.launcher.FindAgentType(t.WorkspaceID, agentName)
	if agentType == nil {
		return apperr.NotFound("task.errors.agent_not_found", "agent", agentName)
	}
	if len(agentType.ValidStages) > 0 && !agentType.SupportsStage(t.Stage) {
		return apperr.Invalid("task.errors.agent_lacks_stage", "agent", agentName, "stage", t.Stage)
	}
	// A relaunch can hit a task whose worktree was never created (dispatch skipped, project linked
	// later); the mounts need a real source.
	if info, _ := d.stagesFor(t).Get(t.Stage); len(t.ProjectIDs) > 0 && !info.Initial {
		t = d.ensureWorktrees(ctx, t)
	}
	turnsStr := ""
	if turnsOverride > 0 {
		turnsStr = strconv.Itoa(turnsOverride)
	}
	settled := make(chan error, 1)
	opts := launchOpts{
		Turns:   turnsStr,
		settled: func(err error) { settled <- err },
	}
	if t.Status.keptByRelaunch() {
		opts.keep = t.Status
	}
	go d.launchStage(ctx, t, t.Stage, agentName, agentType, opts)
	timer := time.NewTimer(relaunchWait)
	defer timer.Stop()
	select {
	case err := <-settled:
		return err
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return nil
	}
}

func (d *Dispatcher) notifyDependentsUnblocked(ctx context.Context, doneTaskID string) {
	freed, err := d.store.JustUnblockedDependents(ctx, doneTaskID)
	if err != nil {
		log.Printf("[task] %s", i18n.Log("task.log.notify_unblocked_failed", "task", doneTaskID, "err", err))
		return
	}
	for _, depID := range freed {
		logx.Warn(d.store.RecordEvent(ctx, depID, EventDepResolved, doneTaskID, "octopus",
			i18n.Text("task.comments.deps_resolved"),
			map[string]string{"unblocked_by": doneTaskID}), "task.log.record_dep_resolved", "task", depID)
	}
}

func (d *Dispatcher) setStatus(ctx context.Context, taskID string, status RunStatus) {
	updated, err := d.store.Update(ctx, taskID, Patch{Status: &status})
	if err != nil {
		log.Printf("[task] %s", i18n.Log("task.log.set_status_failed", "task", taskID, "status", status, "err", err))
		return
	}
	if d.onStatus != nil {
		d.onStatus(updated, status)
	}
}

func (d *Dispatcher) WithStatusHook(fn func(t *Task, status RunStatus)) *Dispatcher {
	d.onStatus = fn
	return d
}

func (d *Dispatcher) WithStageHook(fn func(t *Task, from Stage)) *Dispatcher {
	d.onStage = fn
	return d
}

func (d *Dispatcher) stageMoved(t *Task, from Stage) {
	if d.onStage != nil && t != nil && t.Stage != from {
		d.onStage(t, from)
	}
}

func (d *Dispatcher) systemComment(ctx context.Context, taskID string, n Note) {
	if _, err := d.store.AddNote(ctx, taskID, "", SourceSystem, "octopus", n); err != nil {
		log.Printf("[task-dispatcher] %s", i18n.Log("task.log.system_comment_failed", "err", err))
	}
}

// Call before stopping the run so its exit reads as stopped, not failed.
// In memory only: a stop across a daemon restart reads as a failure.
func (d *Dispatcher) MarkStopped(runID, who string) {
	if runID != "" {
		d.stoppedBy.Store(runID, who)
	}
}

func (d *Dispatcher) takeStopped(runID string) (string, bool) {
	v, ok := d.stoppedBy.LoadAndDelete(runID)
	if !ok {
		return "", false
	}
	who, ok := v.(string)
	return who, ok
}

func (d *Dispatcher) stageName(t *Task, stage Stage) string {
	if stage == "" {
		return ""
	}
	if info, ok := d.stagesFor(t).Get(stage); ok && info.Label != "" {
		return info.Label
	}
	return string(stage)
}

func (d *Dispatcher) stageNote(t *Task, stage Stage) any {
	name := d.stageName(t, stage)
	if key, ok := stagepkg.SeededLabelKey(stage, name); ok {
		return apperr.New(key)
	}
	return name
}

func (d *Dispatcher) StageNote(t *Task, stage Stage) any { return d.stageNote(t, stage) }

func (d *Dispatcher) stageNoteOf(ctx context.Context, taskID string, stage Stage) any {
	t, err := d.store.GetRow(ctx, taskID)
	if err != nil {
		t = nil
	}
	return d.stageNote(t, stage)
}

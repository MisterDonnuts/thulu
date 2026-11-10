package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// Travels with the run (OCTOPUS_LAUNCH_KIND env, octopus.launch_kind label/annotation) because
// Reconcile must rebuild post-run options after a restart loses the exit callback.
type LaunchKind string

const (
	LaunchStage         LaunchKind = "stage"
	LaunchContinuation  LaunchKind = "continuation"
	LaunchAnswerResume  LaunchKind = "answer_resume"
	LaunchChangesResume LaunchKind = "changes_resume"
)

const (
	LabelLaunchKind = "octopus.launch_kind"
	EnvLaunchKind   = "OCTOPUS_LAUNCH_KIND"
)

// Unknown or empty (runs launched before the label existed) replays as a stage run.
func ParseLaunchKind(s string) LaunchKind {
	switch k := LaunchKind(s); k {
	case LaunchStage, LaunchContinuation, LaunchAnswerResume, LaunchChangesResume:
		return k
	}
	return LaunchStage
}

func (k LaunchKind) postRunOpts() postRunOpts {
	if k == LaunchContinuation {
		return postRunOpts{NoAdvance: true, NoAudit: true}
	}
	return postRunOpts{}
}

type launchOpts struct {
	Kind      LaunchKind
	Turns     string
	Session   string
	Message   string
	NoAdvance bool
	onAbort   func()
	settled   func(err error)
	keep      RunStatus
}

func (o launchOpts) kind() LaunchKind {
	switch {
	case o.Kind != "":
		return o.Kind
	case o.Session != "":
		return LaunchContinuation
	}
	return LaunchStage
}

// Persisted in run_not_started event data and read by the run card; keep values stable.
const (
	CauseNoLLMProfile   = "llm_credential"
	CauseCredential     = "credential"
	CauseQuota          = "quota"
	CauseNoAgent        = "no_agent"
	CauseAgentMissing   = "agent_missing"
	CauseAgentInvalid   = "agent_invalid"
	CauseQueueTimeout   = "queue_timeout"
	CauseRuntime        = "runtime"
	CauseBadStage       = "bad_stage"
	CauseNotStartedElse = "launch"
)

type NotStartedError struct {
	Cause string
	Err   error
}

func (e *NotStartedError) Error() string { return e.Err.Error() }
func (e *NotStartedError) Unwrap() error { return e.Err }

type notStartedRun struct {
	Stage      string            `json:"stage"`
	Agent      string            `json:"agent"`
	LaunchKind string            `json:"launch_kind"`
	Cause      string            `json:"cause"`
	Reason     string            `json:"reason"`
	ReasonI18n map[string]string `json:"reason_i18n,omitempty"`
}

func (d *Dispatcher) runNotStarted(ctx context.Context, t *Task, stage Stage, agent string, opts launchOpts,
	cause string, why Note, reason error,
) error {
	kind := opts.kind()
	if kind == LaunchAnswerResume && reason != nil {
		why = NoteOf("task.questions.comment.resume_failed", "err", reason)
	}
	next, ok := notStartedStatus(kind, cause)
	switch d.keptStatus(ctx, t.ID, opts.keep) {
	case keptNone:
	case keptInPlace:
		why, ok = Note{}, false
	case keptAfterWait:
		ok = false
	}
	if !why.Empty() {
		d.systemComment(ctx, t.ID, why)
	}
	logx.Warn(d.store.RecordEvent(ctx, t.ID, EventRunNotStarted, t.ID, "octopus",
		NoteOf("task.comments.run_not_started_event", "stage", d.stageNote(t, stage), "agent", agent).Text(),
		notStartedRun{
			Stage: string(stage), Agent: agent, LaunchKind: string(kind), Cause: cause,
			Reason: apperr.Text(reason), ReasonI18n: localizedTexts(reason),
		}), "task.log.record_run_not_started", "task", t.ID)
	if ok {
		d.setStatus(ctx, t.ID, next)
	}
	return &NotStartedError{Cause: cause, Err: reason}
}

type keptOutcome int

const (
	keptNone keptOutcome = iota
	keptInPlace
	keptAfterWait
)

func (d *Dispatcher) keptStatus(ctx context.Context, taskID string, keep RunStatus) keptOutcome {
	if keep == "" {
		return keptNone
	}
	if cur, err := d.store.Get(ctx, taskID); err == nil && cur.Status == keep {
		return keptInPlace
	}
	if d.store.casStatus(ctx, taskID, StatusQueued, keep) {
		return keptAfterWait
	}
	return keptNone
}

func notStartedStatus(kind LaunchKind, cause string) (RunStatus, bool) {
	switch {
	case kind == LaunchContinuation:
		return "", false
	case cause == CauseCredential:
		return StatusCredentialBlocked, true
	case cause == CauseNoAgent || cause == CauseAgentMissing || cause == CauseAgentInvalid:
		return StatusNoAgent, true
	case kind == LaunchAnswerResume || kind == LaunchChangesResume:
		return StatusResumeFailed, true
	}
	return StatusNotStarted, true
}

func localizedTexts(err error) map[string]string {
	if err == nil {
		return nil
	}
	out := map[string]string{}
	for _, l := range i18n.Default().Locales() {
		out[l] = apperr.Localize(err, l)
	}
	return out
}

func launchCause(err error) string {
	switch apperr.Code(err) {
	case "task.errors.no_llm_credential", "task.errors.no_llm_credential_no_workspace", "task.errors.resolve_llm_profile":
		return CauseNoLLMProfile
	case "task.errors.room_timeout":
		return CauseQueueTimeout
	}
	return CauseRuntime
}

func (d *Dispatcher) stageNotLaunched(ctx context.Context, t *Task, stage Stage, agent string, opts launchOpts, parked bool, err error) error {
	if errors.Is(err, errRoomDropped) {
		if parked && !d.room.waitingTask(t.ID) {
			d.store.casStatus(ctx, t.ID, StatusQueued, StatusIdle)
		}
		return nil
	}
	if parked && d.room.waitingTask(t.ID) {
		d.systemComment(ctx, t.ID, NoteOf("task.comments.launch_failed", "err", err))
		return err
	}
	return d.runNotStarted(ctx, t, stage, agent, opts, launchCause(err),
		NoteOf("task.comments.launch_failed", "err", err), err)
}

func (o launchOpts) postRunOpts() postRunOpts {
	p := o.kind().postRunOpts()
	p.NoAdvance = p.NoAdvance || o.NoAdvance
	return p
}

func (d *Dispatcher) launchStage(
	ctx context.Context, t *Task, stage Stage, agentName string, agentType *types.AgentType, opts launchOpts,
) {
	started := false
	settled := false
	settle := func(err error) {
		if !settled && opts.settled != nil {
			opts.settled(err)
		}
		settled = true
	}
	defer func() {
		if !started && opts.onAbort != nil {
			opts.onAbort()
		}
		settle(nil)
	}()
	ticket, ok := d.admitStage(ctx, t, stage, agentName, opts, settle)
	if !ok {
		return
	}
	// Exactly one release pairs this acquire: on the launch-failure return below or in OnExit.
	slotWS := t.WorkspaceID
	parked := false
	if err := d.acquireStageSlot(ctx, t, stage, agentName, opts, func() { parked = true; settle(nil) }); err != nil {
		d.refund(ctx, t.ID, ticket)
		return
	}

	instanceID := types.SanitizeContainerName(fmt.Sprintf(
		"%s-task-%s-%s-%d", agentName, t.ID, stage, time.Now().UnixMilli()))

	env := buildStageEnv(t, stage, agentName, instanceID, d.agentBaseURL, agentType, opts.Turns)
	_, launcherSteers := d.launcher.(RunSteerer)
	onRunner := d.taskRunsOnRunner(ctx, t)
	// Runners do not support mid-run steering; messages queue for the next run.
	applyConversationEnv(env, agentType, opts, launcherSteers && !onRunner)
	if info, _ := d.stagesFor(t).Get(stage); onRunner && info.PushBranch {
		env[EnvStagePush] = "1"
	}
	env[EnvLaunchKind] = string(opts.kind())
	d.applyMCPURLs(ctx, env, onRunner)
	if d.agentPushesResults && !onRunner {
		env["OCTOPUS_RESULT_REF"] = resultRefFor(t.SandboxRootID())
	}
	runKeyID := d.applyRunAPIKey(ctx, env, t, agentName, stage)
	personal, isPersonal := d.personalLoginFor(ctx, t.WorkspaceID, agentType, agentName)
	if err := d.applyRunLLM(ctx, env, t, agentType, agentName, personal, isPersonal); err != nil {
		d.slots.release(slotWS)
		d.refund(ctx, t.ID, ticket)
		d.revokeRunKey(ctx, runKeyID)
		settle(d.runNotStarted(ctx, t, stage, agentName, opts, launchCause(err),
			NoteOf("task.comments.launch_failed", "err", err), err))
		return
	}
	d.applyTokenOptimizer(ctx, env, t.WorkspaceID, agentType, string(stage))
	wtMounts, stageMounts := d.buildStageMounts(ctx, t)

	run, err := d.launchWithRoom(ctx, roomRun{
		ws: slotWS, taskID: t.ID, gate: d.slots,
		launch: func(lctx context.Context) (launched, error) {
			cid, iid, out, lerr := d.launcher.LaunchForStage(lctx, t, stage, agentType, env, wtMounts, stageMounts, false)
			return launched{cid, iid, out}, lerr
		},
		parked: func(why error) {
			if !parked {
				parked = true
				d.parkStage(ctx, t, stage, agentName, opts, why)
			}
			settle(nil)
		},
		wanted: func(wctx context.Context) bool { return d.stillQueued(wctx, t.ID, stage) },
	})
	if err != nil {
		// launchWithRoom already released the slot; do not release again.
		d.refund(ctx, t.ID, ticket)
		d.revokeRunKey(ctx, runKeyID)
		settle(d.stageNotLaunched(ctx, t, stage, agentName, opts, parked, err))
		return
	}
	containerID, instID, outputDir := run.containerID, run.instanceID, run.outputDir
	started = true
	settle(nil)
	stopDeadline := d.armDeadline(ctx, t.WorkspaceID, t.ID, containerID, func(minutes int) {
		d.systemComment(ctx, t.ID, NoteOf("task.comments.run_deadline_exceeded",
			"stage", d.stageNote(t, stage), "agent", agentName, "minutes", minutes, "instance", instID))
	})

	d.recordRunStarted(ctx, t, stage, agentName, opts, run, env, isPersonal)
	d.rememberRun(containerID, env)

	go d.recordPromptEvent(ctx, t.ID, instID, containerID, agentName, string(stage), outputDir)

	stageAtLaunch := stage
	d.launcher.OnExit(ctx, containerID, func(exitCode int, finalOutputDir, _ string) {
		stopDeadline()
		d.slots.release(slotWS)
		d.room.kick(slotWS)
		d.revokeRunKey(ctx, runKeyID)
		d.steerable.Delete(containerID)
		if finalOutputDir == "" {
			finalOutputDir = outputDir
		}
		d.postStageRun(ctx, t.ID, stageAtLaunch, agentName, containerID, instID, finalOutputDir, exitCode,
			opts.postRunOpts())
		d.runEngines.Delete(containerID)
		d.runProfiles.Delete(containerID)
	})
}

func (d *Dispatcher) recordRunStarted(ctx context.Context, t *Task, stage Stage, agentName string, opts launchOpts,
	run launched, env map[string]string, isPersonal bool,
) {
	containerID, instID, outputDir := run.containerID, run.instanceID, run.outputDir
	runStatus := StatusRunning
	_, updErr := d.store.Update(ctx, t.ID, Patch{
		CurrentRunID:    &containerID,
		CurrentInstance: &instID,
		Status:          &runStatus,
	})
	logx.Warn(updErr, "task.log.update_run_state", "task", t.ID)
	launchNote := NoteOf("task.comments.launched", "stage", d.stageNote(t, stage), "agent", agentName, "instance", instID)
	if opts.Session != "" {
		launchNote = NoteOf("task.comments.launched_resumed", "stage", d.stageNote(t, stage), "agent", agentName, "instance", instID)
	}
	d.systemComment(ctx, t.ID, launchNote)
	runData := map[string]string{
		"container_id": containerID,
		"instance_id":  instID,
		"agent":        agentName,
		"stage":        string(stage),
		"output_dir":   outputDir,
		"resumed":      strconv.FormatBool(opts.Session != ""),
		"launch_kind":  string(opts.kind()),
		"max_turns":    env["MAX_TURNS"],
	}
	if isPersonal {
		// Record which credential, never whose: the person stays in the triggered_by column, which
		// no export carries.
		runData["llm_source"] = engine.LLMSourcePersonal
	}
	logx.Warn(d.store.recordRunEvent(ctx, t.ID, EventAgentRunStarted, instID, "agent:"+agentName,
		fmt.Sprintf("%s · %s", stage, agentName), runData, TriggeredBy(ctx)),
		"task.log.record_run_started", "task", t.ID)
}

func (d *Dispatcher) rememberRun(containerID string, env map[string]string) {
	if env["STEER_ENABLED"] == "1" {
		d.steerable.Store(containerID, true)
	}
	if eng := env["ENGINE"]; eng != "" {
		d.runEngines.Store(containerID, eng)
	}
	if prof := env[engine.LLMProfileEnv]; prof != "" && !engine.ViaGateway(env) {
		d.runProfiles.Store(containerID, prof)
	}
}

func (d *Dispatcher) credentialFaults(ctx context.Context, t *Task) []credentials.Fault {
	if d.credGate == nil {
		return nil
	}
	faults, err := d.credGate(ctx, t)
	if err != nil {
		// Advisory gate: launch when the check fails. Tool calls are still authorized at call time,
		// and a DB hiccup must not stop every task.
		logx.Warn(err, "task.log.credential_gate", "task", t.ID)
		return nil
	}
	return faults
}

func (d *Dispatcher) admitStage(
	ctx context.Context, t *Task, stage Stage, agentName string, opts launchOpts, settle func(error),
) (quotaTicket, bool) {
	// The stage id is embedded in the hyphen-delimited instance ID that parseTaskInstanceID reads
	// back; a malformed id makes the run unreconcilable.
	if !types.ValidStageID(string(stage)) {
		err := apperr.Invalid("task.errors.bad_stage_id", "stage", stage)
		settle(d.runNotStarted(ctx, t, stage, agentName, opts, CauseBadStage,
			NoteOf("task.comments.launch_bad_stage_id", "stage", stage), err))
		return quotaTicket{}, false
	}
	// Runs before the slot is acquired so a refused run holds no capacity.
	// Keep this the only call site: every launch path funnels through launchStage.
	if faults := d.credentialFaults(ctx, t); len(faults) > 0 {
		settle(d.refuseCredentials(ctx, t, stage, agentName, opts, faults))
		return quotaTicket{}, false
	}
	// Consumed before the slot; every return in launchStage that starts no container must refund
	// the ticket.
	ticket, usage, ok := d.consumeRun(ctx, t)
	if !ok {
		settle(d.runNotStarted(ctx, t, stage, agentName, opts, CauseQuota,
			NoteOf("task.comments.run_quota_exceeded", quotaSpentArgs(usage)...),
			apperr.Precondition("task.errors.run_quota_spent", quotaSpentArgs(usage)...)))
		return quotaTicket{}, false
	}
	return ticket, true
}

func (d *Dispatcher) credentialsReady(ctx context.Context, t *Task) bool {
	faults := d.credentialFaults(ctx, t)
	if len(faults) == 0 {
		return true
	}
	return d.refuseCredentials(ctx, t, t.Stage, t.AssignedAgent(t.Stage), launchOpts{Kind: LaunchStage}, faults) == nil
}

func (d *Dispatcher) refuseCredentials(ctx context.Context, t *Task, stage Stage, agent string, opts launchOpts,
	faults []credentials.Fault,
) error {
	n := NoteOf("task.comments.launch_blocked", "count", len(faults))
	for _, f := range faults {
		n = n.Add("task.comments.launch_blocked_fault",
			"name", f.Name, "provider", f.Provider, "detail", f.Detail, "tools", strings.Join(f.Tools, "`, `"))
	}
	n = n.Add("task.comments.launch_blocked_fix")
	logx.Warn(d.store.RecordEvent(ctx, t.ID, EventCredentialBlocked, t.ID, "octopus",
		faultSummary(faults), credentialBlockedEvent{
			Stage:       string(t.Stage),
			Credentials: faultNames(faults),
			Faults:      faultStates(faults),
		}), "task.log.record_credential_blocked", "task", t.ID)
	return d.runNotStarted(ctx, t, stage, agent, opts, CauseCredential, n,
		apperr.Precondition("task.errors.credentials_unusable", "names", strings.ReplaceAll(faultNames(faults), ",", ", ")))
}

type credentialBlockedEvent struct {
	Stage       string            `json:"stage"`
	Credentials string            `json:"credentials"`
	Faults      []credentialFault `json:"faults"`
}

type credentialFault struct {
	Name  string            `json:"name"`
	State credentials.State `json:"state"`
}

func faultStates(faults []credentials.Fault) []credentialFault {
	out := make([]credentialFault, 0, len(faults))
	for _, f := range faults {
		out = append(out, credentialFault{Name: f.Name, State: f.State})
	}
	return out
}

func faultSummary(faults []credentials.Fault) string {
	if len(faults) != 1 {
		return i18n.Text("task.comments.credentials_unusable", "count", len(faults))
	}
	switch f := faults[0]; f.State {
	case credentials.StateExpired:
		return i18n.Text("task.comments.credential_expired", "name", f.Name)
	case credentials.StateInvalid:
		return i18n.Text("task.comments.credential_rejected", "name", f.Name)
	default:
		return i18n.Text("task.comments.credential_unusable", "name", f.Name)
	}
}

func faultNames(faults []credentials.Fault) string {
	names := make([]string, 0, len(faults))
	for _, f := range faults {
		names = append(names, f.Name)
	}
	return strings.Join(names, ",")
}

func (d *Dispatcher) buildStageMounts(ctx context.Context, t *Task) (wtMounts, stageMounts map[string]string) {
	wtMounts = map[string]string{}
	stageMounts = map[string]string{}
	for _, projectID := range t.ProjectIDs {
		path := t.WorktreePaths[projectID]
		if path == "" {
			continue
		}
		proj, err := d.projects.Get(ctx, projectID)
		if err != nil {
			continue
		}
		name := filepath.Base(proj.Path)
		wtMounts[name] = path
		if sd, err := d.projects.EnsureStageDirs(proj); err == nil {
			stageMounts[name] = sd
		}
	}
	return wtMounts, stageMounts
}

type ChatMentionRunner func(ctx context.Context, t *Task, agentType *types.AgentType, comment *Comment)

func (d *Dispatcher) WithChatMentions(r ChatMentionRunner) *Dispatcher {
	d.chatMentions = r
	return d
}

type AgentCommentHook func(ctx context.Context, taskID string, c *Comment)

func (d *Dispatcher) WithAgentComments(h AgentCommentHook) *Dispatcher {
	d.agentComments = h
	return d
}

func (d *Dispatcher) postAgentComment(ctx context.Context, taskID, threadID, agentName, body string) error {
	c, err := d.store.AddToThread(ctx, taskID, threadID, SourceAgent, agentName, body)
	if err != nil {
		return err
	}
	if d.agentComments != nil {
		d.agentComments(ctx, taskID, c)
	}
	return nil
}

func (d *Dispatcher) HandleMention(ctx context.Context, t *Task, agentType *types.AgentType, comment *Comment) {
	if t == nil || agentType == nil || comment == nil {
		return
	}
	thread := comment.ThreadID()
	if !agentType.AnswersMentions() {
		d.threadNotice(ctx, t.ID, thread, NoteOf("task.comments.mention_not_supported", "agent", agentType.Name))
		return
	}
	claimed, queued := d.mentions.begin(mentionKey(t.ID, thread, agentType.Name), comment)
	if !claimed {
		d.threadNotice(ctx, t.ID, thread, NoteOf("task.comments.mention_busy", "agent", agentType.Name, "count", queued))
		return
	}
	d.runMention(ctx, t, agentType, comment)
}

func (d *Dispatcher) runMention(ctx context.Context, t *Task, agentType *types.AgentType, comment *Comment) {
	if !agentType.AnswersMentionsByChat() {
		d.launchMention(ctx, t, agentType, comment)
		return
	}
	key := mentionKey(t.ID, comment.ThreadID(), agentType.Name)
	if d.chatMentions == nil {
		d.mentions.abandon(key)
		d.threadNotice(ctx, t.ID, comment.ThreadID(),
			NoteOf("task.comments.mention_chat_unavailable", "agent", agentType.Name))
		return
	}
	d.chatMentions(ctx, t, agentType, comment)
	d.mentionDone(ctx, t.ID, agentType, key)
}

func (d *Dispatcher) mentionDone(ctx context.Context, taskID string, agentType *types.AgentType, key string) {
	if merged := d.mentions.finish(key); merged != nil {
		go d.resumeMention(ctx, taskID, agentType, merged)
	}
}

func (d *Dispatcher) resumeMention(ctx context.Context, taskID string, agentType *types.AgentType, merged *Comment) {
	t, err := d.store.Get(ctx, taskID)
	if err != nil {
		logx.Warn(err, "task.log.reload_queued_mention", "task", taskID)
		d.mentions.abandon(mentionKey(taskID, merged.ThreadID(), agentType.Name))
		return
	}
	d.runMention(ctx, t, agentType, merged)
}

func (d *Dispatcher) threadNotice(ctx context.Context, taskID, threadID string, n Note) {
	_, err := d.store.AddNote(ctx, taskID, threadID, SourceSystem, "octopus", n)
	logx.Warn(err, "task.log.post_mention_notice", "task", taskID)
}

func MentionInstanceID(agentName, taskID, threadID string) string {
	return types.SanitizeContainerName(fmt.Sprintf(
		"%s-mention-%s-%s-%d", agentName, taskID, threadID, time.Now().UnixMilli()))
}

func buildMentionEnv(t *Task, agentName, instanceID, baseURL, locale string, agentType *types.AgentType, comment *Comment) map[string]string { //nolint:lll // signature
	env := buildStageEnv(t, types.StageMention, agentName, instanceID, baseURL, agentType, "")
	env["OCTOPUS_LOCALE"] = locale
	env["MENTION_COMMENT_ID"] = comment.ID
	env["MENTION_COMMENT_BODY"] = comment.Body
	env["THREAD_ID"] = comment.ThreadID()
	return env
}

func (d *Dispatcher) launchMention(ctx context.Context, t *Task, agentType *types.AgentType, comment *Comment) {
	agentName := agentType.Name
	thread := comment.ThreadID()
	key := mentionKey(t.ID, thread, agentName)
	ticket, usage, ok := d.consumeRun(ctx, t)
	if !ok {
		msg := NoteOf("task.comments.mention_quota_exceeded", append([]any{"agent", agentName}, quotaSpentArgs(usage)...)...)
		if dropped := d.mentions.abandon(key); dropped > 0 {
			msg = msg.Add("task.comments.mention_dropped_queue", "count", dropped)
		}
		d.threadNotice(ctx, t.ID, thread, msg)
		return
	}
	slotWS := t.WorkspaceID
	if err := d.convSlots.acquire(ctx, slotWS); err != nil {
		d.refund(ctx, t.ID, ticket)
		d.mentions.abandon(key)
		return
	}
	instanceID := MentionInstanceID(agentName, t.ID, thread)

	// The locale comes from ctx: HandleMention's callers detach it with context.WithoutCancel to
	// keep the request's values.
	env := buildMentionEnv(t, agentName, instanceID, d.agentBaseURL, i18n.FromContext(ctx), agentType, comment)
	d.applyMCPURLs(ctx, env, d.taskRunsOnRunner(ctx, t))
	runKeyID := d.applyRunAPIKey(ctx, env, t, agentName, types.StageMention)
	giveUp := func() int {
		d.refund(ctx, t.ID, ticket)
		d.revokeRunKey(ctx, runKeyID)
		return d.mentions.abandon(key)
	}
	launchFailed := func(err error) {
		noteKey := "task.comments.mention_launch_failed"
		if launchCause(err) == CauseNoLLMProfile {
			noteKey = "task.comments.mention_launch_no_credential"
		}
		msg := NoteOf(noteKey, "agent", agentName, "err", err)
		if dropped := giveUp(); dropped > 0 {
			msg = msg.Add("task.comments.mention_dropped_queue", "count", dropped)
		}
		d.threadNotice(ctx, t.ID, thread, msg)
	}
	if err := d.applyLLMAuth(ctx, env, t.WorkspaceID, agentType, agentName, t); err != nil {
		d.convSlots.release(slotWS)
		launchFailed(err)
		return
	}
	d.applyTokenOptimizer(ctx, env, t.WorkspaceID, agentType, string(types.StageMention))

	code := types.CodePaths(ReadOnlyCode(ctx, t, d.projects))

	run, err := d.launchWithRoom(ctx, roomRun{
		ws: slotWS, gate: d.convSlots,
		launch: func(lctx context.Context) (launched, error) {
			cid, iid, out, lerr := d.launcher.LaunchForStage(lctx, t, types.StageMention, agentType, env, code, nil, true)
			return launched{cid, iid, out}, lerr
		},
		parked: func(why error) {
			d.threadNotice(ctx, t.ID, thread, NoteOf("task.comments.mention_queued",
				"agent", agentName, "reason", why, "minutes", d.maxQueueMinutes()))
		},
		wanted: func(wctx context.Context) bool {
			_, gerr := d.store.Get(wctx, t.ID)
			return gerr == nil
		},
	})
	if errors.Is(err, errRoomDropped) {
		giveUp()
		return
	}
	if err != nil {
		launchFailed(err)
		return
	}
	containerID, instID, outputDir := run.containerID, run.instanceID, run.outputDir

	d.threadNotice(ctx, t.ID, thread, NoteOf("task.comments.mention_started", "agent", agentName, "instance", instID))
	logx.Warn(d.store.RecordEvent(ctx, t.ID, EventMentionStarted, instID, "agent:"+agentName,
		i18n.Text("task.comments.mention_started_event", "agent", agentName),
		map[string]string{
			"container_id": containerID,
			"instance_id":  instID,
			"agent":        agentName,
			"comment_id":   comment.ID,
			"thread_id":    thread,
			"output_dir":   outputDir,
		}), "task.log.record_mention_started", "task", t.ID)

	go d.recordPromptEvent(ctx, t.ID, instID, containerID, agentName, string(types.StageMention), outputDir)

	stopDeadline := d.armDeadline(ctx, t.WorkspaceID, t.ID, containerID, func(minutes int) {
		d.threadNotice(ctx, t.ID, thread, NoteOf("task.comments.mention_deadline_exceeded",
			"agent", agentName, "minutes", minutes, "instance", instID))
	})
	d.launcher.OnExit(ctx, containerID, func(exitCode int, finalOutputDir, _ string) {
		stopDeadline()
		d.convSlots.release(slotWS)
		d.room.kick(slotWS)
		d.revokeRunKey(ctx, runKeyID)
		if finalOutputDir == "" {
			finalOutputDir = outputDir
		}
		d.takeStopped(containerID)
		d.postMentionRun(ctx, t.ID, thread, agentName, containerID, instID, finalOutputDir, exitCode)
		d.mentionDone(ctx, t.ID, agentType, key)
	})
}

// A pod posts its reply through the API and its .posted marker stays in the pod, so the daemon looks
// for what arrived since the run started.
func (d *Dispatcher) mentionAnsweredThroughAPI(ctx context.Context, taskID, threadID, agentName, instanceID string) bool {
	if d.countRunReports(ctx, taskID, instanceID) > 0 {
		return true
	}
	dash := strings.LastIndexByte(instanceID, '-')
	if dash < 0 {
		return false
	}
	startedMs, err := strconv.ParseInt(instanceID[dash+1:], 10, 64)
	if err != nil {
		return false
	}
	started := time.UnixMilli(startedMs)
	comments, err := d.store.ListComments(ctx, taskID)
	if err != nil {
		logx.Warn(err, "task.log.add_mention_reply", "task", taskID)
		return false
	}
	return slices.ContainsFunc(comments, func(c Comment) bool {
		return c.Source == SourceAgent && c.Author == agentName && c.ParentID == threadID && !c.CreatedAt.Before(started)
	})
}

func (d *Dispatcher) postMentionRun(ctx context.Context, taskID, threadID, agentName, containerID, instanceID, outputDir string, exitCode int) { //nolint:lll // signature
	posted := false
	alreadyPosted := func(name string) bool {
		_, err := os.Stat(filepath.Join(outputDir, name))
		return err == nil
	}
	reply := func(body string) {
		logx.Warn(d.postAgentComment(ctx, taskID, threadID, agentName, body), "task.log.add_mention_reply", "task", taskID)
		posted = true
	}
	if alreadyPosted("comment.posted") {
		posted = true
	} else if body, ok := readFile(outputDir, "comment.md"); ok {
		reply(body)
	}
	if !posted {
		if body, ok := readFile(outputDir, "_index.md"); ok {
			reply(body)
		}
	}
	d.copyAllScreenshots(ctx, taskID, outputDir)

	if !posted {
		posted = d.mentionAnsweredThroughAPI(ctx, taskID, threadID, agentName, instanceID)
	}
	if !posted {
		msg := NoteOf("task.comments.mention_no_reply", "agent", agentName, "code", exitCode)
		if hint := runFailureHint(exitCode); hint != nil {
			msg = NoteOf("task.comments.mention_failed_hint", "agent", agentName, "hint", hint, "code", exitCode)
		} else if exitCode != 0 {
			msg = msg.Then(d.engineFailureNotice("", agentName, containerID, outputDir))
		}
		d.threadNotice(ctx, taskID, threadID, msg)
	}
	logx.Warn(d.store.RecordEvent(ctx, taskID, EventMentionFinished, instanceID, "agent:"+agentName,
		i18n.Text("task.comments.mention_finished_event", "agent", agentName, "code", exitCode),
		map[string]any{
			"container_id": containerID,
			"instance_id":  instanceID,
			"agent":        agentName,
			"thread_id":    threadID,
			"exit":         exitCode,
			"output_dir":   outputDir,
			"posted":       posted,
		}), "task.log.record_mention_finished", "task", taskID)
}

// Older instance IDs carry the triggering root comment id here, which equals its thread id.
func parseMentionInstanceID(instanceID string) (taskID, threadID string, ok bool) {
	const anchor = "-mention-"
	idx := strings.Index(instanceID, anchor)
	if idx < 0 {
		return "", "", false
	}
	tail := instanceID[idx+len(anchor):]
	lastDash := strings.LastIndex(tail, "-")
	if lastDash < 0 {
		return "", "", false
	}
	if _, err := strconv.Atoi(tail[lastDash+1:]); err != nil {
		return "", "", false
	}
	rest := tail[:lastDash]
	// taskID and threadID are hex (generateID) with no internal hyphens.
	cut := strings.LastIndex(rest, "-")
	if cut <= 0 || cut == len(rest)-1 {
		return "", "", false
	}
	return rest[:cut], rest[cut+1:], true
}

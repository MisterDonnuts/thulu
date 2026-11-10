package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/safepath"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type Delivery string

const (
	DeliverySteered      Delivery = "steered"
	DeliveryQueued       Delivery = "queued"
	DeliveryContinuation Delivery = "continuation"
	DeliveryUnreachable  Delivery = "unreachable"
	DeliveryMention      Delivery = "mention"
	DeliveryChat         Delivery = "chat"
)

type ConverseResult struct {
	Delivery Delivery `json:"delivery"`
	Agent    string   `json:"agent,omitempty"`
	Detail   string   `json:"detail"`
	Message  *Message `json:"message,omitempty"`
	ChatID   string   `json:"chat_id,omitempty"`
}

// Only the Docker launcher implements this. Kubernetes would need pods/exec, which the daemon's
// ClusterRole withholds.
type RunSteerer interface {
	SteerRun(ctx context.Context, runID string, payload []byte) error
}

type RunLogReader interface {
	RunLogs(runID string) string
}

// Wire format shared with images/snippets/steer-driver.js.
type steerPayload struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
	Interrupt bool   `json:"interrupt,omitempty"`
	Author    string `json:"author,omitempty"`
	At        string `json:"at"`
}

// Same cap as the mention lane; keep them in sync.
const maxPendingConversation = 5

// In memory on purpose: every message is already in the task's chat, so a restart loses only
// auto-delivery.
type convLane struct {
	mu      sync.Mutex
	pending map[string][]string
}

func newConvLane() *convLane { return &convLane{pending: map[string][]string{}} }

func (l *convLane) add(taskID, text string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	q := l.pending[taskID]
	q = append(q, text)
	if len(q) > maxPendingConversation {
		q = q[len(q)-maxPendingConversation:]
	}
	l.pending[taskID] = q
	return len(q)
}

func (l *convLane) drain(taskID string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	q := l.pending[taskID]
	delete(l.pending, taskID)
	if len(q) == 0 {
		return ""
	}
	return strings.Join(q, "\n\n")
}

// The message is written to the chat before any delivery attempt so the record never depends on a
// live container.
func (d *Dispatcher) Converse(ctx context.Context, t *Task, text, author string, interrupt bool) (ConverseResult, error) {
	if t == nil {
		return ConverseResult{}, apperr.Internal("task.errors.nil_task")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ConverseResult{}, apperr.Invalid("task.errors.empty_message")
	}
	if author == "" {
		author = "user"
	}
	msg, err := d.store.AddMessage(ctx, &Message{
		TaskID: t.ID, Role: RoleUser, Author: author, Body: text,
	})
	if err != nil {
		return ConverseResult{}, apperr.Wrap(err, "task.errors.record_message")
	}
	res := d.deliver(ctx, t, text, author, interrupt, msg.ID)
	res.Message = msg

	msg.Delivery = string(res.Delivery)
	msg.Agent = res.Agent
	if res.Delivery == DeliverySteered || res.Delivery == DeliveryQueued {
		msg.RunID = t.CurrentRunID
	}
	logx.Warn(d.store.UpdateMessageDelivery(ctx, msg.ID, msg.Delivery, msg.Agent, msg.RunID, ""),
		"task.log.stamp_delivery", "message", msg.ID)

	logx.Warn(d.store.RecordEvent(ctx, t.ID, EventConversationMessage, msg.ID, author,
		fmt.Sprintf("%s · %s", res.Delivery, firstLine(text)),
		map[string]string{
			"delivery":   string(res.Delivery),
			"agent":      res.Agent,
			"message_id": msg.ID,
			"interrupt":  fmt.Sprintf("%t", interrupt),
			"line":       firstLine(text),
		}), "task.log.record_conversation_event", "task", t.ID)
	return res, nil
}

func (d *Dispatcher) systemMessage(ctx context.Context, taskID, body string) {
	_, err := d.store.AddMessage(ctx, &Message{
		TaskID: taskID, Role: RoleSystem, Author: "octopus", Body: body,
	})
	logx.Warn(err, "task.log.record_system_message", "task", taskID)
}

func (d *Dispatcher) deliver(ctx context.Context, t *Task, text, author string, interrupt bool, messageID string) ConverseResult { //nolint:lll // signature
	if t.CurrentRunID != "" && d.launcher.ContainerAlive(t.CurrentRunID) {
		agent := t.AssignedAgent(t.Stage)
		if _, armed := d.steerable.Load(t.CurrentRunID); armed {
			err := d.steerRun(ctx, t.CurrentRunID, steerPayload{
				ID: messageID, Kind: "message", Text: text,
				Author: author, Interrupt: interrupt,
			})
			logx.Warn(err, "task.log.steer_run", "run", t.CurrentRunID, "task", t.ID)
			if err == nil {
				detail := i18n.TCtx(ctx, "task.hints.steered_after_turn")
				if interrupt {
					detail = i18n.TCtx(ctx, "task.hints.steered_interrupt")
				}
				return ConverseResult{Delivery: DeliverySteered, Agent: agent, Detail: detail}
			}
		}
		n := d.conv.add(t.ID, text)
		return ConverseResult{
			Delivery: DeliveryQueued, Agent: agent,
			Detail: i18n.TCtx(ctx, "task.hints.queued_until_run_ends", "count", n),
		}
	}

	agent, stage, ok := d.resumeTarget(t)
	if !ok {
		return ConverseResult{
			Delivery: DeliveryUnreachable,
			Detail:   i18n.TCtx(ctx, "task.hints.no_session_yet"),
		}
	}
	if err := d.ContinueStage(ctx, t, text); err != nil {
		return ConverseResult{
			Delivery: DeliveryUnreachable, Agent: agent,
			Detail: i18n.TCtx(ctx, "task.hints.resume_failed", "err", apperr.LocalizeCtx(ctx, err)),
		}
	}
	return ConverseResult{
		Delivery: DeliveryContinuation, Agent: agent,
		Detail: i18n.TCtx(ctx, "task.hints.continuation", "agent", agent, "stage", stage),
	}
}

func (d *Dispatcher) resumeTarget(t *Task) (agentName string, stage Stage, ok bool) {
	if t.SessionID == "" || t.SessionAgent == "" {
		return "", "", false
	}
	stage = Stage(t.SessionStage)
	if stage == "" {
		stage = t.Stage
	}
	if d.launcher.FindAgentType(t.WorkspaceID, t.SessionAgent) == nil {
		return "", "", false
	}
	return t.SessionAgent, stage, true
}

func (d *Dispatcher) ConverseTarget(t *Task) (agent, gone string, ok bool) {
	if t == nil {
		return "", "", false
	}
	if t.CurrentRunID != "" && d.launcher.ContainerAlive(t.CurrentRunID) {
		return t.AssignedAgent(t.Stage), "", true
	}
	if agent, _, ok = d.resumeTarget(t); ok {
		return agent, "", true
	}
	if t.SessionID != "" && t.SessionAgent != "" {
		return "", t.SessionAgent, false
	}
	return "", "", false
}

func (d *Dispatcher) steerRun(ctx context.Context, runID string, p steerPayload) error {
	steerer, ok := d.launcher.(RunSteerer)
	if !ok {
		return apperr.NotImplemented("task.errors.launcher_cannot_steer")
	}
	p.At = time.Now().UTC().Format(time.RFC3339)
	line, err := json.Marshal(p)
	if err != nil {
		return apperr.Wrap(err, "task.errors.encode_steer_payload")
	}
	return steerer.SteerRun(ctx, runID, append(line, '\n'))
}

func (d *Dispatcher) ContinueStage(ctx context.Context, t *Task, message string) error {
	if t == nil {
		return apperr.Internal("task.errors.nil_task")
	}
	if strings.TrimSpace(message) == "" {
		return apperr.Invalid("task.errors.empty_message")
	}
	agentName, stage, ok := d.resumeTarget(t)
	if !ok {
		return apperr.Precondition("task.errors.no_session_to_resume")
	}
	if t.Status == StatusRunning && t.CurrentRunID != "" && d.launcher.ContainerAlive(t.CurrentRunID) {
		return apperr.Conflict("task.errors.run_in_progress")
	}
	agentType := d.launcher.FindAgentType(t.WorkspaceID, agentName)
	if agentType == nil {
		return apperr.NotFound("task.errors.agent_not_found", "agent", agentName)
	}
	if info, _ := d.stagesFor(t).Get(stage); len(t.ProjectIDs) > 0 && !info.Initial {
		t = d.ensureWorktrees(ctx, t)
	}
	go d.launchStage(ctx, t, stage, agentName, agentType, launchOpts{
		Kind:      LaunchContinuation,
		Session:   t.SessionID,
		Message:   message,
		NoAdvance: true,
	})
	return nil
}

// STEER_ENABLED needs both a streaming-input engine and a RunSteerer launcher; arming it on
// Kubernetes made runs look steerable
// when steerRun can only fail. RESUME_MESSAGE is set even without a session: ResumeStage launches
// either way.
func applyConversationEnv(env map[string]string, agentType *types.AgentType, opts launchOpts, launcherSteers bool) {
	if opts.Session != "" {
		env["SESSION_ID"] = opts.Session
	}
	if opts.Message != "" {
		env["RESUME_MESSAGE"] = opts.Message
	}
	if launcherSteers && engineTakesStreamingInput(resolveEngineName(agentType)) {
		env["STEER_ENABLED"] = "1"
	}
}

// Unknown engines answer false: arming a channel nothing reads would swallow messages.
func engineTakesStreamingInput(name string) bool {
	eng, err := engine.Get(name)
	if err != nil {
		return false
	}
	return eng.Capabilities().StreamingInput
}

// Parse logs with the engine's parser, never a pattern: claude-code reports session_id, codex
// thread_id.
func (d *Dispatcher) captureSession(ctx context.Context, taskID, workspaceID string, stage Stage, agentName, outputDir, runID string) {
	sid := readSessionIDFile(outputDir)
	if sid == "" {
		sid = d.sessionFromRunLogs(workspaceID, agentName, runID)
	}
	if sid == "" {
		return
	}
	stageStr := string(stage)
	_, err := d.store.Update(ctx, taskID, Patch{
		SessionID:    &sid,
		SessionAgent: &agentName,
		SessionStage: &stageStr,
	})
	logx.Warn(err, "task.log.persist_session", "task", taskID)
}

var sessionIDFiles = []string{".engine-session/id", ".session_id"}

// The id ends up in a daemon path and in the next run's env, so anything that is not a plain token
// is dropped.
func readSessionIDFile(outputDir string) string {
	if outputDir == "" {
		return ""
	}
	for _, name := range sessionIDFiles {
		if sid := readFirstToken(outputDir, name); ValidSessionName(sid) {
			return sid
		}
	}
	return ""
}

func readFirstToken(dir, name string) string {
	f, err := safepath.Open(dir, name)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 256)
	n, err := f.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	line, _, _ := strings.Cut(string(buf[:n]), "\n")
	return strings.TrimSpace(line)
}

func (d *Dispatcher) sessionFromRunLogs(workspaceID, agentName, runID string) string {
	reader, ok := d.launcher.(RunLogReader)
	if !ok {
		return ""
	}
	logs := reader.RunLogs(runID)
	if logs == "" {
		return ""
	}
	eng, err := engine.Get(d.runEngineName(workspaceID, agentName, runID))
	if err != nil {
		return ""
	}
	sid := ""
	for ev := range eng.ParseStream(strings.NewReader(logs)) {
		if (ev.Kind == engine.EventInit || ev.Kind == engine.EventResult) && ev.SessionID != "" {
			sid = ev.SessionID
		}
	}
	if !ValidSessionName(sid) {
		return ""
	}
	return sid
}

func (d *Dispatcher) runEngineName(workspaceID, agentName, runID string) string {
	if v, ok := d.runEngines.Load(runID); ok {
		if name, isName := v.(string); isName && name != "" {
			return name
		}
	}
	return resolveEngineName(d.launcher.FindAgentType(workspaceID, agentName))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}

const DeliveryThread Delivery = "thread"

func (d *Dispatcher) SteerState(t *Task) (running, steerable bool) {
	if t == nil || t.CurrentRunID == "" {
		return false, false
	}
	if !d.launcher.ContainerAlive(t.CurrentRunID) {
		return false, false
	}
	_, armed := d.steerable.Load(t.CurrentRunID)
	return true, armed
}

func (d *Dispatcher) CanSteer(t *Task) bool {
	_, steerable := d.SteerState(t)
	return steerable
}

func commentNotice(author, body string) string {
	if author == "" {
		author = i18n.Text("task.prompts.someone")
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	for i, ln := range lines {
		lines[i] = "> " + ln
	}
	return i18n.Text("task.prompts.comment_notice", "author", author, "quote", strings.Join(lines, "\n"))
}

// Must run after the thread write so a steering failure never costs the record. Never resumes or
// queues: a comment must not start a container.
func (d *Dispatcher) NotifyComment(ctx context.Context, t *Task, c *Comment) (ConverseResult, error) {
	if t == nil || c == nil {
		return ConverseResult{}, apperr.Internal("task.errors.nil_task_or_comment")
	}
	agent := t.AssignedAgent(t.Stage)
	if !d.CanSteer(t) {
		return ConverseResult{
			Delivery: DeliveryThread, Agent: agent,
			Detail: i18n.TCtx(ctx, "task.hints.comment_no_run"),
		}, nil
	}

	notice := commentNotice(c.Author, c.Body)
	msg, err := d.store.AddMessage(ctx, &Message{
		TaskID: t.ID, Role: RoleUser, Author: c.Author, Body: notice,
	})
	if err != nil {
		return ConverseResult{}, apperr.Wrap(err, "task.errors.record_message")
	}

	res := ConverseResult{Delivery: DeliverySteered, Agent: agent, Message: msg}
	res.Detail = i18n.TCtx(ctx, "task.hints.steered_after_turn")
	if err := d.steerRun(ctx, t.CurrentRunID, steerPayload{
		ID: msg.ID, Kind: "comment", Text: notice, Author: c.Author,
	}); err != nil {
		logx.Warn(err, "task.log.steer_comment", "comment", c.ID, "run", t.CurrentRunID)
		res.Delivery = DeliveryThread
		res.Detail = i18n.TCtx(ctx, "task.hints.comment_steer_refused")
	}

	msg.Delivery = string(res.Delivery)
	msg.Agent = res.Agent
	if res.Delivery == DeliverySteered {
		msg.RunID = t.CurrentRunID
	}
	logx.Warn(d.store.UpdateMessageDelivery(ctx, msg.ID, msg.Delivery, msg.Agent, msg.RunID, ""),
		"task.log.stamp_delivery", "message", msg.ID)

	logx.Warn(d.store.RecordEvent(ctx, t.ID, EventConversationMessage, msg.ID, c.Author,
		i18n.Text("task.comments.comment_notice_event", "delivery", res.Delivery, "line", firstLine(c.Body)),
		map[string]string{
			"delivery":   string(res.Delivery),
			"agent":      res.Agent,
			"message_id": msg.ID,
			"comment_id": c.ID,
			"line":       firstLine(c.Body),
		}), "task.log.record_comment_notice", "task", t.ID)
	return res, nil
}

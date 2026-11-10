package chatruntime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/safepath"
	"github.com/MisterDonnuts/thulu/internal/types"
)

var ErrNotChatCapable error = apperr.Unprocessable("chatruntime.errors.not_chat_capable")

var ErrArchived error = apperr.Conflict("chatruntime.errors.archived")

// Task and thread chats take turns from comments by other people; a delegated agent must take
// instructions from one person only.
var ErrNotDelegable error = apperr.Invalid("chatruntime.errors.not_delegable")

var ErrNotDelegator error = apperr.Forbidden("chatruntime.errors.not_delegator")

type CodeResolver func(taskID string) map[string]types.CodeView

// Implemented by the API server's content-store lookup; it must fail open.
type PromptCheck func(ctx context.Context, workspaceID, agent, stage string) bool

type Service struct {
	store     *chat.Store
	rt        *Manager
	bus       *chat.Bus
	agentsDir string
	agents    types.WorkspaceAgents
	code      CodeResolver
	hasPrompt PromptCheck
}

func NewService(store *chat.Store, rt *Manager, bus *chat.Bus, agentsDir string, code CodeResolver) *Service {
	if rt != nil && store != nil {
		rt.chats = store
	}
	return &Service{store: store, rt: rt, bus: bus, agentsDir: agentsDir, code: code}
}

// src is late-bound: its database half exists only after SetAgentStore, which runs after this
// service is built.
// Without it lookups read an agents directory the image does not have.
func (s *Service) WithAgentSource(src types.WorkspaceAgents) *Service {
	s.agents = src
	return s
}

func (s *Service) WithAgentAugment(fn AgentAugmentFunc) *Service {
	if s.rt != nil {
		s.rt.SetAgentAugment(fn)
	}
	return s
}

func (s *Service) WithPromptCheck(fn PromptCheck) *Service {
	s.hasPrompt = fn
	return s
}

func (s *Service) PromptReady(ctx context.Context, c *chat.Chat) error {
	if s.hasPrompt == nil {
		return nil
	}
	at, err := s.AgentType(c.WorkspaceID, c.AgentType)
	if err != nil {
		// Deliberately nil: the turn reports an unresolvable agent with its own error.
		return nil
	}
	stage := string(types.StageChat)
	if s.hasPrompt(ctx, c.WorkspaceID, at.Name, stage) {
		return nil
	}
	return apperr.Precondition("chatruntime.errors.no_prompt", "agent", at.Name, "stage", stage)
}

func (s *Service) Store() *chat.Store { return s.store }

func (s *Service) Runtime() *Manager { return s.rt }

func (s *Service) AgentType(workspaceID, name string) (*types.AgentType, error) {
	discovered, err := s.allAgentTypes(workspaceID)
	if err != nil {
		return nil, apperr.Wrap(err, "chatruntime.errors.discover_agents")
	}
	for i := range discovered {
		if discovered[i].Type == name || discovered[i].Name == name {
			at := discovered[i]
			if !at.SupportsChat() {
				return nil, ErrNotChatCapable
			}
			return &at, nil
		}
	}
	return nil, apperr.NotFound("chatruntime.errors.agent_type_not_found", "name", name)
}

func (s *Service) allAgentTypes(workspaceID string) ([]types.AgentType, error) {
	if s.agents != nil {
		return s.agents.For(workspaceID).All()
	}
	return types.Discover(s.agentsDir)
}

func (s *Service) CreateChat(ctx context.Context, workspaceID, agentType, taskID, title string, by chat.Creator) (*chat.Chat, error) {
	at, err := s.AgentType(workspaceID, agentType)
	if err != nil {
		return nil, err
	}
	if title == "" {
		title = capitalize(at.Name)
	}
	c := &chat.Chat{
		WorkspaceID: workspaceID,
		AgentType:   at.Type,
		TaskID:      taskID,
		Title:       title,
		CreatedBy:   by.Name,
		Owner:       by.Owner,
	}
	if err := s.store.CreateChat(ctx, c); err != nil {
		return nil, err
	}
	s.bus.Publish(chat.Event{Type: chat.EventChatCreated, Chat: c}.For(c))
	return c, nil
}

func (s *Service) OpenChat(
	ctx context.Context, workspaceID, agentType, taskID, taskTitle, title string, by chat.Creator,
) (c *chat.Chat, reused bool, err error) {
	at, err := s.AgentType(workspaceID, agentType)
	if err != nil {
		return nil, false, err
	}
	if taskID != "" {
		live, ferr := s.store.FindLive(ctx, &chat.Chat{
			WorkspaceID: workspaceID, AgentType: at.Type, TaskID: taskID, CreatedBy: by.Name, Owner: by.Owner,
		})
		if ferr == nil {
			return live, true, nil
		}
		if !errors.Is(ferr, chat.ErrNotFound) {
			return nil, false, ferr
		}
		if title == "" {
			title = taskChatTitle(i18n.FromContext(ctx), at.Name, taskTitle, false)
		}
	}
	c, err = s.CreateChat(ctx, workspaceID, at.Type, taskID, title, by)
	return c, false, err
}

func taskChatTitle(locale, agent, taskTitle string, thread bool) string {
	title := capitalize(agent)
	if strings.TrimSpace(taskTitle) != "" {
		title += " · " + truncateTitle(strings.TrimSpace(taskTitle))
	}
	if thread {
		title = i18n.T(locale, "chatruntime.labels.thread_chat_title", "title", title)
	}
	return title
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (s *Service) FindOrCreateForTask(ctx context.Context, workspaceID, agentType, taskID, threadID, taskTitle string) (*chat.Chat, error) {
	at, err := s.AgentType(workspaceID, agentType)
	if err != nil {
		return nil, err
	}
	if taskID == "" {
		taskTitle = ""
	}
	return s.store.FindOrCreate(ctx, &chat.Chat{
		WorkspaceID: workspaceID, AgentType: at.Type, TaskID: taskID, ThreadID: threadID,
		Title: taskChatTitle(i18n.FromContext(ctx), at.Name, taskTitle, threadID != ""), CreatedBy: chat.SystemCreator,
	})
}

func truncateTitle(s string) string {
	const maxLen = 40
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return strings.TrimSpace(string(r[:maxLen])) + "…"
}

func (s *Service) PostMessage(ctx context.Context, chatID, author, text string) (*chat.Message, error) {
	return s.PostMessageWith(ctx, chatID, author, text, nil)
}

func (s *Service) PostMessageWith(ctx context.Context, chatID, author, text string, meta map[string]string) (*chat.Message, error) {
	c, err := s.store.GetChat(ctx, chatID)
	if err != nil {
		return nil, err
	}
	if c.Archived() {
		return nil, ErrArchived
	}
	userMsg, err := s.startTurn(ctx, c, author, text, meta)
	if err != nil {
		return nil, err
	}

	// The turn outlives the HTTP request; a browser navigating away must not cancel it.
	// ExecTurn caps wall-clock via chat.turn_timeout_seconds.
	turnCtx := context.WithoutCancel(ctx)
	go func() {
		defer s.rt.EndTurn(c.ID)
		_, err := s.runTurnLocked(turnCtx, c, text, userMsg.ID, nil)
		logx.Warn(err, "chatruntime.log.async_turn", "chat", c.ID)
	}()
	return userMsg, nil
}

func (s *Service) RunTurnSync(ctx context.Context, c *chat.Chat, text string, extraMeta map[string]string) (*chat.Message, error) {
	userMsg, err := s.startTurn(ctx, c, "system", text, extraMeta)
	if err != nil {
		return nil, err
	}
	defer s.rt.EndTurn(c.ID)
	return s.runTurnLocked(ctx, c, text, userMsg.ID, extraMeta)
}

const turnWaitInterval = 2 * time.Second

func (s *Service) RunTurnQueued(ctx context.Context, c *chat.Chat, text string, extraMeta map[string]string) (*chat.Message, error) {
	for {
		msg, err := s.RunTurnSync(ctx, c, text, extraMeta)
		if !errors.Is(err, ErrTurnInFlight) {
			return msg, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(turnWaitInterval):
		}
	}
}

// On error the turn slot is already released; the caller owns EndTurn only when a message is
// returned.
func (s *Service) startTurn(ctx context.Context, c *chat.Chat, author, text string, meta map[string]string) (*chat.Message, error) {
	if err := s.rt.BeginTurn(c.ID); err != nil {
		return nil, err
	}
	// Read under the slot, not from the caller's copy: Delegate writes under the same slot, so this
	// sees any delegation that could reach this turn.
	fresh, err := s.store.GetChat(ctx, c.ID)
	if err != nil {
		s.rt.EndTurn(c.ID)
		return nil, err
	}
	if fresh.Delegated(time.Now()) && author != fresh.DelegatedBy {
		s.rt.EndTurn(c.ID)
		return nil, ErrNotDelegator
	}
	if err = s.PromptReady(ctx, fresh); err != nil {
		s.rt.EndTurn(c.ID)
		return nil, err
	}
	userMsg, err := s.store.AddMessage(ctx, &chat.Message{
		ChatID: c.ID, Role: chat.RoleUser, Author: author, Text: text, Metadata: meta,
	})
	if err != nil {
		s.rt.EndTurn(c.ID)
		return nil, err
	}
	s.publishMessage(c, userMsg)
	s.bus.Publish(chat.Event{Type: chat.EventTurnStarted, TurnID: userMsg.ID}.For(c))
	return userMsg, nil
}

// Caller holds the chat's single-flight turn slot.
func (s *Service) runTurnLocked(ctx context.Context, c *chat.Chat, text, turnID string, extraMeta map[string]string) (*chat.Message, error) {
	at, err := s.AgentType(c.WorkspaceID, c.AgentType)
	if err != nil {
		return s.failTurn(ctx, c, turnID, err)
	}
	opts := AcquireOpts{}
	if c.TaskID != "" && s.code != nil {
		opts.Code = s.code(c.TaskID)
	}

	res, err := s.rt.ExecTurn(ctx, c, at, opts, text)
	if err != nil {
		return s.failTurn(ctx, c, turnID, err)
	}

	if res.SessionID != "" && res.SessionID != c.SessionID {
		logx.Warn(s.store.UpdateSessionID(ctx, c.ID, res.SessionID), "chatruntime.log.persist_session_id", "chat", c.ID)
		c.SessionID = res.SessionID
	}

	textOut := strings.TrimSpace(res.Text)
	if textOut == "" {
		textOut = i18n.Text("chatruntime.messages.empty_reply")
	}
	meta := map[string]string{}
	for k, v := range extraMeta {
		meta[k] = v
	}
	if res.RawLogPath != "" {
		meta["raw_log_path"] = res.RawLogPath
	}
	chat.EncodeTools(meta, res.Tools, maxTurnTools)
	msg, err := s.store.AddMessage(ctx, &chat.Message{
		ChatID: c.ID, Role: chat.RoleAssistant, Author: c.AgentType, Text: textOut,
		DurationMs: res.DurationMs, InputTokens: res.InputTokens, Metadata: meta,
	})
	if err != nil {
		s.bus.Publish(chat.Event{Type: chat.EventTurnFailed, TurnID: turnID, Err: err.Error()}.For(c))
		return nil, err
	}
	s.publishMessage(c, msg)
	s.bus.Publish(chat.Event{Type: chat.EventTurnFinished, TurnID: turnID}.For(c))
	return msg, nil
}

func (s *Service) failTurn(ctx context.Context, c *chat.Chat, turnID string, cause error) (*chat.Message, error) {
	log.Printf("[chat] %s", i18n.Log("chatruntime.log.turn_failed", "chat", c.ID, "err", cause))
	failed := &chat.Message{
		ChatID: c.ID, Role: chat.RoleAssistant, Author: c.AgentType,
		Text:     i18n.Text("chatruntime.messages.agent_error", "error", apperr.Text(cause)),
		Metadata: map[string]string{chat.MetaError: "true"},
	}
	failed.SetTexts(errorTexts(cause))
	msg, addErr := s.store.AddMessage(ctx, failed)
	logx.Warn(addErr, "chatruntime.log.persist_turn_error", "chat", c.ID)
	if msg != nil {
		s.publishMessage(c, msg)
	}
	s.bus.Publish(chat.Event{Type: chat.EventTurnFailed, TurnID: turnID, Err: cause.Error()}.For(c))
	return msg, cause
}

func errorTexts(cause error) map[string]string {
	out := map[string]string{}
	for _, l := range i18n.Default().Locales() {
		out[l] = i18n.T(l, "chatruntime.messages.agent_error", "error", apperr.Localize(cause, l))
	}
	return out
}

func (s *Service) publishMessage(c *chat.Chat, m *chat.Message) {
	s.bus.Publish(chat.Event{Type: chat.EventMessageAdded, Message: m}.For(c))
}

func (s *Service) ResetChat(ctx context.Context, chatID string) error {
	c, err := s.store.GetChat(ctx, chatID)
	if err != nil {
		return err
	}
	if s.rt.TurnInFlight(chatID) {
		return ErrTurnInFlight
	}
	if err := s.store.ClearMessages(ctx, chatID); err != nil {
		return err
	}
	logx.Warn(s.store.UpdateSessionID(ctx, chatID, ""), "chatruntime.log.clear_session_id", "chat", chatID)
	if err := s.dropState(ctx, c); err != nil {
		return err
	}
	s.bus.Publish(chat.Event{Type: chat.EventChatUpdated, Chat: c}.For(c))
	return nil
}

func (s *Service) DeleteChat(ctx context.Context, chatID string) error {
	c, err := s.store.GetChat(ctx, chatID)
	if err != nil {
		return err
	}
	if err := s.dropState(ctx, c); err != nil {
		return err
	}
	if err := s.store.DeleteChat(ctx, chatID); err != nil {
		return err
	}
	// Also revokes keys minted before a daemon restart, which the manager does not track.
	logx.Warn(s.rt.EndDelegation(ctx, chatID), "chatruntime.log.revoke_deleted_chat_keys", "chat", chatID)
	s.bus.Publish(chat.Event{Type: chat.EventChatDeleted}.For(c))
	return nil
}

// Writes under the same turn slot startTurn reads the delegation under, so another person's turn is
// either refused or finished before the delegation exists.
func (s *Service) Delegate(ctx context.Context, chatID, by string) (*chat.Chat, error) {
	c, err := s.store.GetChat(ctx, chatID)
	if err != nil {
		return nil, err
	}
	switch {
	case c.Archived():
		return nil, ErrArchived
	case c.TaskID != "" || c.ThreadID != "":
		return nil, ErrNotDelegable
	}
	if err := s.rt.BeginTurn(chatID); err != nil {
		return nil, err
	}
	defer s.rt.EndTurn(chatID)
	if err := s.store.SetDelegation(ctx, chatID, by, time.Now().Add(chat.DelegationTTL)); err != nil {
		return nil, err
	}
	return s.publishUpdated(ctx, chatID)
}

func (s *Service) EndDelegation(ctx context.Context, chatID string) (*chat.Chat, error) {
	if err := s.store.ClearDelegation(ctx, chatID); err != nil {
		return nil, err
	}
	if err := s.rt.EndDelegation(ctx, chatID); err != nil {
		return nil, apperr.Wrap(err, "chatruntime.errors.revoke_keys")
	}
	return s.publishUpdated(ctx, chatID)
}

func (s *Service) EndWorkspaceDelegations(ctx context.Context, workspaceID string) error {
	chats, err := s.store.ListDelegated(ctx, workspaceID)
	if err != nil {
		return err
	}
	var errs []error
	for i := range chats {
		if _, err := s.EndDelegation(ctx, chats[i].ID); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", chats[i].ID, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) publishUpdated(ctx context.Context, chatID string) (*chat.Chat, error) {
	c, err := s.store.GetChat(ctx, chatID)
	if err != nil {
		return nil, err
	}
	s.bus.Publish(chat.Event{Type: chat.EventChatUpdated, Chat: c}.For(c))
	return c, nil
}

func (s *Service) EvictTaskChats(ctx context.Context, taskID string) {
	chats, err := s.store.ListChats(ctx, "", taskID, "", true)
	if err != nil {
		log.Printf("[chat] %s", i18n.Log("chatruntime.log.list_task_chats_failed", "task", taskID, "err", err))
		return
	}
	for i := range chats {
		logx.Warn(s.rt.Evict(ctx, chats[i].ID), "chatruntime.log.evict_task_chat", "chat", chats[i].ID)
	}
}

func (s *Service) PurgeWorkspace(ctx context.Context, workspaceID string) error {
	chats, err := s.store.ListChats(ctx, workspaceID, "", "", true)
	if err != nil {
		return err
	}
	for i := range chats {
		logx.Warn(s.dropState(ctx, &chats[i]), "chatruntime.log.drop_state", "chat", chats[i].ID)
	}
	return s.store.DeleteWorkspaceChats(ctx, workspaceID)
}

// chat-turn.sh appends each turn to /workspace/runs/<chat>.jsonl; in bind mode that is the agent
// dir's runs/ on the daemon's disk and outlives the container.
func (s *Service) dropState(ctx context.Context, c *chat.Chat) error {
	if err := s.rt.DropState(ctx, c.ID); err != nil {
		return err
	}
	s.removeRawLog(c)
	return nil
}

func (s *Service) removeRawLog(c *chat.Chat) {
	if c.ID == "" || filepath.Base(c.ID) != c.ID {
		return
	}
	all, err := s.allAgentTypes(c.WorkspaceID)
	if err != nil {
		log.Printf("[chat] %s", i18n.Log("chatruntime.log.raw_log_list_agents_failed", "chat", c.ID, "err", err))
		return
	}
	for i := range all {
		if all[i].Type != c.AgentType && all[i].Name != c.AgentType {
			continue
		}
		if all[i].Dir == "" {
			return
		}
		logx.Warn(safepath.RemoveAll(filepath.Join(all[i].Dir, "runs"), c.ID+".jsonl"), "chatruntime.log.remove_raw_log", "chat", c.ID)
		return
	}
}

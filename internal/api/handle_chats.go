package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/chatruntime"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
	"github.com/MisterDonnuts/thulu/internal/webhook"

	// Registers the claude-code engine so chat turns resolve the default engine even if no task
	// agent was ever launched.
	_ "github.com/MisterDonnuts/thulu/internal/engine/claudecode"
)

type chatView struct {
	chat.Chat
	Archived           bool   `json:"archived"`
	Status             string `json:"status"`
	LastMessagePreview string `json:"last_message_preview,omitempty"`
	LastMessageAt      string `json:"last_message_at,omitempty"`
	TaskTitle          string `json:"task_title,omitempty"`
	TaskBoardID        string `json:"task_board_id,omitempty"`
}

type chatTaskRef struct{ title, boardID string }

type chatTasks struct {
	s    *Server
	seen map[string]chatTaskRef
}

func (s *Server) newChatTasks() *chatTasks {
	return &chatTasks{s: s, seen: map[string]chatTaskRef{}}
}

func (ct *chatTasks) get(ctx context.Context, id string) chatTaskRef {
	if id == "" {
		return chatTaskRef{}
	}
	if ref, ok := ct.seen[id]; ok {
		return ref
	}
	var ref chatTaskRef
	if t, err := ct.s.taskStore.GetRow(ctx, id); err == nil && t != nil {
		ref = chatTaskRef{title: t.Title, boardID: t.BoardID}
	}
	ct.seen[id] = ref
	return ref
}

func (s *Server) chatView(ctx context.Context, c *chat.Chat, withPreview bool) chatView {
	return s.chatViewWith(ctx, c, withPreview, s.newChatTasks())
}

func (s *Server) chatViewWith(ctx context.Context, c *chat.Chat, withPreview bool, tasks *chatTasks) chatView {
	v := chatView{Chat: *c, Archived: c.Archived(), Status: "idle"}
	if s.chatSvc.Runtime().TurnInFlight(c.ID) {
		v.Status = "running"
	}
	ref := tasks.get(ctx, c.TaskID)
	v.TaskTitle, v.TaskBoardID = ref.title, ref.boardID
	if withPreview {
		if last, err := s.chatSvc.Store().LastMessage(ctx, c.ID); err == nil && last != nil {
			v.LastMessagePreview = chatPreview(last.InLocale(i18n.FromContext(ctx)).Text)
			v.LastMessageAt = last.CreatedAt.Format(time.RFC3339)
		}
	}
	return v
}

// The drawer renders markdown before cutting to one line; 600 runes lets opening markup ("**Cambio
// de criterio:** …") close inside the preview.
const previewRunes = 600

func chatPreview(text string) string {
	if utf8.RuneCountInString(text) <= previewRunes {
		return text
	}
	return string([]rune(text)[:previewRunes]) + "…"
}

func (s *Server) chatRequired(w http.ResponseWriter, r *http.Request) bool {
	if s.chatSvc == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.chats.errors.not_configured")
		return false
	}
	return true
}

func (s *Server) handleCreateChat(w http.ResponseWriter, r *http.Request) {
	if !s.chatRequired(w, r) {
		return
	}
	var body struct {
		AgentType string `json:"agent_type"`
		TaskID    string `json:"task_id"`
		Title     string `json:"title"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.AgentType) == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.chats.errors.agent_type_required")
		return
	}
	var linked *task.Task
	if body.TaskID != "" {
		t, ok := s.taskRowForCaller(w, r, body.TaskID)
		if !ok {
			return
		}
		linked = t
	}
	wid, ok := s.chatWorkspace(w, r, linked)
	if !ok {
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}

	taskTitle := ""
	if linked != nil {
		taskTitle = linked.Title
	}
	c, reused, err := s.chatSvc.OpenChat(r.Context(), wid, strings.TrimSpace(body.AgentType), body.TaskID,
		taskTitle, strings.TrimSpace(body.Title), chatCreator(r, author))
	if err != nil {
		status := http.StatusBadRequest
		if !errors.Is(err, chatruntime.ErrNotChatCapable) && apperr.KindOf(err) != apperr.KindNotFound {
			status = http.StatusInternalServerError
		}
		writeError(w, r, status, err)
		return
	}
	if reused {
		writeJSON(w, http.StatusOK, struct {
			chatView
			Reused bool `json:"reused"`
		}{s.chatView(r.Context(), c, true), true})
		return
	}
	s.emit(r, audit.KindChatCreated, "", c.ID, "chat",
		i18n.Text("api.chats.audit.created", "agent", c.AgentType), nil)
	writeJSON(w, http.StatusCreated, s.chatView(r.Context(), c, false))
}

// Must match the workspace GET /types listed the agent from (targetWorkspace); newResourceWorkspace
// 400s
// on dev-mode installs with zero or two workspaces, and the chat could not be opened.
func (s *Server) chatWorkspace(w http.ResponseWriter, r *http.Request, linked *task.Task) (string, bool) {
	if linked != nil && linked.WorkspaceID != "" {
		return linked.WorkspaceID, true
	}
	return s.targetWorkspace(w, r)
}

func (s *Server) handleListChats(w http.ResponseWriter, r *http.Request) {
	if !s.chatRequired(w, r) {
		return
	}
	includeArchived := r.URL.Query().Get("include_archived") == "true"
	wid, unrestricted := s.callerWorkspace(r)
	if !unrestricted && wid == "" {
		writeJSON(w, http.StatusOK, []chatView{})
		return
	}
	// The store reads "" as every workspace; only an unrestricted caller may pass it.
	filter := wid
	if unrestricted {
		filter = ""
	}
	chats, err := s.chatSvc.Store().ListListed(r.Context(), filter, chatViewer(r), includeArchived)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	runScope := auth.APIKeyRunScope(r.Context())
	out := []chatView{} // never null: the drawer iterates unconditionally
	tasks := s.newChatTasks()
	for i := range chats {
		if !workspaceVisible(chats[i].WorkspaceID, wid, unrestricted) {
			continue
		}
		if runScope.IsRun() && chats[i].ID != runScope.ChatID {
			continue
		}
		out = append(out, s.chatViewWith(r.Context(), &chats[i], true, tasks))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetChat(w http.ResponseWriter, r *http.Request) {
	if !s.chatRequired(w, r) {
		return
	}
	c, ok := s.chatForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.chatView(r.Context(), c, true))
}

func (s *Server) handlePatchChat(w http.ResponseWriter, r *http.Request) {
	if !s.chatRequired(w, r) {
		return
	}
	c, ok := s.chatForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	var body struct {
		Title    *string `json:"title"`
		Archived *bool   `json:"archived"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	store := s.chatSvc.Store()
	if body.Title != nil && strings.TrimSpace(*body.Title) != "" {
		if err := store.RenameChat(r.Context(), c.ID, strings.TrimSpace(*body.Title)); err != nil {
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
	}
	if body.Archived != nil {
		if err := store.SetArchived(r.Context(), c.ID, *body.Archived); err != nil {
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
	}
	updated, err := store.GetChat(r.Context(), c.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.chatBus.Publish(chat.Event{Type: chat.EventChatUpdated, Chat: updated}.For(updated))
	writeJSON(w, http.StatusOK, s.chatView(r.Context(), updated, true))
}

func (s *Server) handleDeleteChat(w http.ResponseWriter, r *http.Request) {
	if !s.chatRequired(w, r) {
		return
	}
	c, ok := s.chatForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if err := s.chatSvc.DeleteChat(r.Context(), c.ID); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.emit(r, audit.KindChatDeleted, "", c.ID, "chat",
		i18n.Text("api.chats.audit.deleted", "agent", c.AgentType), nil)
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) handleResetChat(w http.ResponseWriter, r *http.Request) {
	if !s.chatRequired(w, r) {
		return
	}
	c, ok := s.chatForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if err := s.chatSvc.ResetChat(r.Context(), c.ID); err != nil {
		if errors.Is(err, chatruntime.ErrTurnInFlight) {
			writeErrKey(w, r, http.StatusConflict, "api.chats.errors.turn_running_retry")
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.emit(r, audit.KindChatReset, "", c.ID, "chat",
		i18n.Text("api.chats.audit.reset", "agent", c.AgentType), nil)
	writeJSON(w, http.StatusOK, map[string]bool{"reset": true})
}

func (s *Server) handleListChatMessages(w http.ResponseWriter, r *http.Request) {
	if !s.chatRequired(w, r) {
		return
	}
	c, ok := s.chatForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	limit := 200
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	msgs, err := s.chatSvc.Store().ListMessages(r.Context(), c.ID, limit, r.URL.Query().Get("before"))
	if err != nil {
		if errors.Is(err, chat.ErrNotFound) {
			writeErrKey(w, r, http.StatusBadRequest, "api.chats.errors.unknown_before_id")
			return
		}
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	loc := i18n.RequestLocale(r)
	for i := range msgs {
		msgs[i] = *msgs[i].InLocale(loc)
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (s *Server) handlePostChatMessage(w http.ResponseWriter, r *http.Request) {
	if !s.chatRequired(w, r) {
		return
	}
	c, ok := s.chatForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.chats.errors.text_required")
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}

	msg, err := s.chatSvc.PostMessage(r.Context(), c.ID, author.Name, body.Text)
	if err != nil {
		switch {
		case errors.Is(err, chatruntime.ErrTurnInFlight):
			writeErrKey(w, r, http.StatusConflict, "api.chats.errors.turn_already_running")
		case errors.Is(err, chatruntime.ErrArchived):
			writeErrKey(w, r, http.StatusConflict, "api.chats.errors.archived")
		case errors.Is(err, chatruntime.ErrNotDelegator):
			writeErrKey(w, r, http.StatusForbidden, "api.chats.errors.delegated_write_only", "person", c.DelegatedBy)
		case apperr.KindOf(err) == apperr.KindPrecondition:
			writeError(w, r, http.StatusPreconditionFailed, err)
		default:
			writeError(w, r, http.StatusInternalServerError, err)
		}
		return
	}
	s.emit(r, audit.KindChatMessagePosted, webhook.EventChatMessage, msg.ID, "chat_message",
		i18n.Text("api.chats.audit.message_posted", "agent", c.AgentType),
		map[string]string{"chat_id": c.ID, "agent_type": c.AgentType})
	writeJSON(w, http.StatusAccepted, map[string]any{"message": msg, "turn_id": msg.ID})
}

const chatMentionWait = time.Hour

const maxThreadTranscriptBytes = 16 << 10

func (s *Server) answerMentionByChat(ctx context.Context, t *task.Task, at *types.AgentType, comment *task.Comment) {
	thread := comment.ThreadID()
	notice := func(n task.Note) {
		_, err := s.taskStore.AddNote(ctx, t.ID, thread, task.SourceSystem, "octopus", n)
		logx.Warn(err, "api.chats.log.post_notice_failed", "agent", at.Name, "task", t.ID)
	}
	if s.chatSvc == nil || s.docker == nil {
		notice(task.NoteOf("api.chats.comments.mention_unavailable", "agent", at.Name))
		return
	}
	c, err := s.chatSvc.FindOrCreateForTask(ctx, t.WorkspaceID, at.Type, t.ID, thread, t.Title)
	if err != nil {
		notice(task.NoteOf("api.chats.comments.mention_failed", "agent", at.Name, "err", err))
		return
	}
	if err = s.chatSvc.PromptReady(ctx, c); err != nil {
		notice(task.NoteOf("api.chats.comments.mention_failed", "agent", at.Name, "err", err))
		return
	}
	// Must be read before the mention_started notice lands in the thread.
	var unseen []task.Comment
	if th, ok := s.taskThread(ctx, t.ID, thread); ok {
		unseen = th.Since(at.Name)
	}

	// Uses the same mention_started/mention_finished events as a mention container so the typing
	// indicator tracks it.
	instanceID := task.MentionInstanceID(at.Name, t.ID, thread)
	notice(task.NoteOf("api.chats.comments.mention_started", "agent", at.Name, "instance", instanceID))
	logx.Warn(s.taskStore.RecordEvent(ctx, t.ID, task.EventMentionStarted, instanceID, "agent:"+at.Name,
		i18n.Text("api.chats.comments.mention_started_event", "agent", at.Name),
		map[string]string{"instance_id": instanceID, "agent": at.Name, "comment_id": comment.ID, "thread_id": thread}),
		"api.chats.log.record_mention_started_failed", "task", t.ID)

	waitCtx, cancel := context.WithTimeout(ctx, chatMentionWait)
	defer cancel()
	msg, err := s.chatSvc.RunTurnQueued(waitCtx, c, buildMentionChatMessage(ctx, t, s.stageName(ctx, t), unseen, comment), map[string]string{
		"source":     "task_mention",
		"task_id":    t.ID,
		"comment_id": comment.ID,
		"thread_id":  thread,
	})

	var reply *task.Comment
	switch {
	case err != nil:
		notice(task.NoteOf("api.chats.comments.mention_failed", "agent", at.Name, "err", err))
	case msg != nil && strings.TrimSpace(msg.Text) != "":
		var addErr error
		reply, addErr = s.taskStore.AddToThread(ctx, t.ID, thread, task.SourceAgent, at.Name, msg.Text)
		logx.Warn(addErr, "api.chats.log.add_reply_failed", "agent", at.Name, "task", t.ID)
	}
	posted := reply != nil

	logx.Warn(s.taskStore.RecordEvent(ctx, t.ID, task.EventMentionFinished, instanceID, "agent:"+at.Name,
		i18n.Text("api.chats.comments.mention_finished_event", "agent", at.Name, "posted", posted),
		map[string]any{"instance_id": instanceID, "agent": at.Name, "thread_id": thread, "posted": posted}),
		"api.chats.log.record_mention_finished_failed", "task", t.ID)
	s.dispatchMentions(ctx, t.ID, reply)
}

func (s *Server) taskThread(ctx context.Context, taskID, threadID string) (task.Thread, bool) {
	comments, err := s.taskStore.ListComments(ctx, taskID)
	if err != nil {
		logx.Warn(err, "api.chats.log.list_comments_failed", "task", taskID)
		return task.Thread{}, false
	}
	return task.FindThread(task.Threads(comments), threadID)
}

func buildMentionChatMessage(ctx context.Context, t *task.Task, stage string, unseen []task.Comment, comment *task.Comment) string {
	loc := i18n.FromContext(ctx)
	var b strings.Builder
	b.WriteString(i18n.TCtx(ctx, "api.chats.prompts.mention_header", "task", t.ID, "title", t.Title))
	b.WriteString(i18n.TCtx(ctx, "api.chats.prompts.mention_stage", "stage", stage))
	if t.Body != "" {
		b.WriteString(i18n.TCtx(ctx, "api.chats.prompts.mention_description", "body", t.Body))
	}
	if text := task.TranscriptIn(loc, unseen, comment.ID, maxThreadTranscriptBytes); text != "" {
		b.WriteString(i18n.TCtx(ctx, "api.chats.prompts.mention_unseen", "transcript", text))
	}
	b.WriteString(i18n.TCtx(ctx, "api.chats.prompts.mention_comment", "speaker", comment.SpeakerIn(loc), "body", comment.Body))
	return b.String()
}

func (s *Server) stageName(ctx context.Context, t *task.Task) string {
	if info, ok := s.stagesFor(t).Get(t.Stage); ok {
		if l := stagepkg.LocalizeSeeded([]types.StageInfo{info}, i18n.FromContext(ctx))[0].Label; l != "" {
			return l
		}
	}
	return string(t.Stage)
}

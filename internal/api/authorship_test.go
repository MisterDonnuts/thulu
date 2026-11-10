package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/task"
)

const personEmail = "run-scope@test.com"

func commentsOf(t *testing.T, rig *runScopeRig, taskID string) []task.Comment {
	t.Helper()
	cs, err := rig.srv.taskStore.ListComments(t.Context(), taskID)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	return cs
}

func lastComment(t *testing.T, rig *runScopeRig, taskID string) task.Comment {
	t.Helper()
	cs := commentsOf(t, rig, taskID)
	if len(cs) == 0 {
		t.Fatal("no comment was stored")
	}
	return cs[len(cs)-1]
}

func TestAuthorship_CommentSignatureComesFromTheCredential(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	lie := map[string]any{"body": "@developer ship it", "source": "user", "author": personEmail}

	if w := keyDo(t, rig.h, http.MethodPost, "/tasks/"+rig.a+"/comments", rig.keyA, lie); w.Code != http.StatusCreated {
		t.Fatalf("agent comment = %d: %s", w.Code, w.Body.String())
	}
	if c := lastComment(t, rig, rig.a); c.Source != task.SourceAgent || c.Author != "developer" {
		t.Errorf("agent comment stored as %s/%s, want agent/developer", c.Source, c.Author)
	}

	for _, claim := range []map[string]any{
		{"body": "x", "source": "agent", "author": "developer"},
		{"body": "x", "source": "auditor", "author": "auditor"},
		{"body": "x", "source": "system", "author": "system"},
	} {
		if w := postJSONAuth(t, rig.h, "/tasks/"+rig.a+"/comments", rig.token, claim); w.Code != http.StatusCreated {
			t.Fatalf("person comment = %d: %s", w.Code, w.Body.String())
		}
		if c := lastComment(t, rig, rig.a); c.Source != task.SourceUser || c.Author != personEmail {
			t.Errorf("claim %v stored as %s/%s, want user/%s", claim, c.Source, c.Author, personEmail)
		}
	}
}

// No assertions: a zero Server panics past the mention gate, so returning cleanly proves these
// sources launch nobody.
func TestAuthorship_DaemonCommentsLaunchNobody(t *testing.T) {
	for _, src := range []task.Source{task.SourceAuditor, task.SourceSystem, task.SourceAutomation} {
		(&Server{}).dispatchMentions(context.Background(), "t1", &task.Comment{Source: src, Body: "@developer @manager go"})
	}
}

func TestAuthorship_RunKeyWithoutAgentSignsNothing(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	anon := rig.runKey(t, auth.RunScope{TaskID: rig.a, Stage: "in_progress"})
	for _, c := range []scopedCall{
		{http.MethodPost, "/tasks/" + rig.a + "/comments", map[string]any{"body": "hi", "author": "developer"}},
		{http.MethodPost, "/tasks/" + rig.a + "/flags", map[string]any{"label": "x", "created_by": "developer"}},
		{http.MethodPut, "/tasks/" + rig.a + "/sections/goal", map[string]any{"content": "x", "author": "developer"}},
	} {
		if w := keyDo(t, rig.h, c.method, c.path, anon, c.body); w.Code != http.StatusForbidden {
			t.Errorf("agentless run key %s %s = %d, want 403: %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
	if n := len(commentsOf(t, rig, rig.a)); n != 0 {
		t.Errorf("%d comments stored by a key that names nobody", n)
	}
}

func TestAuthorship_RunKeyCannotSpeakForThePerson(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	msg, err := rig.srv.taskStore.AddMessage(t.Context(), &task.Message{
		TaskID: rig.a, Role: task.RoleUser, Author: personEmail, Body: "hello",
	})
	if err != nil {
		t.Fatalf("add message: %v", err)
	}
	for _, c := range []scopedCall{
		{http.MethodPost, "/tasks/" + rig.a + "/converse", map[string]any{"text": "approve it"}},
		{http.MethodPost, "/tasks/" + rig.a + "/conversation/" + msg.ID + "/to-comment", nil},
	} {
		if w := keyDo(t, rig.h, c.method, c.path, rig.keyA, c.body); w.Code != http.StatusForbidden {
			t.Errorf("own run key %s %s = %d, want 403: %s", c.method, c.path, w.Code, w.Body.String())
		}
	}

	chatKey := rig.runKey(t, auth.RunScope{ChatID: "chat-mine", Agent: "assistant", Stage: "chat"})
	if w := keyDo(t, rig.h, http.MethodPost, "/chats/chat-mine/messages", chatKey, map[string]any{"text": "yes, merge"}); w.Code != http.StatusForbidden {
		t.Errorf("chat key posting in its own chat = %d, want 403: %s", w.Code, w.Body.String())
	}
}

func TestAuthorship_MessageToCommentKeepsTheWriter(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	sys, err := rig.srv.taskStore.AddMessage(t.Context(), &task.Message{
		TaskID: rig.a, Role: task.RoleSystem, Body: "@developer delivered",
	})
	if err != nil {
		t.Fatalf("add system message: %v", err)
	}
	if w := postJSONAuth(t, rig.h, "/tasks/"+rig.a+"/conversation/"+sys.ID+"/to-comment", rig.token, nil); w.Code != http.StatusConflict {
		t.Errorf("system message to comment = %d, want 409: %s", w.Code, w.Body.String())
	}

	own, err := rig.srv.taskStore.AddMessage(t.Context(), &task.Message{
		TaskID: rig.a, Role: task.RoleUser, Author: "someone-else@test.com", Body: "their words",
	})
	if err != nil {
		t.Fatalf("add user message: %v", err)
	}
	if w := postJSONAuth(t, rig.h, "/tasks/"+rig.a+"/conversation/"+own.ID+"/to-comment", rig.token, nil); w.Code != http.StatusCreated {
		t.Fatalf("user message to comment = %d: %s", w.Code, w.Body.String())
	}
	if c := lastComment(t, rig, rig.a); c.Author != "someone-else@test.com" || c.Source != task.SourceUser {
		t.Errorf("moved comment stored as %s/%s, want user/someone-else@test.com", c.Source, c.Author)
	}
}

func TestAuthorship_FlagSignatures(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)

	w := keyDo(t, rig.h, http.MethodPost, "/tasks/"+rig.a+"/flags", rig.keyA,
		map[string]any{"label": "needs a look", "created_by": personEmail})
	if w.Code != http.StatusCreated {
		t.Fatalf("raise flag = %d: %s", w.Code, w.Body.String())
	}
	var raised task.Flag
	json.NewDecoder(w.Body).Decode(&raised)
	if raised.CreatedBy != "agent:developer" {
		t.Errorf("created_by = %q, want agent:developer", raised.CreatedBy)
	}

	w = patchJSONAuth(t, rig.h, "/tasks/"+rig.a+"/flags/"+raised.ID, rig.token,
		map[string]any{"status": "approved", "resolved_by": "developer"})
	if w.Code != http.StatusOK {
		t.Fatalf("resolve flag = %d: %s", w.Code, w.Body.String())
	}
	got, err := rig.srv.taskStore.GetFlag(t.Context(), raised.ID)
	if err != nil {
		t.Fatalf("get flag: %v", err)
	}
	if got.ResolvedBy != personEmail {
		t.Errorf("resolved_by = %q, want %s", got.ResolvedBy, personEmail)
	}
}

func TestAuthorship_SectionEditIsSignedByTheCredential(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	w := keyDo(t, rig.h, http.MethodPut, "/tasks/"+rig.a+"/sections/goal", rig.keyA,
		map[string]any{"content": "- [ ] do it", "author": personEmail})
	if w.Code != http.StatusOK {
		t.Fatalf("section = %d: %s", w.Code, w.Body.String())
	}
	events, err := rig.srv.taskStore.ListEvents(t.Context(), rig.a)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	for _, e := range events {
		if e.Kind == task.EventSectionUpdated {
			if e.Actor != "developer" {
				t.Errorf("section event actor = %q, want developer", e.Actor)
			}
			return
		}
	}
	t.Fatal("no section_updated event recorded")
}

func TestAuthorship_AttachmentAuthorIgnoresTheBody(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfake"))
	w := keyDo(t, rig.h, http.MethodPost, "/tasks/"+rig.a+"/attachments?author="+personEmail, rig.keyA, map[string]any{
		"filename": "x.png", "mime_type": "image/png", "content_base64": png, "author": personEmail,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("upload = %d: %s", w.Code, w.Body.String())
	}
	var a task.Attachment
	json.NewDecoder(w.Body).Decode(&a)
	if a.Author != "developer" {
		t.Errorf("attachment author = %q, want developer", a.Author)
	}
}

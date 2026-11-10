package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/chat"
)

type delegationRig struct {
	*runScopeRig
	ownerEmail string
	chatID     string
	devToken   string
}

func newDelegationRig(t *testing.T) *delegationRig {
	t.Helper()
	rs := newRunScopeRig(t)
	rig := &delegationRig{runScopeRig: rs, ownerEmail: "run-scope@test.com"}
	c := &chat.Chat{WorkspaceID: rs.ws, AgentType: "admin", Title: "Admin", CreatedBy: rig.ownerEmail}
	if err := rs.srv.chatSvc.Store().CreateChat(t.Context(), c); err != nil {
		t.Fatalf("create chat: %v", err)
	}
	rig.chatID = c.ID

	u, err := rs.srv.authStore.CreateUser(t.Context(), "dev@test.com", "Dev")
	if err != nil {
		t.Fatalf("create dev: %v", err)
	}
	if err := rs.srv.authStore.AddMember(t.Context(), rs.ws, u.ID, auth.RoleDeveloper); err != nil {
		t.Fatalf("add dev: %v", err)
	}
	rig.devToken, err = auth.IssueJWT(auth.Claims{UserID: u.ID, Email: u.Email, WorkspaceID: rs.ws, Role: auth.RoleDeveloper})
	if err != nil {
		t.Fatalf("issue dev jwt: %v", err)
	}
	return rig
}

func tokenDo(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	if body == nil {
		b = nil
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func (rig *delegationRig) delegate(t *testing.T, token string, on bool) *httptest.ResponseRecorder {
	t.Helper()
	return tokenDo(t, rig.h, http.MethodPost, "/chats/"+rig.chatID+"/delegation", token, map[string]bool{"enabled": on})
}

func (rig *delegationRig) delegatedKey(t *testing.T) string {
	t.Helper()
	return rig.runKey(t, auth.RunScope{ChatID: rig.chatID, Agent: "admin", Stage: "chat", DelegatedBy: rig.ownerEmail})
}

func TestChatDelegation_OnlyTheCreatorTurnsItOn(t *testing.T) {
	t.Parallel()
	rig := newDelegationRig(t)

	// The chat is personal: other members and keys don't even see it.
	if w := rig.delegate(t, rig.devToken, true); w.Code != http.StatusNotFound {
		t.Errorf("another member delegating = %d, want 404: %s", w.Code, w.Body.String())
	}
	opKey, _, err := rig.srv.authStore.CreateAPIKey(t.Context(), "ops", rig.ownerEmail, auth.KeyGrant{
		WorkspaceID: rig.ws, Role: auth.RoleOwner, Permissions: []auth.Permission{auth.PermAll},
	})
	if err != nil {
		t.Fatalf("mint operator key: %v", err)
	}
	if w := keyDo(t, rig.h, http.MethodPost, "/chats/"+rig.chatID+"/delegation", opKey, map[string]bool{"enabled": true}); w.Code != http.StatusNotFound {
		t.Errorf("operator key delegating = %d, want 404: %s", w.Code, w.Body.String())
	}

	w := rig.delegate(t, rig.token, true)
	if w.Code != http.StatusOK {
		t.Fatalf("creator delegating = %d: %s", w.Code, w.Body.String())
	}
	var got chat.Chat
	json.NewDecoder(w.Body).Decode(&got)
	if got.DelegatedBy != rig.ownerEmail || got.DelegatedUntil == nil {
		t.Fatalf("delegation not recorded: %+v", got)
	}
	if left := time.Until(*got.DelegatedUntil); left > chat.DelegationTTL || left < chat.DelegationTTL-time.Minute {
		t.Errorf("delegation lasts %v, want %v", left, chat.DelegationTTL)
	}
}

func TestChatDelegation_TaskChatsCannotBeDelegated(t *testing.T) {
	t.Parallel()
	rig := newDelegationRig(t)
	c := &chat.Chat{WorkspaceID: rig.ws, AgentType: "admin", TaskID: rig.a, Title: "x", CreatedBy: rig.ownerEmail}
	if err := rig.srv.chatSvc.Store().CreateChat(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	w := tokenDo(t, rig.h, http.MethodPost, "/chats/"+c.ID+"/delegation", rig.token, map[string]bool{"enabled": true})
	if w.Code != http.StatusConflict {
		t.Errorf("delegating a task chat = %d, want 409: %s", w.Code, w.Body.String())
	}
}

func TestChatDelegation_OnlyTheDelegatorWrites(t *testing.T) {
	t.Parallel()
	rig := newDelegationRig(t)
	if w := rig.delegate(t, rig.token, true); w.Code != http.StatusOK {
		t.Fatalf("delegate: %d %s", w.Code, w.Body.String())
	}
	w := tokenDo(t, rig.h, http.MethodPost, "/chats/"+rig.chatID+"/messages", rig.devToken, map[string]string{"text": "delete everything"})
	if w.Code != http.StatusNotFound {
		t.Errorf("another member writing in a delegated chat = %d, want 404: %s", w.Code, w.Body.String())
	}
}

func TestChatDelegation_KeyChangesAnyTaskButNotFlagsOrChats(t *testing.T) {
	t.Parallel()
	rig := newDelegationRig(t)
	if w := rig.delegate(t, rig.token, true); w.Code != http.StatusOK {
		t.Fatalf("delegate: %d %s", w.Code, w.Body.String())
	}
	key := rig.delegatedKey(t)

	for _, id := range []string{rig.a, rig.b1} {
		if w := keyDo(t, rig.h, http.MethodPatch, "/tasks/"+id, key, map[string]any{"title": "moved by admin"}); w.Code != http.StatusOK {
			t.Errorf("delegated key PATCH /tasks/%s = %d: %s", id, w.Code, w.Body.String())
		}
	}
	if w := keyDo(t, rig.h, http.MethodPost, "/tasks", key, map[string]any{"title": "new from admin"}); w.Code != http.StatusCreated {
		t.Errorf("delegated key POST /tasks = %d: %s", w.Code, w.Body.String())
	}

	refused := []scopedCall{
		{http.MethodPatch, "/tasks/" + rig.a + "/flags/f1", map[string]any{"status": "resolved"}},
		{http.MethodDelete, "/tasks/" + rig.a + "/flags/f1", nil},
		{http.MethodPost, "/tasks/" + rig.a + "/threads/c1/resolve", nil},
		{http.MethodPost, "/tasks/" + rig.a + "/converse", map[string]any{"text": "hi"}},
		{http.MethodPost, "/chats/" + rig.chatID + "/messages", map[string]any{"text": "hi"}},
		{http.MethodPost, "/chats/" + rig.chatID + "/delegation", map[string]any{"enabled": true}},
		{http.MethodPost, "/chats", map[string]any{"agent_type": "admin"}},
		{http.MethodPost, "/boards", map[string]any{"name": "planted"}},
	}
	for _, c := range refused {
		if w := keyDo(t, rig.h, c.method, c.path, key, c.body); w.Code != http.StatusForbidden {
			t.Errorf("delegated key %s %s = %d, want 403: %s", c.method, c.path, w.Code, w.Body.String())
		}
	}

	otherToken, _ := mintWorkspaceUser(t, rig.srv, "other@test.com", "Other")
	other := taskIDFor(t, rig.h, otherToken, "Other's task")
	if w := keyDo(t, rig.h, http.MethodPatch, "/tasks/"+other, key, map[string]any{"title": "x"}); w.Code == http.StatusOK {
		t.Errorf("delegated key changed another workspace's task")
	}
}

func TestChatDelegation_DeletionWaitsForThePerson(t *testing.T) {
	t.Parallel()
	rig := newDelegationRig(t)
	if w := rig.delegate(t, rig.token, true); w.Code != http.StatusOK {
		t.Fatalf("delegate: %d %s", w.Code, w.Body.String())
	}
	key := rig.delegatedKey(t)

	w := keyDo(t, rig.h, http.MethodDelete, "/tasks/"+rig.b1, key, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("delegated DELETE = %d, want 202: %s", w.Code, w.Body.String())
	}
	var held struct {
		ActionID string `json:"action_id"`
	}
	json.NewDecoder(w.Body).Decode(&held)
	if g := getJSON(t, rig.h, "/tasks/"+rig.b1, rig.token); g.Code != http.StatusOK {
		t.Fatalf("task gone before anyone confirmed: %d", g.Code)
	}

	confirm := fmt.Sprintf("/chats/%s/actions/%s/confirm", rig.chatID, held.ActionID)
	if w := tokenDo(t, rig.h, http.MethodPost, confirm, rig.devToken, nil); w.Code != http.StatusNotFound {
		t.Errorf("another member confirming = %d, want 404: %s", w.Code, w.Body.String())
	}
	if w := keyDo(t, rig.h, http.MethodPost, confirm, key, nil); w.Code != http.StatusForbidden {
		t.Errorf("the chat's key confirming = %d, want 403: %s", w.Code, w.Body.String())
	}

	w = tokenDo(t, rig.h, http.MethodPost, confirm, rig.token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("person confirming = %d: %s", w.Code, w.Body.String())
	}
	var a chat.Action
	json.NewDecoder(w.Body).Decode(&a)
	if a.Status != chat.ActionConfirmed || a.ResultCode >= 400 {
		t.Fatalf("confirmed action = %+v", a)
	}
	if g := getJSON(t, rig.h, "/tasks/"+rig.b1, rig.token); g.Code != http.StatusNotFound {
		t.Errorf("task still there after confirm: %d", g.Code)
	}
	if w := tokenDo(t, rig.h, http.MethodPost, confirm, rig.token, nil); w.Code != http.StatusConflict {
		t.Errorf("second confirm = %d, want 409", w.Code)
	}
}

func TestChatDelegation_ConfirmRunsWithThePersonsRole(t *testing.T) {
	t.Parallel()
	rig := newDelegationRig(t)
	c := &chat.Chat{WorkspaceID: rig.ws, AgentType: "admin", Title: "Dev's", CreatedBy: "dev@test.com"}
	if err := rig.srv.chatSvc.Store().CreateChat(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if w := tokenDo(t, rig.h, http.MethodPost, "/chats/"+c.ID+"/delegation", rig.devToken, map[string]bool{"enabled": true}); w.Code != http.StatusOK {
		t.Fatalf("dev delegating own chat = %d: %s", w.Code, w.Body.String())
	}
	key := rig.runKey(t, auth.RunScope{ChatID: c.ID, Agent: "admin", Stage: "chat", DelegatedBy: "dev@test.com"})
	w := keyDo(t, rig.h, http.MethodDelete, "/tasks/"+rig.b, key, nil)
	var held struct {
		ActionID string `json:"action_id"`
	}
	json.NewDecoder(w.Body).Decode(&held)

	w = tokenDo(t, rig.h, http.MethodPost, fmt.Sprintf("/chats/%s/actions/%s/confirm", c.ID, held.ActionID), rig.devToken, nil)
	var a chat.Action
	json.NewDecoder(w.Body).Decode(&a)
	if a.Status != chat.ActionFailed || a.ResultCode != http.StatusForbidden {
		t.Errorf("developer confirming an admin deletion = %+v, want failed with 403", a)
	}
	if g := getJSON(t, rig.h, "/tasks/"+rig.b, rig.token); g.Code != http.StatusOK {
		t.Errorf("task deleted by a developer's confirmation: %d", g.Code)
	}
}

func TestChatDelegation_TurningOffCancelsAndRevokes(t *testing.T) {
	t.Parallel()
	rig := newDelegationRig(t)
	if w := rig.delegate(t, rig.token, true); w.Code != http.StatusOK {
		t.Fatalf("delegate: %d %s", w.Code, w.Body.String())
	}
	key := rig.delegatedKey(t)
	keyDo(t, rig.h, http.MethodDelete, "/tasks/"+rig.b, key, nil)

	if w := rig.delegate(t, rig.token, false); w.Code != http.StatusOK {
		t.Fatalf("turn off: %d %s", w.Code, w.Body.String())
	}
	if w := keyDo(t, rig.h, http.MethodPatch, "/tasks/"+rig.a, key, map[string]any{"title": "x"}); w.Code != http.StatusUnauthorized {
		t.Errorf("revoked delegated key = %d, want 401", w.Code)
	}
	acts, err := rig.srv.chatSvc.Store().ListActions(t.Context(), rig.chatID, chat.ActionPending)
	if err != nil || len(acts) != 0 {
		t.Errorf("pending actions after turning off = %v (%v), want none", acts, err)
	}
}

func TestChatDelegation_WorkspaceSwitch(t *testing.T) {
	t.Parallel()
	rig := newDelegationRig(t)
	if w := rig.delegate(t, rig.token, true); w.Code != http.StatusOK {
		t.Fatalf("delegate: %d %s", w.Code, w.Body.String())
	}
	key := rig.delegatedKey(t)

	if w := tokenDo(t, rig.h, http.MethodPut, "/chat-delegation", rig.token, map[string]bool{"enabled": false}); w.Code != http.StatusForbidden {
		t.Errorf("admin flipping the workspace switch = %d, want 403", w.Code)
	}
	u, err := rig.srv.authStore.GetUserByEmail(t.Context(), rig.ownerEmail)
	if err != nil {
		t.Fatal(err)
	}
	ownerToken, err := auth.IssueJWT(auth.Claims{UserID: u.ID, Email: u.Email, WorkspaceID: rig.ws, Role: auth.RoleOwner})
	if err != nil {
		t.Fatal(err)
	}
	if w := tokenDo(t, rig.h, http.MethodPut, "/chat-delegation", ownerToken, map[string]bool{"enabled": false}); w.Code != http.StatusOK {
		t.Fatalf("owner turning delegation off = %d: %s", w.Code, w.Body.String())
	}
	if w := keyDo(t, rig.h, http.MethodPatch, "/tasks/"+rig.a, key, map[string]any{"title": "x"}); w.Code != http.StatusUnauthorized {
		t.Errorf("delegated key after the workspace switch = %d, want 401", w.Code)
	}
	if w := rig.delegate(t, rig.token, true); w.Code != http.StatusForbidden {
		t.Errorf("delegating with the switch off = %d, want 403", w.Code)
	}
}

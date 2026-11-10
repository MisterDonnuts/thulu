package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

func TestEventStreamClosesWhenTheMemberIsRemoved(t *testing.T) {
	srv, h := newIntegrationServer(t)
	srv.streamRecheck = 20 * time.Millisecond
	_, wsID := mintWorkspaceUserAs(t, srv, "stream-owner@test.com", "StreamOwner", auth.RoleOwner)
	member, err := srv.authStore.CreateUser(t.Context(), "stream-member@test.com", "Member")
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.authStore.AddMember(t.Context(), wsID, member.ID, auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	token, err := auth.IssueJWT(auth.Claims{UserID: member.ID, Email: member.Email, WorkspaceID: wsID, Role: auth.RoleViewer})
	if err != nil {
		t.Fatal(err)
	}

	// The token_version bump must run last: the token is dead afterwards.
	cases := []struct {
		path   string
		revoke func() error
	}{
		{"/events?topics=tasks", func() error { return srv.authStore.RemoveMember(t.Context(), wsID, member.ID) }},
		{"/chats/events", func() error { return srv.authStore.BumpTokenVersion(t.Context(), member.ID) }},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			r := httptest.NewRequest(http.MethodGet, tc.path, nil).WithContext(ctx)
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				h.ServeHTTP(w, r)
				close(done)
			}()

			select {
			case <-done:
				t.Fatalf("stream closed while the member was still a member: %d %s", w.Code, w.Body.String())
			case <-time.After(150 * time.Millisecond):
			}

			if err := tc.revoke(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("stream still open after the member lost access")
			}
			if err := srv.authStore.AddMember(t.Context(), wsID, member.ID, auth.RoleViewer); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEventStreamClosesWhenTheKeyIsRevoked(t *testing.T) {
	srv, h := newIntegrationServer(t)
	srv.streamRecheck = 20 * time.Millisecond
	_, wsID := mintWorkspaceUserAs(t, srv, "stream-key-owner@test.com", "StreamKeyOwner", auth.RoleOwner)
	raw, meta, err := srv.authStore.CreateAPIKey(t.Context(), "stream key", "", auth.KeyGrant{
		WorkspaceID: wsID, Role: auth.RoleViewer, Permissions: []auth.Permission{auth.PermTasksRead},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	r := httptest.NewRequest(http.MethodGet, "/events?topics=tasks", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+raw)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(w, r)
		close(done)
	}()

	select {
	case <-done:
		t.Fatalf("stream closed while the key was valid: %d %s", w.Code, w.Body.String())
	case <-time.After(150 * time.Millisecond):
	}
	if err := srv.authStore.RevokeAPIKey(t.Context(), meta.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stream still open after its key was revoked")
	}
}

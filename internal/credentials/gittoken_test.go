package credentials

import (
	"context"
	"errors"
	"testing"
	"time"
)

func githubInput(name, token string) Input {
	return Input{
		Name: name, Provider: "github", Kind: "github_pat",
		Payload: map[string]string{"token": token},
	}
}

func grant(t *testing.T, s *Store, ws, id string, grants ...Grant) {
	t.Helper()
	if err := s.SetGrants(context.Background(), ws, id, grants); err != nil {
		t.Fatalf("SetGrants: %v", err)
	}
}

func TestGithubTokenForProjectPrefersTheProjectGrant(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	wide := mustCreate(t, s, "ws1", githubInput("workspace-wide", "ghp_wide"))
	grant(t, s, "ws1", wide.ID, Grant{SubjectType: SubjectWorkspace})

	got, err := s.GithubTokenForProject(ctx, "ws1", "proj-1")
	if err != nil || got.Token != "ghp_wide" {
		t.Fatalf("workspace grant: got %+v err=%v", got, err)
	}

	narrow := mustCreate(t, s, "ws1", githubInput("just-this-project", "ghp_narrow"))
	grant(t, s, "ws1", narrow.ID, Grant{SubjectType: SubjectProject, SubjectID: "proj-1"})

	got, err = s.GithubTokenForProject(ctx, "ws1", "proj-1")
	if err != nil || got.Token != "ghp_narrow" {
		t.Fatalf("project grant should win: got %+v err=%v", got, err)
	}
	if got.CredentialID != narrow.ID {
		t.Errorf("CredentialID = %q, want %q", got.CredentialID, narrow.ID)
	}

	got, err = s.GithubTokenForProject(ctx, "ws1", "proj-2")
	if err != nil || got.Token != "ghp_wide" {
		t.Fatalf("unrelated project: got %+v err=%v", got, err)
	}
}

func TestGithubTokenForProjectSkipsUnusableCredentials(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	dead := mustCreate(t, s, "ws1", githubInput("revoked", "ghp_dead"))
	grant(t, s, "ws1", dead.ID, Grant{SubjectType: SubjectProject, SubjectID: "proj-1"})
	if err := s.SetIntrospection(ctx, "ws1", dead.ID, Introspection{
		State: StateInvalid, Err: "Bad credentials",
	}); err != nil {
		t.Fatalf("SetIntrospection: %v", err)
	}

	if _, err := s.GithubTokenForProject(ctx, "ws1", "proj-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invalid credential: want ErrNotFound, got %v", err)
	}

	expired := mustCreate(t, s, "ws1", githubInput("expired", "ghp_old"))
	grant(t, s, "ws1", expired.ID, Grant{SubjectType: SubjectWorkspace})
	if err := s.SetIntrospection(ctx, "ws1", expired.ID, Introspection{
		State: StateValid, ExpiresAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("SetIntrospection: %v", err)
	}
	if _, err := s.GithubTokenForProject(ctx, "ws1", "proj-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired credential: want ErrNotFound, got %v", err)
	}

	off := mustCreate(t, s, "ws1", githubInput("disabled", "ghp_off"))
	grant(t, s, "ws1", off.ID, Grant{SubjectType: SubjectWorkspace})
	if err := s.SetDisabled(ctx, "ws1", off.ID, true); err != nil {
		t.Fatalf("SetDisabled: %v", err)
	}
	if _, err := s.GithubTokenForProject(ctx, "ws1", "proj-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled credential: want ErrNotFound, got %v", err)
	}
}

func TestGithubTokenForProjectAcceptsUnknownState(t *testing.T) {
	s := newTestStore(t)
	c := mustCreate(t, s, "ws1", githubInput("fine-grained", "github_pat_x"))
	grant(t, s, "ws1", c.ID, Grant{SubjectType: SubjectWorkspace})

	got, err := s.GithubTokenForProject(context.Background(), "ws1", "proj-1")
	if err != nil || got.Token != "github_pat_x" {
		t.Fatalf("unknown state: got %+v err=%v", got, err)
	}
}

func TestGithubTokenForProjectIgnoresOtherProviders(t *testing.T) {
	s := newTestStore(t)
	c := mustCreate(t, s, "ws1", awsInput("prod-aws"))
	grant(t, s, "ws1", c.ID, Grant{SubjectType: SubjectWorkspace})

	if _, err := s.GithubTokenForProject(context.Background(), "ws1", "proj-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("aws credential: want ErrNotFound, got %v", err)
	}
}

func TestGithubTokenForProjectStaysInsideTheWorkspace(t *testing.T) {
	s := newTestStore(t)
	c := mustCreate(t, s, "ws1", githubInput("theirs", "ghp_theirs"))
	grant(t, s, "ws1", c.ID, Grant{SubjectType: SubjectProject, SubjectID: "proj-1"})

	if _, err := s.GithubTokenForProject(context.Background(), "ws2", "proj-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace read: want ErrNotFound, got %v", err)
	}
}

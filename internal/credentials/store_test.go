package credentials

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(testpg.New(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func mustCreate(t *testing.T, s *Store, ws string, in Input) *Credential {
	t.Helper()
	c, err := s.Create(context.Background(), ws, in, "tester")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return c
}

func awsInput(name string) Input {
	return Input{
		Name: name, Provider: "aws", Kind: "aws_keys",
		Payload: map[string]string{"access_key_id": "AKIA1", "secret_access_key": "shh"},
	}
}

func TestCreateAndPayloadRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, "ws1", awsInput("prod-aws"))

	if c.State != StateUnknown {
		t.Errorf("fresh credential state = %q, want unknown", c.State)
	}
	got, err := s.Payload(ctx, "ws1", c.ID)
	if err != nil {
		t.Fatalf("Payload: %v", err)
	}
	if got["secret_access_key"] != "shh" {
		t.Errorf("payload round trip lost the secret: %v", got)
	}
}

func TestSafeViewNeverCarriesTheSecret(t *testing.T) {
	s := newTestStore(t)
	c := mustCreate(t, s, "ws1", awsInput("prod-aws"))
	for _, field := range []string{c.Hint, c.Identity, c.Name, c.IntrospectError} {
		if field == "shh" {
			t.Fatalf("secret surfaced in the safe view: %+v", c)
		}
	}
}

func TestPayloadIsDomainSeparatedByProvider(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, "ws1", awsInput("prod-aws"))

	if _, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE credentials SET provider = 'github' WHERE id = ?`), c.ID); err != nil {
		t.Fatalf("rewrite provider: %v", err)
	}
	if _, err := s.Payload(ctx, "ws1", c.ID); err == nil {
		t.Error("payload decrypted under the wrong provider label")
	}
}

func TestCreateRejectsDuplicateNameInWorkspace(t *testing.T) {
	s := newTestStore(t)
	mustCreate(t, s, "ws1", awsInput("dup"))
	if _, err := s.Create(context.Background(), "ws1", awsInput("dup"), "tester"); !errors.Is(err, ErrExists) {
		t.Errorf("second create err = %v, want ErrExists", err)
	}
	if _, err := s.Create(context.Background(), "ws2", awsInput("dup"), "tester"); err != nil {
		t.Errorf("cross-workspace name collision rejected: %v", err)
	}
}

func TestGetIsWorkspaceScoped(t *testing.T) {
	s := newTestStore(t)
	c := mustCreate(t, s, "ws1", awsInput("prod-aws"))
	if _, err := s.Get(context.Background(), "ws2", c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another workspace could read the credential: %v", err)
	}
	if _, err := s.Payload(context.Background(), "ws2", c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another workspace could read the payload: %v", err)
	}
}

func TestUpdateRotatingPayloadClearsIntrospection(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, "ws1", awsInput("prod-aws"))
	if err := s.SetIntrospection(ctx, "ws1", c.ID, Introspection{
		State: StateValid, Identity: "arn:old", Capabilities: []string{"aws:logs:StartQuery"},
	}); err != nil {
		t.Fatalf("SetIntrospection: %v", err)
	}

	rotated, err := s.Update(ctx, "ws1", c.ID, Input{
		Payload: map[string]string{"access_key_id": "AKIA2", "secret_access_key": "new"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if rotated.State != StateUnknown || rotated.Identity != "" || len(rotated.Capabilities) != 0 {
		t.Errorf("stale introspection survived a rotation: %+v", rotated)
	}
	if rotated.Name != "prod-aws" {
		t.Errorf("name changed on rotation: %q", rotated.Name)
	}
}

func TestUpdateWithoutPayloadKeepsSecretAndIntrospection(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, "ws1", awsInput("prod-aws"))
	if err := s.SetIntrospection(ctx, "ws1", c.ID, Introspection{State: StateValid, Identity: "arn:keep"}); err != nil {
		t.Fatalf("SetIntrospection: %v", err)
	}
	got, err := s.Update(ctx, "ws1", c.ID, Input{Name: "renamed"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Name != "renamed" || got.Identity != "arn:keep" {
		t.Errorf("got %+v", got)
	}
	payload, err := s.Payload(ctx, "ws1", c.ID)
	if err != nil || payload["secret_access_key"] != "shh" {
		t.Errorf("secret lost on rename: %v %v", payload, err)
	}
}

func TestStateExpiryIsDerivedNotStored(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, "ws1", awsInput("prod-aws"))

	cases := []struct {
		name   string
		in     time.Duration
		want   State
		usable bool
	}{
		{"far future", 90 * 24 * time.Hour, StateValid, true},
		{"inside the warning window", 2 * 24 * time.Hour, StateExpiring, true},
		{"past", -time.Hour, StateExpired, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.SetIntrospection(ctx, "ws1", c.ID, Introspection{
				State: StateValid, ExpiresAt: time.Now().Add(tc.in),
			}); err != nil {
				t.Fatalf("SetIntrospection: %v", err)
			}
			got, err := s.Get(ctx, "ws1", c.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.State != tc.want {
				t.Errorf("state = %q, want %q", got.State, tc.want)
			}
			if got.Usable() != tc.usable {
				t.Errorf("usable = %v, want %v", got.Usable(), tc.usable)
			}
		})
	}
}

func TestSetGrantsReplacesRatherThanMerges(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, "ws1", awsInput("prod-aws"))

	first := []Grant{
		{SubjectType: SubjectEnvironment, SubjectID: "env-prod"},
		{SubjectType: SubjectEnvironment, SubjectID: "env-dev"},
	}
	if err := s.SetGrants(ctx, "ws1", c.ID, first); err != nil {
		t.Fatalf("SetGrants: %v", err)
	}
	if err := s.SetGrants(ctx, "ws1", c.ID, first[:1]); err != nil {
		t.Fatalf("SetGrants (narrow): %v", err)
	}
	got, err := s.Grants(ctx, c.ID)
	if err != nil {
		t.Fatalf("Grants: %v", err)
	}
	if len(got) != 1 || got[0].SubjectID != "env-prod" {
		t.Errorf("grants = %+v — removing one must actually remove it", got)
	}
}

func TestSetGrantsRejectsMalformedSubjects(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, "ws1", awsInput("prod-aws"))
	bad := [][]Grant{
		{{SubjectType: SubjectEnvironment}},
		{{SubjectType: SubjectWorkspace, SubjectID: "ws1"}},
		{{SubjectType: SubjectType("everything"), SubjectID: "x"}},
	}
	for _, grants := range bad {
		if err := s.SetGrants(ctx, "ws1", c.ID, grants); err == nil {
			t.Errorf("accepted malformed grant %+v", grants)
		}
	}
}

func TestReachableOnlyReturnsGrantedCredentials(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	prod := mustCreate(t, s, "ws1", awsInput("prod-aws"))
	dev := mustCreate(t, s, "ws1", awsInput("dev-aws"))
	wide := mustCreate(t, s, "ws1", Input{
		Name: "tracker", Provider: "github", Kind: "github_pat",
		Payload: map[string]string{"token": "t"},
	})

	if err := s.SetGrants(ctx, "ws1", prod.ID, []Grant{{SubjectType: SubjectEnvironment, SubjectID: "env-prod"}}); err != nil {
		t.Fatalf("grants(prod): %v", err)
	}
	if err := s.SetGrants(ctx, "ws1", dev.ID, []Grant{{SubjectType: SubjectEnvironment, SubjectID: "env-dev"}}); err != nil {
		t.Fatalf("grants(dev): %v", err)
	}
	if err := s.SetGrants(ctx, "ws1", wide.ID, []Grant{{SubjectType: SubjectWorkspace}}); err != nil {
		t.Fatalf("grants(wide): %v", err)
	}

	got, err := s.Reachable(ctx, Reach{WorkspaceID: "ws1", EnvironmentIDs: []string{"env-dev"}})
	if err != nil {
		t.Fatalf("Reachable: %v", err)
	}
	names := map[string]bool{}
	for _, c := range got {
		names[c.Name] = true
	}
	if names["prod-aws"] {
		t.Error("a credential granted only to env-prod was reachable from a task that cannot see it")
	}
	if !names["dev-aws"] || !names["tracker"] {
		t.Errorf("reachable = %v, want dev-aws and the workspace-wide one", names)
	}
	ungranted := mustCreate(t, s, "ws1", awsInput("orphan"))
	got, err = s.Reachable(ctx, Reach{WorkspaceID: "ws1", EnvironmentIDs: []string{"env-dev"}})
	if err != nil {
		t.Fatalf("Reachable: %v", err)
	}
	for _, c := range got {
		if c.ID == ungranted.ID {
			t.Error("an ungranted credential was reachable")
		}
	}
}

func TestSetToolPolicyStoresOnlyTheDisabledOnes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, "ws1", awsInput("prod-aws"))

	if err := s.SetToolPolicy(ctx, "ws1", c.ID, []ToolPolicy{
		{Server: "thulu-cloud", Tool: "logs_search", Enabled: true},
		{Server: "thulu-cloud", Tool: "metrics_query", Enabled: false},
	}, "tester"); err != nil {
		t.Fatalf("SetToolPolicy: %v", err)
	}
	policy, err := s.ToolPolicy(ctx, c.ID)
	if err != nil {
		t.Fatalf("ToolPolicy: %v", err)
	}
	if _, ok := policy[PolicyKey("thulu-cloud", "logs_search")]; ok {
		t.Error("an enabled tool was persisted; only overrides belong in the table")
	}
	if enabled, ok := policy[PolicyKey("thulu-cloud", "metrics_query")]; !ok || enabled {
		t.Errorf("disabled override missing: %v", policy)
	}
}

func TestDeleteRemovesGrantsAndPolicy(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, "ws1", awsInput("prod-aws"))
	if err := s.SetGrants(ctx, "ws1", c.ID, []Grant{{SubjectType: SubjectWorkspace}}); err != nil {
		t.Fatalf("SetGrants: %v", err)
	}
	if err := s.SetToolPolicy(ctx, "ws1", c.ID, []ToolPolicy{
		{Server: "thulu-cloud", Tool: "logs_search", Enabled: false},
	}, "tester"); err != nil {
		t.Fatalf("SetToolPolicy: %v", err)
	}
	if err := s.Delete(ctx, "ws1", c.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	grants, err := s.Grants(ctx, c.ID)
	if err != nil || len(grants) != 0 {
		t.Errorf("grants survived the delete: %v %v", grants, err)
	}
	policy, err := s.ToolPolicy(ctx, c.ID)
	if err != nil || len(policy) != 0 {
		t.Errorf("policy survived the delete: %v %v", policy, err)
	}
}

func TestDecisionsAreRecordedForDenialsToo(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, allowed := range []bool{true, false} {
		if err := s.RecordDecision(ctx, DecisionRecord{
			WorkspaceID: "ws1", TaskID: "t1", Server: "thulu-cloud",
			Tool: "logs_search", Allowed: allowed, Reason: "ok",
		}); err != nil {
			t.Fatalf("RecordDecision: %v", err)
		}
	}
	got, err := s.ListDecisions(ctx, "ws1", 10)
	if err != nil {
		t.Fatalf("ListDecisions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d decisions, want 2", len(got))
	}
}

package credentials

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	store *Store
	auth  *Authorizer
	cred  *Credential
	reach Reach
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	c := mustCreate(t, s, "ws1", awsInput("prod-aws"))
	if err := s.SetIntrospection(ctx, "ws1", c.ID, Introspection{
		State:    StateValid,
		Identity: "arn:aws:iam::1:user/octopus",
		Capabilities: []string{
			"aws:logs:StartQuery", "aws:logs:GetQueryResults", "aws:cloudwatch:GetMetricData",
		},
	}); err != nil {
		t.Fatalf("SetIntrospection: %v", err)
	}
	if err := s.SetGrants(ctx, "ws1", c.ID, []Grant{
		{SubjectType: SubjectEnvironment, SubjectID: "env-prod"},
	}); err != nil {
		t.Fatalf("SetGrants: %v", err)
	}
	reloaded, err := s.Get(ctx, "ws1", c.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return &fixture{
		store: s,
		auth:  NewAuthorizer(s, NewCatalog()),
		cred:  reloaded,
		reach: Reach{WorkspaceID: "ws1", ProjectIDs: []string{"proj-a"}, EnvironmentIDs: []string{"env-prod"}},
	}
}

func (f *fixture) req(tool string) Request {
	return Request{
		Reach: f.reach, TaskID: "task-1", Agent: "developer", Stage: "build",
		Server: "thulu-cloud", Tool: tool,
	}
}

func (f *fixture) decide(t *testing.T, req Request) Decision {
	t.Helper()
	d, err := f.auth.Authorize(context.Background(), req)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	return d
}

func TestAuthorize_AllowsWhenEveryGatePasses(t *testing.T) {
	f := newFixture(t)
	got := f.decide(t, f.req("logs_search"))
	if !got.Allowed || got.Reason != ReasonOK {
		t.Fatalf("got %+v, want allow", got)
	}
	if got.CredentialID != f.cred.ID {
		t.Errorf("credential_id = %q, want %q", got.CredentialID, f.cred.ID)
	}
}

func TestAuthorize_UncataloguedToolNeedsNoCredential(t *testing.T) {
	f := newFixture(t)
	req := f.req("screenshot")
	req.Server = "thulu-browser"
	got := f.decide(t, req)
	if !got.Allowed || got.Reason != ReasonNoCredentialNeeded {
		t.Errorf("got %+v", got)
	}
}

func TestAuthorize_RefusesCredentialOutsideTheTasksReach(t *testing.T) {
	f := newFixture(t)
	req := f.req("logs_search")
	req.Reach = Reach{WorkspaceID: "ws1", EnvironmentIDs: []string{"env-someone-else"}}
	got := f.decide(t, req)
	if got.Allowed {
		t.Fatal("a task reached a credential granted to another environment")
	}
	if got.Reason != ReasonNoCredential {
		t.Errorf("reason = %q, want %q", got.Reason, ReasonNoCredential)
	}
}

func TestAuthorize_DenialDoesNotRevealWhetherTheCredentialExists(t *testing.T) {
	f := newFixture(t)

	notGranted := f.req("logs_search")
	notGranted.CredentialID = f.cred.ID
	notGranted.Reach = Reach{WorkspaceID: "ws1", EnvironmentIDs: []string{"env-other"}}

	nonexistent := f.req("logs_search")
	nonexistent.CredentialID = "cred_does_not_exist"

	a, b := f.decide(t, notGranted), f.decide(t, nonexistent)
	if a.Reason != b.Reason || a.Detail != b.Detail {
		t.Errorf("refusals are distinguishable:\n not granted: %+v\n nonexistent: %+v", a, b)
	}
	if a.CredentialID != "" {
		t.Errorf("refusal echoed a credential id back: %+v", a)
	}
}

func TestAuthorize_RefusesToolTheProviderCannotDrive(t *testing.T) {
	f := newFixture(t)

	implicit := f.decide(t, f.req("deploys"))
	if implicit.Allowed || implicit.Reason != ReasonNoCredential {
		t.Errorf("implicit: got %+v, want the generic refusal", implicit)
	}

	req := f.req("deploys")
	req.CredentialID = f.cred.ID
	named := f.decide(t, req)
	if named.Allowed || named.Reason != ReasonProviderUnsupported {
		t.Fatalf("named: got %+v", named)
	}
	if !strings.Contains(named.Detail, "gcp") {
		t.Errorf("detail should name the supported providers, got %q", named.Detail)
	}
}

func TestAuthorize_RefusesWhenTheKeyLacksThePermission(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.store.SetIntrospection(ctx, "ws1", f.cred.ID, Introspection{
		State: StateValid, Capabilities: []string{"aws:cloudwatch:GetMetricData"},
	}); err != nil {
		t.Fatalf("SetIntrospection: %v", err)
	}
	got := f.decide(t, f.req("logs_search"))
	if got.Allowed || got.Reason != ReasonMissingCapability {
		t.Fatalf("got %+v", got)
	}
	if len(got.Missing) != 2 {
		t.Errorf("missing = %v, want both logs permissions", got.Missing)
	}
	if got.Detail == noCredentialDetail(ctx) || got.Detail == "" {
		t.Errorf("detail should name the missing permissions, got %q", got.Detail)
	}
}

func TestAuthorize_RefusesToolSwitchedOffByPolicy(t *testing.T) {
	f := newFixture(t)
	if err := f.store.SetToolPolicy(context.Background(), "ws1", f.cred.ID, []ToolPolicy{
		{Server: "thulu-cloud", Tool: "logs_search", Enabled: false},
	}, "operator"); err != nil {
		t.Fatalf("SetToolPolicy: %v", err)
	}
	got := f.decide(t, f.req("logs_search"))
	if got.Allowed || got.Reason != ReasonPolicyDisabled {
		t.Fatalf("got %+v", got)
	}
	if sibling := f.decide(t, f.req("metrics_query")); !sibling.Allowed {
		t.Errorf("disabling one tool disabled another: %+v", sibling)
	}
}

func TestAuthorize_RefusesExpiredDisabledAndInvalidCredentials(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		setup func(t *testing.T, f *fixture)
		want  Reason
	}{
		{
			name: "expired",
			setup: func(t *testing.T, f *fixture) {
				if err := f.store.SetIntrospection(ctx, "ws1", f.cred.ID, Introspection{
					State:     StateValid,
					ExpiresAt: time.Now().Add(-time.Hour),
					Capabilities: []string{
						"aws:logs:StartQuery", "aws:logs:GetQueryResults",
					},
				}); err != nil {
					t.Fatalf("SetIntrospection: %v", err)
				}
			},
			want: ReasonExpired,
		},
		{
			name: "rejected by the provider",
			setup: func(t *testing.T, f *fixture) {
				if err := f.store.SetIntrospection(ctx, "ws1", f.cred.ID, Introspection{
					State: StateInvalid, Err: "InvalidClientTokenId",
				}); err != nil {
					t.Fatalf("SetIntrospection: %v", err)
				}
			},
			want: ReasonInvalid,
		},
		{
			name: "switched off",
			setup: func(t *testing.T, f *fixture) {
				if err := f.store.SetDisabled(ctx, "ws1", f.cred.ID, true); err != nil {
					t.Fatalf("SetDisabled: %v", err)
				}
			},
			want: ReasonDisabled,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.setup(t, f)
			got := f.decide(t, f.req("logs_search"))
			if got.Allowed || got.Reason != tc.want {
				t.Errorf("got %+v, want %s", got, tc.want)
			}
		})
	}
}

func TestAuthorize_StageAllowlistOnlySubtracts(t *testing.T) {
	f := newFixture(t)
	req := f.req("logs_search")
	req.StageTools = []string{"thulu-cloud@metrics_query"}
	got := f.decide(t, req)
	if got.Allowed || got.Reason != ReasonStageBlocked {
		t.Fatalf("got %+v", got)
	}
	req.StageTools = []string{"thulu-github"}
	if got := f.decide(t, req); got.Allowed || got.Reason != ReasonStageBlocked {
		t.Errorf("other server's entry permitted the tool: %+v", got)
	}
	req.StageTools = []string{}
	if got := f.decide(t, req); got.Allowed {
		t.Errorf("empty allowlist permitted a tool: %+v", got)
	}
	req.StageTools = nil
	if got := f.decide(t, req); !got.Allowed {
		t.Errorf("nil allowlist should not restrict: %+v", got)
	}
}

func TestAuthorize_StageAllowlistEntryFormat(t *testing.T) {
	for _, entries := range [][]string{
		{"thulu-cloud"},
		{"thulu-cloud@logs_search+metrics_query"},
		{"thulu-cloud@read"},
		{"thulu-github", " thulu-cloud "},
	} {
		f := newFixture(t)
		req := f.req("logs_search")
		req.StageTools = entries
		if got := f.decide(t, req); got.Reason == ReasonStageBlocked {
			t.Errorf("%q blocked logs_search: %+v", entries, got)
		}
	}
}

func TestAuthorize_AmbiguousWhenSeveralCredentialsCouldServe(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	second := mustCreate(t, f.store, "ws1", awsInput("dev-aws"))
	if err := f.store.SetIntrospection(ctx, "ws1", second.ID, Introspection{State: StateValid}); err != nil {
		t.Fatalf("SetIntrospection: %v", err)
	}
	if err := f.store.SetGrants(ctx, "ws1", second.ID, []Grant{
		{SubjectType: SubjectEnvironment, SubjectID: "env-prod"},
	}); err != nil {
		t.Fatalf("SetGrants: %v", err)
	}

	got := f.decide(t, f.req("logs_search"))
	if got.Allowed || got.Reason != ReasonAmbiguous {
		t.Fatalf("got %+v, want an ambiguity refusal", got)
	}
	req := f.req("logs_search")
	req.CredentialID = f.cred.ID
	if named := f.decide(t, req); !named.Allowed {
		t.Errorf("naming the credential did not resolve the ambiguity: %+v", named)
	}
}

func TestListAvailable_AgreesWithAuthorizeToolByTool(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.store.SetToolPolicy(ctx, "ws1", f.cred.ID, []ToolPolicy{
		{Server: "thulu-cloud", Tool: "metrics_query", Enabled: false},
	}, "operator"); err != nil {
		t.Fatalf("SetToolPolicy: %v", err)
	}

	rows, err := f.auth.ListAvailable(ctx, ListRequest{
		Reach: f.reach, TaskID: "task-1", Server: "thulu-cloud",
	})
	if err != nil {
		t.Fatalf("ListAvailable: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("no rows returned")
	}
	for _, row := range rows {
		req := f.req(row.Tool)
		req.Server = row.Server
		direct := f.decide(t, req)
		if direct.Allowed != row.Decision.Allowed || direct.Reason != row.Decision.Reason {
			t.Errorf("%s/%s: list says %+v, call says %+v",
				row.Server, row.Tool, row.Decision, direct)
		}
	}
}

func TestToolMatrix_SeparatesPolicyFromCapability(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.store.SetToolPolicy(ctx, "ws1", f.cred.ID, []ToolPolicy{
		{Server: "thulu-cloud", Tool: "metrics_query", Enabled: false},
	}, "operator"); err != nil {
		t.Fatalf("SetToolPolicy: %v", err)
	}
	cred, err := f.store.Get(ctx, "ws1", f.cred.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	rows, err := f.auth.ToolMatrix(ctx, cred)
	if err != nil {
		t.Fatalf("ToolMatrix: %v", err)
	}
	byTool := map[string]Decision{}
	for _, r := range rows {
		byTool[r.Tool] = r.Decision
	}
	if got := byTool["metrics_query"]; got.Reason != ReasonPolicyDisabled {
		t.Errorf("metrics_query = %+v, want a policy refusal", got)
	}
	if got := byTool["logs_search"]; !got.Allowed {
		t.Errorf("logs_search = %+v, want allowed", got)
	}
	if _, ok := byTool["deploys"]; ok {
		t.Error("matrix listed a tool the provider cannot drive")
	}
}

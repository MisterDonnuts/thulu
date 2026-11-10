package credentials

import (
	"context"
	"testing"
	"time"
)

func (f *fixture) check(t *testing.T, entries []string) []Fault {
	t.Helper()
	faults, err := f.auth.CheckDispatch(context.Background(), DispatchRequest{
		Reach: f.reach, Stage: "build", MCPEntries: entries,
	})
	if err != nil {
		t.Fatalf("CheckDispatch: %v", err)
	}
	return faults
}

func (f *fixture) setState(t *testing.T, state State, expires time.Time) {
	t.Helper()
	if err := f.store.SetIntrospection(context.Background(), "ws1", f.cred.ID, Introspection{
		State: state, Identity: "arn:x", ExpiresAt: expires,
		Capabilities: []string{
			"aws:logs:StartQuery", "aws:logs:GetQueryResults", "aws:cloudwatch:GetMetricData",
		},
	}); err != nil {
		t.Fatalf("SetIntrospection: %v", err)
	}
}

func TestCheckDispatch_PassesAHealthyWorkspace(t *testing.T) {
	f := newFixture(t)
	if faults := f.check(t, nil); len(faults) != 0 {
		t.Errorf("healthy workspace produced %+v, want no faults", faults)
	}
}

func TestCheckDispatch_BlocksOnAnExpiredCredential(t *testing.T) {
	f := newFixture(t)
	f.setState(t, StateValid, time.Now().Add(-time.Hour))

	faults := f.check(t, nil)
	if len(faults) != 1 {
		t.Fatalf("got %d faults over an expired credential, want 1: %+v", len(faults), faults)
	}
	if faults[0].Reason != ReasonExpired {
		t.Errorf("reason = %q, want %q", faults[0].Reason, ReasonExpired)
	}
	if faults[0].Name != "prod-aws" || faults[0].State != StateExpired {
		t.Errorf("fault = %+v, want it named and stated for an operator", faults[0])
	}
	if len(faults[0].Tools) == 0 {
		t.Error("the fault does not say which tools stop working")
	}
}

func TestCheckDispatch_BlocksOnARejectedCredential(t *testing.T) {
	f := newFixture(t)
	f.setState(t, StateInvalid, time.Time{})

	faults := f.check(t, nil)
	if len(faults) != 1 || faults[0].Reason != ReasonInvalid {
		t.Fatalf("got %+v, want one invalid-credential fault", faults)
	}
}

func TestCheckDispatch_DisabledIsAChoiceNotAFault(t *testing.T) {
	f := newFixture(t)
	f.setState(t, StateValid, time.Now().Add(-time.Hour))
	if err := f.store.SetDisabled(context.Background(), "ws1", f.cred.ID, true); err != nil {
		t.Fatalf("SetDisabled: %v", err)
	}
	if faults := f.check(t, nil); len(faults) != 0 {
		t.Errorf("a disabled credential blocked dispatch: %+v", faults)
	}
}

func TestCheckDispatch_MissingCapabilityDoesNotBlock(t *testing.T) {
	f := newFixture(t)
	if err := f.store.SetIntrospection(context.Background(), "ws1", f.cred.ID, Introspection{
		State: StateValid, Identity: "arn:x", Capabilities: []string{"aws:logs:StartQuery"},
	}); err != nil {
		t.Fatalf("SetIntrospection: %v", err)
	}
	if faults := f.check(t, nil); len(faults) != 0 {
		t.Errorf("a narrow-but-live credential blocked dispatch: %+v", faults)
	}
}

func TestCheckDispatch_PolicyOverrideDoesNotBlock(t *testing.T) {
	f := newFixture(t)
	if err := f.store.SetToolPolicy(context.Background(), "ws1", f.cred.ID,
		[]ToolPolicy{{Server: "thulu-cloud", Tool: "logs_search", Enabled: false}}, "tester"); err != nil {
		t.Fatalf("SetToolPolicy: %v", err)
	}
	if faults := f.check(t, nil); len(faults) != 0 {
		t.Errorf("a policy-disabled tool blocked dispatch: %+v", faults)
	}
}

func TestCheckDispatch_NoCredentialsIsNotAFault(t *testing.T) {
	f := newFixture(t)
	if err := f.store.Delete(context.Background(), "ws1", f.cred.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if faults := f.check(t, nil); len(faults) != 0 {
		t.Errorf("an empty workspace blocked dispatch: %+v", faults)
	}
}

func TestCheckDispatch_IgnoresServersTheStageDoesNotGet(t *testing.T) {
	f := newFixture(t)
	f.setState(t, StateValid, time.Now().Add(-time.Hour))

	if faults := f.check(t, []string{"thulu-github"}); len(faults) != 0 {
		t.Errorf("a stage without thulu-cloud was blocked by an AWS credential: %+v", faults)
	}
	if faults := f.check(t, []string{"thulu-cloud"}); len(faults) != 1 {
		t.Errorf("a stage with thulu-cloud was not blocked: %+v", faults)
	}
}

func TestCheckDispatch_HonorsPerToolStageEntries(t *testing.T) {
	f := newFixture(t)
	f.setState(t, StateValid, time.Now().Add(-time.Hour))

	faults := f.check(t, []string{"thulu-cloud@logs_search+metrics_query"})
	if len(faults) != 1 {
		t.Fatalf("got %+v, want the two named tools reported", faults)
	}
	if len(faults[0].Tools) != 2 {
		t.Errorf("tools = %v, want exactly the two the stage grants", faults[0].Tools)
	}
}

func TestCheckDispatch_TreatsAnUnknownSuffixAsAScope(t *testing.T) {
	f := newFixture(t)
	f.setState(t, StateValid, time.Now().Add(-time.Hour))

	full := f.check(t, []string{"thulu-cloud"})
	scoped := f.check(t, []string{"thulu-cloud@write"})
	if len(scoped) != 1 || len(full) != 1 {
		t.Fatalf("expected one fault each, got %d and %d", len(full), len(scoped))
	}
	if len(scoped[0].Tools) != len(full[0].Tools) {
		t.Errorf("scope suffix narrowed the gate to %v, want the whole server %v",
			scoped[0].Tools, full[0].Tools)
	}
}

func TestCheckDispatch_AMixedSuffixStaysWide(t *testing.T) {
	f := newFixture(t)
	f.setState(t, StateValid, time.Now().Add(-time.Hour))

	mixed := f.check(t, []string{"thulu-cloud@write+logs_search"})
	full := f.check(t, []string{"thulu-cloud"})
	if len(mixed) != 1 || len(full) != 1 || len(mixed[0].Tools) != len(full[0].Tools) {
		t.Errorf("a mixed suffix narrowed the gate: %+v vs %+v", mixed, full)
	}
}

func TestCheckDispatch_AnEmptyAllowlistBlocksNothing(t *testing.T) {
	f := newFixture(t)
	f.setState(t, StateValid, time.Now().Add(-time.Hour))

	if faults := f.check(t, []string{}); len(faults) != 0 {
		t.Errorf("a stage with no MCP servers was blocked: %+v", faults)
	}
}

func TestCheckDispatch_IgnoresUnreachableCredentials(t *testing.T) {
	f := newFixture(t)
	f.setState(t, StateValid, time.Now().Add(-time.Hour))
	if err := f.store.SetGrants(context.Background(), "ws1", f.cred.ID, []Grant{
		{SubjectType: SubjectEnvironment, SubjectID: "env-someone-else"},
	}); err != nil {
		t.Fatalf("SetGrants: %v", err)
	}
	if faults := f.check(t, nil); len(faults) != 0 {
		t.Errorf("a credential outside the task's reach blocked it: %+v", faults)
	}
}

func TestCheckDispatch_AgreesWithAuthorize(t *testing.T) {
	f := newFixture(t)
	f.setState(t, StateValid, time.Now().Add(-time.Hour))

	faults := f.check(t, nil)
	if len(faults) == 0 {
		t.Fatal("no faults to cross-check")
	}
	for _, tool := range faults[0].Tools {
		_, name, _ := cutLast(tool, "/")
		got := f.decide(t, f.req(name))
		if got.Allowed {
			t.Errorf("%s: gate reported a fault but Authorize allows it", tool)
		}
		if got.Reason != faults[0].Reason {
			t.Errorf("%s: gate says %q, Authorize says %q", tool, faults[0].Reason, got.Reason)
		}
	}
}

func cutLast(s, sep string) (before, after string, found bool) {
	for i := len(s) - len(sep); i >= 0; i-- {
		if s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}

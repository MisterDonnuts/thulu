package credentials

import (
	"context"
	"testing"
)

func TestStaticHeaders(t *testing.T) {
	cases := []struct {
		name    string
		kind    string
		payload map[string]string
		header  string
		want    string
		wantErr bool
	}{
		{name: "bearer", kind: HeaderKindBearer, payload: map[string]string{"token": " tok "},
			header: "Authorization", want: "Bearer tok"},
		{name: "basic", kind: HeaderKindBasic, payload: map[string]string{"username": "ana@example.com", "token": "t0k"},
			header: "Authorization", want: "Basic YW5hQGV4YW1wbGUuY29tOnQwaw=="},
		{name: "custom", kind: HeaderKindCustom, payload: map[string]string{"header_name": "x-api-key", "header_value": "v"},
			header: "X-Api-Key", want: "v"},
		{name: "bearer without token", kind: HeaderKindBearer, payload: map[string]string{}, wantErr: true},
		{name: "basic without user", kind: HeaderKindBasic, payload: map[string]string{"token": "t"}, wantErr: true},
		{name: "custom with a bad name", kind: HeaderKindCustom,
			payload: map[string]string{"header_name": "X-Key: y", "header_value": "v"}, wantErr: true},
		{name: "value with a newline", kind: HeaderKindBearer,
			payload: map[string]string{"token": "a\r\nX-Evil: 1"}, wantErr: true},
		{name: "unknown kind", kind: "oauth", payload: map[string]string{"token": "t"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, err := StaticHeaders(tc.kind, tc.payload)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("StaticHeaders = %v, want an error", h)
				}
				return
			}
			if err != nil {
				t.Fatalf("StaticHeaders: %v", err)
			}
			if got := h.Get(tc.header); got != tc.want {
				t.Errorf("%s = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

func TestHeaderProviderIntrospect(t *testing.T) {
	p := &headerProvider{}
	got, err := p.Introspect(context.Background(), HeaderKindBasic,
		map[string]string{"username": "ana@example.com", "token": "secret-1234"}, nil)
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	if got.State != StateUnknown || got.Identity != "ana@example.com" || got.Hint != "…1234" {
		t.Errorf("Introspect = %+v", got)
	}
	if _, err := p.Introspect(context.Background(), HeaderKindBearer, map[string]string{}, nil); err == nil {
		t.Error("an empty bearer token passed introspection")
	}
	if _, ok := NewRegistry().Get(HeaderProviderID); !ok {
		t.Error("the header provider is not registered")
	}
}

func TestAuthorizeServer(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := NewAuthorizer(s, NewCatalog())
	in := Input{Name: "jira", Provider: HeaderProviderID, Kind: HeaderKindBasic,
		Payload: map[string]string{"username": "ana@example.com", "token": "t"}}
	c := mustCreate(t, s, "ws1", in)
	other := mustCreate(t, s, "ws2", in)
	reach := Reach{WorkspaceID: "ws1", ProjectIDs: []string{"proj-a"}}

	decide := func(ref string, r Reach) Decision {
		t.Helper()
		d, err := a.AuthorizeServer(ctx, ServerRequest{Reach: r, Server: "jira", CredentialRef: ref})
		if err != nil {
			t.Fatalf("AuthorizeServer: %v", err)
		}
		return d
	}

	if d := decide("jira", reach); d.Allowed || d.Reason != ReasonNoCredential {
		t.Fatalf("ungranted = %+v, want no_credential", d)
	}
	if err := s.SetGrants(ctx, "ws1", c.ID, []Grant{{SubjectType: SubjectProject, SubjectID: "proj-a"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGrants(ctx, "ws2", other.ID, []Grant{{SubjectType: SubjectWorkspace}}); err != nil {
		t.Fatal(err)
	}
	if d := decide("jira", reach); !d.Allowed || d.CredentialID != c.ID {
		t.Fatalf("by name = %+v, want ws1's credential %s", d, c.ID)
	}
	if d := decide(c.ID, reach); !d.Allowed || d.CredentialID != c.ID {
		t.Fatalf("by id = %+v", d)
	}
	if d := decide(other.ID, reach); d.Allowed {
		t.Fatalf("another workspace's credential = %+v, want a refusal", d)
	}
	if d := decide("jira", Reach{WorkspaceID: "ws1", ProjectIDs: []string{"proj-b"}}); d.Allowed {
		t.Fatalf("other project = %+v, want a refusal", d)
	}
	if err := s.SetDisabled(ctx, "ws1", c.ID, true); err != nil {
		t.Fatal(err)
	}
	if d := decide("jira", reach); d.Allowed || d.Reason != ReasonDisabled {
		t.Fatalf("disabled = %+v, want credential_disabled", d)
	}
	if d := decide("", reach); d.Allowed {
		t.Fatalf("empty ref = %+v, want a refusal", d)
	}
}

func TestRenderHeaderTemplate(t *testing.T) {
	payload := map[string]string{"username": "ana@example.com", "token": "t0k", "site": "acme"}
	h, err := RenderHeaderTemplate("Authorization: Basic {base64:username:token}\nX-Site: {site}.atlassian.net", payload)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got := h.Get("Authorization"); got != "Basic YW5hQGV4YW1wbGUuY29tOnQwaw==" {
		t.Errorf("Authorization = %q", got)
	}
	if got := h.Get("X-Site"); got != "acme.atlassian.net" {
		t.Errorf("X-Site = %q", got)
	}
	for _, bad := range []string{
		"",
		"Authorization Bearer {token}",
		"Host: {token}",
		"Authorization: {nope:x}",
	} {
		if _, err := ParseHeaderTemplate(bad); err == nil {
			t.Errorf("ParseHeaderTemplate(%q) passed", bad)
		}
	}
	if _, err := RenderHeaderTemplate("Authorization: Bearer {missing}", payload); err == nil {
		t.Error("a missing field rendered")
	}
	if _, err := HeadersFor("github", "github_pat", "", map[string]string{"token": "x"}); err == nil {
		t.Error("a non-header credential rendered without a template")
	}
	if h, err := HeadersFor("github", "github_pat", "Authorization: Bearer {token}", map[string]string{"token": "x"}); err != nil || h.Get("Authorization") != "Bearer x" {
		t.Errorf("HeadersFor with template = %v, %v", h, err)
	}
}

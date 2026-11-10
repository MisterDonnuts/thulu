package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func TestNameToEnvKey(t *testing.T) {
	cases := map[string]string{
		"ticket-reader": "MCP_TICKET_READER_URL",
		"thulu-github":  "MCP_THULU_GITHUB_URL",
		"octopus-api":   "MCP_OCTOPUS_API_URL",
		"single":        "MCP_SINGLE_URL",
		"with-2-dashes": "MCP_WITH_2_DASHES_URL",
	}
	for in, want := range cases {
		if got := NameToEnvKey(in); got != want {
			t.Errorf("NameToEnvKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnvKeyToName(t *testing.T) {
	cases := map[string]string{
		"MCP_TICKET_READER_URL": "ticket-reader",
		"MCP_THULU_GITHUB_URL":  "thulu-github",
		"MCP_OCTOPUS_API_URL":   "octopus-api",
		"MCP_WITH_2_DASHES_URL": "with-2-dashes",
		"MCP__URL":              "",
		"MCP_URL":               "",
		"OCTOPUS_GITHUB_URL":    "",
		"MCP_THULU_GITHUB_KEY":  "",
	}
	for in, want := range cases {
		if got := EnvKeyToName(in); got != want {
			t.Errorf("EnvKeyToName(%q) = %q, want %q", in, got, want)
		}
		if want != "" && NameToEnvKey(want) != in {
			t.Errorf("NameToEnvKey(EnvKeyToName(%q)) = %q", in, NameToEnvKey(want))
		}
	}
}

func TestValidateName(t *testing.T) {
	ok := []string{"ticket-reader", "octopus-api", "abc", "a-b", "with-2-dashes"}
	for _, s := range ok {
		if err := ValidateName(s); err != nil {
			t.Errorf("ValidateName(%q) unexpected error: %v", s, err)
		}
	}
	bad := []string{
		"", "a", "-nope", "nope-", "UPPER", "with_underscore",
		strings.Repeat("a", 65),
	}
	for _, s := range bad {
		if err := ValidateName(s); err == nil {
			t.Errorf("ValidateName(%q) should have failed", s)
		}
	}
	if err := ValidateName("thulu-sandbox"); !errors.Is(err, ErrReservedName) {
		t.Errorf("expected ErrReservedName for thulu-sandbox, got %v", err)
	}
}

func TestValidateURL(t *testing.T) {
	ok := []string{"http://localhost:3014/sse", "https://mcp.example.com/sse", "http://ticket-reader.internal:3000/sse"}
	for _, s := range ok {
		if err := ValidateURL(s); err != nil {
			t.Errorf("ValidateURL(%q) unexpected error: %v", s, err)
		}
	}
	bad := []string{"", "not a url", "ftp://x", "//no-scheme.com", "http://"}
	for _, s := range bad {
		if err := ValidateURL(s); err == nil {
			t.Errorf("ValidateURL(%q) should have failed", s)
		}
	}
}

func TestRegistryCRUD(t *testing.T) {
	db := testpg.New(t)
	reg, err := NewRegistry(db)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	ctx := context.Background()

	if list, err := reg.List(ctx); err != nil || len(list) != 0 {
		t.Fatalf("List (empty) = %v, %v", list, err)
	}

	got, err := reg.Register(ctx, Registration{Name: "ticket-reader", URL: "http://ticket-reader.internal:3000/sse", Description: "Ticket board", CreatedBy: "admin@octopus.local"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got.Name != "ticket-reader" || got.URL != "http://ticket-reader.internal:3000/sse" || got.Description != "Ticket board" {
		t.Fatalf("Register returned unexpected fields: %+v", got)
	}
	if got.EnvKey() != "MCP_TICKET_READER_URL" {
		t.Fatalf("EnvKey = %q, want MCP_TICKET_READER_URL", got.EnvKey())
	}

	if _, err := reg.Register(ctx, Registration{Name: "ticket-reader", URL: "http://x"}); !errors.Is(err, ErrRegistrationExists) {
		t.Fatalf("duplicate Register: got err = %v, want ErrRegistrationExists", err)
	}

	if _, err := reg.Register(ctx, Registration{Name: "UPPER", URL: "http://x/"}); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("bad name: got %v, want ErrInvalidName", err)
	}
	if _, err := reg.Register(ctx, Registration{Name: "another", URL: "not-a-url"}); !errors.Is(err, ErrInvalidURL) {
		t.Fatalf("bad url: got %v, want ErrInvalidURL", err)
	}

	fetched, err := reg.Get(ctx, "ticket-reader")
	if err != nil || fetched.URL != got.URL {
		t.Fatalf("Get: %+v, %v", fetched, err)
	}

	upd, err := reg.Update(ctx, "ticket-reader", Patch{URL: "http://ticket-reader-2.internal:3000/sse"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if upd.URL != "http://ticket-reader-2.internal:3000/sse" || upd.Description != "Ticket board" {
		t.Fatalf("Update kept-desc failure: %+v", upd)
	}
	if !upd.UpdatedAt.After(got.CreatedAt) {
		t.Errorf("UpdatedAt did not advance: %v vs %v", upd.UpdatedAt, got.CreatedAt)
	}

	upd2, err := reg.Update(ctx, "ticket-reader", Patch{Description: "New desc"})
	if err != nil {
		t.Fatalf("Update desc: %v", err)
	}
	if upd2.URL != upd.URL || upd2.Description != "New desc" {
		t.Fatalf("Update desc-only failure: %+v", upd2)
	}

	if err := reg.Delete(ctx, "ticket-reader"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := reg.Get(ctx, "ticket-reader"); !errors.Is(err, ErrRegistrationNotFound) {
		t.Fatalf("Get after delete: %v, want ErrRegistrationNotFound", err)
	}
	if err := reg.Delete(ctx, "ticket-reader"); !errors.Is(err, ErrRegistrationNotFound) {
		t.Fatalf("Delete again: %v, want ErrRegistrationNotFound", err)
	}
}

func TestRegistry_ForwardsCredentialIsOptInAndSticks(t *testing.T) {
	db := testpg.New(t)
	reg, err := NewRegistry(db)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	ctx := context.Background()

	plain, err := reg.Register(ctx, Registration{Name: "third-party", URL: "http://example.test/sse", CreatedBy: "admin"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if plain.ForwardsCredential {
		t.Error("a registration must not receive the agent's credential unless asked")
	}

	trusted, err := reg.Register(ctx, Registration{Name: "acts-for-agent", URL: "http://example.test/sse", CreatedBy: "admin", ForwardsCredential: true})
	if err != nil {
		t.Fatalf("Register trusted: %v", err)
	}
	if !trusted.ForwardsCredential {
		t.Fatal("Register(forwards=true) did not record it")
	}

	same, err := reg.Update(ctx, "acts-for-agent", Patch{URL: "http://example.test/moved"})
	if err != nil {
		t.Fatalf("Update url: %v", err)
	}
	if !same.ForwardsCredential {
		t.Error("changing the URL dropped the credential decision")
	}

	off := false
	revoked, err := reg.Update(ctx, "acts-for-agent", Patch{ForwardsCredential: &off})
	if err != nil {
		t.Fatalf("Update off: %v", err)
	}
	if revoked.ForwardsCredential {
		t.Error("Update(&false) did not turn it off")
	}
	back, err := reg.Get(ctx, "acts-for-agent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if back.ForwardsCredential {
		t.Error("stored row still forwards the credential")
	}
}

func TestRegistryTransportAndAuth(t *testing.T) {
	reg, err := NewRegistry(testpg.New(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	ctx := context.Background()

	plain, err := reg.Register(ctx, Registration{Name: "plain", URL: "http://plain:3000/sse"})
	if err != nil {
		t.Fatal(err)
	}
	if plain.Transport != TransportSSE || plain.Auth != AuthNone {
		t.Errorf("defaults = %q/%q, want sse/none", plain.Transport, plain.Auth)
	}

	jira, err := reg.Register(ctx, Registration{
		Name: "jira", URL: "https://mcp.atlassian.com/v1/mcp", Transport: "HTTP",
		Auth: AuthCredential, CredentialRef: "jira",
		HeaderTemplate: "Authorization: Basic {base64:username:token}",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := reg.Get(ctx, "jira")
	if err != nil {
		t.Fatal(err)
	}
	if got.Transport != TransportHTTP || got.Auth != AuthCredential || got.CredentialRef != "jira" ||
		got.HeaderTemplate != jira.HeaderTemplate {
		t.Errorf("stored = %+v", got)
	}

	for name, in := range map[string]Registration{
		"bad transport":     {Name: "x-one", URL: "http://x", Transport: "ws"},
		"bad auth":          {Name: "x-two", URL: "http://x", Auth: "oauth2"},
		"credential no ref": {Name: "x-three", URL: "http://x", Auth: AuthCredential},
		"bad template":      {Name: "x-four", URL: "http://x", Auth: AuthCredential, CredentialRef: "c", HeaderTemplate: "nope"},
	} {
		if _, err := reg.Register(ctx, in); err == nil {
			t.Errorf("%s: registered", name)
		}
	}

	none := AuthNone
	off, err := reg.Update(ctx, "jira", Patch{Auth: &none})
	if err != nil {
		t.Fatal(err)
	}
	if off.CredentialRef != "" || off.HeaderTemplate != "" || off.Transport != TransportHTTP {
		t.Errorf("after auth=none: %+v", off)
	}
}

func TestRunGrants(t *testing.T) {
	reg, err := NewRegistry(testpg.New(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	ctx := context.Background()
	if _, found, err := reg.LookupRunGrant(ctx, "t1", "dev", "build"); err != nil || found {
		t.Fatalf("empty lookup = %v, %v", found, err)
	}
	if err := reg.RecordRunGrant(ctx, "t1", "dev", "build", RunGrant{Tools: "octopus-api@write", Locale: "es"}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RecordRunGrant(ctx, "t1", "dev", "build", RunGrant{Tools: "thulu-github"}); err != nil {
		t.Fatal(err)
	}
	g, found, err := reg.LookupRunGrant(ctx, "t1", "dev", "build")
	if err != nil || !found || g.Tools != "thulu-github" || g.Locale != "" {
		t.Errorf("lookup = %+v, %v, %v", g, found, err)
	}
	if _, found, _ := reg.LookupRunGrant(ctx, "t1", "dev", "review"); found {
		t.Error("another stage's grant was found")
	}
}

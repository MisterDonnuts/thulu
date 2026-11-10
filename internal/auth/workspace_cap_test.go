package auth

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

type capFixture struct {
	h       *Handler
	store   *UserStore
	wrapped http.Handler
	user    *User
	token   string
}

func newCapFixture(t *testing.T) capFixture {
	t.Helper()
	t.Setenv("OCTOPUS_SAAS_MODE", "1")
	t.Setenv("OCTOPUS_MAX_OWNED_WORKSPACES", "")
	t.Setenv("OCTOPUS_OPERATOR_WORKSPACE", "")
	t.Setenv("OCTOPUS_ALLOW_PERSONAL_WORKSPACES", "1")
	store := newSSOTestStore(t)
	h := NewHandler(store)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	f := capFixture{h: h, store: store, wrapped: JWTMiddlewareWithStore(nil, mux)}
	f.user = ssoUser(t, store, "founder@example.com", "Founder")
	ws, err := store.CreateWorkspace(t.Context(), f.user.ID, "Personal", "personal")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := h.issueTokenPair(t.Context(), f.user, ws.ID, RoleOwner, deviceInfo{})
	if err != nil {
		t.Fatal(err)
	}
	f.token = tok.AccessToken
	return f
}

func (f capFixture) create(t *testing.T, name string) int {
	t.Helper()
	return signupDo(t, f.wrapped, http.MethodPost, "/auth/workspaces", f.token, map[string]string{"name": name}).Code
}

func TestMaxOwnedWorkspaces_Defaults(t *testing.T) {
	cases := []struct {
		saas, env string
		want      int
	}{
		{"", "", 0},
		{"1", "", 2},
		{"1", "5", 5},
		{"1", "0", 0},
		{"", "3", 3},
		{"1", "lots", 2},
	}
	for _, c := range cases {
		t.Setenv("OCTOPUS_SAAS_MODE", c.saas)
		t.Setenv("OCTOPUS_MAX_OWNED_WORKSPACES", c.env)
		if got := MaxOwnedWorkspaces(); got != c.want {
			t.Errorf("SAAS_MODE=%q MAX=%q: got %d, want %d", c.saas, c.env, got, c.want)
		}
	}
	for _, bad := range []string{"lots", "-1", "1.5"} {
		t.Setenv("OCTOPUS_MAX_OWNED_WORKSPACES", bad)
		if validMaxOwnedWorkspacesEnv() {
			t.Errorf("%q accepted at startup", bad)
		}
	}
}

func TestCreateWorkspace_CapInSaaS(t *testing.T) {
	f := newCapFixture(t)
	if code := f.create(t, "Side project"); code != http.StatusCreated {
		t.Fatalf("second workspace: %d, want 201", code)
	}
	if code := f.create(t, "Third"); code != http.StatusForbidden {
		t.Fatalf("third workspace: %d, want 403", code)
	}
	ws, _ := f.store.ListWorkspacesForUser(t.Context(), f.user.ID)
	if len(ws) != 2 {
		t.Errorf("owns %d workspaces, want 2", len(ws))
	}

	t.Setenv("OCTOPUS_MAX_OWNED_WORKSPACES", "0")
	if code := f.create(t, "Third"); code != http.StatusCreated {
		t.Errorf("with the cap off: %d, want 201", code)
	}
}

func TestCreateWorkspace_OperatorExempt(t *testing.T) {
	f := newCapFixture(t)
	ws, _ := f.store.ListWorkspacesForUser(t.Context(), f.user.ID)
	t.Setenv("OCTOPUS_OPERATOR_WORKSPACE", ws[0].ID)
	for _, name := range []string{"Customer A", "Customer B", "Customer C"} {
		if code := f.create(t, name); code != http.StatusCreated {
			t.Fatalf("operator creating %s: %d, want 201", name, code)
		}
	}
}

func TestCreateOwnedWorkspace_ConcurrentCap(t *testing.T) {
	f := newCapFixture(t)
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.store.CreateOwnedWorkspace(t.Context(), f.user.ID, "Race", "race", 2)
		}()
	}
	wg.Wait()
	var ok int
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrWorkspaceCapReached):
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Errorf("%d concurrent creates succeeded, want 1 (cap 2, one already owned)", ok)
	}
}

func TestCreateWorkspace_NameWithoutSlug(t *testing.T) {
	f := newCapFixture(t)
	for _, name := range []string{"!!!", "🐙", "  --  "} {
		if code := f.create(t, name); code != http.StatusBadRequest {
			t.Errorf("name %q: %d, want 400", name, code)
		}
	}
}

func TestCreateWorkspace_TakenSlugGetsSuffix(t *testing.T) {
	f := newCapFixture(t)
	other := ssoUser(t, f.store, "other@example.com", "Other")
	if _, err := f.store.CreateWorkspace(t.Context(), other.ID, "Acme", "acme"); err != nil {
		t.Fatal(err)
	}
	if code := f.create(t, "Acme"); code != http.StatusCreated {
		t.Fatalf("name another tenant used: %d, want 201", code)
	}
}

func TestSlugify_CutsAtRunes(t *testing.T) {
	got := slugify(strings.Repeat("ñ", 60))
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != 50 {
		t.Errorf("slugify(60×ñ) = %q (%d runes, valid %v)", got, utf8.RuneCountInString(got), utf8.ValidString(got))
	}
	if got := slugify(strings.Repeat("a", 49) + " bcd"); strings.HasSuffix(got, "-") {
		t.Errorf("slug ends in '-': %q", got)
	}
}

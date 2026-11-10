package auth

import (
	"context"
	"errors"
	"testing"
)

func TestAuthorFromContext(t *testing.T) {
	t.Setenv("OCTOPUS_DEV_MODE", "")
	bg := context.Background()
	key := func(info APIKeyInfo) context.Context { return withKeyAuth(bg, &info) }

	cases := []struct {
		name string
		ctx  context.Context
		want Author
	}{
		{
			name: "run key signs as the agent it was minted for",
			ctx:  key(APIKeyInfo{ID: "k1", Name: "run:developer", Scope: RunScope{TaskID: "t1", Agent: "developer"}}),
			want: Author{Name: "developer", Agent: true},
		},
		{
			name: "chat run key signs as its agent",
			ctx:  key(APIKeyInfo{ID: "k2", Scope: RunScope{ChatID: "c1", Agent: "manager"}}),
			want: Author{Name: "manager", Agent: true},
		},
		{
			name: "operator key signs with its prefixed label",
			ctx:  key(APIKeyInfo{ID: "k3", Name: "manager"}),
			want: Author{Name: "key:manager"},
		},
		{
			name: "unnamed operator key signs with its id",
			ctx:  key(APIKeyInfo{ID: "k4"}),
			want: Author{Name: "key:k4"},
		},
		{
			name: "session signs with the email",
			ctx:  WithClaims(bg, &Claims{UserID: "u1", Email: "ana@example.com"}),
			want: Author{Name: "ana@example.com"},
		},
		{
			name: "session without email signs with the prefixed user id",
			ctx:  WithClaims(bg, &Claims{UserID: "u1"}),
			want: Author{Name: "user:u1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AuthorFromContext(tc.ctx)
			if err != nil {
				t.Fatalf("AuthorFromContext: %v", err)
			}
			if got != tc.want {
				t.Errorf("author = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestAuthorFromContext_RunKeyWithoutAgentSignsNothing(t *testing.T) {
	ctx := withKeyAuth(context.Background(), &APIKeyInfo{ID: "k1", Name: "legacy", Scope: RunScope{TaskID: "t1"}})
	if a, err := AuthorFromContext(ctx); !errors.Is(err, ErrUnsigned) {
		t.Fatalf("author = %+v, err = %v; want ErrUnsigned", a, err)
	}
}

func TestAuthorFromContext_NoCredential(t *testing.T) {
	t.Setenv("OCTOPUS_DEV_MODE", "")
	if a, err := AuthorFromContext(context.Background()); !errors.Is(err, ErrUnsigned) {
		t.Fatalf("author = %+v, err = %v; want ErrUnsigned", a, err)
	}

	t.Setenv("OCTOPUS_DEV_MODE", "1")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	if a, err := AuthorFromContext(context.Background()); !errors.Is(err, ErrUnsigned) {
		t.Fatalf("dev mode without the session: author = %+v, err = %v; want ErrUnsigned", a, err)
	}
	a, err := AuthorFromContext(context.WithValue(context.Background(), devSessionKey{}, true))
	if err != nil || a != (Author{Name: DevModeAuthorName}) {
		t.Fatalf("dev session author = %+v, err = %v; want %q", a, err, DevModeAuthorName)
	}
}

func TestAuthorLabel(t *testing.T) {
	if got := (Author{Name: "developer", Agent: true}).Label(); got != "agent:developer" {
		t.Errorf("agent label = %q", got)
	}
	if got := (Author{Name: "ana@example.com"}).Label(); got != "ana@example.com" {
		t.Errorf("person label = %q", got)
	}
}

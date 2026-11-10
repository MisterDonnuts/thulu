package workloadid

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"
)

type memStore struct {
	mu   sync.Mutex
	keys []Key
}

func (m *memStore) WorkloadIdentityKeys(context.Context) ([]Key, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Key(nil), m.keys...), nil
}

func (m *memStore) AddWorkloadIdentityKey(_ context.Context, k Key, retireAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.keys {
		if m.keys[i].RetiredAt.IsZero() {
			m.keys[i].RetiredAt = retireAt
		}
	}
	m.keys = append(m.keys, k)
	return nil
}

func env(vals map[string]string) func(string) string {
	return func(k string) string { return vals[k] }
}

func newTestIssuer(t *testing.T) (*Issuer, *time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	iss := New(&memStore{}, env(map[string]string{"OCTOPUS_DOMAIN": "thulu.example.com"}))
	iss.now = func() time.Time { return now }
	return iss, &now
}

func verify(t *testing.T, tok string, jwks JWKS) (map[string]any, map[string]any) {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	dec := func(s string) []byte {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		return b
	}
	var header, claims map[string]any
	if err := json.Unmarshal(dec(parts[0]), &header); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(dec(parts[1]), &claims); err != nil {
		t.Fatal(err)
	}
	var jwk *JWK
	for i := range jwks.Keys {
		if jwks.Keys[i].Kid == header["kid"] {
			jwk = &jwks.Keys[i]
		}
	}
	if jwk == nil {
		t.Fatalf("kid %v is not in the published set", header["kid"])
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, dec(jwk.X)...), dec(jwk.Y)...))
	if err != nil {
		t.Fatalf("published key: %v", err)
	}
	sig := dec(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("signature does not verify against the published key")
	}
	return header, claims
}

func TestMint_ClaimsAndSignature(t *testing.T) {
	iss, now := newTestIssuer(t)
	ctx := context.Background()
	tok, err := iss.Mint(ctx, RunClaims{
		WorkspaceID: "ws1", TaskID: "t1", RunID: "dev-task-t1-build-1",
		Audience: "https://api.anthropic.com", TTL: 90 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	jwks, err := iss.JWKS(ctx)
	if err != nil {
		t.Fatal(err)
	}
	header, claims := verify(t, tok, jwks)
	if header["alg"] != "ES256" || header["kid"] == "" {
		t.Errorf("header = %v", header)
	}
	want := map[string]any{
		"iss": "https://thulu.example.com", "sub": "octopus:workspace:ws1:task:t1",
		"aud": "https://api.anthropic.com", "workspace_id": "ws1", "task_id": "t1",
		"run_id": "dev-task-t1-build-1",
		"iat":    float64(now.Unix()), "exp": float64(now.Add(90 * time.Minute).Unix()),
	}
	for k, v := range want {
		if claims[k] != v {
			t.Errorf("claim %s = %v, want %v", k, claims[k], v)
		}
	}
	if _, ok := claims["jti"]; ok {
		t.Error("token carries a jti")
	}
	if _, ok := claims["chat_id"]; ok {
		t.Error("an empty chat id must not become a claim")
	}
}

func TestMint_TTLCappedAndChatSubject(t *testing.T) {
	iss, now := newTestIssuer(t)
	ctx := context.Background()
	tok, err := iss.Mint(ctx, RunClaims{WorkspaceID: "ws1", ChatID: "chat-1", Audience: "a"})
	if err != nil {
		t.Fatal(err)
	}
	jwks, _ := iss.JWKS(ctx)
	_, claims := verify(t, tok, jwks)
	if claims["sub"] != "octopus:workspace:ws1:chat:chat-1" {
		t.Errorf("sub = %v", claims["sub"])
	}
	if claims["exp"] != float64(now.Add(MaxTokenTTL).Unix()) {
		t.Errorf("exp = %v, want the cap", claims["exp"])
	}
}

func TestReady(t *testing.T) {
	cases := map[string]struct {
		env  map[string]string
		ok   bool
		want string
	}{
		"unset":           {map[string]string{}, false, ""},
		"domain":          {map[string]string{"OCTOPUS_DOMAIN": "o.example.com"}, true, "https://o.example.com"},
		"explicit wins":   {map[string]string{IssuerEnv: "https://id.example.com/", "OCTOPUS_DOMAIN": "o"}, true, "https://id.example.com"},
		"plain http":      {map[string]string{IssuerEnv: "http://id.example.com"}, false, "http://id.example.com"},
		"query in issuer": {map[string]string{IssuerEnv: "https://id.example.com?x=1"}, false, "https://id.example.com?x=1"},
	}
	for name, c := range cases {
		iss := New(&memStore{}, env(c.env))
		if got := iss.URL(); got != c.want {
			t.Errorf("%s: URL = %q, want %q", name, got, c.want)
		}
		if err := iss.Ready(); (err == nil) != c.ok {
			t.Errorf("%s: Ready = %v, want ok=%v", name, err, c.ok)
		}
	}
	if _, err := New(&memStore{}, env(nil)).Mint(context.Background(), RunClaims{WorkspaceID: "w", Audience: "a"}); err == nil {
		t.Error("Mint signed without an issuer URL")
	}
}

func TestDiscovery(t *testing.T) {
	iss, _ := newTestIssuer(t)
	d, err := iss.Discovery()
	if err != nil {
		t.Fatal(err)
	}
	if d.Issuer != "https://thulu.example.com" || d.JWKSURI != "https://thulu.example.com/.well-known/jwks.json" {
		t.Errorf("discovery = %+v", d)
	}
}

func TestRotate(t *testing.T) {
	iss, now := newTestIssuer(t)
	ctx := context.Background()
	mint := func() string {
		t.Helper()
		tok, err := iss.Mint(ctx, RunClaims{WorkspaceID: "w", TaskID: "t", Audience: "a"})
		if err != nil {
			t.Fatal(err)
		}
		jwks, _ := iss.JWKS(ctx)
		h, _ := verify(t, tok, jwks)
		return h["kid"].(string)
	}
	first := mint()
	next, err := iss.Rotate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	jwks, _ := iss.JWKS(ctx)
	if len(jwks.Keys) != 2 {
		t.Fatalf("after rotate the set has %d keys, want 2", len(jwks.Keys))
	}
	if got := mint(); got != first {
		t.Errorf("signed with %s before the successor activated", got)
	}
	*now = now.Add(RotationLead + time.Second)
	iss.loadedAt = time.Time{}
	if got := mint(); got != next.KID {
		t.Errorf("signed with %s after activation, want %s", got, next.KID)
	}
	*now = now.Add(RetiredKeyGrace + time.Second)
	iss.loadedAt = time.Time{}
	jwks, _ = iss.JWKS(ctx)
	if len(jwks.Keys) != 1 || jwks.Keys[0].Kid != next.KID {
		t.Errorf("after the grace period the set is %+v, want only %s", jwks.Keys, next.KID)
	}
	infos, err := iss.Keys(ctx)
	if err != nil || len(infos) != 2 || infos[0].State != "signing" || infos[1].State != "retired" {
		t.Errorf("Keys = %+v, %v", infos, err)
	}
}

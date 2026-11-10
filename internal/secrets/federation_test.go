package secrets

import (
	"bytes"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/workloadid"
)

func TestLLMProfile_FederationRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	fed := engine.FederationConfig{
		RuleID: "fdrl_1", OrganizationID: "123e4567-e89b-12d3-a456-426614174000",
		ServiceAccountID: "svac_1", WorkspaceID: "wrkspc_1",
	}
	p, err := s.CreateLLMProfile(ctx, "ws-1", LLMProfileInput{
		Name: "wif", Engine: "claude-code", Provider: "anthropic", Federation: fed, KeyOptional: true, IsDefault: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.HasKey || p.Federation == nil || *p.Federation != fed {
		t.Fatalf("created = %+v", p)
	}
	res, err := s.ResolveLLMForAgent(ctx, "ws-1", "dev", "claude-code")
	if err != nil || res.Federation != fed || res.Credential != "" {
		t.Fatalf("resolved = %+v, %v", res, err)
	}
	p, err = s.UpdateLLMProfile(ctx, "ws-1", p.ID, LLMProfileInput{
		Engine: "claude-code", Provider: "anthropic", RawKey: "sk-ant-api03-abcdefabcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !p.HasKey || p.Federation != nil {
		t.Errorf("after a key the profile = %+v", p)
	}
}

func TestWorkloadIdentityKeys(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	first := workloadid.Key{KID: "k1", Private: []byte{1, 2, 3}, CreatedAt: now, ActivatesAt: now}
	if err := s.AddWorkloadIdentityKey(ctx, first, now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(15 * time.Minute)
	if err := s.AddWorkloadIdentityKey(ctx, workloadid.Key{KID: "k2", Private: []byte{4, 5}, CreatedAt: now, ActivatesAt: later}, later); err != nil {
		t.Fatal(err)
	}
	keys, err := s.WorkloadIdentityKeys(ctx)
	if err != nil || len(keys) != 2 {
		t.Fatalf("keys = %+v, %v", keys, err)
	}
	for _, k := range keys {
		switch k.KID {
		case "k1":
			if !bytes.Equal(k.Private, []byte{1, 2, 3}) || !k.RetiredAt.Equal(later) {
				t.Errorf("k1 = %+v", k)
			}
		case "k2":
			if !bytes.Equal(k.Private, []byte{4, 5}) || !k.RetiredAt.IsZero() || !k.ActivatesAt.Equal(later) {
				t.Errorf("k2 = %+v", k)
			}
		}
	}
	var sealed string
	if err := s.db.QueryRowContext(ctx, `SELECT private_key_encrypted FROM workload_identity_keys WHERE kid = 'k1'`).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if sealed == "" || sealed == "AQID" {
		t.Errorf("the private key is stored in the clear: %q", sealed)
	}
}

package chatruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type fakeMinter struct {
	mu      sync.Mutex
	scopes  []auth.RunScope
	revoked []string
	n       int
}

func (f *fakeMinter) MintRunKey(_ context.Context, scope auth.RunScope, _ string, _ time.Duration) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	f.scopes = append(f.scopes, scope)
	return fmt.Sprintf("oct_raw%d", f.n), fmt.Sprintf("key%d", f.n), nil
}

func (f *fakeMinter) RevokeAPIKey(_ context.Context, keyID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, keyID)
	return nil
}

func (f *fakeMinter) RevokeChatRunKeys(_ context.Context, chatID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, "chat:"+chatID)
	return nil
}

type keyRig struct {
	mgr    *Manager
	minter *fakeMinter
	docker *fakeDocker

	mu  sync.Mutex
	env map[string]string
}

func newKeyRig(t *testing.T) *keyRig {
	t.Helper()
	db := testpg.New(t)
	st, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	sett, err := settings.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	r := &keyRig{minter: &fakeMinter{}, docker: newFakeDocker()}
	r.docker.stdout = okStream
	launched := 0
	launch := func(_ context.Context, _ types.AgentType, params types.LaunchParams) (string, error) {
		launched++
		id := fmt.Sprintf("ctr-%d", launched)
		r.mu.Lock()
		r.env = params.Env
		r.mu.Unlock()
		r.docker.setRunning(id, true)
		return id, nil
	}
	r.mgr = newManagerForTest(st, sett, r.docker, ManagerConfig{DataDir: t.TempDir(), Launch: launch})
	r.mgr.SetRunKeys(r.minter)
	return r
}

func TestChatContainerCarriesItsOwnRunKey(t *testing.T) {
	r := newKeyRig(t)
	c := &chat.Chat{ID: "chat-k1", WorkspaceID: "ws-1", AgentType: "manager"}

	if _, err := r.mgr.ExecTurn(context.Background(), c, testAgent(), AcquireOpts{}, "hola"); err != nil {
		t.Fatalf("ExecTurn: %v", err)
	}

	r.mu.Lock()
	got := r.env["OCTOPUS_API_KEY"]
	r.mu.Unlock()
	if got == "" {
		t.Fatal("chat container launched with no OCTOPUS_API_KEY — it cannot fetch its prompts")
	}

	r.minter.mu.Lock()
	defer r.minter.mu.Unlock()
	if len(r.minter.scopes) != 1 {
		t.Fatalf("minted %d keys, want 1", len(r.minter.scopes))
	}
	scope := r.minter.scopes[0]
	if scope.ChatID != "chat-k1" || scope.Agent != "manager" || scope.Stage != string(types.StageChat) {
		t.Errorf("scope = %+v, want the chat, its agent and the chat stage", scope)
	}
}

func TestChatKeyDiesWithItsContainer(t *testing.T) {
	r := newKeyRig(t)
	c := &chat.Chat{ID: "chat-k2", WorkspaceID: "ws-1", AgentType: "manager"}

	if _, err := r.mgr.ExecTurn(context.Background(), c, testAgent(), AcquireOpts{}, "hola"); err != nil {
		t.Fatalf("ExecTurn: %v", err)
	}
	if err := r.mgr.Evict(context.Background(), c.ID); err != nil {
		t.Fatalf("Evict: %v", err)
	}

	r.minter.mu.Lock()
	defer r.minter.mu.Unlock()
	if len(r.minter.revoked) != 1 || r.minter.revoked[0] != "key1" {
		t.Errorf("revoked = %v, want the evicted container's key", r.minter.revoked)
	}
}

func TestChatLLMAuthSeesTheRunKeyAndCanRefuse(t *testing.T) {
	r := newKeyRig(t)
	var seen string
	r.mgr.SetLLMAuth(func(env map[string]string, _, _ string, _ *types.AgentType) error {
		seen = env["OCTOPUS_API_KEY"]
		return errors.New("no LLM credential for manager")
	})
	c := &chat.Chat{ID: "chat-k3", WorkspaceID: "ws-1", AgentType: "manager"}
	if _, err := r.mgr.ExecTurn(context.Background(), c, testAgent(), AcquireOpts{}, "hola"); err == nil {
		t.Fatal("chat ran without an LLM credential")
	}
	if seen == "" {
		t.Error("the credential injector ran before the chat had its run key")
	}
	r.mu.Lock()
	launched := r.env != nil
	r.mu.Unlock()
	if launched {
		t.Error("a container was launched after the credential was refused")
	}
	r.minter.mu.Lock()
	defer r.minter.mu.Unlock()
	if len(r.minter.revoked) == 0 {
		t.Error("the refused chat's key stayed live")
	}
}

func TestTurnAuthViaGatewayInjectsNothing(t *testing.T) {
	r := newKeyRig(t)
	r.mgr.SetLLMAuth(func(env map[string]string, _, _ string, _ *types.AgentType) error {
		env[engine.LLMSourceEnv] = "binding"
		env[engine.LLMGatewayEnv] = "1"
		return nil
	})
	c := &chat.Chat{ID: "chat-k4", WorkspaceID: "ws-1", AgentType: "manager"}
	_, authEnv, _, err := r.mgr.turnAuth(context.Background(), c, testAgent())
	if err != nil {
		t.Fatal(err)
	}
	if len(authEnv) != 0 {
		t.Errorf("turn env = %v, want none", authEnv)
	}
}

func TestChatKeyFollowsTheDelegation(t *testing.T) {
	r := newKeyRig(t)
	until := time.Now().Add(time.Hour)
	c := &chat.Chat{ID: "chat-k5", WorkspaceID: "ws-1", AgentType: "manager",
		DelegatedBy: "ana@test.com", DelegatedUntil: &until}
	ctx := context.Background()

	if _, err := r.mgr.ExecTurn(ctx, c, testAgent(), AcquireOpts{}, "hola"); err != nil {
		t.Fatalf("ExecTurn: %v", err)
	}
	if _, err := r.mgr.ExecTurn(ctx, c, testAgent(), AcquireOpts{}, "otra"); err != nil {
		t.Fatalf("ExecTurn: %v", err)
	}
	c.DelegatedBy, c.DelegatedUntil = "", nil
	if _, err := r.mgr.ExecTurn(ctx, c, testAgent(), AcquireOpts{}, "chau"); err != nil {
		t.Fatalf("ExecTurn: %v", err)
	}

	r.minter.mu.Lock()
	defer r.minter.mu.Unlock()
	if len(r.minter.scopes) != 2 {
		t.Fatalf("minted %d keys, want 2 (delegated, then ordinary)", len(r.minter.scopes))
	}
	if got := r.minter.scopes[0].DelegatedBy; got != "ana@test.com" {
		t.Errorf("first key acts for %q, want the delegator", got)
	}
	if got := r.minter.scopes[1].DelegatedBy; got != "" {
		t.Errorf("key after the delegation ended acts for %q, want nobody", got)
	}
	if len(r.minter.revoked) == 0 || r.minter.revoked[0] != "key1" {
		t.Errorf("revoked = %v, want the delegated key revoked on recreate", r.minter.revoked)
	}
}

func TestChatKeyIgnoresALapsedDelegation(t *testing.T) {
	r := newKeyRig(t)
	past := time.Now().Add(-time.Minute)
	c := &chat.Chat{ID: "chat-k6", WorkspaceID: "ws-1", AgentType: "manager",
		DelegatedBy: "ana@test.com", DelegatedUntil: &past}
	if _, err := r.mgr.ExecTurn(context.Background(), c, testAgent(), AcquireOpts{}, "hola"); err != nil {
		t.Fatalf("ExecTurn: %v", err)
	}
	r.minter.mu.Lock()
	defer r.minter.mu.Unlock()
	if got := r.minter.scopes[0].DelegatedBy; got != "" {
		t.Errorf("lapsed delegation minted a key acting for %q", got)
	}
}

func TestEndDelegationReplacesTheContainer(t *testing.T) {
	r := newKeyRig(t)
	until := time.Now().Add(time.Hour)
	c := &chat.Chat{ID: "chat-k7", WorkspaceID: "ws-1", AgentType: "manager",
		DelegatedBy: "ana@test.com", DelegatedUntil: &until}
	ctx := context.Background()
	if _, err := r.mgr.ExecTurn(ctx, c, testAgent(), AcquireOpts{}, "hola"); err != nil {
		t.Fatalf("ExecTurn: %v", err)
	}
	if err := r.mgr.EndDelegation(ctx, c.ID); err != nil {
		t.Fatalf("EndDelegation: %v", err)
	}
	if rt, err := r.mgr.store.Get(c.ID); err != nil || rt.Status != StatusEvicted {
		t.Errorf("runtime after EndDelegation = %+v (%v), want evicted", rt, err)
	}
	r.minter.mu.Lock()
	defer r.minter.mu.Unlock()
	found := false
	for _, k := range r.minter.revoked {
		found = found || k == "chat:chat-k7"
	}
	if !found {
		t.Errorf("revoked = %v, want every key of the chat", r.minter.revoked)
	}
}

func TestTurnAuthRefreshesTheProvider(t *testing.T) {
	r := newKeyRig(t)
	r.mgr.SetLLMAuth(func(env map[string]string, _, _ string, _ *types.AgentType) error {
		env[engine.LLMSourceEnv] = "binding"
		env["ENGINE"] = "opencode"
		env["LLM_API_KEY"] = "sk-ant-api-x"
		env["OCTOPUS_LLM_PROVIDER"] = "anthropic"
		return nil
	})
	c := &chat.Chat{ID: "chat-k6", WorkspaceID: "ws-1", AgentType: "manager"}
	_, authEnv, _, err := r.mgr.turnAuth(context.Background(), c, testAgent())
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(authEnv, " ")
	if !strings.Contains(got, "OCTOPUS_LLM_PROVIDER=anthropic") || !strings.Contains(got, "LLM_API_KEY=sk-ant-api-x") {
		t.Errorf("turn env = %v", authEnv)
	}
}

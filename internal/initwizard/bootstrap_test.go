package initwizard

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func TestHasAPIKeyIgnoresExpiredAndRunKeys(t *testing.T) {
	handle := testpg.New(t)
	store, err := auth.NewUserStore(handle)
	if err != nil {
		t.Fatalf("user store: %v", err)
	}
	boot := &Bootstrap{store: store, handle: handle}
	ctx := t.Context()

	wsID, _, err := boot.EnsureWorkspace(ctx, "op@example.com", "Operator", "Acme", "acme")
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	has := func() bool {
		t.Helper()
		ok, err := boot.HasAPIKey(ctx, wsID)
		if err != nil {
			t.Fatalf("HasAPIKey: %v", err)
		}
		return ok
	}
	if has() {
		t.Fatal("a fresh workspace reports a key")
	}

	if _, _, err := store.MintRunKey(ctx, auth.RunScope{TaskID: "task-1", Agent: "dev", Stage: "in_progress"}, wsID, time.Hour); err != nil {
		t.Fatalf("run key: %v", err)
	}
	if has() {
		t.Error("a run key counted as the operator's key")
	}

	raw, expires, err := boot.MintOperatorKey(ctx, "operator", wsID, auth.RoleOwner, "full")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if raw == "" || expires == nil || !expires.After(time.Now()) {
		t.Fatalf("mint = (%q, %v), want a key with a future expiry", raw, expires)
	}
	if !has() {
		t.Fatal("a live operator key is not counted")
	}

	if _, err := handle.ExecContext(ctx, db.Rebind(`UPDATE api_keys SET expires_at = ? WHERE workspace_id = ?`),
		time.Now().Add(-time.Hour), wsID); err != nil {
		t.Fatalf("expire keys: %v", err)
	}
	if has() {
		t.Error("an expired operator key still counts as live")
	}
}

func TestDaemonDeployment(t *testing.T) {
	if got := (&Config{Release: "octopus"}).DaemonDeployment(); got != "octopus-octopus" {
		t.Errorf("DaemonDeployment = %q, want octopus-octopus", got)
	}
	long := (&Config{Release: "a-very-long-release-name-that-keeps-going-and-going-past-the-cap"}).DaemonDeployment()
	if len(long) > 63 || long[len(long)-1] == '-' {
		t.Errorf("DaemonDeployment = %q, want at most 63 characters without a trailing dash", long)
	}
}

func TestDeferBootstrapNamesARunnableCommand(t *testing.T) {
	for _, c := range []struct {
		cfg  Config
		want string
	}{
		{Config{Mode: ModeDocker, ComposeFile: standaloneComposeFile}, "exec octopus ./octopus workspace create"},
		{Config{Mode: ModeKubernetes, Namespace: "octopus", Release: "octopus"}, "deploy/octopus-octopus -- /app/octopus workspace create"},
	} {
		cfg := c.cfg
		cfg.WorkspaceName, cfg.WorkspaceSlug, cfg.OperatorEmail = "Acme", "acme", "op@example.com"
		w := &run{out: io.Discard, cfg: &cfg}
		w.deferBootstrap("no database")
		if len(cfg.Deferred) != 1 || !strings.Contains(cfg.Deferred[0].Finish, c.want) {
			t.Errorf("%s: deferred = %+v, want a command containing %q", cfg.Mode, cfg.Deferred, c.want)
		}
	}
}

package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/audit"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/workshop"
)

// A flow board that fails to land after its agents were written still leaves an audit entry: the
// agents as written, the flow as failed, and the error.
func TestIntegration_WorkshopAuditKeepsAgentsWhenTheFlowBoardFails(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	store, err := agentstore.New(srv.stageAdmin.DB())
	if err != nil {
		t.Fatalf("agent store: %v", err)
	}
	srv.SetAgentStore(store)
	token, ws := registerUserInWorkspace(t, h, "flow-audit@example.com", "Flow", "")
	// Fill the workspace's boards so the flow board has no room.
	for i := 0; i <= stagepkg.MaxBoards; i++ {
		info := stagepkg.BoardInfo{ID: fmt.Sprintf("full%02d", i), Label: "Full", WorkspaceID: ws}
		if err := srv.stageAdmin.CreateBoard(t.Context(), info, stagepkg.Defaults()); err != nil {
			if !strings.Contains(err.Error(), "boards") {
				t.Fatalf("board %d: %v", i, err)
			}
			break
		}
	}

	w := postJSONAuth(t, h, "/workshop/apply", token, map[string]any{
		"items": []map[string]string{{"kind": workshop.KindFlow, "name": "thulu"}},
	})
	if w.Code == http.StatusOK {
		t.Fatalf("apply with no room for the board = 200: %s", w.Body.String())
	}
	got, err := srv.auditStore.List(audit.ListOptions{Kind: audit.KindWorkshopApplied})
	if err != nil || len(got) != 1 {
		t.Fatalf("audit entries = %d (%v), want 1", len(got), err)
	}
	items := got[0].Metadata["items"]
	if !strings.Contains(items, "flow/thulu:failed") || !strings.Contains(items, "agent/thulu-developer:install") ||
		got[0].Metadata["error"] == "" || !strings.Contains(got[0].Summary, "failed") {
		t.Errorf("audit entry = %+v", got[0])
	}
}

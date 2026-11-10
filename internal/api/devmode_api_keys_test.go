package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type mintedKey struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	WorkspaceID string `json:"workspace_id"`
	Role        string `json:"role"`
}

func devMintKey(t *testing.T, h http.Handler, path string) (*mintedKey, int, string) {
	t.Helper()
	w := postJSON(t, h, path, map[string]any{
		"name": "qa-bot", "role": "owner", "permissions": []string{"full"}, "expires_in_days": 30,
	})
	if w.Code != http.StatusCreated {
		return nil, w.Code, w.Body.String()
	}
	var k mintedKey
	if err := json.Unmarshal(w.Body.Bytes(), &k); err != nil {
		t.Fatalf("decode minted key: %v", err)
	}
	return &k, w.Code, ""
}

func TestDevMode_KeysGoToTheOnlyWorkspace(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	_, wid := registerUserInWorkspace(t, h, "devkeys@test.com", "DevKeys", "SecurePassword1")

	k, code, body := devMintKey(t, h, "/auth/api-keys")
	if k == nil {
		t.Fatalf("POST /auth/api-keys from the dev session = %d, want 201: %s", code, body)
	}
	if k.WorkspaceID != wid || k.Role != "owner" {
		t.Errorf("minted key = workspace %q role %q, want %q owner", k.WorkspaceID, k.Role, wid)
	}

	if w := getJSON(t, h, "/tasks", k.Key); w.Code != http.StatusOK {
		t.Errorf("GET /tasks with the new key = %d, want 200: %s", w.Code, w.Body.String())
	}

	list := getJSON(t, h, "/auth/api-keys", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), k.ID) {
		t.Fatalf("GET /auth/api-keys = %d, want 200 listing %s: %s", list.Code, k.ID, list.Body.String())
	}

	if w := deleteRequest(t, h, "/auth/api-keys/"+k.ID, ""); w.Code != http.StatusOK {
		t.Fatalf("DELETE /auth/api-keys/%s = %d, want 200: %s", k.ID, w.Code, w.Body.String())
	}
	if w := getJSON(t, h, "/tasks", k.Key); w.Code != http.StatusUnauthorized {
		t.Errorf("GET /tasks with the revoked key = %d, want 401", w.Code)
	}
}

func TestDevMode_KeysNeedAWorkspaceWhenThereAreTwo(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	registerUserInWorkspace(t, h, "devkeys-a@test.com", "DevKeysA", "SecurePassword1")
	_, second := registerUserInWorkspace(t, h, "devkeys-b@test.com", "DevKeysB", "SecurePassword1")

	if _, code, body := devMintKey(t, h, "/auth/api-keys"); code != http.StatusBadRequest ||
		!strings.Contains(body, "?workspace=") {
		t.Fatalf("POST /auth/api-keys with two workspaces = %d (%s), want 400 asking for ?workspace=", code, body)
	}
	if w := getJSON(t, h, "/auth/api-keys", ""); w.Code != http.StatusBadRequest {
		t.Errorf("GET /auth/api-keys with two workspaces = %d, want 400", w.Code)
	}

	k, code, body := devMintKey(t, h, "/auth/api-keys?workspace="+second)
	if k == nil {
		t.Fatalf("POST /auth/api-keys?workspace= = %d, want 201: %s", code, body)
	}
	if k.WorkspaceID != second {
		t.Errorf("minted key in %q, want the named %q", k.WorkspaceID, second)
	}

	if _, code, _ := devMintKey(t, h, "/auth/api-keys?workspace=nope"); code != http.StatusNotFound {
		t.Errorf("POST /auth/api-keys?workspace=<missing> = %d, want 404", code)
	}
}

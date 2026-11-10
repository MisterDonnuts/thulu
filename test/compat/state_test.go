//go:build compat

package compat

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

type state struct {
	WorkspaceID string        `json:"workspace_id"`
	Setting     *settingState `json:"setting,omitempty"`
	Steps       []step        `json:"steps"`
}

type settingState struct {
	Key   string `json:"key"`
	Value int    `json:"value"`
}

type step struct {
	Name           string      `json:"name"`
	BoardID        string      `json:"board_id"`
	BoardLabel     string      `json:"board_label"`
	Stages         []string    `json:"stages"`
	Tasks          []taskState `json:"tasks"`
	ProfileID      string      `json:"profile_id"`
	ProfileName    string      `json:"profile_name"`
	ProfileModel   string      `json:"profile_model"`
	AutomationID   string      `json:"automation_id"`
	AutomationName string      `json:"automation_name"`
	KeyID          string      `json:"key_id"`
	KeyName        string      `json:"key_name"`
	Key            string      `json:"key"`
	AuditChain     bool        `json:"audit_chain"`
}

type taskState struct {
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	Body     string         `json:"body"`
	Comments []commentState `json:"comments"`
}

type commentState struct {
	ID       string `json:"id"`
	Body     string `json:"body"`
	ParentID string `json:"parent_id,omitempty"`
}

func statePath(t *testing.T) string { return env(t, "COMPAT_STATE") }

func loadState(t *testing.T) *state {
	t.Helper()
	b, err := os.ReadFile(statePath(t))
	if errors.Is(err, os.ErrNotExist) {
		return &state{}
	}
	if err != nil {
		t.Fatal(err)
	}
	var s state
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("parse %s: %v", statePath(t), err)
	}
	return &s
}

func saveState(t *testing.T, s *state) {
	t.Helper()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath(t), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

type bootstrap struct {
	Key         string `json:"key"`
	WorkspaceID string `json:"workspace_id"`
}

func loadBootstrap(t *testing.T) bootstrap {
	t.Helper()
	b, err := os.ReadFile(env(t, "COMPAT_API_KEY_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	var bs bootstrap
	if err := json.Unmarshal(b, &bs); err != nil {
		t.Fatalf("parse COMPAT_API_KEY_FILE: %v", err)
	}
	if bs.Key == "" || bs.WorkspaceID == "" {
		t.Fatal("COMPAT_API_KEY_FILE has no key or workspace_id")
	}
	return bs
}

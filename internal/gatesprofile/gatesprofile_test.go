package gatesprofile

import (
	"encoding/json"
	"testing"
)

func TestFile(t *testing.T) {
	if got := File(map[string]string{"app": ""}); got != nil {
		t.Errorf("no profile: got %s, want nil", got)
	}
	if got := File(map[string]string{"app": "{broken"}); got != nil {
		t.Errorf("invalid JSON: got %s, want nil", got)
	}
	got := File(map[string]string{"app": `{"coverage_min":60}`, "api": ""})
	var doc struct {
		Schema   int                        `json:"schema"`
		Projects map[string]json.RawMessage `json:"projects"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", got, err)
	}
	if doc.Schema != 1 || len(doc.Projects) != 1 || string(doc.Projects["app"]) != `{"coverage_min":60}` {
		t.Errorf("file = %s", got)
	}
}

package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/task"
)

func TestReviewLabelRendersInReadersLanguage(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"environment": "prod", "environment_id": "env-1",
		"dashboard": "Medios de pago", "panels": 2, "metrics": 1,
	})
	list := []task.Approval{{
		Key: string(task.FlagInfraImpact), Label: "Dashboard \"Medios de pago\" proposed for prod", Payload: payload,
	}}

	localizeApprovals(i18n.WithLocale(context.Background(), "es"), list)
	if !strings.Contains(list[0].Label, "propuesto para prod") || !strings.Contains(list[0].Label, "Medios de pago") {
		t.Errorf("es label = %q", list[0].Label)
	}
	localizeApprovals(i18n.WithLocale(context.Background(), "en"), list)
	if !strings.Contains(list[0].Label, "proposed for prod") {
		t.Errorf("en label = %q", list[0].Label)
	}
}

func TestReviewLabelMappingProposal(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"environment": "prod", "mappings": 7, "services": 3})
	label, ok := reviewLabel(i18n.WithLocale(context.Background(), "es"), string(task.FlagInfraImpact), payload)
	if !ok || !strings.Contains(label, "Mapeo de observabilidad propuesto para prod") {
		t.Errorf("label = %q, ok = %v", label, ok)
	}
}

func TestReviewLabelLeavesOthersAlone(t *testing.T) {
	ctx := i18n.WithLocale(context.Background(), "es")
	full, _ := json.Marshal(map[string]any{"environment": "prod", "dashboard": "x", "panels": 1, "metrics": 1})
	partial, _ := json.Marshal(map[string]any{"environment": "prod", "dashboard": "x"})
	cases := []struct {
		name    string
		key     string
		payload json.RawMessage
	}{
		{"other key", "qa_signoff", full},
		{"no payload", string(task.FlagInfraImpact), nil},
		{"missing counts", string(task.FlagInfraImpact), partial},
		{"not json", string(task.FlagInfraImpact), json.RawMessage(`nope`)},
	}
	for _, c := range cases {
		if label, ok := reviewLabel(ctx, c.key, c.payload); ok {
			t.Errorf("%s: relabeled to %q", c.name, label)
		}
	}
}

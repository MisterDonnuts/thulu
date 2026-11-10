package api

import (
	"context"
	"encoding/json"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// Mirrors the payloads the dashboard and mapping proposals store on their review approvals; a
// mapping proposal carries no dashboard.
type reviewLabelFields struct {
	Environment string `json:"environment"`
	Dashboard   string `json:"dashboard"`
	Panels      *int   `json:"panels"`
	Metrics     *int   `json:"metrics"`
	Mappings    *int   `json:"mappings"`
	Services    *int   `json:"services"`
}

func reviewLabel(ctx context.Context, key string, payload json.RawMessage) (string, bool) {
	if key != string(task.FlagInfraImpact) || len(payload) == 0 {
		return "", false
	}
	var f reviewLabelFields
	if err := json.Unmarshal(payload, &f); err != nil || f.Environment == "" {
		return "", false
	}
	switch {
	case f.Dashboard != "" && f.Panels != nil && f.Metrics != nil:
		return i18n.TCtx(ctx, "api.dashboards.comments.proposal_flag",
			"name", f.Dashboard, "environment", f.Environment,
			"panels", *f.Panels, "metrics", *f.Metrics), true
	case f.Mappings != nil && f.Services != nil:
		return i18n.TCtx(ctx, "api.environments.comments.mapping_flag",
			"environment", f.Environment, "mappings", *f.Mappings, "services", *f.Services), true
	}
	return "", false
}

func localizeApprovals(ctx context.Context, list []task.Approval) {
	for i := range list {
		if label, ok := reviewLabel(ctx, list[i].Key, list[i].Payload); ok {
			list[i].Label = label
		}
	}
}

func localizePendingApprovals(ctx context.Context, list []task.PendingApproval) {
	for i := range list {
		if label, ok := reviewLabel(ctx, list[i].Key, list[i].Payload); ok {
			list[i].Label = label
		}
	}
}

func localizeFlags(ctx context.Context, list []task.Flag) {
	for i := range list {
		if label, ok := reviewLabel(ctx, string(list[i].Kind), list[i].Payload); ok {
			list[i].Label = label
		}
	}
}

// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package billing_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/ee/billing"
	"github.com/MisterDonnuts/thulu/internal/entitlements"
)

func sign(payload []byte, secret string, ts time.Time) string {
	mac := hmac.New(sha256.New, []byte(secret))
	t := fmt.Sprintf("%d", ts.Unix())
	mac.Write([]byte(t + "." + string(payload)))
	return fmt.Sprintf("t=%s,v1=%s", t, hex.EncodeToString(mac.Sum(nil)))
}

func TestVerifySignature(t *testing.T) {
	const secret = "whsec_test"
	payload := []byte(`{"type":"x"}`)
	now := time.Unix(1_700_000_000, 0)

	good := sign(payload, secret, now)
	if err := billing.VerifySignature(payload, good, secret, now); err != nil {
		t.Errorf("valid signature rejected: %v", err)
	}
	if err := billing.VerifySignature(payload, good, "whsec_other", now); err == nil {
		t.Error("signature with wrong secret should fail")
	}
	if err := billing.VerifySignature([]byte(`{"type":"y"}`), good, secret, now); err == nil {
		t.Error("tampered payload should fail")
	}
	stale := sign(payload, secret, now.Add(-10*time.Minute))
	if err := billing.VerifySignature(payload, stale, secret, now); err == nil {
		t.Error("stale timestamp should fail (replay protection)")
	}
	if err := billing.VerifySignature(payload, good, "", now); err == nil {
		t.Error("missing secret should fail")
	}
}

func TestResolvePlanChange(t *testing.T) {
	cases := []struct {
		name     string
		payload  string
		wantOK   bool
		wantWS   string
		wantPlan entitlements.Plan
	}{
		{"active starter", `{"type":"customer.subscription.updated","data":{"object":{"status":"active","metadata":{"octopus_workspace_id":"ws1","octopus_plan":"starter"}}}}`, true, "ws1", entitlements.PlanStarter},
		{"legacy business → team", `{"type":"customer.subscription.updated","data":{"object":{"status":"active","metadata":{"octopus_workspace_id":"ws1","octopus_plan":"business"}}}}`, true, "ws1", entitlements.PlanTeam},
		{"active no plan → team", `{"type":"customer.subscription.created","data":{"object":{"status":"active","metadata":{"octopus_workspace_id":"ws2"}}}}`, true, "ws2", entitlements.PlanTeam},
		{"canceled → inactive", `{"type":"customer.subscription.updated","data":{"object":{"status":"canceled","metadata":{"octopus_workspace_id":"ws3","octopus_plan":"team"}}}}`, true, "ws3", entitlements.PlanInactive},
		{"past_due keeps the plan while Stripe retries", `{"type":"customer.subscription.updated","data":{"object":{"status":"past_due","metadata":{"octopus_workspace_id":"ws6","octopus_plan":"team"}}}}`, true, "ws6", entitlements.PlanTeam},
		{"unpaid after the retries → inactive", `{"type":"customer.subscription.updated","data":{"object":{"status":"unpaid","metadata":{"octopus_workspace_id":"ws7","octopus_plan":"team"}}}}`, true, "ws7", entitlements.PlanInactive},
		{"incomplete first payment → no plan yet", `{"type":"customer.subscription.created","data":{"object":{"status":"incomplete","metadata":{"octopus_workspace_id":"ws8","octopus_plan":"team"}}}}`, false, "", ""},
		{"incomplete_expired → inactive", `{"type":"customer.subscription.updated","data":{"object":{"status":"incomplete_expired","metadata":{"octopus_workspace_id":"ws9","octopus_plan":"team"}}}}`, true, "ws9", entitlements.PlanInactive},
		{"deleted → inactive", `{"type":"customer.subscription.deleted","data":{"object":{"status":"canceled","metadata":{"octopus_workspace_id":"ws4"}}}}`, true, "ws4", entitlements.PlanInactive},
		{"no workspace metadata → ignored", `{"type":"customer.subscription.updated","data":{"object":{"status":"active","metadata":{}}}}`, false, "", ""},
		{"unrelated event → ignored", `{"type":"invoice.paid","data":{"object":{"metadata":{"octopus_workspace_id":"ws5"}}}}`, false, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pc, ok, err := billing.ResolvePlanChange([]byte(tc.payload))
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && (pc.WorkspaceID != tc.wantWS || pc.Plan != tc.wantPlan) {
				t.Errorf("got %s/%s, want %s/%s", pc.WorkspaceID, pc.Plan, tc.wantWS, tc.wantPlan)
			}
		})
	}
}

func TestResolvePlanChange_AgentsNodesAndCustomer(t *testing.T) {
	payload := `{"type":"customer.subscription.updated","data":{"object":{"status":"active","customer":"cus_42",` +
		`"metadata":{"octopus_workspace_id":"ws1","octopus_plan":"enterprise","octopus_agents":"0","octopus_nodes":"3"}}}}`
	pc, ok, err := billing.ResolvePlanChange([]byte(payload))
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if pc.Plan != entitlements.PlanEnterprise || pc.Nodes != 3 || pc.Agents != 0 || pc.Customer != "cus_42" {
		t.Fatalf("change = %+v", pc)
	}
	bad := `{"type":"customer.subscription.updated","data":{"object":{"status":"active",` +
		`"metadata":{"octopus_workspace_id":"ws1","octopus_plan":"team","octopus_agents":"lots"}}}}`
	if pc, _, _ = billing.ResolvePlanChange([]byte(bad)); pc.Agents != 0 {
		t.Fatalf("garbage agent count = %d, want 0 (plan default)", pc.Agents)
	}
}

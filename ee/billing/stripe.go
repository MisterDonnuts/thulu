// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package billing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/entitlements"
)

func init() { entitlements.BillingProvider = Stripe{} }

type Stripe struct{}

func (Stripe) VerifySignature(payload []byte, sigHeader, secret string, now time.Time) error {
	return VerifySignature(payload, sigHeader, secret, now)
}

func (Stripe) ResolvePlanChange(payload []byte) (entitlements.PlanChange, bool, error) {
	return ResolvePlanChange(payload)
}

// Replay protection for captured signed payloads; matches Stripe's 5-minute default.
const signatureTolerance = 5 * time.Minute

func VerifySignature(payload []byte, sigHeader, secret string, now time.Time) error {
	if secret == "" {
		return apperr.Unavailable("billing.errors.webhook_secret_missing")
	}
	tsStr, v1s := parseSigHeader(sigHeader)
	if tsStr == "" || len(v1s) == 0 {
		return apperr.Invalid("billing.errors.malformed_signature_header")
	}
	tsUnix, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return apperr.Invalid("billing.errors.invalid_signature_timestamp")
	}
	ts := time.Unix(tsUnix, 0)
	if now.Sub(ts) > signatureTolerance || ts.Sub(now) > signatureTolerance {
		return apperr.Invalid("billing.errors.signature_timestamp_out_of_tolerance")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(tsStr + "." + string(payload)))
	expected := hex.EncodeToString(mac.Sum(nil))
	// Stripe sends several v1 signatures during secret rotation; accept any.
	for _, v1 := range v1s {
		if hmac.Equal([]byte(expected), []byte(v1)) {
			return nil
		}
	}
	return apperr.Invalid("billing.errors.signature_mismatch")
}

func parseSigHeader(sigHeader string) (ts string, v1s []string) {
	for _, part := range strings.Split(sigHeader, ",") {
		k, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts = val
		case "v1":
			v1s = append(v1s, val)
		}
	}
	return ts, v1s
}

type Event struct {
	Type string `json:"type"`
	Data struct {
		Object Subscription `json:"object"`
	} `json:"data"`
}

type Subscription struct {
	ID       string            `json:"id"`
	Status   string            `json:"status"`
	Metadata map[string]string `json:"metadata"`
}

type PlanChange = entitlements.PlanChange

// Does not verify the signature: call VerifySignature first.
func ResolvePlanChange(payload []byte) (PlanChange, bool, error) {
	var e Event
	if err := json.Unmarshal(payload, &e); err != nil {
		return PlanChange{}, false, apperr.WrapKind(apperr.KindInvalid, err, "billing.errors.parse_event")
	}
	sub := e.Data.Object
	wsID := sub.Metadata["octopus_workspace_id"]
	if wsID == "" {
		return PlanChange{}, false, nil
	}

	switch e.Type {
	case "customer.subscription.deleted":
		return PlanChange{WorkspaceID: wsID, Plan: entitlements.PlanFree}, true, nil
	case "customer.subscription.created", "customer.subscription.updated":
		if isInactiveStatus(sub.Status) {
			return PlanChange{WorkspaceID: wsID, Plan: entitlements.PlanFree}, true, nil
		}
		plan := planFromMetadata(sub.Metadata["octopus_plan"])
		return PlanChange{WorkspaceID: wsID, Plan: plan}, true, nil
	default:
		return PlanChange{}, false, nil
	}
}

func isInactiveStatus(status string) bool {
	switch status {
	case "canceled", "unpaid", "incomplete_expired", "past_due":
		return true
	}
	return false
}

func planFromMetadata(p string) entitlements.Plan {
	switch entitlements.Plan(strings.ToLower(strings.TrimSpace(p))) {
	case entitlements.PlanFree:
		return entitlements.PlanFree
	case entitlements.PlanTeam:
		return entitlements.PlanTeam
	case entitlements.PlanBusiness:
		return entitlements.PlanBusiness
	case entitlements.PlanEnterprise:
		return entitlements.PlanEnterprise
	default:
		return entitlements.PlanTeam
	}
}

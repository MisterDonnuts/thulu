package entitlements

import "time"

type PlanChange struct {
	WorkspaceID string
	Plan        Plan
	// Agents at a time (Starter, Team, shared Enterprise) and dedicated nodes (Enterprise) the
	// subscription pays for; 0 = the plan's default.
	Agents int
	Nodes  int
	// The provider's customer, so the month's extra hours can be charged to it.
	Customer string
}

// Billing reads the payment provider's webhooks. ee/ sets BillingProvider; without it the billing
// webhook answers 503.
type Billing interface {
	VerifySignature(payload []byte, sigHeader, secret string, now time.Time) error
	// Does not verify the signature: call VerifySignature first.
	ResolvePlanChange(payload []byte) (PlanChange, bool, error)
}

var BillingProvider Billing

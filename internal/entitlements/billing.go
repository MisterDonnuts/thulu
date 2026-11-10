package entitlements

import "time"

type PlanChange struct {
	WorkspaceID string
	Plan        Plan
}

// Billing reads the payment provider's webhooks. ee/ sets BillingProvider; without it the billing
// webhook answers 503.
type Billing interface {
	VerifySignature(payload []byte, sigHeader, secret string, now time.Time) error
	// Does not verify the signature: call VerifySignature first.
	ResolvePlanChange(payload []byte) (PlanChange, bool, error)
}

var BillingProvider Billing

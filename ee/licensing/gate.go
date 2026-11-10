// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package licensing

import (
	"time"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

const DefaultGrace = 14 * 24 * time.Hour

type Decision struct {
	State    State
	Claims   *Claims
	Licensed bool
	Reason   string
}

// Never stops the daemon: a missing, tampered or expired license leaves it running as Community.
func Resolve(pubHex, token string, now time.Time, grace time.Duration) Decision {
	if token == "" {
		return Decision{State: StateInvalid, Reason: i18n.Log("licensing.log.community_edition")}
	}
	if pubHex == "" {
		return Decision{State: StateInvalid, Reason: i18n.Log("licensing.log.no_vendor_key")}
	}
	pub, err := PublicKeyFromHex(pubHex)
	if err != nil {
		return Decision{State: StateInvalid, Reason: i18n.Log("licensing.log.vendor_key_invalid", "err", err)}
	}
	claims, state, err := Evaluate(pub, token, now, grace)
	if err != nil {
		return Decision{State: StateInvalid, Reason: i18n.Log("licensing.log.verification_failed", "err", err)}
	}
	switch state {
	case StateValid:
		return Decision{State: state, Claims: claims, Licensed: true, Reason: licensedReason(claims)}
	case StateGrace:
		return Decision{State: state, Claims: claims, Licensed: true,
			Reason: i18n.Log("licensing.log.expired_in_grace", "licensed", licensedReason(claims))}
	case StateExpired:
		return Decision{State: state, Claims: claims, Reason: i18n.Log("licensing.log.expired")}
	default:
		return Decision{State: state, Claims: claims, Reason: i18n.Log("licensing.log.not_yet_valid")}
	}
}

func licensedReason(c *Claims) string {
	if c == nil {
		return i18n.Log("licensing.log.licensed")
	}
	if c.ExpiresAt == 0 {
		return i18n.Log("licensing.log.licensed_perpetual", "org", c.Org, "edition", c.Edition)
	}
	return i18n.Log("licensing.log.licensed_until", "org", c.Org, "edition", c.Edition,
		"expires", time.Unix(c.ExpiresAt, 0).UTC().Format("2006-01-02"))
}

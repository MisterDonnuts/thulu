// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package ee

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/ee/licensing"
	"github.com/MisterDonnuts/thulu/internal/edition"
)

func TestActivate(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	prev := licensing.VendorPublicKeyHex
	licensing.VendorPublicKeyHex = hex.EncodeToString(pub)
	t.Cleanup(func() { licensing.VendorPublicKeyHex = prev; edition.SetLicense(nil) })

	issue := func(features []string, exp time.Time) string {
		tok, err := licensing.Sign(priv, licensing.Claims{Org: "Acme", Edition: "enterprise", Features: features, ExpiresAt: exp.Unix()})
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}

	t.Setenv("OCTOPUS_LICENSE_KEY", issue([]string{"audit_export"}, time.Now().Add(time.Hour)))
	activate()
	if !edition.Has(edition.AuditExport) || edition.Has(edition.Kubernetes) {
		t.Errorf("a license for audit_export must enable exactly that")
	}
	if s := edition.Current(); s.Edition != edition.Enterprise || s.Licensee != "Acme" || s.ExpiresAt.IsZero() {
		t.Errorf("status = %+v", s)
	}

	t.Setenv("OCTOPUS_LICENSE_KEY", issue([]string{"*"}, time.Now().Add(time.Hour)))
	activate()
	if !edition.Has(edition.Kubernetes) || !edition.Has(edition.AuditExport) {
		t.Errorf("* must enable every feature")
	}

	t.Setenv("OCTOPUS_LICENSE_KEY", issue([]string{"*"}, time.Now().Add(-2*licensing.DefaultGrace)))
	if reason := activate(); reason == "" {
		t.Error("an expired license must say why it is ignored")
	}
	if edition.Has(edition.Kubernetes) || edition.Current().Edition != edition.Community {
		t.Errorf("an expired license must fall back to Community, got %+v", edition.Current())
	}
}

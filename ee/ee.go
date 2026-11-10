// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package ee

import (
	"os"
	"slices"
	"strings"
	"time"

	// Each registers itself with the core from init.
	_ "github.com/MisterDonnuts/thulu/ee/auditexport"
	_ "github.com/MisterDonnuts/thulu/ee/billing"
	"github.com/MisterDonnuts/thulu/ee/licensing"
	"github.com/MisterDonnuts/thulu/internal/edition"
)

func init() {
	edition.Activate = activate
}

func activate() string {
	d := licensing.Resolve(licensing.VendorPublicKeyHex, strings.TrimSpace(os.Getenv("OCTOPUS_LICENSE_KEY")),
		time.Now(), licensing.DefaultGrace)
	if d.Licensed {
		edition.SetLicense(licensed{d.Claims})
	} else {
		edition.SetLicense(nil)
	}
	return d.Reason
}

type licensed struct{ c *licensing.Claims }

func (l licensed) Has(f edition.Feature) bool {
	return slices.Contains(l.c.Features, "*") || slices.Contains(l.c.Features, string(f))
}

func (l licensed) Status() edition.Status {
	s := edition.Status{Edition: edition.Enterprise, Licensee: l.c.Org, Features: l.c.Features, Seats: l.c.Seats}
	if s.Features == nil {
		s.Features = []string{}
	}
	if l.c.ExpiresAt != 0 {
		s.ExpiresAt = time.Unix(l.c.ExpiresAt, 0)
	}
	return s
}

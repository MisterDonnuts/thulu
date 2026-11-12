// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package billing

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/entitlements"
)

func init() { entitlements.ExtraHoursReporterFromEnv = StripeMeterFromEnv }

const (
	stripeAPI        = "https://api.stripe.com"
	defaultMeterName = "thulu_extra_hours"
)

// StripeMeter sends extra hours as Stripe Billing meter events. The Stripe price attached to the
// meter charges $0.05 per hour.
type StripeMeter struct {
	SecretKey string
	EventName string
	BaseURL   string
	Client    *http.Client
}

// StripeMeterFromEnv returns nil without OCTOPUS_STRIPE_SECRET_KEY: the daemon then shows extra hours
// and charges nothing.
func StripeMeterFromEnv(getenv func(string) string) entitlements.ExtraHoursReporter {
	key := strings.TrimSpace(getenv("OCTOPUS_STRIPE_SECRET_KEY"))
	if key == "" {
		return nil
	}
	name := strings.TrimSpace(getenv("OCTOPUS_STRIPE_EXTRA_HOURS_METER"))
	if name == "" {
		name = defaultMeterName
	}
	return StripeMeter{SecretKey: key, EventName: name}
}

func (m StripeMeter) ReportExtraHours(ctx context.Context, r entitlements.ExtraHoursReport) error {
	form := url.Values{
		"event_name":                  {m.EventName},
		"identifier":                  {r.Identifier},
		"timestamp":                   {strconv.FormatInt(r.At.Unix(), 10)},
		"payload[stripe_customer_id]": {r.Customer},
		"payload[value]":              {strconv.Itoa(r.Hours)},
	}
	base := m.BaseURL
	if base == "" {
		base = stripeAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/billing/meter_events",
		strings.NewReader(form.Encode()))
	if err != nil {
		return apperr.Wrap(err, "billing.errors.meter_request")
	}
	req.Header.Set("Authorization", "Bearer "+m.SecretKey) //i18n:ignore HTTP auth scheme
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := m.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return apperr.Wrap(err, "billing.errors.meter_send")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return apperr.Wrap(err, "billing.errors.meter_send")
	}
	// Stripe dedupes identifiers for at least 24 hours. A repeat it rejects means that month is
	// already charged; the store's reported_at keeps later hours from sending it again anyway.
	if resp.StatusCode == http.StatusBadRequest && strings.Contains(string(body), "resource_already_exists") {
		return nil
	}
	if resp.StatusCode/100 != 2 {
		return apperr.Unavailable("billing.errors.meter_status", "status", resp.StatusCode, "body", string(body))
	}
	return nil
}

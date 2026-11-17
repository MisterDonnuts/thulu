// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package billing_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/ee/billing"
	"github.com/MisterDonnuts/thulu/internal/entitlements"
)

func TestStripeMeter_SendsOneEventPerClosedMonth(t *testing.T) {
	var got url.Values
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		got, _ = url.ParseQuery(string(b))
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	m := &billing.StripeMeter{SecretKey: "sk_test_x", EventName: "thulu_extra_hours", BaseURL: srv.URL}
	at := time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)
	err := m.ReportExtraHours(context.Background(), entitlements.ExtraHoursReport{
		WorkspaceID: "ws", Customer: "cus_1", Period: "2026-12", Hours: 37, At: at,
		Identifier: "thulu-extra-hours-ws-2026-12",
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/billing/meter_events" || auth != "Bearer sk_test_x" {
		t.Fatalf("path=%q auth=%q", path, auth)
	}
	want := map[string]string{"event_name": "thulu_extra_hours", "identifier": "thulu-extra-hours-ws-2026-12",
		"payload[stripe_customer_id]": "cus_1", "payload[value]": "37", "timestamp": "1798761599"}
	for k, v := range want {
		if got.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, got.Get(k), v)
		}
	}
}

func TestStripeMeter_DuplicateIsDoneAndOtherErrorsRetry(t *testing.T) {
	status, body := http.StatusBadRequest, `{"error":{"code":"resource_already_exists"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	m := &billing.StripeMeter{SecretKey: "sk", EventName: "e", BaseURL: srv.URL}
	rep := entitlements.ExtraHoursReport{Customer: "cus", Hours: 1, At: time.Now(), Identifier: "id"}
	if err := m.ReportExtraHours(context.Background(), rep); err != nil {
		t.Fatalf("a repeated identifier is already billed: %v", err)
	}
	status, body = http.StatusInternalServerError, `oops`
	if err := m.ReportExtraHours(context.Background(), rep); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("a 500 must come back as an error to retry, got %v", err)
	}
}

func TestStripeMeterFromEnv(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	if billing.StripeMeterFromEnv(get) != nil {
		t.Fatal("no secret key should mean no reporter")
	}
	env["OCTOPUS_STRIPE_SECRET_KEY"] = "sk"
	if billing.StripeMeterFromEnv(get) == nil {
		t.Fatal("secret key set but no reporter")
	}
}

// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package auditexport_test

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	_ "github.com/MisterDonnuts/thulu/ee/auditexport"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/edition"
	"github.com/MisterDonnuts/thulu/internal/edition/editiontest"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func TestAudit_Export_NeedsTheFeature(t *testing.T) {
	store, _ := audit.NewStore(testpg.New(t))
	store.Log(audit.Entry{Kind: audit.KindTaskCreated, WorkspaceID: "ws1", Summary: "a"})
	editiontest.Enable(t, edition.Kubernetes)

	var buf bytes.Buffer
	if err := store.Export(audit.ListOptions{WorkspaceID: "ws1"}, "csv", &buf); !errors.Is(err, audit.ErrExportUnavailable) {
		t.Fatalf("Export without audit_export = %v, want ErrExportUnavailable", err)
	}
}

func TestAudit_Export_CSV(t *testing.T) {
	store, _ := audit.NewStore(testpg.New(t))
	editiontest.Enable(t, edition.AuditExport)

	store.Log(audit.Entry{Kind: audit.KindTaskCreated, WorkspaceID: "ws1", ResourceID: "t1", Summary: "made, with \"quote\""})
	store.Log(audit.Entry{Kind: audit.KindProjectCreated, WorkspaceID: "ws1", ResourceID: "p1", Summary: "proj"})
	store.Log(audit.Entry{Kind: audit.KindTaskCreated, WorkspaceID: "ws2", ResourceID: "t2", Summary: "other ws"})

	var buf bytes.Buffer
	if err := store.Export(audit.ListOptions{WorkspaceID: "ws1"}, "csv", &buf); err != nil {
		t.Fatalf("Export csv: %v", err)
	}
	r := csv.NewReader(&buf)
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected header + 2 rows, got %d rows", len(rows))
	}
	if rows[0][1] != "kind" {
		t.Errorf("header[1] = %q, want kind", rows[0][1])
	}
	found := false
	for _, row := range rows[1:] {
		if strings.Contains(row[7], `quote`) {
			found = true
		}
		if row[2] != "ws1" {
			t.Errorf("leaked a non-ws1 row: workspace=%q", row[2])
		}
	}
	if !found {
		t.Error("quoted summary not found in export")
	}
}

func TestAudit_Export_NDJSON(t *testing.T) {
	store, _ := audit.NewStore(testpg.New(t))
	editiontest.Enable(t, edition.AuditExport)
	store.Log(audit.Entry{Kind: audit.KindTaskCreated, WorkspaceID: "ws1", ResourceID: "t1", Summary: "a"})
	store.Log(audit.Entry{Kind: audit.KindTaskDeleted, WorkspaceID: "ws1", ResourceID: "t1", Summary: "b"})

	var buf bytes.Buffer
	if err := store.Export(audit.ListOptions{WorkspaceID: "ws1"}, "json", &buf); err != nil {
		t.Fatalf("Export json: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 ndjson lines, got %d", len(lines))
	}
	var e audit.Entry
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil {
		t.Fatalf("each line must be valid json: %v", err)
	}
}

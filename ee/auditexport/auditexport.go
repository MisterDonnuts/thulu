// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package auditexport

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"iter"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/edition"
)

func init() {
	audit.Exporter = Export
}

func Export(rows iter.Seq2[audit.ExportRow, error], format string, w io.Writer) error {
	if !edition.Has(edition.AuditExport) {
		return audit.ErrExportUnavailable
	}
	if format == "csv" {
		return writeCSV(rows, w)
	}
	return writeNDJSON(rows, w)
}

func writeCSV(rows iter.Seq2[audit.ExportRow, error], w io.Writer) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	header := []string{
		"id", "kind", "workspace_id", "actor_id", "actor_email",
		"resource_id", "resource_type", "summary", "metadata",
		"client_ip", "user_agent", "created_at",
		"actor_type", "actor_label", "prev_hash", "row_hash",
	}
	if err := cw.Write(header); err != nil {
		return apperr.Wrap(err, "audit.errors.write_csv_header")
	}
	for row, err := range rows {
		if err != nil {
			return err
		}
		e := row.Entry
		if err := cw.Write([]string{
			e.ID, string(e.Kind), e.WorkspaceID, e.ActorID, e.ActorEmail,
			e.ResourceID, e.ResourceType, e.Summary, row.Metadata,
			e.ClientIP, e.UserAgent, e.CreatedAt.UTC().Format(time.RFC3339),
			e.ActorType, e.ActorLabel, e.PrevHash, e.RowHash,
		}); err != nil {
			return apperr.Wrap(err, "audit.errors.write_csv_row")
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return apperr.Wrap(err, "audit.errors.flush_csv")
	}
	return nil
}

func writeNDJSON(rows iter.Seq2[audit.ExportRow, error], w io.Writer) error {
	enc := json.NewEncoder(w)
	for row, err := range rows {
		if err != nil {
			return err
		}
		if err := enc.Encode(row.Entry); err != nil {
			return err
		}
	}
	return nil
}

package envmetrics

// Restores preserve IDs and review status verbatim; the create paths mint IDs and force
// proposed/discovered.
// Reuse the create paths' column constants so a new column breaks compilation here.

import (
	"context"
	"encoding/json"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

func (s *Store) ImportMapping(ctx context.Context, m Mapping) error {
	if m.ID == "" || m.EnvironmentID == "" {
		return apperr.Invalid("envmetrics.errors.import_mapping_ids_required")
	}
	src, err := json.Marshal(m.Source)
	if err != nil {
		return apperr.Wrap(err, "envmetrics.errors.marshal_source_for", "id", m.ID)
	}
	_, err = s.db.ExecContext(ctx, s.rb(
		//i18n:ignore SQL statement
		`INSERT INTO environment_metric_mappings (`+mappingCols+`)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT (id) DO UPDATE SET
		    environment_id = excluded.environment_id, service = excluded.service,
		    project_id = excluded.project_id, signal = excluded.signal, source = excluded.source,
		    status = excluded.status, not_applicable = excluded.not_applicable,
		    note = excluded.note, task_id = excluded.task_id, flag_id = excluded.flag_id,
		    created_by = excluded.created_by, updated_at = excluded.updated_at`),
		m.ID, m.EnvironmentID, m.Service, m.ProjectID, m.Signal, string(src),
		m.Status, m.NotApplicable, m.Note, m.TaskID, m.FlagID, m.CreatedBy,
		m.CreatedAt, m.UpdatedAt)
	if err != nil {
		return apperr.Wrap(err, "envmetrics.errors.import_mapping", "id", m.ID)
	}
	return nil
}

func (s *Store) ImportCustomMetric(ctx context.Context, m Metric) error {
	if m.ID == "" || m.EnvironmentID == "" {
		return apperr.Invalid("envmetrics.errors.import_custom_metric_ids_required")
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		//i18n:ignore SQL statement
		`INSERT INTO environment_custom_metrics (`+customMetricCols+`)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT (id) DO UPDATE SET
		    environment_id = excluded.environment_id, service = excluded.service,
		    name = excluded.name, kind = excluded.kind, unit = excluded.unit,
		    display_name = excluded.display_name, description = excluded.description,
		    labels = excluded.labels, label_limit = excluded.label_limit,
		    status = excluded.status, approved_by = excluded.approved_by,
		    first_seen = excluded.first_seen, last_seen = excluded.last_seen,
		    updated_at = excluded.updated_at`),
		m.ID, m.EnvironmentID, m.Service, m.Name, m.Kind, m.Unit,
		m.DisplayName, m.Description, encodeLabelNames(m.Labels), m.LabelLimit, m.Status,
		m.ApprovedBy, m.FirstSeen, m.LastSeen, m.CreatedAt, m.UpdatedAt)
	if err != nil {
		return apperr.Wrap(err, "envmetrics.errors.import_custom_metric", "id", m.ID)
	}
	return nil
}

// Panels are written verbatim, not through fillPanelIDs, so restored panels keep their exported
// ids.
func (s *Store) ImportDashboard(ctx context.Context, d Dashboard) error {
	if d.ID == "" || d.EnvironmentID == "" {
		return apperr.Invalid("envmetrics.errors.import_dashboard_ids_required")
	}
	raw, err := json.Marshal(d.Panels)
	if err != nil {
		return apperr.Wrap(err, "envmetrics.errors.marshal_panels_for", "id", d.ID)
	}
	_, err = s.db.ExecContext(ctx, s.rb(
		//i18n:ignore SQL statement
		`INSERT INTO environment_dashboards (`+dashboardCols+`)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT (id) DO UPDATE SET
		    environment_id = excluded.environment_id, name = excluded.name,
		    description = excluded.description, status = excluded.status,
		    panels = excluded.panels, task_id = excluded.task_id, flag_id = excluded.flag_id,
		    created_by = excluded.created_by, approved_by = excluded.approved_by,
		    updated_at = excluded.updated_at`),
		d.ID, d.EnvironmentID, d.Name, d.Description, d.Status, string(raw),
		d.TaskID, d.FlagID, d.CreatedBy, d.ApprovedBy, d.CreatedAt, d.UpdatedAt)
	if err != nil {
		return apperr.Wrap(err, "envmetrics.errors.import_dashboard", "id", d.ID)
	}
	return nil
}

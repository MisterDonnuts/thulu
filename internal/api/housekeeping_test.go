package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/settings"
)

func TestRestoreCommand(t *testing.T) {
	ok := backupStatusFile{
		Archive: "octopus_20261016_031500.tar.gz", BackupDir: "/opt/app/backups",
		Script: "/opt/app/deploy/backup.sh", Source: "compose",
		RestoreEnv: []string{"OCTOPUS_COMPOSE_FILE=/opt/app/deploy/docker-compose.yml"},
	}
	want := "RESTORE=1 OCTOPUS_COMPOSE_FILE=/opt/app/deploy/docker-compose.yml /opt/app/deploy/backup.sh /opt/app/backups/octopus_20261016_031500.tar.gz"
	if got := restoreCommand(ok); got != want {
		t.Errorf("restoreCommand =\n%s\nwant\n%s", got, want)
	}

	host := ok
	host.Source = "host"
	host.BackupDir = "/home/ops/my backups"
	host.RestoreEnv = []string{"OCTOPUS_DATA_DIR=/var/lib/it's octopus"}
	got := restoreCommand(host)
	for _, part := range []string{`OCTOPUS_DATA_DIR='/var/lib/it'\''s octopus'`, `OCTOPUS_DATABASE_URL="$OCTOPUS_DATABASE_URL"`, `'/home/ops/my backups/octopus_20261016_031500.tar.gz'`} {
		if !strings.Contains(got, part) {
			t.Errorf("restoreCommand = %s\nmissing %s", got, part)
		}
	}

	for name, bad := range map[string]func(*backupStatusFile){
		"archive with a command": func(st *backupStatusFile) { st.Archive = "x; rm -rf /" },
		"a .sql dump":            func(st *backupStatusFile) { st.Archive = "octopus_20261016_031500.sql" },
		"relative script":        func(st *backupStatusFile) { st.Script = "deploy/backup.sh" },
		"relative backup dir":    func(st *backupStatusFile) { st.BackupDir = "backups" },
		"foreign variable":       func(st *backupStatusFile) { st.RestoreEnv = []string{"LD_PRELOAD=/tmp/x.so"} },
	} {
		st := ok
		bad(&st)
		if got := restoreCommand(st); got != "" {
			t.Errorf("%s: restoreCommand = %q, want none", name, got)
		}
	}
}

func TestReadBackupStatus(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backup-status.json")
	if v, err := readBackupStatus(path); err != nil || v.Found {
		t.Fatalf("missing file: %+v %v, want not found", v, err)
	}
	body := `{"archive":"octopus_20261016_031500.tar.gz","backup_dir":"/b","size_bytes":2048,"finished_at":"2026-10-16T03:15:09Z","kept":4,"keep":5,"source":"host","script":"/d/backup.sh","restore_env":["OCTOPUS_DATA_DIR=/data"],"remote":"u1@box:octopus","remote_state":"failed"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := readBackupStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Found || v.SizeBytes != 2048 || v.Kept != 4 || v.Keep != 5 || v.RemoteState != "failed" ||
		v.FinishedAt == nil || !v.FinishedAt.Equal(time.Date(2026, 10, 16, 3, 15, 9, 0, time.UTC)) ||
		!v.NeedsDatabaseURL || v.RestoreCommand == "" {
		t.Errorf("view = %+v", v)
	}
	for _, c := range []struct {
		dsn  bool
		want bool
	}{{false, false}, {true, true}} {
		body := fmt.Sprintf(`{"archive":"octopus_20261016_031500.tar.gz","backup_dir":"/b","finished_at":"2026-10-16T03:15:09Z","source":"compose","script":"/d/backup.sh","restore_env":["OCTOPUS_COMPOSE_FILE=/d/docker-compose.yml"],"database_url":%t}`, c.dsn)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		v, err := readBackupStatus(path)
		if err != nil {
			t.Fatal(err)
		}
		if v.RestoreCommand == "" || v.NeedsDatabaseURL != c.want {
			t.Errorf("compose, database_url=%t: view = %+v, want needs_database_url %t", c.dsn, v, c.want)
		}
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBackupStatus(path); err == nil {
		t.Error("a file that is not JSON read as a status")
	}
}

func TestIntegration_Retention(t *testing.T) {
	srv, h := newIntegrationServer(t)
	tenant := registerUser(t, h, "ret-tenant@test.com", "RT", "SecurePassword1")
	operator := registerCatalogOperator(t, h, "ret-op@test.com", "RO", "SecurePassword1")
	last := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	srv.SetHousekeeping(Housekeeping{Cleanup: func() CleanupStatus {
		return CleanupStatus{LastSweep: last, Every: time.Hour, DiskFreePercent: 37.5, DiskFreeKnown: true}
	}})

	if w := getJSON(t, h, "/infra/retention", tenant); w.Code != http.StatusForbidden {
		t.Fatalf("tenant GET /infra/retention = %d, want 403", w.Code)
	}
	if w := putJSON(t, h, "/settings/"+settings.RetentionRunLogsDaysKey, 3, operator); w.Code != http.StatusNoContent {
		t.Fatalf("PUT retention window = %d: %s", w.Code, w.Body.String())
	}
	w := getJSON(t, h, "/infra/retention", operator)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /infra/retention = %d: %s", w.Code, w.Body.String())
	}
	var v retentionView
	if err := json.NewDecoder(w.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if len(v.Windows) != len(retentionKeys) {
		t.Fatalf("windows = %d, want %d", len(v.Windows), len(retentionKeys))
	}
	byKey := map[string]retentionSetting{}
	for _, rs := range v.Windows {
		byKey[rs.Key] = rs
	}
	if rs := byKey[settings.RetentionRunLogsDaysKey]; rs.Value != 3 || rs.Default != 30 {
		t.Errorf("run logs window = %+v, want 3 with default 30", rs)
	}
	if rs := byKey[settings.AuditRetentionDaysKey]; rs.Value != 0 || rs.Default != 0 {
		t.Errorf("audit window = %+v, want forever", rs)
	}
	if v.DiskFloor.Default != 10 || v.LastSweep == nil || !v.LastSweep.Equal(last) ||
		v.SweepEverySec != 3600 || v.DiskFreePercent == nil || *v.DiskFreePercent != 37.5 {
		t.Errorf("view = %+v", v)
	}
}

func TestIntegration_Backups(t *testing.T) {
	srv, h := newIntegrationServer(t)
	operator := registerCatalogOperator(t, h, "bk-op@test.com", "BO", "SecurePassword1")
	path := filepath.Join(t.TempDir(), "backup-status.json")
	srv.SetHousekeeping(Housekeeping{BackupStatusPath: path})

	w := getJSON(t, h, "/updates/backups", operator)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"found":false`) {
		t.Fatalf("no backup yet: %d %s", w.Code, w.Body.String())
	}
	if err := os.WriteFile(path, []byte(`{"archive":"octopus_20261016_031500.tar.gz","backup_dir":"/b","size_bytes":10,"finished_at":"2026-10-16T03:15:09Z","kept":1,"keep":5,"source":"compose","script":"/d/backup.sh","restore_env":["OCTOPUS_COMPOSE_FILE=/d/docker-compose.yml"],"remote":"","remote_state":"none"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	w = getJSON(t, h, "/updates/backups", operator)
	var v backupView
	if err := json.NewDecoder(w.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if !v.Found || !strings.HasPrefix(v.RestoreCommand, "RESTORE=1 OCTOPUS_COMPOSE_FILE=") {
		t.Errorf("view = %+v", v)
	}
}

func TestIntegration_WebhookDeliveries(t *testing.T) {
	srv, h := newIntegrationServer(t)
	cw := postJSON(t, h, "/webhooks", map[string]any{"url": "https://example.com/hook"})
	if cw.Code != http.StatusCreated {
		t.Fatalf("POST /webhooks = %d: %s", cw.Code, cw.Body.String())
	}
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	id, _ := created["id"].(string)

	refused := i18n.T("en", "webhook.delivery.refused", "host", "example.com")
	db := srv.webhookStore.DB()
	for i, row := range []struct {
		id, err string
		code    any
		ago     time.Duration
	}{{"d-old", "", 200, 2 * time.Hour}, {"d-new", refused, nil, time.Minute}} {
		if _, err := db.ExecContext(t.Context(), `INSERT INTO webhook_deliveries
			(id, endpoint_id, event, payload, status_code, attempt, duration_ms, error, delivered, created_at)
			VALUES ($1, $2, 'task.created', '{"id":"p1"}', $3, 1, 12, $4, $5, $6)`,
			row.id, id, row.code, row.err, 1-i, time.Now().Add(-row.ago).UTC()); err != nil {
			t.Fatal(err)
		}
	}

	r := httptest.NewRequest(http.MethodGet, "/webhooks/"+id+"/deliveries", nil)
	r.AddCookie(&http.Cookie{Name: "octopus_locale", Value: "es"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET deliveries = %d: %s", w.Code, w.Body.String())
	}
	var list []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0]["id"] != "d-new" || list[1]["delivered"] != true {
		t.Fatalf("deliveries = %v", list)
	}
	if want := i18n.T("es", "webhook.delivery.refused", "host", "example.com"); list[0]["error"] != want {
		t.Errorf("error = %v, want %q", list[0]["error"], want)
	}

	if w := postJSON(t, h, "/webhooks/"+id+"/deliveries/nope/retry", nil); w.Code != http.StatusNotFound {
		t.Errorf("retry unknown delivery = %d, want 404", w.Code)
	}
	if w := postJSON(t, h, "/webhooks/"+id+"/deliveries/d-new/retry", nil); w.Code != http.StatusAccepted {
		t.Fatalf("retry = %d: %s", w.Code, w.Body.String())
	}
	var queued int
	if err := db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM webhook_outbox WHERE endpoint_id = $1 AND payload = '{"id":"p1"}'`, id).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Errorf("outbox rows for the retried payload = %d, want 1", queued)
	}
	if w := getJSON(t, h, "/webhooks/nope/deliveries", ""); w.Code != http.StatusNotFound {
		t.Errorf("deliveries of an unknown webhook = %d, want 404", w.Code)
	}
}

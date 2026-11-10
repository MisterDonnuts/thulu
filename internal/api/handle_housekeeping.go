package api

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/kept"
	"github.com/MisterDonnuts/thulu/internal/settings"
)

type Housekeeping struct {
	Cleanup          func() CleanupStatus
	BackupStatusPath string
}

type CleanupStatus struct {
	LastSweep         time.Time
	LastSweepPressure bool
	Every             time.Duration
	DiskFreePercent   float64
	DiskFreeKnown     bool
}

func (s *Server) SetHousekeeping(h Housekeeping) { s.housekeeping = h }

var retentionKeys = []string{
	settings.RetentionRunLogsDaysKey,
	settings.RetentionSessionsDaysKey,
	settings.RetentionWebhookDeliveriesDaysKey,
	settings.RetentionRunKeysDaysKey,
	settings.RetentionSandboxRowsDaysKey,
	settings.RetentionCacheMetricsDaysKey,
	settings.RetentionStuckWorktreeDaysKey,
	settings.AuditRetentionDaysKey,
}

type retentionSetting struct {
	Key     string `json:"key"`
	Value   int    `json:"value"`
	Default int    `json:"default"`
}

type retentionView struct {
	Windows           []retentionSetting `json:"windows"`
	DiskFloor         retentionSetting   `json:"disk_floor"`
	LastSweep         *time.Time         `json:"last_sweep,omitempty"`
	LastSweepPressure bool               `json:"last_sweep_pressure,omitempty"`
	SweepEverySec     int                `json:"sweep_every_sec,omitempty"`
	DiskFreePercent   *float64           `json:"disk_free_percent,omitempty"`
}

func (s *Server) handleRetention(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r, "api.scope.operator_surfaces.host_settings", "api.scope.operator_verbs.read") {
		return
	}
	defaults := settings.DefaultValues()
	read := func(key string) (retentionSetting, error) {
		def := 0
		if v, ok := defaults[key].(int); ok {
			def = v
		}
		out := retentionSetting{Key: key, Value: def, Default: def}
		var n int
		err := s.settingsStore.GetJSON(r.Context(), key, &n)
		switch {
		case errors.Is(err, settings.ErrNotFound):
		case err != nil:
			return out, err
		default:
			out.Value = max(n, 0)
		}
		return out, nil
	}
	v := retentionView{Windows: make([]retentionSetting, 0, len(retentionKeys))}
	for _, key := range retentionKeys {
		rs, err := read(key)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
		v.Windows = append(v.Windows, rs)
	}
	floor, err := read(settings.JanitorDiskFreeMinPercentKey)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	v.DiskFloor = floor
	if s.housekeeping.Cleanup != nil {
		c := s.housekeeping.Cleanup()
		if !c.LastSweep.IsZero() {
			t := c.LastSweep
			v.LastSweep = &t
			v.LastSweepPressure = c.LastSweepPressure
		}
		v.SweepEverySec = int(c.Every / time.Second)
		if c.DiskFreeKnown {
			f := c.DiskFreePercent
			v.DiskFreePercent = &f
		}
	}
	writeJSON(w, http.StatusOK, v)
}

// Mirrors write_status in deploy/backup.sh; change both together.
type backupStatusFile struct {
	Archive     string   `json:"archive"`
	BackupDir   string   `json:"backup_dir"`
	SizeBytes   int64    `json:"size_bytes"`
	FinishedAt  string   `json:"finished_at"`
	Kept        int      `json:"kept"`
	Keep        int      `json:"keep"`
	Source      string   `json:"source"`
	Script      string   `json:"script"`
	RestoreEnv  []string `json:"restore_env"`
	DatabaseURL bool     `json:"database_url"`
	Remote      string   `json:"remote"`
	RemoteState string   `json:"remote_state"`
}

type backupView struct {
	Found            bool       `json:"found"`
	Archive          string     `json:"archive,omitempty"`
	BackupDir        string     `json:"backup_dir,omitempty"`
	SizeBytes        int64      `json:"size_bytes,omitempty"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
	Kept             int        `json:"kept,omitempty"`
	Keep             int        `json:"keep,omitempty"`
	Source           string     `json:"source,omitempty"`
	Remote           string     `json:"remote,omitempty"`
	RemoteState      string     `json:"remote_state,omitempty"`
	RestoreCommand   string     `json:"restore_command,omitempty"`
	NeedsDatabaseURL bool       `json:"needs_database_url,omitempty"`
}

const maxBackupStatus = 64 << 10

func (s *Server) handleBackups(w http.ResponseWriter, r *http.Request) {
	path := s.housekeeping.BackupStatusPath
	if path == "" {
		writeJSON(w, http.StatusOK, backupView{})
		return
	}
	v, err := readBackupStatus(path)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func readBackupStatus(path string) (backupView, error) {
	f, err := os.Open(filepath.Clean(path))
	if errors.Is(err, fs.ErrNotExist) {
		return backupView{}, nil
	}
	if err != nil {
		return backupView{}, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, maxBackupStatus+1))
	if err != nil {
		return backupView{}, err
	}
	if len(body) > maxBackupStatus {
		return backupView{}, apperr.Invalid("api.updates.errors.backup_status_too_large", "path", path)
	}
	var st backupStatusFile
	if err := json.Unmarshal(body, &st); err != nil {
		return backupView{}, apperr.WrapKind(apperr.KindInvalid, err, "api.updates.errors.backup_status_unreadable", "path", path)
	}
	v := backupView{
		Found: true, Archive: st.Archive, BackupDir: st.BackupDir, SizeBytes: st.SizeBytes,
		Kept: st.Kept, Keep: st.Keep, Source: st.Source, Remote: st.Remote, RemoteState: st.RemoteState,
	}
	if t, err := time.Parse(time.RFC3339, st.FinishedAt); err == nil {
		v.FinishedAt = &t
	}
	v.RestoreCommand = restoreCommand(st)
	v.NeedsDatabaseURL = v.RestoreCommand != "" && (st.Source == "host" || st.DatabaseURL)
	return v, nil
}

var restoreEnvRe = regexp.MustCompile(`^OCTOPUS_[A-Z0-9_]+=`)

// The command is pasted into a root shell: every part is validated and quoted, never trusted from
// the file.
func restoreCommand(st backupStatusFile) string {
	if !kept.ValidSnapshot(st.Archive) || !strings.HasSuffix(st.Archive, ".tar.gz") ||
		!filepath.IsAbs(st.BackupDir) || !filepath.IsAbs(st.Script) {
		return ""
	}
	parts := []string{"RESTORE=1"}
	for _, kv := range st.RestoreEnv {
		if !restoreEnvRe.MatchString(kv) {
			return ""
		}
		name, value, _ := strings.Cut(kv, "=")
		parts = append(parts, name+"="+shellWord(value))
	}
	if st.Source == "host" {
		// OCTOPUS_DATABASE_URL holds a password, so backup.sh never writes it; the operator's shell
		// supplies it.
		parts = append(parts, `OCTOPUS_DATABASE_URL="$OCTOPUS_DATABASE_URL"`) //i18n:ignore a shell word
	}
	parts = append(parts, shellWord(st.Script), shellWord(filepath.Join(st.BackupDir, st.Archive)))
	return strings.Join(parts, " ")
}

func shellWord(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./:-@") == "" { //i18n:ignore a character set
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

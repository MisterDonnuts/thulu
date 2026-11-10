package updater

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/fsx"
)

const StateFile = "deploy/.octopus-update.json"

type Ref struct {
	Version string `json:"version,omitempty"`
	Image   string `json:"image,omitempty"`
	// Written by CLIs from before releases carried a manifest; still read from old state files.
	Branch string `json:"branch,omitempty"`
	Commit string `json:"commit,omitempty"`
}

type Result string

const (
	ResultOK         Result = "ok"
	ResultFailed     Result = "failed"
	ResultRolledBack Result = "rolled_back"
)

type KeptVersion struct {
	Version        string       `json:"version"`
	Images         []string     `json:"images,omitempty"`
	Snapshot       string       `json:"snapshot,omitempty"`
	SnapshotMethod BackupMethod `json:"snapshot_method,omitempty"`
	Pinned         bool         `json:"pinned,omitempty"`
}

type State struct {
	UpdatedAt    time.Time    `json:"updated_at"`
	From         Ref          `json:"from"`
	To           Ref          `json:"to"`
	BackupPath   string       `json:"backup_path,omitempty"`
	BackupMethod BackupMethod `json:"backup_method,omitempty"`
	ComposeFile  string       `json:"compose_file,omitempty"`

	Result     Result    `json:"result,omitempty"`
	Mode       string    `json:"mode,omitempty"`
	Operator   string    `json:"operator,omitempty"`
	StartedAt  time.Time `json:"started_at,omitzero"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	Detail     string    `json:"detail,omitempty"`
	RollbackTo string    `json:"rollback_to,omitempty"`

	// Newest first, the running version included; previousVersion relies on this order.
	Kept []KeptVersion `json:"kept,omitempty"`
}

// 0600: it names the operator and the paths of database snapshots.
func SaveState(root string, s *State) error {
	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return apperr.Wrap(err, "updater.errors.encode_state")
	}
	if err := fsx.WriteFile(filepath.Join(root, StateFile), append(body, '\n'), 0o600); err != nil {
		return apperr.Wrap(err, "updater.errors.write_path", "path", StateFile)
	}
	return nil
}

func LoadState(root string) (*State, error) {
	raw, err := os.ReadFile(filepath.Join(root, StateFile)) //nolint:gosec // this install's own state file, under the root the caller resolved
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, apperr.Wrap(err, "updater.errors.read_path", "path", StateFile)
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, apperr.Wrap(err, "updater.errors.parse_path", "path", StateFile)
	}
	return &s, nil
}

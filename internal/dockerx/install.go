package dockerx

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"strings"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// Two installs can share one engine; without this label each counted the other's sandboxes against
// its own cap.
const InstallLabel = "octopus.install"

// Not a setting: settings travel in export bundles, and an import would copy the old install's id.
const installIDFile = "install-id"

var (
	installMu sync.RWMutex
	installID string
)

func LoadInstallID(dataDir string) (string, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return "", apperr.Wrap(err, "dockerx.errors.install_id_dir")
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return "", apperr.Wrap(err, "dockerx.errors.install_id_dir")
	}
	defer func() { logx.Warn(root.Close(), "dockerx.log.close_install_id_dir") }()
	if b, err := root.ReadFile(installIDFile); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", apperr.Wrap(err, "dockerx.errors.install_id_read")
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", apperr.Wrap(err, "dockerx.errors.install_id_generate")
	}
	id := hex.EncodeToString(raw)
	if err := root.WriteFile(installIDFile, []byte(id+"\n"), 0o600); err != nil {
		return "", apperr.Wrap(err, "dockerx.errors.install_id_save")
	}
	return id, nil
}

func SetInstallID(id string) {
	installMu.Lock()
	defer installMu.Unlock()
	installID = id
}

func InstallID() string {
	installMu.RLock()
	defer installMu.RUnlock()
	return installID
}

func StampInstall(labels map[string]string) {
	if id := InstallID(); id != "" {
		labels[InstallLabel] = id
	}
}

// A container with no label is ours: it predates the label, or this process never learned its id.
func ForeignInstall(labels map[string]string) bool {
	id := InstallID()
	other := labels[InstallLabel]
	return id != "" && other != "" && other != id
}

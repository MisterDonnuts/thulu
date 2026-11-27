// Package clicopy finds the copies of the Thulu CLI a machine runs and replaces them with a new one.
// A machine collects several: the installer's (/usr/local/bin/thulu, ~/.local/bin/thulu), an
// `octopus` from before the rename, the one `thulu update` fetches into the install, and the one the
// runner service starts. Each can stay behind on its own.
package clicopy

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/fsx"
	"github.com/MisterDonnuts/thulu/internal/safepath"
)

// Names the CLI goes by on a PATH.
var Names = []string{"thulu", "octopus"}

// ProbeEnv is set on `version` calls made to read another copy's version: a copy that sees it
// prints its version and nothing else, so two copies never probe each other in turn.
const ProbeEnv = "OCTOPUS_VERSION_PROBE"

// Owner of the Thulu CLI's main module. The module is named after the CLI, so a copy built before the
// rename has the owner and "octopus" as its path.
const moduleOwner = "github.com/MisterDonnuts/"

// IsThulu reads the Go build info of path: another tool may be called octopus too.
func IsThulu(path string) bool {
	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		return false
	}
	name, ok := strings.CutPrefix(bi.Main.Path, moduleOwner)
	return ok && slices.Contains(Names, name)
}

const versionTimeout = 5 * time.Second

// Version runs `path version` and returns its first line, or "" when the copy does not answer.
func Version(ctx context.Context, path string) string {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version")
	cmd.Env = append(os.Environ(), ProbeEnv+"=1")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(out.String()), "\n")
	return strings.TrimSpace(line)
}

// Copy is one Thulu binary: the path it was found at and the file that path resolves to.
type Copy struct {
	Path     string
	Resolved string
	Version  string
}

// Resolve follows symlinks to the file a path names.
func Resolve(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return path
}

// Replace puts src at dst as a new file: written next to dst and renamed over it. The rename gives
// dst a new inode, so a process running the old file keeps it, and macOS does not kill the new one
// for a code signature cached on the old inode, as it does after a cp over a signed binary.
func Replace(src, dst string) error {
	resolved := Resolve(src)
	body, err := safepath.ReadFile(filepath.Dir(resolved), filepath.Base(resolved))
	if err != nil {
		return apperr.Wrap(err, "clicopy.errors.read", "path", src)
	}
	if err := fsx.WriteFile(dst, body, 0o755); err != nil {
		return apperr.Wrap(err, "clicopy.errors.write", "path", dst)
	}
	return nil
}

// NoPermission reports a copy the current user cannot replace: its directory needs sudo.
func NoPermission(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EROFS)
}

// ManualCommand is what to run by hand when Replace had no permission: the same copy and rename,
// with sudo.
func ManualCommand(src, dst string) string {
	tmp := quote(dst + ".new")
	script := "cp " + quote(src) + " " + tmp + " && chmod 755 " + tmp + " && mv -f " + tmp + " " + quote(dst) //i18n:ignore a shell command
	return "sudo sh -c " + quote(script)
}

func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

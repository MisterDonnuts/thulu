package updater

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
)

type BackupMethod string

const (
	BackupArchive BackupMethod = "archive"
	//i18n:ignore stored value persisted in the state file; methodLabel is what people read
	BackupContainerDump BackupMethod = "pg_dump (database only)"
	BackupNone          BackupMethod = "none"
)

var methodLabels = map[BackupMethod]string{
	BackupArchive:       "updater.labels.method_archive",
	BackupContainerDump: "updater.labels.method_container_dump",
	BackupNone:          "updater.labels.method_none",
}

func methodLabel(m BackupMethod) string {
	if key, ok := methodLabels[m]; ok {
		return i18n.Text(key)
	}
	return string(m)
}

// There are no down-migrations: this snapshot is the only way back from a failed update, so never
// skip it quietly.
func Backup(ctx context.Context, in *Install, out io.Writer) (path string, method BackupMethod, err error) {
	if in.Mode == initwizard.ModeKubernetes {
		// Deliberate no-op: the chart ships no database, the external DSN's backup is the
		// operator's.
		return "", BackupNone, nil
	}
	dir := filepath.Join(in.Root, "backups")
	if mkErr := os.MkdirAll(dir, 0o750); mkErr != nil {
		return "", BackupNone, apperr.Wrap(mkErr, "updater.errors.create_path", "path", dir)
	}

	if dsn := hostDSN(in); dsn != "" && in.HasBackupScript() && toolPresent("pg_dump") {
		archive, archiveErr := runBackupScript(ctx, in, dsn, dir, out)
		if archiveErr == nil {
			return archive, BackupArchive, nil
		}
		printer{w: out}.say("updater.cli.archive_backup_failed", "err", apperr.Text(archiveErr))
	}
	dump, dumpErr := containerDump(ctx, in, dir, out)
	if dumpErr != nil {
		return "", BackupNone, dumpErr
	}
	return dump, BackupContainerDump, nil
}

func runBackupScript(ctx context.Context, in *Install, dsn, dir string, out io.Writer) (string, error) {
	printer{w: out}.say("updater.cli.running_backup_script")
	cmd := exec.CommandContext(ctx, in.BackupScript(), dir) //nolint:gosec // the bundled script, at a path derived from the repo root
	cmd.Dir = in.Root
	cmd.Env = append(os.Environ(), "OCTOPUS_DATABASE_URL="+dsn)
	// /data is a named volume the host cannot read; without the compose file the archive gets an
	// empty data dir and passes for a full snapshot.
	if in.ComposeFile != "" && os.Getenv("OCTOPUS_DATA_DIR") == "" {
		cmd.Env = append(cmd.Env, "OCTOPUS_COMPOSE_FILE="+in.ComposeFile)
	}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return "", apperr.Wrap(err, "updater.errors.backup_script")
	}
	return newestFile(dir, ".tar.gz")
}

func containerDump(ctx context.Context, in *Install, dir string, out io.Writer) (string, error) {
	service := "pg"
	target := filepath.Join(dir, fmt.Sprintf("octopus_%s.sql", time.Now().Format("20060102_150405")))
	printer{w: out}.say("updater.cli.dumping_database", "service", service)
	// 0600: a dump holds every secret the database does.
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // path built from the repo root and a timestamp
	if err != nil {
		return "", apperr.Wrap(err, "updater.errors.create_path", "path", target)
	}
	defer file.Close()

	//nolint:gosec // fixed argv; ComposeFile is this install's own recorded path
	cmd := exec.CommandContext(ctx, "docker", "compose", "-f", in.ComposeFile, "exec", "-T",
		service, "pg_dump", "--clean", "--if-exists", "--no-owner", "--no-acl", "-U", "octopus", "octopus")
	cmd.Dir = in.Root
	cmd.Stdout = file
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// A half-written dump must not survive: it would be recorded as the snapshot a rollback
		// restores.
		if rmErr := os.Remove(target); rmErr != nil {
			return "", apperr.Wrap(rmErr, "updater.errors.pg_dump_cleanup_failed", "stderr", strings.TrimSpace(stderr.String()))
		}
		return "", apperr.Unavailable("updater.errors.pg_dump_failed", "service", service, "stderr", strings.TrimSpace(stderr.String()))
	}
	return target, nil
}

func hostDSN(in *Install) string {
	env, err := initwizard.LoadEnvFile(in.EnvPath())
	if err != nil {
		return ""
	}
	dsn := env.Get("OCTOPUS_DATABASE_URL")
	if dsn == "" {
		return ""
	}
	if !strings.Contains(dsn, "@pg:5432") {
		return dsn
	}
	if in.PublishesPostgres() {
		return strings.Replace(dsn, "@pg:5432", "@"+initwizard.BundledHostAddr(env), 1)
	}
	return ""
}

func newestFile(dir, suffix string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", apperr.Wrap(err, "updater.errors.read_path", "path", dir)
	}
	var newest string
	var newestTime time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), suffix) {
			continue
		}
		info, statErr := e.Info()
		if statErr != nil {
			continue
		}
		if info.ModTime().After(newestTime) {
			newest, newestTime = filepath.Join(dir, e.Name()), info.ModTime()
		}
	}
	if newest == "" {
		return "", apperr.NotFound("updater.errors.no_file_appeared", "suffix", suffix, "dir", dir)
	}
	return newest, nil
}

func toolPresent(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

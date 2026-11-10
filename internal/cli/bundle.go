//go:build !hostcli

package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/envmetrics"
	"github.com/MisterDonnuts/thulu/internal/exportbundle"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/ingest"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
)

type bundleStores struct {
	s              exportbundle.Stores
	db             *sql.DB
	attachmentsDir string
	close          func()
}

func openBundleStores(dataDir string) (*bundleStores, error) {
	dsn, err := requireDSN()
	if err != nil {
		return nil, err
	}
	if dataDir == "" {
		return nil, apperr.Invalid("cli.errors.data_dir_required")
	}
	handle, err := db.Open(dsn)
	if err != nil {
		return nil, apperr.Wrap(err, "cli.errors.open_database")
	}
	bs, err := wireBundleStores(handle, dataDir)
	if err != nil {
		_ = handle.Close()
		return nil, err
	}
	return bs, nil
}

func wireBundleStores(handle *sql.DB, dataDir string) (*bundleStores, error) {
	fail := func(e error) (*bundleStores, error) { return nil, e }

	agents, err := agentstore.New(handle)
	if err != nil {
		return fail(apperr.Wrap(err, "cli.errors.open_store_agent"))
	}
	settingsStore, err := settings.NewStore(handle)
	if err != nil {
		return fail(apperr.Wrap(err, "cli.errors.open_store_settings"))
	}
	registry, err := mcp.NewRegistry(handle)
	if err != nil {
		return fail(apperr.Wrap(err, "cli.errors.open_store_mcp"))
	}
	boards, err := stage.NewStore(handle)
	if err != nil {
		return fail(apperr.Wrap(err, "cli.errors.open_store_stage"))
	}
	attachmentsDir := filepath.Join(dataDir, "task-attachments")
	tasks, err := task.NewStore(handle, attachmentsDir)
	if err != nil {
		return fail(apperr.Wrap(err, "cli.errors.open_store_task"))
	}
	tasks = tasks.WithBoards(boards)
	projects, err := project.NewStore(handle, filepath.Join(dataDir, "projects"))
	if err != nil {
		return fail(apperr.Wrap(err, "cli.errors.open_store_project"))
	}
	metrics, err := envmetrics.NewStore(handle)
	if err != nil {
		return fail(apperr.Wrap(err, "cli.errors.open_store_envmetrics"))
	}
	ingestStore, err := ingest.NewStore(handle)
	if err != nil {
		return fail(apperr.Wrap(err, "cli.errors.open_store_ingest"))
	}
	creds, err := credentials.NewStore(handle)
	if err != nil {
		return fail(apperr.Wrap(err, "cli.errors.open_store_credentials"))
	}

	return &bundleStores{
		s: exportbundle.Stores{
			Agents: agents, Settings: settingsStore, MCP: registry,
			Boards: boards, Tasks: tasks, Projects: projects,
			Metrics: metrics, Ingest: ingestStore, Creds: creds,
		},
		db:             handle,
		attachmentsDir: attachmentsDir,
		close:          func() { _ = handle.Close() },
	}, nil
}

func ExportBundle(ctx context.Context, outPath, workspace, dataDir string) error {
	bs, err := openBundleStores(dataDir)
	if err != nil {
		return err
	}
	defer bs.close()

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	workspaceID, err := resolveWorkspace(ctx, bs.db, workspace, true)
	if err != nil {
		return err
	}

	bundle, attachments, err := exportbundle.Export(ctx, bs.s, workspaceID)
	if err != nil {
		return err
	}

	out := os.Stdout
	if outPath != "" {
		f, createErr := os.Create(outPath) //nolint:gosec // operator-supplied path, this is a CLI
		if createErr != nil {
			return apperr.Wrap(createErr, "cli.errors.create_file", "path", outPath)
		}
		defer f.Close()
		out = f
	}
	if err := exportbundle.Write(out, bundle, attachments); err != nil {
		return err
	}
	if outPath != "" {
		fmt.Fprintln(os.Stderr, i18n.Text("cli.cli.bundle_written", "path", outPath, "summary", summarize(bundle)))
	}
	return nil
}

// "-" reads the bundle from stdin: ImportInDaemon on the host binary sends it that way.
func ImportBundle(ctx context.Context, inPath, dataDir string, opts ImportOptions) error {
	if inPath == "" {
		return apperr.Invalid("cli.errors.usage_import")
	}
	bs, err := openBundleStores(dataDir)
	if err != nil {
		return err
	}
	defer bs.close()

	in := io.Reader(os.Stdin)
	if inPath != "-" {
		f, openErr := os.Open(filepath.Clean(inPath))
		if openErr != nil {
			return apperr.Wrap(openErr, "cli.errors.open_file", "path", inPath)
		}
		defer f.Close()
		in = f
	}

	bundle, attachments, err := exportbundle.Read(in)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	skipped := 0
	if !opts.WithSettings {
		skipped = len(bundle.Settings)
		bundle.Settings = nil
	}
	target, workspaces, stats, err := importInto(ctx, bs, bundle, attachments, opts.Workspace)
	if err != nil {
		return err
	}
	if opts.JSON {
		raw, jsonErr := json.MarshalIndent(stats, "", "  ")
		if jsonErr != nil {
			return apperr.Wrap(jsonErr, "cli.errors.encode_stats")
		}
		fmt.Println(string(raw))
	} else {
		source := inPath
		if inPath == "-" {
			source = i18n.Text("cli.labels.stdin_bundle")
		}
		printImportSummary(os.Stdout, source, workspaceLabel(workspaces, target), stats, skipped)
	}
	if len(bundle.SecretChecklist) > 0 {
		out := os.Stdout
		if opts.JSON {
			out = os.Stderr
		}
		fmt.Fprintf(out, "\n%s\n", i18n.Text("cli.cli.secrets_to_reenter", "count", len(bundle.SecretChecklist)))
		for _, item := range bundle.SecretChecklist {
			fmt.Fprintln(out, "  - "+secretLine(item))
		}
	}
	return nil
}

// A dev-mode bundle carries the fallback seed board's id; adopt that board into the target first or
// the import collides with a board no workspace owns.
func importInto(
	ctx context.Context, bs *bundleStores, bundle *exportbundle.Bundle, attachments map[string][]byte, named string,
) (string, []workspaceRow, exportbundle.Stats, error) {
	target, workspaces, fallbackInUse, err := importTarget(ctx, bs.db, bundle.WorkspaceID, named)
	if err != nil {
		return "", nil, exportbundle.Stats{}, err
	}
	if target != stage.FallbackWorkspaceID && !fallbackInUse {
		if _, err = bs.s.Boards.AdoptFallbackBoards(ctx, target); err != nil {
			return "", nil, exportbundle.Stats{}, err
		}
	}
	bs.s.WorkspaceLabel = func(id string) string { return workspaceLabel(workspaces, id) }
	bundle.Retarget(target)
	stats, err := exportbundle.Import(ctx, bs.s, bundle, attachments, bs.attachmentsDir)
	return target, workspaces, stats, err
}

func importTarget(ctx context.Context, handle *sql.DB, source, named string) (string, []workspaceRow, bool, error) {
	all, err := listWorkspaces(ctx, handle)
	if err != nil {
		return "", nil, false, err
	}
	holds := false
	if len(all) > 0 {
		if holds, err = fallbackHoldsData(ctx, handle); err != nil {
			return "", nil, false, err
		}
	}
	id, err := pickImportTarget(all, source, strings.TrimSpace(named), holds)
	return id, all, holds, err
}

func pickImportTarget(all []workspaceRow, source, named string, fallbackInUse bool) (string, error) {
	if named != "" {
		return pickWorkspace(all, named, true)
	}
	for _, w := range all {
		if w.id == source {
			return w.id, nil
		}
	}
	if source == stage.FallbackWorkspaceID && (len(all) == 0 || fallbackInUse) {
		return source, nil
	}
	if len(all) == 1 {
		return all[0].id, nil
	}
	return "", apperr.NotFound("cli.errors.import_workspace_missing", "id", source)
}

func workspaceLabel(all []workspaceRow, id string) string {
	for _, w := range all {
		if w.id == id && w.slug != "" {
			return w.slug
		}
	}
	return id
}

func printImportSummary(w io.Writer, path, workspace string, st exportbundle.Stats, skippedSettings int) {
	fmt.Fprintln(w, i18n.Text("cli.cli.import_done", "path", path, "workspace", workspace))
	fmt.Fprintln(w, "  "+i18n.Text("cli.cli.import_counts",
		"tasks", st.Tasks, "boards", st.Boards, "projects", st.Projects, "envs", st.Environments,
		"attachments", st.Attachments, "agents", st.Agents, "skills", st.Skills, "mcps", st.MCPs))
	if skippedSettings > 0 {
		fmt.Fprintln(w, "  "+i18n.Text("cli.cli.import_settings_skipped", "count", skippedSettings))
	} else if st.Settings > 0 {
		fmt.Fprintln(w, "  "+i18n.Text("cli.cli.import_settings_applied", "count", st.Settings))
	}
	if len(st.Warnings) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, i18n.Text("cli.cli.import_warnings"))
		for _, warning := range st.Warnings {
			fmt.Fprintf(w, "  - %s\n", warning)
		}
	}
}

func secretLine(item exportbundle.SecretItem) string {
	parts := []string{"[" + secretKindLabel(item.Kind) + "]"}
	if item.Name != "" {
		parts = append(parts, item.Name)
	}
	if item.Identity != "" {
		parts = append(parts, "("+item.Identity+")")
	}
	if item.Scope != "" {
		parts = append(parts, item.Scope)
	}
	return strings.Join(parts, " ")
}

func secretKindLabel(kind string) string {
	switch kind {
	case "credential":
		return i18n.Text("cli.cli.secret_kind.credential")
	case "ingest_token":
		return i18n.Text("cli.cli.secret_kind.ingest_token")
	case "llm_key":
		return i18n.Text("cli.cli.secret_kind.llm_key")
	case "github_token":
		return i18n.Text("cli.cli.secret_kind.github_token")
	}
	return kind
}

func summarize(b *exportbundle.Bundle) string {
	envs, tasks := 0, len(b.Tasks)
	for i := range b.Projects {
		envs += len(b.Projects[i].Environments)
	}
	return i18n.Text("cli.cli.bundle_summary", "boards", len(b.Boards), "projects", len(b.Projects),
		"envs", envs, "tasks", tasks, "agents", len(b.Agents.Agents))
}

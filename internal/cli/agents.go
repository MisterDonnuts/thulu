package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

func ImportAgents(root, workspace string, asTemplates bool) error {
	store, closeStore, err := openStore(agentstore.New)
	if err != nil {
		return err
	}
	defer closeStore()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	ws, err := catalogWorkspace(ctx, store, workspace)
	if err != nil {
		return err
	}
	stats, err := importSource(ctx, store, ws, root, asTemplates)
	if err != nil {
		return err
	}

	out, err := json.MarshalIndent(stats, "", "  ")
	if err != nil {
		return apperr.Wrap(err, "cli.errors.encode_stats")
	}
	fmt.Println(string(out))
	return nil
}

func importSource(ctx context.Context, store *agentstore.Store, ws, path string, asTemplates bool) (agentstore.ImportStats, error) {
	info, err := os.Stat(path)
	if err != nil {
		return agentstore.ImportStats{}, apperr.Wrap(err, "cli.errors.read_path", "path", path)
	}
	if info.IsDir() {
		stats, importErr := store.ImportCatalogTree(ctx, ws, path, "import", asTemplates)
		if importErr != nil {
			return stats, apperr.Wrap(importErr, "cli.errors.import_path", "path", path)
		}
		return stats, nil
	}
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied path, this is a CLI
	if err != nil {
		return agentstore.ImportStats{}, apperr.Wrap(err, "cli.errors.read_path", "path", path)
	}
	var pack agentstore.Pack
	if parseErr := json.Unmarshal(raw, &pack); parseErr != nil {
		return agentstore.ImportStats{}, apperr.Wrap(parseErr, "cli.errors.parse_pack", "path", path)
	}
	if asTemplates {
		for i := range pack.Agents {
			pack.Agents[i].IsTemplate = true
		}
	}
	stats, err := store.ImportCatalog(ctx, ws, pack, "import")
	if err != nil {
		return stats, apperr.Wrap(err, "cli.errors.import_pack", "path", path)
	}
	return stats, nil
}

func VerifyAgents(root, workspace string) error {
	store, closeStore, err := openStore(agentstore.New)
	if err != nil {
		return err
	}
	defer closeStore()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	ws, err := catalogWorkspace(ctx, store, workspace)
	if err != nil {
		return err
	}
	rep, err := store.VerifyAgainstTree(ctx, ws, root)
	if err != nil {
		return apperr.Wrap(err, "cli.errors.verify_path", "path", root)
	}
	out, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return apperr.Wrap(err, "cli.errors.encode_report")
	}
	fmt.Println(string(out))
	if !rep.OK() {
		return apperr.Conflict("cli.errors.store_does_not_reproduce", "path", root)
	}
	return nil
}

func GCAgentContent(keepPerFile int) error {
	store, closeStore, err := openStore(agentstore.New)
	if err != nil {
		return err
	}
	defer closeStore()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pruned, freed, err := store.VacuumAll(ctx, keepPerFile)
	if err != nil {
		return err
	}
	fmt.Println(i18n.Text("cli.cli.agent_content_vacuumed", "pruned", pruned, "freed", freed))
	return nil
}

func ExportAgents(workspace string) error {
	store, closeStore, err := openStore(agentstore.New)
	if err != nil {
		return err
	}
	defer closeStore()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	ws, err := catalogWorkspace(ctx, store, workspace)
	if err != nil {
		return err
	}
	pack, err := store.ExportCatalog(ctx, ws)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(pack); err != nil {
		return apperr.Wrap(err, "cli.errors.encode_pack")
	}
	return nil
}

func catalogWorkspace(ctx context.Context, store *agentstore.Store, named string) (string, error) {
	return resolveWorkspace(ctx, store.DB(), named, true)
}

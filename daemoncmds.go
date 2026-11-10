//go:build !hostcli

package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/cli"
	"github.com/MisterDonnuts/thulu/internal/daemon"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

func startsDaemon(cmd string) bool { return cmd == "serve" }

func runServe(args []string) error {
	serveCmd := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := serveCmd.String("addr", ":8585", i18n.Text("main.flags.serve_addr"))
	agentsDir := serveCmd.String("agents-dir", "", i18n.Text("main.flags.serve_agents_dir"))
	dataDir := serveCmd.String("data", "", i18n.Text("main.flags.serve_data"))
	if err := serveCmd.Parse(args); err != nil {
		return apperr.Wrap(err, "main.errors.parse_flags", "command", "serve")
	}

	dir := *agentsDir
	data := *dataDir
	if dir == "" {
		exe, err := os.Executable()
		if err != nil {
			return apperr.Wrap(err, "main.errors.resolve_executable")
		}
		exeDir := filepath.Dir(exe)
		if info, statErr := os.Stat(filepath.Join(exeDir, "agents")); statErr == nil && info.IsDir() {
			dir = exeDir
			if data == "" {
				data = filepath.Join(exeDir, "data")
			}
		} else {
			dir = filepath.Dir(exeDir)
		}
	}

	return daemon.Run(daemon.Config{
		ListenAddr: *addr,
		AgentsDir:  dir,
		DataDir:    data,
	})
}

func runExport(args []string) error {
	exportCmd := flag.NewFlagSet("export", flag.ExitOnError)
	out := exportCmd.String("out", "", i18n.Text("main.flags.export_out"))
	workspace := exportCmd.String("workspace", "", i18n.Text("main.flags.export_workspace"))
	data := exportCmd.String("data", os.Getenv("OCTOPUS_DATA_DIR"), i18n.Text("main.flags.export_data"))
	if err := exportCmd.Parse(args); err != nil {
		return apperr.Wrap(err, "main.errors.parse_flags", "command", "export")
	}
	return cli.ExportBundle(context.Background(), *out, *workspace, *data)
}

func runImport(args []string) error {
	importCmd := flag.NewFlagSet("import", flag.ExitOnError)
	data := importCmd.String("data", os.Getenv("OCTOPUS_DATA_DIR"), i18n.Text("main.flags.import_data"))
	workspace := importCmd.String("workspace", "", i18n.Text("main.flags.import_workspace"))
	withSettings := importCmd.Bool("with-settings", false, i18n.Text("main.flags.import_with_settings"))
	asJSON := importCmd.Bool("json", false, i18n.Text("main.flags.import_json"))
	if err := importCmd.Parse(args); err != nil {
		return apperr.Wrap(err, "main.errors.parse_flags", "command", "import")
	}
	return cli.ImportBundle(context.Background(), importCmd.Arg(0), *data, cli.ImportOptions{
		Workspace: *workspace, WithSettings: *withSettings, JSON: *asJSON,
	})
}

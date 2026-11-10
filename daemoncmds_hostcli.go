//go:build hostcli

package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/cli"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

func startsDaemon(string) bool { return false }

func runServe([]string) error {
	fmt.Fprintln(os.Stderr, i18n.Text("main.cli.serve_not_in_host_build"))
	os.Exit(2)
	return nil
}

func runExport(args []string) error {
	exportCmd := flag.NewFlagSet("export", flag.ExitOnError)
	root := exportCmd.String("root", "", i18n.Text("main.flags.root"))
	out := exportCmd.String("out", "", i18n.Text("main.flags.export_out"))
	workspace := exportCmd.String("workspace", "", i18n.Text("main.flags.export_workspace"))
	exportCmd.Usage = func() {
		fmt.Fprint(exportCmd.Output(), i18n.Text("main.cli.usage_export_in_daemon"))
		exportCmd.PrintDefaults()
	}
	if err := exportCmd.Parse(args); err != nil {
		return apperr.Wrap(err, "main.errors.parse_flags", "command", "export")
	}
	return cli.ExportInDaemon(context.Background(), *root, *out, *workspace)
}

func runImport(args []string) error {
	importCmd := flag.NewFlagSet("import", flag.ExitOnError)
	root := importCmd.String("root", "", i18n.Text("main.flags.root"))
	workspace := importCmd.String("workspace", "", i18n.Text("main.flags.import_workspace"))
	withSettings := importCmd.Bool("with-settings", false, i18n.Text("main.flags.import_with_settings"))
	asJSON := importCmd.Bool("json", false, i18n.Text("main.flags.import_json"))
	importCmd.Usage = func() {
		fmt.Fprint(importCmd.Output(), i18n.Text("main.cli.usage_import_in_daemon"))
		importCmd.PrintDefaults()
	}
	if err := importCmd.Parse(args); err != nil {
		return apperr.Wrap(err, "main.errors.parse_flags", "command", "import")
	}
	return cli.ImportInDaemon(context.Background(), *root, importCmd.Arg(0), cli.ImportOptions{
		Workspace: *workspace, WithSettings: *withSettings, JSON: *asJSON,
	})
}

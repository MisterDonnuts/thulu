package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/cli"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/runner/hostrun"
)

// runnerLocal handles the runner commands that only touch this machine: `runner mcp`, `runner
// config` and `runner doctor`. The running runner reads a change on its next job.
func runnerLocal(fs *flag.FlagSet, config *string, args []string) error {
	cmd, rest := args[0], args[1:]
	if cmd == "mcp" {
		// The verb comes before its flags (`runner mcp add NAME -url …`), so only a leading -config
		// is read here.
		for len(rest) > 1 && (rest[0] == "-config" || rest[0] == "--config") {
			*config, rest = rest[1], rest[2:]
		}
		return runnerMCP(fs, *config, rest)
	}
	if err := fs.Parse(rest); err != nil {
		return apperr.Wrap(err, "main.errors.parse_flags", "command", "runner "+cmd) //i18n:ignore the subcommand as typed
	}
	if cmd == "config" {
		return runnerConfig(*config, fs.Args())
	}
	return cli.RunnerDoctor(context.Background(), os.Stdout, *config)
}

// stringMap is a repeatable KEY=VALUE (or KEY:VALUE for headers) flag.
type stringMap struct {
	sep string
	m   map[string]string
}

func (s *stringMap) String() string { return "" }

func (s *stringMap) Set(v string) error {
	k, val, ok := strings.Cut(v, s.sep)
	if !ok || strings.TrimSpace(k) == "" {
		return apperr.Invalid("main.errors.bad_pair", "value", v, "sep", s.sep)
	}
	if s.m == nil {
		s.m = map[string]string{}
	}
	s.m[strings.TrimSpace(k)] = strings.TrimSpace(val)
	return nil
}

// runnerMCP handles `thulu runner mcp import|add|remove|list`.
func runnerMCP(fs *flag.FlagSet, config string, args []string) error {
	if len(args) == 0 {
		return apperr.Invalid("main.errors.usage_runner_mcp")
	}
	switch args[0] {
	case "import":
		return runnerMCPImport(fs, config, args[1:])
	case "add":
		return runnerMCPAdd(fs, config, args[1:])
	case "remove":
		if err := fs.Parse(args[1:]); err != nil {
			return apperr.Wrap(err, "main.errors.parse_flags", "command", "runner mcp remove") //i18n:ignore the subcommand as typed
		}
		if fs.NArg() != 1 {
			return apperr.Invalid("main.errors.usage_runner_mcp")
		}
		ch, err := hostrun.RemoveMCP(config, fs.Arg(0))
		if err != nil {
			return err
		}
		printMCPChange(ch, i18n.Text("main.cli.runner_mcp_removed", "names", strings.Join(ch.Names, ", "), "file", ch.File))
		return nil
	case "list":
		if err := fs.Parse(args[1:]); err != nil {
			return apperr.Wrap(err, "main.errors.parse_flags", "command", "runner mcp list") //i18n:ignore the subcommand as typed
		}
		return runnerMCPList(config)
	}
	return apperr.Invalid("main.errors.usage_runner_mcp")
}

func runnerMCPImport(fs *flag.FlagSet, config string, args []string) error {
	project := fs.String("project", "", i18n.Text("main.flags.runner_mcp_project"))
	from := fs.String("from", "", i18n.Text("main.flags.runner_mcp_from"))
	if err := fs.Parse(args); err != nil {
		return apperr.Wrap(err, "main.errors.parse_flags", "command", "runner mcp import") //i18n:ignore the subcommand as typed
	}
	claudeJSON := *from
	if claudeJSON == "" {
		p, err := hostrun.ClaudeJSONPath()
		if err != nil {
			return err
		}
		claudeJSON = p
	}
	found, err := hostrun.FindLocalMCPs(claudeJSON, *project)
	if err != nil {
		return err
	}
	ch, err := hostrun.ImportMCPs(config, found, fs.Args())
	if err != nil {
		return err
	}
	printMCPChange(ch, i18n.Text("main.cli.runner_mcp_imported", "names", strings.Join(ch.Names, ", "), "file", ch.File))
	return nil
}

// runnerMCPAdd handles `runner mcp add NAME [-url U [-type T] [-header K:V]] [-env K=V] [-- command
// args]`. A connector ("claude.ai Slack") takes no definition: claude.ai holds it.
func runnerMCPAdd(fs *flag.FlagSet, config string, args []string) error {
	url := fs.String("url", "", i18n.Text("main.flags.runner_mcp_url"))
	kind := fs.String("type", "", i18n.Text("main.flags.runner_mcp_type"))
	headers := &stringMap{sep: ":"}
	env := &stringMap{sep: "="}
	fs.Var(headers, "header", i18n.Text("main.flags.runner_mcp_header"))
	fs.Var(env, "env", i18n.Text("main.flags.runner_mcp_env"))
	if len(args) == 0 {
		return apperr.Invalid("main.errors.usage_runner_mcp")
	}
	name := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return apperr.Wrap(err, "main.errors.parse_flags", "command", "runner mcp add") //i18n:ignore the subcommand as typed
	}
	var def []byte
	if *url != "" || fs.NArg() > 0 {
		var command string
		var cargs []string
		if fs.NArg() > 0 {
			command, cargs = fs.Arg(0), fs.Args()[1:]
		}
		d, err := hostrun.LocalMCPDef(command, cargs, *url, *kind, headers.m, env.m)
		if err != nil {
			return err
		}
		def = d
	}
	ch, err := hostrun.AddMCP(config, name, def)
	if err != nil {
		return err
	}
	printMCPChange(ch, i18n.Text("main.cli.runner_mcp_added", "names", strings.Join(ch.Names, ", "), "file", ch.File))
	return nil
}

func runnerMCPList(config string) error {
	cfg, err := hostrun.LoadConfig(config)
	if err != nil {
		return err
	}
	sel, err := cfg.Selection()
	if err != nil {
		return err
	}
	fmt.Println(i18n.Text("main.cli.runner_mcp_list", "mode", cfg.Mode(), "file", orNone(cfg.LocalMCPFile())))
	for _, n := range sel.Names() {
		fmt.Println("  " + n)
	}
	for _, n := range sel.Missing {
		fmt.Println("  " + i18n.Text("main.cli.runner_mcp_missing", "name", n))
	}
	return nil
}

func printMCPChange(ch hostrun.MCPChange, done string) {
	fmt.Println(done)
	if ch.Switched {
		fmt.Println(i18n.Text("main.cli.runner_mcp_switched", "mode", ch.Mode))
	}
	if ch.Mode == hostrun.LocalToolsThulu {
		fmt.Println(i18n.Text("main.cli.runner_mcp_mode_thulu"))
	}
}

// runnerConfig handles `thulu runner config [local-tools <mode> | allowed-tools <rules>]`. The
// machine alone decides these; the daemon only shows them.
func runnerConfig(config string, args []string) error {
	if len(args) == 0 {
		cfg, err := hostrun.LoadConfig(config)
		if err != nil {
			return err
		}
		fmt.Println(i18n.Text("main.cli.runner_config_show", "mode", cfg.Mode(),
			"tools", orNone(strings.Join(cfg.AllowedTools, ", ")), "path", config))
		mem := cfg.MemoryLimits()
		fmt.Println(i18n.Text("main.cli.runner_config_memory", "run", mem.RunMB, "min_free", mem.MinFreeMB,
			"floor", mem.FloorMB))
		return nil
	}
	switch args[0] {
	case "local-tools":
		if len(args) != 2 {
			return apperr.Invalid("main.errors.usage_runner_config")
		}
		if err := hostrun.SetLocalTools(config, args[1]); err != nil {
			return err
		}
		fmt.Println(i18n.Text("main.cli.runner_local_tools_set", "mode", args[1]))
		return nil
	case "allowed-tools":
		var rules []string
		// One rule per argument: a rule such as Bash(git log *) holds spaces of its own.
		for _, a := range args[1:] {
			if r := strings.TrimSpace(a); r != "" {
				rules = append(rules, r)
			}
		}
		if err := hostrun.SetAllowedTools(config, rules); err != nil {
			return err
		}
		if len(rules) == 0 {
			fmt.Println(i18n.Text("main.cli.runner_allowed_tools_cleared"))
		} else {
			fmt.Println(i18n.Text("main.cli.runner_allowed_tools_set", "tools", strings.Join(rules, ", ")))
		}
		return nil
	}
	return apperr.Invalid("main.errors.usage_runner_config")
}

func orNone(s string) string {
	if s == "" {
		return i18n.Text("main.cli.none")
	}
	return s
}

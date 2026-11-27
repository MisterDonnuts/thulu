package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/buildinfo"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/runner/hostrun"
	"github.com/MisterDonnuts/thulu/internal/runner/service"
	"github.com/MisterDonnuts/thulu/internal/updater"
)

var (
	runnerDoctor   = hostrun.Doctor
	readVersions   = updater.ReadVersions
	doctorInstalls = service.Installed
)

var (
	modeFromKeys = map[string]string{
		hostrun.ModeFromConfig:  "cli.runner_doctor.mode_config",
		hostrun.ModeFromLegacy:  "cli.runner_doctor.mode_legacy",
		hostrun.ModeFromDefault: "cli.runner_doctor.mode_default",
	}
	modeMeansKeys = map[string]string{
		hostrun.LocalToolsThulu:    "cli.runner_doctor.mode_means_thulu",
		hostrun.LocalToolsSelected: "cli.runner_doctor.mode_means_selected",
		hostrun.LocalToolsAll:      "cli.runner_doctor.mode_means_all",
	}
	mcpStateKeys = map[string]string{
		hostrun.MCPLoaded:       "cli.runner_doctor.state_loaded",
		hostrun.MCPNotSelected:  "cli.runner_doctor.state_not_selected",
		hostrun.MCPStrict:       "cli.runner_doctor.state_strict",
		hostrun.MCPPolicy:       "cli.runner_doctor.state_policy",
		hostrun.MCPNeedsAuth:    "cli.runner_doctor.state_needs_auth",
		hostrun.MCPFailed:       "cli.runner_doctor.state_failed",
		hostrun.MCPOff:          "cli.runner_doctor.state_off",
		hostrun.MCPUnchecked:    "cli.runner_doctor.state_unchecked",
		hostrun.MCPMissing:      "cli.runner_doctor.state_missing",
		hostrun.MCPNotConnected: "cli.runner_doctor.state_not_connected",
	}
)

// RunnerDoctor prints what a run on this machine gets: the local_tools mode and the MCPs it
// resolves to, the Claude Code policy, the versions of every copy of Thulu, and the service.
func RunnerDoctor(ctx context.Context, out io.Writer, config string) error {
	rep, err := runnerDoctor(ctx, config)
	if err != nil {
		return err
	}
	say := func(key string, args ...any) { fmt.Fprintln(out, i18n.Text(key, args...)) }

	say("cli.runner_doctor.config", "path", rep.Config)
	say(modeFromKeys[rep.ModeFrom], "mode", rep.Mode)
	say(modeMeansKeys[rep.Mode])
	if rep.EnvOverride != "" {
		say("cli.runner_doctor.env_override", "name", hostrun.LocalMCPConfigEnv, "path", rep.EnvOverride)
	}
	if rep.MCPFile != "" {
		say("cli.runner_doctor.mcp_file", "path", rep.MCPFile)
	}

	fmt.Fprintln(out)
	say("cli.runner_doctor.mcps")
	say("cli.runner_doctor.mcp_thulu")
	if rep.ListErr != "" {
		say("cli.runner_doctor.list_failed", "err", rep.ListErr)
	}
	for _, m := range rep.MCPs {
		say("cli.runner_doctor.mcp_row", "name", m.Name, "state", i18n.Text(mcpStateKeys[m.State]))
	}
	if len(rep.MCPs) == 0 && rep.ListErr == "" {
		say("cli.runner_doctor.no_mcps")
	}

	fmt.Fprintln(out)
	say("cli.runner_doctor.policy")
	if rep.Policy.BypassDisabled {
		say("cli.runner_doctor.bypass_off", "source", rep.Policy.BypassSource)
		if len(rep.AllowedTools) == 0 {
			say("cli.runner_doctor.allowed_none")
		} else {
			say("cli.runner_doctor.allowed", "tools", strings.Join(rep.AllowedTools, ", "))
		}
	} else {
		say("cli.runner_doctor.bypass_on")
		if len(rep.AllowedTools) > 0 {
			say("cli.runner_doctor.allowed_unused", "tools", strings.Join(rep.AllowedTools, ", "))
		}
	}
	if len(rep.Policy.Sources) > 0 {
		say("cli.runner_doctor.policy_sources", "paths", strings.Join(rep.Policy.Sources, ", "))
	}
	say("cli.runner_doctor.server_managed")

	fmt.Fprintln(out)
	printMemory(out, rep.Memory)
	printDisk(out, rep)

	fmt.Fprintln(out)
	say("cli.runner_doctor.versions")
	row := func(whatKey, path, version string) {
		say("cli.runner_doctor.version_row", "what", i18n.Text(whatKey), "path", path, "version", unknownIfEmpty(version))
	}
	self, err := os.Executable()
	if err != nil {
		self = ""
	}
	row("cli.runner_doctor.this_cli", self, buildinfo.Version)
	root, rerr := resolveRoot("")
	if rerr != nil {
		root = ""
	}
	v := readVersions(ctx, buildinfo.Version, root, nil)
	for _, c := range v.Copies {
		row("cli.runner_doctor.path_cli", c.Path, c.Version)
	}
	if v.Service != nil {
		row("cli.runner_doctor.service_cli", v.Service.Path, v.Service.Version)
	}
	if v.StackURL != "" {
		row("cli.runner_doctor.stack", v.StackURL, v.Stack)
	}
	for _, line := range v.Drift() {
		fmt.Fprintln(out, "  "+line)
	}

	fmt.Fprintln(out)
	printServiceEnv(ctx, out)
	return nil
}

// printMemory says what the runner does about memory, as runner.json sets it.
func printMemory(out io.Writer, m hostrun.MemoryLimits) {
	say := func(key string, args ...any) { fmt.Fprintln(out, i18n.Text(key, args...)) }
	size := func(mb int64) string {
		if mb <= 0 {
			return i18n.Text("cli.runner_doctor.memory_off")
		}
		return hostrun.SizeText(mb)
	}
	say("cli.runner_doctor.memory")
	say("cli.runner_doctor.memory_run", "limit", size(m.RunMB))
	say("cli.runner_doctor.memory_min_free", "min", size(m.MinFreeMB))
	say("cli.runner_doctor.memory_floor", "floor", size(m.FloorMB))
	ids := make([]string, 0, len(m.Projects))
	for id := range m.Projects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		say("cli.runner_doctor.memory_project", "project", id, "limit", size(int64(m.Projects[id])))
	}
}

// printDisk says how much the runner's work dir holds and when build caches go.
func printDisk(out io.Writer, rep hostrun.DoctorReport) {
	say := func(key string, args ...any) { fmt.Fprintln(out, i18n.Text(key, args...)) }
	fmt.Fprintln(out)
	say("cli.runner_doctor.disk")
	if rep.WorkDirSize != "" {
		say("cli.runner_doctor.disk_work_dir", "path", rep.WorkDir, "size", rep.WorkDirSize)
	}
	if rep.BuildCacheMaxMB > 0 {
		say("cli.runner_doctor.disk_build_cache", "max", hostrun.SizeText(rep.BuildCacheMaxMB))
	} else {
		say("cli.runner_doctor.disk_build_cache_off")
	}
}

func printServiceEnv(ctx context.Context, out io.Writer) {
	say := func(key string, args ...any) { fmt.Fprintln(out, i18n.Text(key, args...)) }
	say("cli.runner_doctor.service")
	def, ok, err := doctorInstalls(ctx)
	switch {
	case err != nil:
		say("cli.runner_doctor.service_unreadable", "err", apperr.Text(err))
		return
	case !ok:
		say("cli.runner_doctor.service_none")
		return
	}
	say("cli.runner_doctor.service_file", "path", def.File, "binary", def.Binary)
	path := def.Env["PATH"]
	say("cli.runner_doctor.service_path", "path", path)
	var names []string
	for k := range def.Env {
		if k != "PATH" {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	if len(names) > 0 {
		// Names only: a value can be a token.
		say("cli.runner_doctor.service_env", "names", strings.Join(names, ", "))
	}
	if found := lookIn(path, "claude"); found != "" {
		say("cli.runner_doctor.claude_found", "path", found)
	} else {
		say("cli.runner_doctor.claude_missing")
	}
}

// lookIn finds name in a PATH that is not this process's.
func lookIn(pathList, name string) string {
	for _, dir := range filepath.SplitList(pathList) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

func unknownIfEmpty(v string) string {
	if v == "" {
		return i18n.Text("cli.runner_doctor.unknown")
	}
	return v
}

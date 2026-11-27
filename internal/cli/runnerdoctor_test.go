package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/clicopy"
	"github.com/MisterDonnuts/thulu/internal/runner/hostrun"
	"github.com/MisterDonnuts/thulu/internal/runner/service"
	"github.com/MisterDonnuts/thulu/internal/updater"
)

func TestMergedSpecKeepsWhatTheInstalledServiceHad(t *testing.T) {
	oldSpec, oldInstalled := specForRunnerService, installedRunnerService
	t.Cleanup(func() { specForRunnerService, installedRunnerService = oldSpec, oldInstalled })
	specForRunnerService = func(config string) (service.Spec, error) {
		return service.Spec{Binary: "/usr/local/bin/thulu", Config: config, Path: "/usr/bin:/bin"}, nil
	}
	installedRunnerService = func(context.Context) (service.Definition, bool, error) {
		return service.Definition{File: "/u/Library/LaunchAgents/x.plist", Binary: "/old/thulu", Env: map[string]string{
			"PATH": "/Users/u/.volta/bin:/usr/bin", "ANTHROPIC_BASE_URL": "https://gw",
		}}, true, nil
	}
	cfg := &hostrun.Config{ServiceEnv: map[string]string{"HTTPS_PROXY": "http://p"}}
	spec, merged, err := mergedSpec(t.Context(), "/u/.octopus/runner.json", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Path != "/Users/u/.volta/bin:/usr/bin:/bin" {
		t.Fatalf("PATH = %s", spec.Path)
	}
	if spec.Env["ANTHROPIC_BASE_URL"] != "https://gw" || spec.Env["HTTPS_PROXY"] != "http://p" {
		t.Fatalf("env = %v", spec.Env)
	}
	if strings.Join(merged.Kept, ",") != "ANTHROPIC_BASE_URL,PATH" {
		t.Fatalf("kept = %v", merged.Kept)
	}
	if spec.Binary != "/usr/local/bin/thulu" {
		t.Fatalf("a reinstall points the service at the CLI on PATH, not the old binary: %s", spec.Binary)
	}
}

func stubDoctor(t *testing.T, rep hostrun.DoctorReport, v updater.Versions, def service.Definition, installed bool) {
	t.Helper()
	oldDoctor, oldVersions, oldInstalls := runnerDoctor, readVersions, doctorInstalls
	t.Cleanup(func() { runnerDoctor, readVersions, doctorInstalls = oldDoctor, oldVersions, oldInstalls })
	runnerDoctor = func(context.Context, string) (hostrun.DoctorReport, error) { return rep, nil }
	readVersions = func(context.Context, string, string, *updater.CLIOps) updater.Versions { return v }
	doctorInstalls = func(context.Context) (service.Definition, bool, error) { return def, installed, nil }
	t.Setenv("OCTOPUS_HOME", t.TempDir())
}

func TestRunnerDoctorPrintsModeMCPsPolicyVersionsAndService(t *testing.T) {
	bin := t.TempDir()
	claude := filepath.Join(bin, "claude")
	if err := os.WriteFile(claude, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rep := hostrun.DoctorReport{
		Config: "/u/.octopus/runner.json", Mode: hostrun.LocalToolsSelected, ModeFrom: hostrun.ModeFromConfig,
		MCPFile: "/u/.octopus/local-mcp.json",
		MCPs: []hostrun.DoctorMCP{
			{Name: "claude.ai Gmail", State: hostrun.MCPNeedsAuth},
			{Name: "fs", State: hostrun.MCPPolicy},
			{Name: "pg", State: hostrun.MCPLoaded},
		},
		Policy: hostrun.Policy{BypassDisabled: true, BypassSource: "/Library/Application Support/ClaudeCode/managed-settings.json",
			Sources: []string{"/Library/Application Support/ClaudeCode/managed-settings.json"}},
	}
	v := updater.Versions{Self: "1.2.0",
		Copies:  []clicopy.Copy{{Path: "/usr/local/bin/thulu", Version: "1.1.0"}},
		Service: &clicopy.Copy{Path: "/usr/local/bin/thulu", Version: "1.1.0"}}
	def := service.Definition{File: "/u/Library/LaunchAgents/dev.thulu.runner.plist", Binary: "/usr/local/bin/thulu",
		Env: map[string]string{"PATH": bin + ":/usr/bin", "ANTHROPIC_BASE_URL": "https://secret-gateway"}}
	stubDoctor(t, rep, v, def, true)

	var out bytes.Buffer
	if err := RunnerDoctor(t.Context(), &out, rep.Config); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"Local tools: selected (local_tools in runner.json)",
		"Thulu's MCPs plus the ones selected below",
		"pg: loaded",
		"fs: denied by a Claude Code policy",
		"claude.ai Gmail: selected, but needs sign-in",
		"Bypass mode turned off by /Library/Application Support/ClaudeCode/managed-settings.json",
		"allowed_tools is empty",
		"CLI on PATH: 1.1.0 (/usr/local/bin/thulu)",
		"runner service: 1.1.0 (/usr/local/bin/thulu)",
		"warning: /usr/local/bin/thulu is 1.1.0, this CLI is 1.2.0",
		"PATH: " + bin + ":/usr/bin",
		"Other variables: ANTHROPIC_BASE_URL",
		"claude on that PATH: " + claude,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("doctor lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "secret-gateway") {
		t.Errorf("doctor printed a variable's value:\n%s", got)
	}
}

func TestRunnerDoctorSaysWhenTheServicePathLacksClaude(t *testing.T) {
	rep := hostrun.DoctorReport{Config: "c", Mode: hostrun.LocalToolsThulu, ModeFrom: hostrun.ModeFromDefault}
	def := service.Definition{File: "f", Binary: "b", Env: map[string]string{"PATH": t.TempDir()}}
	stubDoctor(t, rep, updater.Versions{Self: "1.2.0"}, def, true)
	var out bytes.Buffer
	if err := RunnerDoctor(t.Context(), &out, "c"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Local tools: thulu (default", "claude is not on that PATH", "Bypass mode allowed"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("doctor lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "warning:") {
		t.Errorf("no drift, no warning:\n%s", out.String())
	}
}

func TestWriteDriftWarnsOnlyAboutCopiesOnAnotherVersion(t *testing.T) {
	t.Setenv("OCTOPUS_HOME", t.TempDir())
	read := func(_ context.Context, self, _ string) updater.Versions {
		return updater.Versions{Self: self, Copies: []clicopy.Copy{{Path: "/a/thulu", Version: self}, {Path: "/b/octopus", Version: "1.0.0"}},
			Stack: "1.1.0", StackURL: "http://localhost:8585"}
	}
	var out bytes.Buffer
	writeDrift(&out, "1.2.0", read)
	got := out.String()
	for _, want := range []string{"/b/octopus is 1.0.0, this CLI is 1.2.0", "the stack at http://localhost:8585 runs 1.1.0", "thulu update"} {
		if !strings.Contains(got, want) {
			t.Errorf("drift lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "/a/thulu") {
		t.Errorf("a copy on this version is not drift:\n%s", got)
	}
}

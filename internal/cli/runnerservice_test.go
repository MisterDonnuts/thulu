package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/runner/service"
)

func stubRunnerService(t *testing.T, st service.Status, calls *[]string) {
	t.Helper()
	oldQuery, oldUninstall := queryRunnerService, uninstallRunnerService
	t.Cleanup(func() { queryRunnerService, uninstallRunnerService = oldQuery, oldUninstall })
	queryRunnerService = func(context.Context) (service.Status, error) { return st, nil }
	uninstallRunnerService = func(context.Context) (bool, error) {
		if calls != nil {
			*calls = append(*calls, "service uninstall")
		}
		return st.Installed || st.Loaded, nil
	}
}

func TestRunnerServiceStatusSaysWhyItIsNotRunning(t *testing.T) {
	stubRunnerService(t, service.Status{File: "/u/octopus-runner.service", Installed: true, Loaded: true, LastExit: "1", NoLinger: true}, nil)
	var out bytes.Buffer
	if err := RunnerService(context.Background(), &out, "status", "/u/.octopus/runner.json"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"last run exited with 1", "Log: /u/.octopus/runner.log", "loginctl enable-linger"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status lacks %q:\n%s", want, out.String())
		}
	}
}

func TestRunnerServiceUninstallRemovesTheLog(t *testing.T) {
	stubRunnerService(t, service.Status{File: "/u/octopus-runner.service", Installed: true}, nil)
	dir := t.TempDir()
	for _, name := range []string{"runner.json", "runner.log", "runner.log.1", "runner.log.2", "runner.log.old"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := RunnerService(context.Background(), &out, "uninstall", filepath.Join(dir, "runner.json")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if strings.Join(left, " ") != "runner.json runner.log.old" {
		t.Errorf("left %v, want runner.json and runner.log.old", left)
	}
}

func TestRunnerServiceUninstallWithNothingInstalledSaysSo(t *testing.T) {
	var calls []string
	stubRunnerService(t, service.Status{}, &calls)
	var out bytes.Buffer
	if err := RunnerService(context.Background(), &out, "uninstall", "/u/.octopus/runner.json"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "removed") || !strings.Contains(out.String(), "not installed") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestUninstallStopsTheRunnerServiceFirst(t *testing.T) {
	root := install(t)
	d := fakeHost(root)
	runner := &recordingRunner{}
	stubRunnerService(t, service.Status{File: "/u/Library/LaunchAgents/dev.octopus.runner.plist", Installed: true, Loaded: true}, &runner.calls)
	var out bytes.Buffer
	if err := runUninstall(context.Background(), &out, d, runner, UninstallOptions{Root: root}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Service   /u/Library/LaunchAgents/dev.octopus.runner.plist") {
		t.Errorf("dry run does not list the service:\n%s", out.String())
	}
	if len(runner.calls) != 0 {
		t.Fatalf("a dry run acted: %v", runner.calls)
	}
	if err := runUninstall(context.Background(), &out, d, runner, UninstallOptions{Root: root, Yes: true}); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 2 || runner.calls[0] != "service uninstall" || !strings.Contains(runner.calls[1], "compose") {
		t.Errorf("ran %v, want the service out before compose down", runner.calls)
	}
}

func runnerHome(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".octopus")
	for _, rel := range []string{"runner.json", "runner.log", "runner.log.1", "runner.log.2", "runner.log.bak", "work/repos/p1/HEAD"} {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestUninstallRemovesTheRunnerFilesLikeTheData(t *testing.T) {
	root := install(t)
	dir := runnerHome(t)
	var out bytes.Buffer
	if err := runUninstall(context.Background(), &out, fakeHost(root), &recordingRunner{}, UninstallOptions{Root: root, Yes: true}); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out.String())
	}
	for _, rel := range []string{"runner.log", "runner.log.1", "runner.log.2"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err == nil {
			t.Errorf("%s survived the uninstall", rel)
		}
	}
	for _, rel := range []string{"runner.json", "work", "runner.log.bak"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("%s went without -purge-data", rel)
		}
	}
	if !strings.Contains(sectionAfter(out.String(), "Kept without -purge-data"), filepath.Join(dir, "runner.json")) {
		t.Errorf("the kept runner config is not listed:\n%s", out.String())
	}
	out.Reset()
	if err := runUninstall(context.Background(), &out, fakeHost(root), &recordingRunner{}, UninstallOptions{Root: root, Yes: true, PurgeData: true}); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out.String())
	}
	for _, rel := range []string{"runner.json", "work"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err == nil {
			t.Errorf("%s survived -purge-data", rel)
		}
	}
}

func TestUninstallOfAHomeInstallKeepsTheRunnerData(t *testing.T) {
	root := install(t)
	t.Setenv("HOME", filepath.Dir(root))
	t.Setenv("OCTOPUS_HOME", root)
	t.Setenv("OCTOPUS_RUNNER_CONFIG", filepath.Join(root, "runner.json"))
	for _, rel := range []string{"runner.json", "runner.log", "work/runs/r1/x"} {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := runUninstall(context.Background(), &out, fakeHost(root), &recordingRunner{}, UninstallOptions{Root: root, Yes: true}); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out.String())
	}
	for _, rel := range []string{"runner.json", "work", "deploy/.env"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("%s went without -purge-data:\n%s", rel, out.String())
		}
	}
	if _, err := os.Stat(filepath.Join(root, "runner.log")); err == nil {
		t.Error("the runner log survived the uninstall")
	}
	if n := strings.Count(out.String(), filepath.Join(root, "runner.json")); n != 1 {
		t.Errorf("runner.json listed %d times:\n%s", n, out.String())
	}
}

func sectionAfter(text, marker string) string {
	if i := strings.Index(text, marker); i >= 0 {
		return text[i:]
	}
	return ""
}

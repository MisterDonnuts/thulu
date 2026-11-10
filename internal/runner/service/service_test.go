package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakeManager struct {
	calls []string
	reply map[string]string
	fail  map[string]bool
}

func useFake(t *testing.T, system string) *fakeManager {
	t.Helper()
	f := &fakeManager{reply: map[string]string{}, fail: map[string]bool{}}
	oldGOOS, oldCommand := goos, command
	t.Cleanup(func() { goos, command = oldGOOS, oldCommand })
	goos = system
	command = func(_ context.Context, name string, args ...string) ([]byte, error) {
		call := name + " " + strings.Join(args, " ")
		f.calls = append(f.calls, call)
		key := name + " " + args[0]
		if args[0] == "--user" {
			key = name + " " + args[1]
		}
		if f.fail[key] {
			return nil, errors.New("exit status 1")
		}
		return []byte(f.reply[key]), nil
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	return f
}

func (f *fakeManager) ran(verb string) bool {
	for _, c := range f.calls {
		if strings.Contains(c, verb) {
			return true
		}
	}
	return false
}

func testSpec() Spec {
	return Spec{Binary: "/opt/My Apps/a&b/octopus", Config: "/Users/x/.octopus/runner.json",
		Log: "/Users/x/.octopus/runner.log", Path: "/usr/bin:/bin"}
}

func TestLaunchdPlistEscapesTheSpec(t *testing.T) {
	var buf bytes.Buffer
	data := struct {
		Spec
		Label string
	}{Spec: escaped(testSpec()), Label: Label}
	if err := launchdPlist.Execute(&buf, data); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"<string>/opt/My Apps/a&amp;b/octopus</string>",
		"<string>runner</string>\n    <string>run</string>",
		"<key>PATH</key><string>/usr/bin:/bin</string>",
		"<string>-log</string>\n    <string>/Users/x/.octopus/runner.log</string>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plist lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "StandardOutPath") || strings.Contains(out, "StandardErrorPath") {
		t.Errorf("plist redirects the runner's output:\n%s", out)
	}
	dec := xml.NewDecoder(strings.NewReader(out))
	for {
		_, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("plist is not well-formed XML: %v\n%s", err, out)
		}
	}
}

func TestServiceRestartsOnlyAFailedRunner(t *testing.T) {
	var plist bytes.Buffer
	if err := launchdPlist.Execute(&plist, struct {
		Spec
		Label string
	}{Spec: testSpec(), Label: Label}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plist.String(), "<key>KeepAlive</key>\n  <dict>\n    <key>SuccessfulExit</key><false/>") {
		t.Errorf("plist keeps a stopped runner alive:\n%s", plist.String())
	}
	var unit bytes.Buffer
	if err := systemdUnit.Execute(&unit, testSpec()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unit.String(), "Restart=on-failure\nRestartSec=30") {
		t.Errorf("unit restarts a stopped runner:\n%s", unit.String())
	}
}

func TestSystemdUnitRunsTheRunner(t *testing.T) {
	var buf bytes.Buffer
	s := Spec{Binary: "/usr/local/bin/octopus", Config: "/home/x/.octopus/runner.json", Log: "/home/x/.octopus/runner.log", Path: "/usr/bin"}
	if err := systemdUnit.Execute(&buf, unitEscaped(s)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		`ExecStart="/usr/local/bin/octopus" runner run -config "/home/x/.octopus/runner.json" -log "/home/x/.octopus/runner.log"`,
		"WantedBy=default.target",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("unit lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "StandardOutput") || strings.Contains(out, "StandardError") {
		t.Errorf("unit redirects the runner's output:\n%s", out)
	}
}

func TestSystemdUnitEscapesTheSpec(t *testing.T) {
	var buf bytes.Buffer
	s := Spec{Binary: `/opt/50% "off"/$HOME/octopus`, Config: `/home/x\y/runner.json`, Log: "/home/x/100%/$USER/runner.log", Path: `/a%b:/c"d`}
	if err := systemdUnit.Execute(&buf, unitEscaped(s)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		`ExecStart="/opt/50%% \"off\"/$$HOME/octopus" runner run -config "/home/x\\y/runner.json" -log "/home/x/100%%/$$USER/runner.log"`,
		`Environment="PATH=/a%%b:/c\"d"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("unit lacks %q:\n%s", want, out)
		}
	}
}

func TestSpecForRefusesATemporaryBinary(t *testing.T) {
	if _, err := SpecFor(t.TempDir() + "/runner.json"); err == nil {
		t.Fatal("SpecFor accepted the test binary")
	}
}

func TestStableBinaryKeepsTheNameOnPath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "Cellar", "0.3.2", "octopus")
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o750); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(bin, "octopus")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if got := stableBinary(target); got != link {
		t.Errorf("stableBinary = %s, want the link %s", got, link)
	}
	other := filepath.Join(dir, "elsewhere-octopus")
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := stableBinary(other); got != other {
		t.Errorf("stableBinary = %s for a binary the PATH does not name, want it unchanged", got)
	}
}

func TestLaunchdInstallReplacesALoadedAgent(t *testing.T) {
	f := useFake(t, "darwin")
	f.reply["launchctl print"] = "state = running\nlast exit code = (never exited)\n"
	st, err := Install(context.Background(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	uid := strconv.Itoa(os.Getuid())
	want := []string{
		"launchctl print gui/" + uid + "/" + Label,
		"launchctl bootout gui/" + uid + "/" + Label,
		"launchctl bootstrap gui/" + uid + " " + st.File,
		"launchctl print gui/" + uid + "/" + Label,
	}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(f.calls, "\n"), strings.Join(want, "\n"))
	}
	if st.File != filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", Label+".plist") {
		t.Errorf("plist at %s", st.File)
	}
	if !st.Installed || !st.Loaded || !st.Running || st.LastExit != "" {
		t.Errorf("status %+v", st)
	}
	info, err := os.Stat(st.File)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("plist mode %v", info.Mode().Perm())
	}
	if entries, err := os.ReadDir(filepath.Dir(st.File)); err != nil || len(entries) != 1 {
		t.Errorf("install left more than the plist: %v, %v", entries, err)
	}
}

func TestLaunchdQueryReportsAStoppedAgent(t *testing.T) {
	f := useFake(t, "darwin")
	f.reply["launchctl print"] = "gui/501/dev.octopus.runner = {\n\tstate = not running\n\tlast exit code = 1\n}\n"
	st, err := Query(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Installed || !st.Loaded || st.Running || st.LastExit != "1" {
		t.Errorf("status %+v, want loaded, stopped, last exit 1", st)
	}
}

func TestLaunchdUninstallWithNothingInstalledIsANoOp(t *testing.T) {
	f := useFake(t, "darwin")
	f.fail["launchctl print"] = true
	removed, err := Uninstall(context.Background())
	if err != nil || removed {
		t.Fatalf("Uninstall = %v, %v; want false, nil", removed, err)
	}
	if f.ran("bootout") {
		t.Errorf("booted out an agent launchd does not have: %v", f.calls)
	}
}

func TestLaunchdUninstallStopsAndRemoves(t *testing.T) {
	f := useFake(t, "darwin")
	path, err := plistPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFile(path, []byte("<plist/>")); err != nil {
		t.Fatal(err)
	}
	removed, err := Uninstall(context.Background())
	if err != nil || !removed {
		t.Fatalf("Uninstall = %v, %v", removed, err)
	}
	if !f.ran("launchctl bootout gui/") {
		t.Errorf("did not boot the agent out: %v", f.calls)
	}
	if exists(path) {
		t.Error("plist survived the uninstall")
	}
}

func TestSystemdInstallReloadsEnablesAndRestarts(t *testing.T) {
	f := useFake(t, "linux")
	f.reply["systemctl show"] = "LoadState=loaded\nActiveState=active\nExecMainStatus=0\nExecMainExitTimestampMonotonic=0\n"
	f.reply["loginctl show-user"] = "Linger=no\n"
	st, err := Install(context.Background(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable " + unitName,
		"systemctl --user restart " + unitName,
	}
	if got := strings.Join(f.calls[:3], "\n"); got != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	if st.File != filepath.Join(os.Getenv("HOME"), ".config", "systemd", "user", unitName) {
		t.Errorf("unit at %s", st.File)
	}
	if !st.Installed || !st.Loaded || !st.Running || st.LastExit != "" {
		t.Errorf("status %+v", st)
	}
	if !st.NoLinger {
		t.Error("Linger=no is not reported: the runner stops at logout without the person knowing")
	}
}

func TestSystemdQueryReportsTheLastExit(t *testing.T) {
	f := useFake(t, "linux")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(os.Getenv("HOME"), "xdg"))
	path, err := unitPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFile(path, []byte("[Unit]\n")); err != nil {
		t.Fatal(err)
	}
	f.reply["systemctl show"] = "LoadState=loaded\nActiveState=activating\nExecMainStatus=1\nExecMainExitTimestampMonotonic=123\n"
	f.reply["loginctl show-user"] = "Linger=yes\n"
	st, err := Query(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.File != path || st.Running || st.LastExit != "1" || st.NoLinger {
		t.Errorf("status %+v", st)
	}
}

func TestSystemdUninstallWithNothingInstalledRunsNothing(t *testing.T) {
	f := useFake(t, "linux")
	removed, err := Uninstall(context.Background())
	if err != nil || removed {
		t.Fatalf("Uninstall = %v, %v; want false, nil", removed, err)
	}
	if len(f.calls) != 0 {
		t.Errorf("ran %v with nothing installed", f.calls)
	}
	st, err := Query(context.Background())
	if err != nil || st.Installed || len(f.calls) != 0 {
		t.Errorf("Query = %+v, %v; ran %v", st, err, f.calls)
	}
}

func TestSystemdUninstallStopsRemovesAndReloads(t *testing.T) {
	f := useFake(t, "linux")
	path, err := unitPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFile(path, []byte("[Unit]\n")); err != nil {
		t.Fatal(err)
	}
	removed, err := Uninstall(context.Background())
	if err != nil || !removed {
		t.Fatalf("Uninstall = %v, %v", removed, err)
	}
	want := "systemctl --user disable --now " + unitName + "\nsystemctl --user daemon-reload"
	if got := strings.Join(f.calls, "\n"); got != want {
		t.Errorf("calls:\n%s\nwant:\n%s", got, want)
	}
	if exists(path) {
		t.Error("unit survived the uninstall")
	}
}

func TestInstallWaitsForTheRunnerToStart(t *testing.T) {
	f := useFake(t, "darwin")
	fake := command
	prints := 0
	command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "launchctl" && args[0] == "print" {
			prints++
			if prints >= 3 {
				return []byte("state = running\n"), nil
			}
			return []byte("state = not running\nlast exit code = (never exited)\n"), nil
		}
		return fake(ctx, name, args...)
	}
	st, err := Install(context.Background(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running {
		t.Errorf("status %+v, want running once launchd started it (calls %v)", st, f.calls)
	}
}

func TestInstallReportsARunnerThatNeverStarts(t *testing.T) {
	f := useFake(t, "darwin")
	f.reply["launchctl print"] = "state = not running\nlast exit code = 1\n"
	old := startWait
	startWait = 300 * time.Millisecond
	t.Cleanup(func() { startWait = old })
	st, err := Install(context.Background(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	if st.Running || st.LastExit != "1" {
		t.Errorf("status %+v, want stopped with last exit 1", st)
	}
}
